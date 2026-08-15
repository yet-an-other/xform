import { useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

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
    <Card className="min-h-[190px] gap-0 p-6 max-sm:min-h-[170px] max-sm:p-5">
      <div className="flex items-baseline justify-between gap-4">
        <h2 className="text-muted-foreground text-xs font-bold tracking-[0.13em] uppercase">
          {title}
        </h2>
        <span className="font-mono text-2xl font-semibold tracking-tight">{value}</span>
      </div>
      <div
        aria-label={`${title} usage`}
        aria-valuemax={100}
        aria-valuemin={0}
        aria-valuenow={Math.round(percent)}
        className="bg-secondary mt-9 mb-4 h-1.5 overflow-hidden rounded-full"
        role="progressbar"
      >
        <span
          className="block h-full rounded-full bg-gradient-to-r from-[#3fb98e] to-[#6ee6b9] shadow-[0_0_14px_rgba(85,214,168,0.3)] transition-[width] duration-300 motion-reduce:transition-none"
          style={{ width: `${percent}%` }}
        />
      </div>
      <p className="text-muted-foreground text-sm">{detail}</p>
    </Card>
  );
}

function HostDetail({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-5 px-5 py-4 max-sm:flex-col max-sm:items-start max-sm:gap-2">
      <span className="text-muted-foreground text-xs">{label}</span>
      <strong className="truncate font-mono text-xs font-semibold">{value}</strong>
    </div>
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
    <main className="mx-auto w-[min(1180px,calc(100%-40px))] pt-16 pb-12 max-sm:w-[calc(100%-28px)] max-sm:pt-10">
      <header className="mb-8 flex items-end justify-between gap-6 max-sm:items-start">
        <div>
          <p className="text-primary mb-2 font-mono text-xs font-bold tracking-[0.16em] uppercase">
            xform panel
          </p>
          <h1 className="text-[clamp(2.25rem,6vw,4rem)] leading-none font-semibold tracking-[-0.055em]">
            Server
          </h1>
        </div>
        <Badge
          className="border-primary/30 bg-accent text-accent-foreground gap-2 px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase"
          variant="outline"
        >
          <span
            aria-hidden="true"
            className="bg-primary size-[7px] rounded-full shadow-[0_0_0_4px_rgba(85,214,168,0.12)]"
          />
          Live
        </Badge>
      </header>

      {error ? (
        <p className="border-destructive/30 bg-destructive/10 mb-4 rounded-lg border px-4 py-3 text-sm text-[#ffc3c8]">
          Unable to refresh: {error}
        </p>
      ) : null}

      {stats ? (
        <>
          <section aria-label="Server resources" className="grid gap-4 md:grid-cols-3">
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

          <section
            aria-label="Host details"
            className="divide-border bg-surface/80 mt-4 grid divide-y rounded-xl border md:grid-cols-3 md:divide-x md:divide-y-0"
          >
            <HostDetail label="Host uptime" value={formatUptime(stats.uptime_seconds)} />
            <HostDetail
              label="Load average"
              value={stats.load_avg.map((value) => value.toFixed(2)).join(" / ")}
            />
            <HostDetail
              label="Last collected"
              value={new Date(stats.collected_at * 1000).toLocaleTimeString()}
            />
          </section>
        </>
      ) : (
        <div className="grid gap-4 md:grid-cols-3" role="status">
          <span className="sr-only">Collecting live host stats…</span>
          <Skeleton className="min-h-[190px] rounded-xl" />
          <Skeleton className="min-h-[190px] rounded-xl" />
          <Skeleton className="min-h-[190px] rounded-xl" />
        </div>
      )}
    </main>
  );
}

export default App;
