import { useEffect, useState } from "react";

import { MetricCard } from "@/components/metric-card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  fetchServerStats,
  fetchUsers,
  fetchXrayStatus,
  logout,
  UnauthenticatedError,
  type HostStats,
  type UsersSnapshot,
  type XrayStatus,
} from "@/lib/api";
import { formatBytes, formatAgo, formatSpeed, formatUptime, percentUsed } from "@/lib/format";

const POLL_INTERVAL_MS = 5_000;

function HostDetail({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-5 px-5 py-4 max-sm:flex-col max-sm:items-start max-sm:gap-2">
      <span className="text-muted-foreground shrink-0 text-xs whitespace-nowrap">{label}</span>
      <strong className="min-w-0 truncate font-mono text-xs font-semibold">{value}</strong>
    </div>
  );
}

// UsersTable is the per-user traffic table (SPEC §6): durable totals and
// current speed now; presence and protocol columns fill in with their
// slices and render "—" until then. Speeds read "stale" on a stale
// snapshot — xray is unreachable and the totals are last-known.
function UsersTable({ snapshot }: { snapshot: UsersSnapshot }) {
  return (
    <section aria-label="Users" className="bg-surface/80 mt-4 overflow-hidden rounded-xl border">
      <h2 className="text-muted-foreground px-5 pt-4 pb-2 text-xs font-bold tracking-[0.13em] uppercase">
        Users
      </h2>
      <Table className="table-fixed">
        <TableHeader>
          <TableRow className="text-muted-foreground text-[0.7rem] font-bold tracking-[0.08em] uppercase hover:bg-transparent">
            <TableHead className="w-10 px-5" aria-label="Online" />
            <TableHead>User</TableHead>
            <TableHead className="w-36">Protocol</TableHead>
            <TableHead className="w-24 text-right">Up</TableHead>
            <TableHead className="w-24 text-right">Down</TableHead>
            <TableHead className="w-44 text-right">Speed now</TableHead>
            <TableHead className="w-40">Online IPs</TableHead>
            <TableHead className="w-24 pr-5 text-right">Last seen</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {snapshot.users.length === 0 ? (
            <TableRow className="hover:bg-transparent">
              <TableCell className="text-muted-foreground px-5 py-4 text-xs" colSpan={8}>
                No users with traffic yet.
              </TableCell>
            </TableRow>
          ) : (
            snapshot.users.map((user) => (
              <TableRow key={user.email}>
                <TableCell className="px-5">
                  <span
                    aria-label={user.online ? "online" : "offline"}
                    className={`inline-block size-2 rounded-full ${
                      user.online ? "bg-primary shadow-primary/30 shadow-[0_0_6px]" : "bg-muted"
                    }`}
                  />
                </TableCell>
                <TableCell className="truncate font-semibold">{user.email}</TableCell>
                <TableCell>
                  {user.protocol !== null ? (
                    <>
                      <span className="text-muted-foreground">{user.protocol} · </span>
                      {user.security}
                    </>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="text-right font-mono text-xs">{formatBytes(user.up_bytes_total)}</TableCell>
                <TableCell className="text-right font-mono text-xs">{formatBytes(user.down_bytes_total)}</TableCell>
                <TableCell className="text-right font-mono text-xs">
                  {snapshot.stale ? (
                    <span className="text-muted-foreground">stale</span>
                  ) : user.speed_up_bps > 0 || user.speed_down_bps > 0 ? (
                    `↑ ${formatSpeed(user.speed_up_bps)} ↓ ${formatSpeed(user.speed_down_bps)}`
                  ) : (
                    <span className="text-muted-foreground">idle</span>
                  )}
                </TableCell>
                <TableCell className="truncate font-mono text-xs">
                  {user.ips !== null && user.ips.length > 0 ? (
                    user.ips.join(", ")
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="pr-5 text-right font-mono text-xs">
                  {user.last_seen !== null ? (
                    formatAgo(user.last_seen)
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </section>
  );
}

function XrayPills({ xray }: { xray: XrayStatus }) {
  const dotClass =
    xray.status === "running"
      ? "bg-primary shadow-primary/10"
      : xray.status === "stopped"
        ? "bg-destructive shadow-destructive/10"
        : "bg-warning shadow-warning/10";

  return (
    <>
      <Badge
        className="gap-2 px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase"
        variant="outline"
      >
        <span
          aria-hidden="true"
          className={`size-[7px] rounded-full shadow-[0_0_0_4px] ${dotClass}`}
        />
        xray {xray.status}
      </Badge>
      {xray.version ? (
        <Badge className="px-3 py-1.5 font-mono text-[0.78rem] font-semibold" variant="outline">
          {xray.version}
        </Badge>
      ) : null}
      {xray.status === "running" ? (
        <Badge
          className="px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase"
          variant="outline"
        >
          up {formatUptime(xray.uptime_seconds)}
        </Badge>
      ) : null}
    </>
  );
}

export function Dashboard({ onUnauthenticated }: { onUnauthenticated: () => void }) {
  const [stats, setStats] = useState<HostStats | null>(null);
  const [xray, setXray] = useState<XrayStatus | null>(null);
  const [users, setUsers] = useState<UsersSnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();

    async function poll() {
      try {
        const [serverStats, xrayStats, usersSnapshot] = await Promise.all([
          fetchServerStats(controller.signal),
          fetchXrayStatus(controller.signal),
          fetchUsers(controller.signal),
        ]);
        setStats(serverStats);
        setXray(xrayStats);
        setUsers(usersSnapshot);
        setError(null);
      } catch (cause) {
        if (cause instanceof UnauthenticatedError) {
          onUnauthenticated();
          return;
        }
        if (!controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : "panel unavailable");
        }
      }
    }

    void poll();
    const interval = window.setInterval(() => void poll(), POLL_INTERVAL_MS);
    return () => {
      window.clearInterval(interval);
      controller.abort();
    };
  }, [onUnauthenticated]);

  async function signOut() {
    try {
      await logout();
      onUnauthenticated();
    } catch {
      setError("could not reach the panel — the session may still be active");
    }
  }

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
        <div className="flex items-center gap-3 max-sm:flex-wrap">
          {xray ? <XrayPills xray={xray} /> : null}
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
          <button
            type="button"
            onClick={() => void signOut()}
            className="border-border text-muted-foreground hover:text-foreground rounded-lg border px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase"
          >
            Log out
          </button>
        </div>
      </header>

      {xray && xray.status !== "running" ? (
        <p
          role="alert"
          className="mb-4 rounded-lg border border-warning/30 bg-warning/10 px-4 py-3 text-sm text-warning-foreground"
        >
          xray is {xray.status} — the panel is degraded; host stats stay live.
        </p>
      ) : null}

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

      {xray ? (
        <section
          aria-label="xray row"
          className="divide-border bg-surface/80 mt-4 grid divide-y rounded-xl border md:grid-cols-2 md:divide-y-0 lg:grid-cols-4 lg:divide-x"
        >
          <HostDetail
            label="Speed now"
            value={`↑ ${formatSpeed(xray.speed_up_bps)} · ↓ ${formatSpeed(xray.speed_down_bps)}`}
          />
          <HostDetail
            label="Total traffic"
            value={`↑ ${formatBytes(xray.total_up_bytes)} · ↓ ${formatBytes(xray.total_down_bytes)}`}
          />
          <HostDetail
            label="Users online"
            value={
              xray.users_online !== null
                ? `${xray.users_online} users · ${xray.unique_ips_online} IPs`
                : "unavailable on this xray"
            }
          />
          <HostDetail
            label="xray process"
            value={
              xray.mem_bytes !== null
                ? `${formatBytes(xray.mem_bytes)} · ${xray.goroutines} goroutines`
                : "unavailable"
            }
          />
        </section>
      ) : null}

      {users ? <UsersTable snapshot={users} /> : null}
    </main>
  );
}
