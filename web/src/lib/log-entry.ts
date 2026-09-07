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

// EMBEDDED_PREFIX matches the stamp xray puts on every log line: an optional
// `YYYY/MM/DD HH:MM:SS[.fraction]` timestamp and an optional `[Level]`
// marker — e.g. `2026/09/07 05:40:01.666987 [Warning] …`, or an access line
// with the stamp alone. xray writes its whole log to stdout, so journald
// stamps every record info and the embedded marker is the only severity the
// record carries; the stamp itself repeats the Time column. One parser
// serves every unit alike: records without the prefix (the panel's own)
// pass through untouched.
const EMBEDDED_PREFIX =
  /^(?:\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?\s+)?(?:\[(Debug|Info|Warning|Error)\]\s+)?/;

// EMBEDDED_PRIORITIES maps the embedded level words onto syslog priorities,
// so a parsed severity drives the badge and its tone exactly like a
// journald-carried one.
const EMBEDDED_PRIORITIES: Record<string, number> = {
  Error: 3,
  Warning: 4,
  Info: 6,
  Debug: 7,
};

// parseEmbeddedPrefix lifts the embedded stamp off a message, returning the
// remaining text and the severity the marker named (null when there was no
// marker). A message opening with bracketed non-level text —
// `[vless-reality -> direct]` — is left alone.
function parseEmbeddedPrefix(text: string): { text: string; priority: number | null } {
  const match = EMBEDDED_PREFIX.exec(text);
  if (match === null) {
    return { text, priority: null };
  }
  const level = match[1];
  return {
    text: text.slice(match[0].length),
    priority: level === undefined ? null : (EMBEDDED_PRIORITIES[level] ?? null),
  };
}

// entryPriority is the record's effective priority: the severity embedded in
// the message wins where a unit stamps one, because the journal priority of
// a stdout-only unit is uniformly info and says nothing. Records without an
// embedded marker keep the journald priority. Binary payloads are never
// parsed — base64 text cannot carry a readable prefix.
export function entryPriority(entry: LogEntry): number | null {
  if (entry.message !== null && entry.message_encoding !== "base64") {
    const embedded = parseEmbeddedPrefix(entry.message).priority;
    if (embedded !== null) {
      return embedded;
    }
  }
  return entry.priority;
}

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
  const binary = entry.message_encoding === "base64";
  const text = entry.message ?? "";
  return {
    // The embedded stamp duplicates the Time and Priority columns, so the
    // message shows only what the columns do not.
    text: binary ? text : parseEmbeddedPrefix(text).text,
    binary,
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
