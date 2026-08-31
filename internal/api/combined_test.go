package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/api"
	"github.com/yet-an-other/xform/internal/configsnapshot"
	"github.com/yet-an-other/xform/internal/journal"
	"github.com/yet-an-other/xform/internal/profiles"
	"github.com/yet-an-other/xform/internal/session"
	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
	"github.com/yet-an-other/xform/internal/xraystatus"
)

// TestCombinedSourcesKeepIndependentFreshnessAndErrors puts every source into
// a different state at once (SPEC §8) and asks each endpoint to report only its
// own truth: stale observations, a stopped xray, both profile sources stale,
// a failing Log snapshot, and a Config snapshot that still reads.
func TestCombinedSourcesKeepIndependentFreshnessAndErrors(t *testing.T) {
	view, err := xrayconfig.ParseView([]byte(`{
		"inbounds": [{
			"tag": "primary", "protocol": "vless",
			"settings": {"decryption": "none", "clients": [{"email": "alice@example.com", "id": "1d37a118-4f1b-4dc0-9e3c-3426b07518df"}]},
			"streamSettings": {"network": "raw", "security": "tls"}
		}]
	}`))
	if err != nil {
		t.Fatalf("parse xray view: %v", err)
	}
	advertisedView, err := advertisements.Parse([]byte(`{
		"version": 1,
		"advertisements": [{
			"inbound_tag": "primary", "name": "Primary", "topology": "direct",
			"host": "edge.example.com", "port": 443,
			"transport": {"type": "tcp"}, "security": {"type": "tls"}
		}]
	}`))
	if err != nil {
		t.Fatalf("parse Advertised connection settings: %v", err)
	}
	xrayLoadedAt := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	advertisementsLoadedAt := xrayLoadedAt.Add(-time.Minute)
	const malformedText = "{ this is not json\n"

	handler := api.New(
		fixedHostStats{},
		fixedXrayStatus{status: xraystatus.Status{CollectedAt: 1_723_800_000, Status: "stopped"}},
		fixedUsers{snapshot: users.Snapshot{
			CollectedAt: 1_723_800_000, Stale: true,
			Users: []users.User{{Email: "alice@example.com", IPs: []string{}}},
		}},
		fixedProfileSources{sources: profiles.Sources{
			XrayView: view, XrayAvailable: true, XrayLoadedAt: xrayLoadedAt, XrayStale: true,
			XrayError:          &xrayconfig.SourceError{Reason: xrayconfig.ParseFailed, Message: "safe xray error"},
			AdvertisementsView: advertisedView, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
			AdvertisementsLoadedAt: advertisementsLoadedAt, AdvertisementsStale: true,
			AdvertisementsError: &advertisements.SourceError{Reason: advertisements.ReadFailed, Message: "safe advertisement error"},
		}},
		api.OperationalSources{
			Logs: &stubLogs{err: &journal.Error{Reason: journal.ReasonAccessDenied}},
			Config: &stubConfig{snapshot: configsnapshot.Snapshot{
				CapturedAt: time.Unix(1_723_800_000, 0),
				Path:       "/usr/local/etc/xray/config.json",
				SizeBytes:  int64(len(malformedText)),
				Text:       malformedText,
			}},
		},
		session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)
	cookie := login(t, handler, testPassword)
	get := authedGet(t, handler, cookie)

	// Host monitoring stays live whatever xray or a viewer does.
	if response := get("/api/v1/server"); response.Code != http.StatusOK {
		t.Errorf("GET /api/v1/server: status = %d, want 200", response.Code)
	}
	if response := get("/api/v1/panel"); response.Code != http.StatusOK {
		t.Errorf("GET /api/v1/panel: status = %d, want 200", response.Code)
	}

	// xray reports its own stopped status; nothing borrows it.
	xrayResponse := get("/api/v1/xray")
	if body := decodeObject(t, xrayResponse); body["status"] != "stopped" {
		t.Errorf("GET /api/v1/xray: status = %v, want stopped", body["status"])
	}

	// User observations are stale on their own mark.
	usersResponse := get("/api/v1/users")
	if body := decodeObject(t, usersResponse); body["stale"] != true {
		t.Errorf("GET /api/v1/users: stale = %v, want true", body["stale"])
	}

	// User detail: stale observations ride alongside stale-but-available
	// last-valid profiles, with both source errors in contract order.
	detailResponse := get("/api/v1/users/alice%40example.com")
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("GET user detail: status = %d, want 200; body = %s", detailResponse.Code, detailResponse.Body)
	}
	var detail struct {
		Stale    bool `json:"stale"`
		Profiles struct {
			State  profiles.State `json:"state"`
			Stale  bool           `json:"stale"`
			Errors []struct {
				Source string `json:"source"`
			} `json:"errors"`
			Items []struct {
				Status profiles.Status `json:"status"`
				URI    string          `json:"uri"`
			} `json:"items"`
		} `json:"connection_profiles"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode user detail: %v", err)
	}
	if !detail.Stale {
		t.Error("user detail: top-level stale = false, want the stale observation mark")
	}
	if detail.Profiles.State != profiles.StateReady || !detail.Profiles.Stale {
		t.Errorf("profiles = %q/stale %t, want ready and stale", detail.Profiles.State, detail.Profiles.Stale)
	}
	if len(detail.Profiles.Errors) != 2 ||
		detail.Profiles.Errors[0].Source != "xray_config" ||
		detail.Profiles.Errors[1].Source != "advertisements" {
		t.Errorf("profile errors = %+v, want xray_config then advertisements", detail.Profiles.Errors)
	}
	if len(detail.Profiles.Items) != 1 || detail.Profiles.Items[0].Status != profiles.StatusAvailable || detail.Profiles.Items[0].URI == "" {
		t.Errorf("profile items = %+v, want the last-valid profile still available and copyable", detail.Profiles.Items)
	}

	// The Log snapshot fails with its own reason, and only its own.
	for _, path := range []string{"/api/v1/logs/panel", "/api/v1/logs/xray"} {
		response := get(path)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s: status = %d, want 503", path, response.Code)
		}
		if body := decodeObject(t, response); body["reason"] != string(journal.ReasonAccessDenied) {
			t.Errorf("GET %s: reason = %v, want %s", path, body["reason"], journal.ReasonAccessDenied)
		}
	}

	// The Config snapshot reads the current file, malformed or not.
	configResponse := get("/api/v1/xray/config")
	if configResponse.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/xray/config: status = %d, want 200", configResponse.Code)
	}
	if body := decodeObject(t, configResponse); body["text"] != malformedText {
		t.Errorf("config text = %q, want the exact malformed text %q", body["text"], malformedText)
	}
}

// watchedProfileSources adapts a real xray config watcher to the profile
// sources seam, the way main wires it, with no advertisement source at all.
type watchedProfileSources struct {
	xray *xrayconfig.Watcher
}

func (w watchedProfileSources) Current() profiles.Sources {
	return profiles.SourcesFromSnapshots(w.xray.Snapshot(), advertisements.Snapshot{})
}

// TestMalformedCurrentConfigFileKeepsLastValidParsedState runs the
// current-file-vs-last-valid-parse rule (SPEC §8) through the real watched
// source and Config snapshot reader over one file: the current text turns
// malformed, the Config snapshot shows it unchanged, and the Roster and
// profiles keep serving the last valid parse, flagged stale.
func TestMalformedCurrentConfigFileKeepsLastValidParsedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	valid := `{
		"inbounds": [{
			"tag": "primary", "protocol": "vless",
			"settings": {"decryption": "none", "clients": [{"email": "alice@example.com", "id": "1d37a118-4f1b-4dc0-9e3c-3426b07518df"}]},
			"streamSettings": {"network": "raw", "security": "tls"}
		}]
	}`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatalf("write valid config: %v", err)
	}

	watcher := xrayconfig.NewWatcher(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcher.Start(ctx)
	waitForSnapshot(t, "the first valid parse", func() bool { return watcher.Snapshot().Available() })

	handler := api.New(
		fixedHostStats{}, fixedXrayStatus{},
		fixedUsers{snapshot: users.Snapshot{Users: []users.User{{Email: "alice@example.com", IPs: []string{}}}}},
		watchedProfileSources{xray: watcher},
		api.OperationalSources{Logs: &stubLogs{}, Config: configsnapshot.NewReader(path)},
		session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)
	cookie := login(t, handler, testPassword)
	get := authedGet(t, handler, cookie)
	profilesOf := func(t *testing.T) (state profiles.State, stale bool, reasons []profiles.Reason) {
		t.Helper()
		response := get("/api/v1/users/alice%40example.com")
		if response.Code != http.StatusOK {
			t.Fatalf("GET user detail: status = %d, want 200; body = %s", response.Code, response.Body)
		}
		var detail struct {
			Profiles struct {
				State profiles.State `json:"state"`
				Stale bool           `json:"stale"`
				Items []struct {
					Reason profiles.Reason `json:"reason"`
				} `json:"items"`
			} `json:"connection_profiles"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatalf("decode user detail: %v", err)
		}
		reasons = make([]profiles.Reason, len(detail.Profiles.Items))
		for index, item := range detail.Profiles.Items {
			reasons[index] = item.Reason
		}
		return detail.Profiles.State, detail.Profiles.Stale, reasons
	}

	// Valid file: profiles come from the current parse; no advertisement path
	// is configured, so the one matching inbound reports advertisement_missing.
	state, stale, reasons := profilesOf(t)
	if state != profiles.StateReady || stale || len(reasons) != 1 || reasons[0] != profiles.ReasonAdvertisementMissing {
		t.Fatalf("fresh profiles = %q/stale %t/%v, want ready, current, one advertisement_missing", state, stale, reasons)
	}

	// The file turns malformed. The Config snapshot reads what is really there.
	malformed := "{ not json at all\n"
	if err := os.WriteFile(path, []byte(malformed), 0o600); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}
	waitForSnapshot(t, "the failed reload to mark the source stale", func() bool { return watcher.Snapshot().Stale })

	configResponse := get("/api/v1/xray/config")
	if configResponse.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/xray/config: status = %d, want 200; body = %s", configResponse.Code, configResponse.Body)
	}
	if body := decodeObject(t, configResponse); body["text"] != malformed {
		t.Errorf("config text = %q, want the exact malformed bytes %q", body["text"], malformed)
	}

	// The Roster and profiles keep the last valid parse, flagged stale with a
	// safe source error — they do not borrow the Config snapshot's truth.
	state, stale, reasons = profilesOf(t)
	if state != profiles.StateReady || !stale || len(reasons) != 1 || reasons[0] != profiles.ReasonAdvertisementMissing {
		t.Errorf("stale profiles = %q/stale %t/%v, want ready, stale, the last-valid advertisement_missing", state, stale, reasons)
	}
	roster, _ := watcher.Roster()
	if _, ok := roster.Labels["alice@example.com"]; !ok {
		t.Errorf("roster = %v, want alice retained from the last valid parse", roster.Labels)
	}

	// The file heals: the source turns current again, and the Config snapshot
	// keeps reading whatever the file holds now.
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatalf("restore valid config: %v", err)
	}
	waitForSnapshot(t, "the healed reload to clear the stale mark", func() bool {
		snapshot := watcher.Snapshot()
		return snapshot.Available() && !snapshot.Stale
	})
	state, stale, _ = profilesOf(t)
	if state != profiles.StateReady || stale {
		t.Errorf("healed profiles = %q/stale %t, want ready and current again", state, stale)
	}
}

// authedGet performs GETs with one established Session, so a test can hold
// one login across every endpoint it checks.
func authedGet(t *testing.T, handler http.Handler, cookie *http.Cookie) func(string) *httptest.ResponseRecorder {
	return func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
}

// waitForSnapshot polls until the watched source reaches the expected state —
// a watcher is asynchronous by design (fsnotify + debounce).
func waitForSnapshot(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSubpathDeploymentPassesSessionAndEncodedIdentity exercises the
// documented subpath deployment (SPEC §9): login, the Session cookie, and
// one-decode User identity all work when the Panel hangs under a
// prefix-stripped subpath.
func TestSubpathDeploymentPassesSessionAndEncodedIdentity(t *testing.T) {
	view, err := xrayconfig.ParseView([]byte(`{"inbounds": []}`))
	if err != nil {
		t.Fatalf("parse xray view: %v", err)
	}
	handler := api.New(
		fixedHostStats{}, fixedXrayStatus{},
		fixedUsers{snapshot: users.Snapshot{Users: []users.User{{Email: "café/x@example.com", IPs: []string{}}}}},
		fixedProfileSources{sources: profiles.Sources{XrayView: view, XrayAvailable: true}},
		api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)
	// The documented shape: the proxy strips the subpath prefix (ADR-0001).
	proxied := http.StripPrefix("/xform", handler)

	loginRequest := httptest.NewRequest(http.MethodPost, "/xform/api/v1/login",
		strings.NewReader(`{"password": "`+testPassword+`"}`))
	loginResponse := httptest.NewRecorder()
	proxied.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusNoContent {
		t.Fatalf("subpath login status = %d, want 204; body = %s", loginResponse.Code, loginResponse.Body)
	}
	var cookie *http.Cookie
	for _, candidate := range loginResponse.Result().Cookies() {
		if candidate.Name == "xform_session" {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatal("subpath login did not set an xform_session cookie")
	}
	if cookie.Path != "/" {
		t.Fatalf("session cookie Path = %q, want / so one Session covers the whole origin", cookie.Path)
	}

	request := httptest.NewRequest(http.MethodGet, "/xform/api/v1/users/caf%C3%A9%2Fx%40example.com", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	proxied.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("subpath user detail: status = %d, want 200; body = %s", response.Code, response.Body)
	}
	var detail struct {
		User users.User `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode user detail: %v", err)
	}
	if detail.User.Email != "café/x@example.com" {
		t.Errorf("email = %q, want the one-decode identity %q", detail.User.Email, "café/x@example.com")
	}
	if !strings.Contains(response.Header().Get("Set-Cookie"), "xform_session=") {
		t.Error("subpath response did not slide the session cookie")
	}
}
