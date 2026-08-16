import { useEffect, useState } from "react";

import { MetricCard } from "@/components/metric-card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { fetchServerStats, type HostStats } from "@/lib/api";
import { formatBytes, formatUptime, percentUsed } from "@/lib/format";

const POLL_INTERVAL_MS = 5_000;

function HostDetail({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-5 px-5 py-4 max-sm:flex-col max-sm:items-start max-sm:gap-2">
      <span className="text-muted-foreground text-xs">{label}</span>
      <strong className="truncate font-mono text-xs font-semibold">{value}</strong>
    </div>
  );
}

export function Dashboard() {
  const [stats, setStats] = useState<HostStats | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();

    async function poll() {
      try {
        setStats(await fetchServerStats(controller.signal));
        setError(null);
      } catch (cause) {
        if (!controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : "host stats unavailable");
        }
      }
    }

    void poll();
    const interval = window.setInterval(() => void poll(), POLL_INTERVAL_MS);
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
            className="bg-primary shadow-primary/10 size-[7px] rounded-full shadow-[0_0_0_4px]"
          />
          Live
        </Badge>
      </header>

      {error ? (
        <p className="border-destructive/30 bg-destructive/10 text-destructive-foreground mb-4 rounded-lg border px-4 py-3 text-sm">
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
              percent={percentUsed(stats.mem_used_bytes, stats.mem_total_bytes)}
              title="RAM"
              value={`${percentUsed(stats.mem_used_bytes, stats.mem_total_bytes).toFixed(1)}%`}
            />
            <MetricCard
              detail={`${formatBytes(stats.disk_used_bytes)} of ${formatBytes(stats.disk_total_bytes)} on ${stats.disk_path}`}
              percent={percentUsed(stats.disk_used_bytes, stats.disk_total_bytes)}
              title="Storage"
              value={`${percentUsed(stats.disk_used_bytes, stats.disk_total_bytes).toFixed(1)}%`}
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
