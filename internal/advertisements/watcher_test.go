package advertisements_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

type sourceClock struct {
	mu  sync.Mutex
	now time.Time
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(document []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(document)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (c *sourceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *sourceClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func writeAdvertisements(t *testing.T, path, document string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write Advertised connection settings: %v", err)
	}
}

func waitForAdvertisements(t *testing.T, what string, condition func() bool) {
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

const firstAdvertisement = `{
	"version": 1,
	"advertisements": [{
		"inbound_tag": "first",
		"topology": "direct",
		"host": "first.example.com",
		"port": 443,
		"transport": {"type": "tcp"},
		"security": {"type": "tls"}
	}]
}`

const secondAdvertisement = `{
	"version": 1,
	"advertisements": [{
		"inbound_tag": "second",
		"topology": "direct",
		"host": "second.example.com",
		"port": 8443,
		"transport": {"type": "grpc", "service_name": "second"},
		"security": {"type": "tls"}
	}]
}`

func TestWatcherDistinguishesUnsetFromConfiguredButNeverLoaded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	unset := advertisements.NewWatcher("")
	unset.Start(ctx)
	unsetSnapshot := unset.Snapshot()
	if unsetSnapshot.Configured() || unsetSnapshot.Available() || unsetSnapshot.Stale || unsetSnapshot.Error != nil {
		t.Errorf("unset snapshot = %+v, want unconfigured and unavailable without error", unsetSnapshot)
	}

	missingPath := filepath.Join(t.TempDir(), "connections.json")
	configured := advertisements.NewWatcher(missingPath)
	configured.Start(ctx)
	missingSnapshot := configured.Snapshot()
	if !missingSnapshot.Configured() || missingSnapshot.Available() || missingSnapshot.Stale {
		t.Errorf("missing snapshot = %+v, want configured but never loaded", missingSnapshot)
	}
	if missingSnapshot.Error == nil || missingSnapshot.Error.Reason != advertisements.ReadFailed {
		t.Errorf("missing source error = %+v, want read_failed", missingSnapshot.Error)
	}
}

func TestWatcherWarnsOnceWithoutExposingUnknownInboundTags(t *testing.T) {
	dir := t.TempDir()
	xrayPath := filepath.Join(dir, "xray.json")
	writeAdvertisements(t, xrayPath, `{
		"inbounds": [{
			"tag": "known",
			"protocol": "vless",
			"settings": {"clients": [{"email": "alice@example.com"}]}
		}]
	}`)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	xraySource := xrayconfig.NewWatcher(xrayPath)
	xraySource.Start(ctx)

	advertisementPath := filepath.Join(dir, "connections.json")
	writeAdvertisements(t, advertisementPath, `{
		"version": 1,
		"advertisements": [
			{"inbound_tag":"known","topology":"direct","host":"known.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}},
			{"inbound_tag":"not-present-secret-tag","topology":"direct","host":"one.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}},
			{"inbound_tag":"not-present-secret-tag","topology":"direct","host":"two.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}
		]
	}`)
	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	watcher := advertisements.NewWatcher(advertisementPath).WithInbounds(xraySource).WithLogger(logger)
	watcher.Start(ctx)

	output := logs.String()
	if got := strings.Count(output, "reference no current xray inbound"); got != 1 {
		t.Errorf("unknown-inbound warnings = %d, want 1; logs: %s", got, output)
	}
	if !strings.Contains(output, "unknown_inbound_tags=1") {
		t.Errorf("warning lacks bounded unknown count: %s", output)
	}
	if strings.Contains(output, "not-present-secret-tag") {
		t.Errorf("warning exposed configured inbound tag: %s", output)
	}
}

func TestWatcherWarnsWhenXrayBecomesAvailableAfterAdvertisements(t *testing.T) {
	dir := t.TempDir()
	xrayPath := filepath.Join(dir, "xray.json")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	xraySource := xrayconfig.NewWatcher(xrayPath)
	xraySource.Start(ctx)

	advertisementPath := filepath.Join(dir, "connections.json")
	writeAdvertisements(t, advertisementPath, `{
		"version": 1,
		"advertisements": [{
			"inbound_tag":"not-present-secret-tag",
			"topology":"direct",
			"host":"edge.example.com",
			"port":443,
			"transport":{"type":"tcp"},
			"security":{"type":"tls"}
		}]
	}`)
	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	watcher := advertisements.NewWatcher(advertisementPath).WithInbounds(xraySource).WithLogger(logger)
	watcher.Start(ctx)
	if strings.Contains(logs.String(), "reference no current xray inbound") {
		t.Fatalf("warning emitted before xray config loaded: %s", logs.String())
	}

	writeAdvertisements(t, xrayPath, `{
		"inbounds": [{"tag":"different","protocol":"vless","settings":{"clients":[]}}]
	}`)
	waitForAdvertisements(t, "unknown-inbound warning after xray recovery", func() bool {
		return strings.Contains(logs.String(), "reference no current xray inbound")
	})
	output := logs.String()
	if !strings.Contains(output, "unknown_inbound_tags=1") || strings.Contains(output, "not-present-secret-tag") {
		t.Errorf("recovery warning is not bounded and safe: %s", output)
	}
}

func TestWatcherDefersUnknownWarningWhileXrayViewIsStale(t *testing.T) {
	dir := t.TempDir()
	xrayPath := filepath.Join(dir, "xray.json")
	writeAdvertisements(t, xrayPath, `{
		"inbounds": [{"tag":"old","protocol":"vless","settings":{"clients":[]}}]
	}`)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	xraySource := xrayconfig.NewWatcher(xrayPath)
	xraySource.Start(ctx)

	writeAdvertisements(t, xrayPath, `{"inbounds": [`)
	waitForAdvertisements(t, "stale xray view", func() bool {
		return xraySource.Snapshot().Stale
	})

	advertisementPath := filepath.Join(dir, "connections.json")
	writeAdvertisements(t, advertisementPath, `{
		"version": 1,
		"advertisements": [{
			"inbound_tag":"not-present-secret-tag",
			"topology":"direct",
			"host":"edge.example.com",
			"port":443,
			"transport":{"type":"tcp"},
			"security":{"type":"tls"}
		}]
	}`)
	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	watcher := advertisements.NewWatcher(advertisementPath).WithInbounds(xraySource).WithLogger(logger)
	watcher.Start(ctx)
	if strings.Contains(logs.String(), "reference no current xray inbound") {
		t.Fatalf("warning used a stale xray view: %s", logs.String())
	}

	writeAdvertisements(t, xrayPath, `{
		"inbounds": [{"tag":"different","protocol":"vless","settings":{"clients":[]}}]
	}`)
	waitForAdvertisements(t, "unknown warning after fresh xray recovery", func() bool {
		return strings.Contains(logs.String(), "reference no current xray inbound")
	})
	output := logs.String()
	if !strings.Contains(output, "unknown_inbound_tags=1") || strings.Contains(output, "not-present-secret-tag") {
		t.Errorf("recovery warning is not bounded and safe: %s", output)
	}
}

func TestWatcherReplacesSuccessAndRetainsLastValidAfterFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	writeAdvertisements(t, path, firstAdvertisement)
	firstLoad := time.Date(2026, time.August, 28, 8, 0, 0, 0, time.UTC)
	clock := &sourceClock{now: firstLoad}

	watcher := advertisements.NewWatcher(path).WithClock(clock.Now)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcher.Start(ctx)

	initial := watcher.Snapshot()
	if !initial.Configured() || !initial.Available() || initial.Stale || initial.Error != nil ||
		!initial.LoadedAt.Equal(firstLoad) {
		t.Fatalf("initial snapshot = %+v, want current first load", initial)
	}
	if got := initial.View.Advertisements(); len(got) != 1 || got[0].InboundTag != "first" {
		t.Fatalf("initial advertisements = %+v, want first", got)
	}

	secondLoad := firstLoad.Add(time.Hour)
	clock.Set(secondLoad)
	writeAdvertisements(t, path, secondAdvertisement)
	waitForAdvertisements(t, "successful replacement", func() bool {
		return watcher.Snapshot().LoadedAt.Equal(secondLoad)
	})
	current := watcher.Snapshot()
	if got := current.View.Advertisements(); len(got) != 1 || got[0].InboundTag != "second" {
		t.Fatalf("replacement advertisements = %+v, want only second", got)
	}

	clock.Set(secondLoad.Add(time.Hour))
	writeAdvertisements(t, path, `{"version":1,"advertisements":[]} {"secret":"must-not-leak"}`)
	waitForAdvertisements(t, "stale parse failure", func() bool {
		snapshot := watcher.Snapshot()
		return snapshot.Stale && snapshot.Error != nil && snapshot.Error.Reason == advertisements.ParseFailed
	})
	stale := watcher.Snapshot()
	if !stale.Available() || !stale.LoadedAt.Equal(secondLoad) {
		t.Errorf("stale snapshot = %+v, want retained second load", stale)
	}
	if got := stale.View.Advertisements(); len(got) != 1 || got[0].InboundTag != "second" {
		t.Errorf("stale advertisements = %+v, want retained second", got)
	}
	if stale.Error == nil || stale.Error.Message != "The Advertised connection settings file could not be parsed. Profiles use the last valid Advertised connection settings." {
		t.Errorf("stale parse error = %+v, want safe parse_failed message", stale.Error)
	}

	recoveredAt := secondLoad.Add(2 * time.Hour)
	clock.Set(recoveredAt)
	writeAdvertisements(t, path, firstAdvertisement)
	waitForAdvertisements(t, "recovery", func() bool {
		snapshot := watcher.Snapshot()
		return !snapshot.Stale && snapshot.Error == nil && snapshot.LoadedAt.Equal(recoveredAt)
	})

	writeAdvertisements(t, path, `{"version":2,"advertisements":[]}`)
	waitForAdvertisements(t, "unsupported version", func() bool {
		snapshot := watcher.Snapshot()
		return snapshot.Error != nil && snapshot.Error.Reason == advertisements.UnsupportedVersion
	})
	unsupported := watcher.Snapshot()
	if !unsupported.Stale || !unsupported.LoadedAt.Equal(recoveredAt) ||
		unsupported.Error.Message != "The Advertised connection settings version is not supported. Profiles use the last valid Advertised connection settings." {
		t.Errorf("unsupported-version snapshot = %+v", unsupported)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove Advertised connection settings: %v", err)
	}
	waitForAdvertisements(t, "stale read failure", func() bool {
		snapshot := watcher.Snapshot()
		return snapshot.Error != nil && snapshot.Error.Reason == advertisements.ReadFailed
	})
	readFailure := watcher.Snapshot()
	if !readFailure.Stale || !readFailure.LoadedAt.Equal(recoveredAt) {
		t.Errorf("read-failure snapshot = %+v, want retained recovery", readFailure)
	}
}
