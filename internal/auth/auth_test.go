package auth_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/auth"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time                 { return c.now }
func (c *clock) Advance(duration time.Duration) { c.now = c.now.Add(duration) }

func nextHandler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Application", "reached")
		response.WriteHeader(http.StatusNoContent)
	})
}

func request(gateway *auth.Gateway, method, path string, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	gateway.Handler(nextHandler()).ServeHTTP(response, req)
	return response
}

func TestPasswordGatewayAdmitsOnlyLiveSessions(t *testing.T) {
	clock := &clock{now: time.Unix(1_723_800_000, 0)}
	gateway := auth.NewPassword("correct-horse", clock.Now)

	unauthenticated := request(gateway, http.MethodGet, "/api/v1/server", "", nil)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("protected route status = %d, want 401", unauthenticated.Code)
	}
	if got := unauthenticated.Header().Get("X-Application"); got != "" {
		t.Fatalf("protected route reached application with no session: X-Application=%q", got)
	}
	assertJSON(t, unauthenticated, map[string]string{
		"error":               "unauthenticated",
		"authentication_mode": "password",
	})

	login := request(gateway, http.MethodPost, "/api/v1/login", `{"password":"correct-horse"}`, nil)
	if login.Code != http.StatusNoContent {
		t.Fatalf("login status = %d, want 204; body=%s", login.Code, login.Body)
	}
	if login.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("login Cache-Control = %q, want no-store", login.Header().Get("Cache-Control"))
	}
	cookie := cookieNamed(t, login, "xform_session")
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("login cookie = %+v, want HttpOnly Secure SameSite=Lax Path=/", cookie)
	}
	if cookie.MaxAge != int(auth.SessionTTL.Seconds()) {
		t.Fatalf("login MaxAge = %d, want %d", cookie.MaxAge, int(auth.SessionTTL.Seconds()))
	}

	authed := request(gateway, http.MethodGet, "/api/v1/server", "", cookie)
	if authed.Code != http.StatusNoContent || authed.Header().Get("X-Application") != "reached" {
		t.Fatalf("authenticated route = %d/%q, want application 204", authed.Code, authed.Header().Get("X-Application"))
	}
	if refreshed := cookieNamed(t, authed, "xform_session"); refreshed.MaxAge != int(auth.SessionTTL.Seconds()) {
		t.Fatalf("refreshed MaxAge = %d, want %d", refreshed.MaxAge, int(auth.SessionTTL.Seconds()))
	}

	clock.Advance(auth.SessionTTL)
	expired := request(gateway, http.MethodGet, "/api/v1/server", "", cookie)
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d, want 401", expired.Code)
	}
	assertJSON(t, expired, map[string]string{
		"error":               "unauthenticated",
		"authentication_mode": "password",
	})
}

func TestPasswordGatewayKeepsPublicRoutesAndOwnsLogout(t *testing.T) {
	gateway := auth.NewPassword("correct-horse", time.Now)

	for _, path := range []string{"/", "/api/v1/healthz"} {
		response := request(gateway, http.MethodGet, path, "", nil)
		if response.Code != http.StatusNoContent || response.Header().Get("X-Application") != "reached" {
			t.Errorf("public %s = %d/%q, want application 204", path, response.Code, response.Header().Get("X-Application"))
		}
	}

	cookie := cookieNamed(t, request(gateway, http.MethodPost, "/api/v1/login", `{"password":"correct-horse"}`, nil), "xform_session")
	logout := request(gateway, http.MethodPost, "/api/v1/logout", "", cookie)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", logout.Code)
	}
	if logout.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout Cache-Control = %q, want no-store", logout.Header().Get("Cache-Control"))
	}
	cleared := cookieNamed(t, logout, "xform_session")
	if cleared.MaxAge >= 0 {
		t.Fatalf("logout cookie MaxAge = %d, want a deletion cookie", cleared.MaxAge)
	}
	if response := request(gateway, http.MethodGet, "/api/v1/server", "", cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, want 401", response.Code)
	}
	if response := request(gateway, http.MethodPost, "/api/v1/logout", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("sessionless logout status = %d, want 401", response.Code)
	}
}

func TestPasswordGatewayRestartInvalidatesSessions(t *testing.T) {
	first := auth.NewPassword("correct-horse", time.Now)
	cookie := cookieNamed(t, request(first, http.MethodPost, "/api/v1/login", `{"password":"correct-horse"}`, nil), "xform_session")

	second := auth.NewPassword("correct-horse", time.Now)
	response := request(second, http.MethodGet, "/api/v1/server", "", cookie)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("session from previous gateway status = %d, want 401", response.Code)
	}
}

func TestPasswordGatewayReportsModeOnInvalidLogin(t *testing.T) {
	gateway := auth.NewPassword("correct-horse", time.Now)
	response := request(gateway, http.MethodPost, "/api/v1/login", `{"password":"wrong-horse"}`, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-password status = %d, want 401", response.Code)
	}
	assertJSON(t, response, map[string]string{
		"error":               "invalid password",
		"authentication_mode": "password",
	})
	if len(response.Result().Cookies()) != 0 {
		t.Fatalf("wrong-password response set cookies: %v", response.Result().Cookies())
	}
}

func TestPasswordGatewayRejectsMalformedOrEmptyLogin(t *testing.T) {
	gateway := auth.NewPassword("correct-horse", time.Now)
	for name, body := range map[string]string{
		"malformed":      "not-json",
		"empty password": `{"password":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := request(gateway, http.MethodPost, "/api/v1/login", body, nil)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("login status = %d, want 400", response.Code)
			}
			assertJSON(t, response, map[string]string{"error": "invalid request"})
		})
	}
}

func assertJSON(t *testing.T, response *httptest.ResponseRecorder, want map[string]string) {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON %q: %v", response.Body, err)
	}
	if len(got) != len(want) {
		t.Fatalf("JSON = %v, want exactly %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("JSON[%q] = %q, want %q (whole body %v)", key, got[key], value, got)
		}
	}
}

func cookieNamed(t *testing.T, response *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response did not set %s cookie", name)
	return nil
}

var trustedProxySecret = strings.Repeat("ab", 32)

func TestTrustedProxyRejectsMissingWrongAndDuplicateAssertions(t *testing.T) {
	gateway, err := auth.NewTrustedProxy(trustedProxySecret, time.Now)
	if err != nil {
		t.Fatalf("construct trusted proxy gateway: %v", err)
	}

	requests := map[string]func(*http.Request){
		"missing": func(*http.Request) {},
		"forwarding identity only": func(request *http.Request) {
			request.Header.Set("X-Forwarded-User", "operator@example.com")
			request.Header.Set("X-Auth-Request-Email", "operator@example.com")
		},
		"wrong": func(request *http.Request) {
			request.Header.Set("X-Xform-Authenticated", strings.Repeat("cd", 32))
		},
		"duplicate": func(request *http.Request) {
			request.Header.Add("X-Xform-Authenticated", trustedProxySecret)
			request.Header.Add("X-Xform-Authenticated", trustedProxySecret)
		},
	}
	for name, configure := range requests {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/server", nil)
			configure(request)
			response := httptest.NewRecorder()
			gateway.Handler(nextHandler()).ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
			assertJSON(t, response, map[string]string{
				"error":               "unauthenticated",
				"authentication_mode": "trusted_proxy",
			})
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
			}
			if response.Header().Get("X-Application") != "" {
				t.Fatal("rejected request reached the application")
			}
		})
	}
}

func TestTrustedProxyAdmitsOneAssertionAndStripsTrustHeaders(t *testing.T) {
	gateway, err := auth.NewTrustedProxy(trustedProxySecret, time.Now)
	if err != nil {
		t.Fatalf("construct trusted proxy gateway: %v", err)
	}

	var seen *http.Request
	next := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		seen = request
		response.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/server", nil)
	request.Header.Set("X-Xform-Authenticated", trustedProxySecret)
	request.Header.Set("Authorization", "Bearer identity-token")
	request.Header.Set("X-Forwarded-User", "operator@example.com")
	request.Header.Set("X-Auth-Request-Access-Token", "id-token")
	request.Header.Set("Cookie", "gateway_session=operator-secret")
	request.Header.Set("X-Forwarded-For", "203.0.113.10")
	response := httptest.NewRecorder()

	gateway.Handler(next).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("admitted status = %d, want 204", response.Code)
	}
	if seen == nil {
		t.Fatal("valid assertion did not reach the application")
	}
	for _, name := range []string{
		"X-Xform-Authenticated", "Authorization", "X-Forwarded-User", "X-Auth-Request-Access-Token", "X-Forwarded-For", "Cookie",
	} {
		if values := headerValuesForTest(seen.Header, name); len(values) != 0 {
			t.Errorf("application received %s, want it stripped", name)
		}
	}
}

func TestTrustedProxyHealthIsOpenAndAssertionFree(t *testing.T) {
	gateway, err := auth.NewTrustedProxy(trustedProxySecret, time.Now)
	if err != nil {
		t.Fatalf("construct trusted proxy gateway: %v", err)
	}
	var seen *http.Request
	next := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		seen = request
		response.WriteHeader(http.StatusNoContent)
	})
	for _, assertion := range []string{"", trustedProxySecret} {
		seen = nil
		request := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
		if assertion != "" {
			request.Header.Set("X-Xform-Authenticated", assertion)
		}
		response := httptest.NewRecorder()

		gateway.Handler(next).ServeHTTP(response, request)

		if response.Code != http.StatusNoContent || seen == nil {
			t.Fatalf("health assertion-present=%t = %d/%v, want application 204", assertion != "", response.Code, seen != nil)
		}
		if values := headerValuesForTest(seen.Header, "X-Xform-Authenticated"); len(values) != 0 {
			t.Error("health handler received the assertion, want it stripped")
		}
	}
}

func TestTrustedProxyRejectsPasswordSessionRoutes(t *testing.T) {
	gateway, err := auth.NewTrustedProxy(trustedProxySecret, time.Now)
	if err != nil {
		t.Fatalf("construct trusted proxy gateway: %v", err)
	}
	for _, path := range []string{"/api/v1/login", "/api/v1/logout"} {
		t.Run(path, func(t *testing.T) {
			response := request(gateway, http.MethodPost, path, `{"password":"anything"}`, nil)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("without assertion status = %d, want 401", response.Code)
			}
			assertJSON(t, response, map[string]string{
				"error":               "unauthenticated",
				"authentication_mode": "trusted_proxy",
			})
		})
	}
}

func TestTrustedProxyConstructorValidatesSecretWithoutEchoingIt(t *testing.T) {
	secrets := []struct {
		name   string
		secret string
	}{
		{name: "empty", secret: ""},
		{name: "short", secret: strings.Repeat("ab", 31)},
		{name: "uppercase", secret: strings.Repeat("AB", 32)},
		{name: "non hexadecimal", secret: strings.Repeat("ag", 32)},
	}
	for _, test := range secrets {
		t.Run(test.name, func(t *testing.T) {
			gateway, err := auth.NewTrustedProxy(test.secret, time.Now)
			if gateway != nil || err == nil {
				t.Fatalf("secret length/content %d accepted, gateway=%v err=%v", len(test.secret), gateway, err)
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Error("constructor error echoed the Admission secret")
			}
		})
	}
}

func headerValuesForTest(headers http.Header, name string) []string {
	var values []string
	for key, entries := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, entries...)
		}
	}
	return values
}
