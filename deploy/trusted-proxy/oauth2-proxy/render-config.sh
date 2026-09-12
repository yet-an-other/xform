#!/bin/sh
# Render one secret-free oauth2-proxy config for xform.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

usage() {
    printf 'usage: %s root|subpath OUTPUT\n' "$0" >&2
    exit 2
}

[ "$#" -eq 2 ] || usage
mount=$1
output=$2
case "$mount" in
    root|subpath) template="$root/$mount.cfg.template" ;;
    *) usage ;;
esac

: "${XFORM_PUBLIC_ORIGIN:?set XFORM_PUBLIC_ORIGIN to the public HTTPS origin}"
: "${ZITADEL_ISSUER_URL:?set ZITADEL_ISSUER_URL to the ZITADEL issuer URL}"
: "${ZITADEL_CLIENT_ID:?set ZITADEL_CLIENT_ID to the ZITADEL client ID}"

python3 - "$template" "$output" <<'PY'
import os
import sys
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit


template_path, output_path = sys.argv[1:]


def required(name: str) -> str:
    value = os.environ.get(name, "")
    if not value or any(ord(char) < 0x20 or ord(char) == 0x7f for char in value):
        raise SystemExit(f"{name} must be set and contain no control characters")
    return value


def origin(name: str) -> str:
    value = required(name)
    parsed = urlsplit(value)
    try:
        has_host = parsed.hostname is not None
        parsed.port  # Force malformed ports to fail before writing TOML.
    except ValueError:
        has_host = False
    if (
        parsed.scheme != "https"
        or not parsed.netloc
        or not has_host
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in ("", "/")
    ):
        raise SystemExit(f"{name} must be an HTTPS origin without a path, query, or fragment")
    return urlunsplit(("https", parsed.netloc, "", "", ""))


def toml_content(value: str) -> str:
    # The accepted values are URLs/IDs, but escape defensively before placing
    # them inside the quoted TOML strings in the templates.
    return value.replace("\\", "\\\\").replace('"', '\\"')


public_origin = origin("XFORM_PUBLIC_ORIGIN")
issuer = origin("ZITADEL_ISSUER_URL")
client_id = required("ZITADEL_CLIENT_ID")
if '"' in client_id or "\\" in client_id or client_id.strip() != client_id:
    raise SystemExit("ZITADEL_CLIENT_ID contains unsupported TOML characters")

text = Path(template_path).read_text(encoding="utf-8")
suffix = "/oauth2/callback" if Path(template_path).name == "root.cfg.template" else "/xform/oauth2/callback"
markers = {
    "__XFORM_REDIRECT_URL__": public_origin + suffix,
    "__ZITADEL_ISSUER_URL__": issuer,
    "__ZITADEL_CLIENT_ID__": client_id,
}
for marker, value in markers.items():
    text = text.replace(marker, toml_content(value))

if any(marker in text for marker in markers):
    raise SystemExit("unexpanded oauth2-proxy template marker")

output = Path(output_path)
if output.exists() and output.is_dir():
    raise SystemExit("output path is a directory")
parent = output.parent
if not parent.is_dir():
    raise SystemExit("output parent directory does not exist")

# Replace atomically and never follow an output symlink. The caller can then
# chown the 0600 file to the oauth2-proxy service account.
tmp_fd = None
try:
    import tempfile

    tmp_fd, tmp_name = tempfile.mkstemp(prefix=f".{output.name}.", dir=parent)
    os.fchmod(tmp_fd, 0o600)
    with os.fdopen(tmp_fd, "w", encoding="utf-8") as stream:
        tmp_fd = None
        stream.write(text)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(tmp_name, output)
except BaseException:
    if tmp_fd is not None:
        os.close(tmp_fd)
    if "tmp_name" in locals():
        try:
            os.unlink(tmp_name)
        except FileNotFoundError:
            pass
    raise
PY

printf 'rendered %s oauth2-proxy config: %s\n' "$mount" "$output"
