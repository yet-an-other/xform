package users_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The users table lists every connection shape a user has — one label per
// attached inbound, config order — so a user on both a vision TCP REALITY
// inbound and an XHTTP REALITY one carries both labels, not just the first
// inbound's.
func TestCollectorCarriesEveryInboundLabel(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
	}}
	parsed, err := xrayconfig.Parse([]byte(`{
		"inbounds": [
			{"tag": "vision", "protocol": "vless",
			 "settings": {"clients": [{"email": "alice@example.com", "id": "uuid-a", "flow": "xtls-rprx-vision"}]},
			 "streamSettings": {"network": "tcp", "security": "reality"}},
			{"tag": "xhttp", "protocol": "vless",
			 "settings": {"clients": [{"email": "alice@example.com", "id": "uuid-a"}]},
			 "streamSettings": {"network": "xhttp", "security": "reality"}}
		]
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).
		WithClock(func() time.Time { return now }).
		WithRoster(&fakeRoster{version: 1, roster: users.RosterParse{Labels: parsed}})

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	alice := byEmail(snapshot.Users)["alice@example.com"]
	want := []xrayconfig.Label{
		{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"},
		{Protocol: "VLESS", Security: "Reality", Transport: "xhttp"},
	}
	if !slices.Equal(alice.Labels, want) {
		t.Errorf("alice labels = %+v, want %+v", alice.Labels, want)
	}
}

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
		"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"}}},
		"bob@example.com":   {Labels: []xrayconfig.Label{{Protocol: "TROJAN", Security: "TLS", Transport: "tcp"}}},
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
	if !slices.Equal(alice.Labels, []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"}}) {
		t.Errorf("alice labels = %+v, want VLESS / XTLS-Reality", alice.Labels)
	}
	if alice.Disabled {
		t.Error("alice disabled = true, want false — she is in the config")
	}

	// bob appears automatically, before any traffic.
	if bob := got["bob@example.com"]; bob.UpBytesTotal != 0 || bob.Disabled {
		t.Errorf("bob = %+v, want zero totals, not gone", bob)
	}

	// erin still has counters in xray but is gone from the config.
	if erin := got["erin@example.com"]; !erin.Disabled {
		t.Error("erin disabled = false, want true — the config no longer names her")
	}

	// A config edit is picked up on the next poll: erin returns, alice's
	// security changed.
	roster.update(users.RosterParse{Labels: map[string]users.RosterUser{
		"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}},
		"erin@example.com":  {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}},
	}})
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got = byEmail(snapshot.Users)
	if alice := got["alice@example.com"]; len(alice.Labels) != 1 || alice.Labels[0].Security != "Reality" {
		t.Errorf("alice labels = %+v, want the edited Reality", alice.Labels)
	}
	if erin := got["erin@example.com"]; erin.Disabled {
		t.Error("erin disabled = true after returning to the config, want false")
	}
	if bob := got["bob@example.com"]; !bob.Disabled {
		t.Error("bob disabled = false after the config dropped him, want true — his row is retained")
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
	if alice.Disabled {
		t.Error("alice disabled = true, want false — a never-parsed config marks nobody gone")
	}
	if alice.Labels != nil {
		t.Errorf("alice labels = %+v, want none", alice.Labels)
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
		"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}},
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
	if len(alice.Labels) != 1 || alice.Labels[0].Protocol != "VLESS" {
		t.Errorf("alice labels = %+v, want VLESS — the roster carried into the recovered poll", alice.Labels)
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
		"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}},
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
	if alice := snapshot.Users[0]; !alice.Disabled {
		t.Error("alice disabled = false, want true — the config edit landed without waiting for xray")
	}

	// And once xray recovers there is no pending roster left to re-apply.
	traffic.err = nil
	now = now.Add(5 * time.Second)
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if alice := snapshot.Users[0]; !alice.Disabled {
		t.Error("alice disabled = false after recovery, want true — the roster stays applied")
	}
}

// Hand-adding a client to the config lands in the roster store on the next
// poll, without a panel restart: the users table shows the new user with
// their adopted Client ID and inbound attachments.
func TestCollectorAdoptsConfigClients(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{{}}}
	roster := &fakeRoster{version: 1, roster: users.RosterParse{
		Labels:  map[string]users.RosterUser{"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"}}}},
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
			"alice@example.com": {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"}}},
			"bob@example.com":   {Labels: []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}},
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
	if len(bob.Inbounds) != 1 || bob.Inbounds[0] != "vless-xhttp" || bob.Disabled || bob.UpBytesTotal != 0 {
		t.Errorf("bob = %+v, want [vless-xhttp], not gone, zero totals", bob)
	}
}
