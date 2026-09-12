# ZITADEL + oauth2-proxy for xform

This directory is the maintained identity-provider setup for xform's Trusted
proxy authentication mode. oauth2-proxy owns OIDC and its browser session;
nginx, Caddy, or Traefik owns the public route and sends xform only its opaque
Admission assertion. oauth2-proxy never proxies the Panel: its upstream is
`static://404` and the Authentication gateway makes the final xform request.

The templates target **oauth2-proxy v7.15.4**. CI runs that exact release from
its pinned Quay image and executes `--config-test` for both mount variants.
Treat an oauth2-proxy upgrade as a security-sensitive configuration change:
review every option, update the digest, and rerun the gateway smoke tests.

## Files

| File | Purpose |
| --- | --- |
| `root.cfg.template` | Root deployment: `/oauth2`, root cookie, exact root callback |
| `subpath.cfg.template` | `/xform/` deployment: `/xform/oauth2`, mount-scoped cookie and callback |
| `authenticated-emails.txt.example` | Safe deny-by-default starting fixture for the exact-email file |
| `render-config.sh` | Replaces only the public origin, issuer, and client ID |
| `config-test.sh` | Renders and validates both templates in a network-isolated pinned image |
| `xform-oauth2-proxy.service` | Hardened systemd unit for the selected `active.cfg` |
| `manual-acceptance.md` | Real-tenant acceptance checklist |

The root and subpath templates are alternatives, not two providers to run at
once. Their cookie names and paths deliberately prevent a root session from
being sent to `/xform/`, or a subpath session from being sent to another path.

## Service account prerequisite

Create the service account and group before installing any files owned by
`oauth2-proxy`:

```sh
groupadd --system oauth2-proxy
useradd --system --gid oauth2-proxy --home-dir /nonexistent \
  --shell /usr/sbin/nologin oauth2-proxy
```

## ZITADEL application

Create one ZITADEL application in the project used for the Panel:

1. Select **Web Application**.
2. Select the **Authorization Code** grant.
3. Select client authentication **BASIC** (`client_secret_basic`). Do not put
   the generated client secret in this repository, a command argument, or a
   gateway configuration.
4. Enable PKCE and use **S256**. Do not use `plain`.
5. Register exactly one of these HTTPS callbacks, matching the selected
   template and its `redirect_url` byte-for-byte:

   ```text
   https://panel.example.com/oauth2/callback
   https://panel.example.com/xform/oauth2/callback
   ```

   Replace only the example public origin. Do not add a trailing slash,
   alternate scheme, alternate host, wildcard, or a second mount.
6. Use the ZITADEL issuer URL as `oidc_issuer_url` and leave discovery enabled.
   The issuer's `/.well-known/openid-configuration` document must be reachable
   over HTTPS and return the same issuer URL. Keep issuer and TLS verification
   enabled; do not use development mode for a production Panel.
7. Request exactly `openid email` for the Panel. The templates read the
   `email` claim and require ZITADEL's `email_verified` claim; an unverified
   email is rejected by oauth2-proxy.

The templates intentionally do not configure `email_domains`, `allowed_groups`,
or `allowed_roles`. A domain rule would admit every address in that domain,
not an explicit roster. A ZITADEL Action that emits a **flat string-array** role
claim can be used as a separately reviewed alternative with `allowed_groups`,
but it is not this setup and ZITADEL's native role map must not be treated as
that claim.

## Exact-email allowlist

Copy `authenticated-emails.txt.example` to the path named by the config and
replace its reserved line with one exact, verified address per line. Keep the
file root-owned, readable only by the oauth2-proxy service account, and out of
public build directories:

```sh
install -d -o root -g oauth2-proxy -m 0750 /etc/oauth2-proxy/xform
install -o root -g oauth2-proxy -m 0640 \
  authenticated-emails.txt.example \
  /etc/oauth2-proxy/xform/authenticated-emails.txt
# Edit the installed file with a secret-safe editor, then remove the
# operator@example.invalid fixture line and add exact verified addresses.
```

`operator@example.invalid` is a reserved non-routable fixture and admits no
real Operator. Leaving it in place is safe but intentionally denies everyone.
Do not replace it with `*`, a domain, a role name, an identity-provider group,
or an unverified address. An address absent from the file — including an
otherwise verified address at the same domain — is denied.

oauth2-proxy also verifies the OIDC email before applying this file. The
configuration keeps `insecure_oidc_allow_unverified_email = false`; an
unverified token cannot become admitted by appearing in the file.

## Three independent secrets

Use three different generated values:

1. **Admission assertion** — 32 random bytes rendered as 64 lowercase hex for
   `XFORM_TRUSTED_PROXY_SECRET` and the final gateway's
   `X-Xform-Authenticated` header. It is not an oauth2-proxy secret.
2. **oauth2-proxy cookie secret** — 32 random raw bytes in
   `/etc/oauth2-proxy/xform/cookie-secret`; `cookie_secret_file` requires 16,
   24, or 32 bytes.
3. **ZITADEL client secret** — the secret generated by the ZITADEL Web
   application, written without a trailing newline to
   `/etc/oauth2-proxy/xform/zitadel-client-secret`.

Generate the Admission assertion and keep the one shared copy in a root-only
systemd environment file. The xform service and the operator rendering the
public gateway both consume this file; never generate a second assertion in a
gateway shell:

```sh
install -d -o root -g root -m 0750 /etc/xform
umask 077
{
  printf 'XFORM_TRUSTED_PROXY_SECRET='
  openssl rand -hex 32 | tr -d '\n'
  printf '\n'
} > /etc/xform/trusted-proxy.env
chown root:root /etc/xform/trusted-proxy.env
chmod 0600 /etc/xform/trusted-proxy.env

# Add to a systemctl edit xform drop-in:
# [Service]
# Environment=XFORM_AUTH_MODE=trusted_proxy
# EnvironmentFile=/etc/xform/trusted-proxy.env

# Load the same file before rendering nginx, Caddy, or Traefik. This does not
# print the value and the rendered gateway file must remain root-protected.
set -a
. /etc/xform/trusted-proxy.env
set +a

openssl rand 32 > /etc/oauth2-proxy/xform/cookie-secret
chown root:oauth2-proxy /etc/oauth2-proxy/xform/cookie-secret
chmod 0640 /etc/oauth2-proxy/xform/cookie-secret
```

Place the ZITADEL client secret through a secret manager or a prompt, not in a
shell command, log, unit file, or repository file. After it writes the file,
make it readable by only root and the oauth2-proxy service group:

```sh
chown root:oauth2-proxy /etc/oauth2-proxy/xform/zitadel-client-secret
chmod 0640 /etc/oauth2-proxy/xform/zitadel-client-secret
```

The service account needs read access to the two oauth2-proxy file secrets. No
gateway needs the ZITADEL client secret, and oauth2-proxy does not need the xform
Admission secret beyond the xform process environment.

The rendered config contains only paths to those files. `render-config.sh`
never receives or substitutes either secret.

## Install oauth2-proxy

Install the reviewed oauth2-proxy v7.15.4 binary (or run the same pinned
release in a container). Verify the release checksum using the checksum file
published with the release before installing it as `/usr/local/bin/oauth2-proxy`.
The dedicated unprivileged account must already exist (see the prerequisite
above):

```sh
install -d -o root -g root -m 0750 /etc/oauth2-proxy/xform
# Install the verified v7.15.4 binary at /usr/local/bin/oauth2-proxy.
chmod 0755 /usr/local/bin/oauth2-proxy
```

The account must be able to read the rendered config, client-secret file,
cookie-secret file, and exact-email file. The unit does not need access to the
xform Admission secret.

## Render and install one configuration

Create the three input files first, then render the selected mount as
`active.cfg`:

```sh
export XFORM_PUBLIC_ORIGIN='https://panel.example.com'
export ZITADEL_ISSUER_URL='https://your-tenant.zitadel.cloud'
export ZITADEL_CLIENT_ID='the-client-id-from-zitadel'

./render-config.sh root /etc/oauth2-proxy/xform/active.cfg
# Or, for a Panel mounted at /xform/:
# ./render-config.sh subpath /etc/oauth2-proxy/xform/active.cfg

chown root:oauth2-proxy /etc/oauth2-proxy/xform/active.cfg
chmod 0640 /etc/oauth2-proxy/xform/active.cfg
```

The renderer requires an HTTPS origin and an HTTPS issuer without a path,
query, or fragment. It writes atomically with mode `0600`; the explicit
`chown`/`chmod` above gives only the service account read access. Keep the
rendered config and the input files outside git. Do not use unqualified
`envsubst` or substitute secrets into the TOML.

Install the unit and select the same config shape used by the public gateway:

```sh
install -o root -g root -m 0644 xform-oauth2-proxy.service \
  /etc/systemd/system/xform-oauth2-proxy.service
systemctl daemon-reload
systemctl enable --now xform-oauth2-proxy.service
```

The unit listens only on `127.0.0.1:4180`. Protect that port from public
access. The public gateway's auth check must point to the selected
`/oauth2/auth` or `/xform/oauth2/auth` endpoint, and the gateway must be the
only process that can reach xform's protected listener. Follow the matching
gateway README for Unix-socket group setup (nginx/Caddy) or loopback
host-network setup (Traefik).

## Mount contract

| Deployment | `proxy_prefix` | `redirect_url` | Cookie name | Cookie path | Panel sign-out |
| --- | --- | --- | --- | --- | --- |
| Root | `/oauth2` | `https://HOST/oauth2/callback` | `__Host-xform_oauth2` | `/` | `/oauth2/sign_out` |
| `/xform/` | `/xform/oauth2` | `https://HOST/xform/oauth2/callback` | `__Secure-xform_oauth2` | `/xform/` | `/xform/oauth2/sign_out` |

`__Host-` requires HTTPS, no `Domain` attribute, and `Path=/`; the subpath
cookie therefore uses `__Secure-` with its explicit `/xform/` path. Keep
`cookie_secure = true`, `cookie_httponly = true`, and `cookie_samesite = "lax"`.
The xform Panel's corresponding setting is:

```ini
# root
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/oauth2/sign_out
# subpath
XFORM_TRUSTED_PROXY_SIGN_OUT_URL=/xform/oauth2/sign_out
```

Panel sign-out is local by default: oauth2-proxy clears its browser session,
then the browser returns to the Panel. A surviving ZITADEL session may let the
browser sign in again immediately. That is expected and is not a failure of
Panel sign-out.

## Header and upstream boundary

Both templates set every credential or identity forwarding control explicitly:

```toml
pass_access_token = false
pass_authorization_header = false
pass_basic_auth = false
pass_user_headers = false
set_xauthrequest = false
set_authorization_header = false
set_basic_auth = false
skip_auth_strip_headers = false
```

Access tokens, ID tokens, Basic credentials, Authorization, and identity
headers are not sent to an upstream. `session_cookie_minimal = true` also
keeps unnecessary OAuth tokens out of the oauth2-proxy cookie. The final
nginx/Caddy/Traefik hop independently removes client-supplied credential and
identity headers and overwrites `X-Xform-Authenticated` only after a successful
`/auth` response. A client cannot create an Admission by sending a header to
either component.

`trusted_proxy_ips` is restricted to loopback because `reverse_proxy = true`
allows oauth2-proxy to use forwarded request context. Do not expose
oauth2-proxy's port or widen that list without reviewing the trust boundary.

## Optional identity-provider logout

Do **not** set `backend_logout_url` in the default config. The default
`/oauth2/sign_out` or `/xform/oauth2/sign_out` action is local Panel sign-out.
A server-side oauth2-proxy backend request is not a substitute for the browser
redirect required by ZITADEL's OIDC end-session flow.

If a deployment deliberately adds RP-initiated ZITADEL logout, it must register
the exact post-logout redirect URI in ZITADEL and construct a browser redirect
to the discovered `end_session_endpoint` with a correctly URL-encoded
`post_logout_redirect_uri` and either a valid `id_token_hint` or the client ID.
Never log the hint or place it in a static config. Label the action clearly:
ZITADEL may end **every ZITADEL session represented by the current browser
cookie**, not just xform's oauth2-proxy session. This broader behavior is
optional and is not enabled by these files.

## Validation and CI

Run the deterministic config check from the repository root:

```sh
OAUTH2_PROXY_IMAGE='quay.io/oauth2-proxy/oauth2-proxy:v7.15.4@sha256:b1b2021fe8f4004573e8d690dec6c7bb29cc44364572cf8510a05bf3a0ae2ded' \
  ./config-test.sh
```

It generates secrets in a temporary directory, renders both templates with
reserved `.invalid` endpoints, mounts the files read-only, disables container
networking, and runs:

```text
oauth2-proxy --config=/etc/oauth2-proxy/xform/root.cfg --config-test
oauth2-proxy --config=/etc/oauth2-proxy/xform/subpath.cfg --config-test
```

No live identity provider is used by CI. The gateway smoke matrices remain the
place to test admission, API-vs-document failures, header scrubbing, cookies,
and encoded paths. Pin all gateway images by digest as well.

For real-tenant checks, use [`manual-acceptance.md`](manual-acceptance.md).
