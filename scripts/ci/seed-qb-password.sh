#!/usr/bin/env bash
# Seed the qBittorrent WebUI admin password into qBittorrent.conf.
#
# Why: ghcr.io/linuxserver/qbittorrent ignores WEBUI_PASSWORD on every tag
# (upstream PR linuxserver/docker-qbittorrent#437 is still open), so the dev
# fixture sets the password by writing the PBKDF2 hash qBittorrent itself
# expects directly into its config file.
#
# Usage:
#   BANSOKO_QB_PASSWORD=... scripts/ci/seed-qb-password.sh [path/to/qBittorrent.conf]
# Default target: ./qbittorrent/config/qBittorrent/qBittorrent.conf
#
# Run this while the qbittorrent container is STOPPED: qBittorrent rewrites
# its config from memory on shutdown, which would discard the seeded hash.
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"
conf="${1:-$root/qbittorrent/config/qBittorrent/qBittorrent.conf}"
password="${BANSOKO_QB_PASSWORD:-}"
if [[ -z "$password" ]]; then
  echo "BANSOKO_QB_PASSWORD must be set" >&2
  exit 2
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to derive the PBKDF2 hash" >&2
  exit 2
fi

mkdir -p "$(dirname "$conf")"
QB_SEED_PASSWORD="$password" QB_SEED_CONF="$conf" python3 - <<'PY'
import base64
import hashlib
import os
import secrets
import tempfile

password = os.environ["QB_SEED_PASSWORD"].encode()
conf = os.environ["QB_SEED_CONF"]

salt = secrets.token_bytes(16)
digest = hashlib.pbkdf2_hmac("sha512", password, salt, 100_000, dklen=64)
encoded = "@ByteArray({}:{})".format(
    base64.b64encode(salt).decode(), base64.b64encode(digest).decode()
)

lines = []
if os.path.exists(conf):
    with open(conf, "r", encoding="utf-8") as fh:
        lines = fh.read().splitlines()

wanted = {
    "WebUI\\Username": "admin",
    "WebUI\\Password_PBKDF2": encoded,
}
out, section, seen = [], None, set()


def emit_missing():
    for key, value in wanted.items():
        if key not in seen:
            out.append("{}={}".format(key, value))
            seen.add(key)


for line in lines:
    stripped = line.strip()
    if stripped.startswith("[") and stripped.endswith("]"):
        if section == "Preferences":
            emit_missing()
        section = stripped[1:-1]
        out.append(line)
        continue
    if section == "Preferences" and "=" in stripped:
        key = stripped.split("=", 1)[0].strip()
        if key in wanted:
            if key not in seen:
                out.append("{}={}".format(key, wanted[key]))
                seen.add(key)
            continue  # drop stale duplicate entries
    out.append(line)
if section == "Preferences":
    emit_missing()
if seen != set(wanted):
    if out and out[-1] != "":
        out.append("")
    out.append("[Preferences]")
    emit_missing()

fd, tmp = tempfile.mkstemp(dir=os.path.dirname(conf), prefix=".qBittorrent.conf.")
try:
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write("\n".join(out) + "\n")
    os.chmod(tmp, 0o600)
    os.replace(tmp, conf)
except BaseException:
    os.unlink(tmp)
    raise
PY

echo "seeded WebUI admin password into $conf"
echo "start the qbittorrent container now (seed only while it is stopped)"
