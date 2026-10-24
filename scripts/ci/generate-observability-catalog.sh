#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
out_root="${BANSOKO_OBSERVABILITY_CATALOG_OUT_ROOT:-$root}"
out="$out_root/frontend/src/lib/observabilityCatalog.generated.ts"
mkdir -p "$(dirname "$out")"
cd "$root"
go run ./scripts/observability-catalog > "$out"
