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
	ApplyPoll(ctx context.Context, deltas []Delta, presence []Presence, roster map[string]RosterUser, now time.Time) error
	Users(ctx context.Context) ([]User, error)
}

// pendingDelta is a poll's unapplied traffic, kept when a store write fails
// so the next transaction carries the bytes. seenNow marks that movement
// happened inside a merged window — the seed baseline is not activity.
type pendingDelta struct {
	up, down uint64
	seenNow  bool
}

// Collector polls per-user traffic counters and the live online set into
// the durable store and produces users snapshots. A failed traffic poll is
// data, not an error: the snapshot serves the store's last-known state with
// stale set (SPEC.md §3). Presence has its own degrade ladder: an old xray
// omits it silently (supported=false), and a failed presence poll omits it
// for that one poll — neither touches the stale flag, because the traffic
// data is still fresh. Store failures do fail the poll — and the poll's
// deltas stay pending, so durable totals never silently drop bytes.
//
// The config roster (WithRoster) syncs in the same transaction when it
// changes: protocol · security labels, new users, gone flags. A roster that
// cannot be persisted stays pending like the deltas, and flushes on its own
// while xray is unreachable, so config edits never wait on a recovery.
type Collector struct {
	traffic  TrafficQuerier
	presence PresenceQuerier
	store    persistence
	roster   RosterSource
	geo      GeoResolver
	now      func() time.Time

	mu                   sync.Mutex
	up                   map[string]*reconcile.Tracker
	down                 map[string]*reconcile.Tracker
	seeded               map[string]bool          // emails whose baseline this process established
	pending              map[string]*pendingDelta // unapplied traffic, by email
	pendingRoster        map[string]RosterUser    // unapplied roster (nil once persisted)
	pendingRosterVersion uint64                   // version pendingRoster holds
	rosterVersion        uint64                   // last persisted roster version
	lastPoll             time.Time                // last sampled poll (drives speed windows)
	collectedAt          int64                    // last persisted poll
	stale                bool
	pollErr              string // last logged traffic-poll failure; "" when healthy
	storeErr             string // last logged store-write failure; "" when healthy
	presenceErr          string // last logged presence-poll failure; "" when healthy
	rosterErr            string // last logged roster-flush failure; "" when healthy
	notedUnsupported     bool   // logged the old-xray presence degrade already
}

// NewCollector creates a Collector polling traffic and presence into the
// store.
func NewCollector(traffic TrafficQuerier, presence PresenceQuerier, store persistence) *Collector {
	return &Collector{
		traffic:  traffic,
		presence: presence,
		store:    store,
		now:      time.Now,
		up:       map[string]*reconcile.Tracker{},
		down:     map[string]*reconcile.Tracker{},
		seeded:   map[string]bool{},
		pending:  map[string]*pendingDelta{},
	}
}

// WithClock overrides the time source (tests).
func (c *Collector) WithClock(now func() time.Time) *Collector {
	c.now = now
	return c
}

// WithRoster syncs the user roster from the xray config (SPEC.md §3 step 4)
// into the poll transaction whenever the source's version moves.
func (c *Collector) WithRoster(roster RosterSource) *Collector {
	c.roster = roster
	return c
}

// WithGeo resolves each online IP's country at snapshot time — live
// presence and stale last-known IPs alike, since a country is a property
// of the IP, not of the connection.
func (c *Collector) WithGeo(geo GeoResolver) *Collector {
	c.geo = geo
	return c
}

// Collect runs one poll and returns the current snapshot.
func (c *Collector) Collect(ctx context.Context) (Snapshot, error) {
	c.sampleRoster()
	raws, err := c.traffic.QueryUserTraffic(ctx)
	if err != nil {
		c.logFailure(&c.pollErr, "cannot query per-user traffic; serving the last-known snapshot", err)
		c.mu.Lock()
		c.stale = true
		c.mu.Unlock()
		c.flushRoster(ctx) // config edits land without waiting for xray
		return c.snapshot(ctx, nil, nil)
	}
	c.logRecovery(&c.pollErr, "per-user traffic poll recovered")

	online, supported, presenceErr := c.presence.QueryPresence(ctx)
	switch {
	case presenceErr != nil:
		c.logFailure(&c.presenceErr, "cannot query online presence; presence omitted this poll", presenceErr)
	case !supported:
		// Old xray without the online RPCs (SPEC.md §3): presence omitted,
		// last_seen falls back to the traffic-delta heuristic. Log once —
		// the degrade is a property of the server, not a flapping failure.
		c.mu.Lock()
		if !c.notedUnsupported {
			c.notedUnsupported = true
			slog.Info("xray predates the online RPCs; presence omitted, last_seen falls back to traffic deltas")
		}
		c.mu.Unlock()
	default:
		c.logRecovery(&c.presenceErr, "presence poll recovered")
	}

	existing, err := c.store.ExistingEmails(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read roster: %w", err)
	}

	now := c.now()

	// The live online set: overlaid on the snapshot and persisted as
	// last_seen/last_ips in the same transaction as the deltas.
	var onlineSet map[string]Presence
	var presenceRows []Presence
	if presenceErr == nil && supported {
		onlineSet = make(map[string]Presence, len(online))
		for _, user := range online {
			if user.LastSeen == 0 {
				// Online without per-IP timestamps: being online is being seen.
				user.LastSeen = now.Unix()
			}
			onlineSet[user.Email] = user
			presenceRows = append(presenceRows, user)
		}
	}

	c.mu.Lock()
	elapsed := now.Sub(c.lastPoll)
	c.lastPoll = now
	for _, raw := range raws {
		up, down := c.trackersFor(raw.Email)
		up.Add(raw.UpBytes, elapsed)
		down.Add(raw.DownBytes, elapsed)
		upDelta, downDelta := up.LastDelta(), down.LastDelta()
		seenNow := upDelta+downDelta > 0
		if !c.seeded[raw.Email] {
			c.seeded[raw.Email] = true
			if !existing[raw.Email] {
				// First contact with a user the store has no row for: seed
				// from xray's current counters (no double count — the row
				// did not exist). Existing rows resume instead: their
				// durable totals already hold earlier epochs.
				upDelta, downDelta = raw.UpBytes, raw.DownBytes
				seenNow = false // a baseline seed is not observed activity (SPEC.md §5)
			}
		}
		pending := c.pending[raw.Email]
		if pending == nil {
			pending = &pendingDelta{}
			c.pending[raw.Email] = pending
		}
		pending.up += upDelta
		pending.down += downDelta
		pending.seenNow = pending.seenNow || seenNow
	}
	deltas := make([]Delta, 0, len(c.pending))
	for email, pending := range c.pending {
		deltas = append(deltas, Delta{Email: email, Up: pending.up, Down: pending.down, SeenNow: pending.seenNow})
	}
	c.mu.Unlock()

	c.mu.Lock()
	roster := c.pendingRoster
	c.mu.Unlock()

	// One transaction per poll (SPEC.md §4). On failure the deltas stay
	// pending and merge into the next poll's transaction; presence is
	// volatile and simply re-queried next poll. The roster, too, stays
	// pending until it persists.
	if err := c.store.ApplyPoll(ctx, deltas, presenceRows, roster, now); err != nil {
		c.logFailure(&c.storeErr, "cannot persist per-user traffic; deltas carry into the next poll", err)
		return Snapshot{}, fmt.Errorf("persist poll: %w", err)
	}
	c.logRecovery(&c.storeErr, "per-user traffic persistence recovered")

	c.mu.Lock()
	clear(c.pending)
	if roster != nil {
		c.rosterVersion = c.pendingRosterVersion
		c.pendingRoster = nil
	}
	c.stale = false
	c.collectedAt = now.Unix()
	speeds := c.speedsLocked()
	c.mu.Unlock()
	return c.snapshot(ctx, speeds, onlineSet)
}

// sampleRoster picks up the config roster when its version moved. Version 0
// means the config never parsed — no sync, so a missing or broken config
// marks nobody gone.
func (c *Collector) sampleRoster() {
	if c.roster == nil {
		return
	}
	latest, version := c.roster.Roster()
	c.mu.Lock()
	defer c.mu.Unlock()
	if version > 0 && version != c.rosterVersion && version != c.pendingRosterVersion {
		c.pendingRoster = latest
		c.pendingRosterVersion = version
	}
}

// flushRoster persists a pending roster on its own — the degraded path,
// where there is no poll transaction to carry it. Applying a roster is
// idempotent (upsert + gone flags), so a retry or a racing flush can only
// write the same state twice.
func (c *Collector) flushRoster(ctx context.Context) {
	c.mu.Lock()
	roster := c.pendingRoster
	c.mu.Unlock()
	if roster == nil {
		return
	}
	if err := c.store.ApplyPoll(ctx, nil, nil, roster, c.now()); err != nil {
		c.logFailure(&c.rosterErr, "cannot persist the config roster; it carries into the next poll", err)
		return
	}
	c.logRecovery(&c.rosterErr, "config roster persistence recovered")
	c.mu.Lock()
	c.rosterVersion = c.pendingRosterVersion
	c.pendingRoster = nil
	c.mu.Unlock()
}

// snapshot reads the last-known users from the store and overlays the
// in-memory speeds (nil when stale — speeds zero out per SPEC.md §3) and
// the live online set. A stale snapshot serves the store rows untouched, so
// last-known IPs and last_seen stay visible. On a live poll without usable
// presence (old xray or a failed presence poll) presence is omitted; with a
// live online set, users absent from it are offline with no online IPs.
func (c *Collector) snapshot(ctx context.Context, speeds map[string][2]uint64, online map[string]Presence) (Snapshot, error) {
	list, err := c.store.Users(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if list == nil {
		list = []User{} // the contract is users: [], never null
	}
	c.mu.Lock()
	stale := c.stale
	snapshot := Snapshot{CollectedAt: c.collectedAt, Stale: stale, Users: list}
	c.mu.Unlock()
	for i := range list {
		if speed, ok := speeds[list[i].Email]; ok {
			list[i].SpeedUpBps = speed[0]
			list[i].SpeedDownBps = speed[1]
		}
		if stale {
			continue
		}
		user, isOnline := online[list[i].Email]
		list[i].Online = isOnline
		list[i].IPs = nil // offline or presence omitted: no online IPs
		if isOnline {
			list[i].IPs = user.IPs
		}
	}
	if c.geo != nil {
		for i := range list {
			for _, ip := range list[i].IPs {
				if country := c.geo.Country(ip); country != "" {
					if list[i].IPCountries == nil {
						list[i].IPCountries = map[string]string{}
					}
					list[i].IPCountries[ip] = country
				}
			}
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
