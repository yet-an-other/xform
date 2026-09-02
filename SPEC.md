# xform — panel specification

A read-only web panel monitoring a single xray-core server: host CPU/RAM/storage, xray service status (uptime, total traffic, overall speed), and per-user statistics (durable traffic totals, online status, online IPs, last seen, current speed estimate). Each user can be inspected in a details dialog with current observations and VLESS connection profiles; the header opens bounded, manually refreshed snapshots of the panel and xray journals and of the exact xray config file.

Normative words: **SHALL** marks required behavior, **SHALL NOT** marks prohibited behavior, **MAY** marks an allowed choice that does not affect compatibility. Canonical domain terms come from [`CONTEXT.md`](CONTEXT.md).

Source of truth for decisions: [map issue #1](https://github.com/yet-an-other/xform/issues/1) with child tickets #2–#5, and [map issue #18](https://github.com/yet-an-other/xform/issues/18) with child tickets #19–#39. Decision history: `docs/research/` (xray stats, gRPC client, VLESS share-link contract, bounded journald access) and `docs/prototypes/` (approved dashboard, user-details, and operational-viewers layouts).

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
                             │     ├──────► connections config   │
                             │     │          (parse + fsnotify) │
                             │     ├──────► systemd (go-systemd) │
                             │     ├──────► host stats (gopsutil)│
                             │     ├──────► SQLite (modernc,     │
                             │     │          embedded)          │
                             │     └──────► journalctl (exec,    │
                             │                per request only)  │
                             └──────────────────────────────────┘
```

- **Single host**: the panel runs on the same server as xray-core.
- **Backend**: Go (≥ 1.26), single static binary: JSON API + collector. Consumes `github.com/xtls/xray-core/app/stats/command` as a library — pin `v1.YYMMDD.N` tags (release tags like `v26.x` are rejected by the Go proxy).
- **Dashboard hosting**: embedded in the binary by default; alternatively served as static files by the same-origin reverse proxy (see §9, ADR-0001). Never cross-origin.
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
- Log snapshots additionally require Linux with systemd 245 or newer, `journalctl`, ACL support, and tmpfiles support (§8, §9).

## 3. Collector

Loop every **5 seconds**:

1. **xray gRPC** (`grpc.NewClient` to `127.0.0.1:8080`, insecure creds): `GetUsersStats` / `QueryStats` (traffic), online IP lists (`last_seen` per IP), `GetSysStats` (uptime, goroutines, memory). Version: read from the xray binary (`xray version`) or sysstats when available.
2. **systemd** (`coreos/go-systemd/v22/dbus`): `ActiveState`, `SubState`, `ActiveEnterTimestamp` for the xray unit → status + service uptime.
3. **Host stats** (`shirou/gopsutil/v4`): `cpu.Percent`, `mem.VirtualMemory`, `disk.Usage("/")`, `host.Uptime`, load average.
4. **xray config parse** (fsnotify-triggered re-read): user roster (emails), per-user `protocol` / `security`, VLESS client attachments (Client ID + inbound tags) adopted into the roster store (§4), and the parsed inbound view behind connection profiles (§7).

### Watched sources

The xray config and the advertised connection settings (§7) are watched sources: re-read whenever the file changes, keeping the last valid parse. A failed re-read never empties a watched source: it keeps the last valid value and marks it stale, with a safe source error (no filesystem detail, no config text, no server secret). A successful update replaces the prior snapshot.

The xray config parse retains enough data to produce profile candidates for every matching VLESS inbound, not just the roster: inbound tag, protocol, inbound-level flow and decryption, transport and security settings needed for validation, REALITY accepted names and short IDs, and each user's email, configured client ID, flow, and reverse status.

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

The roster store holds one row per adopted user — the panel-held source of truth behind user management:

```sql
CREATE TABLE roster (
  email      TEXT PRIMARY KEY,        -- identity; email change = new row
  client_id  TEXT NOT NULL,           -- UUID credential
  inbounds   TEXT NOT NULL,           -- JSON array of VLESS inbound tags
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
```

On startup and whenever the config changes, VLESS clients found in the config are adopted into the roster, additively and idempotently: a new email lands with its config Client ID and attachments, a known email only gains attachments it did not have (a stored Client ID is never rewritten by a config edit), and an unchanged re-read writes nothing.

Connection profiles and operational snapshots are never persisted (§7, §8).

## 5. HTTP API

Base prefix `/api/v1`. JSON only, snake_case keys, raw integers (bytes, bytes/sec, unix seconds) — formatting is the UI's job. HTTP handlers translate module results into these contracts; they do not parse xray config, serialize VLESS URIs, invoke journalctl, or normalize journal entries themselves.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/api/v1/login` | — | `{"password": "..."}` → 204 + `Set-Cookie`; 401 on mismatch |
| POST | `/api/v1/logout` | session | clears cookie → 204 |
| GET | `/api/v1/healthz` | — | `{"status":"ok"}` |
| GET | `/api/v1/server` | session | host stats — always live |
| GET | `/api/v1/xray` | session | xray status/version/uptime/process/speeds/totals/online counts |
| GET | `/api/v1/panel` | session | panel identity — release version + process uptime |
| GET | `/api/v1/users` | session | `{"stale": bool, "collected_at": ts, "users": [...]}` |
| GET | `/api/v1/users/{email}` | session | one user's observations + connection profiles (§7) |
| GET | `/api/v1/logs/panel` | session | bounded Log snapshot of the panel's journal (§8) |
| GET | `/api/v1/logs/xray` | session | bounded Log snapshot of xray's journal (§8) |
| GET | `/api/v1/xray/config` | session | exact Config snapshot of the configured xray file (§8) |

**Auth**: password from env (`XFORM_PASSWORD`, constant-time compare); `xform_session` cookie — HttpOnly, SameSite=Lax, **`Secure` always** (browsers exempt `localhost`, so SSH-tunnel access still works; every other access path is TLS-terminated), path `/`, 24h sliding expiry. All endpoints except `login`/`healthz` require it. A session expires 24h after last use and never survives a panel restart.

**Same-origin only**: the API emits **no CORS headers**. The dashboard is always served from the same origin as the API — embedded in the binary or reverse-proxied (ADR-0001). Every session endpoint returns `Cache-Control: no-store`.

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
               "client_id": "1e7f6c2a-9b3d-4f8a-9c1e-2d5a7b8c9d0e", "inbounds": ["vless-reality"],  // the roster store's adopted record; null until adoption
               "up_bytes_total": 12400000000, "down_bytes_total": 148200000000,
               "online": true, "ips": ["203.0.113.10"],          // false/null on xray predating the online RPCs
               "ip_countries": {"203.0.113.10": "NL"},          // ADR-0005; omitted when geoip.dat is unavailable, absent keys = private/unknown
               "speed_up_bps": 512000, "speed_down_bps": 3800000,
               "last_seen": 1723799995,            // durable; null until first observed activity
               "gone": false } ] }

GET /api/v1/panel
{ "version": "v0.10.0",     // the binary's release tag; "dev" outside releases
  "uptime_seconds": 4831 }  // whole seconds since this panel process started (monotonic); resets on restart
```

The dashboard fetches `/api/v1/panel` in the five-second refresh cycle rather than extrapolating uptime in the browser.

### User detail

`GET /api/v1/users/{email}` returns one user's current observations and connection-profile evaluation:

```json
{
  "collected_at": 1723800000,
  "stale": false,
  "user": { "email": "alice@example.com", "...": "the GET /api/v1/users user contract, nullability and omission rules unchanged" },
  "connection_profiles": {
    "state": "ready",
    "loaded_at": 1723800000,
    "stale": false,
    "errors": [],
    "items": []
  }
}
```

- The caller encodes the email with `encodeURIComponent` as one URL path segment; the handler decodes it exactly once. Malformed percent encoding → 400 `invalid_request`; valid encoded `/`, `%`, and non-ASCII bytes remain part of the email identity. The route works through both root and documented subpath proxy deployments (§9).
- A known user → 200 even when profile generation failed. An unknown user → 404. Profile errors never become endpoint-level 500s; a 500 is reserved for an internal failure that prevents any detail response.
- `collected_at` and top-level `stale` describe user observations. `connection_profiles.loaded_at`, `stale`, and `errors` describe the parsed-xray and advertised-connection sources used for profile evaluation (§7): `loaded_at` is the oldest last-success time among the valid source snapshots used (the parsed-xray time when advertisements are unset or never loaded; null only when xray config has never parsed successfully), `stale` is true when either source serves its last-valid snapshot after a reload failure, and `errors` lists `xray_config` then `advertisements` source errors, each `{source, reason, message}` with `reason` ∈ `read_failed | parse_failed | unsupported_version` and a safe human-readable `message`.
- `state` is `ready` (candidates evaluated), `gone_user` (gone user; `items` empty), `no_matching_inbound`, or `source_unavailable` (xray config never parsed; `loaded_at` null, `items` empty).
- `items` holds one available or unavailable result per matching VLESS inbound, in xray inbound order (§7).

## 6. Frontend

React + TS (Vite), single page per the approved prototypes in `docs/prototypes/` (user details dialog, operational viewers). Dashboard modules consume typed HTTP responses and own one-modal-at-a-time state, detail polling, manual Log snapshot refresh, copy actions, QR rendering, focus management, and browser-local retention after a failed manual refresh; SQLite stays outside these modules.

- **Header**: two identity groups. Panel group: `xform` wordmark, version, panel uptime, panel-logs icon action. xray group: status indicator immediately before `xray`, version, service uptime, xray-logs icon action, xray-config icon action. Then the refresh note (cadence + last-successful-poll time, 24h clock) and Log out. Every icon-only action has an accessible name and a visible tooltip or title. Degraded banner when `status != "running"` — full copy naming what went stale and that host stats stay live.
- **Server row**: four cards — CPU / RAM / storage with bars, plus a host-uptime card with load average as its sub-line.
- **Xray row**: four cards — speed now (↑ green / ↓ blue, stacked big lines), total traffic (up + down), users online (`n / total` + unique IPs), xray process memory/goroutines.
- **Users table**: online dot, email, protocol · security, Traffic (up/down stacked on two lines), speed now, online IPs (one per line, country flag beside each — ADR-0005), last seen (relative; literal `now` while online), and per-user icon-only actions with accessible names: details, and — for users who are not gone — edit and remove. The edit action opens the edit dialog with the user's email (immutable — the identity; change = remove + add), the inbound multi-select, and an editable Client ID with a generate button; saving stores the edit and applies it live (store → file render → diff push: attach/detach per inbound, remove + add on every attached inbound when the Client ID changes), showing conflicts inline and apply failures on the dialog banner + row badge (`docs/user-management-spec.md`). The remove action opens a confirmation naming what removal means — off every inbound immediately, an xray restart keeps them gone, traffic history retained behind the gone badge, established connections left to close naturally; DELETE is idempotent (`docs/user-management-spec.md`). Compact row density. `gone` users hidden behind a toggle. At narrow widths the table keeps its fixed columns and scrolls horizontally.

Polls all three observation endpoints every 5s; freshness is the header refresh note, not per-card; shows "stale" speeds when `stale: true`. Login page posting to `/api/v1/login`.

### User details dialog

Opening a user's details action opens the one modal dialog for that exact user, showing in order: email and online/gone status; current Traffic, Speed, Last seen, and online IP observations; then connection profiles. While open it fetches the user detail every five seconds; detail requests never overlap (a slow request skips the next interval), only the newest completed request for the currently open user may update the modal, and closing cancels its request and timer. The ordinary dashboard polling continues behind the modal.

- **Available profiles** render as fully expanded cards in xray inbound order: profile name, inbound tag, client ID with Copy, flow (`none` when null), public endpoint, transport and security, full VLESS URI with Copy, and a QR code generated from the exact URI. Stale last-valid profiles stay visible and copyable with a clear stale warning and the source error.
- **Unavailable results** show the profile name and inbound tag when known, plus the stable reason and readable message — never a client ID copy action, partial URI, or QR code.
- A **gone user** shows the historical-observations view and no profiles. `no_matching_inbound` and `source_unavailable` have distinct empty/error copy.
- Observation staleness and profile staleness display independently.

### Operational dialogs

Only one modal is open at a time. Each viewer reports only its own collection result: a failed viewer says so on its own, without making any other viewer or the dashboard look broken. A stopped or unreachable xray does not disable the xray logs or config viewers.

- **Panel logs** and **xray logs** dialogs: one fresh Log snapshot on open; a dense newest-first table with UTC timestamp, source (`identifier[pid]`, falling back to the unit), syslog priority badge, and message; actual entry count, capture time, and `Bounded · manual refresh`; one Refresh action; the standing notes `No Panel redaction` and `No live tail`. Base64 messages carry a visible binary marker; a truncated null message renders `[message exceeds journal field limit]`. Refresh is disabled while its request runs. If refresh fails after a successful load, the dialog keeps the displayed entries and original timestamp and shows `Refresh failed, showing snapshot from …` with the stable reason; an initial failure shows only the error state.
- **Config snapshot dialog**: one fresh Config snapshot on open; the configured path and the exact text, preserving every character including final newlines; horizontal fixed-format scrolling at narrow widths; Copy, and no Refresh action. A failed Config snapshot shows the stable reason and no Copy action.
- Log and Config snapshot data live only for the current modal opening: closing either modal aborts its request and clears the browser-local snapshot, and reopening always starts with an initial load.

### Modal and session behavior

A modal traps focus, closes on Escape or its close action, and restores focus to its opener. At 560 CSS pixels or narrower, fixed-format content scrolls horizontally rather than reflowing metadata. A 401 from any modal request closes the modal and returns the dashboard to the login flow; other modal failures stay inside that modal and never trigger dashboard degraded mode.

## 7. Connection profiles

A connection profile is a client-ready VLESS connection for one user through one matching xray inbound, identified by `(user email, inbound tag)`. The same email in several uniquely tagged VLESS inbounds produces several profiles; the same email repeated within one inbound produces one unavailable result for that inbound (the client ID is ambiguous).

A profile is derived from two sources: server-derived values from the matching VLESS inbound (canonical client ID, effective flow), and advertised connection settings describing the public client view. The panel never infers a complete public client view from an xray listener — NAT, TLS termination, reverse proxies, CDNs, and REALITY selections make that inference unsafe. Profiles are never persisted. A gone user retains historical observations and has no connection profiles.

The profile module owns matching by exact email, stable identity checks, canonical client ID conversion, effective-flow selection, direct and fronted validation, supported-shape validation, unavailable reason selection, and canonical URI serialization. Callers never build or patch VLESS URIs.

### Advertised connection settings

`XFORM_CONNECTIONS_CONFIG` (optional, no default) names an xform-owned JSON document; leaving it unset never stops the panel — each matching VLESS inbound that needs an advertisement returns `advertisement_missing`. The root shape:

```json
{
  "version": 1,
  "advertisements": [
    {
      "inbound_tag": "vless-reality-main",
      "name": "Primary",
      "topology": "direct",
      "host": "edge.example.com",
      "port": 443,
      "transport": { "type": "tcp" },
      "security": {
        "type": "reality",
        "fingerprint": "chrome",
        "server_name": "www.microsoft.com",
        "public_key": "...",
        "short_id": "..."
      }
    }
  ]
}
```

- The root rejects unknown fields, duplicate object keys at every depth, trailing JSON values, and unsupported versions. Malformed root JSON or an unsupported version rejects the new snapshot as a whole; after at least one successful load the panel retains the last valid snapshot, marks profile data stale, and exposes a safe current source error. With no valid snapshot ever, matching inbounds remain identifiable from parsed xray config and report `source_unavailable` items.
- One advertisement selects one inbound by its unique, non-empty tag and applies to every matching user in it. Per-user advertised settings are not supported. Records are validated independently: an invalid record makes only its selected inbound unavailable; two records selecting one tag make that tag unavailable with `duplicate_inbound_tag`; an advertisement referencing no current inbound produces a bounded server-side warning and does not affect unrelated users.
- Record fields: `inbound_tag` (required, non-empty), `name` (optional, non-empty; defaults to the tag), `topology` (required; `direct` or `fronted`), `host` (required public domain/IPv4/IPv6 host without scheme or path), `port` (1–65535), `transport`, `security`. Unknown advertisement, transport, or security fields make that advertisement invalid.
- Supported transports: `tcp` (no extra fields); `ws` / `httpupgrade` (required non-empty `path`, `host`); `grpc` (required non-empty `service_name`; `mode` defaulting to `gun`, accepting `gun`/`multi`/`guna`; optional `authority`); `xhttp` (required non-empty `path`, `host`, `mode` ∈ `auto`/`packet-up`/`stream-up`/`stream-one`; optional `extra` object acceptable to RFC 8785 canonicalization).
- Supported securities: `tls` and `reality`. `fingerprint` defaults to `chrome`, non-empty when present (no closed enum). TLS `server_name` defaults to the advertised host; `alpn` and `certificate_pins` are arrays of non-empty strings; `ech`, `verify_name` are optional. REALITY requires non-empty `server_name` and `public_key`; `short_id` is required and may be empty only when the matching inbound accepts an empty short ID; `post_quantum_verify`, `spider_x` are optional. Empty optional strings normalize to omission — an explicitly present empty REALITY `short_id` is the only empty string with distinct meaning. `{ "type": "none" }` is recognized only so it can return `insecure_connection`; it never produces an available profile. The panel never derives REALITY public values from server private material.
- `direct` means the advertised transport and security must satisfy the xray inbound: normalize transport aliases and documented defaults before comparison; advertised WS/HTTPUpgrade paths and hosts, gRPC service selections, XHTTP paths/hosts/modes, and REALITY server names and short IDs must be values the inbound accepts. REALITY public-key syntax is validated, but server public material is never derived for comparison. With `encryption=none`, `xtls-rprx-vision` requires advertised TCP with TLS or REALITY.
- `fronted` means a frontend may change the public transport or security view: the panel validates the URI, client ID, flow, and supported field combinations, and does not claim to verify the frontend route.
- The file is watched for changes (§3).

### Result items

An available item exposes `status`, `inbound_tag`, `name`, `topology`, `client_id` (canonical lowercase UUID), `flow` (null when none applies), `endpoint` (`host`, `port`), the full typed `transport` and `security` objects used to build the URI, and `uri`. An unavailable item exposes `status`, `inbound_tag` and `name` (nullable when the failure prevents them from being known), `reason`, and `message` — and carries no partial URI or QR payload.

Stable unavailable reasons: `source_unavailable` (candidates exist but a configured advertisement source has no valid snapshot), `advertisement_missing`, `advertisement_invalid`, `duplicate_inbound_tag` (xray config repeats a tag, or advertisements select one tag more than once), `duplicate_user`, `inbound_tag_missing`, `reverse_user`, `unsupported_transport`, `unsupported_security`, `unsupported_encryption`, `insecure_connection` (`security=none&encryption=none`), `inbound_mismatch` (a supported direct advertisement does not satisfy the matching inbound after normalization, or the advertised shape is incompatible with the user's effective flow), `invalid_client_id`. When several failures apply, the primary reason is the first applicable in this precedence order; secondary failures may appear only in the message:

```text
inbound_tag_missing → duplicate_inbound_tag → duplicate_user → reverse_user
→ source_unavailable → advertisement_missing → advertisement_invalid
→ invalid_client_id → unsupported_transport → unsupported_security
→ unsupported_encryption → insecure_connection → inbound_mismatch
```

A duplicate xray inbound tag produces one unavailable item at each affected inbound position; duplicate advertisement records for one otherwise unique tag produce one unavailable item for that matching inbound.

### Canonical VLESS URI

```text
vless://<canonical-uuid>@<advertised-host>:<advertised-port>?<query>#<email · name-or-tag>
```

1. Convert a custom xray client ID using xray's canonical UUID mapping; emit the lowercase UUID.
2. Use the user-level flow when non-empty, otherwise the inbound-level flow. Never infer the client-only `-udp443` suffix.
3. Emit `encryption=none`; never copy server `decryption` into the client URI.
4. Canonicalize domains with the Unicode UTS #46 non-transitional lookup profile, then lowercase the ASCII result; reject empty labels and a trailing dot. Canonicalize IPv6 literals with brackets; reject zone identifiers.
5. Use ECMAScript `encodeURIComponent` escaping for query values and the fragment; never form-query `+` encoding.
6. Common fields in order: `type`, `encryption`, `flow`, `security`. `type`, `encryption=none`, and `security` are always emitted; `flow` only when non-empty.
7. Transport fields in schema order — WS/HTTPUpgrade: `path`, `host`; gRPC: `serviceName`, `mode`, `authority`; XHTTP: `path`, `host`, `mode`, `extra`.
8. Security fields in order — TLS: `fp`, `sni`, `alpn`, `ech`, `pcs`, `vcn`; REALITY: `fp`, `sni`, `pbk`, `sid`, `pqv`, `spx`.
9. Emit effective `fp=chrome`; omit other empty optional fields except an explicitly empty REALITY `sid`.
10. Join ALPN values and certificate pins with commas before percent-encoding.
11. Serialize XHTTP `extra` with RFC 8785 JSON Canonicalization Scheme before percent-encoding.
12. The fragment is `<email> · <advertisement name or inbound tag>`.

The URI string is the single source for display, copy, and QR generation; the QR payload is the exact UTF-8 bytes of that string — no Base64 wrapper, whitespace, alternate label, or reserialization.

### Compatibility scope

Serialization is pinned to [XTLS/Xray-core discussion 716](https://github.com/XTLS/Xray-core/discussions/716), xray-core commit `f02a35786124a6ad046727f2408e32317cc19a41`, and Xray docs commit `090e425873072704d2a631740a4129ce8013c0eb`. Unknown future or client-specific fields never enter the generated URI automatically. Supported: RAW/TCP, WebSocket, HTTPUpgrade, gRPC, XHTTP, TLS, REALITY, VLESS `encryption=none`. Unsupported: mKCP, FinalMask, RAW header obfuscation, Hysteria, removed HTTP and QUIC transports, non-`none` VLESS encryption, and `security=none&encryption=none`.

## 8. Operational snapshots

An operational snapshot is a point-in-time answer to an admin's explicit request: a **Log snapshot** (latest bounded journal entries for one fixed unit — best-effort, not a live stream or an atomic journal transaction) or a **Config snapshot** (the exact UTF-8 text observed during one bounded read of `XFORM_XRAY_CONFIG`). Neither is persisted by the panel; neither is part of the panel's own history (ADR-0006). The Config snapshot is separate from the parsed roster: it may show malformed JSON while the roster and profiles continue using their last valid parse.

Freshness and errors stay independent across every source: user observation freshness, xray-config parse freshness, advertised-connection freshness, each Log snapshot's success, Config snapshot success, and xray's running/stopped/unreachable status. A failure in one source is never borrowed as another source's status.

### Log snapshots

`GET /api/v1/logs/panel` and `GET /api/v1/logs/xray` each collect the latest 500 records for their one fixed unit, newest first. They accept no query parameter — any parameter → 400 `invalid_request`.

The production adapter executes the equivalent of:

```text
/usr/bin/journalctl
  --system --namespace=xform --unit=<fixed-canonical-unit>
  --lines=500 --reverse --output=json
  --output-fields=__CURSOR,__REALTIME_TIMESTAMP,_SYSTEMD_UNIT,UNIT,OBJECT_SYSTEMD_UNIT,COREDUMP_UNIT,SYSLOG_IDENTIFIER,_PID,PRIORITY,MESSAGE
  --no-pager
```

Separate arguments, no shell, an attached `--unit=<value>`, and a deterministic environment (`LC_ALL=C`, `LANG=C`, `SYSTEMD_COLORS=0`) — never an inherited pager or shell environment. The journal module owns canonical unit selection, the fixed arguments, process timeout/cancellation/concurrency, output limits, newline-delimited JSON decoding, field normalization, and stable errors; its interface accepts no unit, count, filter, cursor, time range, or raw journalctl argument. Process execution sits behind an internal seam with a production adapter and a fake adapter for tests.

Bounds: 5-second process timeout · 500 entries · 8 MiB stdout · 64 KiB stderr · 1 journalctl process globally. Request cancellation kills and reaps the child; a client-disconnect cancellation has no HTTP response requirement. The reader decodes a stream of JSON objects without an unbounded scanner or combined output buffer, rejects more than 500 objects, and never passes `--all`.

Success response:

```json
{
  "captured_at": 1723800000,
  "source": "panel",
  "unit": "xform.service",
  "limit": 500,
  "entry_count": 1,
  "entries": [
    {
      "cursor": "...",
      "timestamp_us": 1723800000123456,
      "unit": "xform.service",
      "identifier": "xform",
      "pid": 1427,
      "priority": 6,
      "message": "Panel started",
      "message_encoding": "utf-8",
      "message_truncated": false
    }
  ]
}
```

`captured_at` is recorded after the child exits and every object is validated and normalized. Every entry always includes the `message`, `message_encoding`, and `message_truncated` keys. Normalization:

- `__CURSOR` must be a non-empty scalar string; `__REALTIME_TIMESTAMP` a scalar unsigned decimal microsecond string fitting `uint64`.
- Trusted unit fields, when present, are scalar strings — an array, object, or number makes the snapshot malformed. `unit` derives from the first non-empty of `_SYSTEMD_UNIT`, `UNIT`, `OBJECT_SYSTEMD_UNIT`, `COREDUMP_UNIT`, falling back to the endpoint's fixed unit.
- `SYSLOG_IDENTIFIER` → `identifier`; absent/null/repeated/other → `null`. Scalar decimal `_PID` fitting a non-negative integer → `pid`, else `null`. Scalar decimal `PRIORITY` 0–7 → `priority`, else `null`.
- A normal UTF-8 `MESSAGE` passes unchanged (`utf-8`, not truncated). Repeated string values join with a newline. A numeric byte array (integers 0–255) Base64-encodes (`base64`, not truncated). Journalctl `null` → `message: null, message_encoding: null, message_truncated: true`. A missing `MESSAGE` → empty UTF-8 string, not truncated. Any other form makes the snapshot malformed.

The whole snapshot is rejected if an object lacks a valid cursor or timestamp, contains invalid trusted-field types, cannot be decoded, or breaches a count or byte bound — never skip entries and still claim a complete snapshot. A successful command with no entries returns 200 and an empty list.

Failure classification (the response body is `{"error": "log snapshot unavailable", "reason": "<reason>"}`; journalctl stderr is never returned):

| Condition | Reason | HTTP |
| --- | --- | --- |
| Global reader already occupied | `snapshot_in_progress` | 429 + `Retry-After: 1` |
| Validated executable later missing, invalid, non-executable, or unable to start | `journalctl_unavailable` | 503 |
| Child starts but fixed-locale stderr reports denial while opening journal data | `access_denied` | 503 |
| Five-second deadline expires | `timeout` | 503 |
| stdout or stderr exceeds its cap | `output_too_large` | 503 |
| JSON, entry count, cursor, timestamp, trusted field, or message form invalid | `malformed_output` | 503 |
| Child exits non-zero for any other reason | `command_failed` | 503 |

The panel may record a bounded error summary, but never copies journal messages into its diagnostic error.

### Journal reader configuration

`XFORM_JOURNALCTL` (optional, default `/usr/bin/journalctl`) must, at startup, be absolute and — after following a root-configured symlink — resolve to a regular file executable by the panel user; initial validation failure stops startup. If the validated executable later disappears, becomes invalid, or loses execute permission, Log snapshot requests report `journalctl_unavailable` without stopping ordinary monitoring. At startup the panel also rejects shorthand or globbed xray unit names, resolves `XFORM_XRAY_UNIT` through systemd to its canonical service `Id` (permitting canonical instances such as `xray@edge.service`), and rejects an identity that cannot be resolved unambiguously. Missing namespace files, missing ACLs, an empty namespace, or later reader failures never stop ordinary monitoring.

### Config snapshot

`GET /api/v1/xray/config` accepts no query parameter (any → 400 `invalid_request`). It opens `XFORM_XRAY_CONFIG` (following a root-configured symlink), requires the opened target to be a regular file, reads at most 4 MiB plus one detection byte, requires valid UTF-8, and never parses or formats the content. Filesystem access and the clock sit behind internal seams shared by production and test adapters. Success:

```json
{ "captured_at": 1723800000, "path": "/usr/local/etc/xray/config.json", "size_bytes": 4812, "text": "{\n  \"inbounds\": []\n}\n" }
```

`path` is the configured path string, not the resolved symlink target; `size_bytes` is the number of bytes actually read; `captured_at` is recorded after regular-file, size, and UTF-8 validation succeeds. A malformed but readable JSON document is returned unchanged. Failures are 503 with `{"error": "config snapshot unavailable", "reason": "<reason>"}` and reason `config_unreadable`, `config_too_large`, or `config_not_utf8`.

## 9. Deployment

Two same-origin deployment shapes (ADR-0001):

- **Embedded (default)**: one Go binary serving the built SPA (embedded via `embed`), the API, and the collector. Install = scp binary + systemd unit + env vars; no web server prerequisite.
- **Proxy-hosted**: nginx (or the host's existing reverse proxy) serves the built SPA as static files and reverse-proxies `/api/*` to the Go API on loopback. Reference config: `deploy/nginx.conf.example`. Gotcha: `proxy_pass` must carry **no URI part**, otherwise `/api/v1/*` is rewritten to `/v1/*`.
- **Subpath mounting**: the SPA is built mount-point agnostic (Vite `base: "./"`, relative API client), so either shape can hang under a subpath of an existing vhost (e.g. `/xform/`). The proxy strips the prefix — a deliberate `proxy_pass` URI rewrite, the one intentional exception to the gotcha above — and redirects the bare subpath to its trailing-slash form. Encoded user identity survives the trip: one path-segment decode at the handler, no encoded byte becomes a route separator. Commented variants ship in both `deploy/` reference configs.

Configuration via env: `XFORM_LISTEN` (default `127.0.0.1:9090`), `XFORM_PASSWORD` (required), `XFORM_XRAY_API` (default `127.0.0.1:8080`), `XFORM_XRAY_CONFIG` (default `/usr/local/etc/xray/config.json`), `XFORM_DB` (default `/var/lib/xform/xform.db`), `XFORM_XRAY_UNIT` (default `xray.service`), `XFORM_GEOIP` (geoip.dat for the country flags; default searches `/usr/local/share/xray/` and the xray config's directory — flags silently off when not found), `XFORM_CONNECTIONS_CONFIG` (optional, no default — §7), `XFORM_JOURNALCTL` (default `/usr/bin/journalctl` — §8).

Ships with a systemd unit (`xform.service`, `After=xray.service`). TLS terminates at the reverse proxy in both shapes — the panel itself serves plain HTTP on loopback.

### Journal namespace

Log viewing uses one fixed journal namespace named `xform`: `LogNamespace=xform` on `xform.service` and on the configured xray service through an installed drop-in. The `xform` user receives inherited read ACLs only on `/var/log/journal/<machine-id>.xform/` and `/run/log/journal/<machine-id>.xform/` — never membership of `systemd-journal`, `adm`, `wheel`, or another broad journal-reading group. Assigning another unit to the `xform` namespace exposes that unit's logs to the panel; keep the namespace to exactly these two units.

Ships: `deploy/xform.service`, `deploy/xray-journal-namespace.conf.example`, and `deploy/xform-journal-acl.conf` (tmpfiles + ACL rules for persistent and volatile namespace paths). Installation, migration, and the executable verification procedure — namespace creation, initial records from both units, ACL inheritance after rotation and reboot, negative access checks against the default namespace and unrelated units — live in [`docs/journal-namespace.md`](docs/journal-namespace.md). The migration is one-time and manual: the binary updater never installs or mutates root-owned unit, drop-in, tmpfiles, or ACL files, and release notes name this.

Existing monitoring continues when `XFORM_CONNECTIONS_CONFIG` is unset or the journal namespace migration has not run: user detail still opens (matching profile candidates report `advertisement_missing`), Log snapshot endpoints report their own stable deployment or access error, host/xray/user monitoring continues, and the Config snapshot works whenever the configured file's permissions allow it.

## 10. Non-goals

Historical traffic graphs & long retention · user-management mutations (add/edit/remove applied to xray) · free-form config editing · subscription URLs · client-specific profile formats or import guarantees · non-VLESS profiles · per-user advertised connection settings · profiles for gone users · profile/credential persistence, masking, or audit trails · multi-server · Prometheus/Grafana export · alerting/quotas/CSV · cross-origin or CDN-hosted UI (same-origin only, ADR-0001) · live log following, pagination, search, filtering, or downloads · caller-selected units, counts, cursors, time ranges, fields, or journal expressions · log clearing or service controls · xray-managed log-file reading · Config snapshot editing, validation, formatting, download, or reload controls · historical Log/Config snapshot storage · migration of old default-journal records · broad default-journal access or a privileged journal broker · non-systemd hosts or systemd older than 245 · automatic installation of root-owned migration files by the binary updater · multiple simultaneous modals · changes to the five-second observation cadence · a new frontend framework, HTTP router, database, or persistent store.
