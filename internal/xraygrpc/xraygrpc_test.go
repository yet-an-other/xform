package xraygrpc_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	statscmd "github.com/xtls/xray-core/app/stats/command"

	"github.com/yet-an-other/xform/internal/xraygrpc"
)

// fakeStatsService implements the generated statscmd.StatsServiceClient.
type fakeStatsService struct {
	sys        *statscmd.SysStatsResponse
	counters   *statscmd.QueryStatsResponse
	online     *statscmd.GetAllOnlineUsersResponse
	onlineErr  error
	ipLists    map[string]map[string]int64 // stat name → ip → last_seen
	ipListErrs map[string]error            // stat name → forced error
}

func (f *fakeStatsService) GetStats(context.Context, *statscmd.GetStatsRequest, ...grpc.CallOption) (*statscmd.GetStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeStatsService) GetStatsOnline(context.Context, *statscmd.GetStatsRequest, ...grpc.CallOption) (*statscmd.GetStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeStatsService) QueryStats(context.Context, *statscmd.QueryStatsRequest, ...grpc.CallOption) (*statscmd.QueryStatsResponse, error) {
	return f.counters, nil
}

func (f *fakeStatsService) GetSysStats(context.Context, *statscmd.SysStatsRequest, ...grpc.CallOption) (*statscmd.SysStatsResponse, error) {
	return f.sys, nil
}

func (f *fakeStatsService) GetStatsOnlineIpList(_ context.Context, req *statscmd.GetStatsRequest, _ ...grpc.CallOption) (*statscmd.GetStatsOnlineIpListResponse, error) {
	if err, ok := f.ipListErrs[req.Name]; ok {
		return nil, err
	}
	ips, ok := f.ipLists[req.Name]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such user")
	}
	return &statscmd.GetStatsOnlineIpListResponse{Name: req.Name, Ips: ips}, nil
}

func (f *fakeStatsService) GetAllOnlineUsers(context.Context, *statscmd.GetAllOnlineUsersRequest, ...grpc.CallOption) (*statscmd.GetAllOnlineUsersResponse, error) {
	return f.online, f.onlineErr
}

func dialToClient(client statscmd.StatsServiceClient) func(context.Context) (statscmd.StatsServiceClient, func(), error) {
	return func(context.Context) (statscmd.StatsServiceClient, func(), error) {
		return client, func() {}, nil
	}
}

func TestGRPCStatsMapsSysstatsCountersAndOnlineUsers(t *testing.T) {
	service := &fakeStatsService{
		sys: &statscmd.SysStatsResponse{Alloc: 88_080_384, NumGoroutine: 183},
		counters: &statscmd.QueryStatsResponse{Stat: []*statscmd.Stat{
			{Name: "inbound>>>vless>>>traffic>>>uplink", Value: 30_000},
			{Name: "inbound>>>vless>>>traffic>>>downlink", Value: 400_000},
			{Name: "inbound>>>trojan>>>traffic>>>uplink", Value: 9_000},
			{Name: "inbound>>>trojan>>>traffic>>>downlink", Value: 111_000},
			// The panel's own gRPC polling flows through the api inbound —
			// it must not inflate the user-traffic totals.
			{Name: "inbound>>>api>>>traffic>>>uplink", Value: 1_000},
			{Name: "inbound>>>api>>>traffic>>>downlink", Value: 1_000},
			// Outbound and per-user counters are not the xray-row totals.
			{Name: "outbound>>>direct>>>traffic>>>uplink", Value: 777_000},
			{Name: "user>>>alice@example.com>>>traffic>>>uplink", Value: 5_000},
		}},
		online: &statscmd.GetAllOnlineUsersResponse{Users: []string{
			// xray returns the online-map keys — full stat names, not bare
			// emails (pinned by TestPresenceAgainstRealXrayCore).
			"user>>>alice@example.com>>>online",
			"user>>>bob@example.com>>>online",
		}},
		ipLists: map[string]map[string]int64{
			"user>>>alice@example.com>>>online": {"203.0.113.10": 1_780_000_000, "203.0.113.11": 1_780_000_001},
			"user>>>bob@example.com>>>online":   {"203.0.113.11": 1_780_000_002, "198.51.100.7": 1_780_000_003},
		},
	}
	stats := xraygrpc.Client{Address: "127.0.0.1:8080", Dial: dialToClient(service)}

	runtime, err := stats.QueryRuntime(context.Background())
	if err != nil {
		t.Fatalf("query stats: %v", err)
	}
	if runtime.MemBytes != 88_080_384 || runtime.Goroutines != 183 {
		t.Errorf("process = %d bytes / %d goroutines, want 88080384/183", runtime.MemBytes, runtime.Goroutines)
	}
	if runtime.UpBytes != 39_000 || runtime.DownBytes != 511_000 {
		t.Errorf("traffic = %d/%d, want 39000/511000 (inbound only, api excluded)", runtime.UpBytes, runtime.DownBytes)
	}
	if runtime.OnlineUsers == nil || *runtime.OnlineUsers != 2 {
		t.Errorf("online users = %v, want 2", runtime.OnlineUsers)
	}
	if runtime.OnlineIPs == nil || *runtime.OnlineIPs != 3 {
		t.Errorf("online IPs = %v, want 3 (203.0.113.11 shared by two users counts once)", runtime.OnlineIPs)
	}
}

func TestGRPCStatsToleratesOlderServersWithoutOnlineRPCs(t *testing.T) {
	service := &fakeStatsService{
		sys:       &statscmd.SysStatsResponse{Alloc: 1024, NumGoroutine: 7},
		counters:  &statscmd.QueryStatsResponse{},
		onlineErr: status.Error(codes.Unimplemented, "unknown method GetAllOnlineUsers"),
	}
	stats := xraygrpc.Client{Address: "127.0.0.1:8080", Dial: dialToClient(service)}

	runtime, err := stats.QueryRuntime(context.Background())
	if err != nil {
		t.Fatalf("query stats: %v", err)
	}
	if runtime.OnlineUsers != nil || runtime.OnlineIPs != nil {
		t.Errorf("online = %v/%v, want null on an old server (SPEC.md §3 degrade)", runtime.OnlineUsers, runtime.OnlineIPs)
	}
	if runtime.MemBytes != 1024 {
		t.Errorf("mem = %d, want 1024 — the rest of the poll still lands", runtime.MemBytes)
	}
}

func TestGRPCStatsPropagatesRealFailures(t *testing.T) {
	stats := xraygrpc.Client{
		Address: "127.0.0.1:8080",
		Dial: func(context.Context) (statscmd.StatsServiceClient, func(), error) {
			return nil, nil, errors.New("connection refused")
		},
	}

	if _, err := stats.QueryRuntime(context.Background()); err == nil {
		t.Fatal("query stats succeeded with a failing dial")
	}
}

func TestQueryPresenceMapsOnlineUsersIPsAndLastSeen(t *testing.T) {
	service := &fakeStatsService{
		online: &statscmd.GetAllOnlineUsersResponse{Users: []string{"alice@example.com", "bob@example.com", "carol@example.com"}},
		ipLists: map[string]map[string]int64{
			"user>>>alice@example.com>>>online": {"203.0.113.11": 1_780_000_005, "203.0.113.10": 1_780_000_000},
		},
		ipListErrs: map[string]error{
			// A mixed-version server without the IP-list RPC: online, but
			// the IPs are unknown rather than the user vanishing.
			"user>>>bob@example.com>>>online": status.Error(codes.Unimplemented, "unknown method GetStatsOnlineIpList"),
			// carol dropped offline between the roster and the IP-list calls:
			// a stale-read race, so she is absent rather than an error.
		},
	}
	client := xraygrpc.Client{Address: "127.0.0.1:8080", Dial: dialToClient(service)}

	presence, supported, err := client.QueryPresence(context.Background())
	if err != nil {
		t.Fatalf("query presence: %v", err)
	}
	if !supported {
		t.Fatal("supported = false on a server with the online RPCs")
	}
	if len(presence) != 2 {
		t.Fatalf("online = %d, want 2 (carol raced offline): %+v", len(presence), presence)
	}
	alice := presence[0]
	if alice.Email != "alice@example.com" {
		t.Errorf("presence[0] = %s, want alice@example.com", alice.Email)
	}
	if len(alice.IPs) != 2 || alice.IPs[0] != "203.0.113.10" || alice.IPs[1] != "203.0.113.11" {
		t.Errorf("alice IPs = %v, want [203.0.113.10 203.0.113.11] (sorted)", alice.IPs)
	}
	if alice.LastSeen != 1_780_000_005 {
		t.Errorf("alice last_seen = %d, want the max per-IP value 1780000005", alice.LastSeen)
	}
	if bob := presence[1]; bob.LastSeen != 0 || bob.IPs != nil {
		t.Errorf("bob = %+v, want online without an IP list (no per-IP timestamps)", bob)
	}
}

func TestQueryPresenceToleratesOlderServersWithoutOnlineRPCs(t *testing.T) {
	service := &fakeStatsService{
		onlineErr: status.Error(codes.Unimplemented, "unknown method GetAllOnlineUsers"),
	}
	client := xraygrpc.Client{Address: "127.0.0.1:8080", Dial: dialToClient(service)}

	presence, supported, err := client.QueryPresence(context.Background())
	if err != nil {
		t.Fatalf("query presence: %v — Unimplemented is a degrade, not an error (SPEC.md §3)", err)
	}
	if supported {
		t.Error("supported = true on a server predating the online RPCs")
	}
	if presence != nil {
		t.Errorf("presence = %v, want nil", presence)
	}
}

func TestQueryPresencePropagatesRealFailures(t *testing.T) {
	service := &fakeStatsService{
		onlineErr: errors.New("connection reset"),
	}
	client := xraygrpc.Client{Address: "127.0.0.1:8080", Dial: dialToClient(service)}

	if _, _, err := client.QueryPresence(context.Background()); err == nil {
		t.Fatal("query presence succeeded with a failing online RPC")
	}
}

func TestQueryPresenceFailsWhenTheIPListCallFails(t *testing.T) {
	service := &fakeStatsService{
		online: &statscmd.GetAllOnlineUsersResponse{Users: []string{"alice@example.com"}},
		ipListErrs: map[string]error{
			"user>>>alice@example.com>>>online": status.Error(codes.Unavailable, "transport is closing"),
		},
	}
	client := xraygrpc.Client{Address: "127.0.0.1:8080", Dial: dialToClient(service)}

	if _, _, err := client.QueryPresence(context.Background()); err == nil {
		t.Fatal("query presence succeeded with a failing IP-list RPC — only NotFound means raced offline")
	}
}

func TestQueryUserTrafficMapsPerUserCounters(t *testing.T) {
	service := &fakeStatsService{
		counters: &statscmd.QueryStatsResponse{Stat: []*statscmd.Stat{
			{Name: "user>>>alice@example.com>>>traffic>>>uplink", Value: 12_400},
			{Name: "user>>>alice@example.com>>>traffic>>>downlink", Value: 148_200},
			{Name: "user>>>bob@example.com>>>traffic>>>uplink", Value: 3_100},
			{Name: "user>>>bob@example.com>>>traffic>>>downlink", Value: 41_700},
			// Presence counters and inbound/outbound aggregates are not
			// per-user traffic.
			{Name: "user>>>alice@example.com>>>online", Value: 1},
			{Name: "inbound>>>vless>>>traffic>>>uplink", Value: 999_000},
		}},
	}
	client := xraygrpc.Client{Address: "127.0.0.1:8080", Dial: dialToClient(service)}

	traffic, err := client.QueryUserTraffic(context.Background())
	if err != nil {
		t.Fatalf("query user traffic: %v", err)
	}
	if len(traffic) != 2 {
		t.Fatalf("users = %d, want 2: %+v", len(traffic), traffic)
	}
	if traffic[0].Email != "alice@example.com" || traffic[0].UpBytes != 12_400 || traffic[0].DownBytes != 148_200 {
		t.Errorf("alice = %+v, want 12400/148200", traffic[0])
	}
	if traffic[1].Email != "bob@example.com" || traffic[1].UpBytes != 3_100 || traffic[1].DownBytes != 41_700 {
		t.Errorf("bob = %+v, want 3100/41700", traffic[1])
	}
}
