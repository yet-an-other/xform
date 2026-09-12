#!/bin/sh
# Run the Traefik Trusted proxy gateway contract against deterministic doubles.
# Traefik has no validate-only command, so each rendered configuration is
# started ephemerally and exercised over a host-networked loopback.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
fake="$root/../nginx/fake-forward-auth.py"
image=${TRAEFIK_IMAGE:-traefik:v3.5.3@sha256:d6be8725d21b45bdd84b93ea01438256e0e3c94aa8fa51834fe87f37cd5d4af8}
workdir=$(mktemp -d)
fake_pid=
traefik_pid=
traefik_container=

cleanup() {
    set +e
    if [ -n "$traefik_container" ]; then
        docker rm -f "$traefik_container" >/dev/null 2>&1
    fi
    if [ -n "$traefik_pid" ]; then
        kill "$traefik_pid" >/dev/null 2>&1
        wait "$traefik_pid" >/dev/null 2>&1
    fi
    if [ -n "$fake_pid" ]; then
        kill "$fake_pid" >/dev/null 2>&1
        wait "$fake_pid" >/dev/null 2>&1
    fi
    rm -rf "$workdir"
}
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' TERM

for command in curl docker python3; do
    command -v "$command" >/dev/null 2>&1 || {
        printf 'missing required command: %s\n' "$command" >&2
        exit 1
    }
done
[ -f "$fake" ] || { printf 'missing fake service: %s\n' "$fake" >&2; exit 1; }

random_port() {
    python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

auth_port=$(random_port)
panel_port=$(random_port)
while [ "$panel_port" = "$auth_port" ]; do
    panel_port=$(random_port)
done
python3 "$fake" --auth-port "$auth_port" --panel-port "$panel_port" >"$workdir/fake.log" 2>&1 &
fake_pid=$!
attempt=0
while [ "$attempt" -lt 100 ]; do
    if curl -sS --connect-timeout 1 --max-time 1 -o /dev/null "http://127.0.0.1:$auth_port/oauth2/auth" 2>/dev/null && \
       curl -sS --connect-timeout 1 --max-time 1 -o /dev/null "http://127.0.0.1:$panel_port/" 2>/dev/null; then
        break
    fi
    kill -0 "$fake_pid" 2>/dev/null || {
        printf 'fake gateway exited\n' >&2
        cat "$workdir/fake.log" >&2
        exit 1
    }
    attempt=$((attempt + 1))
    sleep 0.05
done
kill -0 "$fake_pid" 2>/dev/null || { printf 'fake gateway did not become ready\n' >&2; exit 1; }

render_config() {
    template=$1
    output=$2
    secret=$3
    python3 - "$template" "$output" "$secret" "$auth_port" "$panel_port" <<'PY'
from pathlib import Path
import sys

template, output, secret, auth_port, panel_port = sys.argv[1:]
text = Path(template).read_text()
replacements = {
    "__OAUTH2_PROXY_PORT__": auth_port,
    "__XFORM_PORT__": panel_port,
    "__XFORM_TRUSTED_PROXY_SECRET__": secret,
}
for marker, value in replacements.items():
    text = text.replace(marker, value)
if any(marker in text for marker in replacements):
    raise SystemExit("unexpanded Traefik template marker")
Path(output).write_text(text)
PY
    chmod 0600 "$output"
}

request() {
    request_name=$1
    shift
    headers="$workdir/$case_name.$request_name.headers"
    body="$workdir/$case_name.$request_name.body"
    curl -sS --path-as-is --max-time 5 -D "$headers" -o "$body" -w '%{http_code}' "$@"
}

assert_no_location() {
    ! grep -Eiq '^Location:' "$1" || {
        printf '%s unexpectedly contained a Location header\n' "$1" >&2
        exit 1
    }
}

assert_no_store() {
    grep -Eiq '^Cache-Control: no-store' "$1" || {
        printf '%s did not contain Cache-Control: no-store\n' "$1" >&2
        exit 1
    }
}

run_case() {
    case_name=$1
    template_name=$2
    shape=$3
    template="$root/$template_name"
    [ -f "$template" ] || { printf 'missing Traefik template: %s\n' "$template" >&2; exit 1; }

    if [ "$shape" = root ]; then
        public_prefix=
        auth_prefix=/oauth2
    else
        public_prefix=/xform
        auth_prefix=/xform/oauth2
    fi

    secret=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
    dynamic_config="$workdir/$case_name.dynamic.yml"
    static_config="$workdir/$case_name.static.yml"
    render_config "$template" "$dynamic_config" "$secret"
    cat >"$static_config" <<EOF
entryPoints:
  web:
    address: ":$listen_port"
providers:
  file:
    filename: /etc/traefik/dynamic.yml
    watch: false
log:
  level: ERROR
accessLog: {}
EOF

    traefik_container="xform-traefik-smoke-$$-$case_name"
    docker run --rm --name "$traefik_container" --network host --user 0:0 \
        -v "$dynamic_config:/etc/traefik/dynamic.yml:ro" \
        -v "$static_config:/etc/traefik/traefik.yml:ro" \
        "$image" --configFile=/etc/traefik/traefik.yml \
        >"$workdir/$case_name.traefik.log" 2>&1 &
    traefik_pid=$!

    base="http://127.0.0.1:$listen_port"
    attempt=0
    while [ "$attempt" -lt 100 ]; do
        code=$(curl -sS --connect-timeout 1 --max-time 1 -o /dev/null -w '%{http_code}' \
            "$base$auth_prefix/start?rd=%2F" 2>/dev/null || true)
        if [ "$code" = 302 ]; then
            break
        fi
        if ! kill -0 "$traefik_pid" 2>/dev/null; then
            wait "$traefik_pid" >/dev/null 2>&1 || true
            printf '%s Traefik exited\n' "$case_name" >&2
            cat "$workdir/$case_name.traefik.log" >&2
            exit 1
        fi
        attempt=$((attempt + 1))
        sleep 0.05
    done
    if [ "$attempt" -eq 100 ]; then
        printf '%s Traefik did not become ready\n' "$case_name" >&2
        cat "$workdir/$case_name.traefik.log" >&2
        exit 1
    fi

    document_target="$public_prefix/?return=%25%2F&empty="
    status=$(request document "$base$document_target")
    [ "$status" = 302 ] || { printf '%s unauthenticated document status=%s, want 302\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_store "$workdir/$case_name.document.headers"
    EXPECTED_LOGIN_PATH="$public_prefix/fake-idp" EXPECTED_DOCUMENT_PATH="$public_prefix/" \
        python3 - "$workdir/$case_name.document.headers" <<'PY'
from pathlib import Path
import os
import sys
from urllib.parse import parse_qs, urlsplit

location = next(
    line.split(":", 1)[1].strip()
    for line in Path(sys.argv[1]).read_text().splitlines()
    if line.lower().startswith("location:")
)
parsed_location = urlsplit(location)
if parsed_location.path != os.environ["EXPECTED_LOGIN_PATH"]:
    raise SystemExit(f"document did not redirect through oauth2-proxy start: {location}")
redirect_targets = parse_qs(parsed_location.query, keep_blank_values=True).get("rd", [])
if len(redirect_targets) != 1:
    raise SystemExit(f"document redirect did not carry one rd value: {location}")
redirected = urlsplit(redirect_targets[0])
if redirected.path != os.environ["EXPECTED_DOCUMENT_PATH"]:
    raise SystemExit(f"document redirect lost its path: {redirect_targets[0]}")
if redirected.query != "return=%25%2F&empty=":
    raise SystemExit(f"document redirect lost its encoded query: {redirect_targets[0]}")
PY

    api_target="$public_prefix/api/v1/users/alice%2FJos%C3%A9?query=%25%2F&empty="
    status=$(request api "$base$api_target")
    [ "$status" = 401 ] || { printf '%s unauthenticated API status=%s, want 401\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_location "$workdir/$case_name.api.headers"
    assert_no_store "$workdir/$case_name.api.headers"
    ! grep -Eiq '<html|fake-idp' "$workdir/$case_name.api.body" || {
        printf '%s unauthenticated API leaked sign-in HTML/redirect\n' "$case_name" >&2
        exit 1
    }

    status=$(request health "$base$public_prefix/api/v1/healthz")
    [ "$status" = 401 ] || { printf '%s unauthenticated health status=%s, want 401\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_location "$workdir/$case_name.health.headers"
    assert_no_store "$workdir/$case_name.health.headers"

    status=$(request auth_error -H 'Cookie: xform_test_auth=error' "$base$api_target")
    [ "$status" = 503 ] || { printf '%s auth error status=%s, want fail-closed 503\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_location "$workdir/$case_name.auth_error.headers"
    assert_no_store "$workdir/$case_name.auth_error.headers"

    status=$(request public_auth "$base$auth_prefix/start?rd=%2F")
    [ "$status" = 302 ] || { printf '%s public auth endpoint status=%s, want 302\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_store "$workdir/$case_name.public_auth.headers"
    grep -Eiq "^Location: $public_prefix/fake-idp\?rd=" "$workdir/$case_name.public_auth.headers" || {
        printf '%s public auth endpoint was not reachable at its mount\n' "$case_name" >&2
        cat "$workdir/$case_name.public_auth.headers" >&2
        exit 1
    }

    status=$(request auth_check_public "$base$auth_prefix/auth")
    [ "$status" = 404 ] || { printf '%s public auth-check status=%s, want 404\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_store "$workdir/$case_name.auth_check_public.headers"

    status=$(request oauth2_bare "$base$auth_prefix?return=%2F")
    [ "$status" = 301 ] || { printf '%s bare oauth2 status=%s, want 301\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_store "$workdir/$case_name.oauth2_bare.headers"
    grep -Eiq "^Location: (https?://[^/]+)?$auth_prefix/\?return=%2F" "$workdir/$case_name.oauth2_bare.headers" || {
        printf '%s bare oauth2 redirect lost its query\n' "$case_name" >&2
        cat "$workdir/$case_name.oauth2_bare.headers" >&2
        exit 1
    }

    status=$(request callback "$base$auth_prefix/callback")
    [ "$status" = 200 ] || { printf '%s callback status=%s, want 200\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_store "$workdir/$case_name.callback.headers"
    cookie_path=$public_prefix/
    [ "$shape" = root ] && cookie_path=/
    grep -Fq "Set-Cookie: xform_test_auth=admit; Path=$cookie_path;" "$workdir/$case_name.callback.headers" || {
        printf '%s oauth2 cookie was not scoped to %s\n' "$case_name" "$cookie_path" >&2
        exit 1
    }

    status=$(request denied_document -H 'Cookie: xform_test_auth=deny' "$base$public_prefix/")
    [ "$status" = 302 ] || { printf '%s denied document status=%s, want 302\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_store "$workdir/$case_name.denied_document.headers"
    status=$(request document_error -H 'Cookie: xform_test_auth=error' "$base$public_prefix/")
    [ "$status" = 503 ] || { printf '%s document auth error status=%s, want fail-closed 503\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_store "$workdir/$case_name.document_error.headers"

    expected_target="/api/v1/users/alice%2FJos%C3%A9?query=%25%2F&empty="
    expected_host="panel.example.test:$listen_port"
    status=$(request admitted \
        -H 'Cookie: xform_test_auth=admit' \
        -H 'Forwarded: for=198.51.100.42' \
        -H 'X-Xform-Authenticated: spoofed-admission' \
        -H 'Authorization: Bearer spoofed-access-token' \
        -H 'Proxy-Authorization: Basic spoofed' \
        -H 'X-Forwarded-User: spoofed@example.test' \
        -H 'X-Forwarded-Email: spoofed@example.test' \
        -H 'X-Forwarded-Groups: spoofed-group' \
        -H 'X-Forwarded-Preferred-Username: spoofed@example.test' \
        -H 'X-Forwarded-Access-Token: spoofed-access-token' \
        -H 'X-Forwarded-Authorization: spoofed-authorization' \
        -H 'X-Forwarded-Id-Token: spoofed-id-token' \
        -H 'X-Forwarded-Client-Cert: spoofed-certificate' \
        -H 'X-Auth-Request-User: spoofed@example.test' \
        -H 'X-Auth-Request-Email: spoofed@example.test' \
        -H 'X-Auth-Request-Groups: spoofed-group' \
        -H 'X-Auth-Request-Preferred-Username: spoofed@example.test' \
        -H 'X-Auth-Request-Token: spoofed-token' \
        -H 'X-Auth-Request-Access-Token: spoofed-access-token' \
        -H 'X-Auth-Request-Id-Token: spoofed-id-token' \
        -H 'X-Auth-Request-IdToken: spoofed-id-token' \
        -H 'X-Auth-Request-Redirect: spoofed-redirect' \
        -H 'X-Access-Token: spoofed-access-token' \
        -H 'X-ID-Token: spoofed-id-token' \
        -H 'X-Id-Token: spoofed-id-token' \
        -H 'Remote-User: spoofed@example.test' \
        -H 'Remote-Email: spoofed@example.test' \
        -H 'Remote-Groups: spoofed-group' \
        -H 'X-Remote-User: spoofed@example.test' \
        -H 'X-Remote-Email: spoofed@example.test' \
        -H 'X-Remote-Groups: spoofed-group' \
        -H 'X-Authenticated-User: spoofed@example.test' \
        -H 'X-Authenticated-Email: spoofed@example.test' \
        -H 'X-Authenticated-Groups: spoofed-group' \
        -H 'X-User: spoofed@example.test' \
        -H 'X-Email: spoofed@example.test' \
        -H 'X-Groups: spoofed-group' \
        -H 'X-Group: spoofed-group' \
        -H 'X-Original-URL: spoofed-url' \
        -H 'X-Original-URI: spoofed-uri' \
        -H 'X-Original-Method: spoofed-method' \
        -H 'X-SSL-Client-Cert: spoofed-certificate' \
        -H 'X-Client-Cert: spoofed-certificate' \
        -H "Host: $expected_host" \
        "$base$api_target")
    [ "$status" = 200 ] || { printf '%s admitted request status=%s, want 200\n' "$case_name" "$status" >&2; exit 1; }
    EXPECTED_ASSERTION="$secret" EXPECTED_TARGET="$expected_target" EXPECTED_HOST="$expected_host" \
        python3 - "$workdir/$case_name.admitted.body" <<'PY'
import json
import os
import sys

payload = json.loads(open(sys.argv[1], encoding="utf-8").read())
headers = payload["headers"]
if payload["method"] != "GET" or payload["request_target"] != os.environ["EXPECTED_TARGET"]:
    raise SystemExit("Panel did not receive the expected request target")
if headers["Host"] != os.environ["EXPECTED_HOST"]:
    raise SystemExit("Panel did not receive the original Host")
if headers["X-Xform-Authenticated"] != os.environ["EXPECTED_ASSERTION"]:
    raise SystemExit("Panel did not receive the configured Admission assertion")
for name in (
    "Cookie", "Authorization", "Proxy-Authorization", "Forwarded",
    "X-Forwarded-User", "X-Forwarded-Email", "X-Forwarded-Groups",
    "X-Forwarded-Preferred-Username", "X-Forwarded-Access-Token",
    "X-Forwarded-Authorization", "X-Forwarded-Id-Token",
    "X-Forwarded-Client-Cert", "X-Auth-Request-User", "X-Auth-Request-Email",
    "X-Auth-Request-Groups", "X-Auth-Request-Preferred-Username",
    "X-Auth-Request-Token", "X-Auth-Request-Access-Token",
    "X-Auth-Request-Redirect", "X-Auth-Request-Id-Token",
    "X-Auth-Request-IdToken", "X-Access-Token", "X-ID-Token", "X-Id-Token",
    "Remote-User", "Remote-Email", "Remote-Groups", "X-Remote-User",
    "X-Remote-Email", "X-Remote-Groups", "X-Authenticated-User",
    "X-Authenticated-Email", "X-Authenticated-Groups", "X-User", "X-Email",
    "X-Groups", "X-Group", "X-Original-URL", "X-Original-URI",
    "X-Original-Method", "X-SSL-Client-Cert", "X-Client-Cert",
):
    if headers[name] is not None:
        raise SystemExit(f"Panel received forbidden header {name}")
PY

    if [ "$shape" = subpath ]; then
        status=$(request bare_mount "$base/xform?return=%2F")
        [ "$status" = 301 ] || { printf '%s bare mount status=%s, want 301\n' "$case_name" "$status" >&2; exit 1; }
        grep -Eiq '^Location: (https?://[^/]+)?/xform/\?return=%2F' "$workdir/$case_name.bare_mount.headers" || {
            printf '%s bare mount redirect lost its query\n' "$case_name" >&2
            cat "$workdir/$case_name.bare_mount.headers" >&2
            exit 1
        }
        status=$(request unscoped_auth "$base/oauth2/start?rd=%2F")
        [ "$status" = 404 ] || { printf '%s unscoped oauth2 endpoint status=%s, want 404\n' "$case_name" "$status" >&2; exit 1; }
    fi

    status=$(request embedded_document -H 'Cookie: xform_test_auth=admit' "$base$public_prefix/users/alice%2Fbob?query=%25%2F")
    [ "$status" = 200 ] || { printf '%s embedded document status=%s, want 200\n' "$case_name" "$status" >&2; exit 1; }
    embedded_target='/users/alice%2Fbob?query=%25%2F'
    grep -Fq "\"request_target\":\"$embedded_target\"" "$workdir/$case_name.embedded_document.body" || {
        printf '%s embedded document did not reach Panel with encoded target\n' "$case_name" >&2
        exit 1
    }

    docker rm -f "$traefik_container" >/dev/null
    wait "$traefik_pid" >/dev/null 2>&1 || true
    traefik_container=
    traefik_pid=
    printf '%s: PASS\n' "$case_name"
}

listen_port=$(random_port)
while [ "$listen_port" = "$auth_port" ] || [ "$listen_port" = "$panel_port" ]; do
    listen_port=$(random_port)
done
run_case root-embedded root-embedded.yml.template root
listen_port=$(random_port)
while [ "$listen_port" = "$auth_port" ] || [ "$listen_port" = "$panel_port" ]; do
    listen_port=$(random_port)
done
run_case subpath-embedded subpath-embedded.yml.template subpath

printf '%s\n' 'Traefik Trusted proxy smoke matrix: PASS'
