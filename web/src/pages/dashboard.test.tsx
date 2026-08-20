import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
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
  version: "26.4.13",
  uptime_seconds: 1_209_600, // 14 days
  mem_bytes: 88_080_384,
  goroutines: 183,
  speed_up_bps: 2_400_000,
  speed_down_bps: 18_500_000,
  total_up_bytes: 39_100_000_000,
  total_down_bytes: 511_400_000_000,
  users_online: 3,
  unique_ips_online: 4,
};

const xrayStopped = {
  collected_at: 1_723_800_000,
  status: "stopped",
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

// stubEndpoints routes fetches by URL, since each poll hits both endpoints.
function stubEndpoints(routes: {
  server?: () => Response;
  xray?: () => Response;
  logout?: () => Response;
}): ReturnType<typeof vi.fn> {
  const mock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("api/v1/server") && routes.server) return routes.server();
    if (url.endsWith("api/v1/xray") && routes.xray) return routes.xray();
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
    expect(screen.getByText("0.42 / 0.38 / 0.31")).toBeInTheDocument();
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

describe("xray status", () => {
  it("shows the status, version, and uptime pills when xray is running", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayRunning) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    expect(await screen.findByText("xray running")).toBeInTheDocument();
    expect(screen.getByText("26.4.13")).toBeInTheDocument();
    expect(screen.getByText("up 14 days")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows the degraded banner when xray is stopped", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayStopped) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    expect(await screen.findByRole("alert")).toHaveTextContent(/xray is stopped.*degraded/i);
    // Host stats stay live in degraded mode.
    expect(screen.getByText("23.4%")).toBeInTheDocument();
  });

  it("shows the degraded banner when xray is unreachable", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json({ ...xrayStopped, status: "unreachable" }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    expect(await screen.findByRole("alert")).toHaveTextContent(/xray is unreachable.*degraded/i);
  });
});

describe("xray runtime stats", () => {
  it("renders speed, totals, online counts, and process stats when running", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayRunning) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const row = await screen.findByRole("region", { name: "xray row" });
    expect(row).toHaveTextContent("↑ 2.29 MiB/s · ↓ 17.6 MiB/s");
    expect(row).toHaveTextContent("↑ 36.4 GiB · ↓ 476 GiB");
    expect(row).toHaveTextContent("3 users · 4 IPs");
    expect(row).toHaveTextContent("84.0 MiB · 183 goroutines");
  });

  it("orders the rows: host info, then xray generic info", async () => {
    stubEndpoints({ server: () => json(stats), xray: () => json(xrayRunning) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    await screen.findByRole("region", { name: "xray row" });
    const order = screen
      .getAllByRole("region")
      .map((region) => region.getAttribute("aria-label"));
    expect(order).toEqual(["Server resources", "Host details", "xray row"]);
  });

  it("marks online counts unavailable on an old xray without the online RPCs", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json({ ...xrayRunning, users_online: null, unique_ips_online: null }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const row = await screen.findByRole("region", { name: "xray row" });
    expect(row).toHaveTextContent(/users online.*unavailable/i);
  });

  it("serves stale xray data alongside the banner in degraded mode", async () => {
    stubEndpoints({
      server: () => json(stats),
      xray: () => json({ ...xrayRunning, status: "unreachable", speed_up_bps: 0, speed_down_bps: 0, mem_bytes: null, goroutines: null, users_online: null, unique_ips_online: null }),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);

    expect(await screen.findByRole("alert")).toHaveTextContent(/xray is unreachable/i);
    // The durable totals stay on the stale row; host stats stay live.
    const row = screen.getByRole("region", { name: "xray row" });
    expect(row).toHaveTextContent("↑ 36.4 GiB · ↓ 476 GiB");
    expect(row).toHaveTextContent("↑ 0 B/s · ↓ 0 B/s");
    expect(screen.getByText("23.4%")).toBeInTheDocument();
  });
});
