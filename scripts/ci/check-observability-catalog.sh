#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
generator="$root/scripts/ci/generate-observability-catalog.sh"
generated_path="frontend/src/lib/observabilityCatalog.generated.ts"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

BANSOKO_OBSERVABILITY_CATALOG_OUT_ROOT="$scratch" "$generator"
if ! diff -q "$root/$generated_path" "$scratch/$generated_path" >/dev/null; then
  echo "generated observability catalog out of sync: $generated_path (run scripts/ci/generate-observability-catalog.sh)" >&2
  exit 1
fi
