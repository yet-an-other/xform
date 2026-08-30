// Presentation rules for one journal record (SPEC §6). The API
// normalizes the journal; this module decides only how a normalized record
// reads in the dialog.

import type { LogEntry } from "@/lib/api";

// TRUNCATED_MESSAGE is the approved wording for a record journalctl elided:
// the message is null because it exceeded the journal's field limit and never
// travelled, which is a different thing from an empty message.
export const TRUNCATED_MESSAGE = "[message exceeds journal field limit]";

// SYSLOG_LABELS index by priority, 0–7. Level 3 reads "error" rather than
// syslog's terser "err", matching the approved prototype's badge.
const SYSLOG_LABELS = ["emerg", "alert", "crit", "error", "warning", "notice", "info", "debug"];

// logSource renders the source column: identifier[pid] when the record
// carried both, and otherwise the unit (SPEC §6). A record with an identifier
// but no pid falls back too — half an identity is not the identity the
// column promises, and the unit is always known because the endpoint
// collected one fixed unit.
export function logSource(entry: LogEntry): string {
  if (entry.identifier === null || entry.pid === null) {
    return entry.unit;
  }
  return `${entry.identifier}[${entry.pid}]`;
}

export function priorityLabel(priority: number | null): string | null {
  if (priority === null) {
    return null;
  }
  return SYSLOG_LABELS[priority] ?? null;
}

export interface LogMessage {
  text: string;
  // binary is a base64 payload the panel deliberately does not decode: it is
  // shown marked rather than rendered as mojibake.
  binary: boolean;
  truncated: boolean;
}

export function logMessage(entry: LogEntry): LogMessage {
  if (entry.message_truncated && entry.message === null) {
    return { text: TRUNCATED_MESSAGE, binary: false, truncated: true };
  }
  return {
    text: entry.message ?? "",
    binary: entry.message_encoding === "base64",
    truncated: entry.message_truncated,
  };
}

// formatEntryTime renders a record's microsecond timestamp as UTC to the
// second. UTC, not local: a journal read on one host and shown in another
// timezone must still line up with what `journalctl` prints there.
export function formatEntryTime(timestampMicroseconds: number): string {
  return utcStamp(new Date(timestampMicroseconds / 1000));
}

// formatSnapshotTime renders the capture time, naming the zone because it is
// read on its own rather than in a column of like timestamps.
export function formatSnapshotTime(unixSeconds: number): string {
  return `${utcStamp(new Date(unixSeconds * 1000))} UTC`;
}

function utcStamp(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return (
    `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())} ` +
    `${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}:${pad(date.getUTCSeconds())}`
  );
}
