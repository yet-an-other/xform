# xform — panel specification

A read-only web panel monitoring a single xray-core server: host CPU/RAM/storage, xray service status (uptime, total traffic, overall speed), and per-user statistics (durable traffic totals, online status, online IPs, last seen, current speed estimate).

Source of truth for decisions: [map issue #1](https://github.com/yet-an-other/xform/issues/1) and its closed child tickets (#2–#5). Research: `docs/research/xray-stats-prometheus.md`, `docs/research/xray-grpc-go-client.md`. UI reference: `docs/prototypes/dashboard-wireframe.html`.

## 1. Architecture

```
┌─────────────┐   poll 5s    ┌──────────────────────────────────┐
│ React SPA   │ ◄──────────► │ Go backend (single binary:       │
│ (Vite + TS) │  JSON /api/* │  API + collector; serves the SPA │
└─────────────┘              │  embedded by default — ADR-0001) │
                             │  collector ──► xray gRPC API     │
                             │     │          (127.0.0.1,        │
                             │     │           StatsService only)│
                             │     ├──────► xray config.json     │
                             │     │          (parse + fsnotify) │
                             │     ├──────► systemd (go-systemd) │
                             │     ├──────► host stats (gopsutil)│
                             │     └──────► SQLite (modernc,     │
                             │                embedded)          │
                             └──────────────────────────────────┘
```

- **Single host**: the panel runs on the same server as xray-core.
- **Backend**: Go (≥ 1.26), single static binary: JSON API + collector. Consumes `github.com/xtls/xray-core/app/stats/command` as a library — pin `v1.YYMMDD.N` tags (release tags like `v26.x` are rejected by the Go proxy).
- **Dashboard hosting**: embedded in the binary by default; alternatively served as static files by the same-origin reverse proxy (see §7, ADR-0001). Never cross-origin.
- **Frontend**: React + TypeScript (Vite), pure API client, decoupled so the UI can grow (drill-down, editing) without touching the collector.
- **Read-only**: the panel never mutates xray. HandlerService stays disabled.

## 2. Prerequisites (xray side)

Enable stats, policy, and the API in the xray config:

```json
{
  "stats": {},
  "policy": {
    "levels": { "0": { "statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true } },
    "system": {
      "statsInboundUplink": true, "statsInboundDownlink": true,
      "statsOutboundUplink": true, "statsOutboundDownlink": true
    }
  },
  "api": { "tag": "api", "listen": "127.0.0.1:8080", "services": ["StatsService"] }
}
```

- Every client **must have an `email`** — per-user stats don't exist without it.
- The gRPC API has **no auth/TLS**; loopback binding + StatsService-only is the entire security model.
- xray-core ≥ **v26.4.13** recommended (`GetUsersStats`). Presence and the online counts are gated on `GetAllOnlineUsers` (**≥ v26.1.13**) — on older servers the collector tolerates `Unimplemented` and omits presence (degrade: no online status or IPs; `last_seen` falls back to the traffic-delta heuristic).
- Online tracking only counts **real client IPs** — xray ignores loopback sources in its online maps. If a userspace forwarder fronts xray, it must speak PROXY protocol (`acceptProxyProtocol` on the xray inbound) or presence stays empty.
- xray runs as a systemd unit; the panel reads unit state via D-Bus (needs to run on the same host, permission to query the system bus is sufficient for unit properties).

## 3. Collector

Loop every **5 seconds**:

1. **xray gRPC** (`grpc.NewClient` to `127.0.0.1:8080`, insecure creds): `GetUsersStats` / `QueryStats` (traffic), online IP lists (`last_seen` per IP), `GetSysStats` (uptime, goroutines, memory). Version: read from the xray binary (`xray version`) or sysstats when available.
2. **systemd** (`coreos/go-systemd/v22/dbus`): `ActiveState`, `SubState`, `ActiveEnterTimestamp` for the xray unit → status + service uptime.
3. **Host stats** (`shirou/gopsutil/v4`): `cpu.Percent`, `mem.VirtualMemory`, `disk.Usage("/")`, `host.Uptime`, load average.
4. **xray config parse** (fsnotify-triggered re-read): user roster (emails), per-user `protocol` / `security`.

### Counter reconciliation

xray counters reset on restart; panel totals are durable. Per user, per direction, per poll:

```
delta = raw >= last_raw ? raw - last_raw : raw
```

(`raw < last_raw` ⇒ counter restarted from 0, so `raw` itself is the delta.) Traffic between the last poll and a restart is unknowable — accepted. Speed = mean of the last 2 deltas ÷ interval. Any positive delta marks the user seen now — on xray without the online RPCs that heuristic is `last_seen`'s only source; a panel's first-seen baseline seed is not observed activity.

### Degraded behavior

- xray gRPC unreachable ⇒ `xray.status = "unreachable"`, speeds 0, users served from last-known SQLite snapshot with `stale: true`. Host stats stay live.
- xray unit inactive ⇒ `status: "stopped"`.
- Older xray without online RPCs ⇒ `online`/`ips` omitted, `last_seen` from the traffic-delta heuristic (any positive delta = seen now).

## 4. Data model

SQLite file (pure-Go `modernc.org/sqlite` — no cgo, static binary preserved). One transaction per poll flush.

```sql
CREATE TABLE users (
  email            TEXT PRIMARY KEY,        -- identity; email change = new row
  protocol         TEXT,                    -- from config parse
  security         TEXT,                    -- from config parse
  up_bytes_total   INTEGER NOT NULL DEFAULT 0,  -- durable
  down_bytes_total INTEGER NOT NULL DEFAULT 0,
  last_seen        INTEGER,                 -- unix seconds, NULL = never
  last_ips         TEXT,                    -- JSON array
  gone             INTEGER NOT NULL DEFAULT 0,  -- no longer in xray config; history kept
  first_seen       INTEGER NOT NULL
);
```

Users disappearing from the config get `gone = 1` — never auto-deleted; hidden by default in the UI.

## 5. HTTP API

Base prefix `/api/v1`. JSON only, snake_case keys, raw integers (bytes, bytes/sec, unix seconds) — formatting is the UI's job.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/api/v1/login` | — | `{"password": "..."}` → 204 + `Set-Cookie`; 401 on mismatch |
| POST | `/api/v1/logout` | session | clears cookie → 204 |
| GET | `/api/v1/healthz` | — | `{"status":"ok"}` |
| GET | `/api/v1/server` | session | host stats — always live |
| GET | `/api/v1/xray` | session | xray status/version/uptime/process/speeds/totals/online counts |
| GET | `/api/v1/panel` | session | panel identity — the ldflags-stamped release version |
| GET | `/api/v1/users` | session | `{"stale": bool, "collected_at": ts, "users": [...]}` |

**Auth**: password from env (`XFORM_PASSWORD`, constant-time compare); `xform_session` cookie — HttpOnly, SameSite=Lax, **`Secure` always** (browsers exempt `localhost`, so SSH-tunnel access still works; every other access path is TLS-terminated), 24h sliding expiry. All endpoints except `login`/`healthz` require it.

**Same-origin only**: the API emits **no CORS headers**. The dashboard is always served from the same origin as the API — embedded in the binary or reverse-proxied (ADR-0001).

**Payloads** (200 always, when the panel itself is up):

```json
GET /api/v1/server
{ "collected_at": 1723800000,
  "cpu_percent": 23.4, "cpu_cores": 4,
  "mem_used_bytes": 5100273664, "mem_total_bytes": 8589934592,
  "disk_path": "/", "disk_used_bytes": 90194313216, "disk_total_bytes": 171798691840,
  "uptime_seconds": 1987200, "load_avg": [0.42, 0.38, 0.31] }

GET /api/v1/xray
{ "collected_at": 1723800000,
  "status": "running",                  // running | stopped | unreachable
  "api_endpoint": "127.0.0.1:8080",     // configured XFORM_XRAY_API — named in the degraded banner
  "version": "26.4.13",                 // null when the unit is not active
  "uptime_seconds": 1216800,
  "mem_bytes": 88080384, "goroutines": 183,   // null unless running
  "speed_up_bps": 2400000, "speed_down_bps": 18500000,  // 0 when degraded
  "total_up_bytes": 39100000000, "total_down_bytes": 511400000000,  // durable; last-known when degraded
  "users_online": 3, "unique_ips_online": 4 }  // null on xray predating the online RPCs

GET /api/v1/users
{ "collected_at": 1723800000, "stale": false,
  "users": [ { "email": "alice@example.com",
               "protocol": "VLESS", "security": "XTLS-Reality",  // from the config parse; null until the config first parses
               "up_bytes_total": 12400000000, "down_bytes_total": 148200000000,
               "online": true, "ips": ["203.0.113.10"],          // false/null on xray predating the online RPCs
               "speed_up_bps": 512000, "speed_down_bps": 3800000,
               "last_seen": 1723799995,            // durable; null until first observed activity
               "gone": false } ] }

GET /api/v1/panel
{ "version": "v0.4.2" }   // the binary's release tag; "dev" outside releases
```

## 6. Frontend

React + TS (Vite), single page per the approved wireframe (`docs/prototypes/dashboard-wireframe.html`):

- **Header**: compact — `xform` wordmark, xray status pill, xray version pill, service uptime pill, and a refresh note with the last-successful-poll time (24h clock). Log-out button. Degraded banner when `status != "running"` — full copy naming what went stale and that host stats stay live.
- **Server row**: four cards — CPU / RAM / storage with bars, plus a host-uptime card with load average as its sub-line.
- **Xray row**: four cards — speed now (↑ green / ↓ blue, stacked big lines), total traffic (up + down), users online (`n / total` + unique IPs), xray process memory/goroutines.
- **Users table**: online dot, email, protocol · security, up/down (no total column — derivable), speed now, online IPs (one per line), last seen (relative; literal `now` while online). Compact row density. `gone` users hidden behind a toggle.

Polls all three endpoints every 5s; freshness is the header refresh note, not per-card; shows "stale" speeds when `stale: true`. Login page posting to `/api/v1/login`.

## 7. Deployment

Two same-origin deployment shapes (ADR-0001):

- **Embedded (default)**: one Go binary serving the built SPA (embedded via `embed`), the API, and the collector. Install = scp binary + systemd unit + env vars; no web server prerequisite.
- **Proxy-hosted**: nginx (or the host's existing reverse proxy) serves the built SPA as static files and reverse-proxies `/api/*` to the Go API on loopback. Reference config: `deploy/nginx.conf.example`. Gotcha: `proxy_pass` must carry **no URI part**, otherwise `/api/v1/*` is rewritten to `/v1/*`.
- **Subpath mounting**: the SPA is built mount-point agnostic (Vite `base: "./"`, relative API client), so either shape can hang under a subpath of an existing vhost (e.g. `/xform/`). The proxy strips the prefix — a deliberate `proxy_pass` URI rewrite, the one intentional exception to the gotcha above — and redirects the bare subpath to its trailing-slash form. Commented variants ship in both `deploy/` reference configs.

- Configuration via env: `XFORM_LISTEN` (default `127.0.0.1:9090`), `XFORM_PASSWORD` (required), `XFORM_XRAY_API` (default `127.0.0.1:8080`), `XFORM_XRAY_CONFIG` (default `/usr/local/etc/xray/config.json`), `XFORM_DB` (default `/var/lib/xform/xform.db`), `XFORM_XRAY_UNIT` (default `xray.service`).
- Ships with a systemd unit (`xform.service`, `After=xray.service`). TLS terminates at the reverse proxy in both shapes — the panel itself serves plain HTTP on loopback.

## 8. Non-goals (v1)

Historical traffic graphs & long retention · user management / config editing · per-user drill-down with connection string/QR (post-v1, UI is API-first to allow it) · multi-server · Prometheus/Grafana export · alerting/quotas/CSV · cross-origin or CDN-hosted UI (same-origin only, ADR-0001).
