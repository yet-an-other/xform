package xraystatus_test

import (
	"context"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/xraystatus"
)

// fakeTotalsStore is an in-memory TotalsStore.
type fakeTotalsStore struct {
	up, down uint64
	found    bool
	err      error
	saves    int
}

func (f *fakeTotalsStore) LoadTrafficTotals(context.Context) (up, down uint64, found bool, err error) {
	return f.up, f.down, f.found, f.err
}

func (f *fakeTotalsStore) SaveTrafficTotals(_ context.Context, up, down uint64) error {
	if f.err != nil {
		return f.err
	}
	f.up, f.down, f.found, f.saves = up, down, true, f.saves+1
	return nil
}

// The xray-row totals are the panel's durable totals too (SPEC.md §3): a
// panel restart — even combined with an xray restart — must resume them,
// never reset to xray's current counters.
func TestTrafficTotalsSurvivePanelAndXrayRestarts(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	clock := func() time.Time { return now }
	active := &fakeUnit{info: xraystatus.UnitInfo{ActiveState: "active", ActiveSince: now}}
	store := &fakeTotalsStore{}

	// The panel boots against a long-running xray: the totals seed from
	// xray's current counters (first contact, nothing persisted yet)…
	collector := xraystatus.NewCollector(
		active, fakeVersion{},
		&scriptedStats{raws: [][2]uint64{{5_000, 50_000}}},
		"xray.service",
	).WithClock(clock).WithTotalsStore(store)

	status, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if status.TotalUpBytes != 5_000 || status.TotalDownBytes != 50_000 {
		t.Fatalf("totals = %d/%d, want the seed 5000/50000", status.TotalUpBytes, status.TotalDownBytes)
	}

	// …xray restarts (its counters reset to near zero) AND the panel
	// restarts (fresh collector, in-memory trackers gone). The durable
	// totals resume from the store — never a reset.
	restarted := xraystatus.NewCollector(
		active, fakeVersion{},
		&scriptedStats{raws: [][2]uint64{{300, 900}, {900, 2_900}}},
		"xray.service",
	).WithClock(clock).WithTotalsStore(store)

	status, err = restarted.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect after restarts: %v", err)
	}
	if status.TotalUpBytes != 5_000 || status.TotalDownBytes != 50_000 {
		t.Errorf("totals = %d/%d after a panel+xray restart, want the durable 5000/50000 — never a reset", status.TotalUpBytes, status.TotalDownBytes)
	}

	// …and the new epoch's traffic accumulates onto them from there.
	now = now.Add(5 * time.Second)
	status, err = restarted.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if status.TotalUpBytes != 5_600 || status.TotalDownBytes != 52_000 {
		t.Errorf("totals = %d/%d, want 5600/52000 (durable + new-epoch delta)", status.TotalUpBytes, status.TotalDownBytes)
	}
}

// A totals-store failure degrades durability, not the panel: in-memory
// totals keep accumulating and the next successful poll persists them.
func TestTrafficTotalsPersistAfterStoreRecovery(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	clock := func() time.Time { return now }
	active := &fakeUnit{info: xraystatus.UnitInfo{ActiveState: "active", ActiveSince: now}}
	store := &fakeTotalsStore{}
	stats := &scriptedStats{raws: [][2]uint64{{1_000, 10_000}, {1_500, 11_000}}}

	collector := xraystatus.NewCollector(active, fakeVersion{}, stats, "xray.service").
		WithClock(clock).WithTotalsStore(store)

	if _, err := collector.Collect(context.Background()); err != nil {
		t.Fatalf("collect: %v", err)
	}
	store.err = context.DeadlineExceeded
	now = now.Add(5 * time.Second)
	status, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if status.TotalUpBytes != 1_500 {
		t.Errorf("total = %d, want 1500 — a save failure must not drop the delta", status.TotalUpBytes)
	}

	store.err = nil
	now = now.Add(5 * time.Second)
	if _, err := collector.Collect(context.Background()); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if store.up != 1_500 || store.down != 11_000 {
		t.Errorf("persisted totals = %d/%d, want 1500/11000 after the store recovered", store.up, store.down)
	}
}
