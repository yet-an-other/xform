import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { LogSnapshot, LogSource } from "@/lib/api";
import { json } from "@/test/fixtures";
import { LogSnapshotModal } from "./log-snapshot-modal";

// logSnapshot is one canned Log snapshot; filter defaults to the unfiltered
// echo the API sends for a plain request.
function logSnapshot(overrides: Partial<LogSnapshot> = {}): LogSnapshot {
  return {
    captured_at: 1_723_800_000,
    source: "xray",
    filter: "all",
    unit: "xray.service",
    limit: 500,
    entry_count: 2,
    entries: [
      {
        cursor: "cursor-newest",
        timestamp_us: 1_723_800_000_123_456,
        unit: "xray.service",
        identifier: "xray",
        pid: 900,
        priority: 6,
        message: "2026/08/16 10:40:00 [Warning] client rejected",
        message_encoding: "utf-8",
        message_truncated: false,
      },
      {
        cursor: "cursor-older",
        timestamp_us: 1_723_799_000_123_456,
        unit: "xray.service",
        identifier: "xray",
        pid: 900,
        priority: 6,
        message: "2026/08/16 10:39:50 [Info] xray started",
        message_encoding: "utf-8",
        message_truncated: false,
      },
    ],
    ...overrides,
  };
}

function renderModal(source: LogSource = "xray") {
  const onClose = vi.fn();
  const onExpired = vi.fn();
  const view = render(
    <LogSnapshotModal
      source={source}
      opener={createRef<HTMLElement>()}
      onClose={onClose}
      onExpired={onExpired}
    />,
  );
  return { onClose, onExpired, ...view };
}

// requestedUrls lists every URL the component asked fetch for, in order.
function requestedUrls(fetchMock: ReturnType<typeof vi.fn>): string[] {
  return fetchMock.mock.calls.map(([url]) => String(url));
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("log snapshot dialog", () => {
  it("collects the full journal by default, with no filter parameter", async () => {
    const fetchMock = vi.fn(async () => json(logSnapshot()));
    vi.stubGlobal("fetch", fetchMock);
    renderModal("xray");

    const dialog = screen.getByRole("dialog", { name: "xray logs" });
    await within(dialog).findByText("client rejected");

    expect(requestedUrls(fetchMock)).toEqual(["api/v1/logs/xray"]);
    expect(within(dialog).getByText(/2 entries/)).toBeInTheDocument();
  });

  it("renders the hide-access toggle only in the xray logs viewer", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json(logSnapshot({ source: "panel", unit: "xform.service" }))));
    renderModal("panel");
    const panelDialog = screen.getByRole("dialog", { name: "Panel logs" });
    await within(panelDialog).findByText("client rejected");
    expect(within(panelDialog).queryByRole("button", { name: /hide access records/i })).not.toBeInTheDocument();

    cleanup();

    vi.stubGlobal("fetch", vi.fn(async () => json(logSnapshot())));
    renderModal("xray");
    const xrayDialog = screen.getByRole("dialog", { name: "xray logs" });
    expect(within(xrayDialog).getByRole("button", { name: /hide access records/i })).toBeInTheDocument();
  });

  it("toggling collects a fresh system-only snapshot and says so in the count line", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(async (url: string) =>
        url.includes("filter=system")
          ? json(logSnapshot({ filter: "system", entry_count: 1, entries: logSnapshot().entries.slice(0, 1) }))
          : json(logSnapshot()),
      );
    vi.stubGlobal("fetch", fetchMock);
    renderModal("xray");

    const dialog = screen.getByRole("dialog", { name: "xray logs" });
    await within(dialog).findByText("client rejected");
    fireEvent.click(within(dialog).getByRole("button", { name: /hide access records/i }));

    // A fresh snapshot is collected under the filter — never a client-side
    // re-cut of the cached one (ADR-0006).
    await waitFor(() => expect(requestedUrls(fetchMock)).toContain("api/v1/logs/xray?filter=system"));
    // The count line reads from the snapshot's echoed filter, so a filtered
    // view never wears the unfiltered label.
    expect(await within(dialog).findByText(/1 system records/)).toBeInTheDocument();
  });

  it("resets the filter choice when the dialog closes and reopens", async () => {
    const fetchMock = vi.fn(async () => json(logSnapshot()));
    vi.stubGlobal("fetch", fetchMock);
    const view = renderModal("xray");

    const dialog = screen.getByRole("dialog", { name: "xray logs" });
    await within(dialog).findByText("client rejected");
    fireEvent.click(within(dialog).getByRole("button", { name: /hide access records/i }));
    await waitFor(() => expect(requestedUrls(fetchMock)).toContain("api/v1/logs/xray?filter=system"));

    // Closing unmounts the dialog; the next opening starts from the
    // faithful, unfiltered snapshot again — no hidden state between visits.
    view.unmount();
    fetchMock.mockClear();
    renderModal("xray");

    await screen.findByText("client rejected");
    expect(requestedUrls(fetchMock)).toEqual(["api/v1/logs/xray"]);
  });
});
