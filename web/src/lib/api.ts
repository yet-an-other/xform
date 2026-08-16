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

export async function fetchServerStats(signal?: AbortSignal): Promise<HostStats> {
  const response = await fetch("api/v1/server", {
    cache: "no-store",
    headers: { Accept: "application/json" },
    signal,
  });
  if (response.status === 401) {
    throw new UnauthenticatedError();
  }
  if (!response.ok) {
    throw new Error(`server returned ${response.status}`);
  }
  return (await response.json()) as HostStats;
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
