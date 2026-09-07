import { describe, expect, it } from "vitest";

import { entryPriority, logMessage, logSource, priorityLabel, formatSnapshotTime, formatEntryTime } from "./log-entry";
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

describe("entryPriority", () => {
  // Captured from the live journal: journald recorded priority 6 because
  // xray writes everything to stdout; the severity lives only in the text.
  const XRAY_WARNING =
    "2026/09/07 05:40:07.427399 [Warning] [592939330] app/proxyman/inbound: connection ends > " +
    "proxy/vless/inbound: account f9ef86bc-15a4-4af4-8bfa-0c3dce2f0ef5 is rejected since the client flow is empty";
  const XRAY_ACCESS =
    "2026/09/07 05:40:01.666987 from 128.71.34.88:37486 accepted udp:1.0.0.1:53 " +
    "[vless-reality -> direct] email: ss-book";

  it("prefers the severity embedded in the message over the journal priority", () => {
    expect(entryPriority(entry({ priority: 6, message: XRAY_WARNING }))).toBe(4);
  });

  it("maps every embedded level word", () => {
    const levels: Array<[string, number]> = [
      ["Error", 3],
      ["Warning", 4],
      ["Info", 6],
      ["Debug", 7],
    ];
    for (const [word, priority] of levels) {
      expect(entryPriority(entry({ message: `2026/09/07 05:40:07 [${word}] text` }))).toBe(priority);
    }
  });

  it("reads a marker without a preceding timestamp", () => {
    expect(entryPriority(entry({ message: "[Error] plain failure" }))).toBe(3);
  });

  it("falls back to the journal priority when the message embeds no marker", () => {
    expect(entryPriority(entry({ priority: 6, message: XRAY_ACCESS }))).toBe(6);
  });

  it("ignores bracketed text that names no level", () => {
    expect(entryPriority(entry({ priority: 4, message: "[vless-reality -> direct] rest" }))).toBe(4);
  });

  it("never parses a binary payload", () => {
    expect(
      entryPriority(entry({ priority: 6, message: "3q2+7w==", message_encoding: "base64" })),
    ).toBe(6);
  });

  it("has no priority when neither the message nor the journal carries one", () => {
    expect(entryPriority(entry({ message: "plain text" }))).toBeNull();
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

  it("strips the embedded timestamp that repeats the Time column", () => {
    const message = logMessage(
      entry({
        message:
          "2026/09/07 05:40:01.666987 from 128.71.34.88:37486 accepted udp:1.0.0.1:53 " +
          "[vless-reality -> direct] email: ss-book",
      }),
    );
    expect(message.text).toBe("from 128.71.34.88:37486 accepted udp:1.0.0.1:53 [vless-reality -> direct] email: ss-book");
  });

  it("strips an embedded timestamp and level marker alike", () => {
    const message = logMessage(
      entry({ message: "2026/09/07 05:40:07.427399 [Warning] app/proxyman/inbound: connection ends" }),
    );
    expect(message.text).toBe("app/proxyman/inbound: connection ends");
  });

  it("leaves a message without an embedded prefix untouched", () => {
    expect(logMessage(entry({ message: "xform listening addr=127.0.0.1:9090" })).text).toBe(
      "xform listening addr=127.0.0.1:9090",
    );
  });

  it("never strips a binary payload", () => {
    const message = logMessage(entry({ message: "3q2+7w==", message_encoding: "base64" }));
    expect(message.text).toBe("3q2+7w==");
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
