package advertisements

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

const debounce = 250 * time.Millisecond

// InboundSource supplies the parsed xray view used only to identify
// advertisements that do not reference a current inbound.
type InboundSource interface {
	Snapshot() xrayconfig.Snapshot
	Changes() <-chan struct{}
}

// Watcher keeps an optional Advertised connection settings source current. A
// failed reload retains the last valid immutable View and its load time.
type Watcher struct {
	path     string
	now      func() time.Time
	logger   *slog.Logger
	inbounds InboundSource

	mu        sync.Mutex
	view      View
	loadedAt  time.Time
	loaded    bool
	stale     bool
	sourceErr *SourceError
	lastErr   string
}

// NewWatcher creates a source for path. An empty path is an unconfigured
// source; Start performs no file access and Snapshot reports no error.
func NewWatcher(path string) *Watcher {
	return &Watcher{path: path, now: time.Now, logger: slog.Default()}
}

// WithClock overrides the successful-load clock. Call it before Start.
func (w *Watcher) WithClock(now func() time.Time) *Watcher {
	w.now = now
	return w
}

// WithLogger overrides source logging.
func (w *Watcher) WithLogger(logger *slog.Logger) *Watcher {
	w.logger = logger
	return w
}

// WithInbounds supplies the current parsed xray inbounds for bounded warnings
// about advertisements that select no current tag.
func (w *Watcher) WithInbounds(inbounds InboundSource) *Watcher {
	w.inbounds = inbounds
	return w
}

// Start loads a configured source immediately and then watches its path for
// changes until the context is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	if w.path == "" {
		return
	}
	var inboundChanges <-chan struct{}
	if w.inbounds != nil {
		inboundChanges = w.inbounds.Changes()
	}
	w.reload()

	fileWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		w.logger.Error("watch Advertised connection settings; changes need a Panel restart", "error", err)
		return
	}
	if err := fileWatcher.Add(filepath.Dir(w.path)); err != nil {
		w.logger.Error("watch Advertised connection settings; changes need a Panel restart", "error", err)
		_ = fileWatcher.Close()
		return
	}

	go func() {
		defer func() { _ = fileWatcher.Close() }()
		var timer *time.Timer
		var fired <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		schedule := func() {
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
			} else {
				timer = time.NewTimer(debounce)
			}
			fired = timer.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-fileWatcher.Events:
				if !ok {
					w.logger.Error("Advertised connection settings watch ended; changes need a Panel restart")
					return
				}
				if filepath.Clean(event.Name) == filepath.Clean(w.path) {
					schedule()
				}
			case err, ok := <-fileWatcher.Errors:
				if !ok {
					w.logger.Error("Advertised connection settings watch ended; changes need a Panel restart")
					return
				}
				w.logger.Warn("Advertised connection settings watch error", "error", err)
			case _, ok := <-inboundChanges:
				if !ok {
					inboundChanges = nil
					continue
				}
				w.warnCurrentUnknownInbounds()
			case <-fired:
				timer = nil
				fired = nil
				w.reload()
			}
		}
	}()
}

// Snapshot returns the source's current immutable View and freshness state.
func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()

	snapshot := Snapshot{
		View:       w.view,
		LoadedAt:   w.loadedAt,
		Stale:      w.stale,
		configured: w.path != "",
		available:  w.loaded,
	}
	if w.sourceErr != nil {
		sourceErr := *w.sourceErr
		snapshot.Error = &sourceErr
	}
	return snapshot
}

func (w *Watcher) reload() {
	document, err := os.ReadFile(w.path)
	if err != nil {
		w.recordFailure(ReadFailed, err)
		return
	}
	view, err := Parse(document)
	if err != nil {
		reason := ParseFailed
		if errors.Is(err, ErrUnsupportedVersion) {
			reason = UnsupportedVersion
		}
		w.recordFailure(reason, err)
		return
	}
	loadedAt := w.now()

	w.mu.Lock()
	w.view = view
	w.loadedAt = loadedAt
	w.loaded = true
	w.stale = false
	w.sourceErr = nil
	recovered := w.lastErr != ""
	w.lastErr = ""
	w.mu.Unlock()

	if recovered {
		w.logger.Info("Advertised connection settings recovered")
	}
	w.logger.Info("Advertised connection settings updated", "advertisements", len(view.advertisements))
	w.warnUnknownInbounds(view)
}

func (w *Watcher) warnCurrentUnknownInbounds() {
	w.mu.Lock()
	view := w.view
	loaded := w.loaded
	w.mu.Unlock()
	if loaded {
		w.warnUnknownInbounds(view)
	}
}

func (w *Watcher) warnUnknownInbounds(view View) {
	if w.inbounds == nil {
		return
	}
	inboundSnapshot := w.inbounds.Snapshot()
	if !inboundSnapshot.Available() || inboundSnapshot.Stale {
		return
	}
	known := make(map[string]struct{})
	for _, inbound := range inboundSnapshot.View.Inbounds() {
		known[inbound.Tag] = struct{}{}
	}
	unknown := make(map[string]struct{})
	for _, advertisement := range view.advertisements {
		if advertisement.InboundTag == "" {
			continue
		}
		if _, exists := known[advertisement.InboundTag]; !exists {
			unknown[advertisement.InboundTag] = struct{}{}
		}
	}
	if len(unknown) != 0 {
		w.logger.Warn("Advertised connection settings reference no current xray inbound", "unknown_inbound_tags", len(unknown))
	}
}

func (w *Watcher) recordFailure(reason ErrorReason, diagnostic error) {
	w.mu.Lock()
	w.stale = w.loaded
	sourceErr := safeSourceError(reason, w.stale)
	w.sourceErr = &sourceErr
	failure := string(reason) + ": " + diagnostic.Error()
	changed := failure != w.lastErr
	w.lastErr = failure
	w.mu.Unlock()

	if changed {
		w.logger.Warn("cannot load Advertised connection settings", "reason", reason)
	}
}
