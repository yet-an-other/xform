package xrayconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// Reading, debouncing, and retaining the last valid parse belong to
// internal/filesource and are tested there. What is left here is the wiring:
// that this Watcher parses an xray config into a Roster and keeps serving it
// as the file changes, without a panel restart.

const oneUserConfig = `{
	"inbounds": [
		{"tag": "vless-reality", "protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "id": "alice-uuid"}]}, "streamSettings": {"security": "reality"}}
	]
}`

const twoUserConfig = `{
	"inbounds": [
		{"tag": "vless-reality", "protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "id": "alice-uuid"}, {"email": "bob@example.com", "id": "bob-uuid"}]}, "streamSettings": {"security": "reality"}}
	]
}`

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestWatcherLoadsRosterAndPicksUpEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, oneUserConfig)

	watcher := xrayconfig.NewWatcher(path)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	roster, version := watcher.Roster()
	if version == 0 || len(roster.Labels) != 1 {
		t.Fatalf("initial roster = %d users (version %d), want 1 user", len(roster.Labels), version)
	}
	alice := roster.Clients["alice@example.com"]
	if alice.ClientID != "alice-uuid" || len(alice.Inbounds) != 1 || alice.Inbounds[0] != "vless-reality" {
		t.Fatalf("initial clients = %v, want alice's Client ID and attachment", roster.Clients)
	}
	if got := watcher.Snapshot().Value.View.Inbounds(); len(got) != 1 {
		t.Fatalf("initial view = %+v, want alice's inbound", got)
	}

	// The acceptance criterion: config edits land without a panel restart.
	writeConfig(t, path, twoUserConfig)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if roster, version = watcher.Roster(); len(roster.Labels) == 2 && version > 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(roster.Labels) != 2 {
		t.Fatalf("edited roster = %d users (version %d), want 2", len(roster.Labels), version)
	}
	if _, ok := roster.Labels["bob@example.com"]; !ok {
		t.Errorf("edited roster = %v, want bob@example.com present", roster.Labels)
	}
	bob := roster.Clients["bob@example.com"]
	if bob.ClientID != "bob-uuid" || len(bob.Inbounds) != 1 || bob.Inbounds[0] != "vless-reality" {
		t.Errorf("edited clients = %v, want bob's Client ID and attachment", roster.Clients)
	}
}
