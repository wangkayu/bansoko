#!/usr/bin/env bash
# Bansoko — rotate BANSOKO_SECRET_ENCRYPTION_KEY (SEC-033).
#
# Every saved password, connection key, notification secret and API token raw
# value is encrypted in the database with BANSOKO_SECRET_ENCRYPTION_KEY. This
# re-encrypts all of them under a new key, in one transaction, with the app
# stopped:
#
#   openssl rand -base64 48 > new-key.txt          # or any >= 32 random characters
#   deployment/scripts/rotate-secret-key.sh [--stop-app] --new-key-file new-key.txt
#   # then start the app: docker compose ... up -d app
#
# What it does, in order (any failure stops with nothing changed):
#   1. refuses while the app runs, unless --stop-app;
#   2. starts postgres only and waits for it to be healthy;
#   3. in a one-off container of the app image (same env and /config), runs
#      `bansoko-server migrate rotate-secret-key` from the release the app runs
#      (current embedded-update overlay, else the image). That command refuses while any
#      other session is connected, checks that the current key opens every row
#      before writing anything, re-encrypts every row, reads each back with the
#      new key, and commits once; it then stores the new key in
#      /config/secrets.json (when the key came from there);
#   4. leaves the app stopped. If BANSOKO_SECRET_ENCRYPTION_KEY is set in .env
#      (an override), put the new key there before starting the app.
#
# The current key is BANSOKO_SECRET_ENCRYPTION_KEY when set, else the generated
# key in /config/secrets.json; the new key comes from --new-key-file or
# BANSOKO_NEW_SECRET_ENCRYPTION_KEY in this shell. Neither is put on argv or
# printed. Backups taken before the rotation still need the OLD key to restore.
#
# Compose selection: -f/--file and -p/--project, else COMPOSE_FILE /
# COMPOSE_PROJECT_NAME from the environment, else the production compose next
# to this script and project "bansoko". --env-file is passed through. When a
# compose file is given without a project, the project is left to compose,
# which derives it from that file's directory: naming "bansoko" there would
# re-encrypt and restart a different stack that happens to share the default.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/rotation-compose.sh
. "$script_dir/lib/rotation-compose.sh"

usage() {
  sed -n '2,33p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'USAGE'

Options:
  --new-key-file FILE  file holding the new key (else BANSOKO_NEW_SECRET_ENCRYPTION_KEY)
  --stop-app           stop the app first instead of refusing while it runs
  -f, --file FILE      compose file
  -p, --project NAME   compose project name
  --env-file FILE      compose env file
USAGE
}

compose_file=""
project="${COMPOSE_PROJECT_NAME:-}"
env_file=""
stop_app=0
key_file=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --new-key-file) key_file="${2:?--new-key-file needs a value}"; shift ;;
    --stop-app) stop_app=1 ;;
    -f|--file) compose_file="${2:?--file needs a value}"; shift ;;
    -p|--project) project="${2:?--project needs a value}"; shift ;;
    --env-file) env_file="${2:?--env-file needs a value}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "rotate-secret-key: unknown argument $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

log() { printf 'rotate-secret-key: %s\n' "$*" >&2; }

if [[ -n "$key_file" ]]; then
  [[ -f "$key_file" ]] || { log "file not found: $key_file"; exit 2; }
  BANSOKO_NEW_SECRET_ENCRYPTION_KEY="$(tr -d '\r\n' < "$key_file")"
fi
new_key="${BANSOKO_NEW_SECRET_ENCRYPTION_KEY:-}"
if (( ${#new_key} < 32 )); then
  log "give the new key with --new-key-file or BANSOKO_NEW_SECRET_ENCRYPTION_KEY (at least 32 characters)"
  exit 2
fi
export BANSOKO_NEW_SECRET_ENCRYPTION_KEY="$new_key"
unset new_key

# Project: -p/--project or COMPOSE_PROJECT_NAME wins. Otherwise only the
# bundled production compose gets the "bansoko" name it is deployed under; a
# compose file the caller names is left to compose's own derivation from that
# file's directory, so pointing -f at another stack cannot act on ours.
rotation_compose_defaults
rotation_stop_app rotate-secret-key "$stop_app" || exit $?

log "starting postgres"
dc up -d --wait postgres

# shellcheck disable=SC2016 # expanded by the container shell, not here
inner='
set -eu
bin=/app/bansoko-server
updates_root="${BANSOKO_UPDATES_ROOT:-}"
if [ -n "$updates_root" ] && [ -f "$updates_root/current" ]; then
  token="$(tr -d " \n\r\t" < "$updates_root/current")"
  bin="$updates_root/releases/$token/bansoko-server"
  [ -x "$bin" ] || { echo "rotate-secret-key: the current update pointer names a release without bansoko-server ($token)" >&2; exit 1; }
fi
grep -q rotate-secret-key "$bin" || { echo "rotate-secret-key: the running release does not have this command yet; update first" >&2; exit 1; }
[ -n "${BANSOKO_DATABASE_URL:-}" ] || { echo "rotate-secret-key: the app environment has no BANSOKO_DATABASE_URL" >&2; exit 1; }
exec "$bin" migrate rotate-secret-key
'

log "re-encrypting"
dc run --rm --no-deps -T -e BANSOKO_NEW_SECRET_ENCRYPTION_KEY --entrypoint /bin/sh app -c "$inner"

log "done. The app is stopped. If .env sets BANSOKO_SECRET_ENCRYPTION_KEY, put the new key there."
rotation_start_hint rotate-secret-key
