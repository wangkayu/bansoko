#!/usr/bin/env bash
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"

if [[ "${BANSOKO_BACKUP_REHEARSAL:-0}" != "1" ]]; then
  echo "set BANSOKO_BACKUP_REHEARSAL=1 to run the isolated backup rehearsal" >&2
  exit 2
fi
if [[ "${BANSOKO_ENVIRONMENT:-development}" == "production" ]]; then
  echo "backup rehearsal refuses production environment" >&2
  exit 2
fi

source_url="${BANSOKO_BACKUP_SOURCE_URL:-}"
restore_url="${BANSOKO_BACKUP_RESTORE_URL:-}"
output_dir="${BANSOKO_BACKUP_OUTPUT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/bansoko-backup.XXXXXX")}"
mkdir -p "$output_dir"
if [[ -z "$source_url" || -z "$restore_url" ]]; then
  echo "BANSOKO_BACKUP_SOURCE_URL and BANSOKO_BACKUP_RESTORE_URL are required" >&2
  exit 2
fi
if [[ "$source_url" == "$restore_url" ]]; then
  echo "backup rehearsal source and restore database must be different" >&2
  exit 2
fi

find_bin() {
  local name="$1"
  if command -v "$name" >/dev/null 2>&1; then
    command -v "$name"
    return 0
  fi
  return 1
}

psql_bin="${PSQL_BIN:-}"
pg_dump_bin="${PG_DUMP_BIN:-}"
pg_restore_bin="${PG_RESTORE_BIN:-}"
explicit_psql_bin="$psql_bin"
explicit_pg_dump_bin="$pg_dump_bin"
explicit_pg_restore_bin="$pg_restore_bin"
explicit_tools=0
if [[ -n "$explicit_psql_bin" || -n "$explicit_pg_dump_bin" || -n "$explicit_pg_restore_bin" ]]; then
  explicit_tools=1
fi

version_major() {
  printf '%s\n' "$1" | awk 'match($0, /[0-9]+[.][0-9]+/) { v=substr($0, RSTART, RLENGTH); sub(/[.].*$/, "", v); print v; exit }'
}

server_version_major() {
  printf '%s\n' "$1" | awk '{ major=int($1 / 10000); print major; exit }'
}

local_tool_version() {
  local bin="$1"
  "$bin" --version 2>&1 | tr '\n' ' '
}

assert_pg17_version() {
  local label="$1"
  local version="$2"
  local major
  major="$(version_major "$version")"
  if [[ "$major" != "17" ]]; then
    echo "$label must be PostgreSQL major 17, got: $version" >&2
    exit 2
  fi
}

assert_pg17_server_version_num() {
  local label="$1"
  local version_num="$2"
  local major
  major="$(server_version_major "$version_num")"
  if [[ "$major" != "17" ]]; then
    echo "$label must report PostgreSQL server major 17, got server_version_num=$version_num" >&2
    exit 2
  fi
}

backup_tool_mode="${BANSOKO_BACKUP_TOOL_MODE:-auto}"
case "$backup_tool_mode" in
  auto|local|docker) ;;
  *) echo "BANSOKO_BACKUP_TOOL_MODE must be auto, local, or docker" >&2; exit 2 ;;
esac
if [[ "$explicit_tools" == "1" ]]; then
  backup_tool_mode="local"
  if [[ -n "$explicit_psql_bin" ]]; then
    [[ -x "$explicit_psql_bin" ]] || { echo "PSQL_BIN is not executable: $explicit_psql_bin" >&2; exit 2; }
    psql_version="$(local_tool_version "$explicit_psql_bin")" || { echo "failed to execute PSQL_BIN" >&2; exit 2; }
    assert_pg17_version "PSQL_BIN" "$psql_version"
  fi
  if [[ -n "$explicit_pg_dump_bin" ]]; then
    [[ -x "$explicit_pg_dump_bin" ]] || { echo "PG_DUMP_BIN is not executable: $explicit_pg_dump_bin" >&2; exit 2; }
    pg_dump_version="$(local_tool_version "$explicit_pg_dump_bin")" || { echo "failed to execute PG_DUMP_BIN" >&2; exit 2; }
    assert_pg17_version "PG_DUMP_BIN" "$pg_dump_version"
  fi
  if [[ -n "$explicit_pg_restore_bin" ]]; then
    [[ -x "$explicit_pg_restore_bin" ]] || { echo "PG_RESTORE_BIN is not executable: $explicit_pg_restore_bin" >&2; exit 2; }
    pg_restore_version="$(local_tool_version "$explicit_pg_restore_bin")" || { echo "failed to execute PG_RESTORE_BIN" >&2; exit 2; }
    assert_pg17_version "PG_RESTORE_BIN" "$pg_restore_version"
  fi
elif [[ "$backup_tool_mode" == "local" ]]; then
  :
elif [[ "$backup_tool_mode" == "docker" ]]; then
  :
else
  local_tools_ready=1
  if [[ -z "$psql_bin" ]]; then psql_bin="$(find_bin psql || true)"; fi
  if [[ -z "$pg_dump_bin" ]]; then pg_dump_bin="$(find_bin pg_dump || true)"; fi
  if [[ -z "$pg_restore_bin" ]]; then pg_restore_bin="$(find_bin pg_restore || true)"; fi
  if [[ -z "$psql_bin" || -z "$pg_dump_bin" || -z "$pg_restore_bin" ]]; then
    local_tools_ready=0
  else
    if ! psql_version="$(local_tool_version "$psql_bin")"; then local_tools_ready=0; fi
    if ! pg_dump_version="$(local_tool_version "$pg_dump_bin")"; then local_tools_ready=0; fi
    if ! pg_restore_version="$(local_tool_version "$pg_restore_bin")"; then local_tools_ready=0; fi
    if [[ "$local_tools_ready" == "1" ]]; then
      if [[ "$(version_major "$psql_version")" != "17" || "$(version_major "$pg_dump_version")" != "17" || "$(version_major "$pg_restore_version")" != "17" ]]; then
        local_tools_ready=0
      fi
    fi
  fi
  if [[ "$local_tools_ready" == "0" ]]; then
    backup_tool_mode="docker"
  else
    backup_tool_mode="local"
  fi
fi

tool_mode="$backup_tool_mode"
tool_image=""
if [[ "$tool_mode" == "local" ]]; then
  if [[ -z "$psql_bin" ]]; then psql_bin="$(find_bin psql || true)"; fi
  if [[ -z "$pg_dump_bin" ]]; then pg_dump_bin="$(find_bin pg_dump || true)"; fi
  if [[ -z "$pg_restore_bin" ]]; then pg_restore_bin="$(find_bin pg_restore || true)"; fi
  if [[ -z "$psql_bin" || -z "$pg_dump_bin" || -z "$pg_restore_bin" ]]; then
    echo "local PG17 tools require psql, pg_dump, and pg_restore" >&2
    exit 2
  fi
  if [[ ! -x "$psql_bin" || ! -x "$pg_dump_bin" || ! -x "$pg_restore_bin" ]]; then
    echo "local PG17 tools must be executable" >&2
    exit 2
  fi
elif [[ "$tool_mode" == "docker" ]]; then
  tool_image="${BANSOKO_BACKUP_TOOL_IMAGE:-postgres:17-alpine}"
  if ! command -v docker >/dev/null 2>&1 || ! docker image inspect "$tool_image" >/dev/null 2>&1; then
    echo "PG17 Docker tool image is unavailable; refusing to pull automatically: $tool_image" >&2
    exit 2
  fi
  psql_bin="psql"
  pg_dump_bin="pg_dump"
  pg_restore_bin="pg_restore"
fi

container_url() {
  printf '%s' "$1" | sed -E \
    -e 's#(://)(localhost|127[.]0[.]0[.]1)(:|/)#\1host.docker.internal\3#g' \
    -e 's#(@)(localhost|127[.]0[.]0[.]1)(:|/)#\1host.docker.internal\3#g'
}

run_pg_tool() {
  local tool="$1"
  shift
  if [[ "$tool_mode" == "local" ]]; then
    "$tool" "$@"
    return
  fi

  local -a mapped_args=()
  local arg
  for arg in "$@"; do
    case "$arg" in
      "$output_dir"/*) mapped_args+=("/rehearsal/${arg#"$output_dir"/}") ;;
      --file="$output_dir"/*) mapped_args+=("--file=/rehearsal/${arg#--file="$output_dir"/}") ;;
      *) mapped_args+=("$arg") ;;
    esac
  done
  docker run --rm --add-host host.docker.internal:host-gateway \
    -v "$output_dir:/rehearsal" "$tool_image" "$tool" "${mapped_args[@]}"
}

get_tool_version() {
  local tool="$1"
  if [[ "$tool_mode" == "local" ]]; then
    local_tool_version "$tool"
  else
    run_pg_tool "$tool" --version 2>&1 | tr '\n' ' '
  fi
}

psql_version="$(get_tool_version "$psql_bin")" || { echo "failed to execute psql --version" >&2; exit 2; }
pg_dump_version="$(get_tool_version "$pg_dump_bin")" || { echo "failed to execute pg_dump --version" >&2; exit 2; }
pg_restore_version="$(get_tool_version "$pg_restore_bin")" || { echo "failed to execute pg_restore --version" >&2; exit 2; }
assert_pg17_version "psql" "$psql_version"
assert_pg17_version "pg_dump" "$pg_dump_version"
assert_pg17_version "pg_restore" "$pg_restore_version"

tool_source_url="$source_url"
tool_restore_url="$restore_url"
if [[ "$tool_mode" == "docker" ]]; then
  tool_source_url="$(container_url "$source_url")"
  tool_restore_url="$(container_url "$restore_url")"
fi

read_database_identity() {
  local label="$1"
  local url="$2"
  local result
  if ! result="$(run_pg_tool "$psql_bin" "$url" -At -F $'\t' -v ON_ERROR_STOP=1 -c "SELECT current_database(), COALESCE(inet_server_addr()::text, ''), inet_server_port(), (pg_control_system()).system_identifier, current_setting('server_version_num')")"; then
    echo "$label database identity preflight failed closed" >&2
    return 1
  fi
  if [[ -z "$result" ]]; then
    echo "$label database identity preflight returned no identity" >&2
    return 1
  fi
  printf '%s' "$result"
}

identity_digest() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "$1" | sha256sum | awk '{print $1}'
  else
    printf '%s' "$1" | shasum -a 256 | awk '{print $1}'
  fi
}

archive="$output_dir/v2-backup.dump"
manifest="$output_dir/manifest.txt"
source_identity_raw="$(read_database_identity source "$tool_source_url")" || exit 2
restore_identity_raw="$(read_database_identity restore "$tool_restore_url")" || exit 2
IFS=$'\t' read -r source_database source_server_addr source_server_port source_system_identifier source_server_version_num <<EOF
$source_identity_raw
EOF
IFS=$'\t' read -r restore_database restore_server_addr restore_server_port restore_system_identifier restore_server_version_num <<EOF
$restore_identity_raw
EOF
if [[ -z "$source_database" || -z "$source_system_identifier" || -z "$source_server_version_num" || -z "$restore_database" || -z "$restore_system_identifier" || -z "$restore_server_version_num" ]]; then
  echo "database identity preflight was incomplete; refusing destructive operations" >&2
  exit 2
fi
assert_pg17_server_version_num "source PostgreSQL server" "$source_server_version_num"
assert_pg17_server_version_num "restore PostgreSQL server" "$restore_server_version_num"
if [[ "$source_identity_raw" == "$restore_identity_raw" || ( "$source_database" == "$restore_database" && "$source_system_identifier" == "$restore_system_identifier" ) ]]; then
  echo "source and restore database identities are the same; refusing destructive restore" >&2
  exit 2
fi
source_identity_digest="sha256:$(identity_digest "$source_identity_raw")"
restore_identity_digest="sha256:$(identity_digest "$restore_identity_raw")"
source_server_version="$(run_pg_tool "$psql_bin" "$tool_source_url" -At -v ON_ERROR_STOP=1 -c "SHOW server_version")"
restore_server_version="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SHOW server_version")"
assert_pg17_version "source PostgreSQL server" "$source_server_version"
assert_pg17_version "restore PostgreSQL server" "$restore_server_version"

if ! run_pg_tool "$pg_dump_bin" "$tool_source_url" --format=custom --no-owner --file="$archive" >"$output_dir/pg_dump.log" 2>&1; then
  echo "pg_dump failed:" >&2
  tail -10 "$output_dir/pg_dump.log" >&2 || true
  exit 1
fi
run_pg_tool "$pg_restore_bin" --list "$archive" >"$output_dir/archive.list"
required_archive_members=(
  "schema_identity"
  "schema_migrations"
  "instance_bootstrap"
)
for member in "${required_archive_members[@]}"; do
  if ! grep -q "$member" "$output_dir/archive.list"; then
    echo "backup archive does not contain required table: $member" >&2
    exit 1
  fi
done

if ! run_pg_tool "$pg_restore_bin" --clean --if-exists --no-owner --no-privileges --exit-on-error --single-transaction --dbname="$tool_restore_url" "$archive" >"$output_dir/pg_restore.log" 2>&1; then
  echo "pg_restore failed:" >&2
  tail -10 "$output_dir/pg_restore.log" >&2 || true
  exit 1
fi
restore_identity_post_raw="$(read_database_identity restore "$tool_restore_url")" || exit 2
IFS=$'\t' read -r restore_database_post restore_server_addr_post restore_server_port_post restore_system_identifier_post restore_server_version_num <<EOF
$restore_identity_post_raw
EOF
assert_pg17_server_version_num "source PostgreSQL server" "$(run_pg_tool "$psql_bin" "$tool_source_url" -At -v ON_ERROR_STOP=1 -c "SHOW server_version_num")"
assert_pg17_server_version_num "restore PostgreSQL server" "$restore_server_version_num"
restore_server_version="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SHOW server_version")"
assert_pg17_version "restore PostgreSQL server" "$restore_server_version"
identity="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SELECT identity FROM schema_identity WHERE singleton_id = 1")"
baseline_version="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SELECT baseline_version FROM schema_identity WHERE singleton_id = 1")"
history_count="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SELECT COUNT(*) FROM schema_migrations")"
history_version="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SELECT version FROM schema_migrations ORDER BY version LIMIT 1")"
schedule_count="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SELECT COUNT(*) FROM automation_schedules")"
install_state="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SELECT state FROM instance_bootstrap WHERE singleton_id = 1")"
install_id="$(run_pg_tool "$psql_bin" "$tool_restore_url" -At -v ON_ERROR_STOP=1 -c "SELECT install_id FROM instance_bootstrap WHERE singleton_id = 1")"
expected_history="$(find "$root/db/migrations" -maxdepth 1 -name '*.sql' -type f | wc -l | tr -d '[:space:]')"
if [[ "$identity" != "bansoko:baseline" || "$baseline_version" != "001_baseline.sql" || "$history_count" != "$expected_history" || "$history_version" != "001_baseline.sql" || "$schedule_count" != "4" || -z "$install_state" || -z "$install_id" ]]; then
  echo "restored database failed catalog smoke: identity=$identity baseline=$baseline_version migrations=$history_count history_version=$history_version schedules=$schedule_count state=$install_state" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  digest=$(sha256sum "$archive" | awk '{print $1}')
else
  digest=$(shasum -a 256 "$archive" | awk '{print $1}')
fi
{
  echo "kind=backup-restore-rehearsal"
  echo "tool_mode=$tool_mode"
  if [[ "$tool_mode" == "docker" ]]; then echo "tool_image=$tool_image"; fi
  echo "psql_version=$psql_version"
  echo "pg_dump_version=$pg_dump_version"
  echo "pg_restore_version=$pg_restore_version"
  echo "source_server_version=$source_server_version"
  echo "restore_server_version=$restore_server_version"
  echo "source_server_version_num=$source_server_version_num"
  echo "restore_server_version_num=$restore_server_version_num"
  echo "source_identity=$source_identity_digest"
  echo "restore_identity=$restore_identity_digest"
  echo "archive_digest=sha256:$digest"
  echo "verification_scope=backup_restore_catalog"
  echo "schema_identity=$identity"
  echo "baseline_version=$baseline_version"
  echo "schema_history_rows=$history_count"
  echo "expected_schema_history_rows=$expected_history"
  echo "history_version=$history_version"
  echo "automation_schedule_rows=$schedule_count"
  echo "instance_bootstrap_state=$install_state"
  echo "verified=true"
  echo "source=redacted"
  echo "restore=redacted"
} >"$manifest"
echo "backup restore rehearsal passed: $manifest"
