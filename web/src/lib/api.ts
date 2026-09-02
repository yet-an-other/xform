// Typed client for the panel's API (SPEC §5). One function per endpoint —
// the only module that knows URLs, headers, and payload shapes. URLs are
// relative so the dashboard works mounted under any subpath: the browser
// resolves them next to wherever the page was loaded from.

export interface HostStats {
  collected_at: number;
  cpu_percent: number;
  cpu_cores: number;
  mem_used_bytes: number;
  mem_total_bytes: number;
  disk_path: string;
  disk_used_bytes: number;
  disk_total_bytes: number;
  uptime_seconds: number;
  load_avg: [number, number, number];
}

// UnauthenticatedError means the session is absent or expired (401) — the
// app answers by showing the login page, not an error banner.
export class UnauthenticatedError extends Error {
  constructor() {
    super("unauthenticated");
    this.name = "UnauthenticatedError";
  }
}

// XrayStatus is the panel's view of the xray service (SPEC §5): systemd
// unit state + binary version, plus runtime stats from the gRPC
// StatsService. Process and online fields are null unless running (online
// counts also null on servers predating the online RPCs).
export interface XrayStatus {
  collected_at: number;
  status: "running" | "stopped" | "unreachable";
  api_endpoint: string; // the configured gRPC address, named in the degraded banner
  version: string | null;
  uptime_seconds: number;
  mem_bytes: number | null;
  goroutines: number | null;
  speed_up_bps: number;
  speed_down_bps: number;
  total_up_bytes: number;
  total_down_bytes: number;
  users_online: number | null;
  unique_ips_online: number | null;
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    cache: "no-store",
    headers: { Accept: "application/json" },
    signal,
  });
  if (response.status === 401) {
    throw new UnauthenticatedError();
  }
  if (!response.ok) {
    throw new Error(`panel returned ${response.status}`);
  }
  return (await response.json()) as T;
}

export function fetchServerStats(signal?: AbortSignal): Promise<HostStats> {
  return getJSON<HostStats>("api/v1/server", signal);
}

export function fetchXrayStatus(signal?: AbortSignal): Promise<XrayStatus> {
  return getJSON<XrayStatus>("api/v1/xray", signal);
}

// User is one row of the users table (SPEC §5). Presence fields (online,
// ips, last_seen) are live from the online RPCs — omitted on servers
// predating them; config fields (protocol, security, gone) come from the
// config roster sync and stay zero until the xray config parses. Client ID
// and inbounds are the roster store's adopted record — null until adoption.
// apply_state is the write-side mark (user-management spec §6): pending
// while a change applies, failed when the last apply failed; absent once
// applied.
export interface User {
  email: string;
  protocol: string | null;
  security: string | null;
  client_id: string | null;
  inbounds: string[] | null;
  apply_state?: ApplyState;
  up_bytes_total: number;
  down_bytes_total: number;
  online: boolean;
  ips: string[] | null;
  // ISO alpha-2 per online IP (ADR-0005); absent when geoip.dat is
  // unavailable on the host — or for private/reserved IPs.
  ip_countries?: Record<string, string>;
  speed_up_bps: number;
  speed_down_bps: number;
  last_seen: number | null;
  gone: boolean;
}

// RosterSync is the write-side state of the roster (CONTEXT.md): synced when
// store, config file, and running xray agree; pending while a stored change
// applies; failed when the last apply failed (retries continue).
export type RosterSync = "synced" | "pending" | "failed";

// ApplyState is one user's write-side mark.
export type ApplyState = "pending" | "failed";

// InboundOption is one attachable inbound in the add dialog's multi-select:
// the tag, plus the protocol · security · transport :port label.
export interface InboundOption {
  tag: string;
  label: string;
}

// UsersSnapshot is GET /api/v1/users: durable per-user traffic plus a stale
// flag — true when xray is unreachable and the data is last-known — and the
// roster write side: the sync state and the add dialog's inbound options.
export interface UsersSnapshot {
  collected_at: number;
  stale: boolean;
  users: User[];
  roster_sync: RosterSync;
  inbounds: InboundOption[];
}

export function fetchUsers(signal?: AbortSignal): Promise<UsersSnapshot> {
  return getJSON<UsersSnapshot>("api/v1/users", signal);
}

// RosterUser is the stored roster record a mutation returns.
export interface RosterUser {
  email: string;
  client_id: string;
  inbounds: string[];
  created_at: number;
  updated_at: number;
}

// MutationResult is POST and PATCH /api/v1/users: the stored record plus
// the Roster sync state once the first apply settled (or the settle window
// elapsed).
export interface MutationResult {
  user: RosterUser;
  roster_sync: RosterSync;
}

// ConflictError is a rejected mutation carrying the API's machine-readable
// reason (email_taken / client_id_taken / unknown_inbound / *_invalid).
export class ConflictError extends Error {
  readonly reason: string;

  constructor(reason: string) {
    super(reason);
    this.name = "ConflictError";
    this.reason = reason;
  }
}

// UserNotFoundError is an edit naming an email the roster no longer
// carries — the row was removed out from under the dialog.
export class UserNotFoundError extends Error {
  constructor() {
    super("not_found");
    this.name = "UserNotFoundError";
  }
}

// mutation fetches one mutation verb and maps the panel's answers onto the
// dialog-facing errors: 401 expires the session, 409 carries the conflict
// reason, 404 is a gone record, anything else is unreachable.
async function mutate(method: string, path: string, body: unknown): Promise<MutationResult> {
  const response = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body),
  });
  if (response.status === 401) {
    throw new UnauthenticatedError();
  }
  if (response.status === 409) {
    const reason = await response
      .json()
      .then((parsed: unknown) =>
        typeof parsed === "object" && parsed !== null && typeof (parsed as { error?: unknown }).error === "string"
          ? (parsed as { error: string }).error
          : null,
      )
      .catch(() => null);
    throw new ConflictError(reason ?? "conflict");
  }
  if (response.status === 404) {
    throw new UserNotFoundError();
  }
  if (!response.ok) {
    throw new Error(`panel returned ${response.status}`);
  }
  return (await response.json()) as MutationResult;
}

// addUser stores a new roster user; apply proceeds from there and the
// returned sync state says how the first apply went.
export function addUser(
  email: string,
  clientId: string,
  inbounds: string[],
): Promise<MutationResult> {
  return mutate("POST", "api/v1/users", { email, client_id: clientId, inbounds });
}

// editUser stores one roster edit — the inbound set always sent whole, the
// Client ID only when the dialog has one to send (user-management spec §5).
export function editUser(
  email: string,
  clientId: string | null,
  inbounds: string[],
): Promise<MutationResult> {
  const body: Record<string, unknown> = { inbounds };
  if (clientId !== null) {
    body.client_id = clientId;
  }
  return mutate("PATCH", `api/v1/users/${encodeURIComponent(email)}`, body);
}

// removeUser removes one roster user (user-management spec §5): idempotent,
// always 204 — gone already or just removed alike. Resolves with the Roster
// sync state so the confirm dialog can tell applied from still-retrying.
export async function removeUser(email: string): Promise<RosterSync> {
  const response = await fetch(`api/v1/users/${encodeURIComponent(email)}`, {
    method: "DELETE",
    headers: { Accept: "application/json" },
  });
  if (response.status === 401) {
    throw new UnauthenticatedError();
  }
  if (!response.ok) {
    throw new Error(`panel returned ${response.status}`);
  }
  const body = (await response.json().catch(() => null)) as { roster_sync?: RosterSync } | null;
  return body?.roster_sync ?? "synced";
}

export type ConnectionProfileState =
  | "ready"
  | "gone_user"
  | "no_matching_inbound"
  | "source_unavailable";

export interface ConnectionProfileSourceError {
  source: "xray_config" | "advertisements";
  reason: "read_failed" | "parse_failed" | "unsupported_version";
  message: string;
}

// The typed public transport and security values the URI was built from
// (SPEC §7). Every field beyond `type` is omitted when it does not
// apply — except a REALITY short ID, which is present even when empty,
// because an explicitly empty `sid` is meaningful to the client.
export interface ConnectionTransport {
  type: "tcp" | "ws" | "httpupgrade" | "grpc" | "xhttp";
  path?: string;
  host?: string;
  service_name?: string;
  mode?: string;
  authority?: string;
  extra?: unknown;
}

export interface ConnectionSecurity {
  type: "tls" | "reality";
  fingerprint?: string;
  server_name?: string;
  alpn?: string[];
  ech?: string;
  certificate_pins?: string[];
  verify_name?: string;
  public_key?: string;
  short_id?: string;
  post_quantum_verify?: string;
  spider_x?: string;
}

export interface AvailableConnectionProfile {
  status: "available";
  inbound_tag: string;
  name: string;
  topology: "direct" | "fronted";
  client_id: string;
  flow: string | null;
  endpoint: { host: string; port: number };
  transport: ConnectionTransport;
  security: ConnectionSecurity;
  uri: string;
}

export type ConnectionProfileUnavailableReason =
  | "source_unavailable"
  | "advertisement_missing"
  | "advertisement_invalid"
  | "duplicate_inbound_tag"
  | "duplicate_user"
  | "inbound_tag_missing"
  | "reverse_user"
  | "unsupported_transport"
  | "unsupported_security"
  | "unsupported_encryption"
  | "insecure_connection"
  | "inbound_mismatch"
  | "invalid_client_id";

export interface UnavailableConnectionProfile {
  status: "unavailable";
  inbound_tag: string | null;
  name: string | null;
  reason: ConnectionProfileUnavailableReason;
  message: string;
}

export type ConnectionProfile = AvailableConnectionProfile | UnavailableConnectionProfile;

export interface UserDetail {
  collected_at: number;
  stale: boolean;
  user: User;
  connection_profiles: {
    state: ConnectionProfileState;
    loaded_at: number | null;
    stale: boolean;
    errors: ConnectionProfileSourceError[];
    items: ConnectionProfile[];
  };
}

export function fetchUserDetail(email: string, signal?: AbortSignal): Promise<UserDetail> {
  return getJSON<UserDetail>(`api/v1/users/${encodeURIComponent(email)}`, signal);
}

// PanelInfo is the panel's own identity (SPEC §5): the release
// version stamped into the binary at build time plus the current process
// uptime in whole seconds. Fetched every poll — the dashboard refreshes
// uptime from the API in the five-second cycle instead of extrapolating it
// in the browser.
export interface PanelInfo {
  version: string;
  uptime_seconds: number;
}

export function fetchPanelInfo(signal?: AbortSignal): Promise<PanelInfo> {
  return getJSON<PanelInfo>("api/v1/panel", signal);
}

// login posts the password; true on 204, false on a 401 mismatch.
export async function login(password: string): Promise<boolean> {
  const response = await fetch("api/v1/login", {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ password }),
  });
  if (response.status === 204) {
    return true;
  }
  if (response.status === 401) {
    return false;
  }
  throw new Error(`server returned ${response.status}`);
}

export async function logout(): Promise<void> {
  await fetch("api/v1/logout", { method: "POST" });
}

// LogEntry is one normalized journal record (SPEC §8). identifier,
// pid, priority, message, and message_encoding are null where the record
// carried no usable value; message_truncated marks journalctl's own
// oversized-field elision, where the message is null because it never
// travelled, not because it was empty.
export interface LogEntry {
  cursor: string;
  timestamp_us: number;
  unit: string;
  identifier: string | null;
  pid: number | null;
  priority: number | null;
  message: string | null;
  message_encoding: string | null;
  message_truncated: boolean;
}

// LogSnapshot is GET /api/v1/logs/{source}: one bounded, newest-first,
// point-in-time read — never a live stream. entry_count is what was actually
// collected; limit is the ceiling it was collected under.
export interface LogSnapshot {
  captured_at: number;
  source: LogSource;
  unit: string;
  limit: number;
  entry_count: number;
  entries: LogEntry[];
}

export type LogSource = "panel" | "xray";

// ConfigSnapshot is GET /api/v1/xray/config: the exact text observed during
// one bounded read, never parsed or reformatted. path is the configured path
// string, not a resolved symlink target.
export interface ConfigSnapshot {
  captured_at: number;
  path: string;
  size_bytes: number;
  text: string;
}

// SnapshotUnavailableError carries the stable reason the API reported, which
// is the only failure detail the viewers show: journalctl's stderr, journal
// messages, and file content are the data these snapshots exist to bound.
export class SnapshotUnavailableError extends Error {
  readonly reason: string;

  constructor(reason: string) {
    super(reason);
    this.name = "SnapshotUnavailableError";
    this.reason = reason;
  }
}

// snapshotFailureReason is the only failure detail the viewers show: the
// API's own stable reason. journalctl's stderr, journal messages, and file
// content are the data these snapshots exist to bound, and none of them
// reaches the browser to be shown.
export function snapshotFailureReason(cause: unknown): string {
  if (cause instanceof SnapshotUnavailableError) {
    return cause.reason;
  }
  return cause instanceof Error ? cause.message : "unavailable";
}

// getSnapshot is getJSON plus the stable-reason failure body of SPEC §8. A
// body without one still fails — with the status, so the dialog
// says something true rather than inventing a reason.
async function getSnapshot<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    cache: "no-store",
    headers: { Accept: "application/json" },
    signal,
  });
  if (response.status === 401) {
    throw new UnauthenticatedError();
  }
  if (!response.ok) {
    const reason = await response
      .json()
      .then((body: unknown) =>
        typeof body === "object" && body !== null && typeof (body as { reason?: unknown }).reason === "string"
          ? (body as { reason: string }).reason
          : null,
      )
      .catch(() => null);
    throw reason !== null
      ? new SnapshotUnavailableError(reason)
      : new Error(`panel returned ${response.status}`);
  }
  return (await response.json()) as T;
}

export function fetchLogSnapshot(source: LogSource, signal?: AbortSignal): Promise<LogSnapshot> {
  return getSnapshot<LogSnapshot>(`api/v1/logs/${source}`, signal);
}

export function fetchConfigSnapshot(signal?: AbortSignal): Promise<ConfigSnapshot> {
  return getSnapshot<ConfigSnapshot>("api/v1/xray/config", signal);
}
