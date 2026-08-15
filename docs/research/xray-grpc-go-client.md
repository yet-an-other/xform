# Go client for xray StatsService, systemd, and system stats — research findings

Date: 2026-08-16. Ticket: GitHub issue #2. Companion to [xray-stats-prometheus.md](./xray-stats-prometheus.md) (what xray exposes; this doc covers how to consume it from Go).
Primary sources: Xray-core source and git history (github.com/XTLS/Xray-core), Go module proxy (proxy.golang.org), pkg.go.dev, grpc-go source, systemd D-Bus API spec, go-systemd and gopsutil repos.

## TL;DR — recommendation

1. **Import `github.com/xtls/xray-core/app/stats/command` as a Go library** — it is the official, self-contained generated gRPC client and it is what production tools (compassvpn/xray-exporter, kutovoys/xray-checker) do. The one non-obvious pitfall: **release tags like `v26.7.28` are NOT valid Go module versions** (the module path has no `/v26` suffix, so the proxy rejects them); you must `go get github.com/xtls/xray-core@v1.260327.0` using XTLS's parallel `v1.YYMMDD.N` tags (or a `@main` pseudo-version), and your toolchain must satisfy xray-core's bleeding-edge `go 1.26` directive. If that is too heavy, `command.proto` is fully self-contained (zero imports) — copying it and running protoc/buf is a clean fallback.
2. **Dial loopback plaintext**: `grpc.NewClient("127.0.0.1:PORT", grpc.WithTransportCredentials(insecure.NewCredentials()))`. The xray gRPC API has **no authentication and no TLS** (`grpc.NewServer()` with zero options in source); binding `127.0.0.1` and enabling only `StatsService` in config is the entire security model (`HandlerService` on the same port can add/remove users).
3. **Version floor**: `GetStats`/`QueryStats`/`GetSysStats` exist since v1.0.0 (2020); `GetStatsOnline`/`GetStatsOnlineIpList` since **v24.11.11**; per-IP `last_seen` since **v25.2.18**; `GetAllOnlineUsers` since **v26.1.13**; `GetUsersStats` since **v26.4.13**. For xform, gate the online-user RPCs on server version or just handle `Unimplemented` errors gracefully.
4. **systemd**: use `github.com/coreos/go-systemd/v22/dbus` — `NewSystemdConnectionContext` + `GetUnitPropertiesContext` (`ActiveState`, `SubState`, `ActiveEnterTimestamp` in µs). Do not parse `systemctl show`.
5. **System stats**: use `github.com/shirou/gopsutil/v4` (note the `/v4` path) — `cpu.Percent`, `mem.VirtualMemory`, `disk.Usage`, `host.Uptime`.

---

## 1. Consuming StatsService from a standalone Go app

### Option A (recommended): import xray-core as a library

The generated client lives in `github.com/xtls/xray-core/app/stats/command` (`command.pb.go`, `command_grpc.pb.go`), generated with protoc-gen-go v1.36.11 / protoc v6.33.5 — standard `google.golang.org/protobuf`, no gogo — [command.pb.go header](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.pb.go). Importing that package also compiles `command.go` (the server impl), which pulls `xray-core/common`, `core`, `features/stats` — a subset of the repo, not the whole proxy stack — [command.go imports](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go).

**It works and is battle-tested in production:**

- compassvpn/xray-exporter imports exactly this package and dials with `grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))`, then calls `QueryStats` / `GetSysStats` — [exporter.go](https://github.com/compassvpn/xray-exporter/blob/main/exporter.go), [go.mod](https://github.com/compassvpn/xray-exporter/blob/main/go.mod).
- kutovoys/xray-checker depends on `github.com/xtls/xray-core v1.260327.1-0.20260627131803-45cf2898ab12` (pseudo-version) — [go.mod](https://github.com/kutovoys/xray-checker/blob/main/go.mod).
- MHSanaei/3x-ui (44.7k stars) builds against xray-core with `go 1.26.6` — [go.mod](https://github.com/MHSanaei/3x-ui/blob/main/go.mod).

**Pitfalls (all verified):**

- **Release tags are not Go-module-installable.** The module is `github.com/xtls/xray-core` (no major-version suffix, [go.mod](https://github.com/XTLS/Xray-core/blob/main/go.mod)), but GitHub releases are tagged `v24.x`/`v25.x`/`v26.x`. The Go proxy rejects them: `GET https://proxy.golang.org/github.com/xtls/xray-core/@v/v26.7.28.zip` → `404 ... module contains a go.mod file, so module path must match major version ("github.com/xtls/xray-core/v26")`. XTLS publishes **parallel Go-compatible tags `v1.YYMMDD.N`** (e.g. `v1.260327.0`; see `@v/list` on the proxy or the repo's tag list). So pin with `go get github.com/xtls/xray-core@v1.260327.0`, or use `go get ...@main` for a pseudo-version. This is the single most likely thing to trip up a new consumer.
- **Bleeding-edge Go requirement.** xray-core's go.mod declares `go 1.26` and tracks the latest toolchain aggressively ([go.mod](https://github.com/XTLS/Xray-core/blob/main/go.mod)); consumers must match (3x-ui is on `go 1.26.6`, xray-checker on `go 1.26.3`). With Go ≥1.21 toolchain auto-download this is an annoyance, not a blocker.
- **Dependency weight.** The module zip itself is only ~1.3 MB (`content-length: 1290460` for `v1.260327.0.zip` on proxy.golang.org), and Go's module-graph pruning means only packages you (transitively) compile are downloaded. But `go mod tidy` will still record a large indirect-require closure (quic-go, gvisor, wireguard, utls, reality, ... — [go.mod require block](https://github.com/XTLS/Xray-core/blob/main/go.mod)), and MVS can bump your `google.golang.org/grpc`/`protobuf` to xray-core's very new versions (grpc v1.83.0, protobuf v1.36.11). No `replace` directives exist in xray-core's go.mod, so no replace-directive contagion.
- **Generated field naming quirk**: the proto field `reset` becomes `Reset_` in Go (it collides with `proto.Message.Reset()`), e.g. `&command.QueryStatsRequest{Pattern: "", Reset_: false}` — [command.pb.go L29](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.pb.go), and compassvpn's usage `QueryStatsRequest{Reset_: false}` ([exporter.go L243](https://github.com/compassvpn/xray-exporter/blob/main/exporter.go)).

### Option B (fallback): copy the proto and generate your own client

`app/stats/command/command.proto` is **fully self-contained — it has no `import` statements** ([command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto)), so vendoring it is trivial:

```bash
protoc --go_out=. --go-grpc_out=. app/stats/command/command.proto
# or a buf.gen.yaml with plugins buf.build/protocolbuffers/go and buf.build/grpc/go
```

Override the `go_package` option (`option go_package = "github.com/xtls/xray-core/app/stats/command";`) via `--go_opt=module=`/buf `managed mode` so the package lands in your own module. This gives you zero dependency on xray-core (only grpc + protobuf), but you must re-vendor the proto when new RPCs appear (see §3 — the file changed 4 times in 2024–2026). A hand-written client is not worth it: the messages are plain proto3 and codegen is one command.

### Option A minimal working code (loopback, insecure)

Note `grpc.Dial` is **deprecated: "use NewClient instead"** — [grpc-go clientconn.go L260-263](https://github.com/grpc/grpc-go/blob/master/clientconn.go); compassvpn/xray-exporter already uses `grpc.NewClient` in production.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	statscmd "github.com/xtls/xray-core/app/stats/command"
)

func main() {
	// xray api config: {"listen": "127.0.0.1:10085", "services": ["StatsService"]}
	conn, err := grpc.NewClient("127.0.0.1:10085",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	c := statscmd.NewStatsServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Runtime stats (goroutines, memory, uptime)
	sys, err := c.GetSysStats(ctx, &statscmd.SysStatsRequest{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("uptime=%ds goroutines=%d alloc=%d sys=%d numGC=%d\n",
		sys.Uptime, sys.NumGoroutine, sys.Alloc, sys.Sys, sys.NumGC)

	// All traffic counters (empty pattern matches everything)
	qs, err := c.QueryStats(ctx, &statscmd.QueryStatsRequest{Pattern: "", Reset_: false})
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range qs.Stat {
		fmt.Printf("%s = %d\n", s.Name, s.Value)
	}

	// Per-user stats incl. online IPs (xray-core >= v26.4.13)
	us, err := c.GetUsersStats(ctx, &statscmd.GetUsersStatsRequest{IncludeTraffic: true, Reset_: false})
	if err != nil {
		log.Printf("GetUsersStats unavailable (older core?): %v", err) // codes.Unimplemented on old cores
	} else {
		for _, u := range us.Users {
			fmt.Printf("user %s: up=%d down=%d onlineIPs=%d\n",
				u.Email, u.Traffic.Uplink, u.Traffic.Downlink, len(u.Ips))
		}
	}
}
```

go.mod (as of research date):

```
go 1.26
require (
    github.com/xtls/xray-core v1.260327.0
    google.golang.org/grpc v1.83.0
)
```

---

## 2. Transport/auth on the gRPC API listener

**There is no authentication and no TLS — plaintext gRPC only.**

- The API server is created as `c.server = grpc.NewServer()` with **zero `ServerOption`s** — no `grpc.Creds(...)`, no auth interceptors — [app/commander/commander.go L66](https://github.com/XTLS/Xray-core/blob/main/app/commander/commander.go). The `StatsService` handler registers onto it directly ([app/stats/command/command.go L214-216](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go)).
- The official docs' own example queries the API with `grpcurl -plaintext localhost:10085 list`, confirming plaintext transport, and document **no** auth mechanism for the API — [api config docs](https://xtls.github.io/en/config/api.html).
- Consequently all official examples bind the listener to `127.0.0.1` (`"listen": "127.0.0.1:8080"`) — [api config docs](https://xtls.github.io/en/config/api.html).

**Security caveats beyond binding 127.0.0.1:**

- **Enable only `StatsService`.** The same port can host `HandlerService`, which can add/remove inbounds, outbounds, and users ([api docs service list](https://xtls.github.io/en/config/api.html)) — anyone who can reach the port then owns your proxy. xform needs only `StatsService`.
- **Anyone local can read it.** Loopback binding is the entire access control: any local process/user can query all stats (compassvpn's README warns likewise for remote setups: "listen on 0.0.0.0 ... and firewall the port, since anyone who can reach it can read your stats" — [README](https://github.com/compassvpn/xray-exporter)). On a multi-user host, restrict further via filesystem permissions on the config, or move the API onto a unix socket / abstract socket via the classic (non-simplified) API-inbound config if needed.
- **`reset=true` is a foot-gun, not an auth issue**: `GetStats`/`QueryStats`/`GetUsersStats` will zero counters when read with `reset: true` ([command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto)) — a read-only panel should hard-code `Reset_: false`.
- Client-side, "auth" doesn't exist to implement: just `insecure.NewCredentials()` over loopback.

---

## 3. RPC availability by xray-core version

Source: git history of [app/stats/command/command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto) (6 commits total, verified via GitHub commits API), cross-checked against tag histories:

| RPC | Added in | Evidence |
|---|---|---|
| `GetStats`, `QueryStats`, `GetSysStats` | **v1.0.0** (2020-11-25; inherited from v2ray-core) | present in the proto at commit `c7f7c08e` "v1.0.0" ([proto @c7f7c08e](https://github.com/XTLS/Xray-core/blob/c7f7c08e/app/stats/command/command.proto)) |
| `GetStatsOnline`, `GetStatsOnlineIpList` | **v24.11.11** (2024-11-11) | PR [#3637](https://github.com/XTLS/Xray-core/pull/3637) "API: Add user online stats", merged 2024-11-03, commit `2c728649` ([diff](https://github.com/XTLS/Xray-core/commit/2c72864935f87779b849906d039fd2767fb14849)); first in tag v24.11.11 |
| per-IP list + `last_seen` in `GetStatsOnlineIpList` | **v25.2.18** (2025-02-18) | PR [#4360](https://github.com/XTLS/Xray-core/pull/4360) "API: Add user IPs and access times tracking", commit `e893fa18` (2025-02-07), present in v25.2.18 history (note: corrects the companion doc's "v25.3.6" — the commit is already reachable from v25.2.18) |
| `GetAllOnlineUsers` | **v26.1.13** (2026-01-13) | PR [#5080](https://github.com/XTLS/Xray-core/pull/5080), merged 2025-12-26, commit `ad468e46`; v25.12.8 (2025-12-08) predates the merge, first containing release is v26.1.13 |
| `GetUsersStats` | **v26.4.13** (2026-04-13) | PR [#5776](https://github.com/XTLS/Xray-core/pull/5776), merged 2026-04-11, commit `a91a88c7`; first containing release v26.4.13 |

**Client strategy for xform**: call `GetStats`/`QueryStats`/`GetSysStats` unconditionally; treat the online-user RPCs as optional and degrade on `codes.Unimplemented` (gRPC returns `Unimplemented` for unknown methods by spec). Also remember the data-side toggles: user counters need client `email`s, and online tracking needs `statsUserOnline: true` in policy — see [xray-stats-prometheus.md](./xray-stats-prometheus.md).

**`GetSysStats` / `SysStatsResponse` fields** ([command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto), populated in [command.go GetSysStats](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go)):

| Field | Type | Source |
|---|---|---|
| `NumGoroutine` | uint32 | `runtime.NumGoroutine()` |
| `NumGC` | uint32 | `runtime.MemStats.NumGC` |
| `Alloc` | uint64 | bytes allocated and still in use |
| `TotalAlloc` | uint64 | cumulative bytes allocated |
| `Sys` | uint64 | bytes obtained from OS |
| `Mallocs` / `Frees` | uint64 | cumulative malloc/free counts |
| `LiveObjects` | uint64 | `Mallocs - Frees` |
| `PauseTotalNs` | uint64 | cumulative GC pause |
| `Uptime` | uint32 | **seconds since the gRPC stats server started** (`time.Since(s.startTime)`), i.e. effectively xray-core process uptime |

Note these are stats for the **xray process only** — host CPU/RAM/disk must come from elsewhere (§5).

---

## 4. systemd unit state from Go

**Recommendation: `github.com/coreos/go-systemd/v22/dbus` over D-Bus, not `systemctl show` parsing.** Parsing CLI output is brittle (locale, formatting, `--value` flag availability varies by systemd version); the D-Bus API is the stable interface `systemctl` itself uses.

- Package: [pkg.go.dev/github.com/coreos/go-systemd/v22/dbus](https://pkg.go.dev/github.com/coreos/go-systemd/v22/dbus) — `NewSystemdConnectionContext(ctx)` (private direct connection to systemd, no dbus-daemon needed) or `NewSystemConnectionContext(ctx)` (system bus); always `Close()`.
- Reads: `GetUnitPropertiesContext(ctx, unit)` returns `map[string]any` of all unit properties; `GetUnitPropertyContext(ctx, unit, name)` for one (the non-`Context` variants are deprecated).
- The properties live on `org.freedesktop.systemd1.Unit`: `ActiveState`, `SubState` (strings), and `ActiveEnterTimestamp` — per the [systemd D-Bus API spec](https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.systemd1.html): "ActiveEnterTimestamp ... contain[s] CLOCK_REALTIME ... 64-bit microsecond timestamps of the last time a unit ... entered the active state". So **uptime = now − ActiveEnterTimestamp/1e6 seconds** (guard for 0 = never active).

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

func main() {
	ctx := context.Background()
	conn, err := dbus.NewSystemdConnectionContext(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	props, err := conn.GetUnitPropertiesContext(ctx, "xray.service")
	if err != nil {
		log.Fatal(err)
	}
	active := props["ActiveState"].(string) // "active", "inactive", "failed", ...
	sub := props["SubState"].(string)       // "running", "dead", "exited", ...
	fmt.Printf("ActiveState=%s SubState=%s\n", active, sub)

	if ts, ok := props["ActiveEnterTimestamp"].(uint64); ok && ts > 0 {
		since := time.UnixMicro(int64(ts))
		fmt.Printf("active since %s (uptime %s)\n", since, time.Since(since).Round(time.Second))
	}
}
```

Caveats: needs Linux with systemd (fine for the xray host; xform's panel must degrade elsewhere); reading properties requires no elevated privileges for world-readable units, but a private connection must run on the host.

---

## 5. Host CPU/RAM/storage from Go

**Recommendation: `github.com/shirou/gopsutil/v4`** — pure-Go (no cgo), cross-platform, the de-facto standard.

- Current major version is **v4** with import path `github.com/shirou/gopsutil/v4/...` and calendar tags (`v4.YY.MM`, e.g. v4.24.04); requires Go 1.18+ — [pkg.go.dev/github.com/shirou/gopsutil/v4](https://pkg.go.dev/github.com/shirou/gopsutil/v4). (The old `github.com/shirou/gopsutil` / `/v3` paths are superseded.)
- Confirmed coverage:
  - **CPU**: `func cpu.Percent(interval time.Duration, percpu bool) ([]float64, error)` — [pkg.go.dev .../v4/cpu](https://pkg.go.dev/github.com/shirou/gopsutil/v4/cpu). `cpu_percent` supported on Linux/macOS/Windows/BSD.
  - **Memory**: `mem.VirtualMemory()` → struct with `Total`, `Available`, `Used`, `UsedPercent` — [pkg.go.dev .../v4/mem](https://pkg.go.dev/github.com/shirou/gopsutil/v4/mem).
  - **Disk**: `func disk.Usage(path string) (*UsageStat, error)` — pass a filesystem path like `"/"` (not a device like `/dev/vda1`) — [pkg.go.dev .../v4/disk](https://pkg.go.dev/github.com/shirou/gopsutil/v4/disk).
  - **Host uptime**: `func host.Uptime() (uint64, error)` and `host.Info()` (`InfoStat.Uptime`, plus `BootTime` "seconds since the epoch") — [pkg.go.dev .../v4/host](https://pkg.go.dev/github.com/shirou/gopsutil/v4/host).

```go
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

func main() {
	pct, err := cpu.Percent(time.Second, false) // 1s sampling window, aggregate
	if err != nil {
		log.Fatal(err)
	}
	vm, err := mem.VirtualMemory()
	if err != nil {
		log.Fatal(err)
	}
	du, err := disk.Usage("/")
	if err != nil {
		log.Fatal(err)
	}
	up, err := host.Uptime() // seconds
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("cpu=%.1f%% mem=%.1f%% (%d/%d) disk=%.1f%% (%d/%d) uptime=%s\n",
		pct[0], vm.UsedPercent, vm.Used, vm.Total,
		du.UsedPercent, du.Used, du.Total,
		(time.Duration(up) * time.Second).Round(time.Second))
}
```

Note: these are **host-level** stats — complementary to xray-process stats from `GetSysStats` (§3), which is what a monitoring panel wants to show side by side.

---

## Sources

xray-core / gRPC:
- go.mod (module path, `go 1.26`, deps, no replace): https://github.com/XTLS/Xray-core/blob/main/go.mod
- StatsService proto (self-contained, all 7 RPCs, SysStats fields): https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto
- Generated client (protoc-gen-go v1.36.11, `Reset_` field): https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.pb.go
- Server impl (GetSysStats field sources, uptime): https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go
- gRPC server with no TLS/auth options: https://github.com/XTLS/Xray-core/blob/main/app/commander/commander.go
- Proto commit history (6 commits; verified via https://api.github.com/repos/XTLS/Xray-core/commits?path=app/stats/command/command.proto)
- Feature PRs: https://github.com/XTLS/Xray-core/pull/3637 , https://github.com/XTLS/Xray-core/commit/2c72864935f87779b849906d039fd2767fb14849 , https://github.com/XTLS/Xray-core/pull/4360 , https://github.com/XTLS/Xray-core/pull/5080 , https://github.com/XTLS/Xray-core/pull/5776
- Tag/version mapping verified via https://api.github.com/repos/XTLS/Xray-core/tags and per-tag commit queries
- Go module proxy (v26.x tags rejected; v1.YYMMDD.N tags; zip size): https://proxy.golang.org/github.com/xtls/xray-core/@v/list , https://proxy.golang.org/github.com/xtls/xray-core/@latest , https://proxy.golang.org/github.com/xtls/xray-core/@v/v1.260327.0.zip
- API config docs (plaintext grpcurl example, service list, 127.0.0.1 examples): https://xtls.github.io/en/config/api.html
- Real-world consumers: https://github.com/compassvpn/xray-exporter/blob/main/exporter.go , https://github.com/compassvpn/xray-exporter/blob/main/go.mod , https://github.com/kutovoys/xray-checker/blob/main/go.mod , https://github.com/MHSanaei/3x-ui/blob/main/go.mod
- grpc-go Dial deprecation / NewClient: https://github.com/grpc/grpc-go/blob/master/clientconn.go , https://pkg.go.dev/google.golang.org/grpc , https://pkg.go.dev/google.golang.org/grpc/credentials/insecure

systemd:
- go-systemd dbus package: https://pkg.go.dev/github.com/coreos/go-systemd/v22/dbus
- systemd D-Bus API (Unit properties, timestamp semantics): https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.systemd1.html (source XML: https://github.com/systemd/systemd/blob/main/man/org.freedesktop.systemd1.xml)

gopsutil:
- Module root (v4 path, Go 1.18+): https://pkg.go.dev/github.com/shirou/gopsutil/v4
- cpu: https://pkg.go.dev/github.com/shirou/gopsutil/v4/cpu
- mem: https://pkg.go.dev/github.com/shirou/gopsutil/v4/mem
- disk: https://pkg.go.dev/github.com/shirou/gopsutil/v4/disk
- host (Uptime, Info/BootTime): https://pkg.go.dev/github.com/shirou/gopsutil/v4/host
