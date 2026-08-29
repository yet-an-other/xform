import { useCallback, type RefObject } from "react";

import { CopyButton } from "@/components/copy-button";
import { Modal, ModalClose, ModalFooter } from "@/components/ui/modal";
import { fetchConfigSnapshot } from "@/lib/api";
import { useCollection } from "@/lib/collection";

interface ConfigSnapshotModalProps {
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
  onExpired: () => void;
}

// ConfigSnapshotModal shows the exact text of the configured xray file
// (IN-DEV-SPEC §7.4): one fresh bounded read on open, no Refresh — the file is
// what it is at the moment it was asked for, and a second read is a second
// opening. Nothing is parsed, formatted, or reflowed on the way to the screen.
export function ConfigSnapshotModal({ opener, onClose, onExpired }: ConfigSnapshotModalProps) {
  // No cadence and no Refresh: this Collection is collected once, and closing
  // aborts the read and discards it with the component.
  const collect = useCallback((signal: AbortSignal) => fetchConfigSnapshot(signal), []);
  const { data: snapshot, error } = useCollection(collect, { onExpired });

  return (
    <Modal label="xray config" open opener={opener} onOpenChange={onClose}>
      <header className="flex items-start gap-4 border-b px-5 py-4">
        <div className="min-w-0">
          <p className="text-muted-foreground text-[10px] font-bold tracking-[0.13em] uppercase">
            Configured file
          </p>
          <h2 className="truncate text-xl font-semibold tracking-tight">xray config</h2>
          <p className="text-muted-foreground mt-1 truncate font-mono text-xs">
            <span>{snapshot ? snapshot.path : "—"}</span> · exact file text
          </p>
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-3">
          {/* No Copy for a failed read: there is no text to copy, and an
              action that copies an error message is a trap. */}
          {snapshot ? (
            <CopyButton
              label="Copy xray config"
              value={snapshot.text}
              className="border-border hover:border-primary rounded-lg border px-3 py-1.5 text-[0.78rem]"
            />
          ) : null}
          <ModalClose label="Close xray config" onClose={onClose} />
        </div>
      </header>

      <div className="min-h-0 overflow-auto">
        {error !== null ? (
          <p role="alert" className="text-destructive-foreground px-5 py-6 text-sm">
            Config snapshot unavailable — {error}
          </p>
        ) : snapshot === null ? (
          <p role="status" className="text-muted-foreground px-5 py-6 text-sm">
            Reading the configured file…
          </p>
        ) : (
          // Long lines scroll horizontally rather than wrapping: this is the
          // file's own formatting, and rewrapping it would misrepresent it.
          <div className="overflow-x-auto px-5 py-4">
            <pre className="min-w-[620px] font-mono text-[11.5px] leading-relaxed whitespace-pre">
              {snapshot.text}
            </pre>
          </div>
        )}
      </div>

      <ModalFooter>
        <span>Read-only</span>
        <span>Exact configured text</span>
        <span>Credentials shown in full</span>
        <span>No parsing or reformatting</span>
      </ModalFooter>
    </Modal>
  );
}
