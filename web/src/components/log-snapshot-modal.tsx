import { useCallback, type RefObject } from "react";

import { Modal, ModalClose, ModalFooter } from "@/components/ui/modal";
import { fetchLogSnapshot, type LogSource } from "@/lib/api";
import { useCollection } from "@/lib/collection";
import { formatEntryTime, formatSnapshotTime, logMessage, logSource, priorityLabel } from "@/lib/log-entry";
import { cn } from "@/lib/utils";

interface LogSnapshotModalProps {
  source: LogSource;
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
  onExpired: () => void;
}

const TITLES: Record<LogSource, string> = { panel: "Panel logs", xray: "xray logs" };

// priorityTone colours the badge by severity band, so an error is findable
// while scrolling 500 dense rows.
function priorityTone(priority: number | null): string {
  if (priority === null) return "text-muted-foreground border-border";
  if (priority <= 3) return "text-destructive border-destructive/40";
  if (priority === 4) return "text-warning border-warning/40";
  if (priority === 5) return "text-info border-info/40";
  return "text-muted-foreground border-border";
}

// LogSnapshotModal is one Log snapshot dialog (SPEC §6): one fresh
// bounded snapshot on open, a manual Refresh, and nothing retained once it
// closes — the Panel keeps no Log snapshot, and neither does the browser.
export function LogSnapshotModal({
  source,
  opener,
  onClose,
  onExpired,
}: LogSnapshotModalProps) {
  // No cadence: a Log snapshot is collected when an admin asks, and Refresh is
  // the only thing that asks again (ADR-0006). A failure after a successful
  // load keeps the entries and the capture time that produced them, which is
  // what refreshFailed names; an initial failure has nothing to keep.
  const collect = useCallback((signal: AbortSignal) => fetchLogSnapshot(source, signal), [source]);
  const {
    data: snapshot,
    error,
    loading,
    refreshFailed,
    refresh,
  } = useCollection(collect, { onExpired });

  return (
    <Modal label={TITLES[source]} open opener={opener} onOpenChange={onClose}>
      <header className="flex items-start gap-4 border-b px-5 py-4">
        <div className="min-w-0">
          <p className="text-muted-foreground text-[10px] font-bold tracking-[0.13em] uppercase">
            Bounded journal snapshot
          </p>
          <h2 className="truncate text-xl font-semibold tracking-tight">{TITLES[source]}</h2>
          <p className="text-muted-foreground mt-1 font-mono text-xs">
            journal unit · {snapshot?.unit ?? "—"} · newest first
          </p>
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-2">
          <button
            type="button"
            disabled={loading}
            onClick={refresh}
            className="border-border text-muted-foreground hover:text-foreground rounded-lg border px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase disabled:opacity-50"
          >
            Refresh
          </button>
          <ModalClose label={`Close ${TITLES[source]}`} onClose={onClose} />
        </div>
      </header>

      <div className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 border-b px-5 py-2 text-[11px]">
        {snapshot ? (
          <>
            <span>
              <strong className="text-foreground">
                {snapshot.entry_count} {snapshot.entry_count === 1 ? "entry" : "entries"}
              </strong>{" "}
              · latest available
            </span>
            <span>Snapshot at {formatSnapshotTime(snapshot.captured_at)}</span>
          </>
        ) : null}
        <span className="ml-auto">Bounded · manual refresh</span>
      </div>

      {refreshFailed && snapshot ? (
        <p
          role="alert"
          className="border-destructive/30 bg-destructive/10 text-destructive-foreground border-b px-5 py-2.5 text-xs"
        >
          Refresh failed, showing snapshot from {formatSnapshotTime(snapshot.captured_at)} —{" "}
          {error}
        </p>
      ) : null}

      <div className="min-h-0 overflow-auto">
        {snapshot === null && error !== null ? (
          <p role="alert" className="text-destructive-foreground px-5 py-6 text-sm">
            Log snapshot unavailable — {error}
          </p>
        ) : snapshot === null ? (
          <p role="status" className="text-muted-foreground px-5 py-6 text-sm">
            Collecting {TITLES[source]}…
          </p>
        ) : snapshot.entries.length === 0 ? (
          <p className="text-muted-foreground px-5 py-6 text-sm">
            No records in this snapshot.
          </p>
        ) : (
          // Fixed-format content scrolls horizontally at the narrow
          // breakpoint rather than reflowing (SPEC §6).
          <div className="overflow-x-auto">
            <table className="w-full min-w-[700px] border-collapse text-left font-mono text-[11.5px]">
              <thead>
                <tr className="text-muted-foreground text-[10px] font-bold tracking-[0.08em] uppercase">
                  <th className="w-[150px] px-5 py-1.5 font-bold">Time (UTC)</th>
                  <th className="w-[150px] py-1.5 font-bold">Source</th>
                  <th className="w-[86px] py-1.5 font-bold">Priority</th>
                  <th className="py-1.5 pr-5 font-bold">Message</th>
                </tr>
              </thead>
              <tbody>
                {snapshot.entries.map((item) => {
                  const message = logMessage(item);
                  const label = priorityLabel(item.priority);
                  return (
                    <tr key={item.cursor} className="border-border/50 border-t align-top">
                      <td className="text-muted-foreground px-5 py-1 whitespace-nowrap">
                        {formatEntryTime(item.timestamp_us)}
                      </td>
                      <td className="py-1 whitespace-nowrap">{logSource(item)}</td>
                      <td className="py-1">
                        {label !== null ? (
                          <span
                            className={cn(
                              "rounded-full border px-1.5 py-0.5 text-[10px] font-bold",
                              priorityTone(item.priority),
                            )}
                          >
                            {label}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </td>
                      <td className="py-1 pr-5 whitespace-pre-wrap">
                        {message.binary ? (
                          <span className="border-warning/40 text-warning mr-2 rounded-full border px-1.5 py-0.5 text-[10px] font-bold">
                            binary
                          </span>
                        ) : null}
                        <span className={message.truncated ? "text-muted-foreground italic" : undefined}>
                          {message.text}
                        </span>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <ModalFooter>
        <span>Read-only</span>
        <span>No Panel redaction</span>
        <span>No live tail</span>
      </ModalFooter>
    </Modal>
  );
}
