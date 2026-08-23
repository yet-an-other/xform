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

// Watcher keeps the roster from the xray config file current: it parses the
// file at start and re-parses on every fsnotify change (SPEC.md §3). A
// failed re-parse keeps the last good roster — a half-saved config must not
// empty the users table or mark everyone gone. Version 0 means no config
// was ever parsed, so consumers can tell "no roster yet" from an empty one.
//
// The watcher follows the path, not the inode: it watches the parent
// directory, so atomic replaces (write temp + rename) are picked up.
type Watcher struct {
	path string

	mu      sync.Mutex
	roster  map[string]User
	version uint64
	lastErr string // last logged parse failure; "" when healthy
}

// NewWatcher creates a Watcher for the xray config at path.
func NewWatcher(path string) *Watcher {
	return &Watcher{path: path}
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
// parsed successfully. The map is shared — callers must not mutate it.
func (w *Watcher) Roster() (map[string]User, uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.roster, w.version
}

// reload re-reads and re-parses the config, swapping the roster only when
// the parse succeeds and the roster actually changed.
func (w *Watcher) reload() {
	document, err := os.ReadFile(w.path)
	if err != nil {
		w.logFailure("cannot read xray config; keeping the last known roster", err)
		return
	}
	roster, err := Parse(document)
	if err != nil {
		w.logFailure("cannot parse xray config; keeping the last known roster", err)
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.roster != nil && maps.Equal(w.roster, roster) {
		return // a save that changed nothing the panel reads
	}
	w.roster = roster
	w.version++
	if w.lastErr != "" {
		w.lastErr = ""
		slog.Info("xray config parse recovered")
	}
	slog.Info("xray config roster updated", "path", w.path, "users", len(roster))
}

// logFailure logs a persistent failure once — when its message changes —
// instead of on every save attempt.
func (w *Watcher) logFailure(msg string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastErr == err.Error() {
		return
	}
	w.lastErr = err.Error()
	slog.Warn(msg, "path", w.path, "error", err)
}
