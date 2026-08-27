package xrayconfig

import "time"

// ErrorReason is a stable xray-config source failure reason.
type ErrorReason string

const (
	ReadFailed  ErrorReason = "read_failed"
	ParseFailed ErrorReason = "parse_failed"
)

// SourceError is safe to expose to profile consumers. It never includes a
// filesystem error, malformed config text, or server secret.
type SourceError struct {
	Reason  ErrorReason
	Message string
}

// Snapshot is the current parsed-xray source state. View and LoadedAt remain
// the last successful values after a reload failure.
type Snapshot struct {
	View     View
	LoadedAt time.Time
	Stale    bool
	Error    *SourceError

	available bool
}

// Available reports whether the source has parsed successfully at least once.
// An available stale Snapshot still carries its last valid View.
func (s Snapshot) Available() bool {
	return s.available
}

func safeSourceError(reason ErrorReason, stale bool) SourceError {
	var message string
	switch reason {
	case ReadFailed:
		message = "The configured xray file could not be read."
		if stale {
			message = "The configured xray file could not be read; profiles use the last valid parse."
		}
	case ParseFailed:
		message = "The configured xray file could not be parsed."
		if stale {
			message = "The configured xray file could not be parsed; profiles use the last valid parse."
		}
	}
	return SourceError{Reason: reason, Message: message}
}
