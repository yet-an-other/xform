# Traefik Trusted proxy gateway

This directory contains the maintained Traefik v3 examples for xform's
Trusted proxy authentication mode. The two dynamic file-provider templates
cover the supported Traefik shapes:

| Template | Dashboard | Mount | xform hop |
| --- | --- | --- | --- |
| `root-embedded.yml.template` | embedded in xform | `/` | `127.0.0.1:9090` |
| `subpath-embedded.yml.template` | embedded in xform | `/xform/` | `127.0.0.1:9090`, with `/xform` stripped |

The examples target **Traefik v3.5.3**. CI pins the official image by digest
and starts it ephemerally; Traefik has no validate-only command. Treat a
Traefik upgrade as a configuration change and rerun the smoke matrix against
the new pinned version.

## Transport and shape limits

Traefik has no supported HTTP upstream over a Unix socket. These examples use
a literal loopback TCP xform upstream instead:

```yaml
http:
  services:
    xform:
      loadBalancer:
        passHostHeader: true
        servers:
          - url: "http://127.0.0.1:9090"
```

Run Traefik on the Host or with host networking. An ordinary bridge-network
container cannot reach the Host's loopback listener and must not be used for
this deployment. Keep xform bound to a literal loopback address; do not use a
hostname, wildcard, LAN address, or public listener.

Traefik has no supported local static-file server. The maintained Traefik
shape is therefore embedded xform. For a proxy-hosted static Dashboard, run a
separate static-file service and route it through the same public origin; that
service must apply the same authentication boundary before serving documents
or assets. Do not point a Traefik service at a build directory or expose a
second origin.

## Host setup

Run xform in Trusted proxy authentication on loopback TCP:

```ini
XFORM_AUTH_MODE=trusted_proxy
XFORM_LISTEN=127.0.0.1:9090
XFORM_TRUSTED_PROXY_SECRET=<generated 64-hex Admission secret>
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/oauth2/sign_out
```

Leave `XFORM_PASSWORD` unset. Create the shared Admission secret and xform
systemd handoff described in `../oauth2-proxy/README.md`. Do not generate
another value in this gateway setup. Before rendering Traefik, load that same
file without printing it:

```sh
set -a
. /etc/xform/trusted-proxy.env
set +a
```

The OIDC client and oauth2-proxy cookie secrets are separate values owned by
the oauth2-proxy setup. Keep all secrets out of this repository and logs. For
a subpath mount, set `XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/xform/oauth2/sign_out`.

## oauth2-proxy mount settings

The public mount, oauth2-proxy `--proxy-prefix`, callback, cookie path, and
Panel sign-out path must agree. Register the exact HTTPS callback at the
identity provider.

Root deployment:

```text
--reverse-proxy=true
--upstream=static://404
--proxy-prefix=/oauth2
--redirect-url=https://panel.example.com/oauth2/callback
--cookie-name=__Host-xform_oauth2
--cookie-path=/
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/oauth2/sign_out
```

Subpath deployment:

```text
--reverse-proxy=true
--upstream=static://404
--proxy-prefix=/xform/oauth2
--redirect-url=https://panel.example.com/xform/oauth2/callback
--cookie-name=__Secure-xform_oauth2
--cookie-path=/xform/
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/xform/oauth2/sign_out
```

A `__Host-` cookie is valid only at `/`; the subpath cookie uses the
`__Secure-` prefix and an explicit `/xform/` path. Panel sign-out clears the
oauth2-proxy browser session only. Optional identity-provider end-session
behavior may end every identity-provider session represented by the browser
cookie, so it is not the default.

## Render and run

Render one template, replacing only its three markers. Keep the generated
file out of source control and readable only by the Traefik service account.
The file-provider configuration must be paired with an entry point named
`web`:

```sh
export TRAEFIK_CONFIG=/etc/traefik/xform.yml
export OAUTH2_PROXY_PORT=4180
export XFORM_PORT=9090
: "${XFORM_TRUSTED_PROXY_SECRET:?set the Admission secret first}"
umask 077
python3 - root-embedded.yml.template >"$TRAEFIK_CONFIG" <<'PY'
from pathlib import Path
import os
import sys

text = Path(sys.argv[1]).read_text()
for marker, value in {
    "__OAUTH2_PROXY_PORT__": os.environ["OAUTH2_PROXY_PORT"],
    "__XFORM_PORT__": os.environ["XFORM_PORT"],
    "__XFORM_TRUSTED_PROXY_SECRET__": os.environ["XFORM_TRUSTED_PROXY_SECRET"],
}.items():
    text = text.replace(marker, value)
if "__" in text:
    raise SystemExit("unexpanded Traefik marker")
sys.stdout.write(text)
PY
chmod 0600 "$TRAEFIK_CONFIG"
traefik --entryPoints.web.address=:8080 \
  --providers.file.filename="$TRAEFIK_CONFIG" \
  --providers.file.watch=false
```

Use `subpath-embedded.yml.template` for `/xform/`. Configure TLS in Traefik
or in the enclosing public origin; the examples show plain HTTP so CI can run
them locally.

## Gateway contract

- The oauth2-proxy sign-in, callback, and sign-out endpoints are public under
  the configured mount. The forward-auth check is private and direct requests
  to its path receive 404; do not point a browser at it as a sign-in endpoint.
- `forwardAuth` runs before the Admission header middleware. It accepts only a
  2xx response from oauth2-proxy. A document `401` is handled by the `errors`
  middleware and sent to the same-mount `/oauth2/start` endpoint with Traefik's
  escaped `{url}` value. API and health `401` responses remain `401` and never
  become sign-in HTML. Other authentication failures fail closed as their
  non-2xx response.
- The Admission middleware runs only after successful authentication. It
  overwrites `X-Xform-Authenticated` with the configured secret and removes
  client assertion, identity, token, Cookie, Basic/Authorization, and related
  credential headers. No auth response headers are copied to xform.
- `passHostHeader: true` preserves the original Host. The root service keeps
  the original escaped request target and query. The subpath chain authenticates
  the public URI first, then strips only `/xform`; its remaining escaped path
  bytes and query are sent to xform.
- TLS and public-origin routing are deployment concerns. The xform listener
  remains plain HTTP on loopback, and the gateway must be the only route to it.

## Version-sensitive behavior

These examples rely on Traefik v3.5.3 behavior:

- `forwardAuth` continues only for 2xx responses and receives the generated
  `X-Forwarded-*` request context with `trustForwardHeader: false`.
- The private `noop@internal` router must outrank the public oauth2-proxy
  prefix so the forward-auth check cannot be called by a browser. Its
  `auth-not-found` middleware maps Traefik's noop `418` to `404`. `errors` must
  precede `forward-auth` in the document middleware list so it can replace only
  document authentication `401` responses. It is absent from the API chain,
  which leaves API failures API-shaped.
- `headers.customRequestHeaders` uses an empty value to remove a request
  header. It runs after successful `forwardAuth`; the static secret replaces,
  rather than accepts, a client assertion.
- `stripPrefix` runs after authentication and preserves the path remainder;
  rerun the smoke test after any Traefik upgrade because escaped path
  normalization is security-sensitive.
- `passHostHeader` is explicit on both services. The loopback URL is
  intentionally not a Unix or bridge-network address.

Pin the image and rerun `smoke-test.sh` whenever these behaviors or the image
changes.

## Deterministic smoke matrix

The harness uses the shared fake forward-auth service with its optional TCP
Panel mode. It needs Docker, curl, and Python 3:

```sh
TRAEFIK_IMAGE=traefik:v3.5.3@sha256:d6be8725d21b45bdd84b93ea01438256e0e3c94aa8fa51834fe87f37cd5d4af8 \
  ./smoke-test.sh
```

Each case starts the official image with host networking and checks denial,
document redirect, API/health `401`, non-2xx auth failure, public endpoints,
trailing-slash redirects, callback cookie scope, successful admission,
spoofed assertion and credential-header removal, Host preservation, and
exact encoded URI/query preservation. Secrets are generated at runtime and
never printed. No live identity provider is needed.
