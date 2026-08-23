// Package xraygrpc reads xray's loopback gRPC StatsService: process stats
// and aggregate traffic for the xray row, raw per-user traffic counters for
// the users table. The API has no auth and no TLS
// (docs/research/xray-grpc-go-client.md §2) — loopback binding plus
// StatsService-only is the entire security model.
package xraygrpc

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	statscmd "github.com/xtls/xray-core/app/stats/command"
)

// pollTimeout bounds one poll below the collector's 5s cadence: a
// black-holed API address must degrade to unreachable, not stall the loop.
const pollTimeout = 4 * time.Second

// RuntimeStats is one poll of the StatsService: process stats plus the raw
// cumulative traffic counters (which reset whenever xray restarts).
type RuntimeStats struct {
	MemBytes    uint64
	Goroutines  uint32
	UpBytes     uint64 // raw cumulative inbound uplink
	DownBytes   uint64 // raw cumulative inbound downlink
	OnlineUsers *int   // nil when the server predates the online-user RPCs
	OnlineIPs   *int
}

// UserTraffic is one user's raw cumulative counters (reset on xray restart).
type UserTraffic struct {
	Email     string
	UpBytes   uint64
	DownBytes uint64
}

// UserPresence is one online user's live connection set: the IPs they are
// connected from and the most recent per-IP last_seen (unix seconds, 0 when
// the server reports none — xray tracks per-IP activity since v25.2.18).
type UserPresence struct {
	Email    string
	IPs      []string
	LastSeen int64
}

// Client is the StatsService client. It opens a fresh connection per poll —
// negligible at the 5s cadence, and nothing can rot.
type Client struct {
	Address string
	// Dial connects to the StatsService; nil dials Address with
	// grpc.NewClient and insecure credentials. The returned closer releases
	// the connection. Overridden in tests.
	Dial func(ctx context.Context) (statscmd.StatsServiceClient, func(), error)
}

func (c Client) connect(ctx context.Context) (statscmd.StatsServiceClient, func(), error) {
	dial := c.Dial
	if dial == nil {
		dial = func(context.Context) (statscmd.StatsServiceClient, func(), error) {
			conn, err := grpc.NewClient(c.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return nil, nil, err
			}
			return statscmd.NewStatsServiceClient(conn), func() { _ = conn.Close() }, nil
		}
	}
	client, closeConn, err := dial(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to xray stats API: %w", err)
	}
	return client, closeConn, nil
}

// QueryRuntime reads process stats, aggregate traffic, and online counts.
// Never passes reset=true — that would zero xray's counters (research §2).
func (c Client) QueryRuntime(ctx context.Context) (RuntimeStats, error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	client, closeConn, err := c.connect(ctx)
	if err != nil {
		return RuntimeStats{}, err
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

	if err := c.onlineCounts(ctx, client, &stats); err != nil {
		return RuntimeStats{}, err
	}
	return stats, nil
}

// QueryUserTraffic reads the raw per-user traffic counters — the roster of
// users with traffic, keyed by their email identity.
func (c Client) QueryUserTraffic(ctx context.Context) ([]UserTraffic, error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	client, closeConn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer closeConn()

	counters, err := client.QueryStats(ctx, &statscmd.QueryStatsRequest{Pattern: "", Reset_: false})
	if err != nil {
		return nil, fmt.Errorf("query stats: %w", err)
	}

	byEmail := map[string]*UserTraffic{}
	var order []string
	for _, counter := range counters.Stat {
		email, uplink, ok := userTraffic(counter.Name)
		if !ok {
			continue
		}
		user, seen := byEmail[email]
		if !seen {
			user = &UserTraffic{Email: email}
			byEmail[email] = user
			order = append(order, email)
		}
		if uplink {
			user.UpBytes += uint64(max(counter.Value, 0))
		} else {
			user.DownBytes += uint64(max(counter.Value, 0))
		}
	}
	traffic := make([]UserTraffic, 0, len(order))
	for _, email := range order {
		traffic = append(traffic, *byEmail[email])
	}
	return traffic, nil
}

// QueryPresence reads the live online set: who is connected, from which
// IPs, and when each IP was last seen (research §3). Servers predating
// the online-user RPCs answer Unimplemented → supported=false and no error:
// presence is omitted, never a failure (SPEC.md §3 degrade).
func (c Client) QueryPresence(ctx context.Context) (presence []UserPresence, supported bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	client, closeConn, err := c.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	defer closeConn()

	online, err := client.GetAllOnlineUsers(ctx, &statscmd.GetAllOnlineUsersRequest{})
	if status.Code(err) == codes.Unimplemented {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get online users: %w", err)
	}

	presence = make([]UserPresence, 0, len(online.Users))
	for _, email := range online.Users {
		list, err := client.GetStatsOnlineIpList(ctx, &statscmd.GetStatsRequest{Name: "user>>>" + email + ">>>online"})
		if status.Code(err) == codes.Unimplemented {
			// Online, but the server cannot say from where.
			presence = append(presence, UserPresence{Email: email})
			continue
		}
		if status.Code(err) == codes.NotFound {
			// xray answers NotFound for a vanished online map — the user
			// dropped offline between the two calls, a stale-read race
			// rather than an outage.
			continue
		}
		if err != nil {
			// Only NotFound is a raced-offline skip; anything else is a
			// presence outage — fail the poll rather than report users
			// offline on a guess.
			return nil, true, fmt.Errorf("get online IP list for %s: %w", email, err)
		}
		user := UserPresence{Email: email, IPs: make([]string, 0, len(list.Ips))}
		for ip, lastSeen := range list.Ips {
			user.IPs = append(user.IPs, ip)
			user.LastSeen = max(user.LastSeen, lastSeen)
		}
		slices.Sort(user.IPs)
		presence = append(presence, user)
	}
	return presence, true, nil
}

// onlineCounts fills the online user/IP counts, tolerating servers that
// predate the online-user RPCs (research §3): Unimplemented degrades to null
// counts, any other error fails the poll like the rest of the stats API.
func (c Client) onlineCounts(ctx context.Context, client statscmd.StatsServiceClient, stats *RuntimeStats) error {
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
	return direction(parts[3])
}

// userTraffic reports whether a counter name is per-user traffic
// ("user>>>EMAIL>>>traffic>>>uplink|downlink") and its direction.
func userTraffic(name string) (email string, uplink, ok bool) {
	parts := strings.Split(name, ">>>")
	if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
		return "", false, false
	}
	uplink, ok = direction(parts[3])
	return parts[1], uplink, ok
}

func direction(part string) (uplink, ok bool) {
	switch part {
	case "uplink":
		return true, true
	case "downlink":
		return false, true
	}
	return false, false
}
