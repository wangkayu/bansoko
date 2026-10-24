#!/usr/bin/env bash
# Bansoko — restore a backup file from the host (fallback for Settings > Backups).
#
# The normal way to restore is uploading the file in Settings > Backups. Use
# this script when the app cannot start. Run it on the deployment host next to
# the production compose:
#
#   deployment/scripts/restore.sh [--stop-app] ./bansoko-20261020T031700Z-schema038.bansoko-backup
#
# What it does, in order (any failure stops before the live database changes):
#   1. refuses while the app runs, unless --stop-app;
#   2. starts postgres only and waits for it to be healthy;
#   3. in a one-off container of the app image (same env, PG 17 client tools),
#      checks the BANSOKO-BACKUP/1 signature, the payload size and sha256, and
#      that the backup's schema version is the one the running release has
#      (current embedded-update overlay, else the image);
#   4. restores into a fresh scratch database in one transaction, requires the
#      same schema version there, and signs every session out;
#   5. swaps databases by rename and drops the previous one;
#   6. starts the app and waits for the container to be healthy and /readyz.
#
# Export the current data first if you may want it back: the previous
# database is not kept. Connection credentials stay inside the container
# environment (PG* variables derived from the schema-owner BANSOKO_DATABASE_URL);
# nothing secret is put on argv.
#
# Compose selection: -f/--file and -p/--project, else COMPOSE_FILE /
# COMPOSE_PROJECT_NAME from the environment, else the production compose next
# to this script and project "bansoko". --env-file is passed through.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  sed -n '2,29p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'EOF'

Options:
  --stop-app           stop the app first instead of refusing while it runs
  --no-start           leave the app stopped after the restore
  -f, --file FILE      compose file
  -p, --project NAME   compose project name
  --env-file FILE      compose env file
  --timeout SECONDS    how long to wait for the app to become healthy (300)
EOF
}

compose_file=""
project="${COMPOSE_PROJECT_NAME:-bansoko}"
env_file=""
stop_app=0
start_app=1
timeout=300
source_arg=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stop-app) stop_app=1 ;;
    --no-start) start_app=0 ;;
    -f|--file) compose_file="${2:?--file needs a value}"; shift ;;
    -p|--project) project="${2:?--project needs a value}"; shift ;;
    --env-file) env_file="${2:?--env-file needs a value}"; shift ;;
    --timeout) timeout="${2:?--timeout needs a value}"; shift ;;
    -h|--help) usage; exit 0 ;;
    -*) echo "restore: unknown option $1" >&2; usage >&2; exit 2 ;;
    *)
      if [[ -n "$source_arg" ]]; then
        echo "restore: only one backup file may be given" >&2
        exit 2
      fi
      source_arg="$1"
      ;;
  esac
  shift
done

if [[ -z "$compose_file" && -z "${COMPOSE_FILE:-}" ]]; then
  compose_file="$script_dir/../maintainer/docker-compose.production.yml"
fi
if [[ -z "$source_arg" ]]; then
  echo "restore: name the backup file on this host" >&2
  exit 2
fi
if [[ ! -f "$source_arg" ]]; then
  echo "restore: file not found: $source_arg" >&2
  exit 2
fi
if ! [[ "$timeout" =~ ^[0-9]+$ ]]; then
  echo "restore: --timeout must be a number of seconds" >&2
  exit 2
fi
host_path="$(cd "$(dirname "$source_arg")" && pwd)/$(basename "$source_arg")"

dc() {
  local args=(-p "$project")
  [[ -n "$compose_file" ]] && args+=(-f "$compose_file")
  [[ -n "$env_file" ]] && args+=(--env-file "$env_file")
  docker compose "${args[@]}" "$@"
}

log() { printf 'restore: %s\n' "$*" >&2; }

# --- inner script: runs inside a one-off app-image container ----------------
# shellcheck disable=SC2016 # expanded by the container shell, not here
inner='
set -eu
log() { printf "restore: %s\n" "$*" >&2; }
die() { log "error: $*"; exit 1; }

pct_decode() {
  printf "%s" "$1" | awk '"'"'BEGIN { for (i = 1; i < 256; i++) hex[sprintf("%02x", i)] = sprintf("%c", i) }
    { s = $0; out = ""
      while (match(s, /%[0-9A-Fa-f][0-9A-Fa-f]/)) { out = out substr(s, 1, RSTART - 1) hex[tolower(substr(s, RSTART + 1, 2))]; s = substr(s, RSTART + 3) }
      printf "%s", out s }'"'"'
}

url="${BANSOKO_DATABASE_URL:-}"
[ -n "$url" ] || die "the app environment has no database URL"
rest="${url#*://}"
userinfo="${rest%@*}"
hostpart="${rest##*@}"
PGUSER="$(pct_decode "${userinfo%%:*}")"
PGPASSWORD="$(pct_decode "${userinfo#*:}")"
hostport="${hostpart%%/*}"
PGHOST="${hostport%%:*}"
PGPORT=5432
case "$hostport" in *:*) PGPORT="${hostport##*:}" ;; esac
dbq="${hostpart#*/}"
db="$(pct_decode "${dbq%%\?*}")"
PGSSLMODE=prefer
case "$dbq" in *sslmode=*) PGSSLMODE="$(printf "%s" "${dbq#*sslmode=}" | cut -d "&" -f1)" ;; esac
export PGUSER PGPASSWORD PGHOST PGPORT PGSSLMODE
export PGCONNECT_TIMEOUT=10 PGAPPNAME=bansoko-restore
printf "%s" "$db" | grep -Eq "^[A-Za-z0-9_]{1,40}$" || die "unsupported database name"

q() { PGDATABASE="$1" psql -X -q -v ON_ERROR_STOP=1 -At -c "$2"; }
field() { printf "%s" "$header" | sed -n "s/.*\"$1\":\"\{0,1\}\([^\",}]*\)\"\{0,1\}[,}].*/\1/p"; }

# The backup file: signature line, one JSON header line, then the archive.
src=/restore-input.bansoko-backup
[ "$(head -c 4096 "$src" | head -n 1)" = "BANSOKO-BACKUP/1" ] || die "the file is not a bansoko backup"
header="$(head -c 8192 "$src" | sed -n 2p)"
[ "$(field app)" = bansoko ] && [ "$(field format)" = 1 ] || die "the file is not a bansoko backup"
backup_schema="$(field schema_version)"
printf "%s" "$backup_schema" | grep -Eq "^[0-9]{3,6}_[a-z0-9_]{1,120}\.sql$" || die "the backup header is malformed"
size="$(field payload_bytes)"
sum="$(field payload_sha256)"
printf "%s" "$size" | grep -Eq "^[1-9][0-9]{0,11}$" || die "the backup header is malformed"
printf "%s" "$sum" | grep -Eq "^[0-9a-f]{64}$" || die "the backup header is malformed"

# The schema the running release has.
schema_dir=/app/db/migrations
updates_root="${BANSOKO_UPDATES_ROOT:-}"
if [ -n "$updates_root" ] && [ -f "$updates_root/current" ]; then
  token="$(tr -d " \n\r\t" < "$updates_root/current")"
  schema_dir="$updates_root/releases/$token/db/migrations"
  [ -d "$schema_dir" ] || die "the current update pointer names a release that is missing ($token)"
fi
newest="$(ls -1 "$schema_dir" | grep -E "^[0-9]+_.*\.sql$" | sort | tail -n 1 || true)"
[ -n "$newest" ] || die "no migrations found in $schema_dir"
[ "$backup_schema" = "$newest" ] || die "the backup schema ($backup_schema) is not the release schema ($newest); nothing was changed"

work="$(mktemp -d)"
stamp="$(date -u +%s)"
scratch="${db}__restoring_${stamp}"
previous="${db}__replaced_${stamp}"
swapped=0
cleanup() {
  rm -rf "$work"
  if [ "$swapped" = 0 ]; then
    q postgres "DROP DATABASE IF EXISTS \"$scratch\" WITH (FORCE)" > /dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

offset=$(( $(head -n 2 "$src" | wc -c) + 1 ))
tail -c "+$offset" "$src" > "$work/payload.dump"
[ "$(wc -c < "$work/payload.dump" | tr -d " ")" = "$size" ] || die "the backup file is truncated or has extra data; nothing was changed"
[ "$(sha256sum "$work/payload.dump" | cut -d " " -f1)" = "$sum" ] || die "the backup file is damaged; nothing was changed"
pg_restore --list "$work/payload.dump" > /dev/null 2>&1 || die "the backup archive is unreadable; nothing was changed"
log "backup schema $backup_schema matches the release"

log "restoring into scratch database $scratch"
q postgres "CREATE DATABASE \"$scratch\" TEMPLATE template0" > /dev/null
pg_restore --exit-on-error --single-transaction --no-owner --dbname="$scratch" "$work/payload.dump" \
  || die "restore into the scratch database failed; the current database is unchanged"
restored="$(q "$scratch" "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1" 2>/dev/null || true)"
[ "$restored" = "$newest" ] || die "the restored schema ($restored) is not the release schema ($newest); the current database is unchanged"
q "$scratch" "DELETE FROM auth_sessions" > /dev/null

q postgres "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname IN ('"'"'$db'"'"', '"'"'$scratch'"'"') AND pid <> pg_backend_pid()" > /dev/null
q postgres "ALTER DATABASE \"$db\" RENAME TO \"$previous\"" > /dev/null || die "could not set the current database aside; nothing was changed"
if ! q postgres "ALTER DATABASE \"$scratch\" RENAME TO \"$db\"" > /dev/null; then
  q postgres "ALTER DATABASE \"$previous\" RENAME TO \"$db\"" > /dev/null || true
  die "could not activate the restored database; the previous database was put back"
fi
swapped=1
q postgres "DROP DATABASE \"$previous\" WITH (FORCE)" > /dev/null || log "could not drop the previous database $previous; drop it yourself"
log "restored"
'

# --- host side ---------------------------------------------------------------

running="$(dc ps --status running --status restarting --services 2>/dev/null | grep -Ex 'app' || true)"
if [[ -n "$running" ]]; then
  if [[ "$stop_app" -eq 0 ]]; then
    log "the app is running ($(echo "$running" | tr '\n' ' ')); stop it first or pass --stop-app"
    exit 3
  fi
  log "stopping: $(echo "$running" | tr '\n' ' ')"
  # shellcheck disable=SC2086 # service names are fixed words
  dc stop $running
fi

log "starting postgres"
dc up -d --wait postgres

dc run --rm --no-deps -T -v "$host_path:/restore-input.bansoko-backup:ro" --entrypoint /bin/sh app -c "$inner"

if [[ "$start_app" -eq 0 ]]; then
  log "done; the app was left stopped (--no-start)"
  exit 0
fi

log "starting the app"
dc up -d app
container="$(dc ps -q app)"
deadline=$((SECONDS + timeout))
health=""
while (( SECONDS < deadline )); do
  health="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container" 2>/dev/null || true)"
  [[ "$health" == healthy ]] && break
  sleep 3
done
if [[ "$health" != healthy ]]; then
  log "the app did not become healthy within ${timeout}s (state: ${health:-unknown}); check: docker compose -p $project logs app"
  exit 4
fi
if dc exec -T app wget -q -O /dev/null http://127.0.0.1:8080/api/v1/readyz; then
  log "done; the app is healthy and ready"
else
  log "the app is healthy but not ready yet; check /api/v1/readyz and the app log"
  exit 4
fi
