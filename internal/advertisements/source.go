package advertisements

import (
	"errors"

	"github.com/yet-an-other/xform/internal/filesource"
)

// ErrorReason is a stable Advertised connection settings source failure.
type ErrorReason = filesource.Reason

const (
	// ReadFailed is named by the watched source itself.
	ReadFailed = filesource.ReadFailed
	// ParseFailed and UnsupportedVersion are this source's own.
	ParseFailed        ErrorReason = "parse_failed"
	UnsupportedVersion ErrorReason = "unsupported_version"
)

// SourceError is safe to expose to profile consumers.
type SourceError = filesource.SourceError

// Snapshot is the current Advertised connection settings source state. Its
// Value and LoadedAt remain the last successful ones after a reload failure.
type Snapshot = filesource.Snapshot[View]

// messages is this source's failure vocabulary, worded for the Dashboard.
var messages = filesource.Messages{
	ReadFailed: {
		Fresh: "The Advertised connection settings file could not be read.",
		Stale: "The Advertised connection settings file could not be read. Profiles use the last valid Advertised connection settings.",
	},
	ParseFailed: {
		Fresh: "The Advertised connection settings file could not be parsed.",
		Stale: "The Advertised connection settings file could not be parsed. Profiles use the last valid Advertised connection settings.",
	},
	UnsupportedVersion: {
		Fresh: "The Advertised connection settings version is not supported.",
		Stale: "The Advertised connection settings version is not supported. Profiles use the last valid Advertised connection settings.",
	},
}

// parseSource is the watched source's parse. The previous View is unused: an
// advertisements document means the same thing whatever preceded it.
func parseSource(_ View, document []byte) (View, ErrorReason, error) {
	view, err := Parse(document)
	if err != nil {
		if errors.Is(err, ErrUnsupportedVersion) {
			return View{}, UnsupportedVersion, err
		}
		return View{}, ParseFailed, err
	}
	return view, "", nil
}
