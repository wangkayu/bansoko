#!/usr/bin/env bash
# Real-effect base harness self-test: proves the harness can safely bring up a
# disposable PostgreSQL + controlled qB + temp download/library + auth-enabled
# scratch server env, verify the environment, and tear everything down without
# leftovers. It does NOT run product owner scenarios (those belong to P1.4).
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
harness="$root/scripts/ci/real-effect-harness.sh"
state_dir="$(mktemp -d /tmp/bansoko-harness-self-test.XXXXXX)"
export BANSOKO_HARNESS_STATE_DIR="$state_dir"

cleanup() {
  "$harness" down >/dev/null 2>&1 || true
  rm -rf "$state_dir"
}
trap cleanup EXIT

echo "== harness check (safe defaults) =="
"$harness" check

echo "== harness up =="
"$harness" up

env_file="$state_dir/env"
[[ -f "$env_file" ]] || { echo "harness env file missing" >&2; exit 1; }
set -a
# shellcheck disable=SC1090
source "$env_file"
set +a

echo "== assertions =="
# Auth must be enabled by construction.
[[ "${BANSOKO_SKIP_AUTH:-}" == "0" ]] || { echo "BANSOKO_SKIP_AUTH must be 0" >&2; exit 1; }
# DB must be disposable and reachable (use the guard's own Go check, which
# validates both safety rules and connectivity; no psql client required).
[[ -n "${BANSOKO_TEST_DATABASE_URL:-}" ]] || { echo "BANSOKO_TEST_DATABASE_URL missing" >&2; exit 1; }
(cd "$root" && BANSOKO_TEST_DATABASE_URL="$BANSOKO_TEST_DATABASE_URL" BANSOKO_TEST_DB_DISPOSABLE=1 go run ./cmd/disposable-db-check check) \
  || { echo "postgres not reachable via guard check" >&2; exit 1; }
# qB endpoint must be set and the seeded password must actually log in.
# The container takes a few seconds to start the WebUI; retry briefly.
[[ -n "${BANSOKO_QB_ENDPOINT:-}" && -n "${BANSOKO_QB_PASSWORD:-}" ]] || { echo "qB env missing" >&2; exit 1; }
qb_login=""
for _ in $(seq 1 12); do
  qb_login="$(curl -s -m 3 -c /tmp/harness-qb-cookies -X POST "$BANSOKO_QB_ENDPOINT/api/v2/auth/login" \
    --data-urlencode "username=admin" --data-urlencode "password=$BANSOKO_QB_PASSWORD" || true)"
  [[ "$qb_login" == "Ok." ]] && break
  sleep 5
done
[[ "$qb_login" == "Ok." ]] || { echo "qB seeded password login failed: $qb_login" >&2; exit 1; }
qb_version="$(curl -s -b /tmp/harness-qb-cookies "$BANSOKO_QB_ENDPOINT/api/v2/app/version" || true)"
[[ -n "$qb_version" ]] || { echo "qB version check failed" >&2; exit 1; }
echo "  qB login ok, version: $qb_version"
rm -f /tmp/harness-qb-cookies
# Library root must exist and be writable.
[[ -d "${BANSOKO_HARNESS_LIBRARY_ROOT:-}" ]] || { echo "library root missing" >&2; exit 1; }
touch "$BANSOKO_HARNESS_LIBRARY_ROOT/.harness-write-probe" && rm -f "$BANSOKO_HARNESS_LIBRARY_ROOT/.harness-write-probe"

echo "== harness down =="
"$harness" down

echo "== no leftover harness containers =="
leftovers="$(docker ps --format '{{.Names}}' | grep -cE 'harness|recursing' || true)"
[[ "$leftovers" == "0" ]] || { echo "leftover harness containers: $leftovers" >&2; exit 1; }

echo "Real-effect base harness self-test passed"
