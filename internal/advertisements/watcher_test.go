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

// Reading, debouncing, and retaining the last valid parse belong to
// internal/filesource and are tested there; reason mapping and message
// wording are tested against the parse in source_test.go. What is left here
// is the one behaviour that needs both sources live: the bounded warning
// about advertisements that select no current xray inbound.

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

	waitForAdvertisements(t, "the unknown-inbound warning", func() bool {
		return strings.Contains(logs.String(), "reference no current xray inbound")
	})
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
	waitForAdvertisements(t, "the first advertisements load", func() bool {
		return watcher.Snapshot().Available()
	})
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

// A stale xray view is a view of a config that may already have changed, so
// it cannot say an inbound tag is missing.
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
	waitForAdvertisements(t, "the first advertisements load", func() bool {
		return watcher.Snapshot().Available()
	})
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
