// Display formatting for the dashboard. SPEC §5: the API serves raw integers
// (bytes, seconds) — formatting is the UI's job.

export function formatBytes(bytes: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = value >= 100 || unit === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(digits)} ${units[unit]}`;
}

export function formatSpeed(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`;
}

// formatAgo renders a unix-seconds timestamp as relative time ("just now",
// "5m ago", …) for the users table's last-seen column.
export function formatAgo(unixSeconds: number, now: number = Date.now()): string {
  const elapsed = Math.max(0, Math.floor((now - unixSeconds * 1000) / 1000));
  if (elapsed < 10) {
    return "just now";
  }
  if (elapsed < 60) {
    return `${elapsed}s ago`;
  }
  if (elapsed < 3600) {
    return `${Math.floor(elapsed / 60)}m ago`;
  }
  if (elapsed < 86_400) {
    return `${Math.floor(elapsed / 3600)}h ago`;
  }
  return `${Math.floor(elapsed / 86_400)}d ago`;
}

export function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86_400);
  if (days > 0) {
    return `${days} ${days === 1 ? "day" : "days"}`;
  }
  const hours = Math.floor(seconds / 3_600);
  if (hours > 0) {
    return `${hours} ${hours === 1 ? "hour" : "hours"}`;
  }
  const minutes = Math.floor(seconds / 60);
  return `${minutes} ${minutes === 1 ? "minute" : "minutes"}`;
}

export function percentUsed(used: number, total: number): number {
  if (total <= 0) {
    return 0;
  }
  return Math.min(100, Math.max(0, (used / total) * 100));
}

// formatTime24 renders the refresh note's last-updated time on a 24h clock.
export function formatTime24(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}
