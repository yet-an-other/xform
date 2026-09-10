package users_test

import (
	"context"
	"database/sql"
	"slices"
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

// openRawDB opens a store database directly — the schema-migration tests
// reshape columns the way an older database has them. The sqlite driver is
// registered by the users package import.
func openRawDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
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
	roster := &users.RosterParse{Labels: map[string]xrayconfig.User{
		"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}},
		"bob@example.com":   {Labels: []xrayconfig.Label{{Protocol: "TROJAN", Security: "TLS", Transport: "tcp"}}},
	}}
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
	if !slices.Equal(alice.Labels, []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}) {
		t.Errorf("alice labels = %+v, want VLESS / Reality", alice.Labels)
	}
	if alice.UpBytesTotal != 100 || alice.DownBytesTotal != 1_000 {
		t.Errorf("alice totals = %d/%d, want her 100/1000 history untouched", alice.UpBytesTotal, alice.DownBytesTotal)
	}
	if alice.LastSeen == nil || *alice.LastSeen != now.Unix() {
		t.Errorf("alice last_seen = %v, want the traffic poll's %d", alice.LastSeen, now.Unix())
	}
	if alice.Disabled {
		t.Error("alice disabled = true, want false — she is in the config")
	}

	// bob appears automatically, with zero totals, before his first byte.
	bob := got["bob@example.com"]
	if len(bob.Labels) != 1 || bob.Labels[0].Protocol != "TROJAN" {
		t.Errorf("bob labels = %+v, want TROJAN", bob.Labels)
	}
	if bob.UpBytesTotal != 0 || bob.DownBytesTotal != 0 || bob.LastSeen != nil {
		t.Errorf("bob = %+v, want zero totals and never seen", bob)
	}
	if bob.Disabled {
		t.Error("bob disabled = true, want false")
	}

	// erin is gone: retained, history intact, marked.
	erin := got["erin@example.com"]
	if !erin.Disabled {
		t.Error("erin disabled = false, want true — she was edited out of the config")
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

	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{Labels: map[string]xrayconfig.User{
		"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}},
	}}, now); err != nil {
		t.Fatalf("apply first roster: %v", err)
	}
	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{Labels: map[string]xrayconfig.User{}}, now.Add(time.Minute)); err != nil {
		t.Fatalf("apply empty roster: %v", err)
	}

	got := byEmail(mustUsers(t, store))
	if !got["alice@example.com"].Disabled {
		t.Fatal("alice disabled = false, want true after the config dropped everyone")
	}

	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{Labels: map[string]xrayconfig.User{
		"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"}}},
	}}, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("apply restored roster: %v", err)
	}
	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if alice.Disabled {
		t.Error("alice disabled = true, want false after returning to the config")
	}
	if len(alice.Labels) != 1 || alice.Labels[0].Security != "XTLS-Reality" {
		t.Errorf("alice labels = %+v, want the edited XTLS-Reality", alice.Labels)
	}
}

// Databases from before per-inbound labels carry protocol/security columns;
// Open folds the pair into a one-entry labels array (transport unknown —
// the next config parse resyncs the true list) and drops the old columns.
func TestOpenMigratesLabelColumns(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	store, err := users.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	addRoster(t, store, "alice@example.com", "uuid-alice", []string{"vless-vision"}, now)
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "erin@example.com", Up: 50, Down: 500, SeenNow: true}, // no labels yet
	}, nil, nil, now); err != nil {
		t.Fatalf("apply traffic poll: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Downgrade to the single-pair schema, as a database from before this
	// change has it.
	db, err := openRawDB(path)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	for _, statement := range []string{
		`ALTER TABLE users DROP COLUMN labels`,
		`ALTER TABLE users ADD COLUMN protocol TEXT`,
		`ALTER TABLE users ADD COLUMN security TEXT`,
		`UPDATE users SET protocol = 'VLESS', security = 'XTLS-Reality' WHERE email = 'alice@example.com'`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("downgrade users: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	migrated, err := users.Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer func() { _ = migrated.Close() }()

	got := byEmail(mustUsers(t, migrated))
	if !slices.Equal(got["alice@example.com"].Labels, []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: ""}}) {
		t.Errorf("alice labels = %+v, want the folded single pair", got["alice@example.com"].Labels)
	}
	if got["erin@example.com"].Labels != nil {
		t.Errorf("erin labels = %+v, want none — her pair was null", got["erin@example.com"].Labels)
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

// Adoption (user-management spec §4): every VLESS client found in the config
// joins the roster store with its Client ID and inbound attachments, and the
// users table exposes them. Users the config never adopted — a Trojan user,
// or traffic from a client the config does not name — carry no roster data.
func TestStoreAdoptsConfigClients(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	// erin has traffic but is in no config: she is nobody the panel manages.
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "erin@example.com", Up: 50, Down: 500, SeenNow: true},
	}, nil, nil, now); err != nil {
		t.Fatalf("apply traffic poll: %v", err)
	}

	roster := &users.RosterParse{
		Labels: map[string]xrayconfig.User{
			"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"}}},
			"carol@example.com": {Labels: []xrayconfig.Label{{Protocol: "TROJAN", Security: "TLS", Transport: "tcp"}}},
		},
		Clients: map[string]xrayconfig.Client{
			"alice@example.com": {ClientID: "alice-uuid", Inbounds: []string{"vless-vision", "vless-xhttp"}},
		},
	}
	if err := store.ApplyPoll(ctx, nil, nil, roster, now.Add(5*time.Second)); err != nil {
		t.Fatalf("apply roster sync: %v", err)
	}

	got := byEmail(mustUsers(t, store))
	alice := got["alice@example.com"]
	if alice.ClientID == nil || *alice.ClientID != "alice-uuid" {
		t.Errorf("alice Client ID = %v, want the adopted alice-uuid", alice.ClientID)
	}
	if !slices.Equal(alice.Inbounds, []string{"vless-vision", "vless-xhttp"}) {
		t.Errorf("alice inbounds = %v, want her two attachments", alice.Inbounds)
	}

	// carol is a Trojan user: labelled, but nothing to adopt.
	if carol := got["carol@example.com"]; carol.ClientID != nil || carol.Inbounds != nil {
		t.Errorf("carol = Client ID %v, inbounds %v; want both null for a non-VLESS user", carol.ClientID, carol.Inbounds)
	}
	if erin := got["erin@example.com"]; erin.ClientID != nil || erin.Inbounds != nil {
		t.Errorf("erin = Client ID %v, inbounds %v; want both null — the config never adopted her", erin.ClientID, erin.Inbounds)
	}
}

// Re-reading the config adopts additively and idempotently (user-management
// spec §4): new attachments union in, the store's Client ID wins over a
// conflicting config edit, and an unchanged re-read changes nothing.
func TestStoreAdoptionIsAdditiveAndIdempotent(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	adopt := &users.RosterParse{
		Labels:  map[string]xrayconfig.User{"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}}},
		Clients: map[string]xrayconfig.Client{"alice@example.com": {ClientID: "alice-uuid", Inbounds: []string{"vless-vision"}}},
	}
	if err := store.ApplyPoll(ctx, nil, nil, adopt, now); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	// A hand edit attaches alice to a second inbound — and rewrites her
	// Client ID. The attachment unions in; the store's Client ID stands.
	handEdit := &users.RosterParse{
		Labels:  map[string]xrayconfig.User{"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}}},
		Clients: map[string]xrayconfig.Client{"alice@example.com": {ClientID: "rewritten-uuid", Inbounds: []string{"vless-vision", "vless-xhttp"}}},
	}
	if err := store.ApplyPoll(ctx, nil, nil, handEdit, now.Add(time.Minute)); err != nil {
		t.Fatalf("adopt hand edit: %v", err)
	}
	alice := byEmail(mustUsers(t, store))["alice@example.com"]
	if alice.ClientID == nil || *alice.ClientID != "alice-uuid" {
		t.Errorf("alice Client ID = %v, want the store's alice-uuid to win over the config rewrite", alice.ClientID)
	}
	if !slices.Equal(alice.Inbounds, []string{"vless-vision", "vless-xhttp"}) {
		t.Errorf("alice inbounds = %v, want the union of both attachments", alice.Inbounds)
	}

	// An unchanged re-read changes nothing in the store.
	before := mustUsers(t, store)
	if err := store.ApplyPoll(ctx, nil, nil, handEdit, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("re-adopt unchanged: %v", err)
	}
	after := mustUsers(t, store)
	if !slices.EqualFunc(before, after, func(a, b users.User) bool {
		return a.Email == b.Email &&
			((a.ClientID == nil && b.ClientID == nil) || (a.ClientID != nil && b.ClientID != nil && *a.ClientID == *b.ClientID)) &&
			slices.Equal(a.Inbounds, b.Inbounds) && a.FirstSeen == b.FirstSeen
	}) {
		t.Errorf("re-reading an unchanged config changed the store:\nbefore = %+v\nafter  = %+v", before, after)
	}
}
