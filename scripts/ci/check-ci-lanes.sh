#!/usr/bin/env bash
# Validate the current CI DAG and execute the shared structural/freshness checks.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
ruby scripts/ci/ci-release-dag-test.rb
"$root/scripts/ci/check-ci-shards.sh"
