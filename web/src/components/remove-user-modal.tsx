import { useState, type RefObject } from "react";

import { ApplyFailedBanner } from "@/components/apply-failed-banner";
import { Modal, ModalClose } from "@/components/ui/modal";
import { UnauthenticatedError, removeUser, type User } from "@/lib/api";

interface RemoveUserModalProps {
  user: User;
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
  onExpired: () => void;
}

// RemoveUserModal is the remove confirmation (user-management spec §6,
// issue #55): one paragraph saying exactly what removal means — off every
// inbound at once, history kept behind the gone badge, established
// connections left to close naturally — and one destructive confirm.
// The removal is stored the moment it is confirmed; the apply runs from
// there. A failed apply keeps the dialog open on the banner; the row
// already carries the gone state and retries on its own.
export function RemoveUserModal({ user, opener, onClose, onExpired }: RemoveUserModalProps) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // failed is set once the stored removal's first apply failed: the dialog
  // stays on the banner, the confirm button gone.
  const [failed, setFailed] = useState(false);

  async function confirm() {
    setSubmitting(true);
    setError(null);
    try {
      const sync = await removeUser(user.email);
      if (sync === "failed") {
        setFailed(true);
        return;
      }
      onClose();
    } catch (cause) {
      if (cause instanceof UnauthenticatedError) {
        onExpired();
        return;
      }
      setError("Could not reach the panel.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      label={`Remove ${user.email}`}
      open
      opener={opener}
      onOpenChange={onClose}
      className="w-[min(440px,100%)]"
    >
      <header className="flex items-start gap-4 border-b px-5 py-4">
        <div className="min-w-0">
          <h2 className="truncate text-lg font-semibold tracking-tight">Remove {user.email}</h2>
          <p className="text-muted-foreground mt-1 text-xs">
            Removed from every inbound immediately — no restart.
          </p>
        </div>
        <span className="ml-auto">
          <ModalClose label={`Close ${user.email} remove`} onClose={onClose} />
        </span>
      </header>
      <div className="min-h-0 overflow-auto px-5 py-5">
        {user.apply_state === "failed" || failed ? <ApplyFailedBanner /> : null}
        <p className="text-sm leading-relaxed">
          {user.email} is removed from the Roster: off every inbound at once, and an xray
          restart keeps them gone.
        </p>
        <ul className="text-muted-foreground mt-3 space-y-1.5 text-xs leading-relaxed">
          <li>Their traffic history stays — they appear with the gone badge behind the show-gone toggle.</li>
          <li>Established connections are not force-killed; they close naturally.</li>
          <li>Re-adding the same email later rejoins this history.</li>
        </ul>
        {error ? <p className="text-destructive mt-3 text-xs">{error}</p> : null}
      </div>
      <footer className="flex justify-end gap-2 border-t px-5 py-3.5">
        {failed ? (
          <button
            type="button"
            onClick={onClose}
            className="bg-primary text-primary-foreground rounded-lg px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase"
          >
            Close
          </button>
        ) : (
          <>
            <button
              type="button"
              onClick={onClose}
              className="border-border text-muted-foreground hover:text-foreground rounded-lg border px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => void confirm()}
              disabled={submitting}
              className="bg-destructive text-destructive-foreground rounded-lg px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase disabled:opacity-50"
            >
              Remove user
            </button>
          </>
        )}
      </footer>
    </Modal>
  );
}
