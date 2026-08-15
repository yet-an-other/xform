# Exporting xray-core statistics to Prometheus/Grafana — research findings

Date: 2026-08-15. Primary sources: official Xray docs (xtls.github.io), Xray-core source (github.com/XTLS/Xray-core, inspected at main `7d214f8`), exporter project READMEs/source, GitHub API metadata.

## TL;DR — recommendation

There is **no native Prometheus-format endpoint in xray-core**. The native `metrics` config block serves **expvar JSON + pprof over HTTP only** (the Prometheus part was dropped from the originating PR before merge). The practical, best-supported architecture is:

1. Enable `stats` + `policy` + `api` (StatsService gRPC on `127.0.0.1`) in xray-core.
2. Run **[compassvpn/xray-exporter](https://github.com/compassvpn/xray-exporter)** (actively maintained, Docker images, published Grafana dashboard 23181): it polls the gRPC StatsService for per-user/per-inbound/per-outbound traffic and runtime stats (goroutines, memory, uptime), and optionally parses the access log for user activity (unique users, connections, top destinations, GeoIP).
3. **Latency to targets**: enable `observatory`/`burstObservatory` and read per-outbound `alive`/`delay` either from the native `metrics` expvar endpoint (`/debug/vars`) or the gRPC `ObservatoryService`; for probing *remote* xray proxies from the outside, use [kutovoys/xray-checker](https://github.com/kutovoys/xray-checker) (~876 stars, embeds xray-core as a client, native Prometheus output).
4. **Last seen / user activity**: xray tracks per-user online status and per-IP `last_seen` (requires `statsUserOnline: true` in policy, recent core), but entries vanish when the user disconnects — so either let Prometheus retain the time-series history, or use compassvpn/xray-exporter's access-log parsing for durable activity data.

Everything else (writing a tiny custom exporter against `QueryStats`/`GetSysStats`) is trivial given the proto, but the projects above already cover it.

---

## 1. What xray-core exposes natively

### 1.1 StatsService gRPC API

Enable with three config blocks ([stats docs](https://xtls.github.io/en/config/stats.html), [policy docs](https://xtls.github.io/en/config/policy.html), [api docs](https://xtls.github.io/en/config/api.html)):

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

- `"stats": {}` alone enables the stats feature (no parameters) — [stats docs](https://xtls.github.io/en/config/stats.html).
- Policy toggles decide what is collected: per-level `statsUserUplink`/`statsUserDownlink`/`statsUserOnline`, system-wide `statsInbound*/statsOutbound*` — [policy docs](https://xtls.github.io/en/config/policy.html).
- `api.listen` (simplified mode, since v1.8.12) binds the gRPC API directly; in simplified mode API-inbound traffic is not counted in stats — [api docs](https://xtls.github.io/en/config/api.html).

**Traffic counters** (names per [stats docs](https://xtls.github.io/en/config/stats.html)):

- `user>>>[email]>>>traffic>>>uplink` / `downlink` (bytes; **user stats require the client to have an `email`** — "If the corresponding user does not specify an Email, statistics will not be enabled")
- `inbound>>>[tag]>>>traffic>>>uplink` / `downlink`
- `outbound>>>[tag]>>>traffic>>>uplink` / `downlink`
- `user>>>[email]>>>online` (online user/IP count; created in the dispatcher as `"user>>>" + email + ">>>online"` when `p.Stats.UserOnline` is set — [app/dispatcher/default.go L182–226](https://github.com/XTLS/Xray-core/blob/main/app/dispatcher/default.go))

**gRPC methods** ([app/stats/command/command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto)):

| RPC | Returns |
|---|---|
| `GetStats(name, reset)` | single counter; `reset=true` zeroes it after read |
| `QueryStats(pattern, reset)` | all counters whose name contains `pattern` |
| `GetStatsOnline(name)` | online count for a stat name |
| `GetStatsOnlineIpList(name)` | map of online IPs → last-seen Unix timestamp |
| `GetAllOnlineUsers()` | list of currently online user emails |
| `GetUsersStats(include_traffic, reset)` | per-user: email, online IPs with `last_seen`, uplink/downlink |
| `GetSysStats()` | runtime stats (below) |

**System stats** — `SysStatsResponse` fields ([command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto)): `NumGoroutine`, `NumGC`, `Alloc`, `TotalAlloc`, `Sys`, `Mallocs`, `Frees`, `LiveObjects`, `PauseTotalNs`, `Uptime` (seconds; computed from the gRPC server's `startTime` — [app/stats/command/command.go L190–193](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go)).

Feature timeline (merged PRs): user online stats [PR #3637](https://github.com/XTLS/Xray-core/pull/3637) (2024-11-03); per-IP access times first released in **v25.3.6** ([release notes](https://github.com/XTLS/Xray-core/releases/tag/v25.3.6), PR #4360); `GetAllOnlineUsers` [PR #5080](https://github.com/XTLS/Xray-core/pull/5080) (2025-12-26); `GetUsersStats` [PR #5776](https://github.com/XTLS/Xray-core/pull/5776) (2026-04-11, i.e. v26.4.x+). Use a recent core for the online/last-seen RPCs.

The official CLI wraps these: `xray api stats / statsquery / statssys / statsonline / statsonlineiplist / getallonlineusers` ([command docs](https://xtls.github.io/en/document/command.html), [main/commands/all/api/](https://github.com/XTLS/Xray-core/tree/main/main/commands/all/api), [traffic-stats tutorial](https://xtls.github.io/en/document/level-2/traffic_stats.html)).

### 1.2 Native Prometheus endpoint? — No (expvar/pprof only)

xray-core **does** have a top-level `metrics` config object (unlike plain v2fly-era assumptions), but it is **not** Prometheus text format:

```json
{ "metrics": { "tag": "Metrics", "listen": "127.0.0.1:11111" } }
```

- It serves only `/debug/vars` (expvar JSON) and `/debug/pprof/*` — the HTTP mux registers exactly those paths ([app/metrics/metrics.go `httpHandler()`](https://github.com/XTLS/Xray-core/blob/main/app/metrics/metrics.go)).
- `/debug/vars` includes `stats` (inbound/outbound/user traffic), `observatory` results, plus standard Go expvars (`memstats`, `cmdline`) — [metrics docs](https://xtls.github.io/en/config/metrics.html).
- The originating PR was initially titled "metrics including pprof, expvars **and prometheus**" and the Prometheus part was removed before merge (maintainer: the core is "very cautious on adding more external libraries") — [PR #1000, merged 2022-03-29](https://github.com/XTLS/Xray-core/pull/1000).
- Note: the expvar `stats` output contains traffic counters only; the handler splits counter names on `>>>` and skips entries with fewer than 4 parts, so `user>>>email>>>online` counts are **not** in expvar output — online data requires the gRPC API ([app/metrics/metrics.go `stats()`](https://github.com/XTLS/Xray-core/blob/main/app/metrics/metrics.go)).
- Docs suggest Netdata's `python.d/go_expvar` collector for visualizing `/debug/vars` — [metrics docs](https://xtls.github.io/en/config/metrics.html). For Prometheus, an expvar→Prometheus bridge or the gRPC exporters below are needed.

### 1.3 Observatory / BurstObservatory (latency/connectivity)

- `observatory` probes matched outbounds via HTTPing on a fixed `probeInterval`; `burstObservatory` randomizes probes within `interval * sampling` cycles to reduce fingerprinting; both produce per-outbound results used by the load balancer — [observatory docs](https://xtls.github.io/en/config/observatory.html).
- Result fields per outbound: `alive`, `delay` (probe RTT in ms), `last_error_reason`, `outbound_tag`, `last_seen_time`, `last_try_time` ([app/observatory/config.proto `OutboundStatus`](https://github.com/XTLS/Xray-core/blob/main/app/observatory/config.proto)).
- Exposure paths:
  - Native `metrics` expvar endpoint includes the observatory map (example output in [metrics docs](https://xtls.github.io/en/config/metrics.html)).
  - gRPC `ObservatoryService.GetOutboundStatus` ([app/observatory/command/command.proto](https://github.com/XTLS/Xray-core/blob/main/app/observatory/command/command.proto)); `"ObservatoryService"` is a valid `api.services` entry in the config parser ([infra/conf/api.go L38–39](https://github.com/XTLS/Xray-core/blob/main/infra/conf/api.go)) even though the [api docs page](https://xtls.github.io/en/config/api.html) doesn't list it.

### 1.4 What xray does NOT track

- **No persistent "last login"**. Online tracking is a refcounted in-memory map: `AddIP` on connect, `RemoveIP` deletes the entry at zero references ([app/stats/online_map.go](https://github.com/XTLS/Xray-core/blob/main/app/stats/online_map.go)). `last_seen` per IP exists only while the user is online, and `GetUsersStats` only returns users with active online IPs ([app/stats/command/command.go `GetUsersStats`](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go)). A durable last-seen must be derived externally (Prometheus retains vanished series — `max_over_time(...)` — or parse the access log).
- **No per-user latency**, no request history, no destinations — only byte counters and online maps. Docs define "online" as connection activity within 20 seconds ([policy docs](https://xtls.github.io/en/config/policy.html), `statsUserOnline`).
- **No persistence at all**: counters are in-memory `atomic.Int64` ([app/stats/counter.go](https://github.com/XTLS/Xray-core/blob/main/app/stats/counter.go)) — everything resets on restart.

---

## 2. Existing exporter approaches

### gRPC StatsService → Prometheus exporters

- **[compassvpn/xray-exporter](https://github.com/compassvpn/xray-exporter)** — 18 stars, created 2025-07, last push 2026-08-15 (actively maintained; fork lineage from wi1dcard). Polls StatsService for runtime + traffic metrics and **also parses the xray access log** for user-activity metrics (unique users, connections, top domains/IPs/ASNs/countries/cities via GeoLite2, per-outbound request mix). Docker multi-arch images on GHCR; published Grafana dashboard **23181**. Per-user traffic metrics are opt-in (`--user-traffic-metrics`, "one series per user"). Metric examples: `xray_traffic_uplink_bytes_total{dimension="inbound|outbound|user",target="..."}`, `xray_traffic_downlink_bytes_total{...}`, `xray_uptime_seconds`, `xray_goroutines`, `xray_memstats_alloc_bytes`, `xray_memstats_pause_total_ns`, `xray_unique_users`, `xray_total_connections`, `xray_outbound_requests{outbound=...}`, `xray_up`. ([README](https://github.com/compassvpn/xray-exporter))
- **[anatolykopyl/xray-exporter](https://github.com/anatolykopyl/xray-exporter)** — 16 stars, last push 2026-01-30. Same gRPC-polling design (runtime + traffic only, no log parsing), Docker Hub multi-arch images, ships `dashboard.json` and grafana.com dashboard **23145**, k8s manifests. Same `xray_*` metric naming ([README](https://github.com/anatolykopyl/xray-exporter)).
- **[wi1dcard/v2ray-exporter](https://github.com/wi1dcard/v2ray-exporter)** — 143 stars, the original (created 2020), last push 2024-03; effectively unmaintained. The two projects above descend from it.
- **[AlchemyLink/xray-stats-exporter](https://github.com/AlchemyLink/xray-stats-exporter)** — 0 stars, v1.0.2 (2026-06); per-user/per-inbound traffic via StatsService plus geo/TSPU-DPI-detection extras; too new to assess ([libraries.io listing](https://libraries.io/go/github.com%2Falchemylink%2Fxray-stats-exporter)).
- **[kutovoys/xray-checker](https://github.com/kutovoys/xray-checker)** — 876 stars, last push 2026-07. Different angle: embeds xray-core as a *client* and probes proxy servers (VLESS/VMess/Trojan/Shadowsocks URLs, subscriptions), exporting native Prometheus latency/availability metrics and Uptime Kuma endpoints. Best fit for "latency to targets / is my proxy alive" from a monitoring host ([project announcement in Xray-core discussion #4316](https://github.com/XTLS/Xray-core/discussions/4316)).
- **[batonogov/xray-health-exporter](https://github.com/batonogov/xray-health-exporter)** — 5 stars, active 2026-08; similar health-checking approach, embeds xray-core, native JSON configs.

### Panel ecosystems

- **[MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui)** (44.7k stars) has **no native Prometheus endpoint**; per-client traffic lives in its own SQLite DB. External exporters poll its panel API: **[hteppl/3x-ui-exporter](https://github.com/hteppl/3x-ui-exporter)** (50 stars, last push 2025-09) exposes `x_ui_total_online_users`, `x_ui_client_up_bytes`/`x_ui_client_down_bytes{email=...}`, version/uptime info, with BasicAuth option and Docker images; smaller alternatives: [zbndev/3x-ui-prometheus-exporter](https://github.com/zbndev/3x-ui-prometheus-exporter), [br3d/3x-ui-exporter](https://github.com/br3d/3x-ui-exporter). Only relevant if you actually run 3x-ui.
- **XrayR**: no Prometheus support surfaced in primary sources; nothing to recommend here.

### Custom exporter pattern

The official API client surface is the proto itself — `QueryStats(pattern:"", reset:false)` + `GetSysStats` + (new) `GetUsersStats` cover everything a small exporter needs ([command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto)); `xray api statsquery --server=127.0.0.1:PORT` is the shell-level equivalent ([traffic-stats tutorial](https://xtls.github.io/en/document/level-2/traffic_stats.html)). The XTLS docs point to community [Xray-API-documents](https://github.com/XTLS/Xray-API-documents) for call examples ([api docs](https://xtls.github.io/en/config/api.html)).

---

## 3. Recommended architecture

**Server-side (the xray host):**

1. xray config: `stats:{}`, full `policy` toggles (incl. `statsUserOnline: true`), `api` with `services:["StatsService"]` (+`"ObservatoryService"` if you want probe results over gRPC), `listen: "127.0.0.1:PORT"`. Optionally the `metrics` block for expvar/pprof debugging.
2. **compassvpn/xray-exporter** on the same host (binary or `ghcr.io/compassvpn/xray-exporter`), flags `--xray-endpoint 127.0.0.1:PORT --log-path /var/log/xray/access.log [--user-traffic-metrics]`. Exposes `/scrape` on `:9550`.
3. Prometheus scrapes the exporter → Grafana imports dashboard **23181** (compassvpn) or **23145** (anatolykopyl).

**What lands where:**

| Goal | Source | Metric/data |
|---|---|---|
| Per-user traffic | StatsService (`user>>>email>>>traffic>>>`) | `xray_traffic_uplink_bytes_total{dimension="user",target="email"}` / `..._downlink_...` |
| Inbound/outbound traffic | StatsService | same metric, `dimension="inbound"|"outbound"` |
| System perf (goroutines, mem, GC, uptime) | `GetSysStats` | `xray_goroutines`, `xray_memstats_alloc_bytes`, `xray_memstats_sys_bytes`, `xray_memstats_num_gc`, `xray_memstats_pause_total_ns`, `xray_uptime_seconds` |
| Online users / last-seen | `statsUserOnline` + `GetUsersStats`/`GetStatsOnlineIpList`; access-log parsing for durable activity | `xray_unique_users`, `xray_total_connections` (compassvpn log metrics); per-IP `last_seen` via gRPC — persist via Prometheus retention |
| Latency to targets | `observatory`/`burstObservatory` → expvar `/debug/vars` or `ObservatoryService`; external probing via kutovoys/xray-checker | observatory `delay` (ms) per outbound; xray-checker's native Prometheus metrics |
| Deep profiling (heap/goroutine dumps) | `metrics` block `/debug/pprof/` | ad-hoc debugging, not Prometheus |

**If 3x-ui operates the core**: prefer its ecosystem exporter (hteppl/3x-ui-exporter) which reads the panel's DB/API and gives per-client totals that survive core restarts — something the raw StatsService cannot do.

---

## 4. Comparison of exporter options

| Project | Stars | Last push | What it exports | Last-seen/activity | Docker | Grafana dashboard | Notes |
|---|---|---|---|---|---|---|---|
| [compassvpn/xray-exporter](https://github.com/compassvpn/xray-exporter) | 18 | 2026-08 | traffic (in/out/user), runtime, log-derived activity/GeoIP | via access-log parsing | GHCR multi-arch | 23181 | most complete; actively maintained |
| [anatolykopyl/xray-exporter](https://github.com/anatolykopyl/xray-exporter) | 16 | 2026-01 | traffic, runtime | no | Docker Hub multi-arch | 23145 | simpler; k8s manifests |
| [wi1dcard/v2ray-exporter](https://github.com/wi1dcard/v2ray-exporter) | 143 | 2024-03 | traffic, runtime | no | yes | bundled JSON | original; unmaintained |
| [AlchemyLink/xray-stats-exporter](https://github.com/AlchemyLink/xray-stats-exporter) | 0 | 2026-06 | traffic, geo, DPI-detection | no | ? | ? | very new, unproven |
| [kutovoys/xray-checker](https://github.com/kutovoys/xray-checker) | 876 | 2026-07 | proxy availability + latency (client-side probing) | n/a | yes | referenced in repo | complementary, not a StatsService exporter |
| [batonogov/xray-health-exporter](https://github.com/batonogov/xray-health-exporter) | 5 | 2026-08 | tunnel health checks (embeds core) | n/a | yes | ? | niche |
| [hteppl/3x-ui-exporter](https://github.com/hteppl/3x-ui-exporter) | 50 | 2025-09 | 3x-ui panel: per-client bytes, online users, version | online users | Docker Hub + BasicAuth | ? | only for 3x-ui deployments |
| Custom Go exporter vs `command.proto` | — | — | anything in StatsService/ObservatoryService | you implement | — | — | proto is small and stable (`xray:api:stable` markers in [features/stats/stats.go](https://github.com/XTLS/Xray-core/blob/main/features/stats/stats.go)) |

---

## 5. Caveats (all sourced)

- **Stats reset on restart; in-memory only.** Counters are `atomic.Int64` in process memory ([app/stats/counter.go](https://github.com/XTLS/Xray-core/blob/main/app/stats/counter.go)); there is no persistence layer. Prometheus-side `increase()` handles resets, but billing-grade totals need an external store (panels like 3x-ui do this in SQLite).
- **`reset=true` foot-gun.** `GetStats`/`QueryStats`/`GetUsersStats` accept a `reset` flag that zeroes counters when read ([command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto)); exporters use `reset=false`, but a second consumer using reset will corrupt everyone else's rates.
- **API port security.** The gRPC API has no authentication; official examples bind `127.0.0.1` ([api docs](https://xtls.github.io/en/config/api.html)). compassvpn README: if exporter and xray are on different machines, "listen on `0.0.0.0` instead of `127.0.0.1` and firewall the port, since anyone who can reach it can read your stats" ([README](https://github.com/compassvpn/xray-exporter)). `HandlerService` on the same port can add/remove users and inbounds — enable only `StatsService` ([api docs](https://xtls.github.io/en/config/api.html)).
- **Simplified `api.listen` mode doesn't count API-inbound traffic** in stats ([api docs](https://xtls.github.io/en/config/api.html), since v1.8.12).
- **User stats need emails** on clients, or per-user counters never appear ([stats docs](https://xtls.github.io/en/config/stats.html)).
- **Online data is recent-core-only and ephemeral**: `statsUserOnline` (merged 2024-11, [PR #3637](https://github.com/XTLS/Xray-core/pull/3637)); per-IP last-seen in v25.3.6+ ([release notes](https://github.com/XTLS/Xray-core/releases/tag/v25.3.6)); `GetUsersStats` only in v26.4.x+ ([PR #5776](https://github.com/XTLS/Xray-core/pull/5776)). Entries disappear when the user disconnects (refcounted map, [online_map.go](https://github.com/XTLS/Xray-core/blob/main/app/stats/online_map.go)).
- **Observatory probing creates fingerprintable traffic** — fixed-interval probes "might lead to behavioral fingerprinting"; burstObservatory exists to mitigate this ([observatory docs](https://xtls.github.io/en/config/observatory.html)).
- **Per-user metric cardinality**: compassvpn disables per-user traffic series by default because "it is one series per user" ([README](https://github.com/compassvpn/xray-exporter)).
- No sourced statement was found quantifying the performance cost of enabling stats/policy; counters are atomic add/load operations per connection ([app/stats/counter.go](https://github.com/XTLS/Xray-core/blob/main/app/stats/counter.go)).

---

## Sources

- Stats config: https://xtls.github.io/en/config/stats.html
- Policy config (statsUserOnline etc.): https://xtls.github.io/en/config/policy.html
- API config & services: https://xtls.github.io/en/config/api.html
- Metrics (expvar/pprof) config: https://xtls.github.io/en/config/metrics.html
- Observatory/BurstObservatory: https://xtls.github.io/en/config/observatory.html
- CLI (`xray api ...`): https://xtls.github.io/en/document/command.html
- Traffic-stats tutorial: https://xtls.github.io/en/document/level-2/traffic_stats.html
- StatsService proto: https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto
- StatsService impl: https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go
- Metrics module source: https://github.com/XTLS/Xray-core/blob/main/app/metrics/metrics.go
- Metrics config proto: https://github.com/XTLS/Xray-core/blob/main/app/metrics/config.proto
- Metrics PR (prometheus dropped): https://github.com/XTLS/Xray-core/pull/1000
- Observatory proto/service: https://github.com/XTLS/Xray-core/blob/main/app/observatory/config.proto , https://github.com/XTLS/Xray-core/blob/main/app/observatory/command/command.proto
- API service registration (ObservatoryService): https://github.com/XTLS/Xray-core/blob/main/infra/conf/api.go
- In-memory counter/online map: https://github.com/XTLS/Xray-core/blob/main/app/stats/counter.go , https://github.com/XTLS/Xray-core/blob/main/app/stats/online_map.go
- Dispatcher online counter: https://github.com/XTLS/Xray-core/blob/main/app/dispatcher/default.go
- Feature PRs: https://github.com/XTLS/Xray-core/pull/3637 , /pull/5080 , /pull/5776 ; v25.3.6 notes: https://github.com/XTLS/Xray-core/releases/tag/v25.3.6
- compassvpn/xray-exporter: https://github.com/compassvpn/xray-exporter ; dashboard: https://grafana.com/grafana/dashboards/23181-compassvpn-dashboard/
- anatolykopyl/xray-exporter: https://github.com/anatolykopyl/xray-exporter ; dashboard: https://grafana.com/grafana/dashboards/23145
- wi1dcard/v2ray-exporter: https://github.com/wi1dcard/v2ray-exporter
- AlchemyLink/xray-stats-exporter: https://libraries.io/go/github.com%2Falchemylink%2Fxray-stats-exporter
- kutovoys/xray-checker: https://github.com/kutovoys/xray-checker ; announcement: https://github.com/XTLS/Xray-core/discussions/4316
- batonogov/xray-health-exporter: https://github.com/batonogov/xray-health-exporter
- hteppl/3x-ui-exporter: https://github.com/hteppl/3x-ui-exporter ; 3x-ui: https://github.com/MHSanaei/3x-ui
- zbndev/3x-ui-prometheus-exporter: https://github.com/zbndev/3x-ui-prometheus-exporter
