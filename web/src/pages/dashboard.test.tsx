import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Dashboard } from "./dashboard";

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

const xrayRunning = {
  collected_at: 1_723_800_000,
  status: "running",
  api_endpoint: "127.0.0.1:8080",
  version: "26.4.13",
  uptime_seconds: 1_209_600, // 14 days
  mem_bytes: 88_080_384,
  goroutines: 183,
  speed_up_bps: 2_400_000,
  speed_down_bps: 18_500_000,
  total_up_bytes: 39_100_000_000,
  total_down_bytes: 511_400_000_000,
  users_online: 1,
  unique_ips_online: 1,
};

const xrayStopped = {
  collected_at: 1_723_800_000,
  status: "stopped",
  api_endpoint: "127.0.0.1:8080",
  version: null,
  uptime_seconds: 0,
  mem_bytes: null,
  goroutines: null,
  speed_up_bps: 0,
  speed_down_bps: 0,
  total_up_bytes: 0,
  total_down_bytes: 0,
  users_online: null,
  unique_ips_online: null,
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

// stubEndpoints routes fetches by URL, since each poll hits all endpoints.
function stubEndpoints(routes: {
  server?: () => Response;
  xray?: () => Response;
  users?: () => Response;
  panel?: () => Response;
  logout?: () => Response;
}): ReturnType<typeof vi.fn> {
  const mock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("api/v1/server") && routes.server) return routes.server();
    if (url.endsWith("api/v1/xray") && routes.xray) return routes.xray();
    if (url.endsWith("api/v1/users")) {
      return routes.users ? routes.users() : json({ collected_at: 1_723_800_000, stale: false, users: [] });
    }
    if (url.endsWith("api/v1/panel")) {
      return routes.panel
        ? routes.panel()
        : json({ version: "v0.0.0-test", uptime_seconds: 300 });
    }
    if (url.endsWith("api/v1/logout") && routes.logout) return routes.logout();
    return new Response("not found", { status: 404 });
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("host stats", () => {
  it("renders the live CPU, RAM, and storage cards", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayRunning) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    expect(await screen.findByText("23.4%")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "CPU" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "RAM" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Storage" })).toBeInTheDocument();
    expect(screen.getByText("4 cores")).toBeInTheDocument();
    expect(screen.getByText("23 days")).toBeInTheDocument();
    expect(screen.getByText("load 0.42 0.38 0.31")).toBeInTheDocument();
  });

  it("refreshes the server cards every five seconds", async () => {
    vi.useFakeTimers();
    let cpu = 23.4;
    const fetchMock = stubEndpoints({
      server: () => json({ ...stats, cpu_percent: cpu }),
      xray: () => json(xrayRunning),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByText("23.4%")).toBeInTheDocument();

    cpu = 61.2;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });

    const serverPolls = fetchMock.mock.calls.filter(([input]) =>
      String(input).endsWith("api/v1/server"),
    );
    expect(serverPolls).toHaveLength(2);
    expect(screen.getByText("61.2%")).toBeInTheDocument();
  });

  it("yields to the login page when the session expires", async () => {
    stubEndpoints({ server: () => json({ error: "unauthenticated" }, 401) });
    const onUnauthenticated = vi.fn();
    render(<Dashboard onUnauthenticated={onUnauthenticated} />);

    await act(async () => {
      await Promise.resolve();
    });

    expect(onUnauthenticated).toHaveBeenCalledTimes(1);
  });

  it("yields to the login page when the xray endpoint answers 401", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json({ error: "unauthenticated" }, 401) });
    const onUnauthenticated = vi.fn();
    render(<Dashboard onUnauthenticated={onUnauthenticated} />);

    await act(async () => {
      await Promise.resolve();
    });

    expect(onUnauthenticated).toHaveBeenCalledTimes(1);
  });

  it("keeps the session and shows an error when logout cannot reach the panel", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      logout: () => {
        throw new TypeError("fetch failed");
      },
    });
    const onUnauthenticated = vi.fn();
    render(<Dashboard onUnauthenticated={onUnauthenticated} />);

    expect(await screen.findByText("23.4%")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /log out/i }));

    expect(await screen.findByText(/may still be active/i)).toBeInTheDocument();
    expect(onUnauthenticated).not.toHaveBeenCalled();
  });
});

describe("header", () => {
  it("shows the panel identity group: wordmark, version, and process uptime", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      panel: () => json({ version: "v0.0.0-test", uptime_seconds: 529_200 }), // 6d 3h
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const banner = await screen.findByRole("banner");
    expect(within(banner).getByText("xform")).toBeInTheDocument();
    expect(within(banner).getByText("v0.0.0-test")).toBeInTheDocument();
    expect(within(banner).getByText("up 6d 3h")).toBeInTheDocument();
  });

  it("re-fetches panel uptime in the five-second cycle instead of extrapolating it", async () => {
    vi.useFakeTimers();
    let panelUptime = 300; // 5 minutes
    const fetchMock = stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      panel: () => json({ version: "v0.0.0-test", uptime_seconds: panelUptime }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByText("up 5m")).toBeInTheDocument();

    // Time passes on the panel; the next poll must report the new value.
    panelUptime = 7_200; // 2h 0m
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });

    const panelPolls = fetchMock.mock.calls.filter(([input]) =>
      String(input).endsWith("api/v1/panel"),
    );
    expect(panelPolls).toHaveLength(2);
    expect(screen.getByText("up 2h 0m")).toBeInTheDocument();
    expect(screen.queryByText("up 5m")).not.toBeInTheDocument();
  });

  it("groups the xray identity with the status indicator before the service name", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayRunning) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const banner = await screen.findByRole("banner");
    const dot = within(banner).getByRole("img", { name: "running" });
    const name = within(banner).getByText("xray");
    // The status indicator sits immediately before the service name: it is
    // the name span's first child.
    expect(name.firstChild).toBe(dot);
    expect(within(banner).getByText("v26.4.13")).toBeInTheDocument();
    expect(within(banner).getByText("up 14d 0h")).toBeInTheDocument();
  });

  it("keeps the refresh note and Log out in the header", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayRunning) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const banner = await screen.findByRole("banner");
    expect(
      within(banner).getByText(/refreshing every 5s · updated \d{2}:\d{2}:\d{2}/),
    ).toBeInTheDocument();
    expect(within(banner).getByRole("button", { name: /log out/i })).toBeInTheDocument();
  });

  it("marks the xray group stopped without an uptime, banner carrying the detail", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayStopped) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const banner = await screen.findByRole("banner");
    expect(within(banner).getByRole("img", { name: "stopped" })).toBeInTheDocument();
    // xray's own uptime is gone while stopped (the panel's "up …" remains).
    expect(within(banner).queryByText("up 14d 0h")).not.toBeInTheDocument();
    expect(await screen.findByRole("alert")).toHaveTextContent(/xray-core is stopped/i);
  });
});

describe("xray status", () => {
  it("shows the status indicator, version, and uptime in the xray group when running", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayRunning) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const banner = await screen.findByRole("banner");
    expect(within(banner).getByRole("img", { name: "running" })).toBeInTheDocument();
    expect(within(banner).getByText("v26.4.13")).toBeInTheDocument();
    expect(within(banner).getByText("up 14d 0h")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows the degraded banner when xray is stopped", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayStopped) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /xray-core is stopped.*stale.*host stats stay live/i,
    );
    // Host stats stay live in degraded mode.
    expect(screen.getByText("23.4%")).toBeInTheDocument();
  });

  it("shows the degraded banner when xray is unreachable", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json({ ...xrayStopped, status: "unreachable" }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent(/xray-core is unreachable/i);
    // The banner names the configured gRPC endpoint.
    expect(banner).toHaveTextContent(/127\.0\.0\.1:8080/);
  });
});

describe("xray runtime stats", () => {
  it("renders speed, totals, online counts, and process stats when running", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(usersSnapshot),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const row = await screen.findByRole("region", { name: "xray row" });
    expect(row).toHaveTextContent("↑ 2.29 MiB/s");
    expect(row).toHaveTextContent("↓ 17.6 MiB/s");
    expect(row).toHaveTextContent("↑ 36.4 GiB");
    expect(row).toHaveTextContent("↓ 476 GiB");
    expect(row).toHaveTextContent("1 / 2"); // online of roster (gone users excluded)
    expect(row).toHaveTextContent("1 unique IPs");
    expect(row).toHaveTextContent("84.0 MiB");
    expect(row).toHaveTextContent("183 goroutines");
  });

  it("orders the rows: host info, xray generic info, then users", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayRunning) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    await screen.findByRole("region", { name: "Users" });
    const order = screen
      .getAllByRole("region")
      .map((region) => region.getAttribute("aria-label"));
    expect(order).toEqual(["Server resources", "xray row", "Users"]);
  });

  it("marks online counts unavailable on an old xray without the online RPCs", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json({ ...xrayRunning, users_online: null, unique_ips_online: null }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const row = await screen.findByRole("region", { name: "xray row" });
    expect(row).toHaveTextContent(/users online/i);
    expect(row).toHaveTextContent(/unavailable on this xray/i);
  });

  it("serves stale xray data alongside the banner in degraded mode", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json({ ...xrayRunning, status: "unreachable", speed_up_bps: 0, speed_down_bps: 0, mem_bytes: null, goroutines: null, users_online: null, unique_ips_online: null }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    expect(await screen.findByRole("alert")).toHaveTextContent(/xray-core is unreachable/i);
    // The durable totals stay on the stale row, flagged; speeds read stale;
    // host stats stay live.
    const row = screen.getByRole("region", { name: "xray row" });
    expect(row).toHaveTextContent("↑ 36.4 GiB");
    expect(row).toHaveTextContent("↓ 476 GiB");
    expect(row).toHaveTextContent("stale");
    expect(row).not.toHaveTextContent("0 B/s");
    expect(screen.getByText("23.4%")).toBeInTheDocument();
  });
});

const usersSnapshot = {
  collected_at: 1_723_800_000,
  stale: false,
  users: [
    {
      email: "alice@example.com",
      protocol: null,
      security: null,
      up_bytes_total: 12_400_000_000,
      down_bytes_total: 148_200_000_000,
      online: true,
      ips: ["203.0.113.10"],
      ip_countries: { "203.0.113.10": "NL" },
      speed_up_bps: 512_000,
      speed_down_bps: 3_800_000,
      last_seen: Math.floor(Date.now() / 1000) - 120,
      gone: false,
    },
    {
      email: "bob@example.com",
      protocol: null,
      security: null,
      up_bytes_total: 3_100_000_000,
      down_bytes_total: 41_700_000_000,
      online: false,
      ips: null,
      speed_up_bps: 0,
      speed_down_bps: 0,
      last_seen: null,
      gone: false,
    },
  ],
};

describe("users table", () => {
  it("hides gone users by default and reveals them with the toggle", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () =>
        json({
          ...usersSnapshot,
          users: [
            { ...usersSnapshot.users[0], protocol: "VLESS", security: "XTLS-Reality" },
            { ...usersSnapshot.users[1], gone: true }, // bob was edited out of the config
          ],
        }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const table = await screen.findByRole("region", { name: "Users" });
    // alice is visible with her protocol · security labels; bob is hidden.
    const aliceRow = within(table).getByRole("row", { name: /alice@example\.com/ });
    expect(aliceRow).toHaveTextContent("VLESS · XTLS-Reality");
    expect(within(table).queryByRole("row", { name: /bob@example\.com/ })).not.toBeInTheDocument();

    // The toggle reveals gone users, marked as gone.
    fireEvent.click(within(table).getByRole("button", { name: /show gone/i }));
    const bobRow = within(table).getByRole("row", { name: /bob@example\.com/ });
    expect(bobRow).toHaveTextContent("gone");
    expect(bobRow).toHaveTextContent("2.89 GiB"); // his history is retained

    // And hides them again.
    fireEvent.click(within(table).getByRole("button", { name: /hide gone/i }));
    expect(within(table).queryByRole("row", { name: /bob@example\.com/ })).not.toBeInTheDocument();
  });

  it("renders no toggle when nobody is gone", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(usersSnapshot),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const table = await screen.findByRole("region", { name: "Users" });
    expect(within(table).queryByRole("button", { name: /gone/i })).not.toBeInTheDocument();
  });

  it("renders durable traffic stacked in one Traffic column and current speed per user", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(usersSnapshot),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const table = await screen.findByRole("region", { name: "Users" });
    expect(within(table).getByRole("columnheader", { name: "Traffic" })).toBeInTheDocument();
    // The separate Up and Down columns are gone.
    expect(within(table).queryByRole("columnheader", { name: "Up" })).not.toBeInTheDocument();
    expect(within(table).queryByRole("columnheader", { name: "Down" })).not.toBeInTheDocument();
    expect(within(table).getByText("alice@example.com")).toBeInTheDocument();
    const aliceRow = within(table).getByRole("row", { name: /alice@example\.com/ });
    // Uplink and downlink share one Traffic cell, stacked on two lines.
    const trafficCell = within(aliceRow).getByText("↑ 11.5 GiB").closest("td");
    expect(trafficCell).toHaveTextContent("↓ 138 GiB");
    // Stacked like the Traffic column, so the directions are asserted separately.
    expect(aliceRow).toHaveTextContent("↑ 500 KiB/s"); // speed now, uplink
    expect(aliceRow).toHaveTextContent("↓ 3.62 MiB/s"); // speed now, downlink
    // No total column: up + down already carry the information.
    expect(within(table).queryByRole("columnheader", { name: "Total" })).not.toBeInTheDocument();
    const bobRow = within(table).getByRole("row", { name: /bob@example\.com/ });
    expect(bobRow).toHaveTextContent("idle"); // zero speeds read as idle
  });

  it("adds a named icon-only details action for every visible user", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () =>
        json({
          ...usersSnapshot,
          users: [
            ...usersSnapshot.users,
            { ...usersSnapshot.users[1], email: "carol@example.com", gone: true },
          ],
        }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const table = await screen.findByRole("region", { name: "Users" });
    // alice and bob are visible; gone carol is hidden, so only two actions.
    expect(
      within(table).getByRole("button", { name: "Open alice@example.com details" }),
    ).toBeInTheDocument();
    expect(
      within(table).getByRole("button", { name: "Open bob@example.com details" }),
    ).toBeInTheDocument();
    expect(
      within(table).queryByRole("button", { name: "Open carol@example.com details" }),
    ).not.toBeInTheDocument();

    // Revealing gone users gives them the action too — every visible User.
    fireEvent.click(within(table).getByRole("button", { name: /show gone/i }));
    expect(
      within(table).getByRole("button", { name: "Open carol@example.com details" }),
    ).toBeInTheDocument();
  });

  it("renders presence: the online dot, online IPs, and relative last seen", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(usersSnapshot),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const table = await screen.findByRole("region", { name: "Users" });
    const aliceRow = within(table).getByRole("row", { name: /alice@example\.com/ });
    expect(within(aliceRow).getByLabelText("online")).toBeInTheDocument();
    expect(aliceRow).toHaveTextContent("203.0.113.10");
    // The country flag renders beside the IP, tooltip = ISO code.
    expect(within(aliceRow).getByTitle("NL")).toHaveTextContent("🇳🇱");
    // Online users read "now" in last seen, not a timestamp.
    expect(within(aliceRow).getByText("now")).toBeInTheDocument();

    const bobRow = within(table).getByRole("row", { name: /bob@example\.com/ });
    expect(within(bobRow).getByLabelText("offline")).toBeInTheDocument();
    expect(bobRow).not.toHaveTextContent("203.0.113.10");
  });

  it("marks speeds stale when the snapshot is stale", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json({ ...usersSnapshot, stale: true }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const table = await screen.findByRole("region", { name: "Users" });
    const aliceRow = within(table).getByRole("row", { name: /alice@example\.com/ });
    expect(aliceRow).toHaveTextContent("stale");
    expect(aliceRow).not.toHaveTextContent("500 KiB/s");
  });

  it("keeps the last-known IPs and last seen visible under the stale flag", async () => {    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      // The panel serves the last-known store snapshot: nobody verifiably
      // online, but last-known IPs and last_seen stay.
      users: () =>
        json({
          ...usersSnapshot,
          stale: true,
          users: usersSnapshot.users.map((user) => ({ ...user, online: false })),
        }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const table = await screen.findByRole("region", { name: "Users" });
    const aliceRow = within(table).getByRole("row", { name: /alice@example\.com/ });
    expect(within(aliceRow).getByLabelText("offline")).toBeInTheDocument();
    expect(aliceRow).toHaveTextContent("203.0.113.10");
    expect(aliceRow).toHaveTextContent("2m ago");
    expect(aliceRow).toHaveTextContent("stale");
  });
});

describe("user details dialog", () => {
  function openAction(email: string) {
    return screen.getByRole("button", { name: `Open ${email} details` });
  }

  it("opens a modal for the exact selected User with real current observations", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(usersSnapshot),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    await screen.findByRole("region", { name: "Users" });

    fireEvent.click(openAction("alice@example.com"));

    // The dialog is named for the exact selected User.
    const dialog = await screen.findByRole("dialog", { name: "alice@example.com details" });
    expect(within(dialog).getByText("alice@example.com")).toBeInTheDocument();
    expect(within(dialog).getByText("Online")).toBeInTheDocument();
    expect(within(dialog).getByText("↑ 11.5 GiB")).toBeInTheDocument();
    expect(within(dialog).getByText("↓ 138 GiB")).toBeInTheDocument();
    expect(within(dialog).getByText("↑ 500 KiB/s")).toBeInTheDocument();
    expect(within(dialog).getByText("↓ 3.62 MiB/s")).toBeInTheDocument();
    expect(within(dialog).getByText("now")).toBeInTheDocument(); // online: last seen is now
    expect(within(dialog).getByText("203.0.113.10")).toBeInTheDocument();
    expect(within(dialog).getByTitle("NL")).toHaveTextContent("🇳🇱");

    // Closing it, then activating bob's action, shows bob — not stale alice.
    fireEvent.click(within(dialog).getByRole("button", { name: "Close User details" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    fireEvent.click(openAction("bob@example.com"));
    const bobDialog = await screen.findByRole("dialog", { name: "bob@example.com details" });
    expect(within(bobDialog).getByText("bob@example.com")).toBeInTheDocument();
    expect(within(bobDialog).getByText("Offline")).toBeInTheDocument();
    expect(within(bobDialog).getByText("↑ 2.89 GiB")).toBeInTheDocument();
    expect(within(bobDialog).getByText("↓ 38.8 GiB")).toBeInTheDocument();
    expect(within(bobDialog).getByText("idle")).toBeInTheDocument();
    expect(within(bobDialog).getByText("None")).toBeInTheDocument(); // no online IPs
    expect(within(bobDialog).queryByText("203.0.113.10")).not.toBeInTheDocument();
  });

  it("moves focus into the dialog on open and restores it to the opener on close", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(usersSnapshot),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    await screen.findByRole("region", { name: "Users" });

    const action = openAction("alice@example.com");
    // Keyboard operable: the action is a focusable native button with an
    // accessible name (Enter/Space activation is the browser's job — jsdom
    // does not synthesize it).
    action.focus();
    expect(action).toHaveFocus();
    fireEvent.click(action);

    const dialog = await screen.findByRole("dialog");
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));

    // Escape closes; focus returns to the exact opener.
    fireEvent.keyDown(within(dialog).getByText("Current observations"), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(action).toHaveFocus();

    // The close action behaves the same way.
    fireEvent.click(action);
    const reopened = await screen.findByRole("dialog");
    fireEvent.click(
      within(reopened).getByRole("button", { name: "Close User details" }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(action).toHaveFocus();
  });

  it("keeps focus trapped inside the dialog while it is open", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(usersSnapshot),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    await screen.findByRole("region", { name: "Users" });

    fireEvent.click(openAction("alice@example.com"));
    const dialog = await screen.findByRole("dialog");
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));

    // Tab at the dialog's boundary cannot escape to the page behind it.
    fireEvent.keyDown(within(dialog).getByRole("button", { name: "Close User details" }), {
      key: "Tab",
    });
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("permits one modal at a time", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(usersSnapshot),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    await screen.findByRole("region", { name: "Users" });

    fireEvent.click(openAction("alice@example.com"));
    await screen.findByRole("dialog");

    expect(screen.getAllByRole("dialog")).toHaveLength(1);
  });

  it("keeps dashboard polling running while the modal is open and updates the dialog", async () => {
    vi.useFakeTimers();
    let users = usersSnapshot;
    const fetchMock = stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json(users),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    await act(async () => {
      await Promise.resolve();
    });

    fireEvent.click(openAction("alice@example.com"));
    const dialog = screen.getByRole("dialog", { name: "alice@example.com details" });
    expect(within(dialog).getByText("↑ 11.5 GiB")).toBeInTheDocument();

    // Traffic moves on the next poll; the open dialog follows it.
    users = {
      ...usersSnapshot,
      users: [{ ...usersSnapshot.users[0], down_bytes_total: 150_000_000_000 }, usersSnapshot.users[1]],
    };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });

    const usersPolls = fetchMock.mock.calls.filter(([input]) =>
      String(input).endsWith("api/v1/users"),
    );
    expect(usersPolls).toHaveLength(2);
    expect(
      screen.getByRole("dialog", { name: "alice@example.com details" }),
    ).toBeInTheDocument();
    expect(within(dialog).getByText("↓ 140 GiB")).toBeInTheDocument();
  });

  it("marks a gone User as gone and shows historical observations", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () =>
        json({
          ...usersSnapshot,
          users: [
            ...usersSnapshot.users,
            {
              ...usersSnapshot.users[1],
              email: "carol@example.com",
              gone: true,
              last_seen: Math.floor(Date.now() / 1000) - 2_820, // 47m ago
            },
          ],
        }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const table = await screen.findByRole("region", { name: "Users" });

    fireEvent.click(within(table).getByRole("button", { name: /show gone/i }));
    fireEvent.click(openAction("carol@example.com"));

    const dialog = await screen.findByRole("dialog", { name: "carol@example.com details" });
    expect(within(dialog).getByText("Gone User")).toBeInTheDocument();
    expect(within(dialog).getByText("historical observations")).toBeInTheDocument();
    expect(within(dialog).getByText("47m ago")).toBeInTheDocument();
    expect(within(dialog).getByText("None")).toBeInTheDocument(); // no online IPs
  });

  it("flags observations as stale in the dialog when the users snapshot is stale", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json(xrayRunning),
      users: () => json({ ...usersSnapshot, stale: true }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    await screen.findByRole("region", { name: "Users" });

    fireEvent.click(openAction("alice@example.com"));

    const dialog = await screen.findByRole("dialog", { name: "alice@example.com details" });
    expect(within(dialog).getByText("stale snapshot")).toBeInTheDocument();
    expect(within(dialog).getByText("stale")).toBeInTheDocument(); // speeds read stale
    expect(within(dialog).queryByText("↑ 500 KiB/s")).not.toBeInTheDocument();
  });
});
