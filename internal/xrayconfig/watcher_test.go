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
		{"protocol": "vless", "settings": {"clients": [{"email": "alice@example.com"}]}, "streamSettings": {"security": "reality"}}
	]
}`

const twoUserConfig = `{
	"inbounds": [
		{"protocol": "vless", "settings": {"clients": [{"email": "alice@example.com"}, {"email": "bob@example.com"}]}, "streamSettings": {"security": "reality"}}
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
	if version == 0 || len(roster) != 1 {
		t.Fatalf("initial roster = %d users (version %d), want 1 user", len(roster), version)
	}
	if got := watcher.Snapshot().Value.View.Inbounds(); len(got) != 1 {
		t.Fatalf("initial view = %+v, want alice's inbound", got)
	}

	// The acceptance criterion: config edits land without a panel restart.
	writeConfig(t, path, twoUserConfig)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if roster, version = watcher.Roster(); len(roster) == 2 && version > 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(roster) != 2 {
		t.Fatalf("edited roster = %d users (version %d), want 2", len(roster), version)
	}
	if _, ok := roster["bob@example.com"]; !ok {
		t.Errorf("edited roster = %v, want bob@example.com present", roster)
	}
}
