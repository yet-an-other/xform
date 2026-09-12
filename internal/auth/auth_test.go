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
