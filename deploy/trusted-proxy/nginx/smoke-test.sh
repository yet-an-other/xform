#!/bin/sh
# Run the nginx Trusted proxy gateway contract against local deterministic
# doubles. No live identity provider or xform process is needed.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
template="$root/root-embedded.conf.template"
fake="$root/fake-forward-auth.py"
image=${NGINX_IMAGE:-nginx:1.29.1-alpine@sha256:42a516af16b852e33b7682d5ef8acbd5d13fe08fecadc7ed98605ba5e3b26ab8}
workdir=$(mktemp -d)
fake_pid=
nginx_container="xform-nginx-smoke-$$"

cleanup() {
    set +e
    if [ -n "$nginx_container" ]; then
        docker rm -f "$nginx_container" >/dev/null 2>&1
    fi
    if [ -n "$fake_pid" ]; then
        kill "$fake_pid" >/dev/null 2>&1
        wait "$fake_pid" >/dev/null 2>&1
    fi
    rm -rf "$workdir"
}
trap cleanup EXIT INT TERM

for command in curl docker python3; do
    command -v "$command" >/dev/null 2>&1 || {
        printf 'missing required command: %s\n' "$command" >&2
        exit 1
    }
done
[ -f "$template" ] || { printf 'missing nginx template: %s\n' "$template" >&2; exit 1; }
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
listen_port=$(random_port)
while [ "$auth_port" = "$listen_port" ]; do
    listen_port=$(random_port)
done
secret=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
socket_dir="$workdir/run/xform"
socket_path="$socket_dir/xform.sock"
mkdir -p "$socket_dir"

python3 "$fake" --auth-port "$auth_port" --socket "$socket_path" >"$workdir/fake.log" 2>&1 &
fake_pid=$!
for _ in $(seq 1 100); do
    if [ -S "$socket_path" ] && curl -sS --connect-timeout 1 -o /dev/null "http://127.0.0.1:$auth_port/oauth2/auth" 2>/dev/null; then
        break
    fi
    kill -0 "$fake_pid" 2>/dev/null || {
        printf 'fake gateway exited\n' >&2
        exit 1
    }
    sleep 0.05
done
[ -S "$socket_path" ] || { printf 'fake Panel socket did not appear\n' >&2; exit 1; }

nginx_config="$workdir/nginx.conf"
python3 - "$template" "$nginx_config" "$listen_port" "$auth_port" "$secret" <<'PY'
from pathlib import Path
import sys

template, output, listen_port, auth_port, secret = sys.argv[1:]
text = Path(template).read_text()
replacements = {
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

# Validate the exact generated configuration before starting the gateway.
docker run --rm --network host \
    -v "$nginx_config:/etc/nginx/nginx.conf:ro" \
    -v "$socket_dir:/run/xform:ro" \
    "$image" nginx -t -c /etc/nginx/nginx.conf >/dev/null

docker run --rm --name "$nginx_container" --network host \
    -v "$nginx_config:/etc/nginx/nginx.conf:ro" \
    -v "$socket_dir:/run/xform:ro" \
    "$image" nginx -g 'daemon off;' >"$workdir/nginx.log" 2>&1 &
nginx_pid=$!
base="http://127.0.0.1:$listen_port"
for _ in $(seq 1 100); do
    if curl -sS --connect-timeout 1 -o /dev/null "$base/oauth2/start?rd=%2F" 2>/dev/null; then
        break
    fi
    kill -0 "$nginx_pid" 2>/dev/null || {
        printf 'nginx exited\n' >&2
        cat "$workdir/nginx.log" >&2
        exit 1
    }
    sleep 0.05
done

request() {
    name=$1
    shift
    headers="$workdir/$name.headers"
    body="$workdir/$name.body"
    status=$(curl -sS --path-as-is -D "$headers" -o "$body" -w '%{http_code}' "$@")
    printf '%s' "$status"
}

status=$(request document "$base/")
[ "$status" = 302 ] || { printf 'unauthenticated document status=%s, want 302\n' "$status" >&2; exit 1; }
grep -Eiq '^Location: (https?://[^/]+)?/oauth2/start\?rd=/' "$workdir/document.headers" || {
    printf 'document did not redirect to the public sign-in endpoint\n' >&2
    exit 1
}

status=$(request api "$base/api/v1/server")
[ "$status" = 401 ] || { printf 'unauthenticated API status=%s, want 401\n' "$status" >&2; exit 1; }
! grep -Eiq '^Location:' "$workdir/api.headers" || {
    printf 'unauthenticated API unexpectedly redirected\n' >&2
    exit 1
}
grep -Fq '{"error":"unauthenticated"}' "$workdir/api.body" || {
    printf 'unauthenticated API did not return its JSON error\n' >&2
    exit 1
}
! grep -Fqi 'fake sign-in' "$workdir/api.body" || {
    printf 'unauthenticated API leaked the sign-in response\n' >&2
    exit 1
}

status=$(request health "$base/api/v1/healthz")
[ "$status" = 401 ] || { printf 'unauthenticated health status=%s, want 401\n' "$status" >&2; exit 1; }

status=$(request public_auth -H 'Cookie: xform_test_auth=deny' "$base/oauth2/start?rd=%2F")
[ "$status" = 302 ] || { printf 'public auth endpoint status=%s, want 302\n' "$status" >&2; exit 1; }
grep -Eiq '^Location: /fake-idp\?rd=' "$workdir/public_auth.headers" || {
    printf 'public auth endpoint was not reachable\n' >&2
    exit 1
}

status=$(request denied_document -H 'Cookie: xform_test_auth=deny' "$base/")
[ "$status" = 302 ] || { printf 'denied document status=%s, want 302\n' "$status" >&2; exit 1; }

status=$(request auth_error -H 'Cookie: xform_test_auth=error' "$base/api/v1/server")
[ "$status" = 500 ] || { printf 'forward-auth error status=%s, want fail-closed 500\n' "$status" >&2; exit 1; }

request_target='/api/v1/users/alice%2Fbob?query=%25%2F&empty='
expected_host="panel.example.test:$listen_port"
status=$(request admitted \
    -H 'Cookie: xform_test_auth=admit' \
    -H 'X-Xform-Authenticated: spoofed-admission' \
    -H 'Authorization: Bearer spoofed-access-token' \
    -H 'Proxy-Authorization: Basic spoofed' \
    -H 'X-Forwarded-User: spoofed@example.test' \
    -H 'X-Forwarded-Email: spoofed@example.test' \
    -H 'X-Forwarded-Access-Token: spoofed-access-token' \
    -H 'X-Auth-Request-Email: spoofed@example.test' \
    -H 'X-Auth-Request-Access-Token: spoofed-access-token' \
    -H 'X-Access-Token: spoofed-access-token' \
    -H 'X-ID-Token: spoofed-id-token' \
    -H 'X-Forwarded-For: 198.51.100.42' \
    -H 'X-Real-IP: 198.51.100.42' \
    -H "Host: $expected_host" \
    "$base$request_target")
[ "$status" = 200 ] || { printf 'admitted request status=%s, want 200\n' "$status" >&2; exit 1; }
EXPECTED_ASSERTION="$secret" EXPECTED_TARGET="$request_target" EXPECTED_HOST="$expected_host" \
    python3 - "$workdir/admitted.body" <<'PY'
import json
import os
import sys

payload = json.loads(open(sys.argv[1], encoding="utf-8").read())
headers = payload["headers"]
if payload["method"] != "GET" or payload["request_target"] != os.environ["EXPECTED_TARGET"]:
    raise SystemExit("Panel did not receive the exact method and request target")
if headers["Host"] != os.environ["EXPECTED_HOST"]:
    raise SystemExit("Panel did not receive the original Host")
if headers["X-Xform-Authenticated"] != os.environ["EXPECTED_ASSERTION"]:
    raise SystemExit("Panel did not receive the configured Admission assertion")
for name in (
    "Cookie",
    "Authorization",
    "Proxy-Authorization",
    "X-Forwarded-User",
    "X-Forwarded-Email",
    "X-Forwarded-Access-Token",
    "X-Auth-Request-Email",
    "X-Auth-Request-Access-Token",
    "X-Access-Token",
    "X-ID-Token",
    "X-Forwarded-For",
    "X-Real-IP",
):
    if headers[name] is not None:
        raise SystemExit("Panel received a client or gateway credential header")
PY

printf '%s\n' "nginx Trusted proxy smoke test: PASS"
