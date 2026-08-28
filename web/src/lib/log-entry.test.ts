import { describe, expect, it } from "vitest";

import { logMessage, logSource, priorityLabel, formatSnapshotTime, formatEntryTime } from "./log-entry";
import type { LogEntry } from "./api";

function entry(overrides: Partial<LogEntry> = {}): LogEntry {
  return {
    cursor: "cursor-1",
    timestamp_us: 1_776_868_325_921_000,
    unit: "xform.service",
    identifier: null,
    pid: null,
    priority: null,
    message: "",
    message_encoding: "utf-8",
    message_truncated: false,
    ...overrides,
  };
}

describe("logSource", () => {
  it("renders identifier[pid] when both are present", () => {
    expect(logSource(entry({ identifier: "xform", pid: 2127 }))).toBe("xform[2127]");
  });

  it("falls back to the unit when the identifier is missing", () => {
    expect(logSource(entry({ identifier: null, pid: 2127 }))).toBe("xform.service");
  });

  it("falls back to the unit when the pid is missing", () => {
    expect(logSource(entry({ identifier: "xform", pid: null }))).toBe("xform.service");
  });
});

describe("priorityLabel", () => {
  it("uses syslog labels", () => {
    expect([0, 1, 2, 3, 4, 5, 6, 7].map(priorityLabel)).toEqual([
      "emerg",
      "alert",
      "crit",
      "error",
      "warning",
      "notice",
      "info",
      "debug",
    ]);
  });

  it("has no label without a priority", () => {
    expect(priorityLabel(null)).toBeNull();
  });
});

describe("logMessage", () => {
  it("returns UTF-8 text as it stands", () => {
    const message = logMessage(entry({ message: "Panel started", message_encoding: "utf-8" }));
    expect(message).toEqual({ text: "Panel started", binary: false, truncated: false });
  });

  it("marks a base64 message as binary without decoding it", () => {
    const message = logMessage(
      entry({ message: "3q2+7w==", message_encoding: "base64" }),
    );
    expect(message.binary).toBe(true);
    expect(message.text).toBe("3q2+7w==");
  });

  it("explains a truncated null message in the approved words", () => {
    const message = logMessage(
      entry({ message: null, message_encoding: null, message_truncated: true }),
    );
    expect(message).toEqual({
      text: "[message exceeds journal field limit]",
      binary: false,
      truncated: true,
    });
  });

  it("keeps an empty message empty", () => {
    expect(logMessage(entry({ message: "" })).text).toBe("");
  });
});

describe("timestamps", () => {
  it("renders entry times as UTC to the second", () => {
    expect(formatEntryTime(1_776_868_325_921_000)).toBe("2026-04-22 14:32:05");
  });

  it("names the zone on the snapshot capture time", () => {
    expect(formatSnapshotTime(1_776_868_325)).toBe("2026-04-22 14:32:05 UTC");
  });
});
