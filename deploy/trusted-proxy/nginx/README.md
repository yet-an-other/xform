# nginx Trusted proxy gateway

This directory contains the first maintained Authentication gateway shape:
nginx in front of an **embedded, root-mounted** Panel. The gateway owns the
browser session through an oauth2-proxy-compatible `/oauth2/` endpoint; xform
receives only its opaque Admission assertion.

The subpath and proxy-hosted static shapes are separate deployment slices. Do
not add them to this root example; they need mount-specific URI rules.

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
usermod -aG xform-gateway nginx       # use the distro's nginx worker account
install -d -o xform -g xform-gateway -m 2750 /run/xform
```

The shipped `xform.service` already allows `/run/xform` in its sandbox. Do not
make the directory world-writable and do not let nginx replace the socket.
Restart nginx after changing its supplementary groups.

Generate independent secrets; never copy a cookie or OIDC client secret into
the Admission slot:

```sh
export XFORM_TRUSTED_PROXY_SECRET="$(openssl rand -hex 32)"
export OAUTH2_PROXY_COOKIE_SECRET="$(openssl rand -base64 32)"
```

The OIDC client secret is a third value, owned by the identity provider
configuration. Keep all three in a root-readable secret store, not in this
repository or command output. oauth2-proxy is not configured by this example;
its `/oauth2/` listener is expected at `127.0.0.1:4180`.

## Render and load the gateway

`root-embedded.conf.template` is a complete nginx configuration. Render only
its deployment placeholders: unqualified `envsubst` would also replace nginx
variables such as `$request_uri` and `$http_host`.

```sh
export NGINX_LISTEN='443 ssl'
export OAUTH2_PROXY_UPSTREAM='127.0.0.1:4180'
export XFORM_SOCKET='/run/xform/xform.sock'

envsubst '${NGINX_LISTEN} ${OAUTH2_PROXY_UPSTREAM} ${XFORM_SOCKET} ${XFORM_TRUSTED_PROXY_SECRET}' \
  < root-embedded.conf.template > /etc/nginx/nginx.conf
nginx -t && systemctl reload nginx
```

Install the `gettext`/`envsubst` utility if the host does not provide it.
Configure the TLS certificate directives in the rendered file (or include the
server block in the host's TLS vhost). The template's `user nginx;` line must
match the worker account on the host; Debian-family installations commonly use
`www-data` instead.

The public route contract is deliberately narrow:

- `/oauth2/` is public and belongs to oauth2-proxy; `/oauth2/auth` is an nginx
  internal subrequest only.
- Every other document, asset, and API request runs `auth_request` first.
- A document `401` becomes a same-origin `/oauth2/start` redirect.
- An `/api/*` `401` stays a JSON `401`; it never redirects into sign-in HTML.
- Forward-auth errors are not converted into an admission: nginx fails closed.
- After successful auth, nginx overwrites the Admission assertion and clears
  Cookie, Authorization, forwarding, identity, and token headers before the
  Unix-socket xform hop.
- The original `Host` and raw `$request_uri` (including query and encoded
  path bytes) are sent to xform.

The assertion is rendered into nginx's runtime configuration, so protect that
file like any other gateway secret and avoid logging its contents. nginx does
not receive the xform Password and oauth2-proxy does not receive the xform
Admission secret as its cookie secret. Pin nginx and oauth2-proxy versions in
the deployment environment; do not use floating `latest` images.

## Deterministic smoke test

The harness uses a local fake forward-auth server and a fake xform HTTP server
on a Unix socket. It needs Docker, curl, Python 3, and a Linux Docker host
network; it never contacts an identity provider. It generates its Admission
secret at runtime and does not print it.

```sh
NGINX_IMAGE=nginx:1.29.1-alpine@sha256:42a516af16b852e33b7682d5ef8acbd5d13fe08fecadc7ed98605ba5e3b26ab8 ./smoke-test.sh
```

The exact pinned image is also set in CI. The harness runs `nginx -t` before
starting nginx, then proves document redirect, API `401`, public auth routes,
forward-auth failure, denial, Unix-socket admission, secret injection, header
stripping, original Host, and encoded URI/query preservation. A fake socket is
`0666` only because its process and the nginx container do not share the real
xform gateway group; production xform sockets remain `0660`.
