#!/usr/bin/env bash
# xform-update — install the latest release of the panel on this host.
#
# Compares the local binary's SHA256 against the release's checksums (the
# release tag is never parsed), swaps the binary atomically, restarts the
# panel, and health-checks it. The previous binary is kept as xform.prev.
#
# Overrides: XFORM_INSTALL_PATH, XFORM_SERVICE, XFORM_HEALTH_URL.
set -euo pipefail

REPO="yet-an-other/xform"
INSTALL_PATH="${XFORM_INSTALL_PATH:-/usr/local/bin/xform}"
SERVICE="${XFORM_SERVICE:-xform.service}"
HEALTH_URL="${XFORM_HEALTH_URL:-http://127.0.0.1:9090/api/v1/server}"

exec 9>/var/lock/xform-update.lock
flock -n 9 || exit 0 # another run in progress — the next timer tick retries

case "$(uname -m)" in
x86_64)  asset="xform-linux-amd64" ;;
aarch64) asset="xform-linux-arm64" ;;
*) echo "xform-update: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

base="https://github.com/$REPO/releases/latest/download"
# Temp dir next to the binary: same filesystem, so the final mv is atomic.
tmp="$(mktemp -d "$(dirname "$INSTALL_PATH")/.xform-update.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
expected="$(awk -v f="$asset" '$2 == f {print $1}' "$tmp/checksums.txt")"
if [ -z "$expected" ]; then
  echo "xform-update: $asset not found in checksums.txt" >&2
  exit 1
fi

if [ -x "$INSTALL_PATH" ] && [ "$(sha256sum "$INSTALL_PATH" | awk '{print $1}')" = "$expected" ]; then
  exit 0 # already on the latest release
fi

curl -fsSL "$base/$asset" -o "$tmp/xform"
echo "$expected  $tmp/xform" | sha256sum -c - >/dev/null
chmod 0755 "$tmp/xform"

if [ -x "$INSTALL_PATH" ]; then
  cp -f "$INSTALL_PATH" "$INSTALL_PATH.prev"
fi
mv -f "$tmp/xform" "$INSTALL_PATH"
systemctl restart "$SERVICE"

for _ in $(seq 1 20); do
  if curl -fsS -o /dev/null "$HEALTH_URL"; then
    echo "xform-update: updated $INSTALL_PATH to $asset@${expected:0:12}; $SERVICE healthy"
    exit 0
  fi
  sleep 1
done

echo "xform-update: $SERVICE failed its health check after the update." >&2
echo "xform-update: roll back with: mv $INSTALL_PATH.prev $INSTALL_PATH && systemctl restart $SERVICE" >&2
exit 1
