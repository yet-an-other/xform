package session_test

import (
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/session"
)

// clock is a manually advanced time source for expiry tests.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestLoginRejectsWrongPassword(t *testing.T) {
	manager := session.NewManager("correct-horse", time.Now)

	if _, ok, _ := manager.Login("wrong-horse"); ok {
		t.Error("login with wrong password succeeded, want rejection")
	}
	if _, ok, _ := manager.Login(""); ok {
		t.Error("login with empty password succeeded, want rejection")
	}
}

func TestLoginTokenValidates(t *testing.T) {
	manager := session.NewManager("correct-horse", time.Now)

	token, ok, err := manager.Login("correct-horse")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !ok {
		t.Fatal("login with the panel password rejected, want success")
	}
	if token == "" {
		t.Fatal("login returned an empty token")
	}
	if !manager.Validate(token) {
		t.Error("validate rejected a fresh login token, want acceptance")
	}
	if manager.Validate("not-a-real-token") {
		t.Error("validate accepted an unknown token, want rejection")
	}
}

func TestSessionExpiresAndSlides(t *testing.T) {
	clock := &clock{now: time.Now()}
	manager := session.NewManager("correct-horse", clock.Now)

	token, ok, err := manager.Login("correct-horse")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !ok {
		t.Fatal("login rejected, want success")
	}

	clock.Advance(session.TTL)
	if manager.Validate(token) {
		t.Fatal("validate accepted a token idle for the full TTL, want expiry")
	}

	token, ok, err = manager.Login("correct-horse")
	if err != nil {
		t.Fatalf("re-login: %v", err)
	}
	if !ok {
		t.Fatal("re-login rejected, want success")
	}
	clock.Advance(session.TTL - time.Minute)
	if !manager.Validate(token) {
		t.Fatal("validate rejected a live token near the TTL boundary, want acceptance")
	}
	// The slide above moved the expiry a full TTL out from that use.
	clock.Advance(session.TTL - time.Minute)
	if !manager.Validate(token) {
		t.Error("validate rejected a token kept alive by use, want the expiry to slide")
	}
}

func TestLogoutRevokesImmediately(t *testing.T) {
	manager := session.NewManager("correct-horse", time.Now)

	token, ok, err := manager.Login("correct-horse")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !ok {
		t.Fatal("login rejected, want success")
	}

	manager.Logout(token)

	if manager.Validate(token) {
		t.Error("validate accepted a logged-out token, want rejection")
	}
	// Logging out an unknown token must not panic or create state.
	manager.Logout("not-a-real-token")
}
