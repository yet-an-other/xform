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

// QueryUnit implements UnitQuerier. It opens a fresh private connection per
// query: at the collector's 5s cadence the socket setup is negligible, and a
// stateless connection can never silently rot (docs/research §4: always Close).
func (SystemdUnit) QueryUnit(ctx context.Context, name string) (UnitInfo, error) {
	conn, err := dbus.NewSystemdConnectionContext(ctx)
	if err != nil {
		return UnitInfo{}, fmt.Errorf("connect to systemd: %w", err)
	}
	defer conn.Close()

	props, err := conn.GetUnitPropertiesContext(ctx, name)
	if err != nil {
		return UnitInfo{}, fmt.Errorf("query unit %s: %w", name, err)
	}

	info := UnitInfo{}
	info.ActiveState, _ = props["ActiveState"].(string)
	info.SubState, _ = props["SubState"].(string)
	// ActiveEnterTimestamp is CLOCK_REALTIME in microseconds; 0 = never active.
	if usec, ok := props["ActiveEnterTimestamp"].(uint64); ok && usec > 0 {
		info.ActiveSince = time.Unix(int64(usec)/1_000_000, int64(usec%1_000_000)*1_000)
	}

	if svc, err := conn.GetUnitTypePropertiesContext(ctx, name, "Service"); err == nil {
		info.ExecPath = execStartPath(svc["ExecStart"])
	}
	return info, nil
}

// execStartPath extracts the binary path from the unit's ExecStart property,
// whose D-Bus shape is a(sasbttuii) — an array of per-command structs whose
// first field is the binary path.
func execStartPath(value any) string {
	commands, ok := value.([]any)
	if !ok || len(commands) == 0 {
		return ""
	}
	fields, ok := commands[0].([]any)
	if !ok || len(fields) == 0 {
		return ""
	}
	path, _ := fields[0].(string)
	return path
}
