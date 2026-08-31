import type { RefObject } from "react";

import { Modal, ModalClose } from "@/components/ui/modal";
import type { User } from "@/lib/api";

interface EditUserModalProps {
  user: User;
  opener: RefObject<HTMLElement | null>;
  onClose: () => void;
}

// EditUserModal is the edit dialog in its read-only form (#52): the user's
// real inbound selection and Client ID straight from the roster store. Save
// stays disabled and nothing mutates until the apply path lands — the
// dialog exists so the roster data is visible where it will be edited.
export function EditUserModal({ user, opener, onClose }: EditUserModalProps) {
  return (
    <Modal
      label={`Edit ${user.email}`}
      open
      opener={opener}
      onOpenChange={onClose}
      className="w-[min(460px,100%)]"
    >
      <header className="flex items-start gap-4 border-b px-5 py-4">
        <div className="min-w-0">
          <h2 className="truncate text-lg font-semibold tracking-tight">Edit {user.email}</h2>
          <p className="text-muted-foreground mt-1 text-xs">
            Email is the identity — changing it means remove + add.
          </p>
        </div>
        <span className="ml-auto">
          <ModalClose label={`Close ${user.email} edit`} onClose={onClose} />
        </span>
      </header>
      <div className="min-h-0 overflow-auto px-5 py-5">
        <fieldset className="mb-4">
          <legend className="text-muted-foreground mb-2 px-0 text-[10px] font-bold tracking-[0.1em] uppercase">
            Inbounds
          </legend>
          {user.inbounds !== null && user.inbounds.length > 0 ? (
            user.inbounds.map((tag) => (
              <label
                key={tag}
                className="border-primary/40 bg-accent mb-1.5 flex items-center gap-2.5 rounded-lg border px-3 py-2 last:mb-0"
              >
                {/* Checked and disabled: the real selection, not editable yet. */}
                <input type="checkbox" checked readOnly disabled className="accent-primary" />
                <span className="font-mono text-xs font-semibold">{tag}</span>
              </label>
            ))
          ) : (
            <p className="text-muted-foreground text-xs">—</p>
          )}
        </fieldset>
        <div>
          <span className="text-muted-foreground mb-2 block text-[10px] font-bold tracking-[0.1em] uppercase">
            Client ID
          </span>
          <div className="flex items-center gap-2">
            {user.client_id !== null ? (
              <input
                type="text"
                readOnly
                value={user.client_id}
                spellCheck={false}
                aria-label="Client ID"
                className="border-border bg-background min-w-0 flex-1 rounded-lg border px-2.5 py-2 font-mono text-xs"
              />
            ) : (
              <span className="text-muted-foreground text-xs">—</span>
            )}
            <button
              type="button"
              disabled
              title="Generate a fresh random Client ID"
              className="border-border text-muted-foreground shrink-0 rounded-lg border px-2.5 py-2 text-[0.7rem] font-bold tracking-[0.08em] uppercase disabled:opacity-50"
            >
              ⟳ Generate
            </button>
          </div>
        </div>
      </div>
      <footer className="flex justify-end gap-2 border-t px-5 py-3.5">
        <button
          type="button"
          onClick={onClose}
          className="border-border text-muted-foreground hover:text-foreground rounded-lg border px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase"
        >
          Cancel
        </button>
        <button
          type="button"
          disabled
          title="Applying edits is not available yet"
          className="bg-primary text-primary-foreground rounded-lg px-3 py-1.5 text-[0.78rem] font-bold tracking-[0.08em] uppercase disabled:opacity-50"
        >
          Save
        </button>
      </footer>
    </Modal>
  );
}
