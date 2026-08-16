package hoststats_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/hoststats"
)

type fakeSource struct {
	mu    sync.Mutex
	stats Stats
	err   error
	calls int
}

// Stats is an alias so the fake can construct values without repeating the import path.
type Stats = hoststats.Stats

func (f *fakeSource) Collect(context.Context) (Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.stats, f.err
}

func (f *fakeSource) set(stats Stats, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats, f.err = stats, err
}

func (f *fakeSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestCacheLatestCollectsOnDemandBeforeStart(t *testing.T) {
	source := &fakeSource{stats: Stats{CollectedAt: 100, CPUPercent: 42}}
	cache := hoststats.NewCache(source, time.Hour)

	first, err := cache.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	second, err := cache.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest again: %v", err)
	}

	if first.CollectedAt != 100 || second.CollectedAt != 100 {
		t.Errorf("stats = %v then %v, want the cached snapshot twice", first, second)
	}
	if calls := source.callCount(); calls != 1 {
		t.Errorf("source collected %d times, want 1 (second read served from cache)", calls)
	}
}

func TestCacheLatestErrorsWhenNeverCollected(t *testing.T) {
	source := &fakeSource{err: errors.New("host unreadable")}
	cache := hoststats.NewCache(source, time.Hour)

	if _, err := cache.Latest(context.Background()); err == nil {
		t.Fatal("latest succeeded, want an error when no snapshot was ever collected")
	}
}

func TestCacheRefreshesSnapshotOnInterval(t *testing.T) {
	source := &fakeSource{stats: Stats{CollectedAt: 100}}
	cache := hoststats.NewCache(source, 10*time.Millisecond)

	if _, err := cache.Latest(context.Background()); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}
	source.set(Stats{CollectedAt: 200}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for {
		latest, err := cache.Latest(context.Background())
		if err == nil && latest.CollectedAt == 200 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot still %+v after 2s, want refreshed to CollectedAt=200", latest)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCacheKeepsLastGoodSnapshotWhenRefreshFails(t *testing.T) {
	source := &fakeSource{stats: Stats{CollectedAt: 100, CPUPercent: 42}}
	cache := hoststats.NewCache(source, 10*time.Millisecond)

	if _, err := cache.Latest(context.Background()); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}
	source.set(Stats{}, errors.New("read failed"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for source.callCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if source.callCount() < 2 {
		t.Fatal("refresh loop never ran")
	}

	latest, err := cache.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest after failed refresh: %v", err)
	}
	if latest.CollectedAt != 100 || latest.CPUPercent != 42 {
		t.Errorf("snapshot = %+v, want the last good snapshot", latest)
	}
}
