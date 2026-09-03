package users_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The delete mutation's store half (ADR-0007, issue #59): phase one marks
// the roster row deleting — disabled with it, so the dashboard renders the
// row disabled while the removal applies, and every mutation read treats
// the email as absent — and phase two purges every stored trace keyed by
// the email. Nothing remembers it afterwards; a re-add starts fresh.
func TestMarkRosterDeletingThenPurgeRemovesEveryTrace(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("apply traffic: %v", err)
	}
	if err := store.SaveTrafficTotals(ctx, 5, 50); err != nil {
		t.Fatalf("save totals: %v", err)
	}

	if err := store.MarkRosterDeleting(ctx, "Alice@Example.com", now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}

	// While deleting, the row renders as disabled with its history — the
	// existing Roster-sync surface (pending/failed) carries the act.
	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if !alice.Disabled || alice.UpBytesTotal != 100 {
		t.Errorf("deleting alice = %+v, want rendered disabled, history kept", alice)
	}

	// Every mutation read answers not-found: enable, edit, disable all
	// treat a deleting email as absent.
	if _, err := store.RosterRecord(ctx, "alice@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("live record = %v, want ErrRosterNotFound", err)
	}
	if _, err := store.DisabledRosterRecord(ctx, "alice@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("disabled record = %v, want ErrRosterNotFound — a deleting row is not an enable target", err)
	}
	if _, _, err := store.EnableRosterUser(ctx, "alice@example.com", now); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("enable a deleting email = %v, want ErrRosterNotFound", err)
	}
	if err := store.MarkRosterDeleting(ctx, "alice@example.com", now); err != nil {
		t.Errorf("re-mark = %v, want idempotent success", err)
	}

	// Phase two: the purge erases the email from both tables and touches
	// nothing else.
	if err := store.PurgeRosterUser(ctx, "Alice@Example.com"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if remaining := byEmail(mustUsers(t, store)); len(remaining) != 0 {
		t.Errorf("users after purge = %v, want empty — nothing keyed by the email remains", remaining)
	}
	up, down, found, err := store.LoadTrafficTotals(ctx)
	if err != nil || !found || up != 5 || down != 50 {
		t.Errorf("xray totals after purge = %d/%d found=%t err=%v, want untouched", up, down, found, err)
	}

	// A purge is idempotent.
	if err := store.PurgeRosterUser(ctx, "alice@example.com"); err != nil {
		t.Errorf("re-purge = %v, want idempotent success", err)
	}

	// Re-adding the deleted email with its OLD Client ID starts a
	// brand-new user: fresh first_seen, zero totals, fresh history.
	record, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "alice@example.com", ClientID: "uuid-alice",
		Inbounds: []string{"vless-ws"}, Protocol: "VLESS", Security: "TLS",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("re-add after purge: %v", err)
	}
	if record.CreatedAt != now.Add(3*time.Second).Unix() {
		t.Errorf("re-add created_at = %d, want fresh — nothing was remembered", record.CreatedAt)
	}
	alice = byEmail(mustUsers(t, store))["alice@example.com"]
	if alice.Disabled || alice.UpBytesTotal != 0 || alice.DownBytesTotal != 0 || alice.LastSeen != nil {
		t.Errorf("re-added alice = %+v, want a fresh user with empty history", alice)
	}
}

// While a delete is pending the email and its Client ID stay claimed: a
// re-add of the email is a conflict (the purge may still strand a live
// credential), and another email taking the Client ID is one too. Both
// release only when the purge lands.
func TestDeletingEmailKeepsItsClaimsUntilPurged(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.MarkRosterDeleting(ctx, "alice@example.com", now.Add(time.Second)); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}

	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "alice@example.com", ClientID: "uuid-fresh", Inbounds: []string{},
	}, now.Add(2*time.Second)); !errors.Is(err, users.ErrEmailTaken) {
		t.Errorf("re-add a deleting email = %v, want ErrEmailTaken until the purge lands", err)
	}
	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "bob@example.com", ClientID: "UUID-ALICE", Inbounds: []string{},
	}, now.Add(2*time.Second)); !errors.Is(err, users.ErrClientIDTaken) {
		t.Errorf("claiming a deleting email's Client ID = %v, want ErrClientIDTaken", err)
	}

	if err := store.PurgeRosterUser(ctx, "alice@example.com"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	// Both claims released: the email re-adds — even with its old Client
	// ID — and the Client ID is free for anyone.
	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "alice@example.com", ClientID: "uuid-alice", Inbounds: []string{},
	}, now.Add(3*time.Second)); err != nil {
		t.Errorf("re-add after purge = %v, want a fresh user", err)
	}
}

// DeletingRosterRecords lists the rows awaiting their purge — the startup
// recovery read that re-queues a delete interrupted by a restart.
func TestDeletingRosterRecordsReturnsDeletingRows(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	addRoster(t, store, "bob@example.com", "uuid-bob", []string{"vless-ws"}, now)
	addRoster(t, store, "carol@example.com", "uuid-carol", nil, now)
	if err := store.MarkRosterDeleting(ctx, "alice@example.com", now); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	if err := store.MarkRosterDeleting(ctx, "bob@example.com", now); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	if err := store.DisableRosterUser(ctx, "carol@example.com", now); err != nil {
		t.Fatalf("disable: %v", err)
	}

	records, err := store.DeletingRosterRecords(ctx)
	if err != nil {
		t.Fatalf("deleting records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want the two deleting rows only", records)
	}
	emails := map[string]bool{}
	for _, record := range records {
		emails[record.Email] = true
	}
	if !emails["alice@example.com"] || !emails["bob@example.com"] {
		t.Errorf("records = %+v, want alice and bob — live and disabled rows excluded", records)
	}

	if err := store.PurgeRosterUser(ctx, "alice@example.com"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	records, err = store.DeletingRosterRecords(ctx)
	if err != nil {
		t.Fatalf("deleting records after purge: %v", err)
	}
	if len(records) != 1 || records[0].Email != "bob@example.com" {
		t.Errorf("records after purge = %+v, want bob only", records)
	}
}

// A database from before the delete column gains it in place, every flag
// defaulting to not-deleting.
func TestOpenMigratesRosterDeletionColumn(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	store, err := users.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Strip the column, as a database from before this change has it.
	db, err := openRawDB(path)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE roster DROP COLUMN deleting`); err != nil {
		t.Fatalf("downgrade roster: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	migrated, err := users.Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer func() { _ = migrated.Close() }()

	if _, err := migrated.RosterRecord(ctx, "alice@example.com"); err != nil {
		t.Errorf("alice after migration: %v, want her live — the new column defaults to not-deleting", err)
	}
	if err := migrated.MarkRosterDeleting(ctx, "alice@example.com", now); err != nil {
		t.Errorf("mark deleting after migration: %v", err)
	}
}

// The purge targets deleting rows only: a live user's rows are untouched
// even when the purge names their email (the roster's deleting mark gates
// both tables).
func TestPurgeRosterUserOnlyErasesDeletingRows(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	addRoster(t, store, "bob@example.com", "uuid-bob", []string{"vless-ws"}, now)
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("apply traffic: %v", err)
	}
	if err := store.MarkRosterDeleting(ctx, "bob@example.com", now.Add(time.Second)); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}

	if err := store.PurgeRosterUser(ctx, "alice@example.com"); err != nil {
		t.Fatalf("purge a live user: %v", err)
	}
	remaining := byEmail(mustUsers(t, store))
	if remaining["alice@example.com"].Email == "" || remaining["alice@example.com"].UpBytesTotal != 100 {
		t.Errorf("alice after a misaimed purge = %+v, want untouched — she is not deleting", remaining["alice@example.com"])
	}
	if _, err := store.RosterRecord(ctx, "alice@example.com"); err != nil {
		t.Errorf("alice's roster record after a misaimed purge: %v, want intact", err)
	}

	if err := store.PurgeRosterUser(ctx, "Bob@Example.com"); err != nil {
		t.Fatalf("purge a deleting user: %v", err)
	}
	if remaining := byEmail(mustUsers(t, store)); len(remaining) != 1 {
		t.Errorf("users after bob's purge = %v, want alice only", remaining)
	}
}

// Adoption treats a purged email as foreign (ADR-0007's accepted
// consequence, issue #59's acceptance): a config parse still carrying a
// deleted client adopts it as a brand-new user — live, fresh first_seen,
// zero totals, roster record with the config's Client ID and attachments.
func TestAdoptionTreatsAPurgedEmailAsForeign(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("apply traffic: %v", err)
	}
	if err := store.MarkRosterDeleting(ctx, "alice@example.com", now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	if err := store.PurgeRosterUser(ctx, "alice@example.com"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	// A stale config carrying the deleted client (old Client ID and all)
	// lands on the next parse: nothing remembers her.
	later := now.Add(1_000 * time.Second)
	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{
		Labels: map[string]xrayconfig.User{"alice@example.com": {Protocol: "VLESS", Security: "Reality"}},
		Clients: map[string]users.RosterClient{
			"alice@example.com": {ClientID: "uuid-alice", Inbounds: []string{"vless-vision", "vless-ws"}},
		},
	}, later); err != nil {
		t.Fatalf("apply stale parse: %v", err)
	}

	record, err := store.RosterRecord(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("adopted record: %v, want a brand-new roster member", err)
	}
	if record.ClientID != "uuid-alice" || !slices.Equal(record.Inbounds, []string{"vless-vision", "vless-ws"}) {
		t.Errorf("adopted record = %+v, want the config's credential and attachments", record)
	}
	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if alice.Disabled || alice.UpBytesTotal != 0 || alice.DownBytesTotal != 0 || alice.LastSeen != nil {
		t.Errorf("adopted alice = %+v, want a fresh user with empty history", alice)
	}
	if alice.FirstSeen != later.Unix() {
		t.Errorf("adopted first_seen = %d, want fresh (%d)", alice.FirstSeen, later.Unix())
	}
}
