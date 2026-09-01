import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { json } from "@/test/fixtures";
import { AddUserModal } from "./add-user-modal";

const options = [
  { tag: "vless-vision", label: "VLESS · Reality · tcp :443" },
  { tag: "vless-xhttp", label: "VLESS · Reality · xhttp :8443" },
  { tag: "vless-ws", label: "VLESS · TLS · ws :2053" },
];

function renderModal() {
  const onClose = vi.fn();
  const onExpired = vi.fn();
  render(
    <AddUserModal
      inbounds={options}
      opener={createRef<HTMLElement>()}
      onClose={onClose}
      onExpired={onExpired}
    />,
  );
  return { onClose, onExpired };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("add user dialog", () => {
  it("opens with every inbound selected and a pre-generated editable Client ID", () => {
    vi.stubGlobal("fetch", vi.fn());
    renderModal();

    const dialog = screen.getByRole("dialog", { name: "Add user" });
    expect(within(dialog).getByLabelText("Email")).toHaveValue("");
    for (const { tag } of options) {
      expect(within(dialog).getByRole("checkbox", { name: new RegExp(tag) })).toBeChecked();
    }
    const clientId = within(dialog).getByLabelText("Client ID") as HTMLInputElement;
    expect(clientId.value).toMatch(/^[0-9a-f-]{36}$/);
    expect(within(dialog).getByRole("button", { name: /generate/i })).toBeEnabled();
  });

  it("regenerates the Client ID on demand", () => {
    vi.stubGlobal("fetch", vi.fn());
    renderModal();

    const dialog = screen.getByRole("dialog", { name: "Add user" });
    const before = (within(dialog).getByLabelText("Client ID") as HTMLInputElement).value;
    fireEvent.click(within(dialog).getByRole("button", { name: /generate/i }));
    const after = (within(dialog).getByLabelText("Client ID") as HTMLInputElement).value;
    expect(after).not.toBe(before);
    expect(after).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("stores the user with the chosen inbounds and closes on a settled apply", async () => {
    const fetchMock = vi.fn(async () =>
      json(
        {
          user: {
            email: "iris@example.com",
            client_id: "1d37a118-4f1b-4dc0-9e3c-3426b07518df",
            inbounds: ["vless-vision"],
            created_at: 1_780_000_000,
            updated_at: 1_780_000_000,
          },
          roster_sync: "synced",
        },
        201,
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Add user" });
    fireEvent.change(within(dialog).getByLabelText("Email"), { target: { value: "iris@example.com" } });
    fireEvent.change(within(dialog).getByLabelText("Client ID"), {
      target: { value: "1d37a118-4f1b-4dc0-9e3c-3426b07518df" },
    });
    // Detach ws and xhttp: only vision stays.
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /vless-xhttp/ }));
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /vless-ws/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Add user" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      email: "iris@example.com",
      client_id: "1d37a118-4f1b-4dc0-9e3c-3426b07518df",
      inbounds: ["vless-vision"],
    });
  });

  it("shows the machine-readable conflict reason in the dialog", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ error: "email_taken" }, 409)),
    );
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Add user" });
    fireEvent.change(within(dialog).getByLabelText("Email"), { target: { value: "iris@example.com" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add user" }));

    expect(await within(dialog).findByText(/already in the roster/i)).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    // The form stays editable for another try.
    expect(within(dialog).getByRole("button", { name: "Add user" })).toBeEnabled();
  });

  it("shows the failure banner when the store succeeded but the apply failed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        json(
          {
            user: {
              email: "iris@example.com",
              client_id: "1d37a118-4f1b-4dc0-9e3c-3426b07518df",
              inbounds: ["vless-vision"],
              created_at: 1_780_000_000,
              updated_at: 1_780_000_000,
            },
            roster_sync: "failed",
          },
          201,
        ),
      ),
    );
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Add user" });
    fireEvent.change(within(dialog).getByLabelText("Email"), { target: { value: "iris@example.com" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add user" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/stored.*retries/i);
    expect(onClose).not.toHaveBeenCalled();
    // Stored means stored: no second submit of the same form.
    expect(within(dialog).queryByRole("button", { name: "Add user" })).not.toBeInTheDocument();
  });

  it("hands an expired session back to the dashboard", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ error: "unauthenticated" }, 401)),
    );
    const { onClose, onExpired } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Add user" });
    fireEvent.change(within(dialog).getByLabelText("Email"), { target: { value: "iris@example.com" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add user" }));

    await waitFor(() => expect(onExpired).toHaveBeenCalled());
    expect(onClose).not.toHaveBeenCalled();
  });
});
