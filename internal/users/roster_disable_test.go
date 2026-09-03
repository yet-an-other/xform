package users_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The disable mutation's store half (ADR-0007, user-management spec §3–§4,
// CONTEXT.md Disabled user): the roster row is flagged disabled — never
// erased — the dashboard row keeps its history behind the disabled badge,
// and the roster record reads as absent so edit answers not-found.
func TestDisableRosterUserFlagsDisabledAndKeepsHistory(t *testing.T) {
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

	if err := store.DisableRosterUser(ctx, "Alice@Example.com", now.Add(2*time.Second)); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// The roster record is gone from the live set; a re-disable is a no-op
	// success.
	if _, err := store.RosterRecord(ctx, "alice@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("record after disable = %v, want ErrRosterNotFound", err)
	}
	if err := store.DisableRosterUser(ctx, "alice@example.com", now.Add(3*time.Second)); err != nil {
		t.Errorf("re-disable = %v, want idempotent success", err)
	}

	// The dashboard row stays: disabled, history intact, roster fields null.
	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if !alice.Disabled {
		t.Error("a disabled user must read disabled")
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
	if !alice.Disabled || alice.ClientID != nil {
		t.Errorf("after the drift parse alice = %+v, want still disabled and out of the roster", alice)
	}
}

// The disabled record stays readable — the enable mutation's before-state —
// with its credential and attachments intact; an unknown email is not found.
func TestDisabledRosterRecordKeepsCredentialAndAttachments(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.DisableRosterUser(ctx, "alice@example.com", now.Add(time.Second)); err != nil {
		t.Fatalf("disable: %v", err)
	}

	record, err := store.DisabledRosterRecord(ctx, "Alice@Example.com")
	if err != nil {
		t.Fatalf("disabled record: %v", err)
	}
	if record.Email != "alice@example.com" || record.ClientID != "uuid-alice" ||
		len(record.Inbounds) != 1 || record.Inbounds[0] != "vless-vision" {
		t.Errorf("disabled record = %+v, want the stored credential and attachments", record)
	}
	if _, err := store.DisabledRosterRecord(ctx, "nobody@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("disabled record for a stranger = %v, want ErrRosterNotFound", err)
	}
	addRoster(t, store, "bob@example.com", "uuid-bob", nil, now)
	if _, err := store.DisabledRosterRecord(ctx, "bob@example.com"); err == nil {
		t.Error("a live user must not read as disabled")
	}
}

// Enable revives a disabled user in place: the roster row sheds disabled —
// credential and attachments kept — the dashboard row rejoins its history,
// and idempotent re-enables write nothing new. An unknown email is not
// found (user-management spec §3, ADR-0007).
func TestEnableRosterUserRevivesInPlace(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("apply traffic: %v", err)
	}
	if err := store.DisableRosterUser(ctx, "alice@example.com", now.Add(2*time.Second)); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := store.EnableRosterUser(ctx, "nobody@example.com", now.Add(3*time.Second)); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("enable a stranger = %v, want ErrRosterNotFound", err)
	}

	record, err := store.EnableRosterUser(ctx, "Alice@Example.com", now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if record.ClientID != "uuid-alice" || len(record.Inbounds) != 1 || record.Inbounds[0] != "vless-vision" {
		t.Errorf("enabled record = %+v, want the stored credential and attachments", record)
	}

	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if alice.Disabled || alice.UpBytesTotal != 100 || alice.DownBytesTotal != 1_000 {
		t.Errorf("alice = %+v, want enabled, history rejoined", alice)
	}
	if alice.ClientID == nil || *alice.ClientID != "uuid-alice" {
		t.Errorf("row Client ID = %v, want the stored credential", alice.ClientID)
	}

	// Idempotent: an enable of a live user is a plain success.
	if _, err := store.EnableRosterUser(ctx, "alice@example.com", now.Add(5*time.Second)); err != nil {
		t.Errorf("re-enable = %v, want idempotent success", err)
	}

	// And adoption resumes: a config parse merges attachments again.
	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{
		Labels: map[string]xrayconfig.User{"alice@example.com": {Protocol: "VLESS", Security: "Reality"}},
		Clients: map[string]users.RosterClient{
			"alice@example.com": {ClientID: "uuid-alice", Inbounds: []string{"vless-vision", "vless-xhttp"}},
		},
	}, now.Add(6*time.Second)); err != nil {
		t.Fatalf("apply adoption: %v", err)
	}
	live, err := store.RosterRecord(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("record after adoption: %v", err)
	}
	if len(live.Inbounds) != 2 {
		t.Errorf("inbounds after adoption = %v, want the merged pair", live.Inbounds)
	}
}

// Re-adding a disabled email revives it: the roster row sheds disabled, the
// dashboard row rejoins its history (user-management spec §3, ADR-0007).
func TestAddRosterUserRevivesADisabledUser(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("apply traffic: %v", err)
	}
	if err := store.DisableRosterUser(ctx, "alice@example.com", now.Add(2*time.Second)); err != nil {
		t.Fatalf("disable: %v", err)
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
	if alice.Disabled || alice.UpBytesTotal != 100 || alice.DownBytesTotal != 1_000 {
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

// A disabled user's Client ID stays roster-wide taken — the history row
// keeps its claim (spec §5 uniqueness holds across disabled rows).
func TestDisableKeepsClientIDUniquenessClaim(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", nil, now)
	if err := store.DisableRosterUser(ctx, "alice@example.com", now.Add(time.Second)); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if _, err := store.AddRosterUser(ctx, users.NewRosterUser{
		Email: "bob@example.com", ClientID: "UUID-ALICE", Inbounds: []string{},
	}, now.Add(2*time.Second)); !errors.Is(err, users.ErrClientIDTaken) {
		t.Errorf("reusing a disabled user's Client ID = %v, want ErrClientIDTaken", err)
	}
}

// The convergence read (user-management spec §4): every live roster record,
// disabled rows excluded — convergence re-applies the Roster, and disabled
// users are history, not roster members.
func TestRosterRecordsReturnsLiveRecordsOnly(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "bob@example.com", "uuid-bob", []string{"vless-ws"}, now)
	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.DisableRosterUser(ctx, "bob@example.com", now.Add(time.Second)); err != nil {
		t.Fatalf("disable: %v", err)
	}

	records, err := store.RosterRecords(ctx)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(records) != 1 || records[0].Email != "alice@example.com" || records[0].ClientID != "uuid-alice" {
		t.Errorf("records = %+v, want alice only, disabled bob excluded", records)
	}
}

// A database from before the disable rename keeps its data: the gone
// columns migrate in place to disabled, flags carried over.
func TestOpenMigratesGoneColumnsToDisabled(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	store, err := users.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	// Shape the legacy state with today's vocabulary: a disabled user.
	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.DisableRosterUser(ctx, "alice@example.com", now.Add(time.Second)); err != nil {
		t.Fatalf("disable: %v", err)
	}
	addRoster(t, store, "bob@example.com", "uuid-bob", []string{"vless-ws"}, now)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Rename the columns back to gone, as an old database has them.
	db, err := openRawDB(path)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	for _, table := range []string{"users", "roster"} {
		if _, err := db.ExecContext(ctx, `ALTER TABLE `+table+` RENAME COLUMN disabled TO gone`); err != nil {
			t.Fatalf("downgrade %s: %v", table, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	migrated, err := users.Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer migrated.Close()

	usersByMail := byEmail(mustUsers(t, migrated))
	if !usersByMail["alice@example.com"].Disabled {
		t.Error("alice must migrate disabled")
	}
	if usersByMail["bob@example.com"].Disabled {
		t.Error("bob must migrate live")
	}
	if _, err := migrated.RosterRecord(ctx, "alice@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("alice's roster record after migration = %v, want ErrRosterNotFound", err)
	}
	if _, err := migrated.DisabledRosterRecord(ctx, "alice@example.com"); err != nil {
		t.Errorf("alice's disabled record after migration: %v", err)
	}
}
