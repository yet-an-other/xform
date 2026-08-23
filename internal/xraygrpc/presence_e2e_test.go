package xraygrpc_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	_ "github.com/xtls/xray-core/app/proxyman/inbound"  // registers the api inbound handler
	_ "github.com/xtls/xray-core/app/proxyman/outbound" // registers the default outbound handler
	xraycore "github.com/xtls/xray-core/core"
	xraystats "github.com/xtls/xray-core/features/stats"
	_ "github.com/xtls/xray-core/main/json"      // registers the JSON config loader
	_ "github.com/xtls/xray-core/proxy/dokodemo" // registers the api inbound's protocol
	_ "github.com/xtls/xray-core/proxy/freedom"  // registers the default outbound protocol

	"github.com/yet-an-other/xform/internal/xraygrpc"
)

// startXray boots a real xray-core instance with the StatsService on a
// loopback commander (the config shape of SPEC.md §2) and returns its
// address. This is the e2e seam: the real RPC server implementation, real
// proto wire, real client — only the traffic is absent.
func startXray(t *testing.T) (string, *xraycore.Instance) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	configJSON := fmt.Sprintf(`{
		"log": {"loglevel": "none"},
		"stats": {},
		"policy": {"levels": {"0": {"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}}},
		"api": {"tag": "api", "listen": "%s", "services": ["StatsService"]}
	}`, address)

	config, err := xraycore.LoadConfig("json", strings.NewReader(configJSON))
	if err != nil {
		t.Fatalf("parse xray config: %v", err)
	}
	instance, err := xraycore.New(config)
	if err != nil {
		t.Fatalf("create xray instance: %v", err)
	}
	if err := instance.Start(); err != nil {
		t.Fatalf("start xray instance: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	// The commander listener needs a moment to accept; poll until it does.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return address, instance
		}
		if time.Now().After(deadline) {
			t.Fatalf("xray API never came up on %s: %v", address, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// goOnline registers a user's online map the way xray's dispatcher does on a
// live connection (app/dispatcher/default.go): keyed by the full stat name,
// with the client IP refcounted in.
func goOnline(t *testing.T, instance *xraycore.Instance, email, ip string) {
	t.Helper()
	manager, ok := instance.GetFeature(xraystats.ManagerType()).(xraystats.Manager)
	if !ok {
		t.Fatal("stats manager not found on the instance")
	}
	onlineMap, err := xraystats.GetOrRegisterOnlineMap(manager, "user>>>"+email+">>>online")
	if err != nil {
		t.Fatalf("register online map: %v", err)
	}
	onlineMap.AddIP(ip)
}

// TestPresenceAgainstRealXrayCore is the regression loop for the format
// mismatch class of bug: whatever GetAllOnlineUsers really returns, the
// panel must resolve it back to bare emails and their IP lists.
func TestPresenceAgainstRealXrayCore(t *testing.T) {
	address, instance := startXray(t)
	goOnline(t, instance, "alice@example.com", "203.0.113.10")
	goOnline(t, instance, "alice@example.com", "203.0.113.11")
	goOnline(t, instance, "bob@example.com", "198.51.100.7")

	client := xraygrpc.Client{Address: address}
	presence, supported, err := client.QueryPresence(context.Background())
	if err != nil {
		t.Fatalf("query presence: %v", err)
	}
	if !supported {
		t.Fatal("supported = false against a current xray-core")
	}
	if len(presence) != 2 {
		t.Fatalf("online = %d, want 2: %+v", len(presence), presence)
	}

	var alice *xraygrpc.UserPresence
	for i := range presence {
		if presence[i].Email == "alice@example.com" {
			alice = &presence[i]
		}
	}
	if alice == nil {
		t.Fatalf("alice@example.com missing from presence: %+v — the panel could not resolve the online entry back to the email", presence)
	}
	if len(alice.IPs) != 2 || alice.IPs[0] != "203.0.113.10" || alice.IPs[1] != "203.0.113.11" {
		t.Errorf("alice IPs = %v, want [203.0.113.10 203.0.113.11]", alice.IPs)
	}
	if delta := time.Since(time.Unix(alice.LastSeen, 0)); delta < -time.Minute || delta > time.Minute {
		t.Errorf("alice last_seen = %d, want ~now (%v off)", alice.LastSeen, delta)
	}
}
