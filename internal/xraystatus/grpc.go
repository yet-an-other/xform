package xraystatus

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	statscmd "github.com/xtls/xray-core/app/stats/command"
)

// pollTimeout bounds one stats poll below the collector's 5s cadence: a
// black-holed API address must degrade to unreachable, not stall the loop.
const pollTimeout = 4 * time.Second

// GRPCStats reads xray runtime stats from the loopback gRPC StatsService.
// The API has no auth and no TLS (docs/research/xray-grpc-go-client.md §2) —
// loopback binding plus StatsService-only is the entire security model.
type GRPCStats struct {
	Address string
	// Dial connects to the StatsService; nil dials Address with
	// grpc.NewClient and insecure credentials. The returned closer releases
	// the connection. Overridden in tests.
	Dial func(ctx context.Context) (statscmd.StatsServiceClient, func(), error)
}

// QueryStats implements StatsQuerier. It opens a fresh connection per poll,
// like SystemdUnit: negligible at the 5s cadence, and nothing can rot.
//
// Never passes reset=true — that would zero xray's counters (research §2).
func (g GRPCStats) QueryStats(ctx context.Context) (RuntimeStats, error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	dial := g.Dial
	if dial == nil {
		dial = func(context.Context) (statscmd.StatsServiceClient, func(), error) {
			conn, err := grpc.NewClient(g.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return nil, nil, err
			}
			return statscmd.NewStatsServiceClient(conn), func() { _ = conn.Close() }, nil
		}
	}
	client, closeConn, err := dial(ctx)
	if err != nil {
		return RuntimeStats{}, fmt.Errorf("connect to xray stats API: %w", err)
	}
	defer closeConn()

	sys, err := client.GetSysStats(ctx, &statscmd.SysStatsRequest{})
	if err != nil {
		return RuntimeStats{}, fmt.Errorf("get sys stats: %w", err)
	}
	counters, err := client.QueryStats(ctx, &statscmd.QueryStatsRequest{Pattern: "", Reset_: false})
	if err != nil {
		return RuntimeStats{}, fmt.Errorf("query stats: %w", err)
	}

	stats := RuntimeStats{MemBytes: sys.Alloc, Goroutines: sys.NumGoroutine}
	for _, counter := range counters.Stat {
		switch uplink, ok := inboundTraffic(counter.Name); {
		case !ok:
		case uplink:
			stats.UpBytes += uint64(max(counter.Value, 0))
		default:
			stats.DownBytes += uint64(max(counter.Value, 0))
		}
	}

	if err := g.onlineCounts(ctx, client, &stats); err != nil {
		return RuntimeStats{}, err
	}
	return stats, nil
}

// onlineCounts fills the online user/IP counts, tolerating servers that
// predate the online-user RPCs (research §3): Unimplemented degrades to null
// counts, any other error fails the poll like the rest of the stats API.
func (g GRPCStats) onlineCounts(ctx context.Context, client statscmd.StatsServiceClient, stats *RuntimeStats) error {
	online, err := client.GetAllOnlineUsers(ctx, &statscmd.GetAllOnlineUsersRequest{})
	if status.Code(err) == codes.Unimplemented {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get online users: %w", err)
	}

	users := len(online.Users)
	stats.OnlineUsers = &users
	ips := map[string]struct{}{}
	for _, email := range online.Users {
		list, err := client.GetStatsOnlineIpList(ctx, &statscmd.GetStatsRequest{Name: "user>>>" + email + ">>>online"})
		if status.Code(err) == codes.Unimplemented {
			stats.OnlineIPs = nil
			return nil
		}
		if err != nil {
			// Best effort: a user dropping offline between the two calls is
			// a stale-read race, not an API outage — skip their IPs rather
			// than fail the poll.
			continue
		}
		for ip := range list.Ips {
			ips[ip] = struct{}{}
		}
	}
	uniqueIPs := len(ips)
	stats.OnlineIPs = &uniqueIPs
	return nil
}

// inboundTraffic reports whether a counter name is per-inbound user traffic
// ("inbound>>>TAG>>>traffic>>>uplink|downlink") and its direction. The "api"
// inbound is excluded: the panel's own polling flows through it and must not
// inflate the user-traffic totals (the api tag is fixed by SPEC.md §2).
func inboundTraffic(name string) (uplink, ok bool) {
	parts := strings.Split(name, ">>>")
	if len(parts) != 4 || parts[0] != "inbound" || parts[1] == "api" || parts[2] != "traffic" {
		return false, false
	}
	switch parts[3] {
	case "uplink":
		return true, true
	case "downlink":
		return false, true
	}
	return false, false
}
