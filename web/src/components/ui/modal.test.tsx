import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useRef, useState } from "react";

import { Modal } from "./modal";

afterEach(cleanup);

// Harness opens the modal the way the dashboard does: an opener button the
// modal must remember focus for, plus a body the tests can interact with.
function Harness({ label = "Test dialog" }: { label?: string }) {
  const [open, setOpen] = useState(false);
  const opener = useRef<HTMLButtonElement>(null);
  return (
    <>
      <button ref={opener} onClick={() => setOpen(true)}>
        open
      </button>
      <Modal label={label} open={open} opener={opener} onOpenChange={setOpen}>
        <button onClick={() => setOpen(false)}>close action</button>
        <p>dialog body</p>
      </Modal>
    </>
  );
}

async function open() {
  fireEvent.click(screen.getByText("open"));
  const dialog = await screen.findByRole("dialog");
  // Base UI moves focus into the popup asynchronously after it mounts.
  await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
  return dialog;
}

describe("Modal", () => {
  it("renders nothing until opened, then presents one labelled dialog", async () => {
    render(<Harness />);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    await open();

    const dialog = screen.getByRole("dialog", { name: "Test dialog" });
    expect(screen.getByText("dialog body")).toBeInTheDocument();
    expect(dialog).toBeInTheDocument();
  });

  it("moves focus inside the dialog when opened", async () => {
    render(<Harness />);

    await open();
  });

  it("closes through its close action and restores focus to the opener", async () => {
    render(<Harness />);
    const opener = screen.getByText("open");
    await open();

    fireEvent.click(screen.getByText("close action"));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(opener).toHaveFocus();
  });

  it("closes on Escape and restores focus to the opener", async () => {
    render(<Harness />);
    const opener = screen.getByText("open");
    await open();

    fireEvent.keyDown(screen.getByText("dialog body"), { key: "Escape" });

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(opener).toHaveFocus();
  });

  it("keeps focus trapped inside the dialog while open", async () => {
    render(<Harness />);
    const dialog = await open();

    // Tab away from the dialog's focusable content; the trap pulls focus
    // back inside instead of letting it reach the page behind the modal.
    fireEvent.keyDown(screen.getByText("close action"), { key: "Tab" });

    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("reports outside-close attempts (backdrop dismissal) through onOpenChange", async () => {
    const onOpenChange = vi.fn();
    function Dismissible() {
      const opener = useRef<HTMLButtonElement>(null);
      return (
        <>
          <button ref={opener}>open</button>
          <Modal label="d" open onOpenChange={onOpenChange} opener={opener}>
            <p>body</p>
          </Modal>
        </>
      );
    }
    render(<Dismissible />);

    fireEvent.click(screen.getByTestId("modal-backdrop"));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false, expect.anything()));
  });
});
