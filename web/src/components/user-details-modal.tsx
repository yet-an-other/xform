import { useEffect, useRef, useState, type ReactNode, type RefObject } from "react";

import { Badge } from "@/components/ui/badge";
import { Modal } from "@/components/ui/modal";
import {
  flagEmoji,
  formatAgo,
  formatBytes,
  formatSpeed,
  formatTime24,
} from "@/lib/format";
import {
  fetchUserDetail,
  UnauthenticatedError,
  type UserDetail,
} from "@/lib/api";
import { cn } from "@/lib/utils";

interface UserDetailsModalProps {
  email: string;
  open: boolean;
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
  onUnauthenticated: () => void;
}

const DETAIL_POLL_INTERVAL_MS = 5_000;

export function UserDetailsModal({
  email,
  open,
  opener,
  onClose,
  onUnauthenticated,
}: UserDetailsModalProps) {
  const [detail, setDetail] = useState<UserDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const opening = useRef(0);

  useEffect(() => {
    const openingID = ++opening.current;
    const controller = new AbortController();
    let active = true;
    let inFlight = false;
    let interval: number | null = null;

    function stop() {
      active = false;
      if (interval !== null) window.clearInterval(interval);
      controller.abort();
    }

    async function refresh() {
      if (inFlight) return;
      inFlight = true;
      try {
        const next = await fetchUserDetail(email, controller.signal);
        if (active && opening.current === openingID) {
          setDetail(next);
          setError(null);
        }
      } catch (cause) {
        if (cause instanceof UnauthenticatedError && active) {
          stop();
          onClose();
          onUnauthenticated();
          return;
        }
        if (active && !controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : "User detail unavailable");
        }
      } finally {
        inFlight = false;
      }
    }

    setDetail(null);
    setError(null);
    void refresh();
    interval = window.setInterval(() => void refresh(), DETAIL_POLL_INTERVAL_MS);
    return stop;
  }, [email, onClose, onUnauthenticated]);

  const visibleDetail = detail?.user.email === email ? detail : null;
  const user = visibleDetail?.user ?? null;
  const refreshFailed = error !== null && visibleDetail !== null;

  return (
    <Modal label={`${email} details`} open={open} opener={opener} onOpenChange={onClose}>
      <header className="flex items-start gap-4 border-b px-5 py-4">
        <div className="min-w-0">
          <p className="text-muted-foreground text-[10px] font-bold tracking-[0.13em] uppercase">
            User details
          </p>
          <h2 className="truncate text-xl font-semibold tracking-tight">{email}</h2>
          {user ? (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              {user.gone ? (
                <Badge variant="outline" className="border-warning/35 text-warning gap-2 rounded-full border px-2 py-1 text-[11px] font-bold">
                  Gone User
                </Badge>
              ) : (
                <Badge
                  variant="outline"
                  className={cn(
                    "gap-2 rounded-full border px-2 py-1 text-[11px] font-bold",
                    user.online
                      ? "border-primary/35 text-primary"
                      : "text-muted-foreground",
                  )}
                >
                  <span
                    aria-hidden="true"
                    className={cn(
                      "size-2 rounded-full",
                      user.online ? "bg-primary shadow-primary/55 shadow-[0_0_7px]" : "bg-muted",
                    )}
                  />
                  {user.online ? "Online" : "Offline"}
                </Badge>
              )}
              {user.protocol !== null ? (
                <Badge variant="outline" className="text-muted-foreground rounded-full border px-2 py-1 text-[11px] font-bold">
                  {user.protocol} · {user.security}
                </Badge>
              ) : null}
            </div>
          ) : null}
        </div>
        <button
          type="button"
          aria-label="Close User details"
          onClick={onClose}
          className="border-border text-muted-foreground hover:text-foreground ml-auto grid size-[34px] shrink-0 place-items-center rounded-lg border text-xl leading-none"
        >
          ×
        </button>
      </header>
      <div className="min-h-0 overflow-auto px-5 py-5">
        {error ? (
          <p role="alert" className="border-destructive/30 bg-destructive/10 text-destructive-foreground mb-4 rounded-lg border px-4 py-3 text-sm">
            Unable to refresh User details: {error}
          </p>
        ) : null}
        {user ? (
          <>
            <div className="mb-2.5 flex items-baseline justify-between gap-3">
              <h3 className="text-[13px] font-semibold">Current observations</h3>
              <span className="text-muted-foreground text-[11px]">
                {refreshFailed
                  ? user.gone
                    ? "historical observations · refresh failed"
                    : visibleDetail.stale
                      ? "stale snapshot · refresh failed"
                      : "refresh failed · showing previous observations"
                  : user.gone
                    ? visibleDetail?.stale
                      ? "historical observations · stale snapshot"
                      : "historical observations"
                    : visibleDetail?.stale
                      ? "stale snapshot"
                      : "live snapshot"}
              </span>
            </div>
            <dl className="grid grid-cols-2 gap-2.5 lg:grid-cols-3">
              <Observation label="Traffic">
                {/* Uplink above downlink, mirroring the table's Traffic column. */}
                <span className="grid gap-0.5">
                  <span className="text-primary whitespace-nowrap">
                    ↑ {formatBytes(user.up_bytes_total)}
                  </span>
                  <span className="text-info whitespace-nowrap">
                    ↓ {formatBytes(user.down_bytes_total)}
                  </span>
                </span>
              </Observation>
              <Observation label="Speed now">
                {visibleDetail?.stale ? (
                  <span className="text-muted-foreground">stale</span>
                ) : user.speed_up_bps > 0 || user.speed_down_bps > 0 ? (
                  <span className="grid gap-0.5">
                    <span className="text-primary whitespace-nowrap">
                      ↑ {formatSpeed(user.speed_up_bps)}
                    </span>
                    <span className="text-info whitespace-nowrap">
                      ↓ {formatSpeed(user.speed_down_bps)}
                    </span>
                  </span>
                ) : (
                  <span className="text-muted-foreground">idle</span>
                )}
              </Observation>
              <Observation label="Last seen">
                {user.online ? (
                  "now"
                ) : user.last_seen !== null ? (
                  formatAgo(user.last_seen)
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </Observation>
            </dl>
            <div className="border-border bg-card/60 mt-2.5 flex flex-wrap items-center gap-2 rounded-[10px] border px-3.5 py-3">
              <strong className="mr-1 text-xs">Online IPs</strong>
              {user.ips !== null && user.ips.length > 0 ? (
                user.ips.map((ip) => {
                  const country = user.ip_countries?.[ip];
                  return (
                    <span
                      key={ip}
                      className="border-border bg-secondary rounded-full border px-2 py-1 font-mono text-[11px]"
                    >
                      {country ? (
                        <span className="mr-1.5" title={country}>
                          {flagEmoji(country)}
                        </span>
                      ) : null}
                      {ip}
                    </span>
                  );
                })
              ) : (
                <span className="text-muted-foreground">None</span>
              )}
            </div>
            <ConnectionProfiles
              profiles={visibleDetail!.connection_profiles}
              refreshFailed={refreshFailed}
            />
          </>
        ) : error === null ? (
          <p role="status" className="text-muted-foreground text-sm">Loading User details…</p>
        ) : null}
      </div>
      <footer className="text-muted-foreground flex flex-wrap gap-x-3.5 gap-y-2 border-t px-5 py-2.5 text-[10px]">
        <span>Read-only</span>
        {visibleDetail ? <span>Collected at {formatTime24(new Date(visibleDetail.collected_at * 1000))}</span> : null}
      </footer>
    </Modal>
  );
}

function ConnectionProfiles({
  profiles,
  refreshFailed,
}: {
  profiles: UserDetail["connection_profiles"];
  refreshFailed: boolean;
}) {
  return (
    <section aria-labelledby="connection-profiles-heading" className="mt-6">
      <div className="mb-2.5 flex items-baseline justify-between gap-3">
        <h3 id="connection-profiles-heading" className="text-[13px] font-semibold">
          Connection profiles
        </h3>
        <span
          className={cn(
            "text-[11px]",
            profiles.stale ? "text-warning" : "text-muted-foreground",
          )}
        >
          {refreshFailed
            ? profiles.stale
              ? "stale profile sources · refresh failed"
              : "refresh failed · showing previous profiles"
            : profiles.stale
              ? "stale profile sources"
              : profiles.loaded_at === null
                ? "profile sources unavailable"
                : "current profile sources"}
        </span>
      </div>

      {profiles.errors.length > 0 ? (
        <div role="alert" className="border-warning/35 bg-warning/10 text-warning mb-3 rounded-[10px] border px-3.5 py-3 text-xs">
          <strong>Profile source warning</strong>
          <ul className="mt-1.5 grid gap-1">
            {profiles.errors.map((sourceError) => (
              <li key={`${sourceError.source}:${sourceError.reason}`}>
                <code>{sourceError.source}</code> · <code>{sourceError.reason}</code>: {sourceError.message}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {profiles.state === "ready" ? (
        profiles.items.length > 0 ? (
          <div className="grid gap-3">
            {profiles.items.map((profile, index) =>
              profile.status === "available" ? (
                <article
                  key={`${profile.inbound_tag}:${index}`}
                  aria-label={`${profile.name} Connection profile`}
                  className="border-border-strong bg-card/70 overflow-hidden rounded-xl border"
                >
                  <header className="border-border flex flex-wrap items-center gap-2 border-b px-4 py-3">
                    <h4 className="text-[13px] font-semibold">{profile.name}</h4>
                    <code className="text-muted-foreground text-[10px]">{profile.inbound_tag}</code>
                  </header>
                  <div className="px-4 py-3">
                    <p className="text-muted-foreground text-[10px] font-bold tracking-[0.08em] uppercase">
                      Connection URI
                    </p>
                    <code className="mt-1.5 block [overflow-wrap:anywhere] text-[11px] leading-relaxed">
                      {profile.uri}
                    </code>
                  </div>
                </article>
              ) : (
                <article
                  key={`${profile.inbound_tag ?? "unknown"}:${index}`}
                  aria-label={`${profile.name ?? "Unknown"} unavailable Connection profile`}
                  className="border-destructive/30 bg-destructive/10 rounded-xl border px-4 py-3"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <h4 className="text-[13px] font-semibold">{profile.name ?? "Unknown profile"}</h4>
                    {profile.inbound_tag !== null ? (
                      <code className="text-muted-foreground text-[10px]">{profile.inbound_tag}</code>
                    ) : null}
                  </div>
                  <p className="text-destructive-foreground mt-2 text-xs">
                    <code>{profile.reason}</code>: {profile.message}
                  </p>
                </article>
              ),
            )}
          </div>
        ) : (
          <ProfileState title="No profile results">
            No matching profile result was returned for this User.
          </ProfileState>
        )
      ) : profiles.state === "gone_user" ? (
        <ProfileState title="No connection profiles">
          Gone Users keep Traffic and presence history, but xform no longer has current credentials to expose.
        </ProfileState>
      ) : profiles.state === "no_matching_inbound" ? (
        <ProfileState title="No matching inbound">
          This User is not present in a current matching VLESS inbound.
        </ProfileState>
      ) : (
        <ProfileState title="Profile source unavailable" error>
          The xray config has never parsed successfully, so matching inbounds cannot be identified.
        </ProfileState>
      )}
    </section>
  );
}

function ProfileState({
  title,
  error = false,
  children,
}: {
  title: string;
  error?: boolean;
  children: ReactNode;
}) {
  return (
    <div className={cn("rounded-[10px] border border-dashed px-4 py-4 text-center text-sm", error ? "border-destructive/40 text-destructive-foreground" : "border-border-strong text-muted-foreground")}>
      <strong className="text-foreground block">{title}</strong>
      <span>{children}</span>
    </div>
  );
}

function Observation({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="border-border bg-card/60 min-w-0 rounded-[10px] border px-3.5 py-3">
      <dt className="text-muted-foreground text-[10px] font-bold tracking-[0.08em] uppercase">
        {label}
      </dt>
      <dd className="mt-1.5 font-mono text-[13px] font-semibold">{children}</dd>
    </div>
  );
}
