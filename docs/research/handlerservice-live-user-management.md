# HandlerService as the live user-management path — research findings

Date: 2026-08-30. Ticket: GitHub issue #46 (part of wayfinder map #44). Companion to [xray-grpc-go-client.md](./xray-grpc-go-client.md) (how to dial the gRPC API from Go; this doc covers the write path for adding/removing VLESS clients at runtime).
Primary sources: Xray-core source and git history (github.com/XTLS/Xray-core, `main` branch and release tags), official XTLS docs (xtls.github.io).

## TL;DR — answers for the write path

1. **`HandlerService.AlterInbound` with `AddUserOperation` / `RemoveUserOperation` is the runtime add/remove-client operation** on an existing VLESS inbound. It exists since **Xray-core v1.0.0 (2020-11-25)**, inherited from V2Ray, unchanged in shape since. `AddUserOperation` wraps a `protocol.User{level, email, account}` where `account` is a `TypedMessage` containing `vless.Account{id, flow, ...}`; `RemoveUserOperation` takes only an `email` string.
2. **`flow: "xtls-rprx-vision"` can be added via the API** — the API path does **zero** flow validation (`Account.AsAccount()` copies the string verbatim), so it is actually *less* strict than the config-file JSON parser, which whitelists only `""` and `xtls-rprx-vision`. Vision semantics (TLS 1.3 / REALITY only, no UDP, empty-flow clients rejected) are enforced at connection time, identically for API and config users.
3. **StatsService treats API-added users identically to config-file users**: per-user traffic counters (`user>>><email>>>>traffic>>>uplink|downlink`) and online maps (`user>>><email>>>>online`) are created lazily **keyed by email at session time** in the dispatcher — the user's origin is irrelevant. `GetAllOnlineUsers` (v26.1.13+) and `GetStatsOnlineIpList` read the same maps. Caveats: user must have a non-empty email; the user's policy level must have `statsUserUplink`/`statsUserDownlink`/`statsUserOnline` enabled; counters of a *removed* user linger in the stats manager until reset or restart.
4. **API-added users do NOT survive a restart — confirmed.** Everything lives in the inbound handler's `MemoryValidator` (two `sync.Map`s); the config file is only read at startup and the API never writes back.
5. **API and config users coexist** in the same validator. **Duplicate email → `Add` fails** ("User X already exists.", case-insensitive). Duplicate UUID under a *different* email silently overwrites the UUID→user index (footgun). Removing a user does **not** kill its established connections.
6. **Version gates**: add/remove + stats/query RPCs are ancient (v1.0.0). `GetInboundUsers`/`GetInboundUsersCount` since v24.11.x (PR #3644), online tracking since v24.11.x (PR #3637), `GetStatsOnlineIpList` since v25.2.x (PR #4360), `GetAllOnlineUsers` since **v26.1.13** (PR #5080), `GetUsersStats` since **v26.4.13** (PR #5776). For **xray-core ≥ v26.x** everything the write/read path needs is present.

---

## 1. Exact operations: AlterInbound + AddUserOperation / RemoveUserOperation

### Proto definitions

`app/proxyman/command/command.proto` ([blob](https://github.com/XTLS/Xray-core/blob/main/app/proxyman/command/command.proto)):

```proto
message AddUserOperation {
  xray.common.protocol.User user = 1;
}

message RemoveUserOperation {
  string email = 1;
}

message AlterInboundRequest {
  string tag = 1;                              // tag of the existing inbound
  xray.common.serial.TypedMessage operation = 2; // serialized AddUserOperation or RemoveUserOperation
}

service HandlerService {
  rpc AlterInbound(AlterInboundRequest) returns (AlterInboundResponse) {}
  rpc GetInboundUsers(GetInboundUserRequest) returns (GetInboundUserResponse) {}           // v24.11.x+
  rpc GetInboundUsersCount(GetInboundUserRequest) returns (GetInboundUsersCountResponse) {} // v24.11.x+
  // ... AddInbound/RemoveInbound/ListInbounds/AlterOutbound etc.
}
```

The `User` message ([common/protocol/user.proto](https://github.com/XTLS/Xray-core/blob/main/common/protocol/user.proto)) has exactly three fields: `uint32 level = 1`, `string email = 2`, `TypedMessage account = 3`. For VLESS the account is [proxy/vless/account.proto](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/account.proto): `id` (UUID string), `flow` ("May be xtls-rprx-vision"), plus `encryption`/`xorMode`/`seconds`/`padding`/`reverse`/`testpre`/`testseed` (post-quantum VLESS encryption knobs, irrelevant for plain REALITY+vision).

### Server-side flow (what actually happens)

[app/proxyman/command/command.go](https://github.com/XTLS/Xray-core/blob/main/app/proxyman/command/command.go):

1. `AlterInbound` unmarshals `operation` via `GetInstance()`, asserts it is an `InboundOperation`, looks up the inbound handler by `tag` (`ihm.GetHandler`), and calls `operation.ApplyInbound(ctx, handler)` (L84-L101).
2. `AddUserOperation.ApplyInbound` unwraps the proxy (`proxy.GetInbound`), asserts `proxy.UserManager`, converts `op.User.ToMemoryUser()` (parses the `vless.Account` — fails if `id` is not a valid UUID), and calls `um.AddUser(ctx, mUser)` (L36-L50).
3. `RemoveUserOperation.ApplyInbound` calls `um.RemoveUser(ctx, op.Email)` (L53-L63).
4. For VLESS, `proxy.UserManager` is the inbound `Handler`, and both methods delegate to the same `vless.MemoryValidator` the config-file users were loaded into ([proxy/vless/inbound/inbound.go](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/inbound/inbound.go) — `AddUser`/`RemoveUser` ~L213-L224; config users loaded into the identical validator in `init()` ~L51-74).

`GetInboundUsers` (empty email → all users) and `GetInboundUsersCount` read back the same validator, so they enumerate API-added users too (command.go L104-L150).

### Go client usage

Generated client: `github.com/xtls/xray-core/app/proxyman/command` (`command.pb.go`, `command_grpc.pb.go`). See [xray-grpc-go-client.md](./xray-grpc-go-client.md) for dialing (`grpc.NewClient` + `insecure.NewCredentials()`, loopback) and for the Go-module pinning pitfall (use `v1.YYMMDD.N` tags, not `v26.x` release tags).

```go
import (
	command "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/vless"
)

hs := command.NewHandlerServiceClient(conn)

// Add a vision-flow VLESS client to existing inbound "vless-in":
_, err := hs.AlterInbound(ctx, &command.AlterInboundRequest{
	Tag: "vless-in",
	Operation: serial.ToTypedMessage(&command.AddUserOperation{
		User: &protocol.User{
			Level: 0,
			Email: "alice@example.com",
			Account: serial.ToTypedMessage(&vless.Account{
				Id:   "b831381d-6324-4d53-ad4f-8cda48b30811",
				Flow: vless.XRV, // "xtls-rprx-vision"
			}),
		},
	}),
})

// Remove by email:
_, err = hs.AlterInbound(ctx, &command.AlterInboundRequest{
	Tag:       "vless-in",
	Operation: serial.ToTypedMessage(&command.RemoveUserOperation{Email: "alice@example.com"}),
})
```

Error cases returned by the RPC: unknown operation type (`"unknown operation"`), wrong operation for direction (`"not an inbound operation"`), missing tag (`"failed to get handler: <tag>"`), proxy not a `UserManager`, unparseable user (`"failed to parse user"` — e.g. bad UUID), duplicate email (`"User <email> already exists."`), remove of unknown/empty email (`"User <email> not found."` / `"Email must not be empty."`).

### Enabling the API

Config `api` object listing `"HandlerService"` (and `"StatsService"`) in `services`; since v1.8.12 the simplified form `"api": {"tag": "api", "listen": "127.0.0.1:10085", "services": [...]}` suffices without manual inbound+routing wiring — [XTLS docs, API configuration](https://xtls.github.io/en/config/api.html). The docs explicitly scope HandlerService user ops: *"Add a user to an inbound (supports VMess, VLESS, Trojan, Shadowsocks only)"*. Security model: the gRPC server has **no auth, no TLS** — bind loopback only (see companion doc §TL;DR.2).

## 2. flow=xtls-rprx-vision via the API — yes, with less validation than the config file

- **API path performs no flow validation at all.** `vless.Account.AsAccount()` ([proxy/vless/account.go](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/account.go) L13-L29) only parses the UUID and copies `Flow`, `Encryption`, etc. verbatim into `MemoryAccount`. So `flow: "xtls-rprx-vision"` passes through and works; note a *garbage* flow string would also be accepted and would only fail at connect time ("unknown request flow" / flow-mismatch rejection).
- **Config-file path is stricter.** `VLessInboundConfig.Build()` in [infra/conf/vless.go](https://github.com/XTLS/Xray-core/blob/main/infra/conf/vless.go) whitelists flow: per-user `flow` must be `""` (inherits inbound-level `settings.flow`) or `xtls-rprx-vision`; anything else is a config error (`VLESS users: "flow" doesn't support ...`). Inbound-level `settings.flow` is likewise restricted to `""`/`xtls-rprx-vision`.
- **Runtime semantics are identical for both origins** ([proxy/vless/inbound/inbound.go](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/inbound/inbound.go) `Process`, flow switch): the account's flow must equal the flow the client sent; vision requires the underlying connection to be **TLS 1.3 or REALITY directly** (`"XTLS only supports TLS and REALITY directly for now."`), rejects **UDP**, and a vision account whose client connects **without** a flow is rejected (`"account ... is rejected since the client flow is empty..."`).
- **`level`**: free-form `uint32`, passed through both paths into `MemoryUser.Level`; used for policy lookup (`policyManager.ForLevel(user.Level)` — drives buffering/timeouts and which `statsUser*` flags apply). Default 0 if unset.
- **`email`**: free-form string. The validator allows an empty email ("must be empty or unique") but an empty email makes the user unremovable via `RemoveUserOperation`, invisible to per-user stats (see §3), and unlistable via `GetInboundUsers`-by-email — **always set email**. Uniqueness check is case-insensitive (`strings.ToLower`).

## 3. StatsService and presence: API users counted identically to config users

The key mechanism is in the **dispatcher**, not the inbound: [app/dispatcher/default.go](https://github.com/XTLS/Xray-core/blob/main/app/dispatcher/default.go):

- `getLink` (~L156-L183) and `WrapLink` (~L186-L216): for every session with `user != nil && len(user.Email) > 0`, counters are created **lazily** by email via `stats.GetOrRegisterCounter("user>>>"+email+">>>traffic>>>uplink" | "...downlink")`, gated on the user's policy level (`p.Stats.UserUplink` / `UserDownlink`). Nothing here distinguishes how the user was added — the email comes from the authenticated session.
- `trackOnlineIP` (~L219-L226): registers/updates the online map `user>>><email>>>>online`, gated on `p.Stats.UserOnline`; the IP entry is removed when the connection context ends (`context.AfterFunc`).
- The StatsService RPCs read exactly these counters/maps ([app/stats/command/command.go](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go)): `GetStats`/`QueryStats` (L30-L57, L153-L174), `GetStatsOnline`/`GetStatsOnlineIpList` (L59-L81), `GetAllOnlineUsers` → `stats.GetAllOnlineUsers()` (L83-L87), `GetUsersStats` aggregates online maps + traffic per email with optional reset (L89-L151).

Implications for the write path:

1. **No registration step needed** — the first connection of an API-added user creates its counters, same as a config user.
2. **Policy gate**: per-user stats require the matching `statsUserUplink`/`statsUserDownlink`/`statsUserOnline` on the user's policy level (`policy.levels["0"]` for level-0 users), and `stats` enabled in config. Without them, `GetStats("user>>>...")` returns `NotFound` and `GetAllOnlineUsers` never lists the user.
3. **Stale counters**: removing a user does not delete its counters/online-map from the stats manager; `QueryStats` still lists `user>>><email>>>...` until fetched with `reset=true` or restart. (Counter values themselves do not survive restart either — the stats manager is in-memory.)
4. Online presence follows connections, not validator membership: a removed user with a still-open connection stays "online" until that connection closes.

## 4. Restart persistence: API-added users are in-memory only — confirmed

- Config users are loaded once in the VLESS inbound `init()` into a `new(vless.MemoryValidator)` ([inbound.go L51-74](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/inbound/inbound.go)); `MemoryValidator` is two `sync.Map`s ([proxy/vless/validator.go L26-L31](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/validator.go)).
- `AddUser`/`RemoveUser` only mutate those maps. No code path in `app/proxyman/command` or the VLESS inbound writes the config file or any persistent store. On restart, the inbound is rebuilt from the config file alone — API-added users are gone.
- Consequence for xform: the API is a **live apply layer**; the config file (or xform's own DB) must remain the source of truth and be re-applied/reconciled after restart.

## 5. Coexistence and duplicates

- **Same store**: config-file and API-added users live in the one `MemoryValidator` per inbound handler — they coexist and are indistinguishable at runtime (both served, both counted, both listed by `GetInboundUsers`).
- **Duplicate email** (API-vs-config or API-vs-API): `Add` uses `email.LoadOrStore(strings.ToLower(email), u)`; if already present it returns `errors.New("User ", u.Email, " already exists.")` ([validator.go L33-L45](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/validator.go)). The `AlterInbound` RPC surfaces this as an error; the existing user is untouched (no overwrite/merge semantics). To "update" a user you must `RemoveUserOperation` then `AddUserOperation` (not atomic).
- **Duplicate UUID under a different email**: `users.Store(ProcessUUID(id), u)` overwrites silently — the UUID→user index (used for actual authentication) then points at the newest user, while both emails remain resolvable by email. Avoid reusing UUIDs across emails.
- **Remove is email-keyed and non-existent email errors**: `Del` requires non-empty email and errors `"User <email> not found."` ([validator.go L48-L60](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/validator.go)).
- **Remove does not drop live connections**: VLESS `RemoveUser` only deletes from the validator (plus reverse-proxy cleanup) — [inbound.go L218-L222](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/inbound/inbound.go). Established sessions keep running until they end naturally; new authentications with that UUID fail.

## 6. Version gates

| Capability | Introduced | Evidence |
|---|---|---|
| `HandlerService.AlterInbound` + `AddUserOperation`/`RemoveUserOperation` | **v1.0.0** (2020-11-25), inherited from V2Ray | [command.proto @ v1.0.0](https://github.com/XTLS/Xray-core/blob/v1.0.0/app/proxyman/command/command.proto) |
| `StatsService.GetStats`/`QueryStats`/`GetSysStats` | v1.0.0 | same era, see [xray-grpc-go-client.md](./xray-grpc-go-client.md) |
| Online-user tracking (`statsUserOnline`, `GetStatsOnline`, online maps) | v24.11.x | PR [#3637](https://github.com/XTLS/Xray-core/pull/3637), merged 2024-11-03 (commit 2c72864) |
| `HandlerService.GetInboundUsers`/`GetInboundUsersCount` | v24.11.x (present in v24.11.30) | PR [#3644](https://github.com/XTLS/Xray-core/pull/3644), merged 2024-11-03 (commit 85a1c33); [command.proto @ v24.11.30](https://github.com/XTLS/Xray-core/blob/v24.11.30/app/proxyman/command/command.proto) |
| `GetStatsOnlineIpList` (+ per-IP `last_seen`) | v25.2.x | PR [#4360](https://github.com/XTLS/Xray-core/pull/4360), merged 2025-02-07 (commit e893fa1); companion doc pins v25.2.18 |
| `StatsService.GetAllOnlineUsers` | **v26.1.13** | PR [#5080](https://github.com/XTLS/Xray-core/pull/5080), merged 2025-12-26 (commit ad468e4); [command.proto @ v26.1.13](https://github.com/XTLS/Xray-core/blob/v26.1.13/app/stats/command/command.proto) contains it; tag v25.12.8 (2025-12-08) predates the merge |
| `StatsService.GetUsersStats` (per-user traffic + online IPs + reset) | **v26.4.13** | PR [#5776](https://github.com/XTLS/Xray-core/pull/5776), merged 2026-04-11 (commit a91a88c); first tag after merge is v26.4.13 |
| `ListInbounds`/`ListOutbounds`; `isOnlyTags` | v25.6.x / v25.7.x | PRs [#4723](https://github.com/XTLS/Xray-core/pull/4723), [#4870](https://github.com/XTLS/Xray-core/pull/4870) |

**For xray-core ≥ v26.x**: every RPC the user-management write/read path needs is available; only `GetUsersStats` needs ≥ v26.4.13 specifically. Go-module pinning reminder: release tags `v26.x.y` are not `go get`-able; use parallel `v1.YYMMDD.N` tags or `@main` pseudo-versions ([xray-grpc-go-client.md](./xray-grpc-go-client.md) §1).

## Sources

- [app/proxyman/command/command.proto (main)](https://github.com/XTLS/Xray-core/blob/main/app/proxyman/command/command.proto) — operation and service definitions
- [app/proxyman/command/command.go (main)](https://github.com/XTLS/Xray-core/blob/main/app/proxyman/command/command.go) — AlterInbound dispatch, AddUser/RemoveUser → proxy.UserManager
- [common/protocol/user.proto](https://github.com/XTLS/Xray-core/blob/main/common/protocol/user.proto), [common/protocol/user.go](https://github.com/XTLS/Xray-core/blob/main/common/protocol/user.go) — User fields, ToMemoryUser
- [proxy/vless/account.proto](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/account.proto), [proxy/vless/account.go](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/account.go) — account fields; AsAccount copies flow verbatim (no validation)
- [proxy/vless/validator.go](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/validator.go) — in-memory store, duplicate-email error, email-keyed Del
- [proxy/vless/inbound/inbound.go](https://github.com/XTLS/Xray-core/blob/main/proxy/vless/inbound/inbound.go) — shared validator for config+API users; flow enforcement at connect time; RemoveUser doesn't kill sessions
- [infra/conf/vless.go](https://github.com/XTLS/Xray-core/blob/main/infra/conf/vless.go) — config-file flow whitelist (stricter than API)
- [app/dispatcher/default.go](https://github.com/XTLS/Xray-core/blob/main/app/dispatcher/default.go) — per-email counters/online maps, policy gates
- [app/stats/command/command.proto](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto), [app/stats/command/command.go](https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.go) — stats/presence RPCs
- GitHub commits API for [stats proto history](https://api.github.com/repos/XTLS/Xray-core/commits?path=app/stats/command/command.proto) and [proxyman proto history](https://api.github.com/repos/XTLS/Xray-core/commits?path=app/proxyman/command/command.proto) — version gates
- [XTLS docs: API configuration](https://xtls.github.io/en/config/api.html) — enabling services; HandlerService user ops support VLESS

## Gaps / not verified

- Exact patch-level tags for v24.11.x / v25.2.x introductions (PR merge dates + v24.11.30/v26.1.13 file contents verified; the companion doc's v24.11.11 / v25.2.18 pins were not re-verified against tag contents here).
- Behaviour of `encryption` (ML-KEM VLESS encryption) accounts via the API was not exercised; the proto fields pass through `AsAccount` the same way, but only flow was in scope.
- No runtime test was performed (no live xray instance); all claims are from source reading of `main` @ 2026-08-30 and tagged releases.
