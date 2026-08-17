import { cleanup, fireEvent, render, screen } from "@testing-library/react";
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
  vi.unstubAllGlobals();
});

// stubApi answers fetches by URL suffix; `authed` simulates the session cookie.
function stubApi() {
  let authed = false;
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
        return new Response('{"error":"unauthenticated"}', { status: 401 });
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
});
