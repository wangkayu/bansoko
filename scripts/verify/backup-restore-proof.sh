#!/usr/bin/env bash
# In-app backup export/restore proof on the real production compose (VM only).
#
# Runs ON THE VM inside a vm-session repo copy (never on the dev Mac, never
# against the production stack). It builds the image from this tree, starts a
# disposable copy of deployment/maintainer/docker-compose.production.yml under its own
# project name, volumes and ports, then:
#
#   1. creates the owner and an API token named "before-backup";
#   2. downloads a backup the browser way (POST /backup/download-ticket, then
#      GET /backup/download?ticket=, single use) and checks the file name and
#      the BANSOKO-BACKUP/1 signature; an API token exports with
#      GET /backup/export;
#   3. mutates data: creates an API token named "after-backup";
#   4. proves a tampered file, a file of another schema and any restore by an
#      API token (403 WEB_SESSION_REQUIRED) are refused with the data unchanged;
#   5. uploads the backup through POST /api/v1/backup/restore and waits for
#      the app to restart on the restored database (old session rejected);
#   6. verifies "before-backup" exists and "after-backup" does not, livez and
#      readyz answer 200, and no restore databases are left in the cluster;
#   7. restores the same file again with deployment/scripts/restore.sh (the
#      host-side fallback) and verifies the data again.
#
# Usage: scripts/verify/backup-restore-proof.sh [--keep]
#   --keep   leave the stack running for inspection (default: tear it down)
# Environment (optional):
#   BACKUP_PROOF_IMAGE   reuse an already built image tag instead of building
#   BACKUP_PROOF_RUN_ID  suffix for project/volume names (default: timestamp)
#   BACKUP_PROOF_DIR     evidence directory (default: .local-artifacts/backup-proof/<run>)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
keep=0
[[ "${1:-}" == "--keep" ]] && keep=1

run_id="${BACKUP_PROOF_RUN_ID:-$(date -u +%Y%m%d%H%M%S)}"
[[ "$run_id" =~ ^[a-z0-9-]{1,30}$ ]] || { echo "BACKUP_PROOF_RUN_ID must be [a-z0-9-]{1,30}" >&2; exit 2; }
project="backup-proof-$run_id"
updates_volume="backup-proof-updates-$run_id"
workdir="${BACKUP_PROOF_DIR:-$root/.local-artifacts/backup-proof/$run_id}"
compose="$workdir/compose.yml"
log="$workdir/proof.log"
mkdir -p "$workdir/config"

step() { printf '\n== %s\n' "$*" | tee -a "$log" >&2; }
fail() { echo "PROOF FAILED: $*" | tee -a "$log" >&2; exit 1; }
dc() { docker compose -p "$project" -f "$compose" "$@"; }

cleanup() {
  local status=$?
  dc ps -a >"$workdir/compose-ps.txt" 2>&1 || true
  dc logs --no-color --timestamps app >"$workdir/app.log" 2>&1 || true
  if [[ "$keep" -eq 0 ]]; then
    dc down -v --remove-orphans >/dev/null 2>&1 || true
    docker volume rm "$updates_volume" >/dev/null 2>&1 || true
    # The config bind mount was handed to the image user; hand it back so the
    # evidence directory (and its vm-session) can be removed without root.
    if [[ -n "${image:-}" ]]; then
      docker run --rm --user 0:0 --mount "type=bind,source=$workdir/config,target=/config" --entrypoint sh "$image" \
        -c "chown -R $(id -u):$(id -g) /config" >/dev/null 2>&1 || true
    fi
  else
    echo "kept: docker compose -p $project -f $compose ..." >&2
  fi
  if [[ "$status" -eq 0 ]]; then echo "PROOF PASSED (evidence: $workdir)" >&2; fi
}
trap cleanup EXIT

if ! command -v docker >/dev/null || ! docker compose version >/dev/null; then fail "docker compose is required"; fi
command -v curl >/dev/null || fail "curl is required"
command -v python3 >/dev/null || fail "python3 is required"

step "image"
image="${BACKUP_PROOF_IMAGE:-bansoko-backup-proof:$run_id}"
if [[ -z "${BACKUP_PROOF_IMAGE:-}" ]]; then
  # Readiness refuses an image without a build identity, so the proof image
  # carries one: the checkout HEAD, or the HEAD.txt vm-session writes after sync.
  source_commit="$(git -C "$root" rev-parse --short HEAD 2>/dev/null || cat "$root/HEAD.txt" 2>/dev/null || true)"
  [[ -n "$source_commit" ]] || fail "cannot determine the source commit (no git checkout and no HEAD.txt)"
  docker build --build-arg VERSION=backup-proof --build-arg SOURCE_COMMIT="$source_commit" \
    --build-arg BUILD_ID="backup-proof-$run_id" --build-arg BUILD_TIME=backup-proof --build-arg BUILD_DIRTY=false \
    -t "$image" "$root" >>"$log" 2>&1 || fail "image build failed (see $log)"
fi
# The app drops to PUID:PGID; use the invoking account so cleanup needs no root.
image_uid="$(id -u)"; image_gid="$(id -g)"

step "compose ($project)"
docker volume create "$updates_volume" >/dev/null
python3 "$root/scripts/ci/transform-embedded-compose.py" \
  "$root/deployment/maintainer/docker-compose.production.yml" "$compose" "$updates_volume" "$image"
docker run --rm --user 0:0 --mount "type=bind,source=$workdir/config,target=/config" --entrypoint sh "$image" \
  -c "chown -R $image_uid:$image_gid /config" || fail "config ownership preparation failed"

pick_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
image_id="$(docker image inspect --format '{{.Id}}' "$image")"
[[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || fail "image has no immutable identity"
export BANSOKO_BUNDLE_IMAGE_DIGEST="$image_id"
export BANSOKO_CONFIG_DIR="$workdir/config" PUID="$image_uid" PGID="$image_gid"
export BANSOKO_DB_NAME=bansoko_proof BANSOKO_DB_USER=bansoko BANSOKO_DB_PASSWORD="proof-owner-password-$run_id"
# Secrets and the runtime login are generated by the bootstrap into $workdir/config.
BANSOKO_APP_PORT="$(pick_port)"; BANSOKO_DB_PORT="$(pick_port)"
export BANSOKO_APP_PORT BANSOKO_DB_PORT
api="http://127.0.0.1:$BANSOKO_APP_PORT/api/v1"

dc up -d postgres app >>"$log" 2>&1 || fail "compose up failed (see $log)"

wait_ready() {
  local deadline=$((SECONDS + 300))
  until curl -fsS "$api/readyz" >/dev/null 2>&1; do
    (( SECONDS < deadline )) || fail "app did not become ready"
    sleep 3
  done
}
wait_ready

json() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {}, {'d': d}))" "$1"; }
login() {
  curl -fsS -X POST "$api/auth/login" -H 'Content-Type: application/json' \
    -d '{"username":"proof","password":"proof-password-123456","session_transport":"token"}' | json 'd["session_token"]'
}
tokens() { curl -fsS "$api/api-tokens" "${auth[@]}" | json '",".join(sorted(t["name"] for t in d))'; }
upload() {
  curl -sS -o "$workdir/upload-response.json" -w '%{http_code}' -X POST "$api/backup/restore" "${auth[@]}" \
    -H 'Content-Type: application/octet-stream' --data-binary "@$1"
}

step "owner and pre-backup data"
session="$(curl -fsS -X POST "$api/bootstrap/owner" -H 'Content-Type: application/json' \
  -d '{"username":"proof","password":"proof-password-123456","session_transport":"token"}' | json 'd["session_token"]')"
auth=(-H "Authorization: Bearer $session")
curl -fsS -X POST "$api/api-tokens" "${auth[@]}" -H 'Content-Type: application/json' -d '{"name":"before-backup"}' >/dev/null
# An API token holding every scope: it may export, never restore.
agent_token="$(curl -fsS -X POST "$api/api-tokens" "${auth[@]}" -H 'Content-Type: application/json' \
  -d '{"name":"agent","scopes":["read","write","secrets"]}' | json 'd["token"]')"
info="$(curl -fsS "$api/backup" "${auth[@]}")"
echo "$info" >"$workdir/backup-info.json"
[[ "$(json 'd["export_available"] and d["restore_available"]' <<<"$info")" == True ]] || fail "backup is not available: $info"

step "export (browser ticket, then token)"
backup="$workdir/export.bansoko-backup"
prepared="$(curl -fsS -X POST "$api/backup/download-ticket" "${auth[@]}")"
ticket="$(json 'd["ticket"]' <<<"$prepared")"
curl -fsS "$api/backup/download?ticket=$ticket" -D "$workdir/export-headers.txt" -o "$backup" || fail "ticket download failed"
[[ "$(curl -s -o /dev/null -w '%{http_code}' "$api/backup/download?ticket=$ticket")" == 403 ]] || fail "a download ticket worked twice"
curl -fsS "$api/backup/export" -H "Authorization: Bearer $agent_token" -o "$workdir/token-export.bansoko-backup" \
  || fail "a token holding the secrets scope could not export"
[[ "$(head -n 1 "$workdir/token-export.bansoko-backup")" == "BANSOKO-BACKUP/1" ]] || fail "token export lacks the backup signature"
file_name="$(tr -d '\r' <"$workdir/export-headers.txt" | sed -n 's/^[Cc]ontent-[Dd]isposition:.*filename="\{0,1\}\([^";]*\)"\{0,1\}.*/\1/p')"
[[ "$file_name" =~ ^bansoko-[0-9]{8}T[0-9]{6}Z-schema[0-9]{3,6}\.bansoko-backup$ ]] || fail "unexpected export file name: $file_name"
[[ "$(head -n 1 "$backup")" == "BANSOKO-BACKUP/1" ]] || fail "export lacks the backup signature"
echo "export: $file_name ($(wc -c <"$backup" | tr -d ' ') bytes)" | tee -a "$log"

step "mutate after the backup"
curl -fsS -X POST "$api/api-tokens" "${auth[@]}" -H 'Content-Type: application/json' -d '{"name":"after-backup"}' >/dev/null

step "refusals leave the data unchanged"
tampered="$workdir/tampered.bansoko-backup"
cp "$backup" "$tampered"
printf 'x' >>"$tampered"
[[ "$(upload "$tampered")" == 422 ]] || fail "a tampered file was not refused"
python3 - "$backup" "$workdir/other-schema.bansoko-backup" <<'PY'
import json, sys
data = open(sys.argv[1], "rb").read()
magic, header, payload = data.split(b"\n", 2)
h = json.loads(header)
h["schema_version"] = "001_baseline.sql"
open(sys.argv[2], "wb").write(magic + b"\n" + json.dumps(h, separators=(",", ":")).encode() + b"\n" + payload)
PY
[[ "$(upload "$workdir/other-schema.bansoko-backup")" == 422 ]] || fail "a backup of another schema was not refused"
grep -q BACKUP_SCHEMA_MISMATCH "$workdir/upload-response.json" || fail "schema refusal has the wrong code"
[[ "$(curl -sS -o "$workdir/upload-response.json" -w '%{http_code}' -X POST "$api/backup/restore" \
  -H "Authorization: Bearer $agent_token" -H 'Content-Type: application/octet-stream' --data-binary "@$backup")" == 403 ]] \
  || fail "an API token was allowed to restore"
grep -q WEB_SESSION_REQUIRED "$workdir/upload-response.json" || fail "token restore refusal has the wrong code"
[[ "$(tokens)" == "after-backup,agent,before-backup" ]] || fail "a refused restore changed the data"

verify_restored() {
  curl -fsS "$api/livez" >/dev/null || fail "livez"
  curl -fsS "$api/readyz" >/dev/null || fail "readyz"
  session="$(login)"
  auth=(-H "Authorization: Bearer $session")
  local names
  names="$(tokens)"
  echo "tokens after restore: $names" | tee -a "$log"
  [[ ",$names," == *",before-backup,"* ]] || fail "pre-backup data missing after restore"
  [[ ",$names," != *",after-backup,"* ]] || fail "post-backup data survived the restore"
  # shellcheck disable=SC2016 # expanded by the container shell
  if dc exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d postgres -Atc "SELECT datname FROM pg_database"' \
    | grep -E "^${BANSOKO_DB_NAME}_"; then
    fail "restore databases left in the cluster"
  fi
}

step "restore through the app"
[[ "$(upload "$backup")" == 202 ]] || fail "restore was not accepted: $(cat "$workdir/upload-response.json")"
deadline=$((SECONDS + 600))
while :; do
  code="$(curl -s -o /dev/null -w '%{http_code}' "$api/backup" "${auth[@]}" || true)"
  [[ "$code" == 401 ]] && break
  (( SECONDS < deadline )) || fail "the app did not come back on the restored database (last status $code)"
  sleep 2
done
wait_ready
verify_restored

step "restore with the host-side script"
curl -fsS -X POST "$api/api-tokens" "${auth[@]}" -H 'Content-Type: application/json' -d '{"name":"after-backup"}' >/dev/null
restore=("$root/deployment/scripts/restore.sh" -f "$compose" -p "$project")
set +e
"${restore[@]}" "$backup" >>"$log" 2>&1
refused=$?
set -e
[[ "$refused" -eq 3 ]] || fail "restore.sh did not refuse while the app ran (exit $refused)"
"${restore[@]}" --stop-app "$backup" 2>&1 | tee -a "$log" || fail "restore.sh failed"
verify_restored
echo "verified: export, refusals, in-app restore and host-side restore" | tee -a "$log"
