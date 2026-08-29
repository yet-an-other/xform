// Package filesource watches one file on the Host, re-parses it whenever it
// changes, and keeps the last valid parse — the shared shape behind the
// panel's watched sources (the xray config, the Advertised connection
// settings).
//
// It is the change-driven counterpart to internal/snapshot.Cache[T], which
// refreshes on a timer. Both keep the last good value and mark it stale when
// a refresh fails; only the trigger differs.
package filesource

import "time"

// Reason is a stable source failure reason, safe to expose to consumers. It
// never carries a filesystem error, document text, or server secret.
//
// A Watcher produces exactly one Reason of its own — ReadFailed, for the read
// it performs itself. Every other reason is named by the parse behind the
// seam, as a value of this type.
type Reason string

// ReadFailed is the only Reason a Watcher names: the file could not be read,
// so the parse was never reached.
const ReadFailed Reason = "read_failed"

// SourceError is the current source failure, safe to expose to consumers.
type SourceError struct {
	Reason  Reason
	Message string
}

// Message is one Reason's two renderings: the text shown when the source has
// never loaded, and the text shown when a last valid value survives.
type Message struct {
	Fresh string
	Stale string
}

// Messages is the parse's failure vocabulary — a table rather than a
// template, because the sources word their failures differently and one of
// them ("the version is not supported") fits no read/parse phrasing at all.
// A Reason absent from the table renders an empty message.
type Messages map[Reason]Message

// Parse turns one document into the value the source publishes.
//
// prev is the last valid value, or the zero value when none has parsed yet.
// It is passed so a parse can derive state that must change atomically with
// the swap — xrayconfig's roster version bumps only when the roster actually
// changed, which is knowable only against the previous parse.
//
// The returned Reason names the failure when err is non-nil, and is ignored
// otherwise. A parse that fails without naming a reason reports ParseFailed
// on its own behalf; the Watcher does not invent one.
type Parse[T any] func(prev T, document []byte) (T, Reason, error)

// Snapshot is a watched source's current state. Value and LoadedAt remain the
// last successful ones after a reload failure.
type Snapshot[T any] struct {
	Value    T
	LoadedAt time.Time
	Stale    bool
	Error    *SourceError

	configured bool
	available  bool
}

// Configured reports whether the source was given a path at all. An
// unconfigured source never reads, never fails, and is never stale.
func (s Snapshot[T]) Configured() bool {
	return s.configured
}

// Available reports whether the source has parsed successfully at least once.
// An available stale Snapshot still carries its last valid Value.
func (s Snapshot[T]) Available() bool {
	return s.available
}

// message renders one failure from the parse's table.
func (m Messages) message(reason Reason, stale bool) string {
	text := m[reason]
	if stale {
		return text.Stale
	}
	return text.Fresh
}
