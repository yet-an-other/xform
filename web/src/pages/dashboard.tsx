import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

import { MetricCard } from "@/components/metric-card";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfigSnapshotModal } from "@/components/config-snapshot-modal";
import { AddUserModal } from "@/components/add-user-modal";
import { EditUserModal } from "@/components/edit-user-modal";
import { RemoveUserModal } from "@/components/remove-user-modal";
import { LogSnapshotModal } from "@/components/log-snapshot-modal";
import { UserDetailsModal } from "@/components/user-details-modal";
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
  type LogSource,
  type PanelInfo,
  type UsersSnapshot,
  type XrayStatus,
} from "@/lib/api";
import { cn } from "@/lib/utils";
import {
  formatBytes,
  formatAgo,
  formatSpeed,
  formatTime24,
  formatUptime,
  formatUptimeShort,
  flagEmoji,
  percentUsed,
} from "@/lib/format";

const POLL_INTERVAL_MS = 5_000;

// OpenDialog is the one dialog the Dashboard has open, if any. A single slot
// is what enforces "only one modal at a time" (SPEC §6).
type OpenDialog =
  | { kind: "details"; email: string }
  | { kind: "edit"; email: string }
  | { kind: "remove"; email: string }
  | { kind: "add" }
  | { kind: "logs"; source: LogSource }
  | { kind: "config" }
  | null;

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

// PresenceDot is the live indicator beside a User's name: glowing when
// online, muted otherwise.
function PresenceDot({ online }: { online: boolean }) {
  return (
    <span
      aria-label={online ? "online" : "offline"}
      className={`inline-block size-2 rounded-full ${
        online ? "bg-primary shadow-primary/30 shadow-[0_0_6px]" : "bg-muted"
      }`}
    />
  );
}

// InfoIcon is the details entry point glyph (user-management prototype):
// a circled i.
function InfoIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className="size-4 fill-none stroke-current stroke-[1.8] [stroke-linecap:round] [stroke-linejoin:round]"
    >
      <circle cx="12" cy="12" r="9" />
      <path d="M12 11v5M12 7.8h.01" />
    </svg>
  );
}

// EditIcon is the row-level edit glyph (user-management prototype): a
// pencil, opening the User's edit dialog.
function EditIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className="size-4 fill-none stroke-current stroke-[1.8] [stroke-linecap:round] [stroke-linejoin:round]"
    >
      <path d="M16.5 4.5l3 3L8 19l-4 1 1-4L16.5 4.5z" />
    </svg>
  );
}

// TrashIcon is the row-level remove glyph (user-management prototype): a
// bin, opening the User's remove confirmation.
function TrashIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className="size-4 fill-none stroke-current stroke-[1.8] [stroke-linecap:round] [stroke-linejoin:round]"
    >
      <path d="M4 7h16M10 11v6M14 11v6M6.5 7l1 12.2a1.8 1.8 0 0 0 1.8 1.8h5.4a1.8 1.8 0 0 0 1.8-1.8L17.5 7M9.5 7V5a1.5 1.5 0 0 1 1.5-1.5h2A1.5 1.5 0 0 1 14.5 5v2" />
    </svg>
  );
}

// LogsIcon and ConfigIcon are the approved viewer glyphs (operational-viewers
// prototype): stacked lines for a journal, a document for the configured file.
function LogsIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className="size-4 fill-none stroke-current stroke-[1.8] [stroke-linecap:round] [stroke-linejoin:round]"
    >
      <path d="M4 5h16M4 12h16M4 19h10" />
    </svg>
  );
}

function ConfigIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className="size-4 fill-none stroke-current stroke-[1.8] [stroke-linecap:round] [stroke-linejoin:round]"
    >
      <path d="M7 3h7l4 4v14H7zM14 3v5h4M10 13h5M10 17h5" />
    </svg>
  );
}

// IconAction is one icon-only control — a header snapshot action, a table
// row's details action. It always carries an accessible name and a visible
// title, because an icon alone names nothing (SPEC §6).
function IconAction({
  label,
  title,
  onOpen,
  className,
  children,
}: {
  label: string;
  title: string;
  onOpen: (opener: HTMLButtonElement) => void;
  className?: string;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={title ?? label}
      onClick={(event) => onOpen(event.currentTarget)}
      className={cn(
        "border-border hover:border-primary hover:text-primary hover:bg-accent inline-grid size-[30px] place-items-center rounded-lg border",
        className,
      )}
    >
      {children}
    </button>
  );
}

// UsersTable is the per-user traffic table (SPEC §6): durable totals,
// current speed, presence (online dot, IPs, last seen), and the config
// labels (protocol · security) — plus the named icon-only row actions
// (details, edit) opening the User dialogs (SPEC §6). Uplink and Downlink
// share one Traffic column on two lines. Gone users — edited out of the xray config,
// history retained — are hidden by default behind a toggle. Speeds read
// "stale" on a stale snapshot — xray is unreachable and the totals are
// last-known.
function UsersTable({
  snapshot,
  onOpenDetails,
  onOpenEdit,
  onOpenRemove,
  onOpenAdd,
}: {
  snapshot: UsersSnapshot;
  onOpenDetails: (email: string, opener: HTMLButtonElement) => void;
  onOpenEdit: (email: string, opener: HTMLButtonElement) => void;
  onOpenRemove: (email: string, opener: HTMLButtonElement) => void;
  onOpenAdd: (opener: HTMLButtonElement) => void;
}) {
  const [showGone, setShowGone] = useState(false);
  const goneCount = snapshot.users.filter((user) => user.gone).length;
  const visible = showGone ? snapshot.users : snapshot.users.filter((user) => !user.gone);

  return (
    <section aria-label="Users" className="bg-surface/80 mt-4 overflow-hidden rounded-xl border">
      <div className="flex items-center justify-between gap-4 px-5 pt-4 pb-2">
        <h2 className="text-muted-foreground text-xs font-bold tracking-[0.13em] uppercase">
          Users
          {/* The Roster sync state, shown only while not synced
              (user-management spec §6) — separate from the stale flag,
              which marks read-side last-good data. */}
          {snapshot.roster_sync === "failed" ? (
            <Badge
              variant="destructive"
              className="ml-2 px-1.5 py-0.5 align-middle text-[0.65rem] tracking-[0.08em] uppercase"
            >
              apply failed
            </Badge>
          ) : snapshot.roster_sync === "pending" ? (
            <Badge
              variant="outline"
              className="text-muted-foreground ml-2 px-1.5 py-0.5 align-middle text-[0.65rem] tracking-[0.08em] uppercase"
            >
              applying…
            </Badge>
          ) : null}
        </h2>
        <span className="flex items-center gap-2">
          {goneCount > 0 ? (
            <button
              type="button"
              onClick={() => setShowGone((shown) => !shown)}
              className="border-border text-muted-foreground hover:text-foreground rounded-lg border px-2.5 py-1 text-[0.7rem] font-bold tracking-[0.08em] uppercase"
            >
              {showGone ? "Hide gone" : `Show gone (${goneCount})`}
            </button>
          ) : null}
          <button
            type="button"
            onClick={(event) => onOpenAdd(event.currentTarget)}
            className="bg-primary text-primary-foreground rounded-lg px-2.5 py-1 text-[0.7rem] font-bold tracking-[0.08em] uppercase"
          >
            + Add user
          </button>
        </span>
      </div>
      {/* Fixed columns keep their width past the container and the wrapper
          scrolls horizontally at narrow widths; table-fixed alone would
          compress every column into the viewport. The actions column fits
          all three row actions (3 × 30px + gaps + padding) without
          overflow. */}
      <Table className="min-w-[68.5rem] table-fixed">
        <TableHeader>
          <TableRow className="text-muted-foreground text-[0.7rem] font-bold tracking-[0.08em] uppercase hover:bg-transparent">
            <TableHead className="w-10 px-5" aria-label="Online" />
            <TableHead>User</TableHead>
            <TableHead className="w-48">Protocol</TableHead>
            <TableHead className="w-28 pl-5">Traffic</TableHead>
            <TableHead className="w-52">Speed now</TableHead>
            <TableHead className="w-40">Online IPs</TableHead>
            <TableHead className="w-24 text-right">Last seen</TableHead>
            <TableHead className="w-32 pr-5" aria-label="Actions" />
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
                  <PresenceDot online={user.online} />
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
                  {user.apply_state === "failed" ? (
                    <Badge
                      className="ml-2 px-1.5 py-0.5 align-middle text-[0.65rem] tracking-[0.08em] uppercase"
                      variant="destructive"
                    >
                      apply failed
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
                <TableCell className="py-1.5 pl-5 font-mono text-xs">
                  {/* Uplink above downlink: the approved Traffic column. */}
                  <div className="flex flex-col">
                    <span>↑ {formatBytes(user.up_bytes_total)}</span>
                    <span>↓ {formatBytes(user.down_bytes_total)}</span>
                  </div>
                </TableCell>
                <TableCell className="py-1.5 font-mono text-xs">
                  {snapshot.stale ? (
                    <span className="text-muted-foreground">stale</span>
                  ) : user.speed_up_bps > 0 || user.speed_down_bps > 0 ? (
                    // Uplink above downlink, mirroring the Traffic column
                    // and left-aligned like every other column.
                    <div className="flex flex-col">
                      <span>↑ {formatSpeed(user.speed_up_bps)}</span>
                      <span>↓ {formatSpeed(user.speed_down_bps)}</span>
                    </div>
                  ) : (
                    <span className="text-muted-foreground">idle</span>
                  )}
                </TableCell>
                <TableCell className="truncate py-1.5 align-top font-mono text-xs">
                  {user.ips !== null && user.ips.length > 0 ? (
                    // One IP per line (SPEC §6) — a stacked list stays
                    // scannable when a user holds several connections.
                    user.ips.map((ip) => {
                      const country = user.ip_countries?.[ip];
                      return (
                        <div key={ip}>
                          {country ? (
                            <span className="mr-1.5" title={country}>
                              {flagEmoji(country)}
                            </span>
                          ) : null}
                          {ip}
                        </div>
                      );
                    })
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="py-1.5 text-right font-mono text-xs">
                  {user.online ? (
                    "now"
                  ) : user.last_seen !== null ? (
                    formatAgo(user.last_seen)
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="py-1.5 pr-5 text-right align-middle">
                  <span className="inline-flex gap-1.5">
                    <IconAction
                      label={`Open ${user.email} details`}
                      title={`Open ${user.email} details`}
                      onOpen={(opener) => onOpenDetails(user.email, opener)}
                    >
                      <InfoIcon />
                    </IconAction>
                    {/* Gone Users are history: inspectable, never edited or removed. */}
                    {user.gone ? null : (
                      <>
                        <IconAction
                          label={`Edit ${user.email}`}
                          title={`Edit ${user.email}`}
                          onOpen={(opener) => onOpenEdit(user.email, opener)}
                        >
                          <EditIcon />
                        </IconAction>
                        <IconAction
                          label={`Remove ${user.email}`}
                          title={`Remove ${user.email}`}
                          onOpen={(opener) => onOpenRemove(user.email, opener)}
                        >
                          <TrashIcon />
                        </IconAction>
                      </>
                    )}
                  </span>
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </section>
  );
}

// HeaderMeta is one mono muted readout of the identity groups (panel
// version, uptime), separated by interpuncts as approved.
function HeaderMeta({ children }: { children: ReactNode }) {
  return <span className="text-muted-foreground font-mono text-xs">{children}</span>;
}

function Interpunct() {
  return (
    <span aria-hidden="true" className="text-muted-foreground/60 text-[11px]">
      ·
    </span>
  );
}

// XrayIdentity is the xray identity group (SPEC §6): the status
// indicator immediately before the service name, then version, uptime, and
// the two xray-scoped viewer actions. Those actions stay enabled whatever
// xray's status is — each viewer reports its own collection result, and a
// stopped xray still has a journal and a configured file (SPEC §6).
function XrayIdentity({
  xray,
  onOpenLogs,
  onOpenConfig,
}: {
  xray: XrayStatus | null;
  onOpenLogs: (opener: HTMLButtonElement) => void;
  onOpenConfig: (opener: HTMLButtonElement) => void;
}) {
  const dotClass =
    xray === null
      ? "bg-muted"
      : xray.status === "running"
        ? "bg-primary shadow-primary/10"
        : xray.status === "stopped"
          ? "bg-destructive shadow-destructive/10"
          : "bg-warning shadow-warning/10";

  return (
    <span className="ml-4 flex flex-wrap items-center gap-x-2.5 gap-y-2">
      <span className="inline-flex items-center gap-1.5 text-lg font-semibold tracking-tight">
        <span
          role="img"
          aria-label={xray?.status ?? "unknown"}
          title={xray?.status ?? "unknown"}
          className={`size-[7px] rounded-full shadow-[0_0_0_3px] ${dotClass}`}
        />
        xray
      </span>
      {xray?.version ? <HeaderMeta>v{xray.version}</HeaderMeta> : null}
      {xray?.status === "running" ? (
        <>
          <Interpunct />
          <HeaderMeta>up {formatUptimeShort(xray.uptime_seconds)}</HeaderMeta>
        </>
      ) : null}
      <IconAction label="View xray logs" title="xray logs" onOpen={onOpenLogs}>
        <LogsIcon />
      </IconAction>
      <IconAction label="View xray config" title="xray config" onOpen={onOpenConfig}>
        <ConfigIcon />
      </IconAction>
    </span>
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
  // One slot for every dialog, so at most one modal is ever mounted (SPEC §6) —
  // and the action that opened it, for focus restore.
  const [dialog, setDialog] = useState<OpenDialog>(null);
  const dialogOpener = useRef<HTMLButtonElement | null>(null);

  function openDialog(next: NonNullable<OpenDialog>, opener: HTMLButtonElement) {
    dialogOpener.current = opener;
    setDialog(next);
  }

  // Closing unmounts the dialog, which is what discards its browser-local
  // snapshot: reopening always starts with an initial load (SPEC §6).
  const closeDialog = useCallback(() => setDialog(null), []);

  // An expired Session is the Dashboard's business, not a dialog's: whichever
  // dialog's Collection meets a 401 hands it back here, and the pairing —
  // close, then return to login (SPEC §6) — is written once.
  const sessionExpired = useCallback(() => {
    setDialog(null);
    onUnauthenticated();
  }, [onUnauthenticated]);

  useEffect(() => {
    const controller = new AbortController();

    async function poll() {
      try {
        // One cycle, three observations: host, xray, and users.
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
      // Panel identity rides the same five-second cycle (uptime is
      // re-fetched, never extrapolated), but stays cosmetic: on failure the
      // header keeps the last-known version and uptime instead of erroring
      // the whole dashboard.
      fetchPanelInfo(controller.signal)
        .then(setPanel)
        .catch(() => {});
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
          <>
            <HeaderMeta>{panel.version}</HeaderMeta>
            <Interpunct />
            <HeaderMeta>up {formatUptimeShort(panel.uptime_seconds)}</HeaderMeta>
          </>
        ) : null}
          <IconAction
          label="View Panel logs"
          title="Panel logs"
          onOpen={(opener) => openDialog({ kind: "logs", source: "panel" }, opener)}
        >
          <LogsIcon />
        </IconAction>
        {/* The xray group renders whether or not the observation landed: a
            failed xray poll must not take the Log and Config snapshot actions
            with it, because neither reads xray to answer (SPEC §6). */}
        <XrayIdentity
          xray={xray}
          onOpenLogs={(opener) => openDialog({ kind: "logs", source: "xray" }, opener)}
          onOpenConfig={(opener) => openDialog({ kind: "config" }, opener)}
        />
        <span className="flex-1" />
        <HeaderMeta>
          refreshing every 5s{updatedAt ? ` · updated ${formatTime24(updatedAt)}` : ""}
        </HeaderMeta>
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

      {users ? (
        <UsersTable
          snapshot={users}
          onOpenDetails={(email, opener) => openDialog({ kind: "details", email }, opener)}
          onOpenEdit={(email, opener) => openDialog({ kind: "edit", email }, opener)}
          onOpenRemove={(email, opener) => openDialog({ kind: "remove", email }, opener)}
          onOpenAdd={(opener) => openDialog({ kind: "add" }, opener)}
        />
      ) : null}

      {dialog?.kind === "add" && users ? (
        <AddUserModal
          inbounds={users.inbounds}
          opener={dialogOpener}
          onClose={closeDialog}
          onExpired={sessionExpired}
        />
      ) : null}

      {dialog?.kind === "edit" && users
        ? (() => {
            // The row the dialog edits, re-read from the live snapshot on
            // every poll so the dialog never shows a replaced record.
            const user = users.users.find((candidate) => candidate.email === dialog.email);
            return user ? (
              <EditUserModal
                user={user}
                inbounds={users.inbounds}
                opener={dialogOpener}
                onClose={closeDialog}
                onExpired={sessionExpired}
              />
            ) : null;
          })()
        : null}

      {dialog?.kind === "remove" && users
        ? (() => {
            // The row the dialog confirms, re-read from the live snapshot —
            // a removed user must not keep a confirm dialog open.
            const user = users.users.find((candidate) => candidate.email === dialog.email);
            return user && !user.gone ? (
              <RemoveUserModal
                user={user}
                opener={dialogOpener}
                onClose={closeDialog}
                onExpired={sessionExpired}
              />
            ) : null;
          })()
        : null}

      {dialog?.kind === "details" ? (
        <UserDetailsModal
          email={dialog.email}
          opener={dialogOpener}
          onClose={closeDialog}
          onExpired={sessionExpired}
        />
      ) : null}

      {dialog?.kind === "logs" ? (
        <LogSnapshotModal
          source={dialog.source}
          opener={dialogOpener}
          onClose={closeDialog}
          onExpired={sessionExpired}
        />
      ) : null}

      {dialog?.kind === "config" ? (
        <ConfigSnapshotModal
          opener={dialogOpener}
          onClose={closeDialog}
          onExpired={sessionExpired}
        />
      ) : null}
    </main>
  );
}
