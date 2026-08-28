package api

import (
	"errors"
	"net/http"

	"github.com/yet-an-other/xform/internal/configsnapshot"
	"github.com/yet-an-other/xform/internal/journal"
)

// OperationalSources are the two point-in-time viewers behind IN-DEV-SPEC
// §6.4 and §6.5.
// They are grouped because they share one property the rest of the API does
// not have: each is collected per request and never cached, and a failure in
// either changes no other source's status (§3.4).
type OperationalSources struct {
	Logs   logSnapshots
	Config configSnapshots
}

// The two failure messages §6.4 and §6.5 fix, paired with a stable reason.
const (
	logSnapshotUnavailable    = "log snapshot unavailable"
	configSnapshotUnavailable = "config snapshot unavailable"
)

// logSnapshotResponse is GET /api/v1/logs/{source} (§6.4).
type logSnapshotResponse struct {
	CapturedAt int64          `json:"captured_at"`
	Source     journal.Source `json:"source"`
	Unit       string         `json:"unit"`
	Limit      int            `json:"limit"`
	EntryCount int            `json:"entry_count"`
	Entries    []logEntry     `json:"entries"`
}

// logEntry is one normalized record. message, message_encoding, and
// message_truncated are always present keys, nullable where the record
// carried no usable value (§6.4).
type logEntry struct {
	Cursor           string  `json:"cursor"`
	TimestampUS      uint64  `json:"timestamp_us"`
	Unit             string  `json:"unit"`
	Identifier       *string `json:"identifier"`
	PID              *int    `json:"pid"`
	Priority         *int    `json:"priority"`
	Message          *string `json:"message"`
	MessageEncoding  *string `json:"message_encoding"`
	MessageTruncated bool    `json:"message_truncated"`
}

// configSnapshotResponse is GET /api/v1/xray/config (§6.5).
type configSnapshotResponse struct {
	CapturedAt int64  `json:"captured_at"`
	Path       string `json:"path"`
	SizeBytes  int64  `json:"size_bytes"`
	Text       string `json:"text"`
}

func logSnapshotJSON(snapshot journal.Snapshot) logSnapshotResponse {
	entries := make([]logEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries = append(entries, logEntry{
			Cursor:           entry.Cursor,
			TimestampUS:      entry.TimestampUS,
			Unit:             entry.Unit,
			Identifier:       entry.Identifier,
			PID:              entry.PID,
			Priority:         entry.Priority,
			Message:          entry.Message,
			MessageEncoding:  entry.MessageEncoding,
			MessageTruncated: entry.MessageTruncated,
		})
	}
	return logSnapshotResponse{
		CapturedAt: snapshot.CapturedAt.Unix(),
		Source:     snapshot.Source,
		Unit:       snapshot.Unit,
		Limit:      snapshot.Limit,
		// The count is the entries actually collected, never the limit: an
		// empty journal is a successful snapshot of nothing.
		EntryCount: len(entries),
		Entries:    entries,
	}
}

func configSnapshotJSON(snapshot configsnapshot.Snapshot) configSnapshotResponse {
	return configSnapshotResponse{
		CapturedAt: snapshot.CapturedAt.Unix(),
		Path:       snapshot.Path,
		SizeBytes:  snapshot.SizeBytes,
		Text:       snapshot.Text,
	}
}

// logSnapshotHandler serves one fixed source. The source is bound here at
// route registration, never read from the request: §6.4's endpoints are the
// whole vocabulary.
func logSnapshotHandler(logs logSnapshots, source journal.Source) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if rejectedQuery(response, request) {
			return
		}
		snapshot, err := logs.Collect(request.Context(), source)
		if err != nil {
			writeLogFailure(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, logSnapshotJSON(snapshot))
	}
}

func configSnapshotHandler(config configSnapshots) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if rejectedQuery(response, request) {
			return
		}
		snapshot, err := config.Read(request.Context())
		if err != nil {
			writeConfigFailure(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, configSnapshotJSON(snapshot))
	}
}

// rejectedQuery reports whether the request carried a query parameter, which
// these endpoints accept none of (§6.4, §6.5): a parameter is a caller trying
// to widen a deliberately fixed collection, not a filter to ignore.
// A bare "?" carries no parameter and is therefore not one: the rule is about
// what a caller passed, not about the punctuation they left behind.
func rejectedQuery(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.RawQuery == "" {
		return false
	}
	writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
	return true
}

func writeLogFailure(response http.ResponseWriter, request *http.Request, err error) {
	if abandoned(request) {
		return
	}
	var failure *journal.Error
	if !errors.As(err, &failure) {
		// Not a collection outcome at all, so no stable reason describes it: a
		// Panel-internal failure, reported as one rather than dressed up as a
		// journal reason it never was.
		writeSnapshotFailure(response, http.StatusInternalServerError, logSnapshotUnavailable, "")
		return
	}
	if failure.Reason == journal.ReasonSnapshotInProgress {
		// The one global process slot is busy; the Dashboard may simply try
		// again, so say when rather than only that it failed.
		response.Header().Set("Retry-After", "1")
		writeSnapshotFailure(response, http.StatusTooManyRequests, logSnapshotUnavailable, string(failure.Reason))
		return
	}
	writeSnapshotFailure(response, http.StatusServiceUnavailable, logSnapshotUnavailable, string(failure.Reason))
}

func writeConfigFailure(response http.ResponseWriter, request *http.Request, err error) {
	if abandoned(request) {
		return
	}
	var failure *configsnapshot.Error
	if !errors.As(err, &failure) {
		writeSnapshotFailure(response, http.StatusInternalServerError, configSnapshotUnavailable, "")
		return
	}
	writeSnapshotFailure(response, http.StatusServiceUnavailable, configSnapshotUnavailable, string(failure.Reason))
}

// abandoned reports whether the caller went away mid-collection. A client
// disconnect has no required response body (§6.4); the module's own cleanup
// is what matters, and writing to a gone connection would say nothing.
func abandoned(request *http.Request) bool {
	return request.Context().Err() != nil
}

// writeSnapshotFailure writes the stable failure shape. Only the module's own
// stable reason travels — never journalctl's stderr, a journal message, or
// file content, which are the data these snapshots exist to bound. An empty
// reason is omitted rather than sent blank: the contract's reasons all name a
// real outcome, and none of them names "something else went wrong".
func writeSnapshotFailure(response http.ResponseWriter, status int, message, reason string) {
	body := map[string]string{"error": message}
	if reason != "" {
		body["reason"] = reason
	}
	writeJSON(response, status, body)
}
