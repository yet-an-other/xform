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
  disabled: false,
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

  it("separates the editable scope from the fixed user identity", () => {
    vi.stubGlobal("fetch", vi.fn());
    renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    const heading = within(dialog).getByRole("heading", { name: "Edit user access" });
    const header = heading.closest("header");
    expect(header).not.toBeNull();
    expect(within(header!).getByText("Inbounds and Client ID")).toBeInTheDocument();
    expect(within(header!).getByText("alice@example.com")).toBeInTheDocument();
    expect(within(header!).getByText("Email cannot be changed")).toBeInTheDocument();
  });

  it("flags attachments the config no longer carries; saving drops them", async () => {
    const fetchMock = vi.fn(async () =>
      json({ user: { ...storedUser, inbounds: [] }, roster_sync: "synced" }, 200),
    );
    vi.stubGlobal("fetch", fetchMock);
    renderModal({ ...storedUser, inbounds: ["vless-vision", "vless-vanished-tag"] });

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    expect(within(dialog).getByText(/vless-vanished-tag/i)).toBeInTheDocument();

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

  // The disable act lives in the edit view, not on the rows (ADR-0007);
  // the confirm names what disable means before asking.
  it("disables behind a confirm that names the semantics", async () => {
    const fetchMock = vi.fn(async () => json({ roster_sync: "synced" }, 200));
    vi.stubGlobal("fetch", fetchMock);
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /disable user/i }));

    expect(within(dialog).getByText(/off every inbound immediately/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/not force-killed/i)).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: /disable user/i }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("api/v1/users/alice%40example.com/disable");
    expect(init.method).toBe("POST");
  });

  it("cancels the disable confirm back to the form", async () => {
    vi.stubGlobal("fetch", vi.fn());
    renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /disable user/i }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    // Back on the form, nothing was sent.
    expect(within(dialog).getByRole("button", { name: /save/i })).toBeEnabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  // The delete act also lives in the edit view (ADR-0007): the confirm
  // names the purge — history permanently erased, irreversible.
  it("deletes behind a confirm that names the purge", async () => {
    const fetchMock = vi.fn(async () => json({ roster_sync: "synced" }, 200));
    vi.stubGlobal("fetch", fetchMock);
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /delete user/i }));

    const intro = within(dialog).getByText(/removed from every inbound/i);
    expect(intro.textContent).toContain("alice@example.com");
    expect(within(dialog).getByText(/permanently erased/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/irreversible/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/brand-new user/i)).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: /delete user/i }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("api/v1/users/alice%40example.com");
    expect(init.method).toBe("DELETE");
  });

  it("cancels the delete confirm back to the form", async () => {
    vi.stubGlobal("fetch", vi.fn());
    renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /delete user/i }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    // Back on the form, nothing was sent.
    expect(within(dialog).getByRole("button", { name: /save/i })).toBeEnabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("treats a bare 204 — nothing left to purge — as a closed delete", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 204 })));
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /delete user/i }));
    fireEvent.click(within(dialog).getByRole("button", { name: /delete user/i }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("shows the failure banner when the disable stored but the apply failed", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ roster_sync: "failed" }, 200)));
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /disable user/i }));
    fireEvent.click(within(dialog).getByRole("button", { name: /disable user/i }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/stored.*retries/i);
    expect(onClose).not.toHaveBeenCalled();
    expect(
      within(dialog).queryByRole("button", { name: /disable user/i }),
    ).not.toBeInTheDocument();
  });
});

describe("disabled user's edit dialog", () => {
  const disabledUser = { ...storedUser, disabled: true };

  function renderDisabled(onClose = vi.fn(), onExpired = vi.fn()) {
    render(
      <EditUserModal
        user={disabledUser}
        inbounds={options}
        opener={createRef<HTMLElement>()}
        onClose={onClose}
        onExpired={onExpired}
      />,
    );
    return { onClose, onExpired };
  }

  it("says what disabled means and offers Re-enable instead of the form", () => {
    vi.stubGlobal("fetch", vi.fn());
    renderDisabled();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    expect(within(dialog).getByText(/off every inbound, no connection profiles, history kept/i)).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Client ID")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /re-enable user/i })).toBeEnabled();
    expect(within(dialog).queryByRole("button", { name: /^save$/i })).not.toBeInTheDocument();
  });

  it("re-enables: the stored credential and attachments re-apply", async () => {
    const fetchMock = vi.fn(async () =>
      json({ user: { ...disabledUser, disabled: false }, roster_sync: "synced" }, 200),
    );
    vi.stubGlobal("fetch", fetchMock);
    const onClose = vi.fn();
    renderDisabled(onClose);

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /re-enable user/i }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("api/v1/users/alice%40example.com/enable");
    expect(init.method).toBe("POST");
  });

  it("keeps the dialog open on the failure banner when the re-enable's apply failed", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ user: disabledUser, roster_sync: "failed" }, 200)));
    const onClose = vi.fn();
    renderDisabled(onClose);

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /re-enable user/i }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/stored.*retries/i);
    expect(onClose).not.toHaveBeenCalled();
  });

  // Delete works on disabled users too (ADR-0007): the purge is the only
  // way out of a disabled row an admin does not want to keep.
  it("offers Delete user, purging every trace of the disabled user", async () => {
    const fetchMock = vi.fn(async () => json({ roster_sync: "synced" }, 200));
    vi.stubGlobal("fetch", fetchMock);
    const onClose = vi.fn();
    renderDisabled(onClose);

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    expect(within(dialog).getByRole("button", { name: /delete user/i })).toBeEnabled();
    fireEvent.click(within(dialog).getByRole("button", { name: /delete user/i }));
    expect(within(dialog).getByText(/permanently erased/i)).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: /delete user/i }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("api/v1/users/alice%40example.com");
    expect(init.method).toBe("DELETE");
  });

  it("cancels the disabled user's delete confirm back to the re-enable view", async () => {
    vi.stubGlobal("fetch", vi.fn());
    renderDisabled();

    const dialog = screen.getByRole("dialog", { name: "Edit alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /delete user/i }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    // Back on the disabled view, nothing was sent.
    expect(within(dialog).getByRole("button", { name: /re-enable user/i })).toBeEnabled();
    expect(fetch).not.toHaveBeenCalled();
  });
});
