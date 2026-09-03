import { useState, type RefObject } from "react";

import { ApplyFailedBanner } from "@/components/apply-failed-banner";
import { Modal, ModalClose } from "@/components/ui/modal";
import {
  ConflictError,
  UnauthenticatedError,
  UserNotFoundError,
  editUser,
  type InboundOption,
  type User,
} from "@/lib/api";
import { conflictText } from "@/lib/conflict-text";

interface EditUserModalProps {
  user: User;
  inbounds: InboundOption[];
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
  onExpired: () => void;
}

// EditUserModal is the edit dialog in its live form (user-management spec
// §6, issue #54): the inbound multi-select and an editable Client ID with
// ⟳ Generate. Email is immutable — the identity never changes, only who
// exists. Saving stores the edit; the apply runs from there and its first
// outcome lands in the answer — synced (or still pending) closes the dialog,
// failed keeps it open on the banner, the change stored and retrying.
export function EditUserModal({ user, inbounds, opener, onClose, onExpired }: EditUserModalProps) {
  const stored = user.inbounds ?? [];
  const known = inbounds.map((option) => option.tag);
  // Attachments the config no longer carries can't be kept: the hint says
  // so, and saving drops them (store-wins convergence re-applies what
  // returns).
  const missing = stored.filter((tag) => !known.includes(tag));
  const [selected, setSelected] = useState<string[]>(() => stored.filter((tag) => known.includes(tag)));
  const [clientId, setClientId] = useState<string>(user.client_id ?? "");
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
      const result = await editUser(user.email, user.client_id !== null ? clientId.trim() : null, selected);
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
      if (cause instanceof UserNotFoundError) {
        setError("This user is no longer in the roster.");
        return;
      }
      setError(cause instanceof ConflictError ? conflictText(cause.reason) : "Could not reach the panel.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      label={`Edit ${user.email}`}
      open
      opener={opener}
      onOpenChange={onClose}
      className="w-[min(460px,100%)]"
    >
      <header className="border-b">
        <div className="flex items-start gap-4 px-5 py-3.5">
          <div className="min-w-0">
            <h2 className="text-lg font-semibold tracking-tight">Edit user access</h2>
            <p className="text-muted-foreground mt-0.5 text-xs">Inbounds and Client ID</p>
          </div>
          <span className="ml-auto">
            <ModalClose label={`Close ${user.email} edit`} onClose={onClose} />
          </span>
        </div>
        <div className="border-border bg-background/40 flex min-w-0 flex-wrap items-baseline gap-2 border-t px-5 py-2.5">
          <span className="text-muted-foreground text-[9px] font-bold tracking-[0.1em] uppercase">
            User
          </span>
          <strong className="min-w-0 flex-1 truncate font-mono text-xs font-semibold">
            {user.email}
          </strong>
          <span className="text-muted-foreground shrink-0 text-[10px] max-xs:w-full">
            Email cannot be changed
          </span>
        </div>
      </header>
      <div className="min-h-0 overflow-auto px-5 py-5">
        {/* The write-side failure surface (user-management spec §6): the
            change stays stored and retries — the banner says so. */}
        {user.apply_state === "failed" || failed ? <ApplyFailedBanner /> : null}
        <fieldset disabled={failed || submitting} className="contents">
          <fieldset className="mb-4">
            <legend className="text-muted-foreground mb-2 px-0 text-[10px] font-bold tracking-[0.1em] uppercase">
              Inbounds
            </legend>
            {inbounds.length === 0 && missing.length === 0 ? (
              <p className="text-muted-foreground text-xs">
                No VLESS inbounds in the xray config — the user stays profile-less.
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
            {missing.map((tag) => (
              <p key={tag} className="text-muted-foreground mt-1.5 text-[11px]">
                {tag} — no longer in the xray config; saving detaches it.
              </p>
            ))}
          </fieldset>
          <div>
            <label
              htmlFor="edit-user-client-id"
              className="text-muted-foreground mb-2 block text-[10px] font-bold tracking-[0.1em] uppercase"
            >
              Client ID
            </label>
            <div className="flex items-center gap-2">
              <input
                id="edit-user-client-id"
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
              disabled={submitting}
              className="bg-primary text-primary-foreground rounded-lg px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase disabled:opacity-50"
            >
              Save
            </button>
          </>
        )}
      </footer>
    </Modal>
  );
}
