package advertisements

import (
	"context"
	"log/slog"

	"github.com/yet-an-other/xform/internal/filesource"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// InboundSource supplies the parsed xray view used only to identify
// advertisements that do not reference a current inbound.
type InboundSource interface {
	Snapshot() xrayconfig.Snapshot
	Changes() <-chan struct{}
}

// Watcher keeps an optional Advertised connection settings source current, as
// one watched source. Reading, debouncing, and retaining the last valid parse
// live in filesource; what remains here is the parse and the bounded warning
// about advertisements that select no current inbound tag.
type Watcher struct {
	source   *filesource.Watcher[View]
	logger   *slog.Logger
	inbounds InboundSource
}

// NewWatcher creates a source for path. An empty path is an unconfigured
// source; Start performs no file access and Snapshot reports no error.
func NewWatcher(path string) *Watcher {
	return &Watcher{
		source: filesource.New(path, "Advertised connection settings", parseSource, messages),
		logger: slog.Default(),
	}
}

// WithLogger overrides source logging.
func (w *Watcher) WithLogger(logger *slog.Logger) *Watcher {
	w.logger = logger
	w.source.WithLogger(logger)
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
	var inboundChanges <-chan struct{}
	if w.inbounds != nil {
		inboundChanges = w.inbounds.Changes()
	}
	// Subscribed before the source starts, so the first load's notification
	// lands in the buffer rather than being published to nobody.
	loads := w.source.Changes()
	w.source.Start(ctx)
	go w.reportLoads(ctx, loads, inboundChanges)
}

// Snapshot returns the source's current immutable View and freshness state.
func (w *Watcher) Snapshot() Snapshot {
	return w.source.Snapshot()
}

// reportLoads runs the work that follows a successful load but does not
// belong inside it: the load log, and the unknown-inbound warning — which
// also has to re-run when the xray view changes underneath a View that has
// not itself moved.
func (w *Watcher) reportLoads(ctx context.Context, loads, inboundChanges <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-loads:
			if !ok {
				return
			}
			view := w.source.Snapshot().Value
			w.logger.Info("Advertised connection settings updated", "advertisements", len(view.advertisements))
			w.warnUnknownInbounds(view)
		case _, ok := <-inboundChanges:
			if !ok {
				inboundChanges = nil
				continue
			}
			if snapshot := w.source.Snapshot(); snapshot.Available() {
				w.warnUnknownInbounds(snapshot.Value)
			}
		}
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
	for _, inbound := range inboundSnapshot.Value.View.Inbounds() {
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
