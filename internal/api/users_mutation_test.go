package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/api"
	"github.com/yet-an-other/xform/internal/roster"
	"github.com/yet-an-other/xform/internal/session"
	"github.com/yet-an-other/xform/internal/users"
)

// stubRoster is the roster seam for handler tests: scripted mutations plus
// the state the users endpoint merges in.
type stubRoster struct {
	addResult roster.MutationResult
	addErr    error
	gotEmail  string
	gotID     string
	gotTags   []string
	called    bool

	editResult   roster.MutationResult
	editErr      error
	editCalled   bool
	gotEditEmail string
	gotEditReq   roster.EditRequest

	disableResult   roster.SyncState
	disableErr      error
	disableCalled   bool
	disableDisabled bool
	gotDisableEmail string

	enableResult   roster.MutationResult
	enableErr      error
	enableCalled   bool
	gotEnableEmail string

	sync      roster.SyncState
	userState map[string]roster.ApplyState
	options   []roster.InboundOption
}

func (s *stubRoster) Add(_ context.Context, email, clientID string, inbounds []string) (roster.MutationResult, error) {
	s.called = true
	s.gotEmail, s.gotID, s.gotTags = email, clientID, inbounds
	return s.addResult, s.addErr
}

func (s *stubRoster) Edit(_ context.Context, email string, req roster.EditRequest) (roster.MutationResult, error) {
	s.editCalled = true
	s.gotEditEmail, s.gotEditReq = email, req
	return s.editResult, s.editErr
}

func (s *stubRoster) Disable(_ context.Context, email string) (roster.SyncState, bool, error) {
	s.disableCalled = true
	s.gotDisableEmail = email
	if s.disableResult == "" {
		return roster.Synced, s.disableDisabled, s.disableErr
	}
	return s.disableResult, s.disableDisabled, s.disableErr
}

func (s *stubRoster) Enable(_ context.Context, email string) (roster.MutationResult, error) {
	s.enableCalled = true
	s.gotEnableEmail = email
	if s.enableResult.User.Email == "" && s.enableResult.Sync == "" {
		return roster.MutationResult{User: roster.Record{Email: email}, Sync: roster.Synced}, s.enableErr
	}
	return s.enableResult, s.enableErr
}

func (s *stubRoster) Sync() roster.SyncState {
	if s.sync == "" {
		return roster.Synced
	}
	return s.sync
}

func (s *stubRoster) UserStates() map[string]roster.ApplyState { return s.userState }

func (s *stubRoster) InboundOptions() []roster.InboundOption { return s.options }

func authedPost(t *testing.T, handler http.Handler, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// The add mutation (user-management spec §5): stored once, the answer
// carries the full record and the Roster sync state.
func TestAddUserStoresAndAnswersWithTheRecordAndSync(t *testing.T) {
	rosterSource := &stubRoster{addResult: roster.MutationResult{
		User: users.RosterRecord{
			Email: "alice@example.com", ClientID: "1d37a118-4f1b-4dc0-9e3c-3426b07518df",
			Inbounds: []string{"vless-vision"}, CreatedAt: 1_780_000_000, UpdatedAt: 1_780_000_000,
		},
		Sync: roster.Synced,
	}}
	handler := newRosterHandler(rosterSource)
	cookie := login(t, handler, testPassword)

	response := authedPost(t, handler, cookie, "/api/v1/users",
		`{"email": "alice@example.com", "client_id": "1d37a118-4f1b-4dc0-9e3c-3426b07518df", "inbounds": ["vless-vision"]}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", response.Code, response.Body)
	}
	if !rosterSource.called || rosterSource.gotEmail != "alice@example.com" ||
		rosterSource.gotID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" ||
		len(rosterSource.gotTags) != 1 || rosterSource.gotTags[0] != "vless-vision" {
		t.Errorf("the mutation got %q / %q / %v", rosterSource.gotEmail, rosterSource.gotID, rosterSource.gotTags)
	}
	var body struct {
		User struct {
			Email    string   `json:"email"`
			ClientID string   `json:"client_id"`
			Inbounds []string `json:"inbounds"`
		} `json:"user"`
		RosterSync string `json:"roster_sync"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.User.Email != "alice@example.com" || body.RosterSync != "synced" {
		t.Errorf("body = %+v", body)
	}
}

// Validation violations answer 409 with the machine-readable reason the
// dialog shows (§5).
func TestAddUserConflictsCarryTheReason(t *testing.T) {
	rosterSource := &stubRoster{addErr: &roster.ConflictError{Reason: roster.ReasonEmailTaken}}
	handler := newRosterHandler(rosterSource)
	cookie := login(t, handler, testPassword)

	response := authedPost(t, handler, cookie, "/api/v1/users", `{"email": "alice@example.com"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "email_taken" {
		t.Errorf("reason = %q, want email_taken", body["error"])
	}
}

func TestAddUserRejectsMalformedBodies(t *testing.T) {
	rosterSource := &stubRoster{}
	handler := newRosterHandler(rosterSource)
	cookie := login(t, handler, testPassword)

	response := authedPost(t, handler, cookie, "/api/v1/users", `{not json`)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
	if rosterSource.called {
		t.Error("a malformed body never reaches the roster")
	}
}

// Mutations reject cross-site requests (§5): the SameSite=Lax cookie already
// keeps cross-site POSTs from carrying a session; Origin and Sec-Fetch-Site
// close the remaining gaps.
func TestAddUserRejectsCrossSiteRequests(t *testing.T) {
	for _, test := range []struct {
		name      string
		origin    string
		fetchSite string
		host      string // request Host; defaults to panel.example.com
		want      int
	}{
		{"no headers (same-origin browser form, curl)", "", "", "", http.StatusCreated},
		{"same-origin Origin", "http://panel.example.com", "", "", http.StatusCreated},
		{"same-site fetch", "", "same-origin", "", http.StatusCreated},
		// A TLS proxy forwarding nginx's $host drops the public port while
		// the browser's Origin keeps it — still the same origin, not a
		// cross-site probe.
		{"Origin keeps the public port, Host portless (nginx $host)", "https://panel.example.com:9443", "", "", http.StatusCreated},
		{"Origin keeps the default port, Host portless", "https://panel.example.com:443", "", "", http.StatusCreated},
		{"Origin portless, Host keeps the port", "https://panel.example.com", "", "panel.example.com:9443", http.StatusCreated},
		{"cross-site Origin", "http://evil.example.com", "", "", http.StatusForbidden},
		{"mismatched Origin port, both carry ports", "http://panel.example.com:9999", "", "panel.example.com:9443", http.StatusForbidden},
		{"null Origin", "null", "", "", http.StatusForbidden},
		{"cross-site fetch", "", "cross-site", "", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			rosterSource := &stubRoster{addResult: roster.MutationResult{User: users.RosterRecord{Email: "a@b.c"}, Sync: roster.Synced}}
			handler := newRosterHandler(rosterSource)
			cookie := login(t, handler, testPassword)

			request := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{"email": "a@b.c"}`))
			request.Host = "panel.example.com"
			if test.host != "" {
				request.Host = test.host
			}
			request.AddCookie(cookie)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.want {
				t.Errorf("status = %d, want %d; body = %s", response.Code, test.want, response.Body)
			}
			if test.want == http.StatusForbidden && rosterSource.called {
				t.Error("a cross-site request never reaches the roster")
			}
		})
	}
}

func TestAddUserRequiresASession(t *testing.T) {
	handler := newRosterHandler(&stubRoster{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{"email": "a@b.c"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", response.Code)
	}
}

// GET /api/v1/users carries the write side alongside the observations: the
// Roster sync state, the per-user apply marks, and the add dialog's inbound
// options (§5, §6).
func TestUsersEndpointCarriesTheRosterWriteState(t *testing.T) {
	rosterSource := &stubRoster{
		sync:      roster.Failed,
		userState: map[string]roster.ApplyState{"alice@example.com": roster.ApplyFailed},
		options:   []roster.InboundOption{{Tag: "vless-vision", Label: "VLESS · Reality · tcp :443"}},
	}
	handler := api.New(
		fixedHostStats{},
		fixedXrayStatus{},
		fixedUsers{snapshot: users.Snapshot{
			CollectedAt: 1_723_800_000,
			Users: []users.User{
				{Email: "alice@example.com"},
				{Email: "bob@example.com"},
			},
		}},
		fixedProfileSources{},
		rosterSource,
		api.OperationalSources{},
		session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)
	cookie := login(t, handler, testPassword)
	response := authedGet(t, handler, cookie)("/api/v1/users")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var body struct {
		RosterSync string `json:"roster_sync"`
		Inbounds   []struct {
			Tag   string `json:"tag"`
			Label string `json:"label"`
		} `json:"inbounds"`
		Users []struct {
			Email      string `json:"email"`
			ApplyState string `json:"apply_state"`
		} `json:"users"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RosterSync != "failed" {
		t.Errorf("roster_sync = %q, want failed", body.RosterSync)
	}
	if len(body.Inbounds) != 1 || body.Inbounds[0].Tag != "vless-vision" {
		t.Errorf("inbounds = %+v", body.Inbounds)
	}
	if len(body.Users) != 2 || body.Users[0].ApplyState != "failed" || body.Users[1].ApplyState != "" {
		t.Errorf("users = %+v, want alice marked failed and bob unmarked", body.Users)
	}
}

// The edit mutation (user-management spec §5): PATCH carries the optional
// fields to the roster, the answer carries the stored record and the Roster
// sync state, conflicts keep their machine-readable reason, and an unknown
// email is a 404.
func TestEditUserAppliesAndAnswersWithTheRecordAndSync(t *testing.T) {
	rosterSource := &stubRoster{editResult: roster.MutationResult{
		User: users.RosterRecord{
			Email: "alice@example.com", ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df",
			Inbounds: []string{"vless-ws"}, CreatedAt: 1_780_000_000, UpdatedAt: 1_780_010_000,
		},
		Sync: roster.Synced,
	}}
	handler := newRosterHandler(rosterSource)
	cookie := login(t, handler, testPassword)

	response := authedPatch(t, handler, cookie, "/api/v1/users/alice@example.com",
		`{"client_id": "2d37a118-4f1b-4dc0-9e3c-3426b07518df", "inbounds": ["vless-ws"]}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body)
	}
	if !rosterSource.editCalled || rosterSource.gotEditEmail != "alice@example.com" ||
		rosterSource.gotEditReq.ClientID != "2d37a118-4f1b-4dc0-9e3c-3426b07518df" ||
		len(rosterSource.gotEditReq.Inbounds) != 1 || rosterSource.gotEditReq.Inbounds[0] != "vless-ws" {
		t.Errorf("the mutation got %q / %+v", rosterSource.gotEditEmail, rosterSource.gotEditReq)
	}
	var body struct {
		User struct {
			ClientID string   `json:"client_id"`
			Inbounds []string `json:"inbounds"`
		} `json:"user"`
		RosterSync string `json:"roster_sync"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.User.ClientID != "2d37a118-4f1b-4dc0-9e3c-3426b07518df" || body.RosterSync != "synced" {
		t.Errorf("body = %+v", body)
	}
}

// Absent body fields mean keep: a PATCH without client_id or inbounds passes
// the empty edit through, and an explicit empty inbounds array detaches all.
func TestEditUserDistinguishesAbsentFromEmptyFields(t *testing.T) {
	rosterSource := &stubRoster{}
	handler := newRosterHandler(rosterSource)
	cookie := login(t, handler, testPassword)

	response := authedPatch(t, handler, cookie, "/api/v1/users/a@b.c", `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if rosterSource.gotEditReq.ClientID != "" || rosterSource.gotEditReq.Inbounds != nil {
		t.Errorf("absent fields must arrive as keep: %+v", rosterSource.gotEditReq)
	}

	response = authedPatch(t, handler, cookie, "/api/v1/users/a@b.c", `{"inbounds": []}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if rosterSource.gotEditReq.Inbounds == nil || len(rosterSource.gotEditReq.Inbounds) != 0 {
		t.Errorf("an explicit empty array must detach all: %+v", rosterSource.gotEditReq)
	}
}

func TestEditUserConflictsAndNotFound(t *testing.T) {
	handler := newRosterHandler(&stubRoster{editErr: &roster.ConflictError{Reason: roster.ReasonClientIDTaken}})
	cookie := login(t, handler, testPassword)
	response := authedPatch(t, handler, cookie, "/api/v1/users/a@b.c", `{"inbounds": []}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "client_id_taken" {
		t.Errorf("reason = %q", body["error"])
	}

	handler = newRosterHandler(&stubRoster{editErr: &roster.NotFoundError{Email: "ghost@example.com"}})
	cookie = login(t, handler, testPassword)
	response = authedPatch(t, handler, cookie, "/api/v1/users/ghost@example.com", `{}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", response.Code, response.Body)
	}
}

// The edit mutation sits behind the same guards as add: session, CSRF, and
// a malformed body never reaches the roster.
func TestEditUserRejectsUnauthenticatedAndMalformedRequests(t *testing.T) {
	handler := newRosterHandler(&stubRoster{})
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/users/a@b.c", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", response.Code)
	}

	cookie := login(t, handler, testPassword)
	request = httptest.NewRequest(http.MethodPatch, "/api/v1/users/a@b.c", strings.NewReader(`{not json`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://evil.example.com")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a cross-site edit", response.Code)
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/v1/users/a@b.c", strings.NewReader(`{not json`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

func authedPatch(t *testing.T, handler http.Handler, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// The disable mutation (user-management spec §5, ADR-0007): a live user's
// disable answers 200 with the Roster sync state; an already-disabled
// email answers 204 — idempotent, nothing to say.
func TestDisableUserStoresAndAnswersWithTheSyncState(t *testing.T) {
	rosterSource := &stubRoster{disableDisabled: true}
	handler := newRosterHandler(rosterSource)
	cookie := login(t, handler, testPassword)

	response := authedPost(t, handler, cookie, "/api/v1/users/alice@example.com/disable", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body)
	}
	if !rosterSource.disableCalled || rosterSource.gotDisableEmail != "alice@example.com" {
		t.Errorf("the mutation got %q", rosterSource.gotDisableEmail)
	}
	var body struct {
		RosterSync string `json:"roster_sync"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RosterSync != "synced" {
		t.Errorf("roster_sync = %q, want synced", body.RosterSync)
	}

	// Idempotent: an already-disabled email answers 204, no body.
	rosterSource.disableCalled = false
	rosterSource.disableDisabled = false
	response = authedPost(t, handler, cookie, "/api/v1/users/alice@example.com/disable", "")
	if response.Code != http.StatusNoContent {
		t.Errorf("repeat status = %d, want 204", response.Code)
	}
	if !rosterSource.disableCalled {
		t.Error("the idempotent disable still reaches the roster")
	}
}

// The enable mutation (ADR-0007): the revived record plus the Roster sync
// state; an unknown email is a 404.
func TestEnableUserRevivesAndAnswersWithTheRecord(t *testing.T) {
	rosterSource := &stubRoster{}
	handler := newRosterHandler(rosterSource)
	cookie := login(t, handler, testPassword)

	response := authedPost(t, handler, cookie, "/api/v1/users/alice@example.com/enable", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body)
	}
	if !rosterSource.enableCalled || rosterSource.gotEnableEmail != "alice@example.com" {
		t.Errorf("the mutation got %q", rosterSource.gotEnableEmail)
	}
	var body struct {
		User       roster.Record `json:"user"`
		RosterSync string        `json:"roster_sync"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.User.Email != "alice@example.com" || body.RosterSync != "synced" {
		t.Errorf("body = %+v, want the revived record and synced", body)
	}

	rosterSource.enableErr = &roster.NotFoundError{Email: "alice@example.com"}
	response = authedPost(t, handler, cookie, "/api/v1/users/alice@example.com/enable", "")
	if response.Code != http.StatusNotFound {
		t.Errorf("unknown email status = %d, want 404", response.Code)
	}
}

// The disable and enable mutations sit behind the same guards as add and
// edit: session and the CSRF Origin check.
func TestDisableAndEnableRejectUnauthenticatedAndCrossSiteRequests(t *testing.T) {
	handler := newRosterHandler(&stubRoster{})
	for _, path := range []string{"/api/v1/users/a@b.c/disable", "/api/v1/users/a@b.c/enable"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, response.Code)
		}

		cookie := login(t, handler, testPassword)
		request = httptest.NewRequest(http.MethodPost, path, nil)
		request.Host = "panel.example.com"
		request.AddCookie(cookie)
		request.Header.Set("Origin", "http://evil.example.com")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403 for a cross-site request", path, response.Code)
		}
	}
}
