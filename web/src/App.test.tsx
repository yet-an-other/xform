import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import App from "./App";

const stats = {
  collected_at: 1_723_800_000,
  cpu_percent: 23.4,
  cpu_cores: 4,
  mem_used_bytes: 5_100_273_664,
  mem_total_bytes: 8_589_934_592,
  disk_path: "/",
  disk_used_bytes: 90_194_313_216,
  disk_total_bytes: 171_798_691_840,
  uptime_seconds: 1_987_200,
  load_avg: [0.42, 0.38, 0.31] as [number, number, number],
};

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  vi.unstubAllGlobals();
});

// stubApi answers fetches by URL suffix; `authed` simulates the session cookie.
function stubApi(options: { initiallyAuthed?: boolean; panelStatus?: number } = {}) {
  let authed = options.initiallyAuthed ?? false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("api/v1/login")) {
        authed = true;
        return new Response(null, { status: 204 });
      }
      if (url.endsWith("api/v1/logout")) {
        authed = false;
        return new Response(null, { status: 204 });
      }
      if (!authed) {
        return new Response('{"error":"unauthenticated","authentication_mode":"password"}', { status: 401 });
      }
      if (url.endsWith("api/v1/users")) {
        return new Response(JSON.stringify({ collected_at: 1_723_800_000, stale: false, users: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.endsWith("api/v1/panel")) {
        const panelStatus = options.panelStatus ?? 200;
        if (panelStatus !== 200) return new Response(null, { status: panelStatus });
        return new Response(JSON.stringify({ version: "v0.0.0-test", uptime_seconds: 300 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.endsWith("api/v1/xray")) {
        return new Response(
          JSON.stringify({
            collected_at: 1_723_800_000,
            status: "running",
            version: "26.4.13",
            uptime_seconds: 1_209_600,
            mem_bytes: 88_080_384,
            goroutines: 183,
            speed_up_bps: 2_400_000,
            speed_down_bps: 18_500_000,
            total_up_bytes: 39_100_000_000,
            total_down_bytes: 511_400_000_000,
            users_online: 3,
            unique_ips_online: 4,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response(JSON.stringify(stats), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
}

describe("auth flow", () => {
  it("gates the dashboard behind login and returns on logout", async () => {
    stubApi();
    render(<App />);

    // The dashboard's first poll is 401, so the login page takes over.
    const password = await screen.findByLabelText(/password/i);
    fireEvent.change(password, { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /log in/i }));

    expect(await screen.findByRole("heading", { name: "CPU" })).toBeInTheDocument();

    fireEvent.click(await screen.findByRole("button", { name: /log out/i }));

    expect(await screen.findByLabelText(/password/i)).toBeInTheDocument();
  });

  it("does not fall back to a password form after a trusted gateway failure", async () => {
    sessionStorage.setItem("xform_trusted_auth_redirect", "1");
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response('{"error":"unauthenticated","authentication_mode":"trusted_proxy"}', {
          status: 401,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    render(<App />);

    expect(await screen.findByRole("alert")).toHaveTextContent(/authentication gateway did not admit/i);
    expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /log in/i })).not.toBeInTheDocument();
  });

  it("clears the trusted redirect guard after an admitted observation", async () => {
    sessionStorage.setItem("xform_trusted_auth_redirect", "1");
    stubApi({ initiallyAuthed: true, panelStatus: 500 });
    render(<App />);

    expect(await screen.findByRole("heading", { name: "CPU" })).toBeInTheDocument();
    await waitFor(() => {
      expect(sessionStorage.getItem("xform_trusted_auth_redirect")).toBeNull();
    });
  });

  it("treats an untyped gateway 401 as a trusted failure", async () => {
    sessionStorage.setItem("xform_trusted_auth_redirect", "1");
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response('{"error":"unauthenticated"}', { status: 401 })),
    );
    render(<App />);

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument();
  });
});
