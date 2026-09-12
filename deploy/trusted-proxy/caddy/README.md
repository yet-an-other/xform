# Caddy Trusted proxy gateway

This directory is the maintained Caddy v2 Authentication gateway matrix for
xform's Trusted proxy authentication mode. The four templates cover the two
Dashboard shapes and two mounts:

| Template | Dashboard | Mount | xform hop |
| --- | --- | --- | --- |
| `root-embedded.Caddyfile.template` | embedded | `/` | protected Unix socket |
| `subpath-embedded.Caddyfile.template` | embedded | `/xform/` | protected Unix socket; `/xform` stripped on the hop |
| `root-static.Caddyfile.template` | `/var/www/xform/dist` | `/` | protected Unix socket for API |
| `subpath-static.Caddyfile.template` | `/var/www/xform/dist` | `/xform/` | protected Unix socket for API; `/xform` stripped on the hop |

The examples target **Caddy v2.10.2**. CI pins the official
`caddy:2.10.2-alpine` image by digest and runs `caddy validate` plus the
four-shape smoke matrix. Treat Caddy upgrades as configuration changes: run
the smoke matrix against the new pinned version before deploying it.

## Host setup

Run xform in Trusted proxy authentication through the protected Unix socket:

```ini
XFORM_AUTH_MODE=trusted_proxy
XFORM_LISTEN=unix:/run/xform/xform.sock
XFORM_TRUSTED_PROXY_SECRET=<generated 64-hex Admission secret>
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/oauth2/sign_out
```

Leave `XFORM_PASSWORD` unset. Create the socket directory before starting
xform. Caddy needs group access to use the socket; xform remains the only user
that can replace it:

```sh
groupadd --system xform-gateway
usermod -aG xform-gateway xform
usermod -aG xform-gateway caddy
install -d -o xform -g xform-gateway -m 2750 /run/xform
```

Use the actual Caddy service account on the Host. Restart Caddy after changing
its supplementary groups. The shipped `xform.service` already allows
`/run/xform` in its sandbox.

For static templates, install only the built Dashboard below the configured
root:

```sh
install -d -o root -g xform-gateway -m 2750 /var/www/xform/dist
# copy the contents of web/dist into /var/www/xform/dist
```

Keep this directory dedicated to public build output. Do not place credentials,
config files, or symlinks to private files below it. Caddy's `file_server`
prevents path traversal outside its root, but Caddy v2.10.2 does not make
symlinks a separate sandbox boundary.

Generate independent values for the Admission assertion, the oauth2-proxy
cookie secret, and the OIDC client secret. Never put any of them in this
repository or logs:

```sh
export XFORM_TRUSTED_PROXY_SECRET="$(openssl rand -hex 32)"
export OAUTH2_PROXY_COOKIE_SECRET="$(openssl rand -base64 32)"
```

The OIDC client secret belongs only in the identity-provider/oauth2-proxy
secret store. It is not the xform Admission assertion.

## oauth2-proxy mount settings

The public mount and oauth2-proxy `--proxy-prefix` must agree. Register the
exact HTTPS callback at the identity provider.

Root deployment:

```text
--proxy-prefix=/oauth2
--redirect-url=https://panel.example.com/oauth2/callback
--cookie-name=__Host-xform_oauth2
--cookie-path=/
```

Subpath deployment:

```text
--proxy-prefix=/xform/oauth2
--redirect-url=https://panel.example.com/xform/oauth2/callback
--cookie-name=__Secure-xform_oauth2
--cookie-path=/xform/
```

A `__Host-` cookie is valid only at `/`; the subpath cookie therefore uses the
`__Secure-` prefix and an explicit `/xform/` path. Set the Panel sign-out path
to the matching local route:

```ini
# root
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/oauth2/sign_out
# subpath
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/xform/oauth2/sign_out
```

Panel sign-out clears the oauth2-proxy browser session only. Optional
identity-provider end-session behavior may end every identity-provider session
represented by the browser cookie, so it is not the default.

## Render a template

Render one template, replacing only its markers. Do not run an unqualified
`envsubst`: Caddy request placeholders such as `{http.request.uri}` must stay
in the file. The rendered file contains the Admission assertion and should be owned by
root and readable only by the Caddy service account (typically `root:caddy`
with mode `0640`; use `0600` when Caddy runs as root).

```sh
export CADDY_LISTEN='https://panel.example.com'
export OAUTH2_PROXY_UPSTREAM='127.0.0.1:4180'
export XFORM_UPSTREAM='unix//run/xform/xform.sock'
: "${XFORM_TRUSTED_PROXY_SECRET:?set the Admission secret first}"

umask 077
rendered=/etc/caddy/Caddyfile
tmp=$(mktemp "${rendered}.XXXXXX") || exit 1
trap 'rm -f "$tmp"' 0 1 2 15
python3 - root-embedded.Caddyfile.template <<'PY' >"$tmp"
from pathlib import Path
import os
import sys

source = sys.argv[1]
text = Path(source).read_text()
values = {
    "__CADDY_LISTEN__": os.environ["CADDY_LISTEN"],
    "__OAUTH2_PROXY_UPSTREAM__": os.environ["OAUTH2_PROXY_UPSTREAM"],
    "__XFORM_UPSTREAM__": os.environ["XFORM_UPSTREAM"],
    "__XFORM_TRUSTED_PROXY_SECRET__": os.environ["XFORM_TRUSTED_PROXY_SECRET"],
}
if "__STATIC_ROOT__" in text:
    values["__STATIC_ROOT__"] = "/var/www/xform/dist"
for marker, value in values.items():
    text = text.replace(marker, value)
if any(marker in text for marker in values):
    raise SystemExit("unexpanded Caddy marker")
sys.stdout.write(text)
PY
chown root:caddy "$tmp" && chmod 0640 "$tmp" || exit 1
caddy validate --config "$tmp" --adapter caddyfile || exit 1
mv -f "$tmp" "$rendered" || exit 1
trap - 0 1 2 15
systemctl reload caddy
```

Use `subpath-embedded.Caddyfile.template`, `root-static.Caddyfile.template`,
or `subpath-static.Caddyfile.template` for the other shapes. Keep the rendered
file out of source control. Configure TLS in Caddy or use the template inside
the existing TLS public origin.

## Gateway contract

- `/oauth2/` is public and belongs to oauth2-proxy. The auth-check endpoint is
  not public.
- Every mounted document, asset, and API request runs `forward_auth` first.
- A successful `forward_auth` 2xx continues. A document 401 is proxied to the
  oauth2-proxy `/start` endpoint and becomes a same-origin sign-in redirect;
  an API 401 becomes JSON `401` with no `Location` header.
- Authentication-gateway failures fail closed as `502`; they never create an
  Admission assertion or turn an API request into sign-in HTML.
- The xform hop overwrites `X-Xform-Authenticated` only after admission and
  removes cookies, Authorization/Basic credentials, identity and token
  headers, client-certificate headers, forwarding headers, and the incoming
  assertion. Caddy's automatic `X-Forwarded-*` headers are explicitly deleted.
- The xform hop preserves the original `Host`, query, and escaped request
  target. The subpath templates use `uri path_regexp` on Caddy's escaped path
  instead of `handle_path`, `uri strip_prefix`, or a full URI rewrite; this is
  what keeps an encoded User identity such as `%2F` encoded.
- Unix HTTP upstreams use Caddy's `unix//absolute/path` address syntax. The
  public Caddy process must be in the socket's gateway group.
- Static routes authenticate before lookup; `/assets/*` returns an authenticated
  404 for a missing asset, while other missing documents use the authenticated
  SPA fallback. Directory browsing is not enabled.

The gateway sends no Operator identity, OIDC token, Basic credential, or
`Authorization` header to xform. xform sees only its opaque Admission proof.

## Version-sensitive behavior

These examples rely on Caddy v2.10.2 behavior:

- `forward_auth` continues only for 2xx responses and supports nested
  `handle_response` routes.
- `header_up -Field` and wildcard deletion run after Caddy's implicit
  forwarding-header setup, so the xform hop can fail closed against spoofed
  `X-Forwarded-*` values.
- `uri path_regexp` changes the escaped path while retaining `RawPath`; a
  future Caddy release must keep that property for encoded User identities.
- The sign-in handler's trailing `?` clears the upstream query; the original
  request URI reaches oauth2-proxy only through `X-Auth-Request-Redirect`, so a
  client-supplied `rd` cannot override the gateway's redirect target.
- The Unix upstream spelling is `unix//absolute/path` (two slashes after the
  network name).

Pin the version and rerun `smoke-test.sh` whenever any of these behaviors or
the Caddy image changes.

## Deterministic smoke matrix

The harness uses the shared fake forward-auth/Unix-socket Panel service from
the nginx matrix, a temporary static build fixture, and no identity provider.
It needs Docker, curl, and Python 3:

```sh
CADDY_IMAGE=caddy:2.10.2-alpine@sha256:55cc489b0b057671f28154c12e6efd8dc866439d973c017a384b80e6966859ea \
  ./smoke-test.sh
```

Each case runs the official Caddy validator and exercises denial and document
redirects, API/health `401`, auth failures, public and mount-scoped oauth2
routes, trailing-slash redirects, cookie paths, static index/deep-link/assets
and 404s, protected Unix transport, secret injection, complete credential
header scrubbing, Host preservation, and exact encoded URI/query preservation.
The smoke secret is generated at runtime and is never printed.
