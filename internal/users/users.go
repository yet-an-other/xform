// Package users collects per-user traffic from the xray stats API into the
// panel's durable SQLite store (SPEC.md §3–§4) and produces the users
// snapshots behind GET /api/v1/users.
package users

import (
	"context"

	"github.com/yet-an-other/xform/internal/xraygrpc"
)

// User is one row of the users table — the JSON contract of
// GET /api/v1/users (SPEC.md §5). Presence fields (online, ips, last_seen)
// are live from the online RPCs, degraded on old servers (SPEC.md §3);
// config fields (protocol, security, gone) stay zero until the config-parse
// slice.
type User struct {
	Email          string   `json:"email"`
	Protocol       *string  `json:"protocol"`
	Security       *string  `json:"security"`
	UpBytesTotal   uint64   `json:"up_bytes_total"`
	DownBytesTotal uint64   `json:"down_bytes_total"`
	Online         bool     `json:"online"`
	IPs            []string `json:"ips"`
	SpeedUpBps     uint64   `json:"speed_up_bps"`
	SpeedDownBps   uint64   `json:"speed_down_bps"`
	LastSeen       *int64   `json:"last_seen"`
	Gone           bool     `json:"gone"`

	FirstSeen int64 `json:"-"` // panel-internal
}

// Snapshot is the JSON payload of GET /api/v1/users: the last-known users
// plus whether they come from a failed poll (stale — SPEC.md §3).
type Snapshot struct {
	CollectedAt int64  `json:"collected_at"`
	Stale       bool   `json:"stale"`
	Users       []User `json:"users"`
}

// RawTraffic is one user's raw cumulative counters (reset on xray restart).
// The type lives with the gRPC client; aliased here so the collector reads
// in panel vocabulary.
type RawTraffic = xraygrpc.UserTraffic

// TrafficQuerier reads raw per-user traffic counters — the seam for fakes.
type TrafficQuerier interface {
	QueryUserTraffic(ctx context.Context) ([]RawTraffic, error)
}

// Presence is one online user's live connection set — the IPs they are
// connected from and their most recent per-IP activity (SPEC.md §3). The
// type lives with the gRPC client; aliased here so the collector and store
// read in panel vocabulary.
type Presence = xraygrpc.UserPresence

// PresenceQuerier reads the live online set — the seam for fakes.
// supported is false on xray servers predating the online RPCs: presence is
// omitted, never an error (SPEC.md §3).
type PresenceQuerier interface {
	QueryPresence(ctx context.Context) (presence []Presence, supported bool, err error)
}

// Delta is one poll's reconciled per-user traffic to apply to the store.
// SeenNow marks movement inside this poll's window — the traffic-delta
// heuristic for last_seen (SPEC.md §3). A baseline seed carries traffic but
// no SeenNow: those bytes predate the panel's first look (SPEC.md §5).
type Delta struct {
	Email   string
	Up      uint64
	Down    uint64
	SeenNow bool
}
