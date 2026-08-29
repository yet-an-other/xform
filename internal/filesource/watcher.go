package filesource

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// defaultDebounce absorbs the burst of events a single save produces (write +
// chmod, or temp-file rename) before re-reading the file.
const defaultDebounce = 250 * time.Millisecond

// Watcher keeps one watched source current. It parses the file at startup and
// after each fsnotify change. A failed reload keeps the last valid value, so a
// half-saved file cannot empty what the source feeds.
//
// Watcher follows the path rather than the inode. It watches the parent
// directory so it also detects atomic file replacement.
type Watcher[T any] struct {
	path    string
	subject string // the source's name in log lines, e.g. "xray config"
	parse   Parse[T]
	message Messages
	now     func() time.Time
	logger  *slog.Logger

	// debounce is a field only so tests can shrink it; nothing outside this
	// package can lengthen the window a save is absorbed over.
	debounce time.Duration

	mu          sync.Mutex
	value       T
	loadedAt    time.Time
	loaded      bool
	stale       bool
	sourceErr   *SourceError
	lastErr     string // last logged source failure; "" when healthy
	subscribers []chan struct{}
}

// New creates a Watcher over path, naming it subject in the panel's own logs.
// An empty path is an unconfigured source: Start performs no file access and
// Snapshot reports no error.
func New[T any](path, subject string, parse Parse[T], messages Messages) *Watcher[T] {
	return &Watcher[T]{
		path:     path,
		subject:  subject,
		parse:    parse,
		message:  messages,
		now:      time.Now,
		logger:   slog.Default(),
		debounce: defaultDebounce,
	}
}

// WithClock overrides the successful-load clock. Call it before Start.
func (w *Watcher[T]) WithClock(now func() time.Time) *Watcher[T] {
	w.now = now
	return w
}

// WithLogger overrides source logging. Call it before Start.
func (w *Watcher[T]) WithLogger(logger *slog.Logger) *Watcher[T] {
	w.logger = logger
	return w
}

// Start parses the file immediately, then re-parses on every change until the
// context is cancelled. An unconfigured source does nothing.
func (w *Watcher[T]) Start(ctx context.Context) {
	if w.path == "" {
		return
	}
	w.reload()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		w.logger.Error("watch "+w.subject+"; changes need a panel restart", "path", w.path, "error", err)
		return
	}
	if err := watcher.Add(filepath.Dir(w.path)); err != nil {
		w.logger.Error("watch "+w.subject+"; changes need a panel restart", "path", w.path, "error", err)
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
				timer.Reset(w.debounce)
			} else {
				timer = time.NewTimer(w.debounce)
			}
			fired = timer.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					w.logger.Error(w.subject+" watch ended; changes need a panel restart", "path", w.path)
					return
				}
				if filepath.Clean(event.Name) != filepath.Clean(w.path) {
					continue // a sibling file in the same directory
				}
				schedule()
			case err, ok := <-watcher.Errors:
				if !ok {
					w.logger.Error(w.subject+" watch ended; changes need a panel restart", "path", w.path)
					return
				}
				w.logger.Warn(w.subject+" watch error", "path", w.path, "error", err)
			case <-fired:
				timer = nil
				fired = nil
				w.reload()
			}
		}
	}()
}

// Snapshot returns the source's current state. Its Value is whatever the
// parse published, and its SourceError is copied so callers cannot mutate
// Watcher state.
func (w *Watcher[T]) Snapshot() Snapshot[T] {
	w.mu.Lock()
	defer w.mu.Unlock()

	snapshot := Snapshot[T]{
		Value:      w.value,
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

// Changes subscribes to successful loads. Notifications are buffered and
// coalesced when the receiver is still handling an earlier one.
func (w *Watcher[T]) Changes() <-chan struct{} {
	changes := make(chan struct{}, 1)
	w.mu.Lock()
	w.subscribers = append(w.subscribers, changes)
	w.mu.Unlock()
	return changes
}

// reload re-reads and re-parses the file, swapping the last valid value only
// when the parse succeeds.
func (w *Watcher[T]) reload() {
	document, err := os.ReadFile(w.path)
	if err != nil {
		w.recordFailure(ReadFailed, err)
		return
	}

	w.mu.Lock()
	previous := w.value
	w.mu.Unlock()

	value, reason, err := w.parse(previous, document)
	if err != nil {
		w.recordFailure(reason, err)
		return
	}
	loadedAt := w.now()

	w.mu.Lock()
	w.value = value
	w.loadedAt = loadedAt
	w.loaded = true
	w.stale = false
	w.sourceErr = nil
	recovered := w.lastErr != ""
	w.lastErr = ""
	for _, changes := range w.subscribers {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	w.mu.Unlock()

	if recovered {
		w.logger.Info(w.subject + " load recovered")
	}
}

// recordFailure records safe source state and logs a persistent failure once,
// when its reason or diagnostic changes, instead of on every save attempt.
func (w *Watcher[T]) recordFailure(reason Reason, diagnostic error) {
	w.mu.Lock()
	w.stale = w.loaded
	w.sourceErr = &SourceError{Reason: reason, Message: w.message.message(reason, w.stale)}
	failure := string(reason) + ": " + diagnostic.Error()
	changed := w.lastErr != failure
	w.lastErr = failure
	w.mu.Unlock()

	if changed {
		w.logger.Warn("cannot load "+w.subject+"; keeping the last valid value",
			"path", w.path, "reason", reason, "error", diagnostic)
	}
}
