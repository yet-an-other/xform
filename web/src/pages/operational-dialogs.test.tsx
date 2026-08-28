import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { json, stats, xrayRunning } from "@/test/fixtures";
import { Dashboard } from "./dashboard";

// entry builds one normalized record as the API serves it.
function entry(overrides: Record<string, unknown> = {}) {
  return {
    cursor: "cursor-1",
    timestamp_us: 1_776_868_325_921_000,
    unit: "xform.service",
    identifier: "xform",
    pid: 2127,
    priority: 6,
    message: "Panel started",
    message_encoding: "utf-8",
    message_truncated: false,
    ...overrides,
  };
}

function logSnapshot(overrides: Record<string, unknown> = {}) {
  const entries = (overrides.entries as unknown[]) ?? [entry()];
  return {
    captured_at: 1_776_868_325,
    source: "panel",
    unit: "xform.service",
    limit: 500,
    entry_count: entries.length,
    entries,
    ...overrides,
  };
}

const configSnapshot = {
  captured_at: 1_776_868_325,
  path: "/usr/local/etc/xray/config.json",
  size_bytes: 21,
  text: '{\n  "inbounds": [\n}\n',
};

// stubEndpoints routes by URL: the dashboard polls three endpoints on a timer
// while the operational routes answer per test.
function stubEndpoints(routes: {
  panelLogs?: (init?: RequestInit) => Response | Promise<Response>;
  xrayLogs?: (init?: RequestInit) => Response | Promise<Response>;
  config?: (init?: RequestInit) => Response | Promise<Response>;
  xray?: () => Response;
}): ReturnType<typeof vi.fn> {
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("api/v1/server")) return json(stats);
    if (url.endsWith("api/v1/xray")) return routes.xray ? routes.xray() : json(xrayRunning);
    if (url.endsWith("api/v1/users")) {
      return json({ collected_at: 1_723_800_000, stale: false, users: [] });
    }
    if (url.endsWith("api/v1/panel")) return json({ version: "v0.0.0-test", uptime_seconds: 300 });
    if (url.endsWith("api/v1/logs/panel")) {
      return routes.panelLogs ? routes.panelLogs(init) : json(logSnapshot());
    }
    if (url.endsWith("api/v1/logs/xray")) {
      return routes.xrayLogs
        ? routes.xrayLogs(init)
        : json(logSnapshot({ source: "xray", unit: "xray.service" }));
    }
    if (url.endsWith("api/v1/xray/config")) {
      return routes.config ? routes.config(init) : json(configSnapshot);
    }
    return new Response("not found", { status: 404 });
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

// openDialog clicks one header action and returns the dialog it opened.
async function openDialog(name: string): Promise<HTMLElement> {
  fireEvent.click(await screen.findByRole("button", { name }));
  return await screen.findByRole("dialog");
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  Reflect.deleteProperty(navigator, "clipboard");
});

describe("Log snapshot dialogs", () => {
  it("requests one fresh snapshot on open and shows the newest-first table", async () => {
    const fetchMock = stubEndpoints({
      panelLogs: () =>
        json(
          logSnapshot({
            entries: [
              entry({ cursor: "newest", message: "Panel started" }),
              entry({
                cursor: "older",
                timestamp_us: 1_776_868_320_000_000,
                message: "roster reloaded",
                priority: 5,
              }),
            ],
          }),
        ),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");

    const rows = await within(dialog).findAllByRole("row");
    // Header row plus one row per entry, newest first.
    expect(rows).toHaveLength(3);
    expect(rows[1]).toHaveTextContent("Panel started");
    expect(rows[2]).toHaveTextContent("roster reloaded");
    expect(within(rows[1]).getByText("2026-04-22 14:32:05")).toBeInTheDocument();
    expect(within(rows[1]).getByText("xform[2127]")).toBeInTheDocument();
    expect(within(rows[1]).getByText("info")).toBeInTheDocument();
    expect(within(rows[2]).getByText("notice")).toBeInTheDocument();

    const logRequests = fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("api/v1/logs/panel"),
    );
    expect(logRequests).toHaveLength(1);
  });
});

describe("Config snapshot dialog", () => {
  it("shows the configured path and the exact text, with Copy and no Refresh", async () => {
    const fetchMock = stubEndpoints({});

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View xray config");

    expect(await within(dialog).findByText("/usr/local/etc/xray/config.json")).toBeInTheDocument();
    // Character-for-character, not whitespace-normalized: the point of the
    // viewer is that the file reads exactly as it is on disk.
    expect(dialog.querySelector("pre")?.textContent).toBe(configSnapshot.text);
    expect(within(dialog).getByRole("button", { name: /copy/i })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /refresh/i })).not.toBeInTheDocument();

    const requests = fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("api/v1/xray/config"),
    );
    expect(requests).toHaveLength(1);
  });

  it("copies every character, final newline included", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    stubEndpoints({});

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View xray config");
    await within(dialog).findByText("/usr/local/etc/xray/config.json");

    fireEvent.click(within(dialog).getByRole("button", { name: /copy/i }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(configSnapshot.text));
    expect(writeText.mock.calls[0][0].endsWith("\n")).toBe(true);
  });

  it("offers no Copy action when the read failed", async () => {
    stubEndpoints({
      config: () => json({ error: "config snapshot unavailable", reason: "config_too_large" }, 503),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View xray config");

    expect(await within(dialog).findByText(/config_too_large/)).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /copy/i })).not.toBeInTheDocument();
  });
});

describe("Log snapshot states", () => {
  it("shows a loading state, then the snapshot metadata and standing notes", async () => {
    let release: (value: Response) => void = () => {};
    stubEndpoints({ panelLogs: () => new Promise<Response>((resolve) => (release = resolve)) });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");

    expect(within(dialog).getByRole("status")).toHaveTextContent(/collecting/i);

    await act(async () => {
      release(json(logSnapshot()));
    });

    expect(await within(dialog).findByText("1 entry")).toBeInTheDocument();
    expect(within(dialog).getByText(/Snapshot at 2026-04-22 14:32:05 UTC/)).toBeInTheDocument();
    expect(within(dialog).getByText("Bounded · manual refresh")).toBeInTheDocument();
    expect(within(dialog).getByText("No Panel redaction")).toBeInTheDocument();
    expect(within(dialog).getByText("No live tail")).toBeInTheDocument();
  });

  it("reports an empty snapshot as a success", async () => {
    stubEndpoints({ panelLogs: () => json(logSnapshot({ entries: [] })) });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");

    expect(await within(dialog).findByText(/no records in this snapshot/i)).toBeInTheDocument();
    expect(within(dialog).getByText("0 entries")).toBeInTheDocument();
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
  });

  it("marks a binary message and explains a truncated one", async () => {
    stubEndpoints({
      panelLogs: () =>
        json(
          logSnapshot({
            entries: [
              entry({ cursor: "binary", message: "3q2+7w==", message_encoding: "base64" }),
              entry({
                cursor: "truncated",
                message: null,
                message_encoding: null,
                message_truncated: true,
              }),
            ],
          }),
        ),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");

    expect(await within(dialog).findByText("binary")).toBeInTheDocument();
    expect(within(dialog).getByText("3q2+7w==")).toBeInTheDocument();
    expect(
      within(dialog).getByText("[message exceeds journal field limit]"),
    ).toBeInTheDocument();
  });

  it("falls back to the unit when a record carries no identifier", async () => {
    stubEndpoints({
      panelLogs: () =>
        json(logSnapshot({ entries: [entry({ identifier: null, pid: null, priority: null })] })),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");

    expect(await within(dialog).findByText("xform.service")).toBeInTheDocument();
  });

  it("disables Refresh while its request runs and collects again when it finishes", async () => {
    let release: (value: Response) => void = () => {};
    let collections = 0;
    const fetchMock = stubEndpoints({
      panelLogs: () => {
        collections += 1;
        if (collections === 1) return json(logSnapshot());
        return new Promise<Response>((resolve) => (release = resolve));
      },
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");
    await within(dialog).findByText("1 entry");

    const refresh = within(dialog).getByRole("button", { name: /refresh/i });
    fireEvent.click(refresh);
    await waitFor(() => expect(refresh).toBeDisabled());

    await act(async () => {
      release(json(logSnapshot({ entries: [entry({ cursor: "fresh", message: "second read" })] })));
    });

    expect(await within(dialog).findByText("second read")).toBeInTheDocument();
    await waitFor(() => expect(refresh).not.toBeDisabled());
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).endsWith("api/v1/logs/panel")),
    ).toHaveLength(2);
  });

  it("keeps the earlier entries and capture time when a refresh fails", async () => {
    let collections = 0;
    stubEndpoints({
      panelLogs: () => {
        collections += 1;
        return collections === 1
          ? json(logSnapshot())
          : json({ error: "log snapshot unavailable", reason: "access_denied" }, 503);
      },
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");
    await within(dialog).findByText("Panel started");

    fireEvent.click(within(dialog).getByRole("button", { name: /refresh/i }));

    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveTextContent(/Refresh failed, showing snapshot from 2026-04-22 14:32:05 UTC/);
    expect(alert).toHaveTextContent(/access_denied/);
    // The entries and the time that produced them are still on screen.
    expect(within(dialog).getByText("Panel started")).toBeInTheDocument();
    expect(within(dialog).getByText(/Snapshot at 2026-04-22 14:32:05 UTC/)).toBeInTheDocument();
  });

  it("shows only the error state when the first collection fails", async () => {
    stubEndpoints({
      panelLogs: () => json({ error: "log snapshot unavailable", reason: "timeout" }, 503),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");

    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveTextContent(/timeout/);
    expect(within(dialog).queryByRole("table")).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/showing snapshot from/i)).not.toBeInTheDocument();
  });
});

describe("operational dialog behaviour", () => {
  it("returns to login on a 401 and closes the modal", async () => {
    const onUnauthenticated = vi.fn();
    stubEndpoints({ panelLogs: () => json({ error: "unauthenticated" }, 401) });

    render(<Dashboard onUnauthenticated={onUnauthenticated} />);
    await openDialog("View Panel logs");

    await waitFor(() => expect(onUnauthenticated).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("keeps a snapshot failure out of the Dashboard's own state", async () => {
    stubEndpoints({
      panelLogs: () => json({ error: "log snapshot unavailable", reason: "access_denied" }, 503),
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");
    await within(dialog).findByRole("alert");

    // The dashboard behind the modal is untouched: no degraded banner, no
    // "unable to refresh" strip (§7.5).
    expect(screen.queryByText(/unable to refresh/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/xray-core is/i)).not.toBeInTheDocument();
    expect(screen.getByText("23.4%")).toBeInTheDocument();
  });

  it("keeps the xray snapshot actions usable while xray is stopped", async () => {
    stubEndpoints({ xray: () => json({ ...xrayRunning, status: "stopped", version: null }) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    const logsAction = await screen.findByRole("button", { name: "View xray logs" });
    const configAction = screen.getByRole("button", { name: "View xray config" });
    expect(logsAction).not.toBeDisabled();
    expect(configAction).not.toBeDisabled();

    const dialog = await openDialog("View xray config");
    expect(await within(dialog).findByText("/usr/local/etc/xray/config.json")).toBeInTheDocument();
  });

  it("collects each source separately and keeps one modal open at a time", async () => {
    const fetchMock = stubEndpoints({});

    render(<Dashboard onUnauthenticated={() => {}} />);
    const panelDialog = await openDialog("View Panel logs");
    expect(await within(panelDialog).findByText("Panel logs")).toBeInTheDocument();

    fireEvent.click(within(panelDialog).getByRole("button", { name: /close/i }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    const xrayDialog = await openDialog("View xray logs");
    expect(await within(xrayDialog).findByText("xray logs")).toBeInTheDocument();
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(within(xrayDialog).getByText(/xray\.service/)).toBeInTheDocument();

    const urls = fetchMock.mock.calls
      .map(([url]) => String(url))
      .filter((url) => url.includes("api/v1/logs/"));
    expect(urls.filter((url) => url.endsWith("logs/panel"))).toHaveLength(1);
    expect(urls.filter((url) => url.endsWith("logs/xray"))).toHaveLength(1);
  });

  it("aborts the request and starts fresh on the next opening", async () => {
    const signals: AbortSignal[] = [];
    let collections = 0;
    let release: (value: Response) => void = () => {};
    stubEndpoints({
      panelLogs: (init) => {
        if (init?.signal) signals.push(init.signal);
        collections += 1;
        // The second opening is held pending, so its initial load is
        // observable rather than resolved before the assertion runs.
        return collections === 1
          ? json(logSnapshot())
          : new Promise<Response>((resolve) => (release = resolve));
      },
    });

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");
    await within(dialog).findByText("Panel started");

    fireEvent.click(within(dialog).getByRole("button", { name: /close/i }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(signals[0].aborted).toBe(true);

    // Reopening always performs an initial load rather than showing what the
    // last opening collected (§7.4).
    const reopened = await openDialog("View Panel logs");
    expect(within(reopened).getByRole("status")).toHaveTextContent(/collecting/i);
    // Nothing from the previous opening survived the close.
    expect(within(reopened).queryByText("Panel started")).not.toBeInTheDocument();

    await act(async () => {
      release(json(logSnapshot()));
    });
    await within(reopened).findByText("Panel started");
    expect(signals).toHaveLength(2);
  });

  it("scrolls fixed-format content horizontally instead of reflowing it", async () => {
    stubEndpoints({});

    render(<Dashboard onUnauthenticated={() => {}} />);
    const logs = await openDialog("View Panel logs");
    await within(logs).findByText("Panel started");
    const table = within(logs).getByRole("table");
    // jsdom applies no CSS and measures nothing, so this asserts the
    // structural contract the stylesheet relies on — the scrolling itself is
    // the manual viewport check (§9.3), not something this suite can prove.
    expect(table.parentElement?.className).toContain("overflow-x-auto");
    expect(table.className).toContain("min-w-[700px]");

    fireEvent.click(within(logs).getByRole("button", { name: /close/i }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    const config = await openDialog("View xray config");
    await within(config).findByText("/usr/local/etc/xray/config.json");
    const pre = config.querySelector("pre");
    expect(pre?.className).toContain("whitespace-pre");
    expect(pre?.className).toContain("min-w-[620px]");
    expect(pre?.parentElement?.className).toContain("overflow-x-auto");
  });
});

describe("operational actions and the Dashboard's own polling", () => {
  it("keeps collecting to the moment it was asked, not to the Dashboard's cadence", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = stubEndpoints({});

    render(<Dashboard onUnauthenticated={() => {}} />);
    const dialog = await openDialog("View Panel logs");
    await within(dialog).findByText("Panel started");

    // Two full poll cycles pass with the dialog open.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });

    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).endsWith("api/v1/logs/panel")),
    ).toHaveLength(1);
  });

  it("keeps the snapshot actions when the xray observation itself fails", async () => {
    // A failed xray poll leaves the Dashboard with no status at all. Neither
    // viewer reads xray to answer, so neither action may disappear with it.
    stubEndpoints({ xray: () => new Response("boom", { status: 500 }) });

    render(<Dashboard onUnauthenticated={() => {}} />);

    expect(await screen.findByRole("button", { name: "View xray logs" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "View xray config" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "View Panel logs" })).toBeEnabled();

    const dialog = await openDialog("View xray logs");
    expect(await within(dialog).findByText("Panel started")).toBeInTheDocument();
  });
});
