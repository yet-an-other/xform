# In-development specification: User details and operational views

Status: approved for implementation, not yet implemented.

`SPEC.md` describes the implemented Panel. This file defines the next change. When the change is complete, fold its behavior into `SPEC.md` and delete this file.

## 1. Sources and status

The design was settled through [Wayfinder map #18](https://github.com/yet-an-other/xform/issues/18) and its child tickets:

- [VLESS share-link research](https://github.com/yet-an-other/xform/issues/19), captured in [`docs/research/vless-share-link-contract.md`](docs/research/vless-share-link-contract.md)
- [Bounded journald research](https://github.com/yet-an-other/xform/issues/20), captured in [`docs/research/bounded-journald-access.md`](docs/research/bounded-journald-access.md)
- [User details prototype](https://github.com/yet-an-other/xform/issues/21), captured in [`docs/prototypes/user-details-dialog.html`](docs/prototypes/user-details-dialog.html)
- [Operational viewers prototype](https://github.com/yet-an-other/xform/issues/22), captured in [`docs/prototypes/operational-viewers.html`](docs/prototypes/operational-viewers.html)
- [Connection profile contract](https://github.com/yet-an-other/xform/issues/23)
- [Operational snapshot contract](https://github.com/yet-an-other/xform/issues/24)
- [Acceptance boundary](https://github.com/yet-an-other/xform/issues/25)

The HTML prototypes and their approved PNGs are layout references. They are not production code and do not override the requirements below.

Normative words use these meanings:

- **SHALL** marks required behavior.
- **SHALL NOT** marks prohibited behavior.
- **MAY** marks an allowed choice that does not affect compatibility.

Canonical domain terms come from [`CONTEXT.md`](CONTEXT.md).

## 2. Goals

This change SHALL add four read-only capabilities to the Dashboard:

1. A User details dialog with current observations and one VLESS Connection profile for each matching inbound that satisfies the Connection profile contract.
2. A manually refreshed Log snapshot dialog for the Panel.
3. A manually refreshed Log snapshot dialog for xray.
4. A Config snapshot dialog showing the exact configured xray file text.

It SHALL also add Panel uptime to the Dashboard header and preserve the existing single-page, same-origin, single-admin deployment model.

The change SHALL leave existing host, xray, Traffic, Presence, Roster, Session, and durable-history behavior intact except where this specification explicitly extends an interface.

## 3. Domain model

### 3.1 Connection profile

A Connection profile is a client-ready VLESS connection for one User through one matching xray inbound.

Its stable identity SHALL be:

```text
(User email, inbound tag)
```

The same email in several uniquely tagged VLESS inbounds SHALL produce several Connection profiles. The same email repeated within one inbound SHALL produce one unavailable result for that inbound because the Client ID is ambiguous.

A Connection profile SHALL be derived from two sources:

- Server-derived values from the matching VLESS inbound, including the canonical Client ID and effective flow.
- Advertised connection settings describing the public client view.

The Panel SHALL NOT infer a complete public client view from an xray listener. NAT, TLS termination, reverse proxies, CDNs, and REALITY selections make that inference unsafe.

A Connection profile SHALL never be persisted. A gone User SHALL retain historical observations and SHALL have no Connection profiles.

### 3.2 Advertised connection settings

One advertisement SHALL select one inbound by its unique, non-empty tag. It SHALL apply to every matching User in that inbound. Per-User Advertised connection settings are not supported.

An advertisement SHALL declare either:

- `direct`, where the advertised transport and security must satisfy the xray inbound, or
- `fronted`, where a frontend may change the public transport or security view.

For `fronted`, the Panel SHALL validate the URI, Client ID, flow, and supported field combinations. It SHALL NOT claim to verify the frontend route.

For `direct`, normalize transport aliases and documented defaults before comparison. The advertised transport and security types SHALL equal the inbound's canonical types. Advertised WebSocket and HTTPUpgrade paths and hosts, gRPC service selections, XHTTP paths, hosts, and modes, and REALITY server names and short IDs SHALL be values the inbound accepts. REALITY public-key syntax SHALL be validated, but the Panel SHALL NOT derive server public material to compare keys. With `encryption=none`, `xtls-rprx-vision` SHALL require advertised TCP with TLS or REALITY.

### 3.3 Operational snapshots

A Log snapshot is a best-effort, point-in-time read of the latest bounded journal entries for one fixed unit. It is not a live stream or an atomic journal transaction.

A Config snapshot is the exact UTF-8 text observed during one bounded read of `XFORM_XRAY_CONFIG`. It is separate from the parsed Roster. The Config snapshot may therefore show malformed JSON while the Roster continues using its last valid parse.

Neither snapshot is persisted by the Panel.

### 3.4 Independent freshness

These states SHALL remain independent:

- User observation freshness
- xray-config parse freshness
- advertised connection settings freshness
- Panel Log snapshot success
- xray Log snapshot success
- Config snapshot success
- xray running, stopped, or unreachable status

A failure in one source SHALL NOT be borrowed as the status of another source.

## 4. Integration seams

This section fixes module responsibilities and interfaces, not package names or file layouts.

### 4.1 Profile module

The profile module SHALL accept:

- an immutable parsed view of VLESS inbounds and their matching Users,
- an immutable advertised-connection snapshot,
- source freshness and source errors.

It SHALL return User-level profile state and ordered available or unavailable profile results. It SHALL own:

- matching by exact User email,
- stable identity checks,
- canonical Client ID conversion,
- effective-flow selection,
- direct and fronted validation,
- supported-shape validation,
- unavailable reason selection,
- canonical URI serialization.

Callers SHALL NOT build or patch VLESS URIs.

### 4.2 Journal module

The journal module SHALL expose one collection operation whose only caller choice is the fixed source `panel` or `xray`. It SHALL own:

- canonical unit selection,
- fixed journalctl arguments,
- process timeout and cancellation,
- process concurrency,
- stdout, stderr, and entry-count limits,
- newline-delimited JSON decoding,
- journal-field normalization,
- stable collection errors.

The interface SHALL NOT accept a unit, count, filter, cursor, time range, or raw journalctl argument.

Process execution SHALL sit behind an internal seam with a production adapter and a fake adapter for tests.

### 4.3 Config snapshot module

The Config snapshot module SHALL expose one bounded read operation for the configured xray path. It SHALL own:

- regular-file validation,
- the byte limit,
- UTF-8 validation,
- exact text preservation,
- stable read errors.

Filesystem access and the clock SHALL sit behind internal seams used by production and test adapters.

### 4.4 HTTP interface

HTTP handlers SHALL translate module results into the contracts in section 6. They SHALL NOT parse xray config, serialize VLESS URIs, invoke journalctl, or normalize journal entries directly.

### 4.5 Dashboard interface

Dashboard modules SHALL consume typed HTTP responses and own:

- one-modal-at-a-time state,
- detail polling,
- manual Log snapshot refresh,
- copy actions,
- QR rendering,
- focus management,
- browser-local retention after a failed manual refresh.

SQLite SHALL remain outside every new module in this specification.

## 5. Configuration and deployment

### 5.1 Advertised connection file

Add optional `XFORM_CONNECTIONS_CONFIG` with no default. Leaving it unset SHALL NOT stop the Panel. Each matching VLESS inbound that otherwise needs an advertisement SHALL return `advertisement_missing`.

When set, the path SHALL name an xform-owned JSON document with this strict root shape:

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

The root SHALL reject unknown fields, duplicate object keys at every depth, trailing JSON values, and unsupported versions. Malformed root JSON or an unsupported version SHALL reject the new snapshot as a whole. After at least one successful load, the Panel SHALL retain the last valid snapshot, mark profile data stale, and expose a safe current source error. With no valid advertisement snapshot, matching inbounds remain identifiable from parsed xray config and SHALL report `source_unavailable` items.

Advertisement records SHALL be validated independently after the root document is decoded. An invalid record SHALL make only its selected inbound unavailable. Two advertisement records selecting the same tag SHALL make that tag unavailable with `duplicate_inbound_tag`; other tags SHALL remain usable. An advertisement that references no current inbound SHALL produce a bounded server-side warning and SHALL NOT affect unrelated Users.

Each advertisement SHALL contain:

| Field | Type | Rule |
| --- | --- | --- |
| `inbound_tag` | string | Required, non-empty |
| `name` | string | Optional, non-empty; defaults to `inbound_tag` |
| `topology` | string | Required; `direct` or `fronted` |
| `host` | string | Required public domain, IPv4, or IPv6 host without a scheme or path |
| `port` | integer | Required, 1 through 65535 |
| `transport` | object | Required typed transport |
| `security` | object | Required typed security |

Unknown advertisement, transport, or security fields SHALL make that advertisement invalid.

Supported transport objects are:

```json
{ "type": "tcp" }

{ "type": "ws", "path": "/xray", "host": "edge.example.com" }

{ "type": "httpupgrade", "path": "/xray", "host": "edge.example.com" }

{
  "type": "grpc",
  "service_name": "xray",
  "mode": "gun",
  "authority": "edge.example.com"
}

{
  "type": "xhttp",
  "path": "/xray",
  "host": "edge.example.com",
  "mode": "auto",
  "extra": {}
}
```

Rules:

- `tcp` SHALL accept no transport-specific fields.
- `ws` and `httpupgrade` SHALL require string `path` and `host` values. Both SHALL be non-empty.
- `grpc` SHALL require a non-empty string `service_name`. String `mode` SHALL default to `gun` and accept `gun`, `multi`, or `guna`. String `authority` MAY be omitted or empty.
- `xhttp` SHALL require non-empty string `path`, `host`, and `mode` values. `mode` SHALL accept `auto`, `packet-up`, `stream-up`, or `stream-one`. `extra` MAY be omitted and, when present, SHALL be a JSON object accepted by RFC 8785 JSON Canonicalization Scheme processing.

Supported security objects are:

```json
{
  "type": "tls",
  "fingerprint": "chrome",
  "server_name": "edge.example.com",
  "alpn": ["h2"],
  "ech": "...",
  "certificate_pins": ["..."],
  "verify_name": "edge.example.com"
}

{
  "type": "reality",
  "fingerprint": "chrome",
  "server_name": "www.microsoft.com",
  "public_key": "...",
  "short_id": "...",
  "post_quantum_verify": "...",
  "spider_x": "/"
}
```

The loader SHALL also recognize `{ "type": "none" }` only so it can return `insecure_connection`. It SHALL never produce an available profile from that object.

Rules:

- Every security field SHALL have the JSON type shown by its example. `fingerprint`, `server_name`, `public_key`, `short_id`, `ech`, `verify_name`, `post_quantum_verify`, and `spider_x` are strings.
- `fingerprint` MAY be omitted, SHALL default to `chrome`, and SHALL be non-empty when present. The contract does not define a closed fingerprint enum.
- TLS `server_name` MAY be omitted and SHALL default to the advertised host.
- REALITY SHALL require non-empty `server_name` and `public_key`.
- REALITY SHALL require `short_id` to be present. It MAY be empty only when the matching inbound accepts an empty short ID.
- `alpn` and `certificate_pins` SHALL be arrays of non-empty strings.
- Empty optional strings SHALL normalize to omission. An explicitly present empty REALITY `short_id` is the only empty string with distinct meaning.
- `ech`, `verify_name`, `post_quantum_verify`, and `spider_x` MAY be omitted.
- The Panel SHALL NOT derive REALITY public values from server private material.

The file SHALL be watched for changes. A successful update SHALL replace the prior advertisement snapshot. A read or root-parse failure SHALL use the stale behavior above.

### 5.2 Parsed xray config

The xray-config parser SHALL retain enough data to produce profile candidates for every matching VLESS inbound rather than flattening profiles into the first email match. It SHALL retain at least:

- inbound tag,
- protocol,
- inbound-level flow and decryption,
- transport and security settings needed for validation,
- REALITY accepted names and short IDs,
- each User's email, configured Client ID, flow, and reverse status.

The existing Roster interface and first-inbound table labels MAY remain separate from this richer parsed view.

A parse failure SHALL keep the last valid parsed snapshot. Connection profiles built from it SHALL be marked stale with a safe source error. The Config snapshot endpoint SHALL still show the currently readable exact file text.

### 5.3 Compatibility scope

Connection serialization is pinned to:

- [XTLS/Xray-core discussion 716](https://github.com/XTLS/Xray-core/discussions/716)
- xray-core commit `f02a35786124a6ad046727f2408e32317cc19a41`
- Xray docs commit `090e425873072704d2a631740a4129ce8013c0eb`

Unknown future or client-specific fields SHALL NOT enter the generated URI automatically.

The initial supported set is:

- RAW/TCP
- WebSocket
- HTTPUpgrade
- gRPC
- XHTTP
- TLS
- REALITY
- VLESS `encryption=none`

The unsupported set includes mKCP, FinalMask, RAW header obfuscation, Hysteria, removed HTTP and QUIC transports, non-`none` VLESS Encryption, and `security=none&encryption=none`.

### 5.4 Journal namespace

Operational log viewing requires Linux with systemd 245 or newer, journalctl, ACL support, and tmpfiles support.

Deployment SHALL use one fixed journal namespace named `xform`. It SHALL set `LogNamespace=xform` on:

- `xform.service`, and
- the configured xray service through an installed drop-in.

The `xform` user SHALL receive inherited read ACLs only on:

```text
/var/log/journal/<machine-id>.xform/
/run/log/journal/<machine-id>.xform/
```

The deployment SHALL NOT add `xform` to `systemd-journal`, `adm`, `wheel`, or another broad journal-reading group. Documentation SHALL warn that assigning another unit to the `xform` namespace exposes that unit's logs to the Panel.

Ship:

- the updated `deploy/xform.service`,
- an xray namespace drop-in example,
- tmpfiles and ACL configuration for persistent and volatile namespace paths,
- installation, migration, and verification instructions.

The installation verification SHALL cover namespace creation, initial records from both units, ACL inheritance after rotation, and access after reboot.

### 5.5 Journal reader configuration

Add optional `XFORM_JOURNALCTL`, defaulting to `/usr/bin/journalctl`. At startup, the configured value SHALL be absolute and, after following a root-configured symlink, SHALL resolve to a regular file executable by the Panel user. Initial validation failure SHALL stop startup. If the validated executable later disappears, changes to an invalid file, or loses execute permission, Log snapshot requests SHALL use `journalctl_unavailable` without stopping ordinary monitoring.

At startup, the Panel SHALL:

- reject an unsafe journalctl path,
- reject shorthand or globbed xray unit names,
- resolve `XFORM_XRAY_UNIT` through systemd,
- use its canonical service `Id`,
- permit canonical instances such as `xray@edge.service`,
- reject an identity that cannot be resolved unambiguously.

Missing namespace files, missing ACLs, an empty namespace, or later reader failures SHALL NOT stop ordinary Panel monitoring.

## 6. HTTP contracts

Every endpoint in this section SHALL require a Session and return `Cache-Control: no-store`. They SHALL emit no CORS headers.

### 6.1 Panel identity

Extend the existing endpoint:

```text
GET /api/v1/panel
```

Response:

```json
{
  "version": "v0.10.0",
  "uptime_seconds": 4831
}
```

Panel uptime SHALL be elapsed whole seconds since the current Panel process started, measured from a monotonic start time. It SHALL reset on restart. The Dashboard SHALL fetch it in the existing five-second refresh cycle rather than extrapolating it in the browser.

### 6.2 User detail

Add:

```text
GET /api/v1/users/{email}
```

The caller SHALL encode the User email with `encodeURIComponent` as one URL path segment. The handler SHALL decode that segment exactly once. Malformed percent encoding SHALL return 400 `invalid_request`; valid encoded `/`, `%`, and non-ASCII bytes SHALL remain part of the email identity rather than route separators. The route SHALL work through both root and documented subpath proxy deployments. A known User SHALL return 200 even when profile generation has failed. An unknown User SHALL return 404. Profile errors SHALL NOT become endpoint-level 500 responses. A 500 is reserved for an internal failure that prevents any detail response.

Response shape:

```json
{
  "collected_at": 1723800000,
  "stale": false,
  "user": {
    "email": "alice@example.com",
    "protocol": "VLESS",
    "security": "XTLS-Reality",
    "up_bytes_total": 12400000000,
    "down_bytes_total": 148200000000,
    "online": true,
    "ips": ["203.0.113.10"],
    "ip_countries": {"203.0.113.10": "NL"},
    "speed_up_bps": 512000,
    "speed_down_bps": 3800000,
    "last_seen": 1723799995,
    "gone": false
  },
  "connection_profiles": {
    "state": "ready",
    "loaded_at": 1723800000,
    "stale": false,
    "errors": [],
    "items": []
  }
}
```

The nested `user` object SHALL use the existing `GET /api/v1/users` User contract without changing nullability or omission rules. In particular, `protocol`, `security`, and `last_seen` remain nullable; Presence remains omitted or nullable on unsupported xray versions as defined by the implemented contract; and `ip_countries` remains omitted when geoip data is unavailable.

`collected_at` and top-level `stale` describe User observations. `connection_profiles.loaded_at`, `stale`, and `errors` describe the parsed xray and advertised-connection sources used for profile evaluation. `loaded_at` SHALL be the oldest last-success time among valid source snapshots used for the response. When advertisements are unset or configured but have never loaded, it SHALL use the parsed xray snapshot time. It SHALL be null only when xray config has never parsed successfully. `stale` SHALL be true when either required source is serving its last-valid snapshot after a reload failure. `errors` SHALL appear in `xray_config`, then `advertisements` order and contain zero or more objects:

```json
{
  "source": "xray_config",
  "reason": "parse_failed",
  "message": "The configured xray file could not be parsed; profiles use the last valid parse."
}
```

`source` SHALL be `xray_config` or `advertisements`. `reason` SHALL be `read_failed`, `parse_failed`, or `unsupported_version`. `message` SHALL be safe human-readable text containing no server secret.

User-level profile states are:

| State | Meaning |
| --- | --- |
| `ready` | Matching candidates were evaluated, including unavailable candidates |
| `gone_user` | The User is gone; `items` is empty |
| `no_matching_inbound` | The User has no current matching VLESS inbound |
| `source_unavailable` | xray config has never parsed successfully, so matching candidates cannot be identified; `loaded_at` is null and `items` is empty |

A configured but never valid advertisement file SHALL keep state `ready` when parsed xray candidates are available and SHALL return one `source_unavailable` item per matching inbound. An unset advertisement path SHALL likewise permit candidate evaluation and produce one `advertisement_missing` item per matching inbound.

An available item has this shape:

```json
{
  "status": "available",
  "inbound_tag": "vless-reality-main",
  "name": "Primary",
  "topology": "direct",
  "client_id": "1d37a118-4f1b-4dc0-9e3c-3426b07518df",
  "flow": "xtls-rprx-vision",
  "endpoint": {
    "host": "edge.example.com",
    "port": 443
  },
  "transport": {
    "type": "tcp"
  },
  "security": {
    "type": "reality",
    "fingerprint": "chrome",
    "server_name": "www.microsoft.com",
    "public_key": "...",
    "short_id": "..."
  },
  "uri": "vless://..."
}
```

`flow` SHALL be `null` when no flow applies. Transport and security objects SHALL expose the full typed public values used to build the URI.

An unavailable item has this shape and SHALL contain no partial URI or QR payload:

```json
{
  "status": "unavailable",
  "inbound_tag": "vless-reality-main",
  "name": "Primary",
  "reason": "inbound_mismatch",
  "message": "The advertised transport does not match this direct inbound."
}
```

`inbound_tag` and `name` MAY be `null` when the failure prevents them from being known.

Stable unavailable reasons and their conditions are:

| Reason | Condition |
| --- | --- |
| `source_unavailable` | Parsed xray candidates exist, but a configured advertisement source has no valid snapshot |
| `advertisement_missing` | No advertisement path is configured, or no advertisement selects the inbound tag |
| `advertisement_invalid` | The selected advertisement has invalid fields or values |
| `duplicate_inbound_tag` | The xray config repeats an inbound tag, or advertisements select one tag more than once |
| `duplicate_user` | The same email appears more than once inside one inbound |
| `inbound_tag_missing` | A matching VLESS inbound has no non-empty tag |
| `reverse_user` | The matching VLESS User is configured for reverse rather than ordinary forward connections |
| `unsupported_transport` | The applicable advertised or direct inbound transport is outside the supported set |
| `unsupported_security` | The applicable security is outside TLS, REALITY, or the separately classified insecure `none` case |
| `unsupported_encryption` | The inbound requires non-`none` VLESS Encryption |
| `insecure_connection` | The advertised client view is `security=none&encryption=none` |
| `inbound_mismatch` | A supported direct advertisement does not satisfy the matching inbound after normalization, or a supported advertised shape is incompatible with the User's effective flow |
| `invalid_client_id` | The matching configured Client ID cannot be canonicalized to a UUID |

When several failures apply, validation SHALL choose the first reason in this precedence order:

```text
inbound_tag_missing
duplicate_inbound_tag
duplicate_user
reverse_user
source_unavailable
advertisement_missing
advertisement_invalid
invalid_client_id
unsupported_transport
unsupported_security
unsupported_encryption
insecure_connection
inbound_mismatch
```

`message` MAY explain secondary failures. A duplicate xray inbound tag SHALL produce one unavailable item at each affected inbound position. Duplicate advertisement records for one otherwise unique xray tag SHALL produce one unavailable item for that matching inbound. Available and unavailable items SHALL follow xray inbound order.

### 6.3 Canonical VLESS URI

The profile module SHALL generate:

```text
vless://<canonical-uuid>@<advertised-host>:<advertised-port>?<query>#<email · name-or-tag>
```

It SHALL apply these rules:

1. Convert a custom xray Client ID using xray's canonical UUID mapping and emit the lowercase UUID.
2. Use the User-level flow when non-empty, otherwise the inbound-level flow. Do not infer the client-only `-udp443` suffix.
3. Emit `encryption=none`. Never copy server `decryption` into the client URI.
4. Canonicalize domains with the Unicode UTS #46 non-transitional lookup profile, then lowercase the ASCII result. Reject empty labels and a trailing dot. Canonicalize IPv6 literals with brackets and reject zone identifiers.
5. Use ECMAScript `encodeURIComponent` escaping for query values and the fragment. Do not use form-query `+` encoding.
6. Emit common fields in this order: `type`, `encryption`, `flow`, `security`.
7. Always emit `type`, `encryption=none`, and `security`. Emit `flow` only when non-empty.
8. Emit transport fields in their schema order:
   - WebSocket and HTTPUpgrade: `path`, `host`
   - gRPC: `serviceName`, `mode`, `authority`
   - XHTTP: `path`, `host`, `mode`, `extra`
9. Emit security fields in this order:
   - TLS: `fp`, `sni`, `alpn`, `ech`, `pcs`, `vcn`
   - REALITY: `fp`, `sni`, `pbk`, `sid`, `pqv`, `spx`
10. Emit effective `fp=chrome`. Omit other empty optional fields except an explicitly empty REALITY `sid`.
11. Join ALPN values and certificate pins with commas before percent-encoding.
12. Serialize XHTTP `extra` with RFC 8785 JSON Canonicalization Scheme before percent-encoding. Duplicate keys are rejected while loading the advertisement document.
13. Use `<email> · <advertisement name or inbound tag>` as the fragment.

The URI string SHALL be the single source for display, copy, and QR generation. The QR payload SHALL be the exact UTF-8 bytes of that string, with no Base64 wrapper, whitespace, alternate label, or reserialization.

### 6.4 Log snapshots

Add:

```text
GET /api/v1/logs/panel
GET /api/v1/logs/xray
```

Each endpoint SHALL collect the latest 500 matching records for its one fixed unit. It SHALL accept no query parameter. Any query parameter SHALL return 400 with `invalid_request`.

The production adapter SHALL execute the equivalent of:

```text
/usr/bin/journalctl
  --system
  --namespace=xform
  --unit=<fixed-canonical-unit>
  --lines=500
  --reverse
  --output=json
  --output-fields=__CURSOR,__REALTIME_TIMESTAMP,_SYSTEMD_UNIT,UNIT,OBJECT_SYSTEMD_UNIT,COREDUMP_UNIT,SYSLOG_IDENTIFIER,_PID,PRIORITY,MESSAGE
  --no-pager
```

It SHALL use separate arguments, no shell, an attached `--unit=<value>` argument, and an environment containing deterministic `LC_ALL=C`, `LANG=C`, and `SYSTEMD_COLORS=0` values. It SHALL NOT depend on an inherited pager or shell environment.

Bounds are:

| Limit | Value |
| --- | --- |
| Process timeout | 5 seconds |
| Entries | 500 |
| stdout | 8 MiB |
| stderr | 64 KiB |
| Concurrent journalctl processes | 1 globally |

Request cancellation SHALL kill and reap the child. A client-disconnect cancellation has no HTTP response requirement; tests SHALL assert process cleanup instead. The reader SHALL decode a stream of JSON objects without an unbounded scanner or combined output buffer. It SHALL reject more than 500 objects and SHALL NOT pass `--all`.

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

The response SHALL be newest first. `captured_at` SHALL be Unix seconds recorded after the child exits and every object is validated and normalized. `identifier`, `pid`, `priority`, `message`, and `message_encoding` are nullable where stated below. Every entry SHALL always include `message`, `message_encoding`, and boolean `message_truncated` keys.

Normalize fields as follows:

- Require `__CURSOR` to be a non-empty scalar string.
- Require `__REALTIME_TIMESTAMP` to be a scalar unsigned decimal microsecond string that fits `uint64`.
- Each trusted unit field, when present, SHALL be a scalar string. An array, object, or numeric value makes the snapshot malformed.
- Derive `unit` from the first non-empty value among `_SYSTEMD_UNIT`, `UNIT`, `OBJECT_SYSTEMD_UNIT`, and `COREDUMP_UNIT`; fall back to the endpoint's fixed unit.
- Return a scalar string `SYSLOG_IDENTIFIER` as `identifier`; absent, null, repeated, or other values normalize to `null`.
- Parse a scalar decimal string `_PID` that fits a non-negative integer as `pid`; absent, repeated, or invalid values normalize to `null`.
- Parse a scalar decimal string `PRIORITY` from 0 through 7; absent, repeated, or invalid values normalize to `null`.
- Return a normal UTF-8 `MESSAGE` unchanged with `message_encoding: "utf-8"` and `message_truncated: false`.
- Join repeated string `MESSAGE` values with a newline and mark the result as UTF-8 and not truncated.
- Base64-encode a numeric byte array whose elements are integers from 0 through 255, with `message_encoding: "base64"` and `message_truncated: false`.
- Convert journalctl `null` to `message: null`, `message_encoding: null`, and `message_truncated: true`.
- Convert a missing `MESSAGE` to an empty UTF-8 string with `message_truncated: false`.
- Any other `MESSAGE` form makes the snapshot malformed.

Reject the entire snapshot if an object lacks a valid cursor or timestamp, contains invalid trusted-field types, cannot be decoded, or breaches a count or byte bound. Do not skip individual entries and still claim a complete snapshot.

A successful command with no entries SHALL return 200 and an empty list. Stable collection reasons are:

```text
snapshot_in_progress
journalctl_unavailable
access_denied
timeout
output_too_large
malformed_output
command_failed
```

Failure classification SHALL use this table:

| Condition | Reason | HTTP behavior |
| --- | --- | --- |
| Global reader already occupied | `snapshot_in_progress` | 429 and `Retry-After: 1` |
| Previously validated executable later missing, replaced by an invalid or non-executable file, or unable to start | `journalctl_unavailable` | 503 |
| Child starts but fixed-locale stderr reports denial while opening journal data | `access_denied` | 503 |
| Five-second deadline expires | `timeout` | 503 |
| stdout or stderr exceeds its cap | `output_too_large` | 503 |
| JSON, entry count, cursor, timestamp, trusted field, or message form is invalid | `malformed_output` | 503 |
| Child exits non-zero for any other reason | `command_failed` | 503 |

A client disconnect cancels collection and produces no required response body. The response shape for a reportable failure is:

```json
{
  "error": "log snapshot unavailable",
  "reason": "timeout"
}
```

The API SHALL NOT return journalctl stderr. The Panel MAY record a bounded error summary, but SHALL NOT copy journal messages into its diagnostic error.

### 6.5 Config snapshot

Add:

```text
GET /api/v1/xray/config
```

The endpoint SHALL accept no query parameter. Any query parameter SHALL return 400 with `invalid_request`.

It SHALL:

- open `XFORM_XRAY_CONFIG`, following a root-configured symlink,
- require the opened target to be a regular file,
- read at most 4 MiB plus one detection byte,
- require valid UTF-8,
- avoid parsing or formatting the content.

Success response:

```json
{
  "captured_at": 1723800000,
  "path": "/usr/local/etc/xray/config.json",
  "size_bytes": 4812,
  "text": "{\n  \"inbounds\": []\n}\n"
}
```

`path` SHALL be the configured path string, not the resolved symlink target. `size_bytes` SHALL be the number of bytes actually read. `captured_at` SHALL be Unix seconds recorded after regular-file, size, and UTF-8 validation succeeds.

Stable 503 reasons are:

```text
config_unreadable
config_too_large
config_not_utf8
```

The response shape is:

```json
{
  "error": "config snapshot unavailable",
  "reason": "config_unreadable"
}
```

A malformed but readable JSON document SHALL be returned unchanged.

## 7. Dashboard behavior

### 7.1 Header

The header SHALL follow the approved operational prototype:

- Panel identity group: `xform`, version, Panel uptime, Panel logs icon action.
- xray identity group: status indicator immediately before `xray`, version, xray uptime, xray logs icon action, xray config icon action.
- Existing refresh cadence, update time, and Log out action remain.

Every icon-only action SHALL have an accessible name and visible tooltip or title.

### 7.2 Users table and detail entry point

The Users table SHALL add the approved icon-only eye action with an accessible name for each User. Uplink and Downlink SHALL share one Traffic column on two lines.

Activating the action SHALL open one modal dialog. The dialog SHALL show, in this order:

1. User email and online or gone status.
2. Current Traffic, Speed, Last seen, and online IP observations.
3. Connection profiles.

While open, the dialog SHALL fetch User detail every five seconds. Detail requests SHALL NOT overlap; if one is still running at the next interval, the next poll is skipped. Only the newest completed request for the currently open User may update the modal. Closing it SHALL cancel its request and timer. The ordinary Dashboard polling SHALL continue behind the modal.

### 7.3 Profile presentation

Available profiles SHALL appear as fully expanded cards in xray inbound order. Each card SHALL show:

- profile name,
- inbound tag,
- Client ID with Copy,
- flow, displaying `none` for a null flow,
- public endpoint,
- transport and security,
- full VLESS URI with Copy,
- a QR code generated from the exact URI.

Stale last-valid profiles SHALL remain visible and copyable with a clear stale warning and source error.

Unavailable results SHALL show the profile name and inbound tag when known, plus the stable reason and readable message. They SHALL NOT show a Client ID copy action, partial URI, or QR code.

A gone User SHALL show the approved historical-observations view and no profiles. `no_matching_inbound` and `source_unavailable` SHALL have distinct empty or error copy.

Observation staleness and profile staleness SHALL be displayed independently.

### 7.4 Operational dialogs

Only one modal SHALL be open at a time.

Panel logs and xray logs dialogs SHALL:

- request one fresh Log snapshot on open,
- show a dense newest-first table,
- show UTC timestamp, source, syslog priority, and message,
- show actual entry count, capture time, and `Bounded · manual refresh`,
- provide one Refresh action,
- state `No Panel redaction` and `No live tail`.

The source column SHALL render `identifier[pid]` when available and fall back to the unit. The priority badge SHALL use syslog labels. Base64 messages SHALL have a visible binary marker. A truncated null message SHALL render `[message exceeds journal field limit]`.

Refresh SHALL be disabled while its request runs. If refresh fails after a successful load, the browser SHALL retain the displayed entries and original timestamp and show `Refresh failed, showing snapshot from …` with the stable reason. An initial failure SHALL show only the error state. The Panel SHALL retain no Log snapshot.

The Config snapshot dialog SHALL:

- request one fresh Config snapshot on open,
- show configured path and exact text,
- preserve horizontal fixed-format scrolling at narrow widths,
- provide Copy and no Refresh action,
- preserve every character, including final newlines.

A failed Config snapshot SHALL show the stable reason and no Copy action. Log and Config snapshot data SHALL live only for the current modal opening. Closing either modal SHALL abort its request and clear its browser-local snapshot. Reopening always starts with an initial load. The browser SHALL not retain an earlier Config snapshot across dialog openings.

### 7.5 Modal and Session behavior

A modal SHALL trap focus, close on Escape or its close action, and restore focus to its opener. At the prototype's narrow breakpoint of 560 CSS pixels or less, fixed-format content SHALL scroll horizontally rather than reflowing metadata.

A 401 from any modal request SHALL close the modal and return the Dashboard to the existing login flow. Other modal failures SHALL stay inside that modal and SHALL NOT trigger Dashboard degraded mode.

A stopped or unreachable xray SHALL NOT disable xray logs or the Config snapshot. Each viewer reports only its own collection result.

## 8. Acceptance requirements and scenarios

The scenarios in this section are the acceptance source of truth. Earlier sections define the full interface used by the scenarios.

### Requirement UD-1: User details entry point

The Dashboard SHALL open User details from an accessible action in each Users table row and SHALL preserve the approved compact Traffic layout.

#### Scenario: Open details from a User row

- **Given** a User row is visible
- **When** the admin activates its named details action
- **Then** focus moves into a modal for that exact User
- **And** Uplink and Downlink remain stacked in one Traffic column behind the modal

### Requirement UD-2: Current detail refresh

The Dashboard SHALL refresh an open User detail every five seconds without overlapping requests and SHALL cancel that work when the modal closes.

#### Scenario: User becomes gone while open

- **Given** an active User detail is open
- **When** a successful Roster refresh marks the User gone
- **Then** the next detail refresh retains historical observations
- **And** changes profile state to `gone_user`
- **And** removes all profile items

#### Scenario: Slow detail request

- **Given** a detail request is still running at the next five-second interval
- **When** the interval fires
- **Then** no overlapping request starts
- **And** closing or switching the modal prevents the old response from updating the current view

### Requirement UD-3: One profile per matching inbound

The profile module SHALL produce one ordered result for every matching VLESS inbound.

#### Scenario: Same User in several inbounds

- **Given** one email appears once in two uniquely tagged VLESS inbounds
- **And** each inbound has one valid advertisement
- **When** the profile module evaluates the User
- **Then** it returns two available profiles in xray inbound order
- **And** each identity is the email paired with its inbound tag

#### Scenario: Duplicate User inside one inbound

- **Given** one email appears twice inside the same VLESS inbound
- **When** the profile module evaluates the User
- **Then** it returns one unavailable result with `duplicate_user`
- **And** returns no partial URI for that inbound

### Requirement UD-4: Canonical URI

Every available profile SHALL produce one deterministic URI used unchanged for display, copy, and QR generation.

#### Scenario: Byte-for-byte fixture

- **Given** a fixture for a supported transport and security shape
- **When** the profile is generated repeatedly
- **Then** every generated URI is byte-for-byte identical to the fixture
- **And** its query fields, escaping, host, UUID, and fragment follow section 6.3

#### Scenario: QR round trip

- **Given** an available profile card
- **When** its rendered QR is decoded
- **Then** the decoded bytes exactly equal the displayed URI's UTF-8 bytes

### Requirement UD-5: Supported shape matrix

The implementation SHALL test every supported transport with TLS and every REALITY-compatible supported transport.

#### Scenario: Supported matrix

- **Given** table fixtures for TCP, WebSocket, HTTPUpgrade, gRPC, and XHTTP with TLS
- **And** fixtures for TCP, gRPC, and XHTTP with REALITY
- **When** each fixture is evaluated
- **Then** each valid combination returns its complete expected URI
- **And** direct fixtures prove normalized transport and security equality plus accepted path, host, service, mode, REALITY name, and short-ID values
- **And** fronted fixtures are validated without claiming route verification

#### Scenario: Vision compatibility

- **Given** a matching User whose effective flow is `xtls-rprx-vision`
- **When** the advertised client view is not TCP with TLS or REALITY
- **Then** no URI is generated
- **And** the applicable primary reason follows the precedence table

### Requirement UD-6: Explicit unavailable results

The profile module SHALL return a stable unavailable reason rather than omit or partially generate an invalid profile.

#### Scenario: Unavailable reason table

- **Given** one fixture for every stable unavailable reason in section 6.2
- **When** each fixture is evaluated
- **Then** the result has `status: "unavailable"`
- **And** has the expected primary reason
- **And** contains no URI or QR payload

#### Scenario: Failure precedence

- **Given** table fixtures in which two or more unavailable conditions apply
- **When** each fixture is evaluated
- **Then** the primary reason is the first applicable reason in the section 6.2 precedence list
- **And** secondary failures may appear only in the readable message

### Requirement UD-7: Independent profile freshness

Profile freshness SHALL remain independent of User observation freshness.

#### Scenario: Stale observations, current profiles

- **Given** the xray observation poll fails
- **And** parsed xray and advertisement sources remain current
- **When** User detail is requested
- **Then** top-level `stale` is true
- **And** available profiles remain current and copyable

#### Scenario: Current observations, stale profiles

- **Given** User observations are current
- **And** a source reload fails after a valid profile snapshot
- **When** User detail is requested
- **Then** top-level `stale` is false
- **And** `connection_profiles.stale` is true
- **And** last-valid profiles remain visible and copyable with the source error

#### Scenario: Both profile sources fail after success

- **Given** parsed xray config and advertisements both loaded successfully
- **And** both later reloads fail
- **When** User detail is requested
- **Then** `connection_profiles.loaded_at` is the older successful source timestamp
- **And** `connection_profiles.stale` is true
- **And** `errors` contains xray-config then advertisement errors

#### Scenario: Advertisement source has never loaded

- **Given** xray config parsed successfully and identifies matching inbounds
- **And** a configured advertisement file has never loaded successfully
- **When** User detail is requested
- **Then** profile state is `ready`
- **And** each matching inbound has one unavailable `source_unavailable` item

### Requirement UD-8: Gone User behavior

A gone User SHALL retain observations and SHALL have no Connection profiles.

#### Scenario: Open a gone User

- **Given** a gone User remains in durable history
- **When** its details are opened
- **Then** historical Traffic and Presence fields are shown as available
- **And** profile state is `gone_user`
- **And** no credential, URI, or QR payload is returned

### Requirement OP-1: Panel uptime

The Panel SHALL expose and display current process uptime.

#### Scenario: Process uptime

- **Given** a process start time and controlled monotonic clock
- **When** `/api/v1/panel` is requested at two later times
- **Then** `uptime_seconds` increases by elapsed whole seconds
- **And** a newly constructed process starts again from its own elapsed time

### Requirement OP-2: Fixed bounded Log snapshots

Each log endpoint SHALL collect at most the latest 500 entries for its fixed unit and SHALL return them newest first.

#### Scenario: More than 500 matching entries

- **Given** journalctl returns the selected latest 500 records
- **When** the endpoint succeeds
- **Then** `entry_count` is 500
- **And** entries are newest first
- **And** no pagination or continuation token is returned

#### Scenario: Empty namespace match

- **Given** journalctl succeeds with no matching object
- **When** the endpoint responds
- **Then** it returns 200 with `entry_count: 0` and an empty list
- **And** does not claim that deployment access was verified

### Requirement OP-3: Journal normalization

The journal module SHALL normalize documented JSON forms without losing normal text or silently dropping entries.

#### Scenario: Message forms

- **Given** fixtures containing UTF-8, repeated, binary, missing, and null messages
- **When** they are normalized
- **Then** each response follows section 6.4
- **And** the UI distinguishes binary and truncated values

### Requirement OP-4: Reader containment

The journal module SHALL enforce process, time, entry, byte, and concurrency limits.

#### Scenario: Collection failure table

- **Given** fake-process cases for timeout, non-zero exit, access denial, malformed JSON, entry overflow, stdout overflow, and stderr overflow
- **When** each collection runs
- **Then** the child is reaped
- **And** no partial snapshot is returned
- **And** the endpoint returns the specified status and stable reason

#### Scenario: Client cancellation

- **Given** a journal child is running
- **When** the request context is cancelled by client disconnect
- **Then** the child is killed and reaped
- **And** no HTTP response assertion is required

#### Scenario: Concurrent request

- **Given** one journal collection is running
- **When** another log endpoint is requested
- **Then** the second request returns 429
- **And** includes `Retry-After: 1`
- **And** does not start another journalctl process

### Requirement OP-5: Exact Config snapshot

The Config snapshot endpoint SHALL preserve every UTF-8 character read from a bounded regular file without parsing it.

#### Scenario: Malformed JSON with final newline

- **Given** a readable regular file containing malformed JSON and a final newline
- **When** the Config snapshot is requested
- **Then** the endpoint returns 200
- **And** `text` exactly equals the source bytes decoded as UTF-8
- **And** Copy writes that exact string

#### Scenario: Unsafe file cases

- **Given** fixtures for a symlink to a regular file, a non-regular file, an oversized file, invalid UTF-8, and an unreadable file
- **When** each is requested
- **Then** only the symlink to a bounded UTF-8 regular file succeeds
- **And** its response `path` remains the configured symlink path while `size_bytes` reports bytes actually read
- **And** each failure returns its stable reason without retained content

### Requirement OP-6: Least-privilege journal deployment

The deployed Panel SHALL read only the dedicated namespace and SHALL not gain default-journal authority.

#### Scenario: Real namespace smoke test

- **Given** a disposable supported host with the deployment artifacts installed
- **When** Panel and xray records are emitted, journals rotate, and the host reboots
- **Then** the `xform` user can read both fixed units in the `xform` namespace
- **And** inherited ACL access still works after rotation and reboot
- **And** the `xform` user cannot read the default namespace or an unrelated unit

### Requirement OP-7: Manual refresh behavior

Log snapshot refresh SHALL be manual and SHALL preserve a successful browser-local snapshot after a later failure.

#### Scenario: Failed refresh after success

- **Given** a dialog displays a successful Log snapshot
- **When** the admin refreshes and collection fails
- **Then** the previous entries and capture time remain visible
- **And** the dialog identifies the refresh failure and stable reason
- **And** the Panel retains no snapshot

### Requirement CROSS-1: Source isolation

A viewer failure SHALL remain local to that viewer.

#### Scenario: Operational failure with live Dashboard

- **Given** host and xray observations are live
- **And** journal access is denied
- **When** the admin opens a Log snapshot dialog
- **Then** the modal shows `access_denied`
- **And** Dashboard observations continue refreshing
- **And** Dashboard degraded mode does not activate because of the viewer

#### Scenario: Stopped xray with operational data

- **Given** xray status is stopped or unreachable
- **And** historical namespace records and the configured file remain readable
- **When** xray logs and Config snapshot are requested
- **Then** both endpoints return their own successful snapshots

### Requirement CROSS-2: Current file and last-valid parsed state

The exact file viewer and parsed sources SHALL report their different truths without overwriting one another.

#### Scenario: Malformed current xray file

- **Given** xray config parsed successfully once
- **And** the current file is replaced with readable malformed JSON
- **When** User detail and Config snapshot are requested
- **Then** Config snapshot returns the malformed text unchanged
- **And** the Roster and profiles use their last-valid parsed state
- **And** profile data is marked stale with a source error

### Requirement CROSS-3: Routing and Session handling

Every new endpoint and modal SHALL preserve same-origin subpath routing and use the existing Session behavior.

#### Scenario: Encoded User identity behind a subpath proxy

- **Given** the Dashboard is mounted under a documented subpath
- **And** a User email contains `/`, `%`, or non-ASCII text
- **When** the Dashboard requests the encoded detail route
- **Then** the handler resolves the exact email identity after one decode
- **And** the proxy does not turn encoded identity bytes into route separators
- **And** malformed percent encoding returns 400 `invalid_request`

#### Scenario: Session expires during modal refresh

- **Given** a modal is open
- **And** the Session expires
- **When** its next request returns 401
- **Then** the modal closes
- **And** the app returns to login
- **And** modal polling is cancelled

### Requirement UI-1: Accessible modal operation

The Dashboard SHALL provide keyboard-operable entry points and modal focus behavior.

#### Scenario: Keyboard open and close

- **Given** focus is on an icon-only viewer action
- **When** the admin opens and closes its modal using the keyboard
- **Then** the action has an accessible name
- **And** focus remains trapped while open
- **And** Escape closes the modal
- **And** focus returns to the opener

### Requirement UI-2: Narrow fixed-format layout

Fixed-format operational content SHALL remain readable at 560 CSS pixels and narrower.

#### Scenario: Narrow viewport

- **Given** a viewport 560 CSS pixels wide or narrower
- **When** a log or Config snapshot contains long fixed-format content
- **Then** the content scrolls horizontally
- **And** log metadata does not stack into a different record format
- **And** modal controls remain keyboard accessible

## 9. Verification

### 9.1 Required automated checks

Implementation SHALL add automated checks for all scenarios that do not require a real host. At minimum, completion requires:

```sh
cd internal && go test ./... && go vet ./...
npm --prefix web test
npm --prefix web run typecheck
npm --prefix web run build
```

It SHALL also verify:

- table-driven URI fixtures for every supported transport and security shape,
- every stable unavailable reason,
- QR decode round trip,
- HTTP authentication, fixed routes, rejected query parameters, no-store headers, status codes, and JSON contracts,
- journal fake-process timeout, cancellation, output bounds, count bounds, malformed records, normalization, and concurrency,
- Config snapshot filesystem and byte-bound cases,
- Panel uptime with a controlled clock,
- Dashboard focus, modal, polling, copy, refresh, stale, and error behavior.

The production Dashboard output embedded in the Go binary SHALL be regenerated and committed whenever frontend source changes. The embedded output SHALL match a fresh production build.

Static release builds SHALL still succeed with `CGO_ENABLED=0` for Linux amd64 and arm64. The journal reader SHALL remain an external process adapter and SHALL NOT introduce sd-journal cgo bindings.

### 9.2 Deployment smoke test

Real journal authority SHALL be verified separately from normal unit tests. Provide a root-run smoke test or documented executable test procedure for a disposable systemd 245 or newer host.

It SHALL verify:

1. The namespace and both unit assignments exist.
2. The Panel reads records for `xform.service` and the canonical xray unit.
3. The Panel user cannot read the default namespace.
4. The Panel user cannot read an unrelated unit.
5. ACL access survives journal rotation.
6. ACL access survives reboot.
7. Volatile and persistent namespace paths work when selected by host journal configuration.
8. Old default-namespace records are not exposed through the Panel.
9. The existing systemd sandbox still permits the distro journalctl executable without weakening unrelated hardening.

### 9.3 Manual UI check

Before completion, compare the production Dashboard at the approved wide and narrow viewports with the prototype references. This is a layout and interaction check, not a pixel-perfect screenshot gate.

Verify:

- header identity groups and direct actions,
- details entry point and Traffic column,
- multi-profile, single-profile, unavailable-profile, and gone-User dialogs,
- 500-entry log density,
- binary and truncated-message states,
- Config snapshot long-line scrolling,
- keyboard focus and restored focus.

## 10. Migration and compatibility

Existing monitoring SHALL continue when `XFORM_CONNECTIONS_CONFIG` is unset or journal namespace migration has not run:

- User detail MAY open, but matching profile candidates SHALL report `advertisement_missing`.
- Log snapshot endpoints SHALL report their own stable deployment or access error.
- Host, xray, and User monitoring SHALL continue.
- Config snapshot SHALL work whenever the existing configured file permissions allow it.

The one-time operational migration SHALL:

1. Verify systemd 245 or newer and required host tools.
2. Install the updated Panel unit.
3. Install the xray namespace drop-in for the configured canonical unit.
4. Install tmpfiles and ACL rules for persistent and volatile paths.
5. Reload systemd and restart both services.
6. Run the positive and negative access checks.

The migration SHALL NOT copy historical default-journal entries. New records appear in the `xform` namespace after restart. Administrators SHALL use `journalctl --namespace=xform` for those records.

The binary updater SHALL NOT attempt to install or mutate root-owned unit, drop-in, tmpfiles, or ACL files. Release notes and deployment documentation SHALL identify the manual migration.

When implementation is complete:

- merge the implemented behavior into `SPEC.md`,
- remove `IN-DEV-SPEC.md`,
- retain research and prototype assets as decision history,
- keep `CONTEXT.md` terms that remain part of the implemented domain model.

## 11. Non-goals

### 11.1 Connection profiles

This change does not include:

- User or xray mutation,
- non-VLESS profiles,
- subscription URLs,
- client-specific profile formats or import guarantees,
- mKCP, FinalMask, Hysteria, removed transports, or RAW header obfuscation,
- non-`none` VLESS Encryption,
- unsecured `security=none&encryption=none` profiles,
- per-User Advertised connection settings,
- Connection profiles for gone Users,
- profile or credential persistence,
- credential masking or reveal controls,
- an audit trail of profile access.

### 11.2 Operational viewers

This change does not include:

- live log following,
- pagination, search, filtering, or downloads,
- caller-selected units, counts, cursors, time ranges, fields, or journal expressions,
- log clearing or service controls,
- xray-managed log-file reading,
- Config snapshot editing, validation, formatting, download, or reload controls,
- historical Log or Config snapshot storage,
- migration of old default-journal records,
- broad default-journal access,
- a privileged journal broker,
- support for non-systemd hosts or systemd older than 245,
- automatic installation of root-owned migration files by the binary updater,
- an audit trail of operational-view access.

### 11.3 Dashboard and delivery

This change does not include:

- multiple simultaneous modals,
- pixel-perfect screenshot assertions,
- changes to the existing five-second host, xray, or Users collection cadence,
- a new frontend framework, HTTP router, database, or persistent store.

## 12. Implementation slices

Implementation SHOULD proceed in these reviewable slices. Each slice SHALL leave existing monitoring and tests working.

1. **Decision assets and configuration contracts**
   - Land research, approved prototypes, glossary terms, environment variables, and strict configuration types.
2. **Profile core**
   - Extend parsed xray data, load advertisements, validate candidates, canonicalize Client IDs, serialize URIs, and cover the complete fixture matrix.
3. **User-detail HTTP interface**
   - Add profile-source freshness, User-level states, item unions, authentication, errors, and endpoint tests.
4. **User details Dashboard**
   - Add the table entry point, Traffic column change, modal, polling, profile cards, copy actions, QR generation, stale states, and accessibility tests.
5. **Operational core**
   - Add Panel process uptime, bounded journal execution and normalization, bounded Config snapshot reads, and fake-adapter tests.
6. **Operational HTTP and deployment**
   - Add fixed endpoints, status mappings, namespace units, drop-in, tmpfiles and ACL artifacts, migration docs, and the deployment smoke test.
7. **Operational Dashboard**
   - Add the approved header, Log snapshot dialogs, Config snapshot dialog, manual refresh behavior, copy, errors, and accessibility tests.
8. **Cross-view completion**
   - Add combined-state tests, wide and narrow manual checks, regenerate embedded Dashboard output, run every verification gate, merge the final behavior into `SPEC.md`, and remove this file.
