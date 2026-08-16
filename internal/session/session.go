// Package session manages the panel's login sessions: constant-time password
// checks and in-memory tokens with a 24h sliding expiry (SPEC.md §5).
// Sessions are not durable — a panel restart logs everyone out.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// TTL is how long a session lives since its last use.
const TTL = 24 * time.Hour

// Manager issues, validates, and revokes session tokens. Safe for concurrent use.
type Manager struct {
	passwordHash [32]byte // SHA-256 of the password, so compares hide its length
	now          func() time.Time

	mu       sync.Mutex
	sessions map[string]time.Time // token → expiry
}

// NewManager returns a Manager checking passwords against password; now is the
// time source (time.Now in production, a fake clock in tests).
func NewManager(password string, now func() time.Time) *Manager {
	return &Manager{passwordHash: sha256.Sum256([]byte(password)), now: now, sessions: map[string]time.Time{}}
}

// Login compares candidate against the panel password in constant time and, on
// a match, returns a fresh session token. An error means entropy failed — the
// caller must answer 5xx, not mistake it for a password mismatch.
func (m *Manager) Login(candidate string) (string, bool, error) {
	candidateHash := sha256.Sum256([]byte(candidate))
	if subtle.ConstantTimeCompare(candidateHash[:], m.passwordHash[:]) != 1 {
		return "", false, nil
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", false, fmt.Errorf("read entropy for session token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[token] = m.now().Add(TTL)
	return token, true, nil
}

// Validate reports whether token names a live session, sliding its expiry to
// TTL from now. Expired and unknown tokens are rejected.
func (m *Manager) Validate(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	expiry, ok := m.sessions[token]
	if !ok {
		return false
	}
	if !m.now().Before(expiry) {
		delete(m.sessions, token)
		return false
	}
	m.sessions[token] = m.now().Add(TTL)
	return true
}

// Logout revokes token immediately. Unknown tokens are ignored.
func (m *Manager) Logout(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}
