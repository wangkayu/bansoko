#!/usr/bin/env bash
# Contract and boundary gate runner; checks do not require a Docker daemon.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

# ci-tooling exports the Node 22/pnpm toolchain; source it here so those
# exports remain in this bash process for every command below.
source scripts/ci/ci-tooling.sh

pnpm --dir frontend install --frozen-lockfile --prefer-offline
go vet ./...
"$root/scripts/ci/check-contract.sh"
"$root/scripts/ci/check-observability-catalog.sh"
"$root/scripts/ci/check-fact-copy.sh"
"$root/scripts/generators/generate-dto-golden-vectors.py" --check
CENSUS_COVERAGE_MODE=unit "$root/scripts/ci/census.sh"
BANSOKO_CONTRACT_BOUNDARY_SKIP_COMPOSE=1 "$root/scripts/ci/check-boundary.sh"
"$root/scripts/ci/check-ci-lanes.sh"
bash "$root/scripts/ci/check-ci-shards-test.sh"
"$root/scripts/ci/check-production-compose.sh"
bash "$root/scripts/ci/check-production-compose-database-test.sh"
bash "$root/scripts/ci/check-production-compose-fixture-test.sh"
