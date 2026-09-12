#!/bin/sh
# Run the nginx Trusted proxy gateway contract against local deterministic
# doubles. No live identity provider or xform process is needed.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
fake="$root/fake-forward-auth.py"
image=${NGINX_IMAGE:-nginx:1.29.1-alpine@sha256:42a516af16b852e33b7682d5ef8acbd5d13fe08fecadc7ed98605ba5e3b26ab8}
workdir=$(mktemp -d)
fake_pid=
nginx_pid=
nginx_container=

cleanup() {
    set +e
    if [ -n "$nginx_container" ]; then
        docker rm -f "$nginx_container" >/dev/null 2>&1
    fi
    if [ -n "$nginx_pid" ]; then
        kill "$nginx_pid" >/dev/null 2>&1
        wait "$nginx_pid" >/dev/null 2>&1
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

# Docker's nginx worker must be able to traverse the bind-mounted fixture and
# socket directories. The fake socket itself is deliberately more permissive;
# production xform sockets remain 0660 in a protected group-owned directory.
chmod 0755 "$workdir"
static_root="$workdir/dist"
mkdir -p "$static_root/assets"
printf '%s\n' '<!doctype html><html><body>xform static fixture</body></html>' >"$static_root/index.html"
printf '%s\n' 'xform static asset' >"$static_root/assets/app.js"
chmod 0755 "$static_root" "$static_root/assets"
chmod 0644 "$static_root/index.html" "$static_root/assets/app.js"

random_port() {
    python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

auth_port=$(random_port)
socket_dir="$workdir/run/xform"
socket_path="$socket_dir/xform.sock"
mkdir -p "$socket_dir"
chmod 0755 "$workdir/run" "$socket_dir"

python3 "$fake" --auth-port "$auth_port" --socket "$socket_path" >"$workdir/fake.log" 2>&1 &
fake_pid=$!
attempt=0
while [ "$attempt" -lt 100 ]; do
    if [ -S "$socket_path" ] && curl -sS --connect-timeout 1 --max-time 1 -o /dev/null "http://127.0.0.1:$auth_port/oauth2/auth" 2>/dev/null; then
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
[ -S "$socket_path" ] || { printf 'fake Panel socket did not appear\n' >&2; exit 1; }

render_config() {
    template=$1
    output=$2
    listen_port=$3
    secret=$4
    python3 - "$template" "$output" "$listen_port" "$auth_port" "$secret" <<'PY'
from pathlib import Path
import sys

template, output, listen_port, auth_port, secret = sys.argv[1:]
text = Path(template).read_text()
replacements = {
    "${NGINX_USER}": "nginx",
    "${NGINX_LISTEN}": f"127.0.0.1:{listen_port}",
    "${OAUTH2_PROXY_UPSTREAM}": f"127.0.0.1:{auth_port}",
    "${XFORM_SOCKET}": "/run/xform/xform.sock",
    "${XFORM_TRUSTED_PROXY_SECRET}": secret,
}
for marker, value in replacements.items():
    text = text.replace(marker, value)
if "${" in text:
    raise SystemExit("unexpanded nginx template marker")
Path(output).write_text(text)
PY
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
    [ -f "$template" ] || { printf 'missing nginx template: %s\n' "$template" >&2; exit 1; }

    listen_port=$(random_port)
    while [ "$auth_port" = "$listen_port" ]; do
        listen_port=$(random_port)
    done
    secret=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
    nginx_config="$workdir/$case_name.conf"
    render_config "$template" "$nginx_config" "$listen_port" "$secret"

    nginx_container="xform-nginx-smoke-$$-$case_name"
    docker run --rm --network host \
        -v "$nginx_config:/etc/nginx/nginx.conf:ro" \
        -v "$socket_dir:/run/xform:ro" \
        -v "$static_root:/var/www/xform/dist:ro" \
        "$image" nginx -t -c /etc/nginx/nginx.conf >/dev/null

    docker run --rm --name "$nginx_container" --network host \
        -v "$nginx_config:/etc/nginx/nginx.conf:ro" \
        -v "$socket_dir:/run/xform:ro" \
        -v "$static_root:/var/www/xform/dist:ro" \
        "$image" nginx -g 'daemon off;' >"$workdir/$case_name.nginx.log" 2>&1 &
    nginx_pid=$!

    base="http://127.0.0.1:$listen_port"
    if [ "$shape" = "root" ]; then
        public_prefix=
        public_mount=/
        auth_start="$base/oauth2/start?rd=%2F"
    else
        public_prefix=/xform
        public_mount=/xform/
        auth_start="$base/xform/oauth2/start?rd=%2Fxform%2F"
    fi

    attempt=0
    while [ "$attempt" -lt 100 ]; do
        if curl -sS --connect-timeout 1 --max-time 1 -o /dev/null "$auth_start" 2>/dev/null; then
            break
        fi
        kill -0 "$nginx_pid" 2>/dev/null || {
            printf '%s nginx exited\n' "$case_name" >&2
            cat "$workdir/$case_name.nginx.log" >&2
            exit 1
        }
        attempt=$((attempt + 1))
        sleep 0.05
    done

    document_target="$public_prefix/?return=%25%2F&empty="
    if [ "$shape" = "root" ]; then
        expected_document_rd='%2F%3Freturn%3D%2525%252F%26empty%3D'
    else
        expected_document_rd='%2Fxform%2F%3Freturn%3D%2525%252F%26empty%3D'
    fi
    status=$(request document "$base$document_target")
    [ "$status" = 302 ] || { printf '%s unauthenticated document status=%s, want 302\n' "$case_name" "$status" >&2; exit 1; }
    grep -Fq "Location: $public_prefix/fake-idp?rd=$expected_document_rd" "$workdir/$case_name.document.headers" || {
        printf '%s document did not preserve its encoded query through sign-in\n' "$case_name" >&2
        cat "$workdir/$case_name.document.headers" >&2
        exit 1
    }
    assert_no_store "$workdir/$case_name.document.headers"

    api_target="$public_prefix/api/v1/users/alice%2FJos%C3%A9?query=%25%2F&empty="
    status=$(request api "$base$api_target")
    [ "$status" = 401 ] || { printf '%s unauthenticated API status=%s, want 401\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_location "$workdir/$case_name.api.headers"
    assert_no_store "$workdir/$case_name.api.headers"
    grep -Fq '{"error":"unauthenticated"}' "$workdir/$case_name.api.body" || {
        printf '%s unauthenticated API did not return its JSON error\n' "$case_name" >&2
        exit 1
    }
    ! grep -Fqi 'fake sign-in' "$workdir/$case_name.api.body" || {
        printf '%s unauthenticated API leaked the sign-in response\n' "$case_name" >&2
        exit 1
    }

    status=$(request health "$base$public_prefix/api/v1/healthz")
    [ "$status" = 401 ] || { printf '%s unauthenticated health status=%s, want 401\n' "$case_name" "$status" >&2; exit 1; }
    assert_no_store "$workdir/$case_name.health.headers"

    status=$(request public_auth "$auth_start")
    [ "$status" = 302 ] || { printf '%s public auth endpoint status=%s, want 302\n' "$case_name" "$status" >&2; exit 1; }
    grep -Fq "Location: $public_prefix/fake-idp?rd=" "$workdir/$case_name.public_auth.headers" || {
        printf '%s public auth endpoint was not reachable at its mount\n' "$case_name" >&2
        cat "$workdir/$case_name.public_auth.headers" >&2
        exit 1
    }
    assert_no_store "$workdir/$case_name.public_auth.headers"

    status=$(request oauth2_bare "$base$public_prefix/oauth2?return=%2F")
    [ "$status" = 308 ] || { printf '%s bare oauth2 status=%s, want 308\n' "$case_name" "$status" >&2; exit 1; }
    grep -Eiq "^Location: (https?://[^/]+)?$public_prefix/oauth2/\?return=%2F" "$workdir/$case_name.oauth2_bare.headers" || {
        printf '%s bare oauth2 redirect lost its query\n' "$case_name" >&2
        cat "$workdir/$case_name.oauth2_bare.headers" >&2
        exit 1
    }
    assert_no_store "$workdir/$case_name.oauth2_bare.headers"

    status=$(request callback "$base$public_prefix/oauth2/callback")
    [ "$status" = 200 ] || { printf '%s callback status=%s, want 200\n' "$case_name" "$status" >&2; exit 1; }
    cookie_path=$public_mount
    grep -Fq "Set-Cookie: xform_test_auth=admit; Path=$cookie_path;" "$workdir/$case_name.callback.headers" || {
        printf '%s oauth2 cookie was not scoped to %s\n' "$case_name" "$cookie_path" >&2
        exit 1
    }
    assert_no_store "$workdir/$case_name.callback.headers"

    status=$(request denied_document -H 'Cookie: xform_test_auth=deny' "$base$public_prefix/")
    [ "$status" = 302 ] || { printf '%s denied document status=%s, want 302\n' "$case_name" "$status" >&2; exit 1; }
    status=$(request document_error -H 'Cookie: xform_test_auth=error' "$base$public_prefix/")
    [ "$status" = 500 ] || { printf '%s document auth error status=%s, want fail-closed 500\n' "$case_name" "$status" >&2; exit 1; }

    status=$(request auth_error -H 'Cookie: xform_test_auth=error' "$base$api_target")
    [ "$status" = 500 ] || { printf '%s forward-auth error status=%s, want fail-closed 500\n' "$case_name" "$status" >&2; exit 1; }

    expected_target="/api/v1/users/alice%2FJos%C3%A9?query=%25%2F&empty="
    expected_host="panel.example.test:$listen_port"
    status=$(request admitted \
        -H 'Cookie: xform_test_auth=admit' \
        -H 'X-Xform-Authenticated: spoofed-admission' \
        -H 'Authorization: Bearer spoofed-access-token' \
        -H 'Proxy-Authorization: Basic spoofed' \
        -H 'Forwarded: for=198.51.100.42' \
        -H 'X-Forwarded-User: spoofed@example.test' \
        -H 'X-Forwarded-Email: spoofed@example.test' \
        -H 'X-Forwarded-Groups: spoofed-group' \
        -H 'X-Forwarded-Preferred-Username: spoofed@example.test' \
        -H 'X-Forwarded-Access-Token: spoofed-access-token' \
        -H 'X-Forwarded-Authorization: spoofed-authorization' \
        -H 'X-Forwarded-Id-Token: spoofed-id-token' \
        -H 'X-Forwarded-IdToken: spoofed-id-token' \
        -H 'X-Forwarded-Method: spoofed-method' \
        -H 'X-Forwarded-Query: spoofed-query' \
        -H 'X-Forwarded-Prefix: spoofed-prefix' \
        -H 'X-Forwarded-Protocol: spoofed-protocol' \
        -H 'X-Forwarded-Scheme: spoofed-scheme' \
        -H 'X-Forwarded-Client-Cert: spoofed-certificate' \
        -H 'X-Forwarded-Port: 443' \
        -H 'X-Authenticated-Groups: spoofed-group' \
        -H 'X-Authenticated-Preferred-Username: spoofed@example.test' \
        -H 'X-Authenticated-Token: spoofed-token' \
        -H 'X-Authenticated-Access-Token: spoofed-access-token' \
        -H 'X-Authenticated-Authorization: spoofed-authorization' \
        -H 'X-Authenticated-Id-Token: spoofed-id-token' \
        -H 'X-Authenticated-IdToken: spoofed-id-token' \
        -H 'X-Authenticated-Client-Cert: spoofed-certificate' \
        -H 'X-Auth-Request-User: spoofed@example.test' \
        -H 'X-Auth-Request-Email: spoofed@example.test' \
        -H 'X-Auth-Request-Groups: spoofed-group' \
        -H 'X-Auth-Request-Preferred-Username: spoofed@example.test' \
        -H 'X-Auth-Request-Token: spoofed-token' \
        -H 'X-Auth-Request-Access-Token: spoofed-access-token' \
        -H 'X-Auth-Request-Id-Token: spoofed-id-token' \
        -H 'X-Auth-Request-IdToken: spoofed-id-token' \
        -H 'X-Auth-Request-Client-Cert: spoofed-certificate' \
        -H 'X-Access-Token: spoofed-access-token' \
        -H 'X-ID-Token: spoofed-id-token' \
        -H 'X-Original-Method: spoofed-method' \
        -H 'X-Forwarded-Server: spoofed-server' \
        -H 'X-Forwarded-Uri: spoofed-uri' \
        -H 'X-Auth-Request-Redirect: spoofed-redirect' \
        -H 'X-Id-Token: spoofed-id-token' \
        -H 'Remote-User: spoofed@example.test' \
        -H 'Remote-Email: spoofed@example.test' \
        -H 'Remote-Groups: spoofed-group' \
        -H 'X-Remote-User: spoofed@example.test' \
        -H 'X-Remote-Email: spoofed@example.test' \
        -H 'X-Remote-Groups: spoofed-group' \
        -H 'X-Authenticated-User: spoofed@example.test' \
        -H 'X-Authenticated-Email: spoofed@example.test' \
        -H 'X-User: spoofed@example.test' \
        -H 'X-Email: spoofed@example.test' \
        -H 'X-Groups: spoofed-group' \
        -H 'X-Group: spoofed-group' \
        -H 'X-Original-URL: spoofed-url' \
        -H 'X-Original-URI: spoofed-uri' \
        -H 'X-SSL-Client-Cert: spoofed-certificate' \
        -H 'X-Client-Cert: spoofed-certificate' \
        -H 'X-Forwarded-For: 198.51.100.42' \
        -H 'X-Real-IP: 198.51.100.42' \
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
    raise SystemExit("Panel did not receive the prefix-stripped request target")
if headers["Host"] != os.environ["EXPECTED_HOST"]:
    raise SystemExit("Panel did not receive the original Host")
if headers["X-Xform-Authenticated"] != os.environ["EXPECTED_ASSERTION"]:
    raise SystemExit("Panel did not receive the configured Admission assertion")
for name in (
    "Cookie",
    "Authorization",
    "Proxy-Authorization",
    "Forwarded",
    "X-Forwarded-For",
    "X-Forwarded-Host",
    "X-Forwarded-Port",
    "X-Forwarded-Proto",
    "X-Forwarded-Server",
    "X-Forwarded-Uri",
    "X-Forwarded-User",
    "X-Forwarded-Email",
    "X-Forwarded-Groups",
    "X-Forwarded-Preferred-Username",
    "X-Forwarded-Access-Token",
    "X-Forwarded-Authorization",
    "X-Forwarded-Id-Token",
    "X-Forwarded-IdToken",
    "X-Forwarded-Method",
    "X-Forwarded-Query",
    "X-Forwarded-Prefix",
    "X-Forwarded-Protocol",
    "X-Forwarded-Scheme",
    "X-Forwarded-Client-Cert",
    "X-Auth-Request-User",
    "X-Auth-Request-Email",
    "X-Auth-Request-Groups",
    "X-Auth-Request-Preferred-Username",
    "X-Auth-Request-Token",
    "X-Auth-Request-Access-Token",
    "X-Auth-Request-Redirect",
    "X-Auth-Request-Id-Token",
    "X-Auth-Request-IdToken",
    "X-Auth-Request-Client-Cert",
    "X-Access-Token",
    "X-ID-Token",
    "X-Id-Token",
    "Remote-User",
    "Remote-Email",
    "Remote-Groups",
    "X-Remote-User",
    "X-Remote-Email",
    "X-Remote-Groups",
    "X-Authenticated-User",
    "X-Authenticated-Email",
    "X-Authenticated-Groups",
    "X-Authenticated-Preferred-Username",
    "X-Authenticated-Token",
    "X-Authenticated-Access-Token",
    "X-Authenticated-Authorization",
    "X-Authenticated-Id-Token",
    "X-Authenticated-IdToken",
    "X-Authenticated-Client-Cert",
    "X-User",
    "X-Email",
    "X-Groups",
    "X-Group",
    "X-Original-URL",
    "X-Original-URI",
    "X-Original-Method",
    "X-SSL-Client-Cert",
    "X-Client-Cert",
    "X-Real-IP",
):
    if headers[name] is not None:
        raise SystemExit(f"Panel received client or gateway credential header {name}")
PY

    if [ "$shape" = "subpath" ]; then
        status=$(request bare_mount "$base/xform?return=%2F")
        [ "$status" = 308 ] || { printf '%s bare mount status=%s, want 308\n' "$case_name" "$status" >&2; exit 1; }
        grep -Eiq '^Location: (https?://[^/]+)?/xform/\?return=%2F' "$workdir/$case_name.bare_mount.headers" || {
            printf '%s bare mount redirect lost its query\n' "$case_name" >&2
            cat "$workdir/$case_name.bare_mount.headers" >&2
            exit 1
        }
        status=$(request unscoped_auth "$base/oauth2/start?rd=%2F")
        [ "$status" = 404 ] || { printf '%s unscoped oauth2 endpoint status=%s, want 404\n' "$case_name" "$status" >&2; exit 1; }
    fi

    if [ "$template_name" = "root-embedded.conf.template" ] || [ "$template_name" = "subpath-embedded.conf.template" ]; then
        status=$(request embedded_document -H 'Cookie: xform_test_auth=admit' "$base$public_prefix/users/alice%2Fbob?query=%25%2F")
        [ "$status" = 200 ] || { printf '%s embedded document status=%s, want 200\n' "$case_name" "$status" >&2; exit 1; }
        grep -Fq '"request_target":"/users/alice%2Fbob?query=%25%2F"' "$workdir/$case_name.embedded_document.body" || {
            printf '%s embedded document did not reach the Panel with its encoded target\n' "$case_name" >&2
            exit 1
        }
    fi

    if [ "$template_name" = "root-static.conf.template" ] || [ "$template_name" = "subpath-static.conf.template" ]; then
        status=$(request denied_asset -H 'Cookie: xform_test_auth=deny' "$base$public_prefix/assets/app.js")
        [ "$status" = 302 ] || { printf '%s denied existing asset status=%s, want 302\n' "$case_name" "$status" >&2; exit 1; }
        status=$(request denied_missing_asset -H 'Cookie: xform_test_auth=deny' "$base$public_prefix/assets/missing.js")
        [ "$status" = 302 ] || { printf '%s denied missing asset status=%s, want 302\n' "$case_name" "$status" >&2; exit 1; }
        status=$(request asset_error -H 'Cookie: xform_test_auth=error' "$base$public_prefix/assets/app.js")
        [ "$status" = 500 ] || { printf '%s asset auth error status=%s, want fail-closed 500\n' "$case_name" "$status" >&2; exit 1; }

        status=$(request static_document -H 'Cookie: xform_test_auth=admit' "$base$public_prefix/")
        [ "$status" = 200 ] || { printf '%s static document status=%s, want 200\n' "$case_name" "$status" >&2; exit 1; }
        grep -Fq 'xform static fixture' "$workdir/$case_name.static_document.body" || {
            printf '%s static document did not come from the build root\n' "$case_name" >&2
            exit 1
        }

        status=$(request deep_document -H 'Cookie: xform_test_auth=admit' "$base$public_prefix/users/alice%2Fbob")
        [ "$status" = 200 ] || { printf '%s deep document status=%s, want 200\n' "$case_name" "$status" >&2; exit 1; }
        grep -Fq 'xform static fixture' "$workdir/$case_name.deep_document.body" || {
            printf '%s deep document did not use the SPA fallback\n' "$case_name" >&2
            exit 1
        }

        status=$(request asset -H 'Cookie: xform_test_auth=admit' "$base$public_prefix/assets/app.js")
        [ "$status" = 200 ] || { printf '%s asset status=%s, want 200\n' "$case_name" "$status" >&2; exit 1; }
        grep -Fq 'xform static asset' "$workdir/$case_name.asset.body" || {
            printf '%s asset did not come from the build root\n' "$case_name" >&2
            exit 1
        }

        status=$(request missing_asset -H 'Cookie: xform_test_auth=admit' "$base$public_prefix/assets/missing.js")
        [ "$status" = 404 ] || { printf '%s missing asset status=%s, want 404\n' "$case_name" "$status" >&2; exit 1; }
    fi

    docker rm -f "$nginx_container" >/dev/null
    wait "$nginx_pid" >/dev/null 2>&1 || true
    nginx_container=
    nginx_pid=
    printf '%s: PASS\n' "$case_name"
}

run_case root-embedded root-embedded.conf.template root
run_case subpath-embedded subpath-embedded.conf.template subpath
run_case root-static root-static.conf.template root
run_case subpath-static subpath-static.conf.template subpath

printf '%s\n' 'nginx Trusted proxy smoke matrix: PASS'
