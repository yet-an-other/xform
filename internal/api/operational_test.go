package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/api"
	"github.com/yet-an-other/xform/internal/configsnapshot"
	"github.com/yet-an-other/xform/internal/journal"
	"github.com/yet-an-other/xform/internal/session"
)

// stubLogs serves one canned Log snapshot, recording the sources asked for.
type stubLogs struct {
	snapshot journal.Snapshot
	err      error

	collected []journal.Source
}

func (s *stubLogs) Collect(_ context.Context, source journal.Source) (journal.Snapshot, error) {
	s.collected = append(s.collected, source)
	if s.err != nil {
		return journal.Snapshot{}, s.err
	}
	snapshot := s.snapshot
	snapshot.Source = source
	return snapshot, nil
}

// stubConfig serves one canned Config snapshot.
type stubConfig struct {
	snapshot configsnapshot.Snapshot
	err      error

	reads int
}

func (s *stubConfig) Read(context.Context) (configsnapshot.Snapshot, error) {
	s.reads++
	if s.err != nil {
		return configsnapshot.Snapshot{}, s.err
	}
	return s.snapshot, nil
}

// newOperationalHandler wires the API with the operational sources under test
// and stubs everywhere else.
func newOperationalHandler(operational api.OperationalSources) http.Handler {
	return api.New(fixedHostStats{}, fixedXrayStatus{}, fixedUsers{}, fixedProfileSources{},
		operational, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo)
}

// operationalPaths are the three endpoints §6.4 and §6.5 add.
var operationalPaths = []string{"/api/v1/logs/panel", "/api/v1/logs/xray", "/api/v1/xray/config"}

func TestOperationalEndpointsRequireSession(t *testing.T) {
	handler := newOperationalHandler(api.OperationalSources{Logs: &stubLogs{}, Config: &stubConfig{}})

	for _, path := range operationalPaths {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: status = %d, want %d", path, response.Code, http.StatusUnauthorized)
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Errorf("GET %s: decode 401 body: %v", path, err)
			continue
		}
		if body["error"] == "" {
			t.Errorf("GET %s: 401 body = %v, want a JSON error", path, body)
		}
	}
}

// authenticatedGet performs one authenticated GET against handler.
func authenticatedGet(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// decodeObject reads a JSON object body.
func decodeObject(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", response.Body.String(), err)
	}
	return body
}

func TestConfigSnapshotEndpointReturnsTheContractPayload(t *testing.T) {
	const text = "{\n  \"inbounds\": [\n}\n"
	config := &stubConfig{snapshot: configsnapshot.Snapshot{
		CapturedAt: time.Unix(1_723_800_000, 0),
		Path:       "/usr/local/etc/xray/config.json",
		SizeBytes:  int64(len(text)),
		Text:       text,
	}}
	handler := newOperationalHandler(api.OperationalSources{Logs: &stubLogs{}, Config: config})

	response := authenticatedGet(t, handler, "/api/v1/xray/config")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	body := decodeObject(t, response)
	want := map[string]any{
		"captured_at": float64(1_723_800_000),
		"path":        "/usr/local/etc/xray/config.json",
		"size_bytes":  float64(len(text)),
		"text":        text,
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %#v, want %#v", body, want)
	}
	if config.reads != 1 {
		t.Errorf("reads = %d, want one fresh read per request", config.reads)
	}
}

func TestOperationalEndpointsAreNoStoreAndSendNoCORSHeaders(t *testing.T) {
	handler := newOperationalHandler(api.OperationalSources{Logs: &stubLogs{}, Config: &stubConfig{}})

	for _, path := range operationalPaths {
		response := authenticatedGet(t, handler, path)

		if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, cacheControl)
		}
		for header := range response.Header() {
			if strings.HasPrefix(http.CanonicalHeaderKey(header), "Access-Control-") {
				t.Errorf("GET %s: sent CORS header %s", path, header)
			}
		}
	}
}

func TestOperationalEndpointsRejectAnyQueryParameter(t *testing.T) {
	logs := &stubLogs{}
	config := &stubConfig{}
	handler := newOperationalHandler(api.OperationalSources{Logs: logs, Config: config})

	for _, path := range operationalPaths {
		for _, query := range []string{"?lines=1000", "?unit=sshd.service", "?refresh", "?a=1&b=2"} {
			response := authenticatedGet(t, handler, path+query)

			if response.Code != http.StatusBadRequest {
				t.Errorf("GET %s%s: status = %d, want %d", path, query, response.Code, http.StatusBadRequest)
				continue
			}
			if body := decodeObject(t, response); body["error"] != "invalid_request" {
				t.Errorf("GET %s%s: body = %v, want invalid_request", path, query, body)
			}
		}
	}
	// A rejected request never reaches a collection: the parameter is a caller
	// trying to widen a fixed snapshot, not a filter to drop.
	if len(logs.collected) != 0 || config.reads != 0 {
		t.Errorf("collected = %v, config reads = %d, want no collection for rejected requests", logs.collected, config.reads)
	}

	// A bare "?" carries no parameter, so there is nothing to reject.
	for _, path := range operationalPaths {
		if response := authenticatedGet(t, handler, path+"?"); response.Code != http.StatusOK {
			t.Errorf("GET %s?: status = %d, want %d", path, response.Code, http.StatusOK)
		}
	}
}

func TestConfigSnapshotFailuresMapToStableReasons(t *testing.T) {
	const secret = "vless://client-id-that-must-not-travel"
	tests := []struct {
		name   string
		reason configsnapshot.Reason
	}{
		{name: "unreadable", reason: configsnapshot.ReasonUnreadable},
		{name: "too large", reason: configsnapshot.ReasonTooLarge},
		{name: "not UTF-8", reason: configsnapshot.ReasonNotUTF8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newOperationalHandler(api.OperationalSources{Logs: &stubLogs{}, Config: &stubConfig{
				err: &configsnapshot.Error{Reason: test.reason, Detail: secret},
			}})

			response := authenticatedGet(t, handler, "/api/v1/xray/config")

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			body := decodeObject(t, response)
			want := map[string]any{"error": "config snapshot unavailable", "reason": string(test.reason)}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("body = %#v, want %#v", body, want)
			}
			if strings.Contains(response.Body.String(), secret) {
				t.Errorf("body = %s, want no retained content in the failure", response.Body)
			}
		})
	}
}

// logEntryFixture builds one normalized journal Entry.
func logEntryFixture(cursor string, timestamp uint64, message string) journal.Entry {
	identifier, pid, priority, encoding := "xform", 1427, 6, "utf-8"
	text := message
	return journal.Entry{
		Cursor: cursor, TimestampUS: timestamp, Unit: "xform.service",
		Identifier: &identifier, PID: &pid, Priority: &priority,
		Message: &text, MessageEncoding: &encoding,
	}
}

func TestLogSnapshotEndpointReturnsTheContractPayload(t *testing.T) {
	logs := &stubLogs{snapshot: journal.Snapshot{
		CapturedAt: time.Unix(1_723_800_000, 0),
		Unit:       "xform.service",
		Limit:      500,
		Entries: []journal.Entry{
			logEntryFixture("cursor-newest", 1_723_800_000_123_456, "Panel started"),
			logEntryFixture("cursor-older", 1_723_799_000_123_456, "Panel starting"),
		},
	}}
	handler := newOperationalHandler(api.OperationalSources{Logs: logs, Config: &stubConfig{}})

	response := authenticatedGet(t, handler, "/api/v1/logs/panel")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body struct {
		CapturedAt int64            `json:"captured_at"`
		Source     string           `json:"source"`
		Unit       string           `json:"unit"`
		Limit      int              `json:"limit"`
		EntryCount int              `json:"entry_count"`
		Entries    []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.CapturedAt != 1_723_800_000 || body.Source != "panel" || body.Unit != "xform.service" || body.Limit != 500 {
		t.Errorf("envelope = %+v, want the captured snapshot's fixed source, unit, and limit", body)
	}
	// The count is what was collected, never the limit.
	if body.EntryCount != 2 || len(body.Entries) != 2 {
		t.Fatalf("entry_count = %d with %d entries, want 2", body.EntryCount, len(body.Entries))
	}
	// Newest first, in the order the module collected them.
	if body.Entries[0]["cursor"] != "cursor-newest" || body.Entries[1]["cursor"] != "cursor-older" {
		t.Errorf("entries = %v, want newest first", body.Entries)
	}
	want := map[string]any{
		"cursor": "cursor-newest", "timestamp_us": float64(1_723_800_000_123_456), "unit": "xform.service",
		"identifier": "xform", "pid": float64(1427), "priority": float64(6),
		"message": "Panel started", "message_encoding": "utf-8", "message_truncated": false,
	}
	if !reflect.DeepEqual(body.Entries[0], want) {
		t.Errorf("entry = %#v, want %#v", body.Entries[0], want)
	}
	if !reflect.DeepEqual(logs.collected, []journal.Source{journal.SourcePanel}) {
		t.Errorf("collected = %v, want one panel collection", logs.collected)
	}
}

func TestLogSnapshotEndpointsCollectTheirOwnFixedSource(t *testing.T) {
	logs := &stubLogs{snapshot: journal.Snapshot{Unit: "xray.service", Limit: 500}}
	handler := newOperationalHandler(api.OperationalSources{Logs: logs, Config: &stubConfig{}})

	if body := decodeObject(t, authenticatedGet(t, handler, "/api/v1/logs/xray")); body["source"] != "xray" {
		t.Errorf("source = %v, want xray", body["source"])
	}
	if body := decodeObject(t, authenticatedGet(t, handler, "/api/v1/logs/panel")); body["source"] != "panel" {
		t.Errorf("source = %v, want panel", body["source"])
	}
	if !reflect.DeepEqual(logs.collected, []journal.Source{journal.SourceXray, journal.SourcePanel}) {
		t.Errorf("collected = %v, want each endpoint's own fixed source", logs.collected)
	}
}

func TestLogSnapshotKeepsNullFieldsAndAlwaysWritesMessageKeys(t *testing.T) {
	// journalctl's own oversized-field elision: no message at all, which the
	// entry still reports rather than omitting (§6.4).
	logs := &stubLogs{snapshot: journal.Snapshot{
		Unit: "xform.service", Limit: 500,
		Entries: []journal.Entry{{
			Cursor: "cursor-1", TimestampUS: 1_723_800_000_000_000, Unit: "xform.service",
			MessageTruncated: true,
		}},
	}}
	handler := newOperationalHandler(api.OperationalSources{Logs: logs, Config: &stubConfig{}})

	response := authenticatedGet(t, handler, "/api/v1/logs/panel")

	var body struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]any{
		"cursor": "cursor-1", "timestamp_us": float64(1_723_800_000_000_000), "unit": "xform.service",
		"identifier": nil, "pid": nil, "priority": nil,
		"message": nil, "message_encoding": nil, "message_truncated": true,
	}
	if !reflect.DeepEqual(body.Entries[0], want) {
		t.Errorf("entry = %#v, want %#v", body.Entries[0], want)
	}
}

func TestEmptyLogSnapshotIsASuccess(t *testing.T) {
	// An empty journal is a successful snapshot of nothing. It claims nothing
	// about whether deployment access was ever verified.
	logs := &stubLogs{snapshot: journal.Snapshot{Unit: "xform.service", Limit: 500}}
	handler := newOperationalHandler(api.OperationalSources{Logs: logs, Config: &stubConfig{}})

	response := authenticatedGet(t, handler, "/api/v1/logs/panel")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := decodeObject(t, response)
	if body["entry_count"] != float64(0) {
		t.Errorf("entry_count = %v, want 0", body["entry_count"])
	}
	entries, ok := body["entries"].([]any)
	if !ok || len(entries) != 0 {
		t.Errorf("entries = %#v, want an empty list rather than null", body["entries"])
	}
	wantKeys := []string{"captured_at", "source", "unit", "limit", "entry_count", "entries"}
	if len(body) != len(wantKeys) {
		t.Errorf("body keys = %v, want exactly %v", body, wantKeys)
	}
}

func TestLogSnapshotFailuresMapToStableStatuses(t *testing.T) {
	const stderr = "Failed to open journal: Permission denied"
	tests := []struct {
		reason     journal.Reason
		wantStatus int
		wantRetry  string
	}{
		{reason: journal.ReasonSnapshotInProgress, wantStatus: http.StatusTooManyRequests, wantRetry: "1"},
		{reason: journal.ReasonJournalctlUnavailable, wantStatus: http.StatusServiceUnavailable},
		{reason: journal.ReasonAccessDenied, wantStatus: http.StatusServiceUnavailable},
		{reason: journal.ReasonTimeout, wantStatus: http.StatusServiceUnavailable},
		{reason: journal.ReasonOutputTooLarge, wantStatus: http.StatusServiceUnavailable},
		{reason: journal.ReasonMalformedOutput, wantStatus: http.StatusServiceUnavailable},
		{reason: journal.ReasonCommandFailed, wantStatus: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			handler := newOperationalHandler(api.OperationalSources{
				Logs:   &stubLogs{err: &journal.Error{Reason: test.reason, Detail: stderr}},
				Config: &stubConfig{},
			})

			response := authenticatedGet(t, handler, "/api/v1/logs/panel")

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if retry := response.Header().Get("Retry-After"); retry != test.wantRetry {
				t.Errorf("Retry-After = %q, want %q", retry, test.wantRetry)
			}
			body := decodeObject(t, response)
			want := map[string]any{"error": "log snapshot unavailable", "reason": string(test.reason)}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("body = %#v, want %#v", body, want)
			}
			// journalctl's stderr is the data the snapshot exists to bound.
			if strings.Contains(response.Body.String(), "Permission denied") {
				t.Errorf("body = %s, want no journalctl stderr", response.Body)
			}
		})
	}
}

func TestConcurrentCollectionIsRejectedWithoutStartingAnother(t *testing.T) {
	// The reader holds one global process slot; a second request is told to
	// retry rather than queued behind it.
	logs := &stubLogs{err: &journal.Error{Reason: journal.ReasonSnapshotInProgress}}
	handler := newOperationalHandler(api.OperationalSources{Logs: logs, Config: &stubConfig{}})

	response := authenticatedGet(t, handler, "/api/v1/logs/xray")

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
	if retry := response.Header().Get("Retry-After"); retry != "1" {
		t.Errorf("Retry-After = %q, want 1", retry)
	}
	if body := decodeObject(t, response); body["reason"] != string(journal.ReasonSnapshotInProgress) {
		t.Errorf("reason = %v, want %s", body["reason"], journal.ReasonSnapshotInProgress)
	}
}

func TestClientDisconnectNeedsNoResponseBody(t *testing.T) {
	// A disconnect cancels collection; the module's own kill-and-reap is what
	// matters, and there is nobody left to answer (§6.4).
	logs := &stubLogs{err: context.Canceled}
	config := &stubConfig{err: context.Canceled}
	handler := newOperationalHandler(api.OperationalSources{Logs: logs, Config: config})
	cookie := login(t, handler, testPassword)

	for _, path := range operationalPaths {
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		cancel()

		handler.ServeHTTP(response, request)

		if response.Body.Len() != 0 {
			t.Errorf("GET %s: body = %s, want none for a disconnected client", path, response.Body)
		}
	}
	// Collection was still attempted, so the module can clean up after itself.
	if len(logs.collected) != 2 || config.reads != 1 {
		t.Errorf("collected = %v, config reads = %d, want each request to reach its module", logs.collected, config.reads)
	}
}

func TestOperationalFailuresLeaveOtherEndpointsAlone(t *testing.T) {
	// Independent freshness (§3.4): a viewer's failure is not another source's
	// status.
	handler := newOperationalHandler(api.OperationalSources{
		Logs:   &stubLogs{err: &journal.Error{Reason: journal.ReasonAccessDenied}},
		Config: &stubConfig{err: &configsnapshot.Error{Reason: configsnapshot.ReasonUnreadable}},
	})
	cookie := login(t, handler, testPassword)
	for _, path := range operationalPaths {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}

	for _, path := range []string{"/api/v1/server", "/api/v1/xray", "/api/v1/users", "/api/v1/panel"} {
		response := authenticatedGet(t, handler, path)

		if response.Code != http.StatusOK {
			t.Errorf("GET %s after operational failures: status = %d, want %d", path, response.Code, http.StatusOK)
		}
	}
}

func TestOnlyTheFixedLogRoutesExist(t *testing.T) {
	handler := newOperationalHandler(api.OperationalSources{Logs: &stubLogs{}, Config: &stubConfig{}})

	for _, path := range []string{"/api/v1/logs", "/api/v1/logs/sshd", "/api/v1/logs/panel/extra", "/api/v1/xray/config/raw"} {
		response := authenticatedGet(t, handler, path)

		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}
}

func TestAFailureOutsideTheContractIsNotDressedAsAStableReason(t *testing.T) {
	// The modules only ever return their own stable failures, so this is a
	// Panel-internal fault. It says so instead of borrowing a reason the
	// Dashboard would read as a real collection outcome.
	handler := newOperationalHandler(api.OperationalSources{
		Logs:   &stubLogs{err: errors.New("unknown log source \"sshd\"")},
		Config: &stubConfig{err: errors.New("reader is not configured")},
	})

	for path, want := range map[string]string{
		"/api/v1/logs/panel":  "log snapshot unavailable",
		"/api/v1/xray/config": "config snapshot unavailable",
	} {
		response := authenticatedGet(t, handler, path)

		if response.Code != http.StatusInternalServerError {
			t.Errorf("GET %s: status = %d, want %d", path, response.Code, http.StatusInternalServerError)
			continue
		}
		body := decodeObject(t, response)
		if !reflect.DeepEqual(body, map[string]any{"error": want}) {
			t.Errorf("GET %s: body = %#v, want %#v without a reason", path, body, map[string]any{"error": want})
		}
		if strings.Contains(response.Body.String(), "sshd") {
			t.Errorf("GET %s: body = %s, want no internal detail", path, response.Body)
		}
	}
}
