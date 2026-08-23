package users_test

import (
	"context"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

func openStore(t *testing.T) *users.Store {
	t.Helper()
	store, err := users.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func byEmail(list []users.User) map[string]users.User {
	byEmail := make(map[string]users.User, len(list))
	for _, user := range list {
		byEmail[user.Email] = user
	}
	return byEmail
}

// The roster sync (SPEC.md §3 step 4, §4): config-defined users gain rows
// with their protocol · security labels; users edited out of the config
// become gone but keep their history.
func TestStoreSyncsRosterWithTheConfig(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	// alice has traffic history from before the config parse landed.
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
		{Email: "erin@example.com", Up: 50, Down: 500, SeenNow: true},
	}, nil, nil, now); err != nil {
		t.Fatalf("apply traffic poll: %v", err)
	}

	// The config names alice (VLESS · Reality) and the brand-new bob; erin
	// was edited out.
	roster := map[string]xrayconfig.User{
		"alice@example.com": {Protocol: "VLESS", Security: "Reality"},
		"bob@example.com":   {Protocol: "TROJAN", Security: "TLS"},
	}
	if err := store.ApplyPoll(ctx, nil, nil, roster, now.Add(5*time.Second)); err != nil {
		t.Fatalf("apply roster sync: %v", err)
	}

	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	got := byEmail(list)
	if len(got) != 3 {
		t.Fatalf("users = %d, want 3 (alice, bob, erin)", len(got))
	}

	alice := got["alice@example.com"]
	if alice.Protocol == nil || *alice.Protocol != "VLESS" || alice.Security == nil || *alice.Security != "Reality" {
		t.Errorf("alice labels = %v / %v, want VLESS / Reality", alice.Protocol, alice.Security)
	}
	if alice.UpBytesTotal != 100 || alice.DownBytesTotal != 1_000 {
		t.Errorf("alice totals = %d/%d, want her 100/1000 history untouched", alice.UpBytesTotal, alice.DownBytesTotal)
	}
	if alice.LastSeen == nil || *alice.LastSeen != now.Unix() {
		t.Errorf("alice last_seen = %v, want the traffic poll's %d", alice.LastSeen, now.Unix())
	}
	if alice.Gone {
		t.Error("alice gone = true, want false — she is in the config")
	}

	// bob appears automatically, with zero totals, before his first byte.
	bob := got["bob@example.com"]
	if bob.Protocol == nil || *bob.Protocol != "TROJAN" {
		t.Errorf("bob protocol = %v, want TROJAN", bob.Protocol)
	}
	if bob.UpBytesTotal != 0 || bob.DownBytesTotal != 0 || bob.LastSeen != nil {
		t.Errorf("bob = %+v, want zero totals and never seen", bob)
	}
	if bob.Gone {
		t.Error("bob gone = true, want false")
	}

	// erin is gone: retained, history intact, marked.
	erin := got["erin@example.com"]
	if !erin.Gone {
		t.Error("erin gone = false, want true — she was edited out of the config")
	}
	if erin.UpBytesTotal != 50 || erin.DownBytesTotal != 500 || erin.LastSeen == nil {
		t.Errorf("erin = %+v, want her history retained", erin)
	}
}

// A user returning to the config loses the gone flag; a config that drops
// everyone marks the whole roster gone.
func TestStoreRosterSyncRestoresReturningUsers(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	if err := store.ApplyPoll(ctx, nil, nil, map[string]xrayconfig.User{
		"alice@example.com": {Protocol: "VLESS", Security: "Reality"},
	}, now); err != nil {
		t.Fatalf("apply first roster: %v", err)
	}
	if err := store.ApplyPoll(ctx, nil, nil, map[string]xrayconfig.User{}, now.Add(time.Minute)); err != nil {
		t.Fatalf("apply empty roster: %v", err)
	}

	got := byEmail(mustUsers(t, store))
	if !got["alice@example.com"].Gone {
		t.Fatal("alice gone = false, want true after the config dropped everyone")
	}

	if err := store.ApplyPoll(ctx, nil, nil, map[string]xrayconfig.User{
		"alice@example.com": {Protocol: "VLESS", Security: "XTLS-Reality"},
	}, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("apply restored roster: %v", err)
	}
	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if alice.Gone {
		t.Error("alice gone = true, want false after returning to the config")
	}
	if alice.Security == nil || *alice.Security != "XTLS-Reality" {
		t.Errorf("alice security = %v, want the edited XTLS-Reality", alice.Security)
	}
}

func mustUsers(t *testing.T, store *users.Store) []users.User {
	t.Helper()
	list, err := store.Users(context.Background())
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	return list
}
