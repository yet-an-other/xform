package xrayconfig

import (
	"maps"
	"slices"

	"github.com/yet-an-other/xform/internal/filesource"
)

// ErrorReason is a stable xray-config source failure reason.
type ErrorReason = filesource.Reason

const (
	// ReadFailed is named by the watched source itself.
	ReadFailed = filesource.ReadFailed
	// ParseFailed is this source's own: the file was read but is not an xray
	// config the panel can parse.
	ParseFailed ErrorReason = "parse_failed"
)

// SourceError is safe to expose to profile consumers. It never includes a
// filesystem error, malformed config text, or server secret.
type SourceError = filesource.SourceError

// Snapshot is the current parsed-xray source state. Its Value and LoadedAt
// remain the last successful ones after a reload failure.
type Snapshot = filesource.Snapshot[Parsed]

// Parsed is one successful parse of the xray config: the Roster parse, the
// version that Roster is at, and the immutable inbound View behind
// Connection profiles.
//
// Version bumps only when the Roster actually changes — a label, a Client
// ID, or an attachment — so a profile-only config edit does not re-sync
// every user. 0 means no config has ever parsed successfully — a missing or
// broken config must not mark anybody disabled.
type Parsed struct {
	// Roster is shared with every caller; none of them may mutate it.
	Roster  RosterParse
	Version uint64
	View    View
}

// messages is this source's failure vocabulary, worded for the Dashboard.
var messages = filesource.Messages{
	ReadFailed: {
		Fresh: "The configured xray file could not be read.",
		Stale: "The configured xray file could not be read; profiles use the last valid parse.",
	},
	ParseFailed: {
		Fresh: "The configured xray file could not be parsed.",
		Stale: "The configured xray file could not be parsed; profiles use the last valid parse.",
	},
}

// parseSource is the watched source's parse. It carries the roster version
// forward from the previous parse, bumping it only on a real roster change —
// the version has to move in the same step as the swap, which is why the
// previous parse is an input rather than watcher state.
func parseSource(previous Parsed, document []byte) (Parsed, ErrorReason, error) {
	roster, view, err := parse(document)
	if err != nil {
		return Parsed{}, ParseFailed, err
	}
	next := Parsed{Roster: roster, Version: previous.Version, View: view}
	if previous.Roster.Labels == nil || !sameRoster(previous.Roster, roster) {
		next.Version = previous.Version + 1
	}
	return next, "", nil
}

// sameRoster reports whether two parses hand off the same roster: identical
// labels and identical clients (Client ID and ordered attachments).
func sameRoster(a, b RosterParse) bool {
	return maps.Equal(a.Labels, b.Labels) &&
		maps.EqualFunc(a.Clients, b.Clients, func(x, y Client) bool {
			return x.ClientID == y.ClientID && slices.Equal(x.Inbounds, y.Inbounds)
		})
}
