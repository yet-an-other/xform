package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/api"
	"github.com/yet-an-other/xform/internal/profiles"
	"github.com/yet-an-other/xform/internal/session"
	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

type fixedProfileSources struct {
	sources profiles.Sources
}

func (f fixedProfileSources) Current() profiles.Sources {
	return f.sources
}

func TestUserDetailReturnsKnownUserAndProfileState(t *testing.T) {
	protocol := "VLESS"
	view, err := xrayconfig.ParseView([]byte(`{"inbounds": []}`))
	if err != nil {
		t.Fatalf("parse xray view: %v", err)
	}
	loadedAt := time.Date(2026, time.August, 31, 10, 30, 0, 0, time.UTC)
	handler := api.New(
		fixedHostStats{},
		fixedXrayStatus{},
		fixedUsers{snapshot: users.Snapshot{
			CollectedAt: 1_723_800_000,
			Users: []users.User{{
				Email: "alice@example.com", Protocol: &protocol, IPs: []string{},
			}},
		}},
		fixedProfileSources{sources: profiles.Sources{
			XrayView: view, XrayAvailable: true, XrayLoadedAt: loadedAt,
		}},
		&stubRoster{},
		api.OperationalSources{},
		session.NewManager(testPassword, time.Now),
		http.NotFoundHandler(),
		testPanelInfo,
	)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/alice%40example.com", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var payload struct {
		CollectedAt int64      `json:"collected_at"`
		Stale       bool       `json:"stale"`
		User        users.User `json:"user"`
		Profiles    struct {
			State    profiles.State `json:"state"`
			LoadedAt *int64         `json:"loaded_at"`
			Stale    bool           `json:"stale"`
			Errors   []any          `json:"errors"`
			Items    []any          `json:"items"`
		} `json:"connection_profiles"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.CollectedAt != 1_723_800_000 || payload.Stale {
		t.Errorf("observation freshness = %d/%t", payload.CollectedAt, payload.Stale)
	}
	if payload.User.Email != "alice@example.com" || payload.User.Protocol == nil || *payload.User.Protocol != protocol {
		t.Errorf("User = %+v", payload.User)
	}
	if payload.Profiles.State != profiles.StateNoMatchingInbound || payload.Profiles.LoadedAt == nil || *payload.Profiles.LoadedAt != loadedAt.Unix() {
		t.Errorf("Connection profiles = %+v", payload.Profiles)
	}
	if payload.Profiles.Errors == nil || payload.Profiles.Items == nil {
		t.Errorf("Connection profile arrays must be [], got errors=%v items=%v", payload.Profiles.Errors, payload.Profiles.Items)
	}
}

func TestUserDetailPreservesUsersEndpointNullabilityAndOmissions(t *testing.T) {
	view, err := xrayconfig.ParseView([]byte(`{"inbounds": []}`))
	if err != nil {
		t.Fatalf("parse xray view: %v", err)
	}
	handler := api.New(
		fixedHostStats{}, fixedXrayStatus{},
		fixedUsers{snapshot: users.Snapshot{Users: []users.User{{Email: "alice@example.com", FirstSeen: 123}}}},
		fixedProfileSources{sources: profiles.Sources{XrayView: view, XrayAvailable: true}},
		&stubRoster{},
		api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/alice%40example.com", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var payload struct {
		User map[string]json.RawMessage `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, field := range []string{"protocol", "security", "last_seen"} {
		value, ok := payload.User[field]
		if !ok || string(value) != "null" {
			t.Errorf("%s = %s (present %t), want null", field, value, ok)
		}
	}
	if _, ok := payload.User["ip_countries"]; ok {
		t.Error("ip_countries is present without geoip data")
	}
	if _, ok := payload.User["FirstSeen"]; ok {
		t.Error("panel-internal FirstSeen leaked into User JSON")
	}
	if _, ok := payload.User["first_seen"]; ok {
		t.Error("panel-internal first_seen leaked into User JSON")
	}
}

func TestUserDetailPreservesEncodedEmailIdentityAtRootAndSubpath(t *testing.T) {
	view, err := xrayconfig.ParseView([]byte(`{"inbounds": []}`))
	if err != nil {
		t.Fatalf("parse xray view: %v", err)
	}
	handler := api.New(
		fixedHostStats{}, fixedXrayStatus{},
		fixedUsers{snapshot: users.Snapshot{Users: []users.User{
			{Email: "slash/percent%café@example.com", IPs: []string{}},
			{Email: "once%2F@example.com", IPs: []string{}},
		}}},
		fixedProfileSources{sources: profiles.Sources{XrayView: view, XrayAvailable: true}},
		&stubRoster{},
		api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)
	cookie := login(t, handler, testPassword)

	for _, test := range []struct {
		name      string
		handler   http.Handler
		path      string
		wantEmail string
	}{
		{
			name: "root encoded slash percent and Unicode", handler: handler,
			path:      "/api/v1/users/slash%2Fpercent%25caf%C3%A9%40example.com",
			wantEmail: "slash/percent%café@example.com",
		},
		{
			name: "decoded once", handler: handler,
			path:      "/api/v1/users/once%252F%40example.com",
			wantEmail: "once%2F@example.com",
		},
		{
			name: "subpath proxy", handler: http.StripPrefix("/xform", handler),
			path:      "/xform/api/v1/users/slash%2Fpercent%25caf%C3%A9%40example.com",
			wantEmail: "slash/percent%café@example.com",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			test.handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
			}
			var payload struct {
				User users.User `json:"user"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.User.Email != test.wantEmail {
				t.Errorf("email = %q, want %q", payload.User.Email, test.wantEmail)
			}
		})
	}
}

func TestUserDetailRequiresSessionWithoutCachingOrCORS(t *testing.T) {
	handler := api.New(
		fixedHostStats{}, fixedXrayStatus{}, fixedUsers{}, fixedProfileSources{},
		&stubRoster{},
		api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/alice%40example.com", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cacheControl)
	}
	if cors := response.Header().Get("Access-Control-Allow-Origin"); cors != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want no CORS header", cors)
	}
}

func TestUserDetailUses404OnlyForUnknownUsersAnd500ForInternalFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		users      fixedUsers
		wantStatus int
	}{
		{name: "unknown User", users: fixedUsers{snapshot: users.Snapshot{Users: []users.User{}}}, wantStatus: http.StatusNotFound},
		{name: "snapshot failure", users: fixedUsers{err: context.Canceled}, wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := api.New(
				fixedHostStats{}, fixedXrayStatus{}, test.users, fixedProfileSources{},
				&stubRoster{},
				api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
			)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/users/unknown%40example.com", nil)
			request.AddCookie(login(t, handler, testPassword))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cacheControl)
			}
			if cors := response.Header().Get("Access-Control-Allow-Origin"); cors != "" {
				t.Errorf("Access-Control-Allow-Origin = %q, want no CORS header", cors)
			}
		})
	}
}

func TestUserDetailRejectsMalformedPercentEncoding(t *testing.T) {
	handler := api.New(
		fixedHostStats{}, fixedXrayStatus{}, fixedUsers{snapshot: users.Snapshot{Users: []users.User{}}},
		fixedProfileSources{}, &stubRoster{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/placeholder", nil)
	// net/http rejects a malformed request target before dispatch. Mutate the
	// parsed request to exercise the API's response when an upstream passes one.
	request.URL.Path = "/api/v1/users/alice%ZZexample.com"
	request.URL.RawPath = ""
	request.RequestURI = "/api/v1/users/alice%ZZexample.com"
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["error"] != "invalid_request" {
		t.Errorf("error = %q, want invalid_request", payload["error"])
	}
}

func TestUserDetailKeepsObservationAndProfileFreshnessIndependent(t *testing.T) {
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

	for _, test := range []struct {
		name             string
		observationStale bool
		sources          profiles.Sources
		wantProfileStale bool
		wantLoadedAt     int64
		wantErrors       []string
	}{
		{
			name: "current observations and stale profile sources",
			sources: profiles.Sources{
				XrayView: view, XrayAvailable: true, XrayLoadedAt: xrayLoadedAt, XrayStale: true,
				XrayError:          &xrayconfig.SourceError{Reason: xrayconfig.ParseFailed, Message: "safe xray error"},
				AdvertisementsView: advertisedView, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
				AdvertisementsLoadedAt: advertisementsLoadedAt, AdvertisementsStale: true,
				AdvertisementsError: &advertisements.SourceError{Reason: advertisements.UnsupportedVersion, Message: "safe advertisement error"},
			},
			wantProfileStale: true,
			wantLoadedAt:     advertisementsLoadedAt.Unix(),
			wantErrors:       []string{"xray_config", "advertisements"},
		},
		{
			name:             "stale observations and current profile sources",
			observationStale: true,
			sources: profiles.Sources{
				XrayView: view, XrayAvailable: true, XrayLoadedAt: xrayLoadedAt,
				AdvertisementsView: advertisedView, AdvertisementsConfigured: true,
				AdvertisementsAvailable: true, AdvertisementsLoadedAt: xrayLoadedAt,
			},
			wantLoadedAt: xrayLoadedAt.Unix(),
			wantErrors:   []string{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := api.New(
				fixedHostStats{}, fixedXrayStatus{},
				fixedUsers{snapshot: users.Snapshot{Stale: test.observationStale, Users: []users.User{{Email: "alice@example.com", IPs: []string{}}}}},
				fixedProfileSources{sources: test.sources}, &stubRoster{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now),
				http.NotFoundHandler(), testPanelInfo,
			)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/users/alice%40example.com", nil)
			request.AddCookie(login(t, handler, testPassword))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			var payload struct {
				Stale    bool `json:"stale"`
				Profiles struct {
					LoadedAt *int64 `json:"loaded_at"`
					Stale    bool   `json:"stale"`
					Errors   []struct {
						Source  string `json:"source"`
						Reason  string `json:"reason"`
						Message string `json:"message"`
					} `json:"errors"`
					Items []struct {
						Status profiles.Status `json:"status"`
						URI    string          `json:"uri"`
					} `json:"items"`
				} `json:"connection_profiles"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Stale != test.observationStale || payload.Profiles.Stale != test.wantProfileStale {
				t.Errorf("freshness = observations %t, profiles %t", payload.Stale, payload.Profiles.Stale)
			}
			if payload.Profiles.LoadedAt == nil || *payload.Profiles.LoadedAt != test.wantLoadedAt {
				t.Errorf("loaded_at = %v, want %d", payload.Profiles.LoadedAt, test.wantLoadedAt)
			}
			gotSources := make([]string, len(payload.Profiles.Errors))
			for index, sourceError := range payload.Profiles.Errors {
				gotSources[index] = sourceError.Source
				if sourceError.Reason == "" || sourceError.Message == "" {
					t.Errorf("source error = %+v, want safe reason and message", sourceError)
				}
			}
			if len(gotSources) != len(test.wantErrors) {
				t.Fatalf("error sources = %v, want %v", gotSources, test.wantErrors)
			}
			for index := range gotSources {
				if gotSources[index] != test.wantErrors[index] {
					t.Errorf("error sources = %v, want %v", gotSources, test.wantErrors)
				}
			}
			if len(payload.Profiles.Items) != 1 || payload.Profiles.Items[0].Status != profiles.StatusAvailable || payload.Profiles.Items[0].URI == "" {
				t.Errorf("profiles = %+v, want one copyable available profile", payload.Profiles.Items)
			}
		})
	}
}

func TestUserDetailSerializesEveryUserLevelProfileState(t *testing.T) {
	matchingView, err := xrayconfig.ParseView([]byte(`{
		"inbounds": [{
			"tag": "primary", "protocol": "vless",
			"settings": {"clients": [{"email": "alice@example.com", "id": "1d37a118-4f1b-4dc0-9e3c-3426b07518df"}]},
			"streamSettings": {"network": "raw", "security": "tls"}
		}]
	}`))
	if err != nil {
		t.Fatalf("parse matching xray view: %v", err)
	}
	emptyView, err := xrayconfig.ParseView([]byte(`{"inbounds": []}`))
	if err != nil {
		t.Fatalf("parse empty xray view: %v", err)
	}

	for _, test := range []struct {
		name       string
		disabled   bool
		sources    profiles.Sources
		wantState  profiles.State
		wantReason profiles.Reason
	}{
		{name: "disabled User", disabled: true, wantState: profiles.StateDisabledUser},
		{name: "source unavailable", wantState: profiles.StateSourceUnavailable},
		{name: "no matching inbound", sources: profiles.Sources{XrayView: emptyView, XrayAvailable: true}, wantState: profiles.StateNoMatchingInbound},
		{
			name:      "configured profile source has never loaded",
			sources:   profiles.Sources{XrayView: matchingView, XrayAvailable: true, AdvertisementsConfigured: true},
			wantState: profiles.StateReady, wantReason: profiles.ReasonSourceUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := api.New(
				fixedHostStats{}, fixedXrayStatus{},
				fixedUsers{snapshot: users.Snapshot{Users: []users.User{{
					Email: "alice@example.com", Disabled: test.disabled, UpBytesTotal: 123, DownBytesTotal: 456,
					Online: true, IPs: []string{"203.0.113.10"},
				}}}},
				fixedProfileSources{sources: test.sources}, &stubRoster{}, api.OperationalSources{}, session.NewManager(testPassword, time.Now),
				http.NotFoundHandler(), testPanelInfo,
			)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/users/alice%40example.com", nil)
			request.AddCookie(login(t, handler, testPassword))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
			}
			var payload struct {
				User     users.User `json:"user"`
				Profiles struct {
					State profiles.State `json:"state"`
					Items []struct {
						Reason profiles.Reason `json:"reason"`
					} `json:"items"`
				} `json:"connection_profiles"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Profiles.State != test.wantState {
				t.Errorf("state = %q, want %q", payload.Profiles.State, test.wantState)
			}
			if test.disabled && (payload.User.UpBytesTotal != 123 || payload.User.DownBytesTotal != 456 || !payload.User.Online || len(payload.User.IPs) != 1) {
				t.Errorf("disabled User observations = %+v, want retained Traffic and Presence", payload.User)
			}
			if test.wantReason == "" {
				if len(payload.Profiles.Items) != 0 {
					t.Errorf("items = %v, want []", payload.Profiles.Items)
				}
			} else if len(payload.Profiles.Items) != 1 || payload.Profiles.Items[0].Reason != test.wantReason {
				t.Errorf("items = %v, want one %q item", payload.Profiles.Items, test.wantReason)
			}
		})
	}
}

func TestUserDetailSerializesAvailableAndUnavailableProfileUnions(t *testing.T) {
	xrayView, err := xrayconfig.ParseView([]byte(`{
		"inbounds": [
			{
				"tag": "primary", "protocol": "vless",
				"settings": {"decryption": "none", "clients": [{"email": "alice@example.com", "id": "1d37a118-4f1b-4dc0-9e3c-3426b07518df"}]},
				"streamSettings": {"network": "raw", "security": "tls"}
			},
			{
				"tag": "secondary", "protocol": "vless",
				"settings": {"decryption": "none", "clients": [{"email": "alice@example.com", "id": "1d37a118-4f1b-4dc0-9e3c-3426b07518df"}]},
				"streamSettings": {"network": "raw", "security": "tls"}
			}
		]
	}`))
	if err != nil {
		t.Fatalf("parse xray view: %v", err)
	}
	advertisedView, err := advertisements.Parse([]byte(`{
		"version": 1,
		"advertisements": [{
			"inbound_tag": "primary", "name": "Primary", "topology": "direct",
			"host": "edge.example.com", "port": 443,
			"transport": {"type": "tcp"},
			"security": {"type": "tls"}
		}]
	}`))
	if err != nil {
		t.Fatalf("parse Advertised connection settings: %v", err)
	}
	handler := api.New(
		fixedHostStats{}, fixedXrayStatus{},
		fixedUsers{snapshot: users.Snapshot{Users: []users.User{{Email: "alice@example.com", IPs: []string{}}}}},
		fixedProfileSources{sources: profiles.Sources{
			XrayView: xrayView, XrayAvailable: true,
			AdvertisementsView: advertisedView, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
		}},
		&stubRoster{},
		api.OperationalSources{}, session.NewManager(testPassword, time.Now), http.NotFoundHandler(), testPanelInfo,
	)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/alice%40example.com", nil)
	request.AddCookie(login(t, handler, testPassword))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Profiles struct {
			Items []map[string]json.RawMessage `json:"items"`
		} `json:"connection_profiles"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Profiles.Items) != 2 {
		t.Fatalf("items = %d, want 2; body = %s", len(payload.Profiles.Items), response.Body.String())
	}
	available := payload.Profiles.Items[0]
	if string(available["status"]) != `"available"` {
		t.Errorf("available status = %s, want available", available["status"])
	}
	for _, field := range []string{"status", "inbound_tag", "name", "topology", "client_id", "flow", "endpoint", "transport", "security", "uri"} {
		if _, ok := available[field]; !ok {
			t.Errorf("available item omitted %q: %s", field, response.Body.String())
		}
	}
	for _, field := range []string{"reason", "message"} {
		if _, ok := available[field]; ok {
			t.Errorf("available item contains unavailable field %q", field)
		}
	}
	var transport map[string]json.RawMessage
	if err := json.Unmarshal(available["transport"], &transport); err != nil {
		t.Fatalf("decode transport: %v", err)
	}
	if len(transport) != 1 || string(transport["type"]) != `"tcp"` {
		t.Errorf("TCP transport = %s, want only type", available["transport"])
	}

	unavailable := payload.Profiles.Items[1]
	if string(unavailable["status"]) != `"unavailable"` {
		t.Errorf("unavailable status = %s, want unavailable", unavailable["status"])
	}
	for _, field := range []string{"status", "inbound_tag", "name", "reason", "message"} {
		if _, ok := unavailable[field]; !ok {
			t.Errorf("unavailable item omitted %q: %s", field, response.Body.String())
		}
	}
	for _, field := range []string{"client_id", "flow", "endpoint", "transport", "security", "uri"} {
		if _, ok := unavailable[field]; ok {
			t.Errorf("unavailable item exposes %q", field)
		}
	}
}

var _ interface {
	Latest(context.Context) (users.Snapshot, error)
} = fixedUsers{}
