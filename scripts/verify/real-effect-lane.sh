#!/usr/bin/env bash
# Authenticated real-effect lane — single authoritative entry that proves
# the real chain end to end with auth enabled:
#   1. harness up          scratch PG + migration + controlled qB + temp dirs
#   2. single-episode      RSS -> qB -> artifact -> file operation (service)
#   3. fault injection     qB ambiguous lookup-before-retry + filesystem
#                          permission/collision settlement on the harness PG
#   4. frontend build      current frontend/dist for the backend static handler
#   5. auth server         bootstrap owner + login + livez/readyz assertions
#   6. authenticated UI    Playwright bootstrap/login gate + login + catalog
#   7. evidence            redacted summary + logs under $state_dir/evidence/
#
# Usage:
#   BANSOKO_HARNESS_STATE_DIR=/tmp/bansoko-harness-lane scripts/verify/real-effect-lane.sh
#   (defaults to a fresh mktemp dir under /tmp/bansoko-harness-lane.*)
#
# On success the harness is torn down; on failure the harness is kept alive
# (with a redacted diagnostic summary) so the failure can be reproduced.
#
# Never touches production databases, production qB, user media directories,
# or long-lived credentials. Never runs with BANSOKO_SKIP_AUTH=1.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
export GOCACHE="${GOCACHE:-/tmp/bansoko-gocache}"

# Harness backend: VM Docker by default. The native harness is retained only for
# explicitly provisioned VM environments without Docker, never for developer hosts.
harness_script="${BANSOKO_HARNESS_SCRIPT:-real-effect-harness.sh}"

lane_id="real-effect-$(date +%Y%m%d-%H%M%S)"
state_dir="${BANSOKO_HARNESS_STATE_DIR:-$(mktemp -d /tmp/bansoko-harness-lane.XXXXXX)}"
mkdir -p "$state_dir/evidence"
log="$state_dir/evidence/lane.log"
owner_pass="lane-owner-$(openssl rand -hex 8)"
server_port="$(( 30000 + RANDOM % 20000 ))"
listen_host="${BANSOKO_LANE_LISTEN_HOST:-0.0.0.0}"
public_host="${BANSOKO_LANE_PUBLIC_HOST:-$(hostname -I 2>/dev/null | awk '{print $1}')}"
public_host="${public_host:-127.0.0.1}"

say()  { printf '%s\n' "$*" | tee -a "$log"; }
fail() { say "FAIL: $*"; exit 1; }

# --- phase bookkeeping -------------------------------------------------------
phase=""
run_phase() {
  phase="$1"
  say ""
  say "== [$phase] $(date -u +%H:%M:%S) =="
}

cleanup() {
  if [[ "${KEEP_ON_FAIL:-0}" == "1" ]]; then
    say "lane failed during [$phase]; harness kept at $state_dir (KEEP_ON_FAIL=1)"
    exit 1
  fi
  # Kill the scratch server if we started one. `go run` execs the built
  # binary as a child, so also sweep any orphaned bansoko-server binaries.
  if [[ -f "$state_dir/server.pid" ]]; then
    pid="$(cat "$state_dir/server.pid")"
    kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
    rm -f "$state_dir/server.pid"
  fi
  sleep 1
  # The server is started in its own process group; do not sweep unrelated
  # systemd or user-managed bansoko-server processes on the VM.
  "$root/scripts/$harness_script" down >>"$log" 2>&1 || true
  say "harness torn down; evidence at $state_dir/evidence/"
  exit 1
}
trap cleanup EXIT

harness_env="$state_dir/env"
if [[ -f "$harness_env" ]]; then
  say "lane: reusing harness state at $state_dir"
else
  run_phase "1.harness-up"
  export BANSOKO_HARNESS_STATE_DIR="$state_dir"
  "$root/scripts/$harness_script" up >>"$log" 2>&1 || fail "harness up"
fi
set -a; source "$harness_env"; set +a

# --- single-episode real chain -----------------------------------------------------------
# Runs BEFORE the auth server: both the single-episode chain and the fault scenarios reset the
# disposable harness schema (disposabledb.ResetDisposableSchema), which would
# wipe a previously created owner/bootstrap row. The auth server therefore
# boots last, after all schema-resetting test phases.
run_phase "2.single-episode-real-chain"
export BANSOKO_HARNESS_STATE_DIR="$state_dir"
"$root/scripts/verify/test-single-episode-real-chain.sh" >>"$state_dir/evidence/single-episode.log" 2>&1 \
  || fail "single-episode real chain"
say "single-episode real chain passed (RSS -> qB -> artifact -> file operation)"

# --- fault injection ---------------------------------------------------------
run_phase "3.fault-injection"
export BANSOKO_TEST_DATABASE_URL BANSOKO_TEST_DB_DISPOSABLE=1
"$root/scripts/ci/go-test-no-skips.sh" real-effect-faults -p 1 \
  ./internal/download/application/ \
  -run '^(TestQBSubmitLookupBeforeAddAndNoDuplicateAdd|TestQBSyncPathSnapshotMismatchFailsTerminallyAtTheRetryBudget)$' \
  -count=1 >>"$state_dir/evidence/faults-download.log" 2>&1 || fail "qB ambiguous fault scenarios"
"$root/scripts/ci/go-test-no-skips.sh" real-effect-faults -p 1 \
  ./internal/integration/ \
  -run '^(TestRealEffectPermissionDeniedSettlesTerminalWithOwnerReason|TestRealEffectWritableTargetCompletes)$' \
  -count=1 >>"$state_dir/evidence/faults-filesystem.log" 2>&1 || fail "filesystem fault scenarios"
"$root/scripts/ci/go-test-no-skips.sh" real-effect-faults -p 1 \
  ./internal/integration/ \
  -run '^(TestRecoveryRealEffectQBIsReadOnlyForCommittedSubmit)$' \
  -count=1 >>"$state_dir/evidence/faults-recovery.log" 2>&1 || fail "recovery real-effect scenarios"
say "fault injection passed (qB ambiguous + filesystem permission + recovery real-effect)"

# --- frontend build ----------------------------------------------------------
# Repository synchronization excludes ignored build output. Build the frontend
# on the authoritative VM before starting the server so browser failures test
# the current UI rather than an absent frontend/dist directory.
run_phase "4.frontend-build"
(
  cd "$root/frontend"
  if [[ ! -d node_modules ]]; then
    pnpm install --frozen-lockfile
  fi
  pnpm build
  [[ -f dist/index.html ]] || { echo "frontend build did not produce dist/index.html" >&2; exit 1; }
) >>"$state_dir/evidence/frontend-build.log" 2>&1 || fail "frontend build"
say "frontend build passed (frontend/dist/index.html present)"

# --- auth server -------------------------------------------------------------
run_phase "5.auth-server"
export BANSOKO_SKIP_AUTH=0 BANSOKO_LISTEN_ADDRESS="${listen_host}:$server_port"
setsid go run ./cmd/bansoko-server >"$state_dir/evidence/server.log" 2>&1 &
echo "$!" > "$state_dir/server.pid"
server_url="http://127.0.0.1:$server_port"
public_url="http://${public_host}:$server_port"
ready=0
for _ in $(seq 1 30); do
  if curl -sf -o /dev/null "$server_url/api/v1/livez"; then ready=1; break; fi
  sleep 1
done
[[ "$ready" == "1" ]] || fail "server did not come alive at $server_url"
say "server alive: $server_url"

status="$(curl -sf "$server_url/api/v1/bootstrap/status" || true)"
if [[ "$status" != *owner_exists* ]]; then
  code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$server_url/api/v1/bootstrap/owner" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"owner\",\"password\":\"$owner_pass\",\"session_transport\":\"token\"}")"
  [[ "$code" == "201" ]] || fail "bootstrap owner returned $code"
  say "bootstrap owner created (201)"
else
  say "bootstrap owner already exists; login only"
fi

token="$(curl -sf -X POST "$server_url/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"owner\",\"password\":\"$owner_pass\",\"session_transport\":\"token\"}" \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['session_token'])")" \
  || fail "owner login"
[[ "${#token}" -ge 32 ]] || fail "session token too short"
health_code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" \
  "$server_url/api/v1/system/health")"
[[ "$health_code" == "200" ]] || fail "authenticated system/health returned $health_code"
say "login + authenticated health OK"

# readyz: bootstrap/owner_read must pass. The dev `go run` process has no
# ldflags-injected build identity, so overall readiness stays 503 here — that
# is expected; the production bundle (P0.7 builder) is what flips it to 200.
# Plain `-s` (not `-f`): a 503 body must still be parsed for the per-check
# assertions below.
readyz="$(curl -s "$server_url/api/v1/readyz" || true)"
for check in bootstrap owner_read; do
  echo "$readyz" | grep -q "\"$check\".*\"ok\": *true" \
    || fail "readyz check $check not ok: $readyz"
done
say "readyz bootstrap+owner_read ok (total=503 only because dev build identity is unset)"

# --- authenticated browser ---------------------------------------------------
# Browser processes are optional here: the authoritative VM owns the database,
# qB and filesystem effects, while the preferred live browser run happens from
# the developer machine against public_url on the LAN. Set BANSOKO_LANE_RUN_BROWSER=1
# for a VM-local desktop smoke when needed.
run_phase "6.authenticated-browser"
if [[ "${BANSOKO_LANE_RUN_BROWSER:-0}" == "1" ]]; then
  (
    cd "$root/frontend"
    browser_output_dir="$state_dir/playwright-output"
    browser_auth_dir="$state_dir/playwright-auth"
    mkdir -p "$browser_output_dir" "$browser_auth_dir"
    BANSOKO_TEST_BASE_URL="$server_url" BANSOKO_E2E_USERNAME=owner BANSOKO_E2E_PASSWORD="$owner_pass" \
      BANSOKO_E2E_REAL_EFFECT=1 \
      BANSOKO_E2E_OUTPUT_DIR="$browser_output_dir" \
      BANSOKO_E2E_AUTH_STATE_PATH="$browser_auth_dir/state.json" \
      BANSOKO_E2E_AUTH_MARKER_PATH="$browser_auth_dir/skip-auth" \
      pnpm exec playwright test e2e/auth.spec.ts e2e/catalog.spec.ts e2e/same-origin-assets.spec.ts e2e/settings.spec.ts e2e/settings-live.spec.ts \
      --project=desktop --workers=1
  ) >>"$state_dir/evidence/browser.log" 2>&1 || fail "authenticated browser specs"
  grep -E 'passed|failed|skipped' "$state_dir/evidence/browser.log" | tail -3 \
    | tee -a "$log"
  say "authenticated browser specs passed on VM (BANSOKO_LANE_RUN_BROWSER=1)"
else
  say "authenticated browser deferred to LAN client; use $public_url with owner credentials"
  printf '%s\n' "deferred_to_lan_client: $public_url" >"$state_dir/evidence/browser.log"
fi

# --- evidence ----------------------------------------------------------------
run_phase "7.evidence"
{
  echo "lane_id: $lane_id"
  echo "state_dir: $state_dir"
  echo "server_url: $server_url"
  echo "public_url: $public_url"
  echo "listen_address: ${listen_host}:$server_port"
  echo "owner: owner"
  echo "bootstrap: owner_exists (or created during lane)"
  echo "phases: 1.harness-up 2.single-episode-real-chain 3.fault-injection 4.frontend-build 5.auth-server 6.authenticated-browser 7.evidence 8.teardown"
  echo "server_process: go run ./cmd/bansoko-server (dev, build_identity unset)"
  echo "browser_mode: ${BANSOKO_LANE_RUN_BROWSER:-0} (0=LAN client, 1=VM-local smoke)"
} > "$state_dir/evidence/summary.txt"
say "evidence written to $state_dir/evidence/ (summary.txt + lane.log + server.log + single-episode.log + fault logs + browser.log)"

# --- teardown ----------------------------------------------------------------
run_phase "8.teardown"
if [[ -f "$state_dir/server.pid" ]]; then
  pid="$(cat "$state_dir/server.pid")"
  kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  rm -f "$state_dir/server.pid"
fi
sleep 1
"$root/scripts/$harness_script" down >>"$log" 2>&1 || fail "harness down"
say "LANE PASSED ($lane_id); harness torn down; evidence kept at $state_dir/evidence/"
trap - EXIT
