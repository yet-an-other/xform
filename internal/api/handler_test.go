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
	"github.com/yet-an-other/xform/internal/hoststats"
	"github.com/yet-an-other/xform/internal/session"
	"github.com/yet-an-other/xform/internal/xraystatus"
)

type fixedHostStats struct {
	stats hoststats.Stats
}

func (f fixedHostStats) Latest(context.Context) (hoststats.Stats, error) {
	return f.stats, nil
}

const testPassword = "test-panel-password"

// newHandler wires the API with a real session manager against stub sources.
func newHandler(snapshots hostStatsSnapshots) http.Handler {
	return api.New(snapshots, fixedXrayStatus{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler())
}

type hostStatsSnapshots interface {
	Latest(context.Context) (hoststats.Stats, error)
}

// fixedXrayStatus serves one canned xray status.
type fixedXrayStatus struct {
	status xraystatus.Status
	err    error
}

func (f fixedXrayStatus) Latest(context.Context) (xraystatus.Status, error) {
	if f.err != nil {
		return xraystatus.Status{}, f.err
	}
	return f.status, nil
}

// login establishes a session through the real login endpoint and returns its cookie.
func login(t *testing.T, handler http.Handler, password string) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/login",
		strings.NewReader(`{"password": "`+password+`"}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("login status = %d, want %d; body = %s", response.Code, http.StatusNoContent, response.Body.String())
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "xform_session" {
			return cookie
		}
	}
	t.Fatal("login did not set an xform_session cookie")
	return nil
}

func TestHealthzIsOpen(t *testing.T) {
	handler := newHandler(fixedHostStats{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want %d without a session", response.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("healthz body = %v, want {\"status\":\"ok\"}", body)
	}
}

func TestProtectedEndpointsRequireSession(t *testing.T) {
	handler := newHandler(fixedHostStats{})

	for _, path := range []string{"/api/v1/server", "/api/v1/xray", "/api/v1/users", "/api/v1/no-such-route"} {
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

func TestDashboardStaysOpen(t *testing.T) {
	handler := newHandler(fixedHostStats{})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	// The stub dashboard 404s; what matters is the request was NOT gated to 401.
	if response.Code == http.StatusUnauthorized {
		t.Fatal("dashboard request without a session was gated; the SPA must load openly")
	}
}

func TestLoginSetsContractCookie(t *testing.T) {
	handler := newHandler(fixedHostStats{})

	cookie := login(t, handler, testPassword)

	if !cookie.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if !cookie.Secure {
		t.Error("session cookie is not Secure (SPEC.md §5: Secure always)")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("session cookie Path = %q, want /", cookie.Path)
	}
	if cookie.MaxAge != int(session.TTL.Seconds()) {
		t.Errorf("session cookie MaxAge = %d, want %d (24h)", cookie.MaxAge, int(session.TTL.Seconds()))
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	handler := newHandler(fixedHostStats{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/login",
		strings.NewReader(`{"password": "wrong"}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-password login status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Fatalf("401 body = %q, want a JSON error", response.Body.String())
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("wrong-password login set cookies %v, want none", cookies)
	}
}

func TestAuthenticatedRequestsSlideTheCookie(t *testing.T) {
	handler := newHandler(fixedHostStats{})
	cookie := login(t, handler, testPassword)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/server", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("authed status = %d, want %d", response.Code, http.StatusOK)
	}
	var refreshed *http.Cookie
	for _, c := range response.Result().Cookies() {
		if c.Name == "xform_session" {
			refreshed = c
		}
	}
	if refreshed == nil {
		t.Fatal("authed request did not re-set the session cookie (sliding expiry)")
	}
	if refreshed.MaxAge != int(session.TTL.Seconds()) {
		t.Errorf("slid cookie MaxAge = %d, want a fresh %d", refreshed.MaxAge, int(session.TTL.Seconds()))
	}
}

func TestLogoutRevokesAndClears(t *testing.T) {
	handler := newHandler(fixedHostStats{})
	cookie := login(t, handler, testPassword)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/logout", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want %d", response.Code, http.StatusNoContent)
	}
	var cleared *http.Cookie
	for _, c := range response.Result().Cookies() {
		if c.Name == "xform_session" {
			cleared = c
		}
	}
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("logout did not clear the cookie (MaxAge<0), got %+v", cleared)
	}

	// The revoked token must no longer authenticate.
	request = httptest.NewRequest(http.MethodGet, "/api/v1/server", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked-token status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	// Logout itself requires a session (decision #5).
	request = httptest.NewRequest(http.MethodPost, "/api/v1/logout", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("sessionless logout status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestXrayEndpointReturnsStatusContract(t *testing.T) {
	version := "26.4.13"
	memBytes, goroutines := uint64(88_080_384), uint32(183)
	usersOnline, uniqueIPs := 3, 4
	want := xraystatus.Status{
		CollectedAt:     1_723_800_000,
		Status:          "running",
		Version:         &version,
		UptimeSeconds:   1_216_800,
		MemBytes:        &memBytes,
		Goroutines:      &goroutines,
		SpeedUpBps:      2_400_000,
		SpeedDownBps:    18_500_000,
		TotalUpBytes:    39_100_000_000,
		TotalDownBytes:  511_400_000_000,
		UsersOnline:     &usersOnline,
		UniqueIPsOnline: &uniqueIPs,
	}
	handler := api.New(fixedHostStats{}, fixedXrayStatus{status: want}, session.NewManager(testPassword, time.Now), http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/xray", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode response fields: %v", err)
	}
	wantFields := map[string]struct{}{
		"collected_at": {}, "status": {}, "version": {}, "uptime_seconds": {},
		"mem_bytes": {}, "goroutines": {},
		"speed_up_bps": {}, "speed_down_bps": {},
		"total_up_bytes": {}, "total_down_bytes": {},
		"users_online": {}, "unique_ips_online": {},
	}
	gotFields := make(map[string]struct{}, len(fields))
	for field := range fields {
		gotFields[field] = struct{}{}
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("response fields = %v, want %v", gotFields, wantFields)
	}

	var got xraystatus.Status
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %#v, want %#v", got, want)
	}

	// A stopped xray reports a null version, not a 5xx (SPEC.md §5: 200 always).
	stopped := api.New(fixedHostStats{}, fixedXrayStatus{status: xraystatus.Status{
		CollectedAt: 1_723_800_000, Status: "stopped",
	}}, session.NewManager(testPassword, time.Now), http.NotFoundHandler())
	request = httptest.NewRequest(http.MethodGet, "/api/v1/xray", nil)
	request.AddCookie(login(t, stopped, testPassword))
	response = httptest.NewRecorder()

	stopped.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("stopped status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), `"version":null`) {
		t.Errorf("stopped body = %s, want a null version", response.Body.String())
	}
}

type failingSessions struct{}

func (failingSessions) Login(string) (string, bool, error) {
	return "", false, errors.New("no entropy")
}
func (failingSessions) Validate(string) bool { return false }
func (failingSessions) Logout(string)        {}

func TestLoginFailureInSessionManagerIs500Not401(t *testing.T) {
	handler := api.New(fixedHostStats{}, fixedXrayStatus{}, failingSessions{}, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/login",
		strings.NewReader(`{"password": "anything"}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d — a session-manager failure is not a password mismatch",
			response.Code, http.StatusInternalServerError)
	}
}

func TestServerEndpointReturnsHostStatsContract(t *testing.T) {
	want := hoststats.Stats{
		CollectedAt:    1_723_800_000,
		CPUPercent:     23.4,
		CPUCores:       4,
		MemUsedBytes:   5_100_273_664,
		MemTotalBytes:  8_589_934_592,
		DiskPath:       "/",
		DiskUsedBytes:  90_194_313_216,
		DiskTotalBytes: 171_798_691_840,
		UptimeSeconds:  1_987_200,
		LoadAvg:        [3]float64{0.42, 0.38, 0.31},
	}
	handler := newHandler(fixedHostStats{stats: want})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/server", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	body := response.Body.Bytes()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode response fields: %v", err)
	}
	wantFields := map[string]struct{}{
		"collected_at": {}, "cpu_percent": {}, "cpu_cores": {},
		"mem_used_bytes": {}, "mem_total_bytes": {}, "disk_path": {},
		"disk_used_bytes": {}, "disk_total_bytes": {}, "uptime_seconds": {},
		"load_avg": {},
	}
	gotFields := make(map[string]struct{}, len(fields))
	for field := range fields {
		gotFields[field] = struct{}{}
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("response fields = %v, want %v", gotFields, wantFields)
	}

	var got hoststats.Stats
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}
