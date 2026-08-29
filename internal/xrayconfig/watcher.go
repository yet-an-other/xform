package xrayconfig

import (
	"context"
	"log/slog"

	"github.com/yet-an-other/xform/internal/filesource"
)

// Watcher keeps the Roster and parsed Connection profile view current, as one
// watched source over the xray config. Everything about reading, debouncing,
// and retaining the last valid parse lives in filesource; what remains here is
// the parse itself and the roster-change log.
type Watcher struct {
	source *filesource.Watcher[Parsed]
	path   string
}

// NewWatcher creates a Watcher for the xray config at path.
func NewWatcher(path string) *Watcher {
	return &Watcher{
		source: filesource.New(path, "xray config", parseSource, messages),
		path:   path,
	}
}

// Start parses the config immediately, then re-parses on every file change
// until the context is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	// Subscribed before the source starts, so the first load's notification
	// lands in the buffer rather than being published to nobody.
	loads := w.source.Changes()
	w.source.Start(ctx)
	go w.logRosterChanges(ctx, loads)
}

// Roster returns the last good roster parse and its version. The map is
// shared; callers must not mutate it.
func (w *Watcher) Roster() (map[string]User, uint64) {
	parsed := w.source.Snapshot().Value
	return parsed.Roster, parsed.Version
}

// Snapshot returns the current parsed-xray source state.
func (w *Watcher) Snapshot() Snapshot {
	return w.source.Snapshot()
}

// Changes subscribes to successful parses.
func (w *Watcher) Changes() <-chan struct{} {
	return w.source.Changes()
}

// logRosterChanges reports a roster that actually moved. It runs off the load
// notification rather than inside the parse, so the parse stays a function of
// its inputs.
func (w *Watcher) logRosterChanges(ctx context.Context, loads <-chan struct{}) {
	var reported uint64
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-loads:
			if !ok {
				return
			}
			parsed := w.source.Snapshot().Value
			if parsed.Version == reported {
				continue // a profile-only edit: the roster did not move
			}
			reported = parsed.Version
			slog.Info("xray config roster updated", "path", w.path, "users", len(parsed.Roster))
		}
	}
}
