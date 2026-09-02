package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/xraystatus"
)

// syncBuffer is a bytes.Buffer safe to read while the child process writes
// it — the boot log is polled before the panel exits.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPanelBootsAndListens is the process-level smoke test (v1.2.0 shipped a
// panel that never listened: a blocking roster Start kept main from reaching
// ListenAndServe, and every unit seam mocked the loop away). It builds the
// real binary and boots it through the real main: the startup gates must
// pass, and healthz must answer on the configured port within seconds.
//
// The journal gate needs a live systemd D-Bus; where none exists (CI
// containers), the test skips — the protection runs on systemd hosts, which
// is where panels deploy.
func TestPanelBootsAndListens(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the real binary")
	}
	probe, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := (xraystatus.SystemdUnit{}).CanonicalID(probe, "xray.service"); err != nil {
		t.Skipf("no systemd here; the boot would stop at the journal gate: %v", err)
	}

	// Build the binary under test from this package.
	binary := filepath.Join(t.TempDir(), "xform")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the panel: %v\n%s", err, out)
	}

	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick a port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	fakeJournalctl := filepath.Join(dir, "journalctl")
	if err := os.WriteFile(fakeJournalctl, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake journalctl: %v", err)
	}

	panel := exec.Command(binary)
	panel.Env = append(os.Environ(),
		"XFORM_PASSWORD=smoke",
		"XFORM_DB="+filepath.Join(dir, "xform.db"),
		"XFORM_LISTEN="+address,
		"XFORM_XRAY_API=127.0.0.1:1", // nothing there: degraded, never fatal
		"XFORM_XRAY_CONFIG="+filepath.Join(dir, "config.json"),
		"XFORM_JOURNALCTL="+fakeJournalctl,
	)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"inbounds":[]}`), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	var boot syncBuffer
	panel.Stdout = &boot
	panel.Stderr = &boot
	if err := panel.Start(); err != nil {
		t.Fatalf("start the panel: %v", err)
	}
	t.Cleanup(func() {
		_ = panel.Process.Kill()
		_, _ = panel.Process.Wait()
	})

	healthz := "http://" + address + "/api/v1/healthz"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(healthz)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				if !strings.Contains(boot.String(), "xform listening") {
					t.Errorf("healthz answered but the boot log never said listening:\n%s", boot.String())
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the panel never listened on %s; boot log:\n%s", address, boot.String())
}
