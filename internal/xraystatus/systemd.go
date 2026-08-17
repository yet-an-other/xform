package xraystatus

import (
	"context"
	"fmt"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

// UnitConn is the slice of a systemd D-Bus connection the panel uses —
// *dbus.Conn in production, fakes in tests.
type UnitConn interface {
	GetUnitPropertiesContext(ctx context.Context, unit string) (map[string]any, error)
	GetUnitTypePropertiesContext(ctx context.Context, unit string, unitType string) (map[string]any, error)
	Close()
}

// SystemdUnit reads unit state over D-Bus — the stable interface systemctl
// itself uses (docs/research/xray-grpc-go-client.md §4). On hosts without
// systemd the connection simply fails and the collector reports unreachable.
type SystemdUnit struct {
	// DialSystem connects to the D-Bus system bus; nil dials with
	// dbus.NewSystemConnectionContext. Overridden in tests.
	DialSystem func(ctx context.Context) (UnitConn, error)
	// DialPrivate connects to systemd's private socket; nil dials with
	// dbus.NewSystemdConnectionContext. Overridden in tests.
	DialPrivate func(ctx context.Context) (UnitConn, error)
}

// QueryUnit implements UnitQuerier. It opens a fresh connection per query:
// at the collector's 5s cadence the socket setup is negligible, and a
// stateless connection can never silently rot (docs/research §4: always Close).
func (s SystemdUnit) QueryUnit(ctx context.Context, name string) (UnitInfo, error) {
	conn, err := s.dial(ctx)
	if err != nil {
		return UnitInfo{}, err
	}
	defer conn.Close()

	props, err := conn.GetUnitPropertiesContext(ctx, name)
	if err != nil {
		return UnitInfo{}, fmt.Errorf("query unit %s: %w", name, err)
	}

	// The Service properties only feed ExecPath; tolerate their absence.
	svc, _ := conn.GetUnitTypePropertiesContext(ctx, name, "Service")
	return UnitInfoFromProperties(props, svc), nil
}

// dial prefers the system bus because the panel deploys as an unprivileged
// user: PID 1 creates /run/systemd/private root-only (0700), so a private
// connection fails with EPERM for anyone but root. The private socket stays
// as a fallback for root-run hosts without a dbus daemon. Property reads on
// either bus need no elevated privileges (§4).
//
// Only dial errors trigger the fallback: a successful dial whose property
// reads then fail is returned as-is — that failure is post-connect and would
// repeat identically on the other socket.
func (s SystemdUnit) dial(ctx context.Context) (UnitConn, error) {
	dialSystem := s.DialSystem
	if dialSystem == nil {
		dialSystem = func(ctx context.Context) (UnitConn, error) {
			return dbus.NewSystemConnectionContext(ctx)
		}
	}
	conn, err := dialSystem(ctx)
	if err == nil {
		return conn, nil
	}

	dialPrivate := s.DialPrivate
	if dialPrivate == nil {
		dialPrivate = func(ctx context.Context) (UnitConn, error) {
			return dbus.NewSystemdConnectionContext(ctx)
		}
	}
	privateConn, privateErr := dialPrivate(ctx)
	if privateErr != nil {
		// Name both causes: a dbus-less unprivileged host must see the
		// system-bus failure, not just the fallback's misleading EPERM.
		return nil, fmt.Errorf("connect to systemd: system bus: %w; private socket: %w", err, privateErr)
	}
	return privateConn, nil
}

// UnitInfoFromProperties maps raw systemd D-Bus property maps (Unit and
// Service interfaces, as decoded by go-systemd/godbus) into a UnitInfo.
func UnitInfoFromProperties(unitProps, svcProps map[string]any) UnitInfo {
	info := UnitInfo{}
	info.ActiveState, _ = unitProps["ActiveState"].(string)
	info.SubState, _ = unitProps["SubState"].(string)
	// ActiveEnterTimestamp is CLOCK_REALTIME in microseconds; 0 = never active.
	if usec, ok := unitProps["ActiveEnterTimestamp"].(uint64); ok && usec > 0 {
		info.ActiveSince = time.Unix(int64(usec)/1_000_000, int64(usec%1_000_000)*1_000)
	}
	info.ExecPath = execStartPath(svcProps["ExecStart"])
	return info
}

// execStartPath extracts the binary path from the unit's ExecStart property,
// whose D-Bus shape is a(sasbttuii) — an array of per-command structs whose
// first field is the binary path. godbus decodes that to [][]any (struct
// fields within), so accept exactly that; the []any branch tolerates a
// pre-unwrapped shape.
func execStartPath(value any) string {
	switch commands := value.(type) {
	case [][]any:
		if len(commands) > 0 && len(commands[0]) > 0 {
			path, _ := commands[0][0].(string)
			return path
		}
	case []any:
		if len(commands) > 0 {
			if fields, ok := commands[0].([]any); ok && len(fields) > 0 {
				path, _ := fields[0].(string)
				return path
			}
		}
	}
	return ""
}
