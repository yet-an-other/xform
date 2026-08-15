package hoststats

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

const rootDiskPath = "/"

// Collector reads a fresh host snapshot from the operating system.
type Collector struct{}

// NewCollector creates a host statistics collector.
func NewCollector() *Collector {
	return &Collector{}
}

// Collect returns the host's current CPU, memory, storage, uptime, and load.
func (*Collector) Collect(ctx context.Context) (Stats, error) {
	cpuPercent, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		return Stats{}, fmt.Errorf("read CPU usage: %w", err)
	}
	if len(cpuPercent) != 1 {
		return Stats{}, fmt.Errorf("read CPU usage: expected one aggregate value, got %d", len(cpuPercent))
	}

	cpuCores, err := cpu.CountsWithContext(ctx, true)
	if err != nil {
		return Stats{}, fmt.Errorf("read CPU count: %w", err)
	}

	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("read memory usage: %w", err)
	}

	storage, err := disk.UsageWithContext(ctx, rootDiskPath)
	if err != nil {
		return Stats{}, fmt.Errorf("read storage usage: %w", err)
	}

	uptime, err := host.UptimeWithContext(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("read host uptime: %w", err)
	}

	loadAverage, err := load.AvgWithContext(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("read load average: %w", err)
	}

	return Stats{
		CollectedAt:    time.Now().Unix(),
		CPUPercent:     clampPercent(cpuPercent[0]),
		CPUCores:       cpuCores,
		MemUsedBytes:   memory.Used,
		MemTotalBytes:  memory.Total,
		DiskPath:       rootDiskPath,
		DiskUsedBytes:  storage.Used,
		DiskTotalBytes: storage.Total,
		UptimeSeconds:  uptime,
		LoadAvg:        [3]float64{loadAverage.Load1, loadAverage.Load5, loadAverage.Load15},
	}, nil
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
