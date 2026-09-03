package users_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
)

func addRoster(t *testing.T, store *users.Store, email, clientID string, inbounds []string, now time.Time) users.RosterRecord {
	t.Helper()
	record, err := store.AddRosterUser(context.Background(), users.NewRosterUser{
		Email: email, ClientID: clientID, Inbounds: inbounds,
		Protocol: "VLESS", Security: "Reality",
	}, now)
	if err != nil {
		t.Fatalf("add %s: %v", email, err)
	}
	return record
}

// The edit mutation's store half (user-management spec §4–§5): the record
// keeps its identity and creation, takes the new attachment set and Client
// ID, and the dashboard row follows at once — not gone, relabelled.
func TestEditRosterUserUpdatesAttachmentsAndClientID(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)
	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision", "vless-ws"}, now)

	rotate := "uuid-alice-new"
	record, err := store.EditRosterUser(ctx, "Alice@Example.com", users.RosterEdit{
		ClientID: &rotate,
		Inbounds: []string{"vless-xhttp"},
		Protocol: "VLESS", Security: "XTLS-Reality",
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if record.ClientID != "uuid-alice-new" || !slices.Equal(record.Inbounds, []string{"vless-xhttp"}) {
		t.Errorf("record = %+v", record)
	}
	if record.CreatedAt != now.Unix() {
		t.Errorf("created_at = %d, want the original", record.CreatedAt)
	}
	if record.UpdatedAt != now.Add(time.Minute).Unix() {
		t.Errorf("updated_at = %d, want the edit time", record.UpdatedAt)
	}

	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	alice := byEmail(list)["alice@example.com"]
	if alice.Disabled {
		t.Error("an edited roster member must not read gone")
	}
	if alice.ClientID == nil || *alice.ClientID != "uuid-alice-new" {
		t.Errorf("row Client ID = %v", alice.ClientID)
	}
	if !slices.Equal(alice.Inbounds, []string{"vless-xhttp"}) {
		t.Errorf("row inbounds = %v", alice.Inbounds)
	}
	if alice.Security == nil || *alice.Security != "XTLS-Reality" {
		t.Errorf("row security = %v, want the relabelled one", alice.Security)
	}
}

// PATCH is idempotent (user-management spec §5): repeating the same edit
// leaves the same state — not even updated_at moves.
func TestEditRosterUserIsIdempotent(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)
	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)

	same := "uuid-alice"
	record, err := store.EditRosterUser(ctx, "alice@example.com", users.RosterEdit{
		ClientID: &same,
		Inbounds: []string{"vless-vision"},
		Protocol: "VLESS", Security: "Reality",
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("repeat edit: %v", err)
	}
	if record.UpdatedAt != now.Unix() {
		t.Errorf("updated_at = %d, want the untouched original — an unchanged edit writes nothing", record.UpdatedAt)
	}
}

// Detaching every inbound keeps the roster member listed and manageable —
// a profile-less user is not a gone user (CONTEXT.md: gone = removed from
// the Roster).
func TestEditRosterUserCanDetachEveryInboundAndStaysListed(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)
	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)

	record, err := store.EditRosterUser(ctx, "alice@example.com", users.RosterEdit{
		Inbounds: []string{},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("detach-all edit: %v", err)
	}
	if len(record.Inbounds) != 0 {
		t.Errorf("inbounds = %v, want none", record.Inbounds)
	}

	// A later config parse without alice must not mark her gone — the roster
	// decides gone-ness, not the config.
	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{
		Labels: map[string]users.RosterUser{}, Clients: map[string]users.RosterClient{},
	}, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("apply roster sync: %v", err)
	}
	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if alice.Disabled {
		t.Error("a profile-less roster member must stay listed, not gone")
	}
}

// The Client ID rule holds on edit with one carve-out: another user's ID is
// a conflict, the user's own is a plain no-op.
func TestEditRosterUserClientIDConflicts(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)
	addRoster(t, store, "alice@example.com", "uuid-alice", nil, now)
	addRoster(t, store, "bob@example.com", "uuid-bob", nil, now)

	taken := "UUID-BOB"
	if _, err := store.EditRosterUser(ctx, "alice@example.com", users.RosterEdit{ClientID: &taken}, now.Add(time.Minute)); !errors.Is(err, users.ErrClientIDTaken) {
		t.Errorf("another user's Client ID = %v, want ErrClientIDTaken", err)
	}

	own := "UUID-ALICE" // case-variant own spelling: still alice's
	record, err := store.EditRosterUser(ctx, "alice@example.com", users.RosterEdit{ClientID: &own}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("own Client ID: %v", err)
	}
	if record.ClientID != "uuid-alice" {
		t.Errorf("Client ID = %q, want the stored spelling untouched", record.ClientID)
	}
}

// Editing an email the roster does not carry is a not-found, not a conflict
// — and the lookup the service diffs against reports the same.
func TestEditRosterUserUnknownEmailIsNotFound(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	if _, err := store.EditRosterUser(ctx, "ghost@example.com", users.RosterEdit{}, now); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("edit unknown = %v, want ErrRosterNotFound", err)
	}
	if _, err := store.RosterRecord(ctx, "ghost@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("lookup unknown = %v, want ErrRosterNotFound", err)
	}
}
