// Package hoststats collects live statistics for the host running xform.
package hoststats

// Stats is the JSON contract returned by GET /api/v1/server.
type Stats struct {
	CollectedAt    int64      `json:"collected_at"`
	CPUPercent     float64    `json:"cpu_percent"`
	CPUCores       int        `json:"cpu_cores"`
	MemUsedBytes   uint64     `json:"mem_used_bytes"`
	MemTotalBytes  uint64     `json:"mem_total_bytes"`
	DiskPath       string     `json:"disk_path"`
	DiskUsedBytes  uint64     `json:"disk_used_bytes"`
	DiskTotalBytes uint64     `json:"disk_total_bytes"`
	UptimeSeconds  uint64     `json:"uptime_seconds"`
	LoadAvg        [3]float64 `json:"load_avg"`
}
