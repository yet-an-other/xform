package users_test

import (
	"context"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
)

// Purge drops the collector's in-memory trace of one email — trackers,
// speed windows, seeded baseline, and unapplied deltas — so nothing the
// panel still remembers can resurrect a purged user's row (issue #59): a
// delta carried across the purge would re-insert the erased users row on
// the next flush.
func TestCollectorPurgeDropsInMemoryState(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	store := &flakyStore{inner: openMemoryStore(t)}
	traffic := &fakeTraffic{pages: [][]users.RawTraffic{
		{{Email: "alice@example.com", UpBytes: 5_000, DownBytes: 50_000}},
		{{Email: "alice@example.com", UpBytes: 5_500, DownBytes: 51_000}},
		{}, // the removal has landed: xray reports the email no more
	}}
	collector := users.NewCollector(traffic, unsupportedPresence, store).WithClock(func() time.Time { return now })

	if _, err := collector.Collect(context.Background()); err != nil {
		t.Fatalf("collect: %v", err)
	}

	// A failed poll leaves alice's delta unapplied — exactly what must not
	// survive a purge.
	store.fail = true
	if _, err := collector.Collect(context.Background()); err == nil {
		t.Fatal("collect succeeded with a failing store")
	}

	collector.Purge("alice@example.com")
	if err := store.inner.PurgeRosterUser(context.Background(), "alice@example.com"); err != nil {
		t.Fatalf("purge store rows: %v", err)
	}

	// The next poll carries no delta for the purged email — the row stays
	// gone.
	store.fail = false
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect after purge: %v", err)
	}
	if len(snapshot.Users) != 0 {
		t.Errorf("users after purge = %+v, want empty — the pending delta must not resurrect the row", snapshot.Users)
	}
}
