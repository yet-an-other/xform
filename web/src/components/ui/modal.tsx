import { Dialog } from "@base-ui/react/dialog";
import type { ReactNode, RefObject } from "react";

import { cn } from "@/lib/utils";

interface ModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // opener receives focus back when the modal closes — typically the button
  // that opened it (IN-DEV-SPEC §7.5).
  opener: RefObject<HTMLElement | null>;
  // label names the dialog for assistive technology ("Open alice@… details").
  label: string;
  className?: string;
  children: ReactNode;
}

// Modal is the shared dialog controller behind every Dashboard dialog: one
// overlay, one focused dialog, Escape and close-action dismissal, and focus
// restored to the opener. Base UI's Dialog provides the trap; one modal at
// a time is the caller's job — render at most one Modal, driven by a single
// piece of open state.
export function Modal({ open, onOpenChange, opener, label, className, children }: ModalProps) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Backdrop data-testid="modal-backdrop" className="fixed inset-0 z-40 bg-[rgba(3,7,11,0.74)] backdrop-blur-[5px]" />
        <Dialog.Popup
          aria-label={label}
          finalFocus={opener}
          className={cn(
            // Centred by default; edge to edge at the approved narrow
            // breakpoint, which needs the centring offsets dropped from both
            // the position and the translate.
            "bg-surface border-border-strong shadow-modal fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100vh-48px)] w-[min(1040px,100%)] max-w-full -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-2xl border max-xs:top-0 max-xs:left-0 max-xs:max-h-screen max-xs:min-h-screen max-xs:w-screen max-xs:translate-x-0 max-xs:translate-y-0 max-xs:rounded-none max-xs:border-0",
            className,
          )}
        >
          {children}
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

// ModalClose is the dismiss action every dialog carries in its header. It is
// shared rather than repeated because its accessible name is the only part
// that differs between dialogs (ADR-0002: extract as the UI demands it).
export function ModalClose({ label, onClose }: { label: string; onClose: () => void }) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClose}
      className="border-border text-muted-foreground hover:text-foreground grid size-[34px] shrink-0 place-items-center rounded-lg border text-xl leading-none"
    >
      ×
    </button>
  );
}

// ModalFooter is the standing-notes strip: what the dialog does not do, said
// once per dialog and never varying in shape.
export function ModalFooter({ children }: { children: ReactNode }) {
  return (
    <footer className="text-muted-foreground flex flex-wrap gap-x-3.5 gap-y-2 border-t px-5 py-2.5 text-[10px]">
      {children}
    </footer>
  );
}
