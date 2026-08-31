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
	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xraystatus"
)

type fixedHostStats struct {
	stats hoststats.Stats
}

func (f fixedHostStats) Latest(context.Context) (hoststats.Stats, error) {
	return f.stats, nil
}

const testPassword = "test-panel-password"

// testPanelInfo is the panel identity handed to every test handler.
var testPanelInfo = api.PanelInfo{
	Version:         "v0.0.0-test",
	XrayAPIEndpoint: "127.0.0.1:8080",
	Uptime:          func() int64 { return 0 },
}

// newHandler wires the API with a real session manager against stub sources.
func newHandler(snapshots hostStatsSnapshots) http.Handler {
	return api.New(snapshots, fixedXrayStatus{}, fixedUsers{}, fixedProfileSources{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo)
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

// fixedUsers serves one canned users snapshot.
type fixedUsers struct {
	snapshot users.Snapshot
	err      error
}

func (f fixedUsers) Latest(context.Context) (users.Snapshot, error) {
	return f.snapshot, f.err
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

func TestUsersEndpointReturnsContractPayload(t *testing.T) {
	lastSeen := int64(1_723_799_995)
	protocol, security := "VLESS", "XTLS-Reality"
	clientID := "1e7f6c2a-9b3d-4f8a-9c1e-2d5a7b8c9d0e"
	handler := api.New(fixedHostStats{}, fixedXrayStatus{}, fixedUsers{snapshot: users.Snapshot{
		CollectedAt: 1_723_800_000,
		Users: []users.User{{
			Email:          "alice@example.com",
			Protocol:       &protocol,
			Security:       &security,
			ClientID:       &clientID,
			Inbounds:       []string{"vless-vision", "vless-xhttp"},
			UpBytesTotal:   12_400_000_000,
			DownBytesTotal: 148_200_000_000,
			Online:         true,
			IPs:            []string{"203.0.113.10"},
			SpeedUpBps:     512_000,
			SpeedDownBps:   3_800_000,
			LastSeen:       &lastSeen,
		}},
	}}, fixedProfileSources{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}

	var payload struct {
		CollectedAt int64 `json:"collected_at"`
		Stale       bool  `json:"stale"`
		Users       []struct {
			Email          string   `json:"email"`
			Protocol       *string  `json:"protocol"`
			Security       *string  `json:"security"`
			ClientID       *string  `json:"client_id"`
			Inbounds       []string `json:"inbounds"`
			UpBytesTotal   uint64   `json:"up_bytes_total"`
			DownBytesTotal uint64   `json:"down_bytes_total"`
			Online         bool     `json:"online"`
			IPs            []string `json:"ips"`
			SpeedUpBps     uint64   `json:"speed_up_bps"`
			SpeedDownBps   uint64   `json:"speed_down_bps"`
			LastSeen       *int64   `json:"last_seen"`
			Gone           bool     `json:"gone"`
		} `json:"users"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.CollectedAt != 1_723_800_000 || payload.Stale {
		t.Errorf("collected_at = %d, stale = %v; want 1723800000, false", payload.CollectedAt, payload.Stale)
	}
	if len(payload.Users) != 1 {
		t.Fatalf("users = %d, want 1", len(payload.Users))
	}
	alice := payload.Users[0]
	if alice.Email != "alice@example.com" || alice.UpBytesTotal != 12_400_000_000 || alice.DownBytesTotal != 148_200_000_000 {
		t.Errorf("alice = %+v", alice)
	}
	if alice.SpeedUpBps != 512_000 || alice.SpeedDownBps != 3_800_000 {
		t.Errorf("alice speeds = %d/%d", alice.SpeedUpBps, alice.SpeedDownBps)
	}
	if alice.Protocol == nil || *alice.Protocol != "VLESS" || alice.Security == nil || *alice.Security != "XTLS-Reality" {
		t.Errorf("alice protocol/security = %v/%v", alice.Protocol, alice.Security)
	}
	if alice.ClientID == nil || *alice.ClientID != clientID {
		t.Errorf("alice client_id = %v, want the roster store's %s", alice.ClientID, clientID)
	}
	if len(alice.Inbounds) != 2 || alice.Inbounds[0] != "vless-vision" || alice.Inbounds[1] != "vless-xhttp" {
		t.Errorf("alice inbounds = %v, want her two adopted attachments", alice.Inbounds)
	}
	if !alice.Online || len(alice.IPs) != 1 || alice.LastSeen == nil || *alice.LastSeen != 1_723_799_995 || alice.Gone {
		t.Errorf("alice presence = online %v, ips %v, last_seen %v, gone %v", alice.Online, alice.IPs, alice.LastSeen, alice.Gone)
	}
}

// A stale snapshot (xray unreachable) is data, not a 5xx (SPEC.md §5).
func TestUsersEndpointServesStaleSnapshot(t *testing.T) {
	handler := api.New(fixedHostStats{}, fixedXrayStatus{}, fixedUsers{snapshot: users.Snapshot{
		CollectedAt: 1_723_799_000,
		Stale:       true,
		Users:       []users.User{},
	}}, fixedProfileSources{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var payload struct {
		Stale bool         `json:"stale"`
		Users []users.User `json:"users"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Stale {
		t.Error("stale = false, want true")
	}
	if payload.Users == nil {
		t.Error("users = null, want [] — the contract is an array")
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
	handler := api.New(fixedHostStats{}, fixedXrayStatus{status: want}, fixedUsers{}, fixedProfileSources{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo)
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
		"users_online": {}, "unique_ips_online": {}, "api_endpoint": {},
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
	}}, fixedUsers{}, fixedProfileSources{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo)
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

func TestXrayEndpointNamesTheConfiguredAPIEndpoint(t *testing.T) {
	handler := api.New(fixedHostStats{}, fixedXrayStatus{}, fixedUsers{}, fixedProfileSources{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/xray", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if !strings.Contains(response.Body.String(), `"api_endpoint":"127.0.0.1:8080"`) {
		t.Errorf("body = %s, want the configured gRPC endpoint named", response.Body.String())
	}
}

// TestPanelEndpointReturnsTheReleaseVersion proves the panel identity
// response: version plus current uptime, never cacheable (SPEC §5:
// every endpoint returns Cache-Control: no-store).
func TestPanelEndpointReturnsTheReleaseVersion(t *testing.T) {
	handler := api.New(fixedHostStats{}, fixedXrayStatus{}, fixedUsers{}, fixedProfileSources{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/panel", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Version != testPanelInfo.Version {
		t.Errorf("version = %q, want %q", payload.Version, testPanelInfo.Version)
	}
}

// TestPanelEndpointReportsCurrentUptime proves the endpoint evaluates the
// uptime source per request (SPEC §5): the dashboard polls it every
// five seconds, so each response carries the value at request time.
func TestPanelEndpointReportsCurrentUptime(t *testing.T) {
	elapsed := 4_831
	panel := testPanelInfo
	panel.Uptime = func() int64 { return int64(elapsed) }
	handler := api.New(fixedHostStats{}, fixedXrayStatus{}, fixedUsers{}, fixedProfileSources{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), panel)
	cookie := login(t, handler, testPassword)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/panel", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var payload struct {
		UptimeSeconds int64 `json:"uptime_seconds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.UptimeSeconds != 4_831 {
		t.Errorf("uptime_seconds = %d, want 4831", payload.UptimeSeconds)
	}

	// Time passes; the next poll must observe it.
	elapsed = 4_836
	request = httptest.NewRequest(http.MethodGet, "/api/v1/panel", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if payload.UptimeSeconds != 4_836 {
		t.Errorf("second uptime_seconds = %d, want 4836", payload.UptimeSeconds)
	}
}

// TestPanelUptimeMeasuresWholeMonotonicSeconds is the controlled-clock
// proof of OP-1: uptime counts elapsed whole seconds from one monotonic
// start, and a newly started process reads from its own start again.
func TestPanelUptimeMeasuresWholeMonotonicSeconds(t *testing.T) {
	start := time.Unix(1_723_800_000, 0)
	now := start
	clock := func() time.Time { return now }

	uptime := api.UptimeSeconds(start, clock)

	// 30.9s in: whole seconds only — no rounding up.
	now = start.Add(30_900 * time.Millisecond)
	if got := uptime(); got != 30 {
		t.Errorf("uptime after 30.9s = %d, want 30", got)
	}
	// Elapsed whole seconds increase one for one.
	now = start.Add(91_200 * time.Millisecond)
	if got := uptime(); got != 91 {
		t.Errorf("uptime after 91.2s = %d, want 91", got)
	}

	// A new process starts again from its own elapsed time.
	newStart := start.Add(500 * time.Second)
	newProcessUptime := api.UptimeSeconds(newStart, clock)
	now = newStart.Add(5_400 * time.Millisecond)
	if got := newProcessUptime(); got != 5 {
		t.Errorf("new process uptime = %d, want 5", got)
	}
	if got := uptime(); got != 505 {
		t.Errorf("first process uptime = %d, want 505", got)
	}
}

type failingSessions struct{}

func (failingSessions) Login(string) (string, bool, error) {
	return "", false, errors.New("no entropy")
}
func (failingSessions) Validate(string) bool { return false }
func (failingSessions) Logout(string)        {}

func TestLoginFailureInSessionManagerIs500Not401(t *testing.T) {
	handler := api.New(fixedHostStats{}, fixedXrayStatus{}, fixedUsers{}, fixedProfileSources{}, api.OperationalSources{}, failingSessions{}, http.NotFoundHandler(), testPanelInfo)
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
