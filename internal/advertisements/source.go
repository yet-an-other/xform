package advertisements

import "time"

// ErrorReason is a stable Advertised connection settings source failure.
type ErrorReason string

const (
	ReadFailed         ErrorReason = "read_failed"
	ParseFailed        ErrorReason = "parse_failed"
	UnsupportedVersion ErrorReason = "unsupported_version"
)

// SourceError is safe to expose to profile consumers.
type SourceError struct {
	Reason  ErrorReason
	Message string
}

// Snapshot is the current Advertised connection settings source state. View
// and LoadedAt remain the last successful values after a reload failure.
type Snapshot struct {
	View     View
	LoadedAt time.Time
	Stale    bool
	Error    *SourceError

	configured bool
	available  bool
}

// Configured reports whether XFORM_CONNECTIONS_CONFIG selected a file.
func (s Snapshot) Configured() bool {
	return s.configured
}

// Available reports whether the configured source has loaded successfully at
// least once.
func (s Snapshot) Available() bool {
	return s.available
}

func safeSourceError(reason ErrorReason, stale bool) SourceError {
	var message string
	switch reason {
	case ReadFailed:
		message = "The Advertised connection settings file could not be read."
	case ParseFailed:
		message = "The Advertised connection settings file could not be parsed."
	case UnsupportedVersion:
		message = "The Advertised connection settings version is not supported."
	}
	if stale {
		message += " Profiles use the last valid Advertised connection settings."
	}
	return SourceError{Reason: reason, Message: message}
}
