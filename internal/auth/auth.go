// Package auth owns admission to the Panel's HTTP interface. Password
// authentication keeps its Session store and all cookie and route behavior
// behind one HTTP wrapper so application handlers do not know how an Operator
// was admitted.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Mode identifies the policy a Panel uses to admit an Operator.
type Mode string

const (
	// ModePassword is the existing shared-password mode.
	ModePassword Mode = "password"
	// ModeTrustedProxy is reserved for the Authentication gateway mode added
	// by the next authentication slice.
	ModeTrustedProxy Mode = "trusted_proxy"
)

// SessionTTL is the idle lifetime of a Password Session.
const SessionTTL = 24 * time.Hour

const sessionCookieName = "xform_session"

// sessionStore is the private seam between the HTTP admission wrapper and its
// in-memory Password Session implementation. Keeping it private prevents the
// application router from acquiring a dependency on Session storage.
type sessionStore interface {
	Login(password string) (token string, ok bool, err error)
	Validate(token string) bool
	Logout(token string)
}

// Admission is the small seam the application router needs from an
// Authentication gateway. The gateway handles mode-specific routes and
// admits or rejects requests before forwarding to the application handler.
type Admission interface {
	Handler(next http.Handler) http.Handler
	Mode() string
}

// Gateway is the Panel's authentication adapter.
type Gateway struct {
	mode     Mode
	sessions sessionStore
}

// New constructs the currently supported authentication mode. An empty mode
// is treated as the documented Password default; configuration normally
// supplies the explicit value before this constructor is called.
func New(mode string, password string, now func() time.Time) (*Gateway, error) {
	if mode == "" {
		mode = string(ModePassword)
	}
	if Mode(mode) != ModePassword {
		return nil, fmt.Errorf("unsupported authentication mode %q", mode)
	}
	return NewPassword(password, now), nil
}

// NewPassword constructs Password authentication. Configuration rejects an
// empty password; this constructor deliberately leaves validation to the
// configuration seam so tests and callers can provide their own policy.
func NewPassword(password string, now func() time.Time) *Gateway {
	if now == nil {
		now = time.Now
	}
	return newGateway(ModePassword, newSessionStore(password, now))
}

func newGateway(mode Mode, sessions sessionStore) *Gateway {
	return &Gateway{mode: mode, sessions: sessions}
}

var _ Admission = (*Gateway)(nil)

// Mode reports the public authentication mode string for Panel metadata.
func (gateway *Gateway) Mode() string {
	return string(gateway.mode)
}

// Handler wraps the application handler with Password route admission.
// Dashboard documents and the GET health route remain public. Every other
// /api/ route requires a live Session; unknown API routes are protected too.
func (gateway *Gateway) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/login":
			gateway.login(response, request)
			return
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/logout":
			gateway.logout(response, request)
			return
		case isHealthRequest(request):
			next.ServeHTTP(response, request)
			return
		case strings.HasPrefix(request.URL.Path, "/api/"):
			gateway.requireSession(response, request, next)
			return
		default:
			next.ServeHTTP(response, request)
			return
		}
	})
}

func isHealthRequest(request *http.Request) bool {
	return (request.Method == http.MethodGet || request.Method == http.MethodHead) && request.URL.Path == "/api/v1/healthz"
}

func (gateway *Gateway) login(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	var body struct {
		Password string `json:"password"`
	}
	reader := request.Body
	if reader == nil {
		reader = http.NoBody
	}
	if err := json.NewDecoder(reader).Decode(&body); err != nil || body.Password == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	token, ok, err := gateway.sessions.Login(body.Password)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "login unavailable"})
		return
	}
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{
			"error":               "invalid password",
			"authentication_mode": gateway.Mode(),
		})
		return
	}
	setSessionCookie(response, token)
	response.WriteHeader(http.StatusNoContent)
}

func (gateway *Gateway) logout(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || !gateway.sessions.Validate(cookie.Value) {
		gateway.unauthorized(response)
		return
	}
	gateway.sessions.Logout(cookie.Value)
	http.SetCookie(response, &http.Cookie{
		Name: sessionCookieName, MaxAge: -1, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	response.WriteHeader(http.StatusNoContent)
}

func (gateway *Gateway) requireSession(response http.ResponseWriter, request *http.Request, next http.Handler) {
	response.Header().Set("Cache-Control", "no-store")
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || !gateway.sessions.Validate(cookie.Value) {
		gateway.unauthorized(response)
		return
	}
	// Slide both sides of the 24-hour expiry: the store entry (via Validate)
	// and the cookie's Max-Age.
	setSessionCookie(response, cookie.Value)
	next.ServeHTTP(response, request)
}

func (gateway *Gateway) unauthorized(response http.ResponseWriter) {
	writeJSON(response, http.StatusUnauthorized, map[string]string{
		"error":               "unauthenticated",
		"authentication_mode": gateway.Mode(),
	})
}

// Login, Validate, and Logout are retained on the concrete Gateway for the
// compatibility facade in internal/session. Application code should use
// Handler instead, which keeps Session storage behind this module.
func (gateway *Gateway) Login(password string) (string, bool, error) {
	return gateway.sessions.Login(password)
}

func (gateway *Gateway) Validate(token string) bool {
	return gateway.sessions.Validate(token)
}

func (gateway *Gateway) Logout(token string) {
	gateway.sessions.Logout(token)
}

type sessions struct {
	passwordHash [32]byte
	now          func() time.Time

	mu       sync.Mutex
	sessions map[string]time.Time
}

func newSessionStore(password string, now func() time.Time) *sessions {
	return &sessions{
		passwordHash: sha256.Sum256([]byte(password)),
		now:          now,
		sessions:     map[string]time.Time{},
	}
}

func (manager *sessions) Login(candidate string) (string, bool, error) {
	candidateHash := sha256.Sum256([]byte(candidate))
	if subtle.ConstantTimeCompare(candidateHash[:], manager.passwordHash[:]) != 1 {
		return "", false, nil
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", false, fmt.Errorf("read entropy for session token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.sessions[token] = manager.now().Add(SessionTTL)
	return token, true, nil
}

func (manager *sessions) Validate(token string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	expiry, ok := manager.sessions[token]
	if !ok {
		return false
	}
	if !manager.now().Before(expiry) {
		delete(manager.sessions, token)
		return false
	}
	manager.sessions[token] = manager.now().Add(SessionTTL)
	return true
}

func (manager *sessions) Logout(token string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	delete(manager.sessions, token)
}

func setSessionCookie(response http.ResponseWriter, token string) {
	http.SetCookie(response, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/",
		MaxAge:   int(SessionTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
