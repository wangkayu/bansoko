#!/usr/bin/env bash
# Release / Restore / Rollback required lane (P0.8).
#
# Consumes the SAME real signed digest bundle produced by the P0.7 builder and
# the P0.6 readiness/restore contract. Five serial phases, any failure exits 1:
#   1. Release integrity: check-release.sh (+ reproducible-build.sh unless
#      BANSOKO_RELEASE_SKIP_BUILD=1).
#   2. Maintenance writes-rejection evidence: P0.6 Web/MCP gate tests + real
#      server read-only smoke.
#   3. Scratch restore: scratch-restore-rehearsal.sh against the bundle inputs.
#   4. Second disposable-target catalog restore: the same restore rehearsal
#      against a different disposable target. It does NOT boot an old bundle and
#      does NOT prove online rollback.
#   5. Machine-readable summary.
#
# verification_scope of this lane is signed-bundle integrity plus catalog
# restore only. Full online upgrade and automation resume remain unavailable
# until deployment recovery exists.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
run_id="release-restore-$(date +%Y%m%d-%H%M%S)"
summary="$root/.local-artifacts/$run_id"
mkdir -p "$summary"
# Signing material never lives under the uploaded artifact root; this directory
# is removed on every exit path.
signing_dir="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-release-restore-signing.XXXXXX")"
cleanup_signing() { rm -rf "$signing_dir"; }
trap cleanup_signing EXIT

log() { echo "[release-restore] $*"; }
fail() { echo "{\"run_id\":\"$run_id\",\"ok\":false,\"phase\":\"${1:-unknown}\",\"reason\":\"$2\",\"rollout_capability\":\"unavailable\"}" >"$summary/summary.json"; echo "release-restore lane FAILED at ${1:-unknown}: $2" >&2; exit 1; }

# --- Inputs ---------------------------------------------------------------
manifest="${BANSOKO_RELEASE_MANIFEST:-}"
public_key="${BANSOKO_RELEASE_PUBLIC_KEY:-}"
artifact="${BANSOKO_RELEASE_ARTIFACT:-}"
source_url="${BANSOKO_BACKUP_SOURCE_URL:-}"
restore_url="${BANSOKO_RESTORE_TARGET_URL:-}"
owner_password="${BANSOKO_RESTORE_OWNER_PASSWORD:-}"
session_secret="${BANSOKO_SESSION_SECRET:-}"
encryption_key="${BANSOKO_SECRET_ENCRYPTION_KEY:-}"

if [[ -z "$public_key" || -z "$source_url" || -z "$restore_url" || -z "$owner_password" || -z "$session_secret" ]]; then
  echo "required: BANSOKO_RELEASE_PUBLIC_KEY, BANSOKO_BACKUP_SOURCE_URL, BANSOKO_RESTORE_TARGET_URL, BANSOKO_RESTORE_OWNER_PASSWORD, BANSOKO_SESSION_SECRET" >&2
  exit 2
fi

# --- Pre-write guards -----------------------------------------------------
# Phases 2.5 and 3 migrate and bootstrap the source database, so the source
# must be explicitly disposable and must not look like a real deployment. Both
# checks run before the first write.
if [[ "${BANSOKO_BACKUP_SOURCE_DISPOSABLE:-${BANSOKO_TEST_DB_DISPOSABLE:-0}}" != "1" ]]; then
  echo "required: BANSOKO_BACKUP_SOURCE_DISPOSABLE=1 (BANSOKO_TEST_DB_DISPOSABLE=1 is also accepted); the lane writes to the source database" >&2
  exit 2
fi
if [[ "${BANSOKO_RELEASE_ALLOW_REMOTE_SOURCE:-0}" != "1" ]]; then
  source_host="${source_url#*://}"
  source_host="${source_host%%/*}"
  source_host="${source_host##*@}"
  source_host="${source_host%%:*}"
  source_host="${source_host#[}"
  source_host="${source_host%]}"
  case "$source_host" in
    localhost|127.0.0.1|::1|"") ;;
    *)
      echo "refusing non-loopback source host ${source_host}; set BANSOKO_RELEASE_ALLOW_REMOTE_SOURCE=1 only for a disposable CI service" >&2
      exit 2
      ;;
  esac
fi
# CI consumes a bundle built in-lane: when no external manifest is provided,
# build one with the lane signing key (test-only in CI) and consume it.
if [[ -z "$manifest" ]]; then
  log "no external manifest; building lane bundle"
  if [[ -z "${BANSOKO_RELEASE_LANE_PRIVATE_KEY:-}" ]]; then
    fail "input" "BANSOKO_RELEASE_LANE_PRIVATE_KEY is required when BANSOKO_RELEASE_MANIFEST is not provided"
  fi
  printf '%s' "$BANSOKO_RELEASE_LANE_PRIVATE_KEY" >"$signing_dir/lane-private.key"
  chmod 600 "$signing_dir/lane-private.key"
  # build-release/check-release expect file paths, not hex values.
  printf '%s' "$public_key" >"$signing_dir/lane-public.key"
  chmod 644 "$signing_dir/lane-public.key"
  public_key="$signing_dir/lane-public.key"
  # Build straight into the summary dir (reproducible-build.sh forces its own
  # tmp output dirs, which the lane cannot consume).
  BANSOKO_RELEASE_VERSION="lane-$(date +%s)" BANSOKO_RELEASE_ID="$run_id" BANSOKO_RELEASE_SEQUENCE="1" \
    BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION="embedded-bootstrap-v1" \
    BANSOKO_RELEASE_PRIVATE_KEY="$signing_dir/lane-private.key" BANSOKO_RELEASE_PUBLIC_KEY="$public_key" \
    BANSOKO_ALLOW_DIRTY_RELEASE=1 BANSOKO_RELEASE_SKIP_IMAGES=1 \
    BANSOKO_RELEASE_OUTPUT_DIR="$summary/bundle" \
    "$root/scripts/ci/build-release.sh" >"$summary/release-build.log" 2>&1 \
    || fail "release-integrity" "lane bundle build failed (see release-build.log)"
  manifest="$summary/bundle/release-manifest.json"
  artifact="$summary/bundle/bansoko-server-linux-amd64"
  export BANSOKO_RELEASE_SKIP_BUILD=1
fi
if [[ "$source_url" == "$restore_url" ]]; then
  fail "input" "source and restore target must be different databases"
fi

# --- Phase 1: Release integrity -------------------------------------------
log "phase 1: release integrity"
if [[ "${BANSOKO_RELEASE_SKIP_BUILD:-0}" != "1" ]]; then
  BANSOKO_RELEASE_VERSION="lane-$(date +%s)" BANSOKO_RELEASE_ID="$run_id" BANSOKO_RELEASE_SEQUENCE="1" \
    BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION="embedded-bootstrap-v1" \
    BANSOKO_RELEASE_PRIVATE_KEY="${BANSOKO_RELEASE_PRIVATE_KEY:-}" BANSOKO_RELEASE_PUBLIC_KEY="$public_key" \
    BANSOKO_ALLOW_DIRTY_RELEASE=1 BANSOKO_RELEASE_SKIP_IMAGES=1 \
    "$root/scripts/maintainer/reproducible-build.sh" >"$summary/reproducible.log" 2>&1 \
    || fail "release-integrity" "reproducible double-build check failed (see reproducible.log)"
fi
# The lane proves minimal rollback evidence with a binary-only bundle (no
# docker in CI): declare the empty-images rehearsal form explicitly.
BANSOKO_RELEASE_MANIFEST="$manifest" BANSOKO_RELEASE_PUBLIC_KEY="$public_key" BANSOKO_RELEASE_ARTIFACT="$artifact" \
BANSOKO_REHEARSAL=1 BANSOKO_RELEASE_ALLOW_EMPTY_IMAGES=1 \
  "$root/scripts/ci/check-release.sh" >"$summary/check-release.log" 2>&1 \
  || fail "release-integrity" "check-release validator failed (see check-release.log)"

# --- Phase 2: Maintenance writes-rejection evidence -----------------------
log "phase 2: maintenance writes-rejection evidence (P0.6 gate tests)"
go test -count=1 ./internal/transport/httpapi -run 'TestMaintenance' >"$summary/maintenance-http.log" 2>&1 \
  || fail "maintenance" "Web maintenance gate tests failed"
go test -count=1 ./internal/transport/mcp -run 'TestMaintenanceRejectsWriteTools' >"$summary/maintenance-mcp.log" 2>&1 \
  || fail "maintenance" "MCP maintenance gate tests failed"
go test -count=1 ./internal/runtime/maintenance >"$summary/maintenance-gate.log" 2>&1 \
  || fail "maintenance" "maintenance gate unit tests failed"

# --- Phase 2.5: bootstrap the owner on the source database -----------------
# The scratch restore verifies owner login; a fresh CI database has no owner
# and no schema. Migrate first, then create the owner via a temporary server.
log "phase 2.5: migrate source database + bootstrap owner"
(cd "$root" && BANSOKO_DATABASE_URL="$source_url" BANSOKO_SCHEMA_DIR="$root/db/migrations" \
  go run ./cmd/bansoko-migrate >"$summary/migrate.log" 2>&1) \
  || fail "bootstrap" "source database migration failed (see migrate.log)"
listen="127.0.0.1:$(( 26000 + RANDOM % 5000 ))"
(
  cd "$root"
  BANSOKO_DATABASE_URL="$source_url" \
  BANSOKO_SESSION_SECRET="$session_secret" \
  BANSOKO_SECRET_ENCRYPTION_KEY="${encryption_key:-}" \
  BANSOKO_ENVIRONMENT=development \
  BANSOKO_SKIP_AUTH=false \
  BANSOKO_LISTEN_ADDRESS="$listen" \
  BANSOKO_FRONTEND_DIR=/dev/null \
  BANSOKO_WORKER_DISABLED=true \
    go run ./cmd/bansoko-server >"$summary/bootstrap-server.log" 2>&1
) &
bootstrap_pid=$!
boot_ok=""
for _ in $(seq 1 45); do
  if curl -sf "http://$listen/api/v1/livez" >/dev/null 2>&1; then boot_ok=1; break; fi
  sleep 1
done
if [[ -z "$boot_ok" ]]; then
  kill "$bootstrap_pid" 2>/dev/null || true
  fail "bootstrap" "bootstrap server did not start (see bootstrap-server.log)"
fi
curl -s -X POST "http://$listen/api/v1/bootstrap/owner" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"owner\",\"password\":\"$owner_password\"}" >/dev/null 2>&1 \
  || true
kill "$bootstrap_pid" 2>/dev/null || true
wait "$bootstrap_pid" 2>/dev/null || true

# --- Phase 3: Scratch restore ---------------------------------------------
log "phase 3: scratch restore from bundle inputs"
BANSOKO_BACKUP_SOURCE_URL="$source_url" \
BANSOKO_RESTORE_TARGET_URL="$restore_url" \
BANSOKO_RESTORE_TARGET_DISPOSABLE=1 \
BANSOKO_SESSION_SECRET="$session_secret" \
BANSOKO_SECRET_ENCRYPTION_KEY="${encryption_key:-}" \
BANSOKO_RESTORE_OWNER_PASSWORD="$owner_password" \
BANSOKO_RESTORE_OUTPUT_DIR="$summary/restore" \
  "$root/scripts/verify/scratch-restore-rehearsal.sh" >"$summary/restore.log" 2>&1 \
  || fail "scratch-restore" "scratch restore rehearsal failed (see restore.log)"

# --- Phase 4: Second disposable-target restore ----------------------------
# This repeats the phase-3 catalog restore against a different disposable
# target. It does NOT boot an old signed bundle and does not prove online
# rollback or restored automation state; that capability stays unavailable
# until deployment recovery exists.
log "phase 4: second disposable-target catalog restore"
rollback_url="${BANSOKO_ROLLBACK_TARGET_URL:-}"
if [[ -z "$rollback_url" ]]; then
  # Derive a second disposable target by suffixing the database name.
  base="${restore_url%%\?*}"
  query=""
  if [[ "$restore_url" == *\?* ]]; then query="?${restore_url#*\?}"; fi
  rollback_url="${base}_rollback${query}"
fi
BANSOKO_BACKUP_SOURCE_URL="$source_url" \
BANSOKO_RESTORE_TARGET_URL="$rollback_url" \
BANSOKO_RESTORE_TARGET_DISPOSABLE=1 \
BANSOKO_SESSION_SECRET="$session_secret" \
BANSOKO_SECRET_ENCRYPTION_KEY="${encryption_key:-}" \
BANSOKO_RESTORE_OWNER_PASSWORD="$owner_password" \
BANSOKO_RESTORE_OUTPUT_DIR="$summary/rollback" \
  "$root/scripts/verify/scratch-restore-rehearsal.sh" >"$summary/rollback.log" 2>&1 \
  || fail "second-restore" "second disposable restore rehearsal failed (see rollback.log)"

# Whether the maintenance/resume gate appears in the restore evidence is
# reported, never silently assumed: a missing gate is an unverified claim, not
# a pass.
maintenance_evidence="absent"
if [[ -f "$summary/rollback/checks.json" ]] && grep -q '"maintenance"' "$summary/rollback/checks.json" 2>/dev/null; then
  maintenance_evidence="present"
fi
if [[ "$maintenance_evidence" == "absent" && -f "$summary/rollback/restore-manifest.json" ]] \
  && grep -q '"automation_resume_gate"' "$summary/rollback/restore-manifest.json" 2>/dev/null; then
  maintenance_evidence="present"
fi
if [[ "$maintenance_evidence" == "present" ]]; then
  log "maintenance/resume-gate evidence present in the second restore"
else
  log "maintenance/resume-gate evidence absent; recorded as unverified"
fi

# An empty encryption key means no ciphertext was exercised; the summary must
# not imply secret decryption passed.
secret_decryption="unverified"
if [[ -n "$encryption_key" ]]; then
  secret_decryption="key_supplied"
fi

# --- Phase 5: Summary -----------------------------------------------------
python3 - "$summary" "$run_id" "$manifest" "$maintenance_evidence" "$secret_decryption" <<'PY' || fail "summary" "summary generation failed"
import json, sys
out, run_id, manifest, maintenance_evidence, secret_decryption = sys.argv[1:]
summary = {
    "kind": "release-restore-lane",
    "run_id": run_id,
    "bundle": manifest,
    "phases": {
        "release_integrity": "passed",
        "maintenance_writes_rejected": "passed",
        "scratch_restore": "passed",
        "second_disposable_restore": "passed",
    },
    "verification_scope": "signed_bundle_integrity_and_catalog_restore",
    "maintenance_resume_gate_evidence": maintenance_evidence,
    "secret_decryption": secret_decryption,
    "test_skips": 0,
    "rollout_capability": "unavailable",
    "note": "Two independent disposable catalog restores; no old-bundle boot and no online rollback. Full online upgrade and automation resume remain unavailable until deployment recovery exists.",
    "artifacts": {
        "check_release": out + "/check-release.log",
        "restore": out + "/restore/restore-manifest.json",
        "second_restore": out + "/rollback/restore-manifest.json",
    },
}
json.dump(summary, open(out + "/summary.json", "w"), indent=2, ensure_ascii=False)
print("summary written:", out + "/summary.json")
PY

log "release-restore lane PASSED (rollout_capability=unavailable)"
