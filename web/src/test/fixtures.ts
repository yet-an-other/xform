// Shared test fixtures: the canned observations every Dashboard test needs
// before it gets to the behaviour it is actually about.

export const stats = {
  collected_at: 1_723_800_000,
  cpu_percent: 23.4,
  cpu_cores: 4,
  mem_used_bytes: 5_100_273_664,
  mem_total_bytes: 8_589_934_592,
  disk_path: "/",
  disk_used_bytes: 90_194_313_216,
  disk_total_bytes: 171_798_691_840,
  uptime_seconds: 1_987_200,
  load_avg: [0.42, 0.38, 0.31] as [number, number, number],
};

export const xrayRunning = {
  collected_at: 1_723_800_000,
  status: "running",
  api_endpoint: "127.0.0.1:8080",
  version: "26.4.13",
  uptime_seconds: 1_209_600, // 14 days
  mem_bytes: 88_080_384,
  goroutines: 183,
  speed_up_bps: 2_400_000,
  speed_down_bps: 18_500_000,
  total_up_bytes: 39_100_000_000,
  total_down_bytes: 511_400_000_000,
  users_online: 1,
  unique_ips_online: 1,
};

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
