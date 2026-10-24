#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
generator="$root/scripts/ci/generate-fact-copy.sh"
generated_path="frontend/src/lib/i18n/facts.generated.ts"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

BANSOKO_FACT_COPY_OUT_ROOT="$scratch" "$generator"
if ! diff -q "$root/$generated_path" "$scratch/$generated_path" >/dev/null; then
  echo "generated fact copy out of sync: $generated_path (run scripts/ci/generate-fact-copy.sh)" >&2
  exit 1
fi
