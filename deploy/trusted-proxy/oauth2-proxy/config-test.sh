#!/bin/sh
# Validate both rendered oauth2-proxy configurations in the pinned image.
# No identity-provider request is made: config-test validates the file, and the
# isolated container has no network access.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
image=${OAUTH2_PROXY_IMAGE:-quay.io/oauth2-proxy/oauth2-proxy:v7.15.4@sha256:b1b2021fe8f4004573e8d690dec6c7bb29cc44364572cf8510a05bf3a0ae2ded}

case "$image" in
    *@sha256:????????????????????????????????????????????????????????????????) ;;
    *)
        printf 'OAUTH2_PROXY_IMAGE must use a 64-character sha256 digest\n' >&2
        exit 2
        ;;
esac
digest=${image##*@sha256:}
case "$digest" in
    *[!0123456789abcdef]*|"")
        printf 'OAUTH2_PROXY_IMAGE digest must be lowercase hexadecimal\n' >&2
        exit 2
        ;;
esac

for command in docker python3; do
    command -v "$command" >/dev/null 2>&1 || {
        printf 'missing required command: %s\n' "$command" >&2
        exit 1
    }
done

python3 - "$root/root.cfg.template" "$root/subpath.cfg.template" <<'PY'
from pathlib import Path
import sys
import tomllib

expected = {
    "root.cfg.template": {
        "proxy_prefix": "/oauth2",
        "cookie_name": "__Host-xform_oauth2",
        "cookie_path": "/",
    },
    "subpath.cfg.template": {
        "proxy_prefix": "/xform/oauth2",
        "cookie_name": "__Secure-xform_oauth2",
        "cookie_path": "/xform/",
    },
}

for filename in sys.argv[1:]:
    path = Path(filename)
    config = tomllib.loads(path.read_text(encoding="utf-8"))
    for key, value in expected[path.name].items():
        if config.get(key) != value:
            raise SystemExit(f"{path.name}: unexpected {key}")
    required = {
        "provider": "oidc",
        "provider_display_name": "ZITADEL",
        "scope": "openid email",
        "code_challenge_method": "S256",
        "oidc_issuer_url": "__ZITADEL_ISSUER_URL__",
        "client_id": "__ZITADEL_CLIENT_ID__",
        "redirect_url": "__XFORM_REDIRECT_URL__",
        "upstreams": ["static://404"],
        "trusted_proxy_ips": ["127.0.0.1/32", "::1/128"],
    }
    for key, value in required.items():
        if config.get(key) != value:
            raise SystemExit(f"{path.name}: unexpected {key}")
    booleans = {
        "insecure_oidc_skip_issuer_verification": False,
        "insecure_oidc_allow_unverified_email": False,
        "insecure_oidc_skip_nonce": False,
        "skip_oidc_discovery": False,
        "skip_jwt_bearer_tokens": False,
        "cookie_secure": True,
        "cookie_httponly": True,
        "session_cookie_minimal": True,
        "pass_access_token": False,
        "pass_authorization_header": False,
        "pass_basic_auth": False,
        "pass_user_headers": False,
        "set_xauthrequest": False,
        "set_authorization_header": False,
        "set_basic_auth": False,
    }
    for key, value in booleans.items():
        if config.get(key) != value:
            raise SystemExit(f"{path.name}: unexpected {key}")
    if config.get("skip_auth_strip_headers") is not False:
        raise SystemExit(f"{path.name}: auth-header stripping must stay enabled")
    if config.get("client_secret_file") == config.get("cookie_secret_file"):
        raise SystemExit(f"{path.name}: client and cookie secret files must differ")
    if any(key in config for key in ("client_secret", "cookie_secret", "email_domains", "allowed_groups", "allowed_roles")):
        raise SystemExit(f"{path.name}: forbidden inline secret or broad allowlist")
PY

workdir=$(mktemp -d)
cleanup() {
    rm -rf "$workdir"
}
trap cleanup EXIT

fixture="$workdir/xform"
mkdir -m 0755 "$fixture"
python3 - "$fixture" <<'PY'
from pathlib import Path
import os
import sys

root = Path(sys.argv[1])
# The cookie secret is deliberately raw 32-byte data: cookie_secret_file has
# stricter requirements than the human-facing base64 generation shortcut.
(root / "cookie-secret").write_bytes(os.urandom(32))
(root / "zitadel-client-secret").write_text(os.urandom(32).hex(), encoding="ascii")
# A reserved address keeps this fixture deny-by-default for every real tenant.
(root / "authenticated-emails.txt").write_text("operator@example.invalid\n", encoding="ascii")
for path in root.iterdir():
    path.chmod(0o444)
PY

render_and_test() {
    mount=$1
    config="$fixture/$mount.cfg"
    XFORM_PUBLIC_ORIGIN=https://panel.example.invalid \
    ZITADEL_ISSUER_URL=https://issuer.example.invalid \
    ZITADEL_CLIENT_ID=ci-client-id \
        "$root/render-config.sh" "$mount" "$config"
    # The test fixture is mounted into the image's unprivileged runtime user.
    chmod 0444 "$config"

    printf 'checking oauth2-proxy %s configuration\n' "$mount"
    docker run --rm --network none --read-only \
        -v "$fixture:/etc/oauth2-proxy/xform:ro" \
        "$image" \
        --config="/etc/oauth2-proxy/xform/$mount.cfg" --config-test
}

render_and_test root
render_and_test subpath
printf 'oauth2-proxy configurations are valid\n'
