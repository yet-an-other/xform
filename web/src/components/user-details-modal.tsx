import type { ReactNode, RefObject } from "react";

import { Badge } from "@/components/ui/badge";
import { Modal } from "@/components/ui/modal";
import {
  flagEmoji,
  formatAgo,
  formatBytes,
  formatSpeed,
  formatTime24,
} from "@/lib/format";
import type { User, UsersSnapshot } from "@/lib/api";
import { cn } from "@/lib/utils";

interface UserDetailsModalProps {
  // email is the exact selected User identity — the modal shows this User
  // only, looked up in the live Users snapshot the dashboard already polls.
  email: string;
  snapshot: UsersSnapshot | null;
  open: boolean;
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
}

// UserDetailsModal is the first User details dialog (issue #31): real
// current observations — Traffic, Speed, Last seen, online IPs — from the
// existing Users response. It refreshes with the dashboard's ordinary
// polling while open; the detail endpoint, profiles, and copy/QR actions
// land with the next slice and are deliberately absent here.
export function UserDetailsModal({ email, snapshot, open, opener, onClose }: UserDetailsModalProps) {
  // Exact identity match: one email, one User.
  const user = snapshot?.users.find((candidate) => candidate.email === email) ?? null;

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
        {user ? (
          <>
            <div className="mb-2.5 flex items-baseline justify-between gap-3">
              <h3 className="text-[13px] font-semibold">Current observations</h3>
              <span className="text-muted-foreground text-[11px]">
                {user.gone
                  ? "historical observations"
                  : snapshot?.stale
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
                {snapshot?.stale ? (
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
          </>
        ) : (
          <p className="text-muted-foreground text-sm">
            This User is not in the current observations.
          </p>
        )}
      </div>
      <footer className="text-muted-foreground flex flex-wrap gap-x-3.5 gap-y-2 border-t px-5 py-2.5 text-[10px]">
        <span>Read-only</span>
        {snapshot ? <span>Collected at {formatTime24(new Date(snapshot.collected_at * 1000))}</span> : null}
      </footer>
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
