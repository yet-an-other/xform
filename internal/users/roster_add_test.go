package users_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
)

// The mutation half of the roster store (user-management spec §3, §5): a
// panel-added user lands in the roster and appears on the dashboard at once,
// a returning gone user rejoins their history, and the two uniqueness rules
// are conflicts, not overwrites.
func TestAddRosterUserStoresTheRecordAndShowsTheRow(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	record, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "alice@example.com", ClientID: "uuid-alice",
		Inbounds: []string{"vless-vision", "vless-xhttp"},
		Protocol: "VLESS", Security: "XTLS-Reality",
	}, now)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if record.Email != "alice@example.com" || record.ClientID != "uuid-alice" ||
		!slices.Equal(record.Inbounds, []string{"vless-vision", "vless-xhttp"}) {
		t.Errorf("record = %+v", record)
	}
	if record.CreatedAt != now.Unix() || record.UpdatedAt != now.Unix() {
		t.Errorf("timestamps = %d/%d, want the add time", record.CreatedAt, record.UpdatedAt)
	}

	// The row shows immediately — not-gone, labelled, with the roster fields
	// joined — without waiting for the next config parse.
	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	alice := byEmail(list)["alice@example.com"]
	if alice.Disabled {
		t.Error("a just-added user must not read gone")
	}
	if alice.ClientID == nil || *alice.ClientID != "uuid-alice" {
		t.Errorf("row Client ID = %v", alice.ClientID)
	}
	if !slices.Equal(alice.Inbounds, []string{"vless-vision", "vless-xhttp"}) {
		t.Errorf("row inbounds = %v", alice.Inbounds)
	}
	if alice.Protocol == nil || *alice.Protocol != "VLESS" || alice.Security == nil || *alice.Security != "XTLS-Reality" {
		t.Errorf("row labels = %v · %v", alice.Protocol, alice.Security)
	}
}

// Re-adding a gone user's email rejoins the history (user-management spec
// §3): the same users row sheds the gone flag and keeps its totals.
func TestAddRosterUserRejoinsAGoneUsersHistory(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, nil, now); err != nil {
		t.Fatalf("apply traffic poll: %v", err)
	}
	// Edited out of the config: gone, history kept.
	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{Labels: map[string]users.RosterUser{}, Clients: map[string]users.RosterClient{}}, now.Add(time.Second)); err != nil {
		t.Fatalf("apply roster sync: %v", err)
	}

	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "alice@example.com", ClientID: "uuid-alice-new",
		Inbounds: []string{"vless-vision"}, Protocol: "VLESS", Security: "Reality",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("re-add: %v", err)
	}

	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("users = %d, want exactly one row — rejoining, not duplicating", len(list))
	}
	alice := list[0]
	if alice.Disabled || alice.UpBytesTotal != 100 || alice.DownBytesTotal != 1_000 {
		t.Errorf("alice = %+v, want not-gone with her history intact", alice)
	}
	if alice.LastSeen == nil {
		t.Error("last_seen must survive the re-add")
	}
}

// The two roster-wide uniqueness rules (user-management spec §5): email
// conflicts case-insensitively, Client IDs exactly — xray's own auth index
// silently overwrites on same-UUID-different-email, so the store refuses.
func TestAddRosterUserConflicts(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "alice@example.com", ClientID: "uuid-alice",
		Inbounds: []string{}, Protocol: "VLESS", Security: "Reality",
	}, now); err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "Alice@Example.com", ClientID: "uuid-other",
		Inbounds: []string{}, Protocol: "VLESS", Security: "Reality",
	}, now); !errors.Is(err, users.ErrEmailTaken) {
		t.Errorf("case-variant email = %v, want ErrEmailTaken", err)
	}
	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "bob@example.com", ClientID: "UUID-ALICE",
		Inbounds: []string{}, Protocol: "VLESS", Security: "Reality",
	}, now); !errors.Is(err, users.ErrClientIDTaken) {
		t.Errorf("case-variant Client ID = %v, want ErrClientIDTaken", err)
	}

	// Nothing was written by the conflicts.
	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("users = %d, want 1 — a conflict writes nothing", len(list))
	}
}
