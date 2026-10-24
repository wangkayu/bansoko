#!/usr/bin/env bash
# Unit/Static lane (Go part): build, vet, and database-free unit tests.
# Must not depend on PostgreSQL, public providers, qB or filesystem effects.
#
# The PG package set is authoritative (scripts/ci/postgres-packages.txt, generated
# by cmd/disposable-db-check inventory). This lane runs the complement with the
# no-skip wrapper, so a silent t.Skip here turns the gate red instead of
# quietly shrinking acceptance. Frontend gates run in ci-frontend-quality.sh.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
export GOCACHE="${GOCACHE:-/tmp/bansoko-gocache}"
run_gate="$root/scripts/ci/go-test-no-skips.sh"

echo "== release identity contract =="
"$root/scripts/ci/ci-release-identity-test.sh"
python3 scripts/ci/ci-test-release-test.py
python3 scripts/ci/ci-release-trust-test.py
python3 scripts/ci/check-embedded-fixture-cleanup-test.py
python3 scripts/ci/check-release-compose-test.py
python3 scripts/ci/real-effect-browser-db-snapshot-test.py
ruby scripts/ci/ci-release-dag-mutation-test.rb

echo "== go build =="
go build ./...

echo "== go vet =="
go vet ./...

echo "== database-free unit tests (PG package complement) =="
# Complement of the canonical PG set: every package except the ones the
# PostgreSQL lane owns.
all_pkgs="$(go list ./internal/... ./cmd/... | sort)"
pg_pkgs="$(sed 's|^\./|bansoko/|' "$root/scripts/ci/postgres-packages.txt" | sort)"
unit_pkgs=()
for pkg in $all_pkgs; do
  if ! grep -qxF "$pkg" <<<"$pg_pkgs"; then
    unit_pkgs+=("$pkg")
  fi
done

if [ "${#unit_pkgs[@]}" -eq 0 ]; then
  echo "no database-free packages to test" >&2
  exit 1
fi

# Explicit opt-in skips, declared not silent: public-network AniBT provider
# test and live qBittorrent tests require external services (BANSOKO_PROVIDER_OPTIN
# / BANSOKO_QB_*) and are never part of the required acceptance set. They are run
# by the documented opt-in lanes (ci-provider-optin.sh / real-effect harness).
export BANSOKO_TEST_SKIP_ALLOW="TestAniBTRealTorrentDownload|TestLiveHealth|TestLiveSync"

"$run_gate" unit-static "${unit_pkgs[@]}" -count=1

echo "Unit/static lane (Go) passed with no skipped tests"
