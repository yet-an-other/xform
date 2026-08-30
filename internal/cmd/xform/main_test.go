package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yet-an-other/xform/internal/config"
)

// fakeSystemd answers the canonical-identity query the startup gate makes.
type fakeSystemd struct {
	id  string
	err error
}

func (f fakeSystemd) CanonicalID(context.Context, string) (string, error) {
	return f.id, f.err
}

// executableFile writes a file the Panel user may execute.
func executableFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journalctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake journalctl: %v", err)
	}
	return path
}

func TestJournalXrayUnitPassesTheStartupGate(t *testing.T) {
	cfg := config.Config{JournalctlPath: executableFile(t), XrayUnitName: "xray-vless.service"}

	// The unit the reader will use is systemd's identity, not the configured
	// spelling.
	unit, err := journalXrayUnit(context.Background(), cfg, fakeSystemd{id: "xray.service"})

	if err != nil {
		t.Fatalf("journalXrayUnit() error = %v, want nil", err)
	}
	if unit != "xray.service" {
		t.Errorf("unit = %q, want xray.service", unit)
	}
}

func TestJournalXrayUnitStopsStartupOnAnUnusableConfiguration(t *testing.T) {
	// SPEC §8: an unsafe reader path or a unit that cannot be resolved to one
	// canonical identity is a configuration error, caught before the Panel
	// serves rather than at the first log request.
	usable := executableFile(t)
	unreadable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(unreadable, []byte("plain"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tests := []struct {
		name    string
		cfg     config.Config
		systemd fakeSystemd
	}{
		{
			name: "the journalctl path is not executable",
			cfg:  config.Config{JournalctlPath: unreadable, XrayUnitName: "xray.service"},
		},
		{
			name: "the journalctl path is relative",
			cfg:  config.Config{JournalctlPath: "usr/bin/journalctl", XrayUnitName: "xray.service"},
		},
		{
			name: "the configured unit is a glob",
			cfg:  config.Config{JournalctlPath: usable, XrayUnitName: "xray*.service"},
		},
		{
			name: "the configured unit is shorthand",
			cfg:  config.Config{JournalctlPath: usable, XrayUnitName: "xray"},
		},
		{
			name:    "systemd cannot resolve the unit",
			cfg:     config.Config{JournalctlPath: usable, XrayUnitName: "xray.service"},
			systemd: fakeSystemd{err: errors.New("no such unit")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := journalXrayUnit(context.Background(), test.cfg, test.systemd); err == nil {
				t.Error("journalXrayUnit() error = nil, want startup refused")
			}
		})
	}
}
