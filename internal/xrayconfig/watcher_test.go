package xrayconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/xrayconfig"
)

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

// waitFor polls until condition holds or the deadline passes — the watcher
// is asynchronous by design (fsnotify + debounce).
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
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

	// The acceptance criterion: config edits land without a panel restart.
	writeConfig(t, path, twoUserConfig)
	waitFor(t, "the edited roster", func() bool {
		roster, version = watcher.Roster()
		return len(roster) == 2 && version > 1
	})
	if _, ok := roster["bob@example.com"]; !ok {
		t.Errorf("edited roster = %v, want bob@example.com present", roster)
	}
}

// A broken write (a config saved half-edited) must not empty the roster:
// the watcher keeps serving the last good parse.
func TestWatcherKeepsLastGoodRosterOnParseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, oneUserConfig)

	watcher := xrayconfig.NewWatcher(path)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	_, initialVersion := watcher.Roster()

	writeConfig(t, path, `{"inbounds": [`) // truncated
	// Give the watcher time to (fail to) re-parse, then prove nothing moved.
	time.Sleep(600 * time.Millisecond)
	roster, version := watcher.Roster()
	if version != initialVersion || len(roster) != 1 {
		t.Errorf("after a broken write roster = %d users (version %d), want the last good 1 user (version %d)",
			len(roster), version, initialVersion)
	}

	// A fixed config is picked up again.
	writeConfig(t, path, twoUserConfig)
	waitFor(t, "the recovered roster", func() bool {
		roster, _ = watcher.Roster()
		return len(roster) == 2
	})
}

// Editors and config managers often replace the file atomically (write temp
// + rename). The watcher follows the path, not the inode.
func TestWatcherFollowsAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, oneUserConfig)

	watcher := xrayconfig.NewWatcher(path)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	temp := filepath.Join(dir, ".config.json.tmp")
	writeConfig(t, temp, twoUserConfig)
	if err := os.Rename(temp, path); err != nil {
		t.Fatalf("rename: %v", err)
	}
	waitFor(t, "the replaced roster", func() bool {
		roster, _ := watcher.Roster()
		return len(roster) == 2
	})
}

// A config that never parses (missing file, bad JSON) reports version 0 —
// "no roster yet", so the collector leaves every user's labels and gone
// flags alone rather than marking everyone gone on a panel misconfiguration.
func TestWatcherWithoutReadableConfigReportsNoRoster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	watcher := xrayconfig.NewWatcher(path)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	time.Sleep(300 * time.Millisecond)
	if roster, version := watcher.Roster(); version != 0 || roster != nil {
		t.Errorf("roster = %v (version %d), want nil at version 0 for a missing config", roster, version)
	}

	writeConfig(t, path, oneUserConfig)
	waitFor(t, "the appearing config", func() bool {
		_, version := watcher.Roster()
		return version > 0
	})
}
