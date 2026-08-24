import { useEffect, useState, type ReactNode } from "react";

import { MetricCard } from "@/components/metric-card";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
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
  fetchPanelInfo,
  fetchServerStats,
  fetchUsers,
  fetchXrayStatus,
  logout,
  UnauthenticatedError,
  type HostStats,
  type PanelInfo,
  type UsersSnapshot,
  type XrayStatus,
} from "@/lib/api";
import {
  formatBytes,
  formatAgo,
  formatSpeed,
  formatTime24,
  formatUptime,
  percentUsed,
} from "@/lib/format";

const POLL_INTERVAL_MS = 5_000;

// XrayCard is one tile of the xray row (SPEC §6): a labeled big readout
// with a sub-line, the same visual language as the server MetricCards but
// without a progress bar.
function XrayCard({ title, sub, children }: { title: string; sub: string; children: ReactNode }) {
  return (
    <Card className="min-h-[120px] gap-0 p-5">
      <h2 className="text-muted-foreground text-xs font-bold tracking-[0.13em] uppercase">
        {title}
      </h2>
      <div className="mt-3 font-mono text-lg font-semibold tracking-tight">{children}</div>
      <p className="text-muted-foreground mt-auto pt-3 text-xs">{sub}</p>
    </Card>
  );
}

// UsersTable is the per-user traffic table (SPEC §6): durable totals,
// current speed, presence (online dot, IPs, last seen), and the config
// labels (protocol · security). Gone users — edited out of the xray config,
// history retained — are hidden by default behind a toggle. Speeds read
// "stale" on a stale snapshot — xray is unreachable and the totals are
// last-known.
function UsersTable({ snapshot }: { snapshot: UsersSnapshot }) {
  const [showGone, setShowGone] = useState(false);
  const goneCount = snapshot.users.filter((user) => user.gone).length;
  const visible = showGone ? snapshot.users : snapshot.users.filter((user) => !user.gone);

  return (
    <section aria-label="Users" className="bg-surface/80 mt-4 overflow-hidden rounded-xl border">
      <div className="flex items-center justify-between gap-4 px-5 pt-4 pb-2">
        <h2 className="text-muted-foreground text-xs font-bold tracking-[0.13em] uppercase">
          Users
        </h2>
        {goneCount > 0 ? (
          <button
            type="button"
            onClick={() => setShowGone((shown) => !shown)}
            className="border-border text-muted-foreground hover:text-foreground rounded-lg border px-2.5 py-1 text-[0.7rem] font-bold tracking-[0.08em] uppercase"
          >
            {showGone ? "Hide gone" : `Show gone (${goneCount})`}
          </button>
        ) : null}
      </div>
      <Table className="table-fixed">
        <TableHeader>
          <TableRow className="text-muted-foreground text-[0.7rem] font-bold tracking-[0.08em] uppercase hover:bg-transparent">
            <TableHead className="w-10 px-5" aria-label="Online" />
            <TableHead>User</TableHead>
            <TableHead className="w-36">Protocol</TableHead>
            <TableHead className="w-24 text-right">Up</TableHead>
            <TableHead className="w-24 text-right">Down</TableHead>
            <TableHead className="w-52 text-right">Speed now</TableHead>
            <TableHead className="w-40">Online IPs</TableHead>
            <TableHead className="w-24 pr-5 text-right">Last seen</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {visible.length === 0 ? (
            <TableRow className="hover:bg-transparent">
              <TableCell className="text-muted-foreground px-5 py-4 text-xs" colSpan={8}>
                {goneCount > 0
                  ? `Every known user is gone — ${goneCount} hidden behind the toggle.`
                  : "No users with traffic yet."}
              </TableCell>
            </TableRow>
          ) : (
            visible.map((user) => (
              <TableRow key={user.email} className={user.gone ? "opacity-50" : undefined}>
                <TableCell className="px-5 py-1.5">
                  <span
                    aria-label={user.online ? "online" : "offline"}
                    className={`inline-block size-2 rounded-full ${
                      user.online ? "bg-primary shadow-primary/30 shadow-[0_0_6px]" : "bg-muted"
                    }`}
                  />
                </TableCell>
                <TableCell className="truncate py-1.5 font-semibold">
                  {user.email}
                  {user.gone ? (
                    <Badge
                      className="text-muted-foreground ml-2 px-1.5 py-0.5 align-middle text-[0.65rem] tracking-[0.08em] uppercase"
                      variant="outline"
                    >
                      gone
                    </Badge>
                  ) : null}
                </TableCell>
                <TableCell className="py-1.5">
                  {user.protocol !== null ? (
                    <>
                      <span className="text-muted-foreground">{user.protocol} · </span>
                      {user.security}
                    </>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="py-1.5 text-right font-mono text-xs">{formatBytes(user.up_bytes_total)}</TableCell>
                <TableCell className="py-1.5 text-right font-mono text-xs">{formatBytes(user.down_bytes_total)}</TableCell>
                <TableCell className="py-1.5 text-right font-mono text-xs">
                  {snapshot.stale ? (
                    <span className="text-muted-foreground">stale</span>
                  ) : user.speed_up_bps > 0 || user.speed_down_bps > 0 ? (
                    // One nowrap span per direction inside a flex-wrap
                    // container: the pair can wrap between directions but
                    // never overflow into the next column.
                    <div className="flex flex-wrap justify-end gap-x-1.5">
                      <span className="whitespace-nowrap">↑ {formatSpeed(user.speed_up_bps)}</span>{" "}
                      <span className="whitespace-nowrap">↓ {formatSpeed(user.speed_down_bps)}</span>
                    </div>
                  ) : (
                    <span className="text-muted-foreground">idle</span>
                  )}
                </TableCell>
                <TableCell className="truncate py-1.5 align-top font-mono text-xs">
                  {user.ips !== null && user.ips.length > 0 ? (
                    // One IP per line (SPEC §6) — a stacked list stays
                    // scannable when a user holds several connections.
                    user.ips.map((ip) => <div key={ip}>{ip}</div>)
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="py-1.5 pr-5 text-right font-mono text-xs">
                  {user.online ? (
                    "now"
                  ) : user.last_seen !== null ? (
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

// XrayRow is the four xray tiles (SPEC §6). Degraded mode keeps last-known
// durable totals on display but flags them stale — CONTEXT.md: stale data
// is shown, never hidden.
function XrayRow({ xray, rosterSize }: { xray: XrayStatus; rosterSize: number | null }) {
  const degraded = xray.status !== "running";
  const staleSub = "stale — last known";

  return (
    <section aria-label="xray row" className="mt-4 grid gap-4 md:grid-cols-2 lg:grid-cols-4">
      <XrayCard sub={degraded ? staleSub : "all users, sampled 5s"} title="Speed now">
        {degraded ? (
          <span className="text-muted-foreground">stale</span>
        ) : (
          <>
            <div className="text-primary">↑ {formatSpeed(xray.speed_up_bps)}</div>
            <div className="text-info">↓ {formatSpeed(xray.speed_down_bps)}</div>
          </>
        )}
      </XrayCard>
      <XrayCard sub={degraded ? staleSub : "durable totals"} title="Total traffic">
        <div>↑ {formatBytes(xray.total_up_bytes)}</div>
        <div>↓ {formatBytes(xray.total_down_bytes)}</div>
      </XrayCard>
      <XrayCard
        sub={
          degraded
            ? staleSub
            : xray.users_online !== null
              ? `${xray.unique_ips_online ?? 0} unique IPs`
              : "unavailable on this xray"
        }
        title="Users online"
      >
        {xray.users_online !== null && !degraded ? (
          <>
            {xray.users_online}
            {rosterSize !== null ? (
              <span className="text-muted-foreground"> / {rosterSize}</span>
            ) : null}
          </>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </XrayCard>
      <XrayCard
        sub={degraded ? staleSub : xray.goroutines !== null ? `${xray.goroutines} goroutines` : ""}
        title="xray process"
      >
        {xray.mem_bytes !== null && !degraded ? (
          formatBytes(xray.mem_bytes)
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </XrayCard>
    </section>
  );
}

export function Dashboard({ onUnauthenticated }: { onUnauthenticated: () => void }) {
  const [stats, setStats] = useState<HostStats | null>(null);
  const [xray, setXray] = useState<XrayStatus | null>(null);
  const [users, setUsers] = useState<UsersSnapshot | null>(null);
  const [panel, setPanel] = useState<PanelInfo | null>(null);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  const [error, setError] = useState<string | null>(null);

  // The panel's own version never changes at runtime — fetch it once.
  useEffect(() => {
    const controller = new AbortController();
    fetchPanelInfo(controller.signal)
      .then(setPanel)
      .catch(() => {
        // Cosmetic only — the header simply omits the version.
      });
    return () => controller.abort();
  }, []);

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
        setUpdatedAt(new Date());
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

  const degraded = xray !== null && xray.status !== "running";

  return (
    <main className="mx-auto w-[min(1180px,calc(100%-40px))] pt-8 pb-12 max-sm:w-[calc(100%-28px)]">
      <header className="mb-6 flex flex-wrap items-center gap-x-3 gap-y-2">
        <h1 className="text-lg font-semibold tracking-tight">xform</h1>
        {panel ? (
          <span className="text-muted-foreground font-mono text-xs">{panel.version}</span>
        ) : null}
        {xray ? <XrayPills xray={xray} /> : null}
        <span className="flex-1" />
        <span className="text-muted-foreground font-mono text-xs">
          refreshing every 5s{updatedAt ? ` · updated ${formatTime24(updatedAt)}` : ""}
        </span>
        <button
          type="button"
          onClick={() => void signOut()}
          className="border-border text-muted-foreground hover:text-foreground rounded-lg border px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase"
        >
          Log out
        </button>
      </header>

      {degraded ? (
        <p
          role="alert"
          className="border-destructive/30 bg-destructive/10 text-destructive-foreground mb-4 rounded-lg border px-4 py-3 text-sm"
        >
          {xray.status === "unreachable"
            ? `xray-core is unreachable — the gRPC API on ${xray.api_endpoint || "the configured address"} is not responding. Showing last known data: user speeds and online status are stale. Host stats stay live.`
            : "xray-core is stopped. Showing last known data: user speeds and online status are stale. Host stats stay live."}
        </p>
      ) : null}

      {error ? (
        <p className="border-destructive/30 bg-destructive/10 text-destructive-foreground mb-4 rounded-lg border px-4 py-3 text-sm">
          Unable to refresh: {error}
        </p>
      ) : null}

      {stats ? (
        <section aria-label="Server resources" className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
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
          <MetricCard
            detail={`load ${stats.load_avg.map((value) => value.toFixed(2)).join(" ")}`}
            title="Host uptime"
            value={formatUptime(stats.uptime_seconds)}
          />
        </section>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4" role="status">
          <span className="sr-only">Collecting live host stats…</span>
          <Skeleton className="min-h-[190px] rounded-xl" />
          <Skeleton className="min-h-[190px] rounded-xl" />
          <Skeleton className="min-h-[190px] rounded-xl" />
          <Skeleton className="min-h-[190px] rounded-xl" />
        </div>
      )}

      {xray ? (
        <XrayRow
          rosterSize={users ? users.users.filter((user) => !user.gone).length : null}
          xray={xray}
        />
      ) : null}

      {users ? <UsersTable snapshot={users} /> : null}
    </main>
  );
}
