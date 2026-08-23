package users_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
)

// fakeTraffic plays back scripted raw per-user counters, one entry per poll.
type fakeTraffic struct {
	pages [][]users.RawTraffic
	err   error
	calls int
}

func (f *fakeTraffic) QueryUserTraffic(context.Context) ([]users.RawTraffic, error) {
	if f.err != nil {
		return nil, f.err
	}
	page := f.pages[min(f.calls, len(f.pages)-1)]
	f.calls++
	return page, nil
}

// fakePresence plays back scripted online sets, one entry per poll.
type fakePresence struct {
	pages     [][]users.Presence
	supported bool
	err       error
	calls     int
}

func (f *fakePresence) QueryPresence(context.Context) ([]users.Presence, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if len(f.pages) == 0 {
		return nil, f.supported, nil
	}
	page := f.pages[min(f.calls, len(f.pages)-1)]
	f.calls++
	return page, f.supported, nil
}

// unsupportedPresence is the old-xray degrade: no online RPCs.
var unsupportedPresence = &fakePresence{supported: false}

func openMemoryStore(t *testing.T) *users.Store {
	t.Helper()
	store, err := users.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// flakyStore wraps a real store with a fail switch — failure injection
// through the collector's persistence seam, with real SQLite underneath.
type flakyStore struct {
	inner *users.Store
	fail  bool
}

func (f *flakyStore) ExistingEmails(ctx context.Context) (map[string]bool, error) {
	if f.fail {
		return nil, errors.New("store down")
	}
	return f.inner.ExistingEmails(ctx)
}

func (f *flakyStore) ApplyDeltas(ctx context.Context, deltas []users.Delta, presence []users.Presence, now time.Time) error {
	if f.fail {
		return errors.New("store down")
	}
	return f.inner.ApplyDeltas(ctx, deltas, presence, now)
}

func (f *flakyStore) Users(ctx context.Context) ([]users.User, error) {
	return f.inner.Users(ctx)
}

func TestCollectorCarriesDeltasAcrossStoreFailures(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	store := &flakyStore{inner: openMemoryStore(t)}
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
		{{Email: "alice@example.com", UpBytes: 5_500, DownBytes: 51_000}},
	}}
	collector := users.NewCollector(traffic, unsupportedPresence, store).WithClock(func() time.Time { return now })

	// The store write fails: the poll errors, and the seed delta must not be
	// lost — in-memory tracker state already advanced, so only a carried
	// pending delta can keep the durable total whole.
	store.fail = true
	if _, err := collector.Collect(context.Background()); err == nil {
		t.Fatal("collect succeeded with a failing store")
	}
	store.fail = false
	now = now.Add(5 * time.Second)

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	alice := snapshot.Users[0]
	if alice.UpBytesTotal != 5_500 || alice.DownBytesTotal != 51_000 {
		t.Errorf("alice totals = %d/%d, want 5500/51000 — the failed poll's seed carries into the next poll", alice.UpBytesTotal, alice.DownBytesTotal)
	}
}

func TestCollectorAccumulatesPerUserTotalsAndSpeeds(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 12_000, DownBytes: 148_000}},
		{{Email: "alice@example.com", UpBytes: 12_500, DownBytes: 149_000}},
	}}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).WithClock(func() time.Time { return now })

	poll := func() users.Snapshot {
		t.Helper()
		snapshot, err := collector.Collect(context.Background())
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		now = now.Add(5 * time.Second)
		return snapshot
	}

	// First poll: the panel just met alice — her row seeds from xray's
	// current counters (no double count: the row did not exist), and there
	// is no speed sample yet.
	snapshot := poll()
	if snapshot.Stale {
		t.Error("stale = true on a healthy poll")
	}
	if len(snapshot.Users) != 1 {
		t.Fatalf("users = %d, want 1", len(snapshot.Users))
	}
	alice := snapshot.Users[0]
	if alice.UpBytesTotal != 12_000 || alice.DownBytesTotal != 148_000 {
		t.Errorf("alice totals = %d/%d, want the seed 12000/148000", alice.UpBytesTotal, alice.DownBytesTotal)
	}
	if alice.SpeedUpBps != 0 || alice.SpeedDownBps != 0 {
		t.Errorf("alice speeds = %d/%d, want 0 on the seed poll", alice.SpeedUpBps, alice.SpeedDownBps)
	}

	snapshot = poll()
	alice = snapshot.Users[0]
	if alice.UpBytesTotal != 12_500 || alice.DownBytesTotal != 149_000 {
		t.Errorf("alice totals = %d/%d, want 12500/149000", alice.UpBytesTotal, alice.DownBytesTotal)
	}
	if alice.SpeedUpBps != 100 || alice.SpeedDownBps != 200 {
		t.Errorf("alice speeds = %d/%d, want 100/200 (deltas over 5s)", alice.SpeedUpBps, alice.SpeedDownBps)
	}
}

func TestCollectorTotalsSurviveXrayRestarts(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
		{{Email: "alice@example.com", UpBytes: 6_000, DownBytes: 52_000}},
		{{Email: "alice@example.com", UpBytes: 300, DownBytes: 900}}, // xray restarted
	}}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).WithClock(func() time.Time { return now })

	var last users.Snapshot
	for range 3 {
		var err error
		last, err = collector.Collect(context.Background())
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		now = now.Add(5 * time.Second)
	}

	alice := last.Users[0]
	// 6000 + 300: the reset raw counts as the delta, never a decrease.
	if alice.UpBytesTotal != 6_300 || alice.DownBytesTotal != 52_900 {
		t.Errorf("alice totals = %d/%d after a counter reset, want 6300/52900", alice.UpBytesTotal, alice.DownBytesTotal)
	}
}

func TestCollectorServesStaleSnapshotWhenTrafficPollFails(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
	}}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).WithClock(func() time.Time { return now })

	if _, err := collector.Collect(context.Background()); err != nil {
		t.Fatalf("collect: %v", err)
	}

	traffic.err = errors.New("stats API down")
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect returned an error; the 200-always contract serves stale data: %v", err)
	}
	if !snapshot.Stale {
		t.Error("stale = false, want true when the traffic poll fails")
	}
	if len(snapshot.Users) != 1 || snapshot.Users[0].UpBytesTotal != 5_000 {
		t.Errorf("users = %+v, want the last-known totals from the store", snapshot.Users)
	}
	if snapshot.Users[0].SpeedUpBps != 0 || snapshot.Users[0].SpeedDownBps != 0 {
		t.Errorf("speeds = %d/%d, want 0 while stale", snapshot.Users[0].SpeedUpBps, snapshot.Users[0].SpeedDownBps)
	}
	if snapshot.CollectedAt != now.Unix() {
		t.Errorf("collected_at = %d, want the last successful poll %d", snapshot.CollectedAt, now.Unix())
	}

	traffic.err = nil
	now = now.Add(5 * time.Second)
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if snapshot.Stale {
		t.Error("stale = true after the poll recovered")
	}
}

func TestCollectorOverlaysLivePresence(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
	}}
	presence := &fakePresence{supported: true, pages: [][]users.Presence{
		{
			{Email: "alice@example.com", IPs: []string{"203.0.113.10"}, LastSeen: now.Unix()},
			// bob has no traffic counters yet — presence alone puts him on
			// the board.
			{Email: "bob@example.com", IPs: []string{"198.51.100.7", "203.0.113.99"}, LastSeen: now.Unix() - 3},
			// carol is online but the server reported no per-IP timestamps:
			// being online is being seen now.
			{Email: "carol@example.com"},
		},
	}}
	collector := users.NewCollector(traffic, presence, openMemoryStore(t)).WithClock(func() time.Time { return now })

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if snapshot.Stale {
		t.Error("stale = true on a healthy poll")
	}
	byEmail := userByEmail(snapshot.Users)
	if len(snapshot.Users) != 3 {
		t.Fatalf("users = %d, want 3 (presence upserts traffic-less users): %+v", len(snapshot.Users), snapshot.Users)
	}

	alice := byEmail["alice@example.com"]
	if !alice.Online {
		t.Error("alice online = false, want true")
	}
	if len(alice.IPs) != 1 || alice.IPs[0] != "203.0.113.10" {
		t.Errorf("alice IPs = %v, want [203.0.113.10]", alice.IPs)
	}
	if alice.LastSeen == nil || *alice.LastSeen != now.Unix() {
		t.Errorf("alice last_seen = %v, want %d", alice.LastSeen, now.Unix())
	}

	bob := byEmail["bob@example.com"]
	if !bob.Online || len(bob.IPs) != 2 {
		t.Errorf("bob online = %v IPs = %v, want online from two IPs", bob.Online, bob.IPs)
	}
	if bob.LastSeen == nil || *bob.LastSeen != now.Unix()-3 {
		t.Errorf("bob last_seen = %v, want the per-IP max %d", bob.LastSeen, now.Unix()-3)
	}
	if bob.UpBytesTotal != 0 || bob.DownBytesTotal != 0 {
		t.Errorf("bob totals = %d/%d, want 0/0 (online but no traffic yet)", bob.UpBytesTotal, bob.DownBytesTotal)
	}

	carol := byEmail["carol@example.com"]
	if !carol.Online || carol.IPs != nil {
		t.Errorf("carol online = %v IPs = %v, want online with unknown IPs", carol.Online, carol.IPs)
	}
	if carol.LastSeen == nil || *carol.LastSeen != now.Unix() {
		t.Errorf("carol last_seen = %v, want now %d (online without timestamps)", carol.LastSeen, now.Unix())
	}
}

func TestCollectorOmitsPresenceWhenXrayPredatesOnlineRPCs(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}}, // first contact: seed
		{{Email: "alice@example.com", UpBytes: 5_500, DownBytes: 51_000}}, // movement
	}}
	collector := users.NewCollector(traffic, unsupportedPresence, openMemoryStore(t)).WithClock(func() time.Time { return now })

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v — an old xray is a degrade, not an error", err)
	}
	alice := snapshot.Users[0]
	if alice.Online || alice.IPs != nil {
		t.Errorf("alice online = %v IPs = %v, want presence omitted (SPEC.md §3)", alice.Online, alice.IPs)
	}
	// The seed is a baseline import, not observed activity: last_seen stays
	// null until the panel sees movement (SPEC.md §5).
	if alice.LastSeen != nil {
		t.Errorf("alice last_seen = %v on the seed poll, want null", alice.LastSeen)
	}

	now = now.Add(5 * time.Second)
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	alice = snapshot.Users[0]
	// The traffic-delta heuristic still feeds last_seen: alice moved bytes
	// inside the window.
	if alice.LastSeen == nil || *alice.LastSeen != now.Unix() {
		t.Errorf("alice last_seen = %v, want %d from the delta heuristic", alice.LastSeen, now.Unix())
	}
}

func TestCollectorPresenceSurvivesDisconnect(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}}, // disconnected: counters stop
	}}
	presence := &fakePresence{supported: true, pages: [][]users.Presence{
		{{Email: "alice@example.com", IPs: []string{"203.0.113.10"}, LastSeen: now.Unix()}},
		{}, // alice disconnected
	}}
	collector := users.NewCollector(traffic, presence, openMemoryStore(t)).WithClock(func() time.Time { return now })

	if _, err := collector.Collect(context.Background()); err != nil {
		t.Fatalf("collect: %v", err)
	}
	now = now.Add(5 * time.Second)
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	alice := snapshot.Users[0]
	if alice.Online || alice.IPs != nil {
		t.Errorf("alice online = %v IPs = %v after disconnect, want offline with no online IPs", alice.Online, alice.IPs)
	}
	// xray forgets last_seen on disconnect; the panel remembers it durably.
	if alice.LastSeen == nil || *alice.LastSeen != now.Unix()-5 {
		t.Errorf("alice last_seen = %v, want the durable %d", alice.LastSeen, now.Unix()-5)
	}
}

func TestCollectorServesLastKnownPresenceWhenStale(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
	}}
	presence := &fakePresence{supported: true, pages: [][]users.Presence{
		{{Email: "alice@example.com", IPs: []string{"203.0.113.10"}, LastSeen: now.Unix()}},
	}}
	collector := users.NewCollector(traffic, presence, openMemoryStore(t)).WithClock(func() time.Time { return now })

	if _, err := collector.Collect(context.Background()); err != nil {
		t.Fatalf("collect: %v", err)
	}

	traffic.err = errors.New("stats API down")
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect returned an error; the 200-always contract serves stale data: %v", err)
	}
	if !snapshot.Stale {
		t.Error("stale = false, want true when the traffic poll fails")
	}
	alice := snapshot.Users[0]
	// Nobody is verifiably online while xray is unreachable, but the
	// last-known IPs and last_seen stay visible, flagged stale (SPEC.md §3).
	if alice.Online {
		t.Error("alice online = true while stale, want false")
	}
	if len(alice.IPs) != 1 || alice.IPs[0] != "203.0.113.10" {
		t.Errorf("alice IPs = %v, want the last-known [203.0.113.10]", alice.IPs)
	}
	if alice.LastSeen == nil || *alice.LastSeen != now.Unix() {
		t.Errorf("alice last_seen = %v, want the durable %d", alice.LastSeen, now.Unix())
	}
}

func TestCollectorToleratesPresencePollFailure(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
		{{Email: "alice@example.com", UpBytes: 5_500, DownBytes: 51_000}},
	}}
	presence := &fakePresence{supported: true, pages: [][]users.Presence{
		{{Email: "alice@example.com", IPs: []string{"203.0.113.10"}, LastSeen: now.Unix()}},
	}}
	collector := users.NewCollector(traffic, presence, openMemoryStore(t)).WithClock(func() time.Time { return now })

	if _, err := collector.Collect(context.Background()); err != nil {
		t.Fatalf("collect: %v", err)
	}

	// The presence RPC fails while traffic still flows: presence is omitted
	// for the poll, the snapshot is not stale, and totals keep accumulating.
	presence.err = errors.New("online RPC reset")
	now = now.Add(5 * time.Second)
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v — a presence failure is not a poll failure", err)
	}
	if snapshot.Stale {
		t.Error("stale = true, want false — traffic is fresh")
	}
	alice := snapshot.Users[0]
	if alice.Online || alice.IPs != nil {
		t.Errorf("alice online = %v IPs = %v, want presence omitted on the failed poll", alice.Online, alice.IPs)
	}
	if alice.UpBytesTotal != 5_500 || alice.DownBytesTotal != 51_000 {
		t.Errorf("alice totals = %d/%d, want 5500/51000 (traffic unaffected)", alice.UpBytesTotal, alice.DownBytesTotal)
	}

	presence.err = nil
	now = now.Add(5 * time.Second)
	snapshot, err = collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !snapshot.Users[0].Online {
		t.Error("alice online = false after the presence poll recovered")
	}
}

func TestCollectorResumesTotalsAfterPanelRestart(t *testing.T) {
	store := openMemoryStore(t)
	now := time.Unix(1_780_000_000, 0)

	first := users.NewCollector(&fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
		{{Email: "alice@example.com", UpBytes: 6_000, DownBytes: 52_000}},
	}}, unsupportedPresence, store).WithClock(func() time.Time { return now })
	for range 2 {
		if _, err := first.Collect(context.Background()); err != nil {
			t.Fatalf("collect: %v", err)
		}
		now = now.Add(5 * time.Second)
	}

	// The panel restarted: fresh collector, empty in-memory trackers, same
	// store. xray kept counting meanwhile (7000/54000). Resuming must not
	// re-credit the raw counters — the row already holds those bytes.
	second := users.NewCollector(&fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 7_000, DownBytes: 54_000}},
	}}, unsupportedPresence, store).WithClock(func() time.Time { return now })
	snapshot, err := second.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	alice := snapshot.Users[0]
	if alice.UpBytesTotal != 6_000 || alice.DownBytesTotal != 52_000 {
		t.Errorf("alice totals = %d/%d after a panel restart, want 6000/52000 (resume, not re-seed)", alice.UpBytesTotal, alice.DownBytesTotal)
	}
}
