package users_test

import (
	"context"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
)

// fakeRoster plays back scripted config parses; the version bumps whenever
// the roster changes, mirroring the xrayconfig watcher.
type fakeRoster struct {
	roster  users.RosterParse
	version uint64
}

func (f *fakeRoster) Roster() (users.RosterParse, uint64) {
	return f.roster, f.version
}

func (f *fakeRoster) update(roster users.RosterParse) {
	f.roster = roster
	f.version++
}

// The config parse reaches the users table through the poll (SPEC.md §3
// step 4): config-defined users gain protocol · security labels, new users
// appear before their first byte, and users edited out of the config become
// gone.
func TestCollectorSyncsTheConfigRoster(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{
			{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000},
			{Email: "erin@example.com", UpBytes: 100, DownBytes: 900}, // in xray, not in the config
		},
	}}
	roster := &fakeRoster{version: 1, roster: users.RosterParse{Labels: map[string]users.RosterUser{
		"alice@example.com": {Protocol: "VLESS", Security: "XTLS-Reality"},
		"bob@example.com":   {Protocol: "TROJAN", Security: "TLS"},
	}}}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).
		WithClock(func() time.Time { return now }).
		WithRoster(roster)

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := byEmail(snapshot.Users)
	if len(got) != 3 {
		t.Fatalf("users = %d, want 3 (alice, erin, and bob from the config)", len(got))
	}

	alice := got["alice@example.com"]
	if alice.Protocol == nil || *alice.Protocol != "VLESS" || alice.Security == nil || *alice.Security != "XTLS-Reality" {
		t.Errorf("alice labels = %v / %v, want VLESS / XTLS-Reality", alice.Protocol, alice.Security)
	}
	if alice.Gone {
		t.Error("alice gone = true, want false — she is in the config")
	}

	// bob appears automatically, before any traffic.
	if bob := got["bob@example.com"]; bob.UpBytesTotal != 0 || bob.Gone {
		t.Errorf("bob = %+v, want zero totals, not gone", bob)
	}

	// erin still has counters in xray but is gone from the config.
	if erin := got["erin@example.com"]; !erin.Gone {
		t.Error("erin gone = false, want true — the config no longer names her")
	}

	// A config edit is picked up on the next poll: erin returns, alice's
	// security changed.
	roster.update(users.RosterParse{Labels: map[string]users.RosterUser{
		"alice@example.com": {Protocol: "VLESS", Security: "Reality"},
		"erin@example.com":  {Protocol: "VLESS", Security: "Reality"},
	}})
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got = byEmail(snapshot.Users)
	if alice := got["alice@example.com"]; alice.Security == nil || *alice.Security != "Reality" {
		t.Errorf("alice security = %v, want the edited Reality", alice.Security)
	}
	if erin := got["erin@example.com"]; erin.Gone {
		t.Error("erin gone = true after returning to the config, want false")
	}
	if bob := got["bob@example.com"]; !bob.Gone {
		t.Error("bob gone = false after the config dropped him, want true — his row is retained")
	}
}

// Version 0 means the config never parsed: the collector must not sync an
// empty roster over the store (everyone would become gone).
func TestCollectorIgnoresANeverParsedConfig(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
	}}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).
		WithClock(func() time.Time { return now }).
		WithRoster(&fakeRoster{}) // version 0: no roster yet

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	alice := snapshot.Users[0]
	if alice.Gone {
		t.Error("alice gone = true, want false — a never-parsed config marks nobody gone")
	}
	if alice.Protocol != nil || alice.Security != nil {
		t.Errorf("alice labels = %v / %v, want null", alice.Protocol, alice.Security)
	}
}

// Like carried deltas, a roster that cannot be persisted stays pending and
// lands with the next successful poll.
func TestCollectorCarriesRosterAcrossStoreFailures(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	store := &flakyStore{inner: openMemoryStore(t)}
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
	}}
	roster := &fakeRoster{version: 1, roster: users.RosterParse{Labels: map[string]users.RosterUser{
		"alice@example.com": {Protocol: "VLESS", Security: "Reality"},
	}}}
	collector := users.NewCollector(traffic, unsupportedPresence, store).
		WithClock(func() time.Time { return now }).
		WithRoster(roster)

	store.fail = true
	if _, err := collector.Collect(context.Background()); err == nil {
		t.Fatal("collect succeeded with a failing store")
	}
	store.fail = false

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	alice := snapshot.Users[0]
	if alice.Protocol == nil || *alice.Protocol != "VLESS" {
		t.Errorf("alice protocol = %v, want VLESS — the roster carried into the recovered poll", alice.Protocol)
	}
}

// Config edits land even while xray is unreachable: the roster flushes on
// its own, and the stale snapshot shows the fresh labels.
func TestCollectorFlushesRosterWhileStale(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
	}}
	roster := &fakeRoster{version: 1, roster: users.RosterParse{Labels: map[string]users.RosterUser{
		"alice@example.com": {Protocol: "VLESS", Security: "Reality"},
	}}}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).
		WithClock(func() time.Time { return now }).
		WithRoster(roster)

	if _, err := collector.Collect(context.Background()); err != nil {
		t.Fatalf("collect: %v", err)
	}

	traffic.err = context.DeadlineExceeded
	roster.update(users.RosterParse{Labels: map[string]users.RosterUser{}}) // alice edited out mid-outage
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect returned an error; the 200-always contract serves stale data: %v", err)
	}
	if !snapshot.Stale {
		t.Error("stale = false, want true when the traffic poll fails")
	}
	if alice := snapshot.Users[0]; !alice.Gone {
		t.Error("alice gone = false, want true — the config edit landed without waiting for xray")
	}

	// And once xray recovers there is no pending roster left to re-apply.
	traffic.err = nil
	now = now.Add(5 * time.Second)
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if alice := snapshot.Users[0]; !alice.Gone {
		t.Error("alice gone = false after recovery, want true — the roster stays applied")
	}
}

// Hand-adding a client to the config lands in the roster store on the next
// poll, without a panel restart: the users table shows the new user with
// their adopted Client ID and inbound attachments.
func TestCollectorAdoptsConfigClients(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{{}}}
	roster := &fakeRoster{version: 1, roster: users.RosterParse{
		Labels:  map[string]users.RosterUser{"alice@example.com": {Protocol: "VLESS", Security: "XTLS-Reality"}},
		Clients: map[string]users.RosterClient{"alice@example.com": {ClientID: "alice-uuid", Inbounds: []string{"vless-vision"}}},
	}}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).
		WithClock(func() time.Time { return now }).
		WithRoster(roster)

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	alice := snapshot.Users[0]
	if alice.ClientID == nil || *alice.ClientID != "alice-uuid" {
		t.Errorf("alice Client ID = %v, want the adopted alice-uuid", alice.ClientID)
	}
	if len(alice.Inbounds) != 1 || alice.Inbounds[0] != "vless-vision" {
		t.Errorf("alice inbounds = %v, want [vless-vision]", alice.Inbounds)
	}

	// A hand edit attaches alice to a second inbound and adds bob.
	roster.update(users.RosterParse{
		Labels: map[string]users.RosterUser{
			"alice@example.com": {Protocol: "VLESS", Security: "XTLS-Reality"},
			"bob@example.com":   {Protocol: "VLESS", Security: "Reality"},
		},
		Clients: map[string]users.RosterClient{
			"alice@example.com": {ClientID: "alice-uuid", Inbounds: []string{"vless-vision", "vless-xhttp"}},
			"bob@example.com":   {ClientID: "bob-uuid", Inbounds: []string{"vless-xhttp"}},
		},
	})
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := byEmail(snapshot.Users)
	alice = got["alice@example.com"]
	if len(alice.Inbounds) != 2 || alice.Inbounds[0] != "vless-vision" || alice.Inbounds[1] != "vless-xhttp" {
		t.Errorf("alice inbounds = %v, want the union [vless-vision vless-xhttp]", alice.Inbounds)
	}
	bob := got["bob@example.com"]
	if bob.ClientID == nil || *bob.ClientID != "bob-uuid" {
		t.Errorf("bob Client ID = %v, want the adopted bob-uuid", bob.ClientID)
	}
	if len(bob.Inbounds) != 1 || bob.Inbounds[0] != "vless-xhttp" || bob.Gone || bob.UpBytesTotal != 0 {
		t.Errorf("bob = %+v, want [vless-xhttp], not gone, zero totals", bob)
	}
}
