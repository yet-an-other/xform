package xrayconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
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

type watcherClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *watcherClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *watcherClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

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

func TestWatcherUpdatesViewWithoutChangingRosterVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, `{
		"inbounds": [{
			"tag": "before",
			"protocol": "vless",
			"settings": {"clients": [{"email": "alice@example.com", "id": "before-id"}]},
			"streamSettings": {"network": "raw", "security": "reality"}
		}]
	}`)
	firstLoad := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.UTC)
	clock := &watcherClock{now: firstLoad}

	watcher := xrayconfig.NewWatcher(path).WithClock(clock.Now)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)
	_, rosterVersion := watcher.Roster()

	secondLoad := firstLoad.Add(time.Minute)
	clock.Set(secondLoad)
	writeConfig(t, path, `{
		"inbounds": [{
			"tag": "after",
			"protocol": "vless",
			"settings": {"clients": [{"email": "alice@example.com", "id": "after-id"}]},
			"streamSettings": {"network": "raw", "security": "reality"}
		}]
	}`)
	waitFor(t, "profile-only config edit", func() bool {
		return watcher.Snapshot().LoadedAt.Equal(secondLoad)
	})

	snapshot := watcher.Snapshot()
	inbound := snapshot.View.Inbounds()[0]
	if inbound.Tag != "after" || inbound.Users()[0].ClientID != "after-id" {
		t.Errorf("updated view = %+v, want changed tag and Client ID", inbound)
	}
	if _, version := watcher.Roster(); version != rosterVersion {
		t.Errorf("roster version = %d after profile-only edit, want unchanged %d", version, rosterVersion)
	}
}

func TestWatcherPublishesOnlySuccessfulParsedViewChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, oneUserConfig)

	watcher := xrayconfig.NewWatcher(path)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)
	changes := watcher.Changes()

	writeConfig(t, path, `{"inbounds": [`)
	select {
	case <-changes:
		t.Fatal("received a parsed-view change for malformed config")
	case <-time.After(600 * time.Millisecond):
	}

	writeConfig(t, path, twoUserConfig)
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for successful parsed-view change")
	}
	if got := watcher.Snapshot().View.Inbounds()[0].Users(); len(got) != 2 {
		t.Errorf("changed view Users = %d, want 2", len(got))
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

func TestWatcherRetainsAStaleLastValidViewAfterParseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, oneUserConfig)
	loadedAt := time.Date(2026, time.August, 27, 8, 30, 0, 0, time.UTC)
	clock := &watcherClock{now: loadedAt}

	watcher := xrayconfig.NewWatcher(path).WithClock(clock.Now)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	initial := watcher.Snapshot()
	if !initial.Available() || !initial.LoadedAt.Equal(loadedAt) || initial.Stale || initial.Error != nil {
		t.Fatalf("initial snapshot = %+v, want current view loaded at %v", initial, loadedAt)
	}
	if got := initial.View.Inbounds(); len(got) != 1 || got[0].Users()[0].Email != "alice@example.com" {
		t.Fatalf("initial view = %+v, want alice's inbound", got)
	}
	_, initialRosterVersion := watcher.Roster()

	clock.Set(loadedAt.Add(time.Hour))
	writeConfig(t, path, `{"inbounds": []} {"privateKey": "must-not-leak"}`)
	waitFor(t, "stale parsed view", func() bool {
		return watcher.Snapshot().Stale
	})

	stale := watcher.Snapshot()
	if !stale.Available() || !stale.LoadedAt.Equal(loadedAt) {
		t.Errorf("stale snapshot loaded_at = %v (available %t), want retained %v", stale.LoadedAt, stale.Available(), loadedAt)
	}
	if stale.Error == nil || stale.Error.Reason != xrayconfig.ParseFailed {
		t.Fatalf("stale snapshot error = %+v, want parse_failed", stale.Error)
	}
	if got, want := stale.Error.Message, "The configured xray file could not be parsed; profiles use the last valid parse."; got != want {
		t.Errorf("safe error = %q, want %q", got, want)
	}
	if got := stale.View.Inbounds(); len(got) != 1 || got[0].Users()[0].Email != "alice@example.com" {
		t.Errorf("stale view = %+v, want retained alice inbound", got)
	}
	if roster, version := watcher.Roster(); version != initialRosterVersion || len(roster) != 1 {
		t.Errorf("stale roster = %v (version %d), want retained version %d", roster, version, initialRosterVersion)
	}

	recoveredAt := loadedAt.Add(2 * time.Hour)
	clock.Set(recoveredAt)
	writeConfig(t, path, twoUserConfig)
	waitFor(t, "recovered parsed view", func() bool {
		snapshot := watcher.Snapshot()
		return !snapshot.Stale && snapshot.LoadedAt.Equal(recoveredAt)
	})
	recovered := watcher.Snapshot()
	if recovered.Error != nil || len(recovered.View.Inbounds()[0].Users()) != 2 {
		t.Errorf("recovered snapshot = %+v, want current two-User view", recovered)
	}
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
	loadedAt := time.Date(2026, time.August, 27, 9, 45, 0, 0, time.UTC)

	watcher := xrayconfig.NewWatcher(path).WithClock(func() time.Time { return loadedAt })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	time.Sleep(300 * time.Millisecond)
	if roster, version := watcher.Roster(); version != 0 || roster != nil {
		t.Errorf("roster = %v (version %d), want nil at version 0 for a missing config", roster, version)
	}
	snapshot := watcher.Snapshot()
	if snapshot.Available() || snapshot.Stale || !snapshot.LoadedAt.IsZero() || len(snapshot.View.Inbounds()) != 0 {
		t.Errorf("never-loaded snapshot = %+v, want unavailable without a stale View", snapshot)
	}
	if snapshot.Error == nil || snapshot.Error.Reason != xrayconfig.ReadFailed ||
		snapshot.Error.Message != "The configured xray file could not be read." {
		t.Errorf("never-loaded error = %+v, want safe read_failed", snapshot.Error)
	}

	writeConfig(t, path, oneUserConfig)
	waitFor(t, "the appearing config", func() bool {
		return watcher.Snapshot().Available()
	})
	snapshot = watcher.Snapshot()
	if !snapshot.LoadedAt.Equal(loadedAt) || snapshot.Stale || snapshot.Error != nil {
		t.Errorf("first successful snapshot = %+v, want current View loaded at %v", snapshot, loadedAt)
	}
}
