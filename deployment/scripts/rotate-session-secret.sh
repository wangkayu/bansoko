#!/usr/bin/env bash
# Bansoko — rotate BANSOKO_SESSION_SECRET (SEC-033).
#
# BANSOKO_SESSION_SECRET keys the hash of every web session and API token. A new
# secret ends every web session, so rotate it only when you mean to sign every
# browser out. This does it with the app stopped and keeps your API tokens
# working: their stored hash is recomputed from the encrypted raw value, so
# cron jobs, scripts and agents do not need new tokens.
#
#   deployment/scripts/rotate-session-secret.sh --stop-app
#   # then start the app: docker compose ... up -d app
#
# What it does, in order (any failure stops with nothing changed):
#   1. refuses while the app runs, unless --stop-app;
#   2. starts postgres only and waits for it to be healthy;
#   3. in a one-off container of the app image (same env and /config), runs
#      `bansoko-migrate rotate-session-secret` from the release the app runs
#      (current embedded-update overlay, else the image). That command generates
#      a new secret, refuses while any other session is connected, then in one
#      transaction revokes every web session, re-hashes every API token from its
#      encrypted raw value and drops the tokens whose raw value cannot be
#      recovered (they would no longer authenticate, so keeping them would show
#      a dead key);
#   4. stores the new secret in /config/secrets.json and leaves the app stopped.
#      If BANSOKO_SESSION_SECRET is set in .env (an override), that value wins
#      at every start: the rotation refuses rather than store a secret the app
#      will not use, so rotate by updating the override yourself.
#
# Rotating this secret also invalidates the hashes in any backup taken before
# the rotation: a restore of such a backup needs the old secret back.
#
# Compose selection: -f/--file and -p/--project, else COMPOSE_FILE /
# COMPOSE_PROJECT_NAME from the environment, else the production compose next
# to this script and project "bansoko". --env-file is passed through.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/rotation-compose.sh
. "$script_dir/lib/rotation-compose.sh"

usage() {
  sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' | sed '$d'
  cat <<'USAGE'

Options:
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

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stop-app) stop_app=1 ;;
    -f|--file) compose_file="${2:?--file needs a value}"; shift ;;
    -p|--project) project="${2:?--project needs a value}"; shift ;;
    --env-file) env_file="${2:?--env-file needs a value}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "rotate-session-secret: unknown argument $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

log() { printf 'rotate-session-secret: %s\n' "$*" >&2; }

rotation_compose_defaults
rotation_stop_app rotate-session-secret "$stop_app" || exit $?

log "starting postgres"
dc up -d --wait postgres

# shellcheck disable=SC2016 # expanded by the container shell, not here
inner='
set -eu
bin=/app/bansoko-migrate
updates_root="${BANSOKO_UPDATES_ROOT:-}"
if [ -n "$updates_root" ] && [ -f "$updates_root/current" ]; then
  token="$(tr -d " \n\r\t" < "$updates_root/current")"
  bin="$updates_root/releases/$token/bansoko-migrate"
  [ -x "$bin" ] || { echo "rotate-session-secret: the current update pointer names a release without bansoko-migrate ($token)" >&2; exit 1; }
fi
grep -q rotate-session-secret "$bin" || { echo "rotate-session-secret: the running release does not have this command yet; update first" >&2; exit 1; }
[ -n "${BANSOKO_DATABASE_URL:-}" ] || { echo "rotate-session-secret: the app environment has no BANSOKO_DATABASE_URL" >&2; exit 1; }
if [ -n "${BANSOKO_SESSION_SECRET:-}" ]; then
  echo "rotate-session-secret: BANSOKO_SESSION_SECRET is set in the environment; update that value instead of storing a new one in the config directory" >&2
  exit 1
fi
exec "$bin" rotate-session-secret
'

log "rotating"
dc run --rm --no-deps -T --entrypoint /bin/sh app -c "$inner"

log "done. The app is stopped."
rotation_start_hint rotate-session-secret
