# nginx Trusted proxy gateway

This directory contains the maintained nginx Authentication gateway matrix for
xform's Trusted proxy authentication mode. Each template is complete and
runnable; it differs only in how the Dashboard is mounted and served:

| Template | Panel route | Dashboard source | xform hop |
| --- | --- | --- | --- |
| `root-embedded.conf.template` | `/` | embedded in xform | `/` over Unix socket |
| `subpath-embedded.conf.template` | `/xform/` | embedded in xform | `/xform/` is stripped |
| `root-static.conf.template` | `/` | `/var/www/xform/dist` | `/` API over Unix socket |
| `subpath-static.conf.template` | `/xform/` | `/var/www/xform/dist` | `/xform/` is stripped |

The public gateway owns OIDC and the browser session through an oauth2-proxy-
compatible `/oauth2/` endpoint. xform receives only its opaque Admission
assertion. It receives no Operator identity, cookie, token, Basic credentials,
or `Authorization` header.

## Host setup

Run xform in Trusted proxy authentication through the protected Unix socket:

```ini
XFORM_AUTH_MODE=trusted_proxy
XFORM_LISTEN=unix:/run/xform/xform.sock
XFORM_TRUSTED_PROXY_SECRET=<the generated Admission secret>
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/oauth2/sign_out
```

Leave `XFORM_PASSWORD` unset. Create the socket directory before starting
xform. The xform service owns the directory; nginx's worker account gets only
shared-group access to traverse it and use the socket:

```sh
groupadd --system xform-gateway
usermod -aG xform-gateway xform
usermod -aG xform-gateway nginx       # replace nginx with the worker account
install -d -o xform -g xform-gateway -m 2750 /run/xform
```

The shipped `xform.service` already allows `/run/xform` in its sandbox. Do not
make the directory world-writable and do not let nginx replace the socket.
Restart nginx after changing its supplementary groups.

The static templates additionally expect the built SPA at
`/var/www/xform/dist`:

```sh
install -d -o root -g xform-gateway -m 2750 /var/www/xform/dist
# copy the contents of web/dist into /var/www/xform/dist
```

Generate independent secrets; never copy a cookie or OIDC client secret into
the Admission slot:

```sh
export XFORM_TRUSTED_PROXY_SECRET="$(openssl rand -hex 32)"
export OAUTH2_PROXY_COOKIE_SECRET="$(openssl rand -base64 32)"
```

The OIDC client secret is a third value, owned by the identity provider
configuration. Keep all three in a root-readable secret store, not in this
repository or command output. oauth2-proxy is not configured by these nginx
files; its listener is expected at `127.0.0.1:4180`.

## oauth2-proxy mount settings

The gateway and oauth2-proxy must use the same public mount. The callback
registered at the identity provider must exactly equal oauth2-proxy's
`redirect_url`.

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

Use HTTPS and the appropriate oauth2-proxy cookie security settings. A
`__Host-` cookie is only valid at `/`; the subpath cookie therefore uses the
`__Secure-` prefix and an explicit `/xform/` path. Set
`XFORM_TRUSTED_PROXY_SIGN_OUT_URL` to the matching local sign-out path when the
Dashboard should offer **Sign out of Panel**:

```ini
# root
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/oauth2/sign_out
# subpath
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/xform/oauth2/sign_out
```

Panel sign-out ends the oauth2-proxy browser session only. Identity-provider
end-session behavior is optional and may end every identity-provider session
represented by the browser cookie; do not enable it as the default.

## Render and load a gateway

Render one template, replacing only its five deployment placeholders. Do not
run unqualified `envsubst`: it would also replace nginx variables such as
`$request_uri` and `$http_host`.

```sh
export NGINX_USER='nginx'              # use www-data on Debian/Ubuntu
export NGINX_LISTEN='443 ssl'
export OAUTH2_PROXY_UPSTREAM='127.0.0.1:4180'
export XFORM_SOCKET='/run/xform/xform.sock'
# Reuse the same value generated in Host setup; do not generate a second one.
: "${XFORM_TRUSTED_PROXY_SECRET:?set the Admission secret first}"

umask 077
rendered=/etc/nginx/nginx.conf
tmp=$(mktemp "${rendered}.XXXXXX") || exit 1
trap 'rm -f "$tmp"' 0 1 2 15
envsubst '${NGINX_USER} ${NGINX_LISTEN} ${OAUTH2_PROXY_UPSTREAM} ${XFORM_SOCKET} ${XFORM_TRUSTED_PROXY_SECRET}' \
  < root-embedded.conf.template > "$tmp" || exit 1
chown root:root "$tmp" && chmod 0600 "$tmp" || exit 1
nginx -t -c "$tmp" || exit 1
mv -f "$tmp" "$rendered" || exit 1
trap - 0 1 2 15
nginx -t && systemctl reload nginx
```

Use `subpath-embedded.conf.template`, `root-static.conf.template`, or
`subpath-static.conf.template` for the other three shapes. The static
examples serve only files below `/var/www/xform/dist`; `/assets/` has an
explicit authenticated 404 for missing files, while other missing documents
use the authenticated SPA fallback. The subpath examples return a trailing-
slash redirect for `/xform` and return 404 outside the declared mount.

Configure TLS in the rendered public vhost (or include these server rules in
an existing TLS vhost). The templates use `Host` and the raw nginx
`$request_uri` so the xform hop preserves the public Host, query string, and
encoded User email bytes. Document sign-in is proxied internally to the
oauth2-proxy `/start` endpoint with `X-Auth-Request-Redirect`, so query
strings are not reparsed as nested `rd` parameters. Only the xform hop strips
`/xform`.

## Gateway contract

- `/oauth2/` is public and belongs to oauth2-proxy; the internal auth
  subrequest cannot be called directly by a browser.
- Every mounted document, asset, and API request runs `auth_request` first.
- A document `401` becomes a same-origin sign-in redirect; an `/api/*` `401`
  remains JSON `401` and never becomes sign-in HTML. The oauth2-proxy version
  must support `X-Auth-Request-Redirect` on `/oauth2/start`.
- Auth-service failures fail closed and never become an Admission.
- After successful auth, nginx overwrites the Admission assertion and clears
  Cookie, Authorization, Basic, forwarding, identity, token, and client-cert
  headers before the Unix-socket xform hop.
- The gateway does not proxy oauth2-proxy's identity or tokens to xform.
- Static files and APIs share one authenticated public origin; no CORS is
  needed.

The rendered nginx configuration contains the Admission secret. Protect it
like any other gateway secret and avoid logging its contents. nginx does not
receive the xform Password, and oauth2-proxy does not receive the Admission
secret as its cookie secret. Pin nginx and oauth2-proxy versions; do not use
floating `latest` images.

## Deterministic smoke matrix

The harness uses a local fake forward-auth server, a fake xform HTTP server on
a Unix socket, and a temporary static build fixture. It needs Docker, curl,
Python 3, and a Linux Docker host network; it never contacts an identity
provider or prints a secret.

```sh
NGINX_IMAGE=nginx:1.29.1-alpine@sha256:42a516af16b852e33b7682d5ef8acbd5d13fe08fecadc7ed98605ba5e3b26ab8 ./smoke-test.sh
```

The pinned image is also set in CI. Each run validates the generated file with
`nginx -t` and exercises all four templates: denial and redirect behavior,
API `401`, public and mount-scoped oauth2 routes, subpath trailing-slash
redirects, cookie paths, static index/deep-link/assets/404s, fail-closed auth
errors, Unix transport, secret injection, credential-header stripping, Host
preservation, and exact encoded URI/query preservation. A fake socket is
permissive only because its process and the nginx container do not share the
real xform gateway group; production xform sockets remain `0660`.
