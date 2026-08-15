package hoststats_test

import (
	"context"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/hoststats"
)

func TestCollectorReturnsLiveHostStats(t *testing.T) {
	before := time.Now().Unix()
	stats, err := hoststats.NewCollector().Collect(context.Background())
	after := time.Now().Unix()
	if err != nil {
		t.Fatalf("collect host stats: %v", err)
	}

	if stats.CollectedAt < before || stats.CollectedAt > after {
		t.Errorf("collected_at = %d, want between %d and %d", stats.CollectedAt, before, after)
	}
	if stats.CPUPercent < 0 || stats.CPUPercent > 100 {
		t.Errorf("cpu_percent = %v, want between 0 and 100", stats.CPUPercent)
	}
	if stats.CPUCores < 1 {
		t.Errorf("cpu_cores = %d, want at least 1", stats.CPUCores)
	}
	if stats.MemTotalBytes == 0 || stats.MemUsedBytes > stats.MemTotalBytes {
		t.Errorf("memory = %d/%d, want a valid used/total pair", stats.MemUsedBytes, stats.MemTotalBytes)
	}
	if stats.DiskPath != "/" {
		t.Errorf("disk_path = %q, want /", stats.DiskPath)
	}
	if stats.DiskTotalBytes == 0 || stats.DiskUsedBytes > stats.DiskTotalBytes {
		t.Errorf("disk = %d/%d, want a valid used/total pair", stats.DiskUsedBytes, stats.DiskTotalBytes)
	}
	if stats.UptimeSeconds == 0 {
		t.Error("uptime_seconds = 0, want a running host uptime")
	}
	for index, value := range stats.LoadAvg {
		if value < 0 {
			t.Errorf("load_avg[%d] = %v, want a non-negative value", index, value)
		}
	}
}
