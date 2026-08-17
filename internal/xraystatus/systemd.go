package xraystatus

import (
	"context"
	"fmt"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

// SystemdUnit reads unit state over D-Bus — the stable interface systemctl
// itself uses (docs/research/xray-grpc-go-client.md §4). On hosts without
// systemd the connection simply fails and the collector reports unreachable.
type SystemdUnit struct{}

// QueryUnit implements UnitQuerier. It opens a fresh connection per query:
// at the collector's 5s cadence the socket setup is negligible, and a
// stateless connection can never silently rot (docs/research §4: always Close).
//
// The system bus is preferred because the panel deploys as an unprivileged
// user: PID 1 creates /run/systemd/private root-only (0700), so a private
// connection fails with EPERM for anyone but root. The private socket stays
// as a fallback for root-run hosts without a dbus daemon. Property reads on
// either bus need no elevated privileges (§4).
func (SystemdUnit) QueryUnit(ctx context.Context, name string) (UnitInfo, error) {
	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		conn, err = dbus.NewSystemdConnectionContext(ctx)
		if err != nil {
			return UnitInfo{}, fmt.Errorf("connect to systemd: %w", err)
		}
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
