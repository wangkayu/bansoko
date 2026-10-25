#!/usr/bin/env bash
set -euo pipefail

: "${BANSOKO_TEST_DATABASE_URL:?set BANSOKO_TEST_DATABASE_URL to an isolated PostgreSQL database}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
export GOCACHE="${GOCACHE:-/tmp/bansoko-gocache}"

# Every test listed here must actually run: a silent t.Skip would shrink the
# acceptance set without turning the gate red. See go-test-no-skips.sh.
run_gate="$root/scripts/ci/go-test-no-skips.sh"

# 1. Fail-closed disposable database guard: refuse empty/unreachable/
# non-disposable/production-looking targets before any mutation.
"$root/scripts/ci/require-disposable-db.sh"

# 1b. Segregated runtime role. Application tests run under the real runtime
# privilege boundary: a random internal password is generated in-process and
# never printed, the owner DSN stays BANSOKO_TEST_DATABASE_URL, and
# BANSOKO_TEST_RUNTIME_DATABASE_URL is the runtime handle ResetDisposableSchema
# returns. Migration/reset keeps using the owner DSN.
if [[ -z "${BANSOKO_TEST_RUNTIME_DATABASE_URL:-}" ]]; then
  runtime_user="bansoko_test_runtime"
  runtime_pass="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  BANSOKO_TEST_DB_RUNTIME_USER="$runtime_user" \
  BANSOKO_TEST_DB_RUNTIME_PASSWORD="$runtime_pass" \
    go run "$root/cmd/disposable-db-check" provision-runtime
  dsn_prefix="${BANSOKO_TEST_DATABASE_URL%%://*}"
  dsn_rest="${BANSOKO_TEST_DATABASE_URL#*://}"
  dsn_tail="${dsn_rest##*@}"
  export BANSOKO_TEST_RUNTIME_DATABASE_URL="${dsn_prefix}://${runtime_user}:${runtime_pass}@${dsn_tail}"
  unset runtime_user runtime_pass
fi

# 2. Active PostgreSQL package inventory gate: the canonical acceptance set is
# the committed postgres-packages.txt. The inventory scan (cmd/disposable-db-check)
# discovers every test package that reads BANSOKO_TEST_DATABASE_URL or executes
# DROP SCHEMA; if a new active PG package is added without updating the list,
# this diff fails and reports the paths. This is the single source of truth for
# "which packages are the PG gate".
go run ./cmd/disposable-db-check inventory --expect-file "$root/scripts/ci/postgres-packages.txt"

# 3. Package selection. runtime/migrations is intentionally separate and last:
# its tests replay the legacy and forward-migration scenarios that deliberately
# leave the shared disposable schema in a non-baseline intermediate state
# (pre-existing migration history, tampered identity, forward-017/018 fixtures);
# running it before another PG package would make that package's first DROP
# SCHEMA race the leftover state and cascade unrelated failures. Keeping it
# last lets every earlier package observe a pristine baseline, then restores it
# at the end via its own openDisposableDB resets. integration runs only the
# group-discovery flow (its m1 real-effect scenario is an explicit opt-in lane).
postgres_packages=(
  $(grep -v -e '^./internal/runtime/migrations$' -e '^./internal/integration$' "$root/scripts/ci/postgres-packages.txt")
)

# Long race/churn soak matrices (the 100-round 150 retirement competition, the
# continuous publication churn) live behind the `soak` build tag and run only
# through scripts/ci/test-soak.sh; they are not part of this gate. Without them
# the migrations lane measured 464s (B1), inside the 900s budget.

has_package() {
  local needle="$1" package
  for package in "${postgres_packages[@]}"; do
    [[ "$package" == "$needle" ]] && return 0
  done
  return 1
}

# 4. Optional container-lane shard. The CI workflow runs this same script in
# three parallel jobs, each with its own PostgreSQL service, so the shard must
# select a subset rather than run everything: BANSOKO_TEST_PG_SHARD=<n>/<total>
# names the job. The assignment lives in scripts/ci/postgres-shards.txt and
# scripts/ci/check-ci-shards.sh proves it covers exactly postgres-packages.txt;
# without the variable (host-runner and local runs) the full set runs.
shard_migrations=1
shard_integration=1
if [[ -n "${BANSOKO_TEST_PG_SHARD:-}" ]]; then
  # The selector, the table and the workflow are cross-checked here and by
  # scripts/ci/check-ci-shards.sh; this script only consumes the selection.
  shard_packages=()
  while IFS= read -r assigned_package; do
    [[ -n "$assigned_package" ]] && shard_packages+=("$assigned_package")
  done < <("$root/scripts/ci/pg-shard-packages.sh" "$BANSOKO_TEST_PG_SHARD")
  postgres_packages=("${shard_packages[@]}")
  has_package ./internal/runtime/migrations || shard_migrations=0
  has_package ./internal/integration || shard_integration=0
  echo "shard ${BANSOKO_TEST_PG_SHARD}: ${#shard_packages[@]} package(s)"
fi

if [[ "$shard_migrations" == 1 ]]; then
  "$run_gate" pg-migrations -p 1 ./internal/runtime/migrations -count=1 -timeout 900s
fi

# fileoperation/application alone measured 830-900s on a shared VM (B12), so
# the package lane gets a 1500s per-binary budget.
package_set=()
for package in "${postgres_packages[@]}"; do
  case "$package" in
    ./internal/runtime/migrations|./internal/integration) ;;
    *) package_set+=("$package") ;;
  esac
done
if ((${#package_set[@]} > 0)); then
  # The live Mikan search readers are provider opt-in tests, declared like the
  # unit lane's AniBT opt-in: their skip is an explicit public-network choice,
  # never a quietly shrunk gate.
  BANSOKO_TEST_SKIP_ALLOW="TestMikanRealProgramSearch|TestMikanRealSeedSearch" \
    "$run_gate" pg-packages -p 1 "${package_set[@]}" -count=1 -timeout 1500s
fi

if [[ "$shard_integration" == 1 ]]; then
  "$run_gate" pg-integration -p 1 ./internal/integration \
    -run 'Test(GroupDiscoveryCreatesRSSSources)$' \
    -count=1 -timeout 900s
fi

echo "PostgreSQL integration gate passed with no skipped tests"
