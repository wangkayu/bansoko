#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
out_root="${BANSOKO_FACT_COPY_OUT_ROOT:-$root}"
out="$out_root/frontend/src/lib/i18n/facts.generated.ts"
mkdir -p "$(dirname "$out")"
cd "$root"
go run ./scripts/fact-copy > "$out"
