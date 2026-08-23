// Package xraystatus reports the panel's view of the xray service from
// host-level sources: the systemd unit state, the xray binary itself, and
// the loopback gRPC StatsService (SPEC.md §3).
package xraystatus

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/yet-an-other/xform/internal/reconcile"
	"github.com/yet-an-other/xform/internal/xraygrpc"
)

// Status values for the panel's view of the xray service (CONTEXT.md).
const (
	StatusRunning     = "running"
	StatusStopped     = "stopped"
	StatusUnreachable = "unreachable"
)

// Status is the JSON contract returned by GET /api/v1/xray.
type Status struct {
	CollectedAt     int64   `json:"collected_at"`
	Status          string  `json:"status"`  // running | stopped | unreachable
	Version         *string `json:"version"` // null unless the unit is active
	UptimeSeconds   uint64  `json:"uptime_seconds"`
	MemBytes        *uint64 `json:"mem_bytes"` // null unless running (gRPC sysstats)
	Goroutines      *uint32 `json:"goroutines"`
	SpeedUpBps      uint64  `json:"speed_up_bps"` // 0 when degraded
	SpeedDownBps    uint64  `json:"speed_down_bps"`
	TotalUpBytes    uint64  `json:"total_up_bytes"` // durable totals, survive xray restarts
	TotalDownBytes  uint64  `json:"total_down_bytes"`
	UsersOnline     *int    `json:"users_online"` // null when the server predates the online RPCs
	UniqueIPsOnline *int    `json:"unique_ips_online"`
}

// UnitInfo is what the panel needs from a systemd unit.
type UnitInfo struct {
	ActiveState string
	SubState    string
	ActiveSince time.Time // zero when the unit has never been active
	ExecPath    string    // the binary the unit runs (ExecStart)
}

// UnitQuerier reads systemd unit state — the seam for fakes.
type UnitQuerier interface {
	QueryUnit(ctx context.Context, name string) (UnitInfo, error)
}

// VersionRunner reads the xray binary's version — the seam for fakes.
type VersionRunner interface {
	Version(ctx context.Context, execPath string) (string, error)
}

// RuntimeStats is one poll of xray's gRPC StatsService: process stats plus
// the raw cumulative traffic counters (which reset whenever xray restarts).
// The type lives with the client; aliased here so the panel's seams read in
// panel vocabulary.
type RuntimeStats = xraygrpc.RuntimeStats

// StatsQuerier reads xray runtime stats over gRPC — the seam for fakes.
type StatsQuerier interface {
	QueryRuntime(ctx context.Context) (RuntimeStats, error)
}

// TotalsStore persists the durable aggregate traffic totals across panel
// restarts — the seam for fakes.
type TotalsStore interface {
	// LoadTrafficTotals returns the stored totals; found is false when no
	// row exists yet (first boot with persistence).
	LoadTrafficTotals(ctx context.Context) (up, down uint64, found bool, err error)
	SaveTrafficTotals(ctx context.Context, up, down uint64) error
}

// Collector gathers the xray service status from the unit and the binary.
type Collector struct {
	unit     UnitQuerier
	version  VersionRunner
	stats    StatsQuerier
	unitName string
	now      func() time.Time

	mu        sync.Mutex
	queryErr  string // last logged unit-query failure; "" when healthy
	binaryErr string // last logged version-read failure; "" when healthy
	statsErr  string // last logged stats-API failure; "" when healthy
	totalsErr string // last logged totals-store failure; "" when healthy
	up, down  *reconcile.Tracker
	lastPoll  time.Time // last successful stats poll (drives speed windows)

	totalsStore            TotalsStore // nil: in-memory display totals only
	totalsLoaded           bool
	durableUp, durableDown uint64 // durable totals, persisted per poll
}

// NewCollector creates a Collector for the named systemd unit.
func NewCollector(unit UnitQuerier, version VersionRunner, stats StatsQuerier, unitName string) *Collector {
	return &Collector{
		unit: unit, version: version, stats: stats, unitName: unitName, now: time.Now,
		// The xray-row totals are display figures, so they adopt xray's
		// current counters as their baseline.
		up: reconcile.NewTracker(true), down: reconcile.NewTracker(true),
	}
}

// WithClock overrides the time source (tests).
func (c *Collector) WithClock(now func() time.Time) *Collector {
	c.now = now
	return c
}

// WithTotalsStore makes the xray-row totals durable across panel restarts
// (SPEC.md §3: panel totals are durable). Call before the first Collect.
// The trackers stop crediting the baseline — the store owns the totals.
func (c *Collector) WithTotalsStore(store TotalsStore) *Collector {
	c.totalsStore = store
	c.up = reconcile.NewTracker(false)
	c.down = reconcile.NewTracker(false)
	return c
}

// Collect returns the current status. It never fails: when the unit cannot be
// queried at all, the state is "unreachable" — the degraded-mode contract
// (SPEC.md §5: 200 always, when the panel itself is up).
func (c *Collector) Collect(ctx context.Context) (Status, error) {
	status := Status{CollectedAt: c.now().Unix()}

	info, err := c.unit.QueryUnit(ctx, c.unitName)
	if err != nil {
		// The failure reason stays observable even though the payload
		// degrades to "unreachable" (SPEC.md §5: 200 always).
		c.logFailure(&c.queryErr, "cannot query xray unit; reporting unreachable", err)
		status.Status = StatusUnreachable
		return status, nil
	}
	c.logRecovery(&c.queryErr, "xray unit query recovered")

	if info.ActiveState != "active" {
		status.Status = StatusStopped
		return status, nil
	}

	// The unit is active, so the panel knows the version (from the binary)
	// and the uptime (from systemd) regardless of what happens next.
	status.Status = StatusRunning
	if !info.ActiveSince.IsZero() {
		status.UptimeSeconds = uint64(c.now().Sub(info.ActiveSince).Seconds())
	}
	if version, err := c.version.Version(ctx, info.ExecPath); err == nil {
		c.logRecovery(&c.binaryErr, "xray version read recovered")
		status.Version = &version
	} else {
		c.logFailure(&c.binaryErr, "cannot read xray version from the unit's binary", err)
	}

	// An active unit whose stats API does not answer is unreachable (SPEC.md
	// §3 degraded mode) — the speeds zero out and the process/online fields
	// drop to null, while version, uptime, and the durable totals stay live.
	runtime, err := c.stats.QueryRuntime(ctx)
	if err != nil {
		c.logFailure(&c.statsErr, "cannot query xray stats API; reporting unreachable", err)
		c.mu.Lock()
		status.TotalUpBytes = c.up.Total()
		status.TotalDownBytes = c.down.Total()
		if c.totalsLoaded {
			status.TotalUpBytes = c.durableUp
			status.TotalDownBytes = c.durableDown
		}
		c.mu.Unlock()
		status.Status = StatusUnreachable
		return status, nil
	}
	c.logRecovery(&c.statsErr, "xray stats API query recovered")
	status.MemBytes = &runtime.MemBytes
	status.Goroutines = &runtime.Goroutines
	status.UsersOnline = runtime.OnlineUsers
	status.UniqueIPsOnline = runtime.OnlineIPs

	now := c.now()
	c.mu.Lock()
	elapsed := now.Sub(c.lastPoll)
	c.up.Add(runtime.UpBytes, elapsed)
	c.down.Add(runtime.DownBytes, elapsed)
	c.lastPoll = now
	status.SpeedUpBps = c.up.Speed()
	status.SpeedDownBps = c.down.Speed()
	upDelta, downDelta := c.up.LastDelta(), c.down.LastDelta()
	loadTotals := c.totalsStore != nil && !c.totalsLoaded
	c.mu.Unlock()

	if loadTotals {
		c.loadTotals(ctx, runtime)
	}

	c.mu.Lock()
	if c.totalsLoaded {
		c.durableUp += upDelta
		c.durableDown += downDelta
		status.TotalUpBytes = c.durableUp
		status.TotalDownBytes = c.durableDown
	} else {
		status.TotalUpBytes = c.up.Total()
		status.TotalDownBytes = c.down.Total()
	}
	saveTotals := c.totalsLoaded
	c.mu.Unlock()

	// Persist the durable totals every poll; on failure the in-memory totals
	// keep accumulating and the next successful save persists the superset.
	if saveTotals {
		if err := c.totalsStore.SaveTrafficTotals(ctx, status.TotalUpBytes, status.TotalDownBytes); err != nil {
			c.logFailure(&c.totalsErr, "cannot persist traffic totals; will retry next poll", err)
		} else {
			c.logRecovery(&c.totalsErr, "traffic totals persistence recovered")
		}
	}
	return status, nil
}

// loadTotals loads the durable totals once, seeding them from xray's current
// counters on the first boot with persistence (mirrors the first-contact
// seeding of user rows in the users package). A load failure degrades to
// in-memory display totals and retries on the next poll.
func (c *Collector) loadTotals(ctx context.Context, runtime RuntimeStats) {
	up, down, found, err := c.totalsStore.LoadTrafficTotals(ctx)
	if err != nil {
		c.logFailure(&c.totalsErr, "cannot load durable traffic totals; serving in-memory totals", err)
		return
	}
	if !found {
		up, down = runtime.UpBytes, runtime.DownBytes
	}
	c.mu.Lock()
	c.durableUp, c.durableDown = up, down
	c.totalsLoaded = true
	c.mu.Unlock()
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
	slog.Warn(msg, "unit", c.unitName, "error", err)
}

// logRecovery logs once when a failure cleared by logFailure goes away.
func (c *Collector) logRecovery(slot *string, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *slot == "" {
		return
	}
	*slot = ""
	slog.Info(msg, "unit", c.unitName)
}
