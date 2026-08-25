> Context: [Wayfinder ticket: Determine the VLESS share-link contract](https://github.com/yet-an-other/xform/issues/19)

# VLESS share-link contract research

## Result in brief

A usable VLESS link has this shape:

```text
vless://<canonical-uuid>@<advertised-host>:<advertised-port>?<client-query>#<label>
```

The authority and query describe a client outbound, not an xray inbound. The Panel can safely obtain the User's canonical UUID and effective `flow` from the matching VLESS inbound. It can also inspect the inbound's transport and security settings as constraints. It cannot infer the public address, public port, frontend topology, client SNI or HTTP Host, or which value to select from a REALITY `serverNames` or `shortIds` list. Those are advertised connection settings.

The safest contract is therefore not "server config plus a few overrides." It is a client-view advertisement keyed to a stable inbound identity, with server-derived credentials and constraints merged into it. This matters for listeners behind NAT, a CDN, Nginx/Caddy, a port forward, a Unix socket, or a REALITY fallback chain.

The link text should be the single source for both presentation and QR generation. Encode that exact URI as the QR payload, with no Base64 wrapper, newline, alternate label, or QR-only normalization. Gone Users have no profiles; none of the derivation rules below changes that established behavior.

## What is actually standard

There is no IETF or IANA VLESS URI standard. `vless` does not appear in the [IANA URI Schemes registry](https://www.iana.org/assignments/uri-schemes/uri-schemes.xhtml). The owner-written definition is the Project X discussion titled ["VMess / VLESS share-link proposal"](https://github.com/XTLS/Xray-core/discussions/716). It is an official Project X proposal and the best available owner source, but it is still a mutable GitHub discussion, not an RFC, a registered scheme, or an xray-core parser API. Current xray-core contains no `vless://` parser.

The proposal has also accumulated stale examples. Its current field list allows `security=none|tls|reality` and current transport fields, while examples near the bottom still use removed legacy XTLS, mKCP `seed`, and `headerType` forms. Current xray-core rejects legacy `security=xtls`, maps current transport aliases explicitly, and has removed old HTTP/QUIC transports and mKCP header/seed settings ([transport parsing at `f02a357`](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/infra/conf/transport_internet.go#L13-L39), [mKCP parsing at `f02a357`](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/infra/conf/transport_method.go#L523-L542)). The later contract should pin both an xray-core compatibility floor and a dated snapshot of the proposal. It should not turn the proposal's old examples into acceptance tests.

## URI rules to preserve

The Project X proposal defines these rules ([sections 1 through 4](https://github.com/XTLS/Xray-core/discussions/716)):

- The scheme is lowercase `vless`.
- UUID, host, and port are mandatory. Port is an integer from 1 through 65535.
- A host may be a domain, IPv4 address, or bracketed IPv6 literal. Internationalized domains must use their ASCII IDNA form.
- Query field order is insignificant, but a field name may not occur twice. Field names and constant values are case-sensitive.
- Every field value uses JavaScript `encodeURIComponent` escaping. In particular, `/`, `,`, `#`, `&`, `=`, and non-ASCII label text need percent encoding in their URI components. The normative algorithm is [ECMAScript `encodeURIComponent`](https://tc39.es/ecma262/multipage/global-object.html#sec-encodeuricomponent-uricomponent). Go's `url.QueryEscape` is not a drop-in contract because it serializes spaces as `+`.
- The fragment is an optional human-readable connection label. It does not carry connection semantics.

These are scheme-specific rules layered on the generic URI grammar in [RFC 3986](https://www.rfc-editor.org/rfc/rfc3986.html). The later contract should choose a fixed query-field order even though the proposal does not require one. Deterministic output gives the Panel one byte-for-byte URI for display, copy, tests, and QR generation.

The proposal is unclear about omitting `type`: its examples omit it for TCP, but the field definition does not state a default. Emit `type=tcp` for RAW/TCP rather than relying on client convention. For other fields, omission defaults are stated below. Avoid empty query values unless the proposal explicitly permits them.

## Current share fields

The following table is the current owner-defined proposal, not a survey of client-specific extensions. Field names are exact.

| URI part | Client meaning | Proposal rule | Server-config relationship |
|---|---|---|---|
| userinfo | VLESS UUID | Required, non-empty | Canonicalize `settings.users[].id`; see [UUID handling](#user-id-and-flow). |
| host | Outbound `address` | Required; domain, IPv4, or bracketed IPv6 | Must be advertised. `listen` is not an outbound address. |
| port | Outbound `port` | Required, 1 through 65535 | Must be advertised. An inbound can use an internal port, range, environment value, or Unix socket ([InboundObject](https://xtls.github.io/en/config/inbound.html)). |
| fragment | Display label | Optional, percent-encoded | Must be chosen by the Panel contract. Email and inbound tag are available ingredients, not a prescribed label. |
| `type` | Transport | Current proposal values: `tcp`, `kcp`, `ws`, `http`, `grpc`, `httpupgrade`, `xhttp` | Canonicalize current aliases only after deciding the advertised frontend transport. See the shape matrix below. |
| `encryption` | VLESS outbound `settings.encryption` | `none` or current `mlkem768x25519...`; omitted means `none`; never empty | `decryption=none` implies `encryption=none`. Non-`none` is a client counterpart, not a copy of server `decryption`. |
| `flow` | VLESS outbound `settings.flow` | Current value includes `xtls-rprx-vision`; empty is permitted | Resolve per-User `flow` against inbound `settings.flow`. |
| `security` | Transport security | `none`, `tls`, or `reality`; omitted means `none`; never empty | Inbound value is a useful constraint, but a TLS-terminating frontend can change the advertised client view. |
| `fm` | Entire FinalMask JSON | Optional, percent-encoded JSON | Required when the advertised path depends on FinalMask. The old `headerType` and mKCP `seed` fields are not current grammar. |

The VLESS inbound and outbound field sets are documented separately by Project X ([inbound](https://xtls.github.io/en/config/inbounds/vless.html), [outbound](https://xtls.github.io/en/config/outbounds/vless.html)). Server `level`, `email`, `fallbacks`, and reverse-proxy settings do not become URI query fields.

### Transport query fields

| `type` | Share fields | Rules and derivability |
|---|---|---|
| `tcp` | No current transport query fields | This is xray RAW. `network: raw` and `network: tcp` both build the same core transport ([alias parser](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/infra/conf/transport_internet.go#L16-L20)). RAW HTTP header obfuscation remains configurable in core, but the current proposal has no corresponding field. A RAW inbound that depends on it is not faithfully exportable unless the configuration has moved that behavior to `fm`. |
| `ws` | `path`, `host` | `path` defaults to `/`; the proposal recommends emitting it. `host` has no useful default recommendation. Server-side WebSocket can verify both, but the client chooses Host with priority `host`, then headers, then address ([Project X WebSocket docs](https://xtls.github.io/en/config/transports/websocket.html)). Copying server values is safe only for a direct, unfronted profile. |
| `httpupgrade` | `path`, `host` | Same omission guidance as WebSocket ([proposal](https://github.com/XTLS/Xray-core/discussions/716), [Project X HTTPUpgrade docs](https://xtls.github.io/en/config/transports/httpupgrade.html)). Frontends commonly make Host an advertised value. |
| `grpc` | `serviceName`, `mode`, `authority` | `serviceName` should be present and non-empty. Server-side gRPC verifies it, but a server name beginning with `/` may offer alternatives separated by `|`, so the client value can require a selection. `mode` defaults to `gun`; `authority` may be empty. Current core's `multiMode` and several tuning fields are client-only, so an inbound cannot supply them ([Project X gRPC docs](https://xtls.github.io/en/config/transports/grpc.html)). |
| `xhttp` | `path`, `host`, `mode`, `extra` | `path` defaults to `/` and should be emitted. `host` should normally be emitted. `extra` is percent-encoded JSON. The proposal points `mode` and `extra` to the owning xray-core changes [#3994](https://github.com/XTLS/Xray-core/pull/3994) and [#4000](https://github.com/XTLS/Xray-core/pull/4000). `extra` includes client behavior that need not appear on the inbound, so it belongs in advertised metadata. |
| `kcp` | `mtu`, `tti`, optionally `fm` | `mtu` and `tti` may be omitted to use core defaults. Current mKCP removed `header` and `seed` in favor of FinalMask ([Project X mKCP docs](https://xtls.github.io/en/config/transports/mkcp.html)). Emit explicit non-default wire values and any required `fm`. |
| `http` | Historical HTTP/2/3 transport fields are absent | The proposal still lists `http`, but current core rejects `h2`, `h3`, and `http` and directs users to XHTTP ([current parser](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/infra/conf/transport_internet.go#L30-L36)). Do not generate this shape for the current compatibility target. |

### TLS and REALITY query fields

These fields come from the current proposal's [TLS section](https://github.com/XTLS/Xray-core/discussions/716):

| Field | Applies to | Proposal rule | Source of truth |
|---|---|---|---|
| `fp` | TLS and REALITY | TLS ClientHello fingerprint. Omitted means `chrome`; required for REALITY. | Client-only behavior. Advertise it or deliberately adopt the proposal's `chrome` default. The current core default is also `chrome` ([TLS docs](https://xtls.github.io/en/config/transports/tls.html)). |
| `sni` | TLS and REALITY | Client `serverName`. Omitted means remote host; never empty. | Must be advertised when different from host. For REALITY it must select a permitted server name, not copy the list. |
| `alpn` | TLS and REALITY | Comma-separated without spaces; percent-encode the comma. Omitted lets core decide. | Server and client defaults vary by transport. Advertise only when the profile requires an override ([TLS defaults](https://xtls.github.io/en/config/transports/tls.html)). |
| `ech` | TLS | Client `echConfigList`; may be empty. | Client data. Server `echServerKeys` can recover a corresponding config with xray tooling, but DNS discovery and frontend deployment make the intended advertised value a policy choice ([TLS ECH docs](https://xtls.github.io/en/config/transports/tls.html)). |
| `pcs` | TLS | Client `pinnedPeerCertSha256`; may be empty. | Client trust policy. Do not infer it merely because the server has a certificate. |
| `vcn` | TLS | Client `verifyPeerCertByName`; may be empty. | Client trust policy, including domain-fronting cases. Must be advertised if used. |
| `pbk` | REALITY | Client `password`, formerly `publicKey`; required and non-empty. | Deterministically derivable from server `privateKey` with `xray x25519 -i`, but never copy the private key ([REALITY docs](https://xtls.github.io/en/config/transports/reality.html)). |
| `sid` | REALITY | Client `shortId`; may be empty. | Must be one of server `shortIds`. A list does not say which one this profile should advertise. |
| `pqv` | REALITY | Client `mldsa65Verify`; may be empty. | Deterministically derivable from server `mldsa65Seed` with `xray mldsa65 -i` ([command source at `f02a357`](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/main/commands/all/mldsa65.go#L12-L45)). |
| `spx` | REALITY | Client `spiderX`; may be empty and is percent-encoded. | Client-only. Project X recommends a different value per client ([REALITY docs](https://xtls.github.io/en/config/transports/reality.html)). |

Current xray-core has removed `allowInsecure` and points clients to certificate pins and verification names instead ([config parser at `f02a357`](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/infra/conf/transport_security.go#L300-L317), [removal check](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/infra/conf/transport_security.go#L360-L382)). Do not emit ecosystem `allowInsecure` or `insecure` parameters as if they belonged to the current owner proposal.

## Supported inbound-shape matrix

Current xray-core accepts these transport aliases and security combinations. The share proposal can represent only a subset.

| Inbound `streamSettings.network` or `method` | Canonical share `type` | `none` | `tls` | `reality` | Contract status |
|---|---:|---:|---:|---:|---|
| `raw`, `tcp` | `tcp` | Yes | Yes | Yes | Exportable if RAW HTTP obfuscation is absent or represented by current `fm`. |
| `xhttp`, `splithttp` | `xhttp` | Yes | Yes | Yes | Exportable; advertised `host`, `path`, `mode`, and possibly `extra` are needed. |
| `grpc` | `grpc` | Yes | Yes | Yes | Exportable; service selection and client-only mode/authority may require metadata. |
| `ws`, `websocket` | `ws` | Yes | Yes | No | Exportable for none/TLS. |
| `httpupgrade` | `httpupgrade` | Yes | Yes | No | Exportable for none/TLS. |
| `kcp`, `mkcp` | `kcp` | Yes | Yes | No | Exportable when current `mtu`, `tti`, and FinalMask needs fit the proposal. |
| `hysteria` | none | Yes | Yes | No | Not exportable as a standard VLESS proposal link. `hysteria` is accepted by current core and has its own transport authentication, but it is absent from the proposal's `type` values ([Project X Hysteria docs](https://xtls.github.io/en/config/transports/hysteria.html)). |
| `h2`, `h3`, `http`, `quic` | none for current core | No | No | No | Removed from current core; treat as unsupported config for the pinned target. |

The REALITY restriction is enforced in current source: only core `tcp`, `splithttp`, and `grpc` pass configuration build ([`infra/conf/transport_internet.go` at `f02a357`](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/infra/conf/transport_internet.go#L85-L116)). TLS officially supports RAW, XHTTP, mKCP, gRPC, WebSocket, HTTPUpgrade, and Hysteria ([Project X TLS docs](https://xtls.github.io/en/config/transports/tls.html)).

A syntactically representable `security=none&encryption=none` public profile is not a sound default. Project X requires VLESS to use transport security unless the peer and link are private/trusted or VLESS Encryption is enabled ([VLESS inbound docs](https://xtls.github.io/en/config/inbounds/vless.html)). The later contract should either reject this advertised shape or require an explicit acknowledgment that it is private/trusted.

Vision adds another shape rule. `xtls-rprx-vision` works with RAW/TCP plus TLS or REALITY, or with any transport when VLESS Encryption is enabled. Without one of those combinations, the profile is invalid even if every URI field parses ([VLESS flow combinations](https://xtls.github.io/en/config/outbounds/vless.html)). The client-only `xtls-rprx-vision-udp443` variant should not be inferred from the server's `flow`; it is a client choice.

## What the Panel can and cannot derive

### Safe server-derived values

1. **Matching membership.** A normal forward-proxy User appears in each VLESS inbound whose `settings.users` or legacy alias `settings.clients` contains that email. A VLESS User with `reverse` is not a normal connection profile because Project X says `reverse` disables normal forward-proxy use ([VLESS UserObject](https://xtls.github.io/en/config/inbounds/vless.html)).
2. **Canonical UUID.** The share proposal requires a UUID even though xray config accepts a custom UTF-8 string of 1 through 30 bytes. Xray maps the custom value with UUIDv5 using a nil namespace, and the mapping owner explicitly requires subscriptions/share links to publish the mapped UUID rather than the original text ([mapping specification](https://github.com/XTLS/Xray-core/issues/158), [current parser](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/common/uuid/uuid.go#L66-L98)).
3. **Effective inbound flow.** Use `users[].flow` when non-empty; otherwise use inbound `settings.flow`. Current inbound parsing implements that fallback and accepts only empty or `xtls-rprx-vision` ([`infra/conf/vless.go` at `f02a357`](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/infra/conf/vless.go#L33-L78)). Do not derive the outbound-only `-udp443` suffix.
4. **No VLESS Encryption.** `settings.decryption: "none"` safely maps to URI `encryption=none`, or omission under the proposal default.
5. **Server constraints.** Transport, security, path checks, service name checks, accepted REALITY names/IDs, and FinalMask are valid inputs to profile validation. They describe what the inbound accepts. They do not prove how an external client reaches it.
6. **Derived public counterparts.** REALITY `pbk` and optional `pqv` can be calculated without exposing `privateKey` or `mldsa65Seed`; the official xray commands support deterministic input. This is technically unambiguous. The contract still needs to decide whether the Panel may perform these cryptographic derivations or requires administrators to advertise the already-derived values.
7. **Singleton REALITY selections in direct mode.** Exactly one non-empty `serverNames` value and exactly one `shortIds` value give valid `sni` and `sid` selections for a direct listener. Multiple values are ambiguous. An empty server name is also not directly reusable because the client must send an IP placeholder instead ([REALITY field semantics](https://xtls.github.io/en/config/transports/reality.html)). Explicit metadata remains clearer and is mandatory for a fronted profile.

### Values that require advertised connection settings

- **Public host and port.** `listen` may be `0.0.0.0`, `::`, a private address, loopback, or a Unix socket. `port` may be an internal port, environment reference, or range. Xray's own outbound docs say client `address` points to the server and `port` is only "usually" the listening port ([InboundObject](https://xtls.github.io/en/config/inbound.html), [VLESS outbound](https://xtls.github.io/en/config/outbounds/vless.html)). NAT, a CDN, and reverse proxies break any general inference.
- **Advertised transport and security when a frontend participates.** The client may connect with TLS/WebSocket/XHTTP/gRPC to a frontend while xray listens on a different internal transport/security shape. The Panel reads xray config only, not the frontend configuration.
- **TLS `sni`, `host`, ALPN overrides, ECH, pins, and verification name.** Certificates and inbound settings can offer candidates, but they do not identify the intended public route and client trust policy.
- **REALITY selections.** `serverNames[]` and `shortIds[]` are sets accepted by the server. A profile needs one `sni` and one `sid`. `target` is the camouflage/fallback destination for unauthenticated traffic, not the xray server's advertised address ([REALITY field semantics](https://xtls.github.io/en/config/transports/reality.html)). `fp` and `spx` are client settings.
- **VLESS Encryption client string.** Server `decryption` and client `encryption` have different session and padding semantics, and the official `xray vlessenc` command generates them as a pair. Never copy `decryption` into the URI. The command source constructs different server and client strings and key material ([`vlessenc.go` at `f02a357`](https://github.com/XTLS/Xray-core/blob/f02a35786124a6ad046727f2408e32317cc19a41/main/commands/all/vlessenc.go#L23-L36)). Although X25519 and ML-KEM public material can be recomputed from server secrets, explicit advertised `encryption` avoids reimplementing key generation rules and lets the administrator choose client padding and `0rtt` versus `1rtt`.
- **Client-only transport choices.** gRPC `mode`/`authority` and XHTTP `extra` can be absent from or differ from the inbound. A frontend may require them.
- **Label.** The share proposal gives the fragment no derivation rule. The contract should define a stable, readable format.

## Duplicate Users and stable profile identity

Email remains the User identity, but it is not a connection-profile identity. The same email can appear in several VLESS inbounds, with a different UUID, flow, transport, endpoint, or REALITY selection in each. Parsing into a map keyed only by email loses profiles.

Use a separate key for the matching inbound. Xray's `tag` is designed to identify an inbound and must be unique when non-empty ([InboundObject `tag`](https://xtls.github.io/en/config/inbound.html)). The natural derived key is therefore `(User email, inbound tag)`, provided the connection-profile contract requires every exportable VLESS inbound to have a non-empty stable tag.

There is no safe fallback when a tag is absent:

- array index changes when the config is reordered;
- host/port/transport changes during legitimate profile edits;
- UUID can rotate and can also be reused across inbounds;
- hashing the whole inbound turns any edit into a new identity.

The later contract must choose one of two honest options: require a unique inbound tag for export, or require a separate explicit advertised profile ID mapped to the inbound. A config-array index can locate an error message during one parse, but it should not be an API/profile identity.

A User duplicated twice inside the same inbound is also ambiguous. xray's traffic identity is email, while the two entries may carry different IDs. Treat that inbound/User match as invalid for export rather than choosing the first credential.

## Recommended contract boundaries

The later connection-profile contract should decide these points explicitly:

1. **Pin compatibility.** Name the xray-core version floor and snapshot the proposal fields accepted by the Panel. Unknown or client-specific query fields should not silently enter a "standard" link.
2. **Require an advertised endpoint record.** At minimum it needs stable inbound selection, public host, public port, and label policy. For fronted deployments it needs the full advertised client transport/security view rather than patches over the inbound.
3. **Define direct derivation mode, if wanted.** Only an opt-in direct-listener mode should derive transport/security/path fields from the inbound. It must still require public host and port, and it should reject Unix sockets, port ranges, unsupported RAW obfuscation, and incompatible flow/security combinations.
4. **Choose REALITY policy.** Decide whether `pbk` and `pqv` are derived from server secrets or supplied explicitly. Always require explicit selection of `sni` and `sid` when the server offers more than one. Decide whether to use explicit `spx` or permit empty/default behavior.
5. **Choose VLESS Encryption policy.** For non-`none` server decryption, require the complete advertised client `encryption` string unless the project deliberately implements and tests xray's key derivation and client-side padding policy.
6. **Require stable inbound identity.** Prefer a required unique xray inbound tag. If that is too restrictive, add an explicit profile ID and mapping rule. Never use email alone.
7. **Define deterministic serialization.** Fix query order, omission rules, percent-encoding, lower/upper-case normalization, UUID canonicalization, IDNA handling, IPv6 brackets, and fragment format. Validate by parsing the result as a URI and by comparing the displayed string with the exact QR payload.
8. **Define unavailable states by cause.** Useful causes include missing advertisement, duplicate match, unsupported transport, unsupported server/client field, incompatible flow/security, missing REALITY selection, and invalid advertised metadata. Gone Users simply have no profiles, per established scope.

## Unresolved questions for the contract ticket

- Must every exportable inbound have a non-empty unique xray tag, or will advertised metadata introduce its own stable profile ID?
- Does the first release support a direct-listener derivation mode, or does every profile require a complete advertised client view?
- Which representable transports are in the first supported set: RAW, WebSocket, HTTPUpgrade, gRPC, XHTTP, and mKCP, or a smaller tested subset?
- Is FinalMask in scope? If yes, which FinalMask JSON forms and credentials can be put into `fm` without client-specific assumptions?
- Will the Panel derive REALITY `pbk` and `pqv`, or require their public/client forms in advertised metadata?
- When REALITY has several `serverNames` or `shortIds`, how must metadata choose exactly one value for the inbound's single profile, and what unavailable state applies when that choice is missing?
- For VLESS Encryption, is only `none` supported initially, or must advertisements carry the complete paired client `encryption` string?
- Are private/trusted `security=none&encryption=none` profiles permitted? If yes, what explicit opt-in prevents accidental public export?
- What exact fragment label and query ordering become the Panel's canonical byte string?
- What xray-core version floor governs removal of legacy HTTP/QUIC/mKCP fields and the addition of `flow`, VLESS Encryption, TLS pin fields, and REALITY ML-DSA fields?

## Source snapshot

Research checked:

- xray-core commit [`f02a35786124a6ad046727f2408e32317cc19a41`](https://github.com/XTLS/Xray-core/tree/f02a35786124a6ad046727f2408e32317cc19a41)
- Project X docs commit [`090e425873072704d2a631740a4129ce8013c0eb`](https://github.com/XTLS/Xray-docs-next/tree/090e425873072704d2a631740a4129ce8013c0eb)
- Current owner proposal: [XTLS/Xray-core discussion 716](https://github.com/XTLS/Xray-core/discussions/716)
