package xrayconfig

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce absorbs the burst of events a single save produces (write +
// chmod, or temp-file rename) before re-reading the file.
const debounce = 250 * time.Millisecond

// Watcher keeps the Roster and parsed Connection profile view current. It
// parses the xray config at startup and after each fsnotify change. A failed
// reload keeps both last-valid results, so a half-saved config cannot empty
// the Users table or mark every User gone. Roster version 0 and an unavailable
// Snapshot mean the config has never parsed successfully.
//
// Watcher follows the path rather than the inode. It watches the parent
// directory so it also detects atomic file replacement.
type Watcher struct {
	path string
	now  func() time.Time

	mu        sync.Mutex
	roster    map[string]User
	version   uint64
	view      View
	loadedAt  time.Time
	loaded    bool
	stale     bool
	sourceErr *SourceError
	lastErr   string // last logged source failure; "" when healthy
}

// NewWatcher creates a Watcher for the xray config at path.
func NewWatcher(path string) *Watcher {
	return &Watcher{path: path, now: time.Now}
}

// WithClock overrides the successful-load clock. Call it before Start.
func (w *Watcher) WithClock(now func() time.Time) *Watcher {
	w.now = now
	return w
}

// Start parses the config immediately, then re-parses on every file change
// until the context is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	w.reload()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("watch xray config; config edits need a panel restart", "path", w.path, "error", err)
		return
	}
	if err := watcher.Add(filepath.Dir(w.path)); err != nil {
		slog.Error("watch xray config; config edits need a panel restart", "path", w.path, "error", err)
		_ = watcher.Close()
		return
	}

	go func() {
		defer func() { _ = watcher.Close() }()
		var timer *time.Timer
		var fired <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		// schedule (re)arms the debounce, draining a stale tick first so an
		// expired-but-undrained timer cannot collapse the debounce.
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
			case event, ok := <-watcher.Events:
				if !ok {
					slog.Error("xray config watch ended; config edits need a panel restart", "path", w.path)
					return
				}
				if filepath.Clean(event.Name) != filepath.Clean(w.path) {
					continue // a sibling file in the same directory
				}
				schedule()
			case err, ok := <-watcher.Errors:
				if !ok {
					slog.Error("xray config watch ended; config edits need a panel restart", "path", w.path)
					return
				}
				slog.Warn("xray config watch error", "path", w.path, "error", err)
			case <-fired:
				timer = nil
				fired = nil
				w.reload()
			}
		}
	}()
}

// Roster returns the last good roster parse and its version. The version
// bumps only when the roster actually changes; 0 means no config was ever
// parsed successfully. The map is shared; callers must not mutate it.
func (w *Watcher) Roster() (map[string]User, uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.roster, w.version
}

// Snapshot returns the current parsed-xray source state. Its View is
// immutable, and its SourceError is copied so callers cannot mutate Watcher
// state.
func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()

	snapshot := Snapshot{
		View:      w.view,
		LoadedAt:  w.loadedAt,
		Stale:     w.stale,
		available: w.loaded,
	}
	if w.sourceErr != nil {
		sourceErr := *w.sourceErr
		snapshot.Error = &sourceErr
	}
	return snapshot
}

// reload re-reads and re-parses the config, swapping each last-valid result
// only when the shared parse succeeds. Roster versioning remains independent
// from successful profile-view loads.
func (w *Watcher) reload() {
	document, err := os.ReadFile(w.path)
	if err != nil {
		w.logFailure(ReadFailed, "cannot read xray config; keeping the last known roster", err)
		return
	}
	roster, view, err := parse(document)
	if err != nil {
		w.logFailure(ParseFailed, "cannot parse xray config; keeping the last known roster", err)
		return
	}
	loadedAt := w.now()

	w.mu.Lock()
	rosterChanged := w.roster == nil || !maps.Equal(w.roster, roster)
	if rosterChanged {
		w.roster = roster
		w.version++
	}
	w.view = view
	w.loadedAt = loadedAt
	w.loaded = true
	w.stale = false
	w.sourceErr = nil
	recovered := w.lastErr != ""
	w.lastErr = ""
	w.mu.Unlock()

	if recovered {
		slog.Info("xray config parse recovered")
	}
	if rosterChanged {
		slog.Info("xray config roster updated", "path", w.path, "users", len(roster))
	}
}

// logFailure records safe source state and logs a persistent failure once,
// when its reason or diagnostic changes, instead of on every save attempt.
func (w *Watcher) logFailure(reason ErrorReason, msg string, err error) {
	w.mu.Lock()
	w.stale = w.loaded
	sourceErr := safeSourceError(reason, w.stale)
	w.sourceErr = &sourceErr
	failure := string(reason) + ": " + err.Error()
	changed := w.lastErr != failure
	w.lastErr = failure
	w.mu.Unlock()

	if changed {
		slog.Warn(msg, "path", w.path, "error", err)
	}
}
