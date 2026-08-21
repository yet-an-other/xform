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

func (f *flakyStore) ApplyDeltas(ctx context.Context, deltas []users.Delta, now time.Time) error {
	if f.fail {
		return errors.New("store down")
	}
	return f.inner.ApplyDeltas(ctx, deltas, now)
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
	collector := users.NewCollector(traffic, store).WithClock(func() time.Time { return now })

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
	collector := users.NewCollector(traffic, openMemoryStore(t)).WithClock(func() time.Time { return now })

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
	collector := users.NewCollector(traffic, openMemoryStore(t)).WithClock(func() time.Time { return now })

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
	collector := users.NewCollector(traffic, openMemoryStore(t)).WithClock(func() time.Time { return now })

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

func TestCollectorResumesTotalsAfterPanelRestart(t *testing.T) {
	store := openMemoryStore(t)
	now := time.Unix(1_780_000_000, 0)

	first := users.NewCollector(&fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
		{{Email: "alice@example.com", UpBytes: 6_000, DownBytes: 52_000}},
	}}, store).WithClock(func() time.Time { return now })
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
	}}, store).WithClock(func() time.Time { return now })
	snapshot, err := second.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	alice := snapshot.Users[0]
	if alice.UpBytesTotal != 6_000 || alice.DownBytesTotal != 52_000 {
		t.Errorf("alice totals = %d/%d after a panel restart, want 6000/52000 (resume, not re-seed)", alice.UpBytesTotal, alice.DownBytesTotal)
	}
}
