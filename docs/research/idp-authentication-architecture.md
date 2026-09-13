# Trusted proxy authentication architecture

**Decision confirmed 2026-09-11:** xform will keep Password authentication as its default and add an explicit, provider-neutral `trusted_proxy` mode. Deployments that need an identity provider will put an Authentication gateway in front of xform. The supported example uses oauth2-proxy for OIDC and nginx, Caddy, or Traefik for public routing.

The gateway authenticates and authorizes Operators. xform does not implement OIDC, receive identity claims or tokens, create Operator accounts, or assign roles. Every admitted Operator has full authority over the Panel.

> Source note: external specifications and first-party documentation were checked on 2026-09-11. Gateway flags and configuration fields change between releases. The shipped examples must pin and test exact versions.

## Why this fits xform

xform runs on one Host beside xray. It serves one same-origin dashboard and JSON API, either from its embedded web server or with the static dashboard hosted by a reverse proxy ([ADR-0001](../adr/0001-two-same-origin-deployment-shapes.md), [SPEC](../../SPEC.md)). It currently has one shared password, no Operator identity, no roles, and no audit trail keyed by Operator.

Native OIDC would make xform responsible for discovery, authorization callbacks, PKCE, state and nonce checks, token and key validation, claims, refresh, provider compatibility, and logout. Those duties would add no current authorization value because every Operator can perform every action. They belong in oauth2-proxy unless Operator identity later drives xform-owned permissions or audit.

The project name is **oauth2-proxy**. "Authentication gateway" is xform's generic architecture term; oauth2-proxy is one component in the supported deployment.

## Trust contract

```text
browser -- HTTPS --> nginx, Caddy, or Traefik
                       |-- authentication endpoints --> oauth2-proxy --> ZITADEL
                       |-- forward-auth check --------> oauth2-proxy
                       `-- admitted request ----------> xform
```

The gateway must protect the whole Panel origin. Authentication endpoints are public. Document requests without a valid gateway session redirect to sign-in. `/api/*` requests return `401` instead of redirecting to HTML. The public gateway also protects `/api/v1/healthz`, although xform leaves that endpoint assertion-free on its private listener.

After oauth2-proxy admits a request, the gateway overwrites this header before forwarding to xform:

```http
X-Xform-Authenticated: <64-lowercase-hex-secret>
```

`XFORM_TRUSTED_PROXY_SECRET` holds the same 256-bit secret. Generate it with:

```sh
openssl rand -hex 32
```

This Admission assertion identifies no Operator. It is separate from the oauth2-proxy cookie secret and the OIDC client secret. The gateway must remove client-supplied copies before setting it and must not forward `Authorization`, access tokens, ID tokens, or identity headers to xform.

xform requires exactly one assertion header, hashes its value, compares it in constant time, and returns the same JSON `401` for a missing, duplicate, or wrong value. Every xform-generated authentication failure includes `authentication_mode`; a gateway-generated `401` may not. It removes the header before application handlers run and never logs it.

## Authentication modes and startup validation

`XFORM_AUTH_MODE` accepts `password` or `trusted_proxy` and defaults to `password`.

### Password authentication

- `XFORM_PASSWORD` is required.
- Trusted-proxy-only settings are rejected.
- Existing password login, sliding 24-hour Session, cookie, logout, and restart behavior stay unchanged.
- Existing TCP `XFORM_LISTEN` values remain compatible. The new Unix-listener syntax is also available.

### Trusted proxy authentication

- `XFORM_PASSWORD` is rejected. There is no fallback when the gateway or identity provider fails.
- `XFORM_TRUSTED_PROXY_SECRET` is required and must contain exactly 64 lowercase hexadecimal characters.
- `XFORM_TRUSTED_PROXY_SIGN_OUT_URL` is optional. When present, it must be a same-origin absolute path beginning with one `/`; it may have a query, but no scheme, host, fragment, control character, backslash, or leading `//`.
- `XFORM_LISTEN` may be `unix:/absolute/path` or an IPv4 or IPv6 loopback TCP address. Hostnames, wildcard addresses, LAN addresses, and public addresses are rejected in this mode.
- A TCP request must also have a direct loopback peer. xform never uses forwarding headers to establish trust.
- `/api/v1/healthz` is the only assertion-free xform route. The dashboard and every other API route require the assertion.
- Password login and logout routes are not registered.

nginx and Caddy use a Unix socket in the examples. xform creates the socket as `0660` in an existing protected directory, removes it on clean shutdown, and replaces only a stale socket owned by its runtime user. It refuses regular files, symlinks, foreign-owned sockets, and live listeners. The gateway user receives access through a shared group.

Traefik cannot proxy an HTTP backend over a Unix socket. Its official [http+unix backend issue](https://github.com/traefik/traefik/issues/4881) remains open. The Traefik example therefore uses loopback TCP plus the Admission assertion. A containerized Traefik must use host networking to retain that loopback-only contract.

## HTTP and browser behavior

The Authentication gateway owns sign-in redirects. xform never knows the sign-in URL and always returns `401` when admission fails.

`GET /api/v1/panel` reports:

```json
{
  "version": "v0.10.0",
  "uptime_seconds": 4831,
  "authentication_mode": "trusted_proxy",
  "sign_out_url": "/oauth2/sign_out"
}
```

`authentication_mode` is always present. `sign_out_url` is omitted when no Panel sign-out is configured.

Every xform-generated authentication `401` includes `authentication_mode`. A `password` failure opens the password login flow. A `trusted_proxy` failure reloads the current document once so the gateway can start sign-in. A gateway-generated `401` may omit the mode and receives the same one-reload treatment. A repeated failure shows an Authentication gateway configuration error rather than reloading forever or offering a password.

When `sign_out_url` exists, the dashboard shows **Sign out of Panel** and performs full-page navigation to it. When absent, the action is hidden. Panel sign-out ends the oauth2-proxy browser session only. It does not promise to end the identity-provider session.

The current Origin and `Sec-Fetch-Site` mutation checks remain active in both modes. External authentication does not replace CSRF protection. Gateways must preserve the original `Host` and URI, including encoded User email path segments and subpath mounts.

## Gateway support

| Gateway | xform transport | Embedded dashboard | Proxy-hosted static dashboard |
| --- | --- | --- | --- |
| nginx | Unix socket | root and subpath | root and subpath |
| Caddy v2 | Unix socket | root and subpath | root and subpath |
| Traefik | loopback TCP | root and subpath | requires a separate static-file service routed through the same public origin |

Caddy supports Unix upstreams using `unix//absolute/path` and can overwrite the Admission assertion with `header_up`. Its `forward_auth` flow can leave API failures as `401` while document requests redirect.

Traefik supports forward authentication, error handling, request-header replacement, and path-preserving HTTP services. It has no native local static-file server. Its [static-file server request](https://github.com/traefik/traefik/issues/4240) was declined, so xform's embedded deployment is the maintained Traefik shape.

The implementation will keep explicit, testable files under `deploy/trusted-proxy/`:

- nginx: embedded and static, each at root and subpath;
- Caddy: embedded and static, each at root and subpath;
- Traefik: embedded at root and subpath;
- oauth2-proxy with ZITADEL: root and subpath;
- shared setup, secret, systemd, failure, and upgrade guidance.

xform does not install, start, or upgrade any Authentication gateway.

## Complete ZITADEL example contract

The maintained identity-provider example uses one ZITADEL Project with a **Web Application** configured in the console for:

- the **Code** flow preset, which sets the authentication method **BASIC** (`client_secret_basic`) and the Authorization Code grant;
- an exact HTTPS callback URI.

The remaining contract values are oauth2-proxy configuration, not ZITADEL application settings: issuer discovery from the ZITADEL issuer, scopes `openid email`, verified email, a deny-by-default exact-email allowlist, and PKCE `S256` via `code_challenge_method`. The console's PKCE preset is a different choice entirely: a public client with authentication method None and no secret, which oauth2-proxy cannot use because it refuses to start without a `client_secret` (oauth2-proxy issue 2929). The proxy sends the challenge itself, and ZITADEL honors the per-request challenge for confidential clients — ZITADEL documents Web apps as "Authorization Code Flow (PKCE recommended) with a Client Secret".

Do not select Native, User Agent, API, implicit, device, or client-credentials flows. Do not enable ZITADEL's development mode in production.

Root mount callback and endpoints:

```text
Callback: https://panel.example.com/oauth2/callback
Prefix:   /oauth2
Sign-out: /oauth2/sign_out
Cookie:   __Host-xform_oauth2, Path=/
```

Subpath mount at `/xform/`:

```text
Callback: https://host.example.com/xform/oauth2/callback
Prefix:   /xform/oauth2
Sign-out: /xform/oauth2/sign_out
Cookie:   __Secure-xform_oauth2, Path=/xform/
```

The ZITADEL callback must exactly equal oauth2-proxy's `redirect_url`. The issuer is the ZITADEL issuer origin, with discovery at `/.well-known/openid-configuration`.

The oauth2-proxy template explicitly requests `openid email`, keeps issuer and TLS checks enabled, uses `code_challenge_method="S256"`, and leaves `insecure_oidc_allow_unverified_email=false`. It uses `authenticated_emails_file` with one verified email per line. Domain allowlisting and a ZITADEL Action that emits a flat role array are documented alternatives, not defaults. ZITADEL's native role map must not be treated as oauth2-proxy's string-array group claim.

The template disables identity and token forwarding:

```toml
pass_access_token = false
pass_authorization_header = false
pass_basic_auth = false
pass_user_headers = false
set_xauthrequest = false
set_authorization_header = false
set_basic_auth = false
skip_auth_strip_headers = true
```

oauth2-proxy does not proxy to xform. A `static://404` upstream catches accidental non-endpoint traffic; the gateway performs the final xform request after a successful `/auth` check.

ZITADEL's official oauth2-proxy guide was tested with oauth2-proxy 7.4.0 and enables access-token forwarding. The xform example must not copy that setting. Its CI pins a reviewed oauth2-proxy release and validates the exact stable configuration fields used.

### ZITADEL logout

The default example uses oauth2-proxy local sign-out only. A returning browser may immediately sign in through its surviving ZITADEL session.

Federated ZITADEL logout is optional and requires a registered Post Logout Redirect URI plus a correctly encoded browser redirect to ZITADEL's end-session endpoint. ZITADEL documents that this may end every ZITADEL session represented by the current browser cookie. The example must label this broader effect and must not enable it by default. Server-side oauth2-proxy backend logout is not equivalent because ZITADEL's end-session flow depends on the browser cookie.

## Code shape

A deep `internal/auth` module owns both Authentication modes. Its interface is one HTTP wrapper plus immutable public mode information. It hides password Sessions, Admission assertion checks, mode-specific login and logout routes, health exemption, and unauthorized responses.

`internal/api` no longer depends on a session-manager interface. It owns application routes and receives only the authentication fields returned by `GET /api/v1/panel`. The existing `internal/session` implementation moves behind `internal/auth`; session expiry and revocation are tested through the auth module's HTTP interface.

A separate `internal/listener` module opens TCP or Unix listeners and owns safe Unix-socket cleanup. `main` calls `http.Server.Serve` with that listener. `internal/config` parses typed listener and authentication values and rejects every unsafe cross-field combination before stores, watchers, xray clients, or HTTP listeners start.

## Verification

Go and frontend tests cover:

- the complete startup-validation matrix;
- Password authentication compatibility;
- missing, duplicate, malformed, and wrong assertions;
- assertion removal before application handlers;
- Unix-socket ownership and stale-path failures;
- loopback listener and direct-peer enforcement;
- health, dashboard, login, logout, and catch-all route policy;
- retained Origin and `Sec-Fetch-Site` checks;
- authentication metadata and optional Panel sign-out;
- one-time reload after trusted-mode `401` and repeated-failure UI;
- root/subpath URI preservation and encoded User emails.

Gateway smoke tests use a deterministic fake forward-auth server rather than a live ZITADEL tenant. They test denial, document redirects, API `401`, spoofed-header replacement, secret forwarding, Host/URI preservation, and each supported transport and mount. CI pins gateway versions. It runs nginx and Caddy validators, oauth2-proxy `--config-test` on versions that provide it, and starts Traefik ephemerally because Traefik has no validate-only command.

The ZITADEL guide includes manual acceptance checks for callback, exact-email denial, unverified-email denial, Session expiry, sign-out, and root/subpath cookies. The project does not claim automatic interoperability with every IdP release.

## Rejected designs

- **Direct OIDC in xform:** too much protocol and provider behavior for a Panel with no Operator-level authorization.
- **oauth2-proxy-specific xform code:** couples the Panel to one gateway and claim format.
- **Identity headers:** xform has nowhere to use Operator identity and should not receive personal data without a purpose.
- **Fixed public marker such as `X-Xform-Authenticated: 1`:** forgeable by another local process, especially for Traefik's TCP upstream.
- **Unix-only ingress:** stronger transport isolation, but incompatible with Traefik's supported HTTP backends.
- **Password fallback in trusted mode:** converts gateway failure into an authentication bypass.
- **API redirects to sign-in:** fetch follows them into HTML or cross-origin failures; API calls must receive `401`.
- **Live ZITADEL in CI:** high maintenance and still not proof of every production tenant policy.

## Primary sources

- [OpenID Connect Core](https://openid.net/specs/openid-connect-core-1_0.html)
- [OpenID Connect Discovery](https://openid.net/specs/openid-connect-discovery-1_0.html)
- [OpenID Connect RP-Initiated Logout](https://openid.net/specs/openid-connect-rpinitiated-1_0.html)
- [oauth2-proxy configuration](https://oauth2-proxy.github.io/oauth2-proxy/configuration/overview/)
- [oauth2-proxy endpoints](https://oauth2-proxy.github.io/oauth2-proxy/features/endpoints/)
- [oauth2-proxy nginx integration](https://oauth2-proxy.github.io/oauth2-proxy/configuration/integrations/nginx/)
- [oauth2-proxy Caddy integration](https://oauth2-proxy.github.io/oauth2-proxy/configuration/integrations/caddy)
- [oauth2-proxy Traefik integration](https://oauth2-proxy.github.io/oauth2-proxy/configuration/integrations/traefik)
- [Caddy `forward_auth`](https://caddyserver.com/docs/caddyfile/directives/forward_auth)
- [Caddy `reverse_proxy`](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
- [Traefik ForwardAuth](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- [Traefik HTTP services](https://doc.traefik.io/traefik/reference/routing-configuration/http/load-balancing/service/)
- [Traefik issue 4881, Unix backend support](https://github.com/traefik/traefik/issues/4881)
- [Traefik issue 4240, static files](https://github.com/traefik/traefik/issues/4240)
- [ZITADEL oauth2-proxy example](https://zitadel.com/docs/examples/identity-proxy/oauth2-proxy)
- [ZITADEL OIDC login and PKCE](https://zitadel.com/docs/guides/integrate/login/oidc/login-users)
- [ZITADEL OIDC endpoints](https://zitadel.com/docs/apis/openidoauth/endpoints)
- [ZITADEL scopes](https://zitadel.com/docs/apis/openidoauth/scopes)
- [ZITADEL claims](https://zitadel.com/docs/apis/openidoauth/claims)
- [ZITADEL logout](https://zitadel.com/docs/guides/integrate/login/oidc/logout)
- [nginx `auth_request`](https://nginx.org/en/docs/http/ngx_http_auth_request_module.html)
- [nginx `proxy_set_header`](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_set_header)

## Remaining risks

- Gateway examples still need implementation and executable CI coverage.
- No live ZITADEL tenant was tested during this design session.
- A process that reads the Admission assertion secret has full Panel authority.
- A gateway configuration that injects the assertion before successful forward authentication fails open. Smoke tests must prove middleware and route order.
- Traefik's loopback requirement excludes ordinary bridge-network containers; use host networking or another supported gateway.

## Secret and private-data inspection

This report contains no credentials, tokens, cookies, private hostnames, personal email addresses, or private IP addresses. Values are public examples, loopback addresses, or explicit placeholders.
