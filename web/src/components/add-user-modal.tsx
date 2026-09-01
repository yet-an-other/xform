import { useState, type RefObject } from "react";

import { ApplyFailedBanner } from "@/components/apply-failed-banner";
import { Modal, ModalClose } from "@/components/ui/modal";
import {
  addUser,
  ConflictError,
  UnauthenticatedError,
  type InboundOption,
} from "@/lib/api";

interface AddUserModalProps {
  inbounds: InboundOption[];
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
  onExpired: () => void;
}

// conflictText renders the API's machine-readable rejection reason for the
// dialog (user-management spec §5).
function conflictText(reason: string): string {
  switch (reason) {
    case "email_taken":
      return "That email is already in the roster.";
    case "email_invalid":
      return "Enter an email address.";
    case "client_id_taken":
      return "That Client ID is already used by another user.";
    case "client_id_invalid":
      return "Client ID must be a valid UUID.";
    case "unknown_inbound":
      return "An attached inbound no longer exists — close and reopen to refresh.";
    default:
      return "The panel rejected the change.";
  }
}

// AddUserModal is the + Add user dialog (user-management spec §6): email,
// the inbound multi-select, and a pre-generated editable Client ID with
// ⟳ Generate. Saving stores the user; the apply runs from there and its
// first outcome lands in the answer — synced (or still pending) closes the
// dialog, failed keeps it open on the banner, the change stored and
// retrying.
export function AddUserModal({ inbounds, opener, onClose, onExpired }: AddUserModalProps) {
  const [email, setEmail] = useState("");
  const [selected, setSelected] = useState<string[]>(() => inbounds.map((option) => option.tag));
  const [clientId, setClientId] = useState<string>(() => crypto.randomUUID());
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // failed is set once the stored change's first apply failed: the dialog
  // stays on the banner, the form locks — a second submit would only
  // conflict with the stored record.
  const [failed, setFailed] = useState(false);

  function toggle(tag: string) {
    setSelected((current) =>
      current.includes(tag) ? current.filter((candidate) => candidate !== tag) : [...current, tag],
    );
  }

  async function submit() {
    setSubmitting(true);
    setError(null);
    try {
      const result = await addUser(email.trim(), clientId.trim(), selected);
      if (result.roster_sync === "failed") {
        setFailed(true);
        return;
      }
      onClose();
    } catch (cause) {
      if (cause instanceof UnauthenticatedError) {
        onExpired();
        return;
      }
      setError(cause instanceof ConflictError ? conflictText(cause.reason) : "Could not reach the panel.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal label="Add user" open opener={opener} onOpenChange={onClose} className="w-[min(460px,100%)]">
      <header className="flex items-start gap-4 border-b px-5 py-4">
        <div className="min-w-0">
          <h2 className="text-lg font-semibold tracking-tight">Add user</h2>
          <p className="text-muted-foreground mt-1 text-xs">
            Stored, applied immediately — no restart.
          </p>
        </div>
        <span className="ml-auto">
          <ModalClose label="Close add user" onClose={onClose} />
        </span>
      </header>
      <div className="min-h-0 overflow-auto px-5 py-5">
        {failed ? <ApplyFailedBanner /> : null}
        <fieldset disabled={failed || submitting} className="contents">
          <div className="mb-4">
            <label
              htmlFor="add-user-email"
              className="text-muted-foreground mb-2 block text-[10px] font-bold tracking-[0.1em] uppercase"
            >
              Email
            </label>
            <input
              id="add-user-email"
              type="text"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              placeholder="user@example.com"
              spellCheck={false}
              className="border-border bg-background w-full rounded-lg border px-2.5 py-2 font-mono text-[0.8rem] disabled:opacity-60"
            />
          </div>
          <fieldset className="mb-4">
            <legend className="text-muted-foreground mb-2 px-0 text-[10px] font-bold tracking-[0.1em] uppercase">
              Inbounds
            </legend>
            {inbounds.length === 0 ? (
              <p className="text-muted-foreground text-xs">
                No VLESS inbounds in the xray config — the user is stored profile-less.
              </p>
            ) : (
              inbounds.map((option) => (
                <label
                  key={option.tag}
                  className={`mb-1.5 flex items-baseline gap-2.5 rounded-lg border px-3 py-2 last:mb-0 ${
                    selected.includes(option.tag)
                      ? "border-primary/40 bg-accent"
                      : "border-border"
                  }`}
                >
                  <input
                    type="checkbox"
                    checked={selected.includes(option.tag)}
                    onChange={() => toggle(option.tag)}
                    className="accent-primary"
                  />
                  <span className="min-w-0">
                    <span className="block truncate font-mono text-xs font-semibold">{option.tag}</span>
                    <span className="text-muted-foreground block text-[11px]">{option.label}</span>
                  </span>
                </label>
              ))
            )}
          </fieldset>
          <div>
            <label
              htmlFor="add-user-client-id"
              className="text-muted-foreground mb-2 block text-[10px] font-bold tracking-[0.1em] uppercase"
            >
              Client ID
            </label>
            <div className="flex items-center gap-2">
              <input
                id="add-user-client-id"
                type="text"
                value={clientId}
                onChange={(event) => setClientId(event.target.value)}
                spellCheck={false}
                className="border-border bg-background min-w-0 flex-1 rounded-lg border px-2.5 py-2 font-mono text-xs disabled:opacity-60"
              />
              <button
                type="button"
                onClick={() => setClientId(crypto.randomUUID() as string)}
                title="Generate a fresh random Client ID"
                className="border-border text-muted-foreground hover:text-foreground shrink-0 rounded-lg border px-2.5 py-2 text-[0.7rem] font-bold tracking-[0.08em] uppercase disabled:opacity-50"
              >
                ⟳ Generate
              </button>
            </div>
          </div>
        </fieldset>
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
              onClick={() => void submit()}
              disabled={submitting || email.trim() === ""}
              className="bg-primary text-primary-foreground rounded-lg px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase disabled:opacity-50"
            >
              Add user
            </button>
          </>
        )}
      </footer>
    </Modal>
  );
}
