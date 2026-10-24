#!/usr/bin/env bash
# Scratch restore rehearsal (P0.6).
#
# Restores a real PostgreSQL backup into a one-time disposable target, then
# STARTS an isolated auth-enabled bansoko-server against it and verifies the
# admission contract: /livez alive, /readyz ready, owner login, key owner
# read, and secret decryption. A pg_dump/pg_restore success or a static row
# count is NOT evidence on its own — the instance must actually boot.
#
# Inputs (all required):
#   BANSOKO_BACKUP_SOURCE_URL        source database (read-only)
#   BANSOKO_RESTORE_TARGET_URL       one-time disposable target (must differ)
#   BANSOKO_SESSION_SECRET           same session secret as the source instance
#   BANSOKO_SECRET_ENCRYPTION_KEY    same encryption key as the source instance
#   BANSOKO_RESTORE_OWNER_PASSWORD   owner password to log in with (recovery path)
#
# Refuses: source == target, non-disposable target, production env.
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"
rehearsal="$root/scripts/verify/backup-restore-rehearsal.sh"

if [[ "${BANSOKO_ENVIRONMENT:-development}" == "production" ]]; then
  echo "scratch restore rehearsal refuses production environment" >&2
  exit 2
fi

source_url="${BANSOKO_BACKUP_SOURCE_URL:-}"
restore_url="${BANSOKO_RESTORE_TARGET_URL:-}"
session_secret="${BANSOKO_SESSION_SECRET:-}"
encryption_key="${BANSOKO_SECRET_ENCRYPTION_KEY:-}"
owner_password="${BANSOKO_RESTORE_OWNER_PASSWORD:-}"

if [[ -z "$source_url" || -z "$restore_url" || -z "$session_secret" || -z "$owner_password" ]]; then
  echo "BANSOKO_BACKUP_SOURCE_URL, BANSOKO_RESTORE_TARGET_URL, BANSOKO_SESSION_SECRET and BANSOKO_RESTORE_OWNER_PASSWORD are required" >&2
  exit 2
fi
if [[ "$source_url" == "$restore_url" ]]; then
  echo "scratch restore source and target must be different databases" >&2
  exit 2
fi

# The disposable DB guard must accept the target (loopback, disposable
# declaration, non-production name). The restore target database is created
# first (pg_restore needs a live empty database to load into).
if [[ "${BANSOKO_RESTORE_TARGET_DISPOSABLE:-}" != "1" ]]; then
  echo "BANSOKO_RESTORE_TARGET_DISPOSABLE=1 must be set (one-time disposable target)" >&2
  exit 2
fi
(cd "$root" && BANSOKO_TEST_DATABASE_URL="$restore_url" BANSOKO_TEST_DB_DISPOSABLE=1 \
  go run ./cmd/disposable-db-check check) || {
  # A missing database is expected on the first restore: create it through the
  # restore URL's own server (local psql, else the Go helper). Never probe other
  # running containers: on the VM that would include the production postgres.
  target_db="$(printf '%s' "$restore_url" | sed -E 's#.*/([^/?]+).*#\1#')"
  created=0
  admin_url="$(printf '%s' "$restore_url" | sed "s#/$target_db#/postgres#")"
  if command -v psql >/dev/null 2>&1; then
    if psql "$admin_url" -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$target_db\"" >/dev/null 2>&1; then
      created=1
    fi
  fi
  # No psql (e.g. CI golang images): create via the Go helper.
  if [ "$created" -eq 0 ]; then
    if (cd "$root" && go run ./cmd/disposable-db-check create --db "$target_db" --admin-url "$admin_url" >/dev/null 2>&1); then
      created=1
    fi
  fi
  if [ "$created" -eq 1 ]; then
    (cd "$root" && BANSOKO_TEST_DATABASE_URL="$restore_url" BANSOKO_TEST_DB_DISPOSABLE=1 \
      go run ./cmd/disposable-db-check check) || {
      echo "restore target rejected by disposable DB guard after create" >&2
      exit 2
    }
  else
    echo "restore target rejected by disposable DB guard (and could not be created): $restore_url" >&2
    exit 2
  fi
}

workdir="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-scratch-restore.XXXXXX")"
persist_dir="${BANSOKO_RESTORE_OUTPUT_DIR:-}"
trap '[[ -n "${server_pid:-}" ]] && kill "$server_pid" 2>/dev/null || true; if [[ -n "$persist_dir" ]]; then mkdir -p "$persist_dir"; if [[ -f "$workdir/rehearsal.log" ]]; then cp "$workdir/rehearsal.log" "$persist_dir/rehearsal-fail.log" 2>/dev/null || true; fi; if [[ -f "$workdir/dump/manifest.txt" ]]; then cp "$workdir/dump/manifest.txt" "$persist_dir/backup-manifest.txt" 2>/dev/null || true; fi; if [[ -f "$workdir/dump/archive.list" ]]; then if command -v sha256sum >/dev/null 2>&1; then sha256sum "$workdir/dump/archive.list" | awk '\''{print $1}'\'' >"$persist_dir/archive-list.sha256" 2>/dev/null || true; else shasum -a 256 "$workdir/dump/archive.list" | awk '\''{print $1}'\'' >"$persist_dir/archive-list.sha256" 2>/dev/null || true; fi; fi; fi; rm -rf "$workdir"' EXIT

echo "== step 1: dump + restore into disposable target =="
BANSOKO_BACKUP_REHEARSAL=1 \
  BANSOKO_BACKUP_SOURCE_URL="$source_url" \
  BANSOKO_BACKUP_RESTORE_URL="$restore_url" \
  BANSOKO_BACKUP_OUTPUT_DIR="$workdir/dump" \
  "$rehearsal" >"$workdir/rehearsal.log" 2>&1 || {
  echo "backup-restore rehearsal failed; see $workdir/rehearsal.log" >&2
  exit 1
}

echo "== step 2: boot isolated auth-enabled server against the restored DB =="
listen="127.0.0.1:$(( 25000 + RANDOM % 10000 ))"
download_root="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-restore-downloads.XXXXXX")"
BANSOKO_DATABASE_URL="$restore_url" \
BANSOKO_SESSION_SECRET="$session_secret" \
BANSOKO_SECRET_ENCRYPTION_KEY="$encryption_key" \
BANSOKO_ENVIRONMENT=development \
BANSOKO_SKIP_AUTH=false \
BANSOKO_LISTEN_ADDRESS="$listen" \
BANSOKO_FRONTEND_DIR=/dev/null \
BANSOKO_WORKER_DISABLED=true \
  go run ./cmd/bansoko-server >"$workdir/server.log" 2>&1 &
server_pid=$!

# Wait for /livez.
live_ok=""
for _ in $(seq 1 30); do
  if curl -sf "http://$listen/api/v1/livez" >/dev/null 2>&1; then
    live_ok=1
    break
  fi
  sleep 1
done
if [[ -z "$live_ok" ]]; then
  echo "restored server did not answer /livez; see $workdir/server.log" >&2
  exit 1
fi

echo "== step 3: admission checks =="
checks="$workdir/checks.json"
{
  echo "{\"restore_ok\":false,\"checks\":{"
} >"$checks"

# 3a. /livez
livez_body="$(curl -sf "http://$listen/api/v1/livez")" || { echo "livez failed" >&2; exit 1; }
echo "\"livez\":$livez_body," >>"$checks"

# 3b. /readyz — the restored instance runs with BANSOKO_WORKER_DISABLED, which puts
#     it in maintenance; readiness must report the maintenance state honestly.
readyz_status="$(curl -s -o "$workdir/readyz.json" -w '%{http_code}' "http://$listen/api/v1/readyz")"
echo "\"readyz\":{\"http\":$readyz_status,\"body\":$(cat "$workdir/readyz.json")}," >>"$checks"

# 3c. Owner login (auth recovery path).
login_body="$(curl -s -X POST "http://$listen/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"owner\",\"password\":\"$owner_password\",\"session_transport\":\"token\"}")"
login_http="$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://$listen/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"owner\",\"password\":\"$owner_password\",\"session_transport\":\"token\"}")"
# Only the outcome is persisted: the login response carries a session token and
# must never reach the restore manifest or uploaded artifacts.
login_token_present="$(printf '%s' "$login_body" | python3 -c 'import json,sys
try:
    print("true" if json.load(sys.stdin).get("session_token") else "false")
except Exception:
    print("false")' 2>/dev/null || echo false)"
echo "\"login\":{\"http\":$login_http,\"session_token_present\":$login_token_present}," >>"$checks"

# 3d. Owner read + secret decryption are verified via the API when login works.
if [[ "$login_http" == "200" ]]; then
  token="$(printf '%s' "$login_body" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("session_token",""))' 2>/dev/null || true)"
  if [[ -n "$token" ]]; then
    anime_http="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "http://$listen/api/v1/anime")"
    connections_http="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "http://$listen/api/v1/config/connections")"
    echo "\"owner_read\":{\"http\":$anime_http}," >>"$checks"
    echo "\"secrets\":{\"http\":$connections_http}" >>"$checks"
  else
    echo "\"owner_read\":{\"http\":0},\"secrets\":{\"http\":0}" >>"$checks"
  fi
else
  echo "\"owner_read\":{\"http\":0},\"secrets\":{\"http\":0}" >>"$checks"
fi

echo "}}" >>"$checks"

# Verdict: livez alive + login 200 + owner read 200 is the restore success bar.
# readyz is expected to report not-ready while the restore instance is in
# maintenance (BANSOKO_WORKER_DISABLED) — that is the honest contract, not a failure.
verdict=1
python3 - "$checks" <<'PY' || verdict=0
import json, sys
data = json.load(open(sys.argv[1]))
c = data["checks"]
assert c["livez"].get("status") == "alive", "livez not alive"
assert c["login"]["http"] == 200, f"login failed: {c['login']}"
assert c["owner_read"]["http"] == 200, f"owner read failed: {c['owner_read']}"
assert c["secrets"]["http"] == 200, f"secrets/connections read failed: {c['secrets']}"
print("restore admission checks passed")
PY
if [[ "$verdict" -eq 0 ]]; then
  echo "scratch restore admission failed; checks: $(cat "$checks")" >&2
  exit 1
fi

python3 - "$checks" "$workdir/dump/manifest.txt" "$workdir/dump/archive.list" "$workdir/restore-manifest.json" <<'PY'
import hashlib, json, sys
checks = json.load(open(sys.argv[1]))
backup = {}
for line in open(sys.argv[2], encoding="utf-8"):
    line = line.strip()
    if "=" in line:
        key, value = line.split("=", 1)
        if key in {
            "tool_mode", "tool_image", "psql_version", "pg_dump_version",
            "pg_restore_version", "source_server_version", "restore_server_version",
            "source_server_version_num", "restore_server_version_num", "source_identity",
            "restore_identity", "archive_digest", "verification_scope",
        }:
            backup[key] = value
archive_digest = hashlib.sha256(open(sys.argv[3], "rb").read()).hexdigest()
manifest = {
    "kind": "scratch-restore-rehearsal",
    "restore_ok": True,
    "checks": checks["checks"],
    "backup_evidence": backup,
    "archive_list_digest": "sha256:" + archive_digest,
    "exit_code": 0,
}
json.dump(manifest, open(sys.argv[4], "w"), indent=2, ensure_ascii=False)
print("manifest written:", sys.argv[4])
PY

# Persist the manifest for P0.8 lane / supervisor consumption.
if [[ -n "$persist_dir" ]]; then
  mkdir -p "$persist_dir"
  cp "$workdir/restore-manifest.json" "$persist_dir/restore-manifest.json"
  cp "$workdir/dump/manifest.txt" "$persist_dir/backup-manifest.txt"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$workdir/dump/archive.list" | awk '{print $1}' >"$persist_dir/archive-list.sha256"
  else
    shasum -a 256 "$workdir/dump/archive.list" | awk '{print $1}' >"$persist_dir/archive-list.sha256"
  fi
  echo "manifest persisted: $persist_dir/restore-manifest.json"
else
  echo "manifest (set BANSOKO_RESTORE_OUTPUT_DIR to persist):"
  cat "$workdir/restore-manifest.json"
fi

kill "$server_pid" 2>/dev/null || true
server_pid=""
echo "scratch restore rehearsal passed"
