package xraystatus_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/xraystatus"
)

// fakeStats is a scripted StatsQuerier.
type fakeStats struct {
	stats xraystatus.RuntimeStats
	err   error
}

func (f fakeStats) QueryStats(context.Context) (xraystatus.RuntimeStats, error) {
	return f.stats, f.err
}

func intPtr(v int) *int { return &v }

func TestRunningUnitReportsRuntimeStats(t *testing.T) {
	collector := xraystatus.NewCollector(
		&fakeUnit{info: xraystatus.UnitInfo{
			ActiveState: "active",
			SubState:    "running",
			ActiveSince: time.Unix(1_780_000_000, 0),
			ExecPath:    "/usr/local/bin/xray",
		}},
		fakeVersion{version: "26.4.13"},
		fakeStats{stats: xraystatus.RuntimeStats{
			MemBytes:    88_080_384,
			Goroutines:  183,
			UpBytes:     39_100_000_000,
			DownBytes:   511_400_000_000,
			OnlineUsers: intPtr(3),
			OnlineIPs:   intPtr(4),
		}},
		"xray.service",
	).WithClock(testClock)

	status, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if status.Status != "running" {
		t.Errorf("state = %q, want running", status.Status)
	}
	if status.MemBytes == nil || *status.MemBytes != 88_080_384 {
		t.Errorf("mem_bytes = %v, want 88080384", status.MemBytes)
	}
	if status.Goroutines == nil || *status.Goroutines != 183 {
		t.Errorf("goroutines = %v, want 183", status.Goroutines)
	}
	if status.UsersOnline == nil || *status.UsersOnline != 3 {
		t.Errorf("users_online = %v, want 3", status.UsersOnline)
	}
	if status.UniqueIPsOnline == nil || *status.UniqueIPsOnline != 4 {
		t.Errorf("unique_ips_online = %v, want 4", status.UniqueIPsOnline)
	}
}

func TestUnansweringStatsAPIReportsUnreachable(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// The unit is up and the binary answers, but the gRPC StatsService does
	// not (stats API disabled, or xray wedged): SPEC.md §3 degraded mode.
	collector := xraystatus.NewCollector(
		&fakeUnit{info: xraystatus.UnitInfo{
			ActiveState: "active",
			ActiveSince: time.Unix(1_780_000_000, 0),
			ExecPath:    "/usr/local/bin/xray",
		}},
		fakeVersion{version: "26.4.13"},
		fakeStats{err: errors.New("connection refused")},
		"xray.service",
	).WithClock(testClock)

	for range 3 {
		status, err := collector.Collect(context.Background())
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if status.Status != "unreachable" {
			t.Errorf("state = %q, want unreachable when the stats API does not answer", status.Status)
		}
		if status.SpeedUpBps != 0 || status.SpeedDownBps != 0 {
			t.Errorf("speeds = %d/%d, want 0 when unreachable", status.SpeedUpBps, status.SpeedDownBps)
		}
		if status.MemBytes != nil || status.Goroutines != nil || status.UsersOnline != nil || status.UniqueIPsOnline != nil {
			t.Errorf("status = %+v, want null process and online fields when unreachable", status)
		}
	}
	if got := strings.Count(buf.String(), "cannot query xray stats API"); got != 1 {
		t.Errorf("stats failure logged %d times across 3 polls, want 1; log:\n%s", got, buf.String())
	}
}

// scriptedStats plays back a sequence of raw counter values, one per poll.
type scriptedStats struct {
	raws  [][2]uint64 // {uplink, downlink} per poll
	calls int
	err   error
}

func (s *scriptedStats) QueryStats(context.Context) (xraystatus.RuntimeStats, error) {
	raw := s.raws[min(s.calls, len(s.raws)-1)]
	s.calls++
	return xraystatus.RuntimeStats{UpBytes: raw[0], DownBytes: raw[1]}, s.err
}

func TestUnreachableKeepsDurableTotalsWhileSpeedsZero(t *testing.T) {
	stats := &scriptedStats{raws: [][2]uint64{{1_000, 10_000}, {1_500, 11_000}}}
	collector := xraystatus.NewCollector(
		&fakeUnit{info: xraystatus.UnitInfo{ActiveState: "active", ActiveSince: time.Unix(1_780_000_000, 0)}},
		fakeVersion{},
		stats,
		"xray.service",
	).WithClock(testClock)

	for range 2 {
		if _, err := collector.Collect(context.Background()); err != nil {
			t.Fatalf("collect: %v", err)
		}
	}

	// The stats API stops answering: degraded, but the durable totals the
	// panel already accumulated stay on the payload (SPEC.md §3).
	stats.err = errors.New("connection refused")
	status, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if status.Status != "unreachable" {
		t.Errorf("state = %q, want unreachable", status.Status)
	}
	if status.TotalUpBytes != 1_500 || status.TotalDownBytes != 11_000 {
		t.Errorf("totals = %d/%d, want the last-known 1500/11000 while unreachable", status.TotalUpBytes, status.TotalDownBytes)
	}
	if status.SpeedUpBps != 0 || status.SpeedDownBps != 0 {
		t.Errorf("speeds = %d/%d, want 0 while unreachable", status.SpeedUpBps, status.SpeedDownBps)
	}
}

func TestTrafficTotalsSurviveXrayRestartsAndSpeedsUseRecentDeltas(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	collector := xraystatus.NewCollector(
		&fakeUnit{info: xraystatus.UnitInfo{ActiveState: "active", ActiveSince: now}},
		fakeVersion{},
		&scriptedStats{raws: [][2]uint64{
			{1_000, 10_000}, // first poll: xray's lifetime counters seed the totals
			{1_500, 11_000}, // +500 / +1000 over 5s
			{2_500, 12_000}, // +1000 / +1000 over 5s
			{300, 700},      // xray restarted: counters reset, raw itself is the delta
		}},
		"xray.service",
	).WithClock(func() time.Time { return now })

	poll := func() xraystatus.Status {
		status, err := collector.Collect(context.Background())
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		now = now.Add(5 * time.Second)
		return status
	}

	// The first poll adopts the raw counters as the baseline: totals start
	// there, but there is no delta yet, so the speeds stay zero instead of
	// spiking with lifetime-traffic-per-poll.
	status := poll()
	if status.TotalUpBytes != 1_000 || status.TotalDownBytes != 10_000 {
		t.Errorf("totals = %d/%d, want the raw counters as the baseline", status.TotalUpBytes, status.TotalDownBytes)
	}
	if status.SpeedUpBps != 0 || status.SpeedDownBps != 0 {
		t.Errorf("speeds = %d/%d, want 0 on the baseline poll", status.SpeedUpBps, status.SpeedDownBps)
	}

	status = poll()
	if status.TotalUpBytes != 1_500 || status.TotalDownBytes != 11_000 {
		t.Errorf("totals = %d/%d, want 1500/11000", status.TotalUpBytes, status.TotalDownBytes)
	}
	if status.SpeedUpBps != 100 || status.SpeedDownBps != 200 {
		t.Errorf("speeds = %d/%d, want 100/200 (single delta over 5s)", status.SpeedUpBps, status.SpeedDownBps)
	}

	// SPEC.md §3: speed = mean of the last 2 deltas ÷ interval.
	status = poll()
	if status.SpeedUpBps != 150 || status.SpeedDownBps != 200 {
		t.Errorf("speeds = %d/%d, want 150/200 (mean of last 2 deltas over 5s)", status.SpeedUpBps, status.SpeedDownBps)
	}

	// The counter reset must not subtract: the post-restart raw counts as the
	// delta (SPEC.md §3 reconciliation), and the total keeps climbing.
	status = poll()
	if status.TotalUpBytes != 2_800 || status.TotalDownBytes != 12_700 {
		t.Errorf("totals = %d/%d after a counter reset, want 2800/12700", status.TotalUpBytes, status.TotalDownBytes)
	}
	if status.SpeedUpBps != 130 || status.SpeedDownBps != 170 {
		t.Errorf("speeds = %d/%d after a counter reset, want 130/170", status.SpeedUpBps, status.SpeedDownBps)
	}
}
