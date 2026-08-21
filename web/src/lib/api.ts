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
// ips, last_seen) stay zero until the presence slice; config fields
// (protocol, security, gone) until the config-parse slice.
export interface User {
  email: string;
  protocol: string | null;
  security: string | null;
  up_bytes_total: number;
  down_bytes_total: number;
  online: boolean;
  ips: string[] | null;
  speed_up_bps: number;
  speed_down_bps: number;
  last_seen: number | null;
  gone: boolean;
}

// UsersSnapshot is GET /api/v1/users: durable per-user traffic plus a stale
// flag — true when xray is unreachable and the data is last-known.
export interface UsersSnapshot {
  collected_at: number;
  stale: boolean;
  users: User[];
}

export function fetchUsers(signal?: AbortSignal): Promise<UsersSnapshot> {
  return getJSON<UsersSnapshot>("api/v1/users", signal);
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
