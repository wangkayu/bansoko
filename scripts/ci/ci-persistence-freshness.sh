#!/usr/bin/env bash
# Execute every persistence generator's freshness check; failures must propagate.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
generator_dir="${BANSOKO_PERSISTENCE_GENERATOR_DIR:-$root/scripts/generators}"
shopt -s nullglob
generators=("$generator_dir"/generate-*-persistence.sh)
((${#generators[@]} > 0)) || { echo 'no persistence generators found' >&2; exit 1; }
for generator in "${generators[@]}"; do
  "$generator" --check
done
