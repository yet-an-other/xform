package users

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yet-an-other/xform/internal/reconcile"
)

// persistence is the store seam — *Store in production, failure-injecting
// wrappers in tests.
type persistence interface {
	ExistingEmails(ctx context.Context) (map[string]bool, error)
	ApplyDeltas(ctx context.Context, deltas []Delta, now time.Time) error
	Users(ctx context.Context) ([]User, error)
}

// Collector polls per-user traffic counters into the durable store and
// produces users snapshots. A failed traffic poll is data, not an error: the
// snapshot serves the store's last-known state with stale set (SPEC.md §3).
// Store failures do fail the poll — and the poll's deltas stay pending, so
// durable totals never silently drop bytes.
type Collector struct {
	traffic TrafficQuerier
	store   persistence
	now     func() time.Time

	mu          sync.Mutex
	up          map[string]*reconcile.Tracker
	down        map[string]*reconcile.Tracker
	seeded      map[string]bool              // emails whose baseline this process established
	pending     map[string]*[2]uint64        // unapplied {up, down} deltas, by email
	lastPoll    time.Time                    // last sampled poll (drives speed windows)
	collectedAt int64                         // last persisted poll
	stale       bool
	pollErr     string // last logged traffic-poll failure; "" when healthy
	storeErr    string // last logged store-write failure; "" when healthy
}

// NewCollector creates a Collector polling traffic into the store.
func NewCollector(traffic TrafficQuerier, store persistence) *Collector {
	return &Collector{
		traffic: traffic,
		store:   store,
		now:     time.Now,
		up:      map[string]*reconcile.Tracker{},
		down:    map[string]*reconcile.Tracker{},
		seeded:  map[string]bool{},
		pending: map[string]*[2]uint64{},
	}
}

// WithClock overrides the time source (tests).
func (c *Collector) WithClock(now func() time.Time) *Collector {
	c.now = now
	return c
}

// Collect runs one poll and returns the current snapshot.
func (c *Collector) Collect(ctx context.Context) (Snapshot, error) {
	raws, err := c.traffic.QueryUserTraffic(ctx)
	if err != nil {
		c.logFailure(&c.pollErr, "cannot query per-user traffic; serving the last-known snapshot", err)
		c.mu.Lock()
		c.stale = true
		c.mu.Unlock()
		return c.snapshot(ctx, nil)
	}
	c.logRecovery(&c.pollErr, "per-user traffic poll recovered")

	existing, err := c.store.ExistingEmails(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read roster: %w", err)
	}

	now := c.now()
	c.mu.Lock()
	elapsed := now.Sub(c.lastPoll)
	c.lastPoll = now
	for _, raw := range raws {
		up, down := c.trackersFor(raw.Email)
		up.Add(raw.UpBytes, elapsed)
		down.Add(raw.DownBytes, elapsed)
		upDelta, downDelta := up.LastDelta(), down.LastDelta()
		if !c.seeded[raw.Email] {
			c.seeded[raw.Email] = true
			if !existing[raw.Email] {
				// First contact with a user the store has no row for: seed
				// from xray's current counters (no double count — the row
				// did not exist). Existing rows resume instead: their
				// durable totals already hold earlier epochs.
				upDelta, downDelta = raw.UpBytes, raw.DownBytes
			}
		}
		pending := c.pending[raw.Email]
		if pending == nil {
			pending = &[2]uint64{}
			c.pending[raw.Email] = pending
		}
		pending[0] += upDelta
		pending[1] += downDelta
	}
	deltas := make([]Delta, 0, len(c.pending))
	for email, pending := range c.pending {
		deltas = append(deltas, Delta{Email: email, Up: pending[0], Down: pending[1]})
	}
	c.mu.Unlock()

	// One transaction per poll (SPEC.md §4). On failure the deltas stay
	// pending and merge into the next poll's transaction.
	if err := c.store.ApplyDeltas(ctx, deltas, now); err != nil {
		c.logFailure(&c.storeErr, "cannot persist per-user traffic; deltas carry into the next poll", err)
		return Snapshot{}, fmt.Errorf("persist poll: %w", err)
	}
	c.logRecovery(&c.storeErr, "per-user traffic persistence recovered")

	c.mu.Lock()
	clear(c.pending)
	c.stale = false
	c.collectedAt = now.Unix()
	speeds := c.speedsLocked()
	c.mu.Unlock()
	return c.snapshot(ctx, speeds)
}

// snapshot reads the last-known users from the store and overlays the
// in-memory speeds (nil when stale — speeds zero out per SPEC.md §3).
func (c *Collector) snapshot(ctx context.Context, speeds map[string][2]uint64) (Snapshot, error) {
	list, err := c.store.Users(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if list == nil {
		list = []User{} // the contract is users: [], never null
	}
	c.mu.Lock()
	snapshot := Snapshot{CollectedAt: c.collectedAt, Stale: c.stale, Users: list}
	c.mu.Unlock()
	for i := range list {
		if speed, ok := speeds[list[i].Email]; ok {
			list[i].SpeedUpBps = speed[0]
			list[i].SpeedDownBps = speed[1]
		}
	}
	return snapshot, nil
}

// trackersFor returns (creating on first contact) the user's reconcilers.
// Durable totals never credit the baseline — the store owns the totals.
func (c *Collector) trackersFor(email string) (up, down *reconcile.Tracker) {
	up, ok := c.up[email]
	if !ok {
		up = reconcile.NewTracker(false)
		c.up[email] = up
	}
	down, ok = c.down[email]
	if !ok {
		down = reconcile.NewTracker(false)
		c.down[email] = down
	}
	return up, down
}

// speedsLocked maps each known user to their {up, down} speed estimate.
func (c *Collector) speedsLocked() map[string][2]uint64 {
	speeds := make(map[string][2]uint64, len(c.up))
	for email := range c.up {
		speeds[email] = [2]uint64{c.up[email].Speed(), c.down[email].Speed()}
	}
	return speeds
}

// logFailure logs a persistent failure once — when it starts or its message
// changes — instead of on every 5s poll. slot points to a Collector field.
func (c *Collector) logFailure(slot *string, msg string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *slot == err.Error() {
		return
	}
	*slot = err.Error()
	slog.Warn(msg, "error", err)
}

// logRecovery logs once when a failure cleared by logFailure goes away.
func (c *Collector) logRecovery(slot *string, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *slot == "" {
		return
	}
	*slot = ""
	slog.Info(msg)
}
