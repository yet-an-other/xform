import { useCallback, type ReactNode, type RefObject } from "react";

import { ConnectionProfiles } from "@/components/connection-profile-card";
import { Badge } from "@/components/ui/badge";
import { Modal, ModalClose, ModalFooter } from "@/components/ui/modal";
import {
  flagEmoji,
  formatAgo,
  formatBytes,
  formatSpeed,
  formatTime24,
} from "@/lib/format";
import { fetchUserDetail } from "@/lib/api";
import { useCollection } from "@/lib/collection";
import { cn } from "@/lib/utils";

interface UserDetailsModalProps {
  email: string;
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
  onExpired: () => void;
}

const DETAIL_POLL_INTERVAL_MS = 5_000;

export function UserDetailsModal({
  email,
  opener,
  onClose,
  onExpired,
}: UserDetailsModalProps) {
  // A User's Observations keep moving while the dialog is open, so this
  // Collection carries a cadence. Its value always belongs to the email it was
  // collected for: a different one is a different Collection.
  const collect = useCallback((signal: AbortSignal) => fetchUserDetail(email, signal), [email]);
  const {
    data: detail,
    error,
    refreshFailed,
  } = useCollection(collect, { onExpired, intervalMs: DETAIL_POLL_INTERVAL_MS });
  const user = detail?.user ?? null;

  return (
    <Modal label={`${email} details`} open opener={opener} onOpenChange={onClose}>
      <header className="flex items-start gap-4 border-b px-5 py-4">
        <div className="min-w-0">
          <p className="text-muted-foreground text-[10px] font-bold tracking-[0.13em] uppercase">
            User details
          </p>
          <h2 className="truncate text-xl font-semibold tracking-tight">{email}</h2>
          {user ? (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              {user.disabled ? (
                <Badge variant="outline" className="border-warning/35 text-warning gap-2 rounded-full border px-2 py-1 text-[11px] font-bold">
                  Disabled User
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
        <span className="ml-auto">
          <ModalClose label="Close User details" onClose={onClose} />
        </span>
      </header>
      <div className="min-h-0 overflow-auto px-5 py-5">
        {error ? (
          <p role="alert" className="border-destructive/30 bg-destructive/10 text-destructive-foreground mb-4 rounded-lg border px-4 py-3 text-sm">
            Unable to refresh User details: {error}
          </p>
        ) : null}
        {user && detail ? (
          <>
            <div className="mb-2.5 flex items-baseline justify-between gap-3">
              <h3 className="text-[13px] font-semibold">Current observations</h3>
              <span className="text-muted-foreground text-[11px]">
                {refreshFailed
                  ? user.disabled
                    ? "historical observations · refresh failed"
                    : detail.stale
                      ? "stale snapshot · refresh failed"
                      : "refresh failed · showing previous observations"
                  : user.disabled
                    ? detail.stale
                      ? "historical observations · stale snapshot"
                      : "historical observations"
                    : detail.stale
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
                {detail.stale ? (
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
              profiles={detail.connection_profiles}
              refreshFailed={refreshFailed}
            />
          </>
        ) : error === null ? (
          <p role="status" className="text-muted-foreground text-sm">Loading User details…</p>
        ) : null}
      </div>
      <ModalFooter>
        <span>Read-only</span>
        {detail ? <span>Collected at {formatTime24(new Date(detail.collected_at * 1000))}</span> : null}
      </ModalFooter>
    </Modal>
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
