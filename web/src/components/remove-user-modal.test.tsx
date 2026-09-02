import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { json } from "@/test/fixtures";
import { RemoveUserModal } from "./remove-user-modal";

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
    <RemoveUserModal
      user={user}
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

describe("remove user dialog", () => {
  it("says what removal means before asking for the confirm", () => {
    vi.stubGlobal("fetch", vi.fn());
    renderModal();

    const dialog = screen.getByRole("dialog", { name: "Remove alice@example.com" });
    expect(within(dialog).getByText(/history stays/i)).toBeInTheDocument();
    // The documented behavior: established connections are not force-killed.
    expect(within(dialog).getByText(/not force-killed/i)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /remove user/i })).toBeEnabled();
  });

  it("removes the user and closes on a settled apply", async () => {
    const fetchMock = vi.fn(async () => json({ roster_sync: "synced" }, 200));
    vi.stubGlobal("fetch", fetchMock);
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Remove alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /remove user/i }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("api/v1/users/alice%40example.com");
    expect(init.method).toBe("DELETE");
  });

  it("treats the idempotent 204 as a settled removal", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Remove alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /remove user/i }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("shows the failure banner when the store succeeded but the apply failed", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ roster_sync: "failed" }, 200)));
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Remove alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /remove user/i }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/stored.*retries/i);
    expect(onClose).not.toHaveBeenCalled();
    expect(within(dialog).queryByRole("button", { name: /remove user/i })).not.toBeInTheDocument();
  });

  it("shows a reachable-panel error and stays confirmable", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ error: "roster unavailable" }, 500)));
    const { onClose } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Remove alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /remove user/i }));

    expect(await within(dialog).findByText(/could not reach the panel/i)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /remove user/i })).toBeEnabled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("hands an expired session back to the dashboard", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ error: "unauthenticated" }, 401)));
    const { onClose, onExpired } = renderModal();

    const dialog = screen.getByRole("dialog", { name: "Remove alice@example.com" });
    fireEvent.click(within(dialog).getByRole("button", { name: /remove user/i }));

    await waitFor(() => expect(onExpired).toHaveBeenCalled());
    expect(onClose).not.toHaveBeenCalled();
  });
});
