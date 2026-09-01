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

// stubRoster is the roster seam for handler tests: a scripted Add plus the
// state the users endpoint merges in.
type stubRoster struct {
	addResult roster.AddResult
	addErr    error
	gotEmail  string
	gotID     string
	gotTags   []string
	called    bool

	sync      roster.SyncState
	userState map[string]string
	options   []roster.InboundOption
}

func (s *stubRoster) Add(_ context.Context, email, clientID string, inbounds []string) (roster.AddResult, error) {
	s.called = true
	s.gotEmail, s.gotID, s.gotTags = email, clientID, inbounds
	return s.addResult, s.addErr
}

func (s *stubRoster) Sync() roster.SyncState {
	if s.sync == "" {
		return roster.Synced
	}
	return s.sync
}

func (s *stubRoster) UserStates() map[string]string { return s.userState }

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
	rosterSource := &stubRoster{addResult: roster.AddResult{
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
		name     string
		origin   string
		fetchSite string
		want     int
	}{
		{"no headers (same-origin browser form, curl)", "", "", http.StatusCreated},
		{"same-origin Origin", "http://panel.example.com", "", http.StatusCreated},
		{"same-site fetch", "", "same-origin", http.StatusCreated},
		{"cross-site Origin", "http://evil.example.com", "", http.StatusForbidden},
		{"mismatched Origin port", "http://panel.example.com:9999", "", http.StatusForbidden},
		{"null Origin", "null", "", http.StatusForbidden},
		{"cross-site fetch", "", "cross-site", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			rosterSource := &stubRoster{addResult: roster.AddResult{User: users.RosterRecord{Email: "a@b.c"}, Sync: roster.Synced}}
			handler := newRosterHandler(rosterSource)
			cookie := login(t, handler, testPassword)

			request := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{"email": "a@b.c"}`))
			request.Host = "panel.example.com"
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
		userState: map[string]string{"alice@example.com": roster.ApplyFailed},
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
