#!/usr/bin/env bash
set -euo pipefail

# Manual soak lane: long race/churn matrices that live behind the `soak` build
# tag (for example the 100-round 150 retirement-vs-acceptance competition and
# the publication cleanup fault matrix). It is NOT part of the canonical
# PostgreSQL integration gate (scripts/ci/test-integration.sh) and is not a release gate; run it by hand
# when touching the code those matrices stress, and record the result.
#
# Same guards and environment as test-integration.sh: a disposable database, the
# segregated runtime role, -p 1, and the no-skip / S4 pending gate.

: "${BANSOKO_TEST_DATABASE_URL:?set BANSOKO_TEST_DATABASE_URL to an isolated PostgreSQL database}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
export GOCACHE="${GOCACHE:-/tmp/bansoko-gocache}"

run_gate="$root/scripts/ci/go-test-no-skips.sh"

"$root/scripts/ci/require-disposable-db.sh"

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

# Discover every soak-tagged test file and run exactly its Test functions, so a
# new soak matrix cannot be silently left out and the default-build tests of
# the same package are not re-run here.
mapfile -t soak_files < <(grep -rlE '^//go:build .*\bsoak\b' --include='*_test.go' internal cmd | sort)
if [[ ${#soak_files[@]} -eq 0 ]]; then
  echo "no soak-tagged test files found" >&2
  exit 1
fi
declare -A pkg_tests=()
for file in "${soak_files[@]}"; do
  pkg="./$(dirname "$file")"
  names="$(sed -nE 's/^func (Test[A-Za-z0-9_]+)\(t \*testing\.T\).*/\1/p' "$file" | paste -sd'|' -)"
  if [[ -z "$names" ]]; then
    echo "soak file without Test functions: $file" >&2
    exit 1
  fi
  pkg_tests[$pkg]="${pkg_tests[$pkg]:+${pkg_tests[$pkg]}|}$names"
done

status=0
for pkg in $(printf '%s\n' "${!pkg_tests[@]}" | sort); do
  label="soak-$(basename "$pkg")"
  echo "== $label: ${pkg_tests[$pkg]}"
  "$run_gate" "$label" -tags soak -p 1 "$pkg" -run "^(${pkg_tests[$pkg]})\$" \
    -count=1 -timeout "${BANSOKO_SOAK_TIMEOUT:-3h}" || status=1
done
if [[ $status -ne 0 ]]; then
  echo "Soak lane FAILED (manual lane; not a release gate)" >&2
  exit 1
fi
echo "Soak lane passed with no skipped tests"
