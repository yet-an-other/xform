import { useEffect, useState } from "react";

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

function formatBytes(bytes: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = value >= 100 || unit === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(digits)} ${units[unit]}`;
}

function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86_400);
  if (days > 0) {
    return `${days} ${days === 1 ? "day" : "days"}`;
  }
  const hours = Math.floor(seconds / 3_600);
  if (hours > 0) {
    return `${hours} ${hours === 1 ? "hour" : "hours"}`;
  }
  const minutes = Math.floor(seconds / 60);
  return `${minutes} ${minutes === 1 ? "minute" : "minutes"}`;
}

function percentage(used: number, total: number): number {
  if (total <= 0) {
    return 0;
  }
  return Math.min(100, Math.max(0, (used / total) * 100));
}

interface MetricCardProps {
  title: string;
  value: string;
  detail: string;
  percent: number;
}

function MetricCard({ title, value, detail, percent }: MetricCardProps) {
  return (
    <article className="metric-card">
      <div className="metric-card__header">
        <h2>{title}</h2>
        <span className="metric-card__value">{value}</span>
      </div>
      <div
        aria-label={`${title} usage`}
        aria-valuemax={100}
        aria-valuemin={0}
        aria-valuenow={Math.round(percent)}
        className="meter"
        role="progressbar"
      >
        <span className="meter__fill" style={{ width: `${percent}%` }} />
      </div>
      <p>{detail}</p>
    </article>
  );
}

function App() {
  const [stats, setStats] = useState<HostStats | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();

    async function loadStats() {
      try {
        const response = await fetch("/api/v1/server", {
          cache: "no-store",
          headers: { Accept: "application/json" },
          signal: controller.signal,
        });
        if (!response.ok) {
          throw new Error(`server returned ${response.status}`);
        }
        setStats((await response.json()) as HostStats);
        setError(null);
      } catch (cause) {
        if (!controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : "host stats unavailable");
        }
      }
    }

    void loadStats();
    const interval = window.setInterval(() => void loadStats(), 5_000);
    return () => {
      window.clearInterval(interval);
      controller.abort();
    };
  }, []);

  return (
    <main>
      <header className="page-header">
        <div>
          <p className="eyebrow">xform panel</p>
          <h1>Server</h1>
        </div>
        <span className="live-status">
          <span aria-hidden="true" className="live-status__dot" />
          Live
        </span>
      </header>

      {error ? <p className="error-banner">Unable to refresh: {error}</p> : null}

      {stats ? (
        <>
          <section aria-label="Server resources" className="metrics-grid">
            <MetricCard
              detail={`${stats.cpu_cores} ${stats.cpu_cores === 1 ? "core" : "cores"}`}
              percent={stats.cpu_percent}
              title="CPU"
              value={`${stats.cpu_percent.toFixed(1)}%`}
            />
            <MetricCard
              detail={`${formatBytes(stats.mem_used_bytes)} of ${formatBytes(stats.mem_total_bytes)}`}
              percent={percentage(stats.mem_used_bytes, stats.mem_total_bytes)}
              title="RAM"
              value={`${percentage(stats.mem_used_bytes, stats.mem_total_bytes).toFixed(1)}%`}
            />
            <MetricCard
              detail={`${formatBytes(stats.disk_used_bytes)} of ${formatBytes(stats.disk_total_bytes)} on ${stats.disk_path}`}
              percent={percentage(stats.disk_used_bytes, stats.disk_total_bytes)}
              title="Storage"
              value={`${percentage(stats.disk_used_bytes, stats.disk_total_bytes).toFixed(1)}%`}
            />
          </section>

          <section aria-label="Host details" className="host-details">
            <div>
              <span>Host uptime</span>
              <strong>{formatUptime(stats.uptime_seconds)}</strong>
            </div>
            <div>
              <span>Load average</span>
              <strong>{stats.load_avg.map((value) => value.toFixed(2)).join(" / ")}</strong>
            </div>
            <div>
              <span>Last collected</span>
              <strong>{new Date(stats.collected_at * 1000).toLocaleTimeString()}</strong>
            </div>
          </section>
        </>
      ) : (
        <p className="loading" role="status">
          Collecting live host stats…
        </p>
      )}
    </main>
  );
}

export default App;
