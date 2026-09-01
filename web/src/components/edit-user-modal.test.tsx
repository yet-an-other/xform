import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { json } from "@/test/fixtures";
import { EditUserModal } from "./edit-user-modal";

const options = [
  { tag: "vless-vision", label: "VLESS · Reality · tcp :443" },
  { tag: "vless-ws", label: "VLESS · TLS · ws :2053" },
];

const storedUser = {
  email: "alice@example.com",
  protocol: "VLESS",
  security: "Reality",
  client_id: "1d37a118-4f1b-4dc0-9e3c-3426b07518df",
  inbounds: ["vless-vision"],
  up_bytes_total: 1,
  down_bytes_total: 2,
  online: false,
  ips: null,
  speed_up_bps: 0,
  speed_down_bps: 0,
  last_seen: null,
  gone: false,
};

function renderModal(user = storedUser) {
  const onClose = vi.fn();
  const onExpired = vi.fn();
  render(
    <EditUserModal
      user={user}
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

describe("edit user dialog", () => {
  it("shows the stored selection, an editable Client ID, and no email field", () => {
    vi.stubGlobal("fetch", vi.fn());
    renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    expect(within(dialog).getByRole("checkbox", { name: /vless-vision/ })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: /vless-ws/ })).not.toBeChecked();
    const clientId = within(dialog).getByLabelText("Client ID") as HTMLInputElement;
    expect(clientId.value).toBe("1d37a118-4f1b-4dc0-9e3c-3426b07518df");
    expect(clientId).toBeEnabled();
    // Email is the identity — the dialog offers no way to change it.
    expect(within(dialog).queryByLabelText("Email")).not.toBeInTheDocument();
  });

  it("flags attachments the config no longer carries; saving drops them", async () => {
    const fetchMock = vi.fn(async () =>
      json({ user: { ...storedUser, inbounds: [] }, roster_sync: "synced" }, 200),
    );
    vi.stubGlobal("fetch", fetchMock);
    renderModal({ ...storedUser, inbounds: ["vless-vision", "vless-gone-tag"] });

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    expect(within(dialog).getByText(/vless-gone-tag/i)).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("checkbox", { name: /vless-vision/ })); // detach all
    fireEvent.click(within(dialog).getByRole("button", { name: /save/i }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("api/v1/users/alice%40example.com");
    expect(JSON.parse(String(init.body))).toEqual({
      client_id: "1d37a118-4f1b-4dc0-9e3c-3426b07518df",
      inbounds: [],
    });
  });

  it("saves the new inbound selection and a rotated Client ID", async () => {
    const fetchMock = vi.fn(async () =>
      json(
        {
          user: {
            ...storedUser,
            client_id: "2d37a118-4f1b-4dc0-9e3c-3426b07518df",
            inbounds: ["vless-vision", "vless-ws"],
          },
          roster_sync: "synced",
        },
        200,
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /vless-ws/ })); // attach ws
    const before = (within(dialog).getByLabelText("Client ID") as HTMLInputElement).value;
    fireEvent.click(within(dialog).getByRole("button", { name: /generate/i }));
    const after = (within(dialog).getByLabelText("Client ID") as HTMLInputElement).value;
    expect(after).not.toBe(before);
    fireEvent.click(within(dialog).getByRole("button", { name: /save/i }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    const body = JSON.parse(String(init.body)) as { client_id: string; inbounds: string[] };
    expect(body.client_id).toMatch(/^[0-9a-f-]{36}$/);
    expect(body.inbounds).toEqual(["vless-vision", "vless-ws"]);
  });

  it("shows the machine-readable conflict reason in the dialog", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ error: "client_id_taken" }, 409)));
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /save/i }));

    expect(await within(dialog).findByText(/already used by another user/i)).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    expect(within(dialog).getByRole("button", { name: /save/i })).toBeEnabled();
  });

  it("shows the failure banner when the store succeeded but the apply failed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ user: storedUser, roster_sync: "failed" }, 200)),
    );
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /save/i }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/stored.*retries/i);
    expect(onClose).not.toHaveBeenCalled();
    expect(within(dialog).queryByRole("button", { name: /save/i })).not.toBeInTheDocument();
  });

  it("hands an expired session back to the dashboard", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ error: "unauthenticated" }, 401)));
    const { onClose, onExpired } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /save/i }));

    await waitFor(() => expect(onExpired).toHaveBeenCalled());
    expect(onClose).not.toHaveBeenCalled();
  });
});
