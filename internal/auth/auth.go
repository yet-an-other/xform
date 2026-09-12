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
	"errors"
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
	// ModeTrustedProxy delegates admission to a same-Host Authentication gateway.
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
	// trustedHash is the SHA-256 digest of the Admission secret. The clear
	// assertion is never retained, logged, or passed to application handlers.
	trustedHash [32]byte
}

// New constructs the currently supported authentication mode. An empty mode
// is treated as the documented Password default; configuration normally
// supplies the explicit value before this constructor is called.
func New(mode string, credential string, now func() time.Time) (*Gateway, error) {
	if mode == "" {
		mode = string(ModePassword)
	}
	switch Mode(mode) {
	case ModePassword:
		return NewPassword(credential, now), nil
	case ModeTrustedProxy:
		return NewTrustedProxy(credential, now)
	default:
		return nil, fmt.Errorf("unsupported authentication mode %q", mode)
	}
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

// NewTrustedProxy constructs admission for a same-Host Authentication
// gateway. The secret is hashed immediately and is not retained in cleartext.
func NewTrustedProxy(secret string, _ func() time.Time) (*Gateway, error) {
	if err := ValidateTrustedProxySecret(secret); err != nil {
		return nil, err
	}
	return &Gateway{mode: ModeTrustedProxy, trustedHash: sha256.Sum256([]byte(secret))}, nil
}

// ValidateTrustedProxySecret enforces the wire-format needed for an Admission
// secret. It never includes the candidate in its error.
func ValidateTrustedProxySecret(secret string) error {
	if len(secret) != 64 {
		return errors.New("trusted proxy secret must contain exactly 64 lowercase hexadecimal characters")
	}
	for _, character := range secret {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return errors.New("trusted proxy secret must contain exactly 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

func newGateway(mode Mode, sessions sessionStore) *Gateway {
	return &Gateway{mode: mode, sessions: sessions}
}

var _ Admission = (*Gateway)(nil)

// Mode reports the public authentication mode string for Panel metadata.
func (gateway *Gateway) Mode() string {
	return string(gateway.mode)
}

// Handler wraps the application handler with mode-specific admission.
// Password mode keeps the Dashboard document public; trusted mode protects the
// whole xform origin because the gateway owns the public sign-in boundary.
func (gateway *Gateway) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if isHealthRequest(request) {
			if gateway.mode == ModeTrustedProxy {
				stripTrustedHeaders(request.Header)
			}
			next.ServeHTTP(response, request)
			return
		}
		if gateway.mode == ModeTrustedProxy {
			gateway.requireTrustedAssertion(response, request, next)
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/login":
			gateway.login(response, request)
			return
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/logout":
			gateway.logout(response, request)
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

func (gateway *Gateway) requireTrustedAssertion(response http.ResponseWriter, request *http.Request, next http.Handler) {
	response.Header().Set("Cache-Control", "no-store")
	values := headerValues(request.Header, "X-Xform-Authenticated")
	if len(values) != 1 {
		gateway.unauthorized(response)
		return
	}
	candidateHash := sha256.Sum256([]byte(values[0]))
	if subtle.ConstantTimeCompare(candidateHash[:], gateway.trustedHash[:]) != 1 {
		gateway.unauthorized(response)
		return
	}
	// Never let gateway identity, token, forwarding, or assertion headers
	// become application inputs. Admission is solely the exact assertion.
	stripTrustedHeaders(request.Header)
	next.ServeHTTP(response, request)
}

var trustedHeaders = [...]string{
	"X-Xform-Authenticated", "Authorization", "Proxy-Authorization", "Forwarded", "Cookie",
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Port", "X-Forwarded-Proto",
	"X-Forwarded-User", "X-Forwarded-Email", "X-Forwarded-Groups", "X-Forwarded-Preferred-Username",
	"X-Forwarded-Access-Token", "X-Forwarded-Authorization", "X-Forwarded-Client-Cert",
	"X-Auth-Request-User", "X-Auth-Request-Email", "X-Auth-Request-Groups",
	"X-Auth-Request-Preferred-Username", "X-Auth-Request-Token", "X-Auth-Request-Access-Token",
	"X-Auth-Request-Id-Token", "X-Auth-Request-IdToken", "X-Access-Token", "X-ID-Token", "X-Id-Token",
	"Remote-User", "Remote-Email", "Remote-Groups", "X-Remote-User", "X-Remote-Email", "X-Remote-Groups",
	"X-Authenticated-User", "X-Authenticated-Email", "X-User", "X-Email", "X-Groups", "X-Group",
	"X-Real-IP", "X-Original-URL", "X-Original-URI", "X-SSL-Client-Cert", "X-Client-Cert",
}

func headerValues(headers http.Header, name string) []string {
	var values []string
	for key, entries := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, entries...)
		}
	}
	return values
}

func stripTrustedHeaders(headers http.Header) {
	for key := range headers {
		for _, trustedHeader := range trustedHeaders {
			if strings.EqualFold(key, trustedHeader) {
				delete(headers, key)
				break
			}
		}
	}
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
	if gateway.sessions == nil {
		return "", false, errors.New("password authentication is disabled")
	}
	return gateway.sessions.Login(password)
}

func (gateway *Gateway) Validate(token string) bool {
	return gateway.sessions != nil && gateway.sessions.Validate(token)
}

func (gateway *Gateway) Logout(token string) {
	if gateway.sessions != nil {
		gateway.sessions.Logout(token)
	}
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
