// Package xraystatus reports the panel's view of the xray service from
// host-level sources only: the systemd unit state and the xray binary itself.
// No gRPC — that slice comes later (SPEC.md §3).
package xraystatus

import (
	"context"
	"time"
)

// Status values for the panel's view of the xray service (CONTEXT.md).
const (
	StatusRunning     = "running"
	StatusStopped     = "stopped"
	StatusUnreachable = "unreachable"
)

// Status is the JSON contract returned by GET /api/v1/xray.
type Status struct {
	CollectedAt   int64   `json:"collected_at"`
	Status        string  `json:"status"`  // running | stopped | unreachable
	Version       *string `json:"version"` // null unless running
	UptimeSeconds uint64  `json:"uptime_seconds"`
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

// Collector gathers the xray service status from the unit and the binary.
type Collector struct {
	unit     UnitQuerier
	version  VersionRunner
	unitName string
	now      func() time.Time
}

// NewCollector creates a Collector for the named systemd unit.
func NewCollector(unit UnitQuerier, version VersionRunner, unitName string) *Collector {
	return &Collector{unit: unit, version: version, unitName: unitName, now: time.Now}
}

// WithClock overrides the time source (tests).
func (c *Collector) WithClock(now func() time.Time) *Collector {
	c.now = now
	return c
}

// Collect returns the current status. It never fails: when the unit cannot be
// queried at all, the state is "unreachable" — the degraded-mode contract
// (SPEC.md §5: 200 always, when the panel itself is up).
func (c *Collector) Collect(ctx context.Context) (Status, error) {
	status := Status{CollectedAt: c.now().Unix()}

	info, err := c.unit.QueryUnit(ctx, c.unitName)
	if err != nil {
		status.Status = StatusUnreachable
		return status, nil
	}

	if info.ActiveState != "active" {
		status.Status = StatusStopped
		return status, nil
	}

	status.Status = StatusRunning
	if !info.ActiveSince.IsZero() {
		status.UptimeSeconds = uint64(c.now().Sub(info.ActiveSince).Seconds())
	}
	if version, err := c.version.Version(ctx, info.ExecPath); err == nil {
		status.Version = &version
	}
	return status, nil
}
