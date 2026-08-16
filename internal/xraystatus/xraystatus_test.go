package xraystatus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/xraystatus"
)

// fakeUnit is a scripted UnitQuerier.
type fakeUnit struct {
	info xraystatus.UnitInfo
	err  error
}

func (f fakeUnit) QueryUnit(context.Context, string) (xraystatus.UnitInfo, error) {
	return f.info, f.err
}

// fakeVersion is a scripted VersionRunner.
type fakeVersion struct {
	version string
	err     error
}

func (f fakeVersion) Version(context.Context, string) (string, error) {
	return f.version, f.err
}

var testClock = func() time.Time { return time.Unix(1_780_000_000, 0) }

func TestStoppedUnitReportsStoppedWithoutVersion(t *testing.T) {
	collector := xraystatus.NewCollector(
		fakeUnit{info: xraystatus.UnitInfo{ActiveState: "inactive", SubState: "dead"}},
		fakeVersion{version: "26.4.13"},
		"xray.service",
	).WithClock(testClock)

	status, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if status.Status != "stopped" {
		t.Errorf("state = %q, want stopped", status.Status)
	}
	if status.Version != nil {
		t.Errorf("version = %v, want null when stopped", *status.Version)
	}
	if status.UptimeSeconds != 0 {
		t.Errorf("uptime = %d, want 0 when stopped", status.UptimeSeconds)
	}
	if status.CollectedAt != 1_780_000_000 {
		t.Errorf("collected_at = %d, want the collection time", status.CollectedAt)
	}
}

func TestUnqueryableUnitReportsUnreachable(t *testing.T) {
	collector := xraystatus.NewCollector(
		fakeUnit{err: errors.New("dbus down")},
		fakeVersion{},
		"xray.service",
	).WithClock(testClock)

	status, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect returned an error; the 200-always contract means unreachable is data: %v", err)
	}
	if status.Status != "unreachable" {
		t.Errorf("state = %q, want unreachable", status.Status)
	}
	if status.Version != nil || status.UptimeSeconds != 0 {
		t.Errorf("status = %+v, want null version and zero uptime when unreachable", status)
	}
}

func TestActiveUnitReportsRunningWithUptimeAndVersion(t *testing.T) {
	collector := xraystatus.NewCollector(
		fakeUnit{info: xraystatus.UnitInfo{
			ActiveState: "active",
			SubState:    "running",
			ActiveSince: time.Unix(1_780_000_000-14*24*3600, 0), // up 14 days
			ExecPath:    "/usr/local/bin/xray",
		}},
		fakeVersion{version: "26.4.13"},
		"xray.service",
	).WithClock(testClock)

	status, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if status.Status != "running" {
		t.Errorf("state = %q, want running", status.Status)
	}
	if status.UptimeSeconds != 14*24*3600 {
		t.Errorf("uptime = %d, want %d (14 days)", status.UptimeSeconds, 14*24*3600)
	}
	if status.Version == nil || *status.Version != "26.4.13" {
		t.Errorf("version = %v, want 26.4.13", status.Version)
	}
}

func TestUnreadableVersionToleratedWhileRunning(t *testing.T) {
	collector := xraystatus.NewCollector(
		fakeUnit{info: xraystatus.UnitInfo{ActiveState: "active", ActiveSince: time.Unix(1_780_000_000, 0)}},
		fakeVersion{err: errors.New("exec failed")},
		"xray.service",
	).WithClock(testClock)

	status, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if status.Status != "running" {
		t.Errorf("state = %q, want running even when the version exec fails", status.Status)
	}
	if status.Version != nil {
		t.Errorf("version = %v, want null on exec failure", *status.Version)
	}
}

func TestBinaryVersion(t *testing.T) {
	cases := map[string]struct {
		output  string
		runErr  error
		want    string
		wantErr bool
	}{
		"current xray format": {
			"Xray 26.4.13 (Xray, Penetrates Everything.) 8ab9a61 (go1.26.1 linux/amd64)\nA unified platform for anti-censorship.\n",
			nil, "26.4.13", false,
		},
		"bare version line": {"Xray 1.8.24", nil, "1.8.24", false},
		"not xray output":   {"sh: xray: command not found", nil, "", true},
		"exec failure":      {"", errors.New("no such binary"), "", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runner := xraystatus.BinaryVersion{
				Run: func(context.Context, string) ([]byte, error) { return []byte(tc.output), tc.runErr },
			}
			got, err := runner.Version(context.Background(), "/usr/local/bin/xray")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Version error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("Version = %q, want %q", got, tc.want)
			}
		})
	}
}
