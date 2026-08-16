// Typed client for the panel's API (SPEC §5). One function per endpoint —
// the only module that knows URLs, headers, and payload shapes.

export interface HostStats {
  collected_at: number;
  cpu_percent: number;
  cpu_cores: number;
  mem_used_bytes: number;
  mem_total_bytes: number;
  disk_path: string;
  disk_used_bytes: number;
  disk_total_bytes: number;
  uptime_seconds: number;
  load_avg: [number, number, number];
}

export async function fetchServerStats(signal?: AbortSignal): Promise<HostStats> {
  const response = await fetch("/api/v1/server", {
    cache: "no-store",
    headers: { Accept: "application/json" },
    signal,
  });
  if (!response.ok) {
    throw new Error(`server returned ${response.status}`);
  }
  return (await response.json()) as HostStats;
}
