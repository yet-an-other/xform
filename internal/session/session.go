// Package session is the compatibility facade for Password Sessions.
// Authentication owns the Session store and HTTP admission; this package keeps
// the small historical constructor available to package-level callers while
// the application depends on internal/auth's HTTP seam.
package session

import (
	"time"

	"github.com/yet-an-other/xform/internal/auth"
)

// TTL is the Password Session idle lifetime.
const TTL = auth.SessionTTL

// Manager is retained as an alias for the authentication gateway so existing
// Session-focused callers continue to test Login, Validate, and Logout while
// route admission remains owned by auth.Gateway.
type Manager = auth.Gateway

// NewManager constructs Password authentication. New application wiring
// should use auth.NewPassword directly; this adapter is kept for compatibility
// with the existing Session tests and callers.
func NewManager(password string, now func() time.Time) *Manager {
	return auth.NewPassword(password, now)
}
