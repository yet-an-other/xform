package users_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The remove mutation's store half (user-management spec §3–§4, CONTEXT.md
// Gone user): the roster row is flagged gone — never erased — the dashboard
// row keeps its history behind the gone badge, and the roster record reads
// as absent so edit/remove answer not-found or no-op.
func TestRemoveRosterUserFlagsGoneAndKeepsHistory(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	// alice exists, with traffic history.
	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("apply traffic: %v", err)
	}

	if err := store.RemoveRosterUser(ctx, "Alice@Example.com", now.Add(2*time.Second)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// The roster record is gone; a re-remove is a no-op success.
	if _, err := store.RosterRecord(ctx, "alice@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("record after remove = %v, want ErrRosterNotFound", err)
	}
	if err := store.RemoveRosterUser(ctx, "alice@example.com", now.Add(3*time.Second)); err != nil {
		t.Errorf("re-remove = %v, want idempotent success", err)
	}

	// The dashboard row stays: gone, history intact, roster fields null.
	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if !alice.Gone {
		t.Error("a removed user must read gone")
	}
	if alice.UpBytesTotal != 100 || alice.DownBytesTotal != 1_000 || alice.LastSeen == nil {
		t.Errorf("alice = %+v, want her history retained", alice)
	}
	if alice.ClientID != nil || alice.Inbounds != nil {
		t.Errorf("roster fields = %v / %v, want null — she is no longer a roster member", alice.ClientID, alice.Inbounds)
	}

	// A config parse carrying her (drift before the file render lands) must
	// not revive her: neither the labels upsert nor adoption.
	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{
		Labels: map[string]xrayconfig.User{
			"alice@example.com": {Protocol: "VLESS", Security: "Reality"},
		},
		Clients: map[string]users.RosterClient{
			"alice@example.com": {ClientID: "uuid-alice", Inbounds: []string{"vless-vision"}},
		},
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("apply drift parse: %v", err)
	}
	alice = byEmail(mustUsers(t, store))["alice@example.com"]
	if !alice.Gone || alice.ClientID != nil {
		t.Errorf("after the drift parse alice = %+v, want still gone and out of the roster", alice)
	}
}

// Re-adding a removed email revives it: the roster row sheds gone, the
// dashboard row rejoins its history (user-management spec §3).
func TestAddRosterUserRevivesARemovedUser(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("apply traffic: %v", err)
	}
	if err := store.RemoveRosterUser(ctx, "alice@example.com", now.Add(2*time.Second)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	record, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "alice@example.com", ClientID: "uuid-alice-new",
		Inbounds: []string{"vless-ws"}, Protocol: "VLESS", Security: "TLS",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if record.ClientID != "uuid-alice-new" || len(record.Inbounds) != 1 {
		t.Errorf("revived record = %+v", record)
	}

	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if alice.Gone || alice.UpBytesTotal != 100 || alice.DownBytesTotal != 1_000 {
		t.Errorf("alice = %+v, want revived, history rejoined", alice)
	}
	if alice.ClientID == nil || *alice.ClientID != "uuid-alice-new" {
		t.Errorf("row Client ID = %v, want the new credential", alice.ClientID)
	}

	// And adoption resumes for her: a config parse with an extra attachment
	// merges normally again.
	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{
		Labels: map[string]xrayconfig.User{"alice@example.com": {Protocol: "VLESS", Security: "Reality"}},
		Clients: map[string]users.RosterClient{
			"alice@example.com": {ClientID: "uuid-alice-new", Inbounds: []string{"vless-ws", "vless-xhttp"}},
		},
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("apply adoption: %v", err)
	}
	record, err = store.RosterRecord(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("record after adoption: %v", err)
	}
	if len(record.Inbounds) != 2 {
		t.Errorf("inbounds after adoption = %v, want the merged pair", record.Inbounds)
	}
}

// A removed user's Client ID stays roster-wide taken — the history row
// keeps its claim (spec §5 uniqueness holds across gone rows).
func TestRemoveKeepsClientIDUniquenessClaim(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", nil, now)
	if err := store.RemoveRosterUser(ctx, "alice@example.com", now.Add(time.Second)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "bob@example.com", ClientID: "UUID-ALICE", Inbounds: []string{},
	}, now.Add(2*time.Second)); !errors.Is(err, users.ErrClientIDTaken) {
		t.Errorf("reusing a removed user's Client ID = %v, want ErrClientIDTaken", err)
	}
}
