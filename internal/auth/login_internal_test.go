package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingSessions struct{}

func (failingSessions) Login(string) (string, bool, error) { return "", false, errors.New("no entropy") }
func (failingSessions) Validate(string) bool               { return false }
func (failingSessions) Logout(string)                      {}

// A session-store failure during login is a 500, never the 401 of a
// password mismatch — the store being broken must not read as a wrong
// password.
func TestLoginFailureInSessionStoreIs500Not401(t *testing.T) {
	gateway := newGateway(ModePassword, failingSessions{})
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/login",
		strings.NewReader(`{"password":"anything"}`))
	response := httptest.NewRecorder()

	gateway.Handler(next).ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d — a session-store failure is not a password mismatch",
			response.Code, http.StatusInternalServerError)
	}
}
