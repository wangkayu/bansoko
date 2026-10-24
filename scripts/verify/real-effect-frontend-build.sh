#!/usr/bin/env bash
# Install/build frontend into a caller-owned fresh output directory, failing closed.
set -euo pipefail
frontend_dir="${1:?frontend directory required}"
output_dir="${2:?fresh output directory required}"
[[ -d "$frontend_dir" ]] || { echo "frontend directory missing: $frontend_dir" >&2; exit 2; }
[[ ! -e "$output_dir" ]] || { echo "frontend output directory must be fresh: $output_dir" >&2; exit 2; }
mkdir -p "$(dirname "$output_dir")"
if [[ ! -d "$frontend_dir/node_modules" ]]; then
  if (cd "$frontend_dir" && pnpm install --frozen-lockfile); then
    :
  else
    install_status=$?
    echo "frontend dependency install failed (exit $install_status)" >&2
    exit "$install_status"
  fi
fi
mkdir -m 700 "$output_dir"
# pnpm 11 forwards a literal "--" to the script, and vite then ignores the
# options after it and writes to frontend/dist; pass the options directly.
if (cd "$frontend_dir" && pnpm build --outDir "$output_dir" --emptyOutDir); then
  :
else
  build_status=$?
  rm -rf "$output_dir"
  echo "frontend build failed (exit $build_status)" >&2
  exit "$build_status"
fi
if [[ ! -s "$output_dir/index.html" ]]; then
  rm -rf "$output_dir"
  echo "frontend build produced no non-empty index.html: $output_dir" >&2
  exit 20
fi
if ! find "$output_dir" -type f -size +0c -print -quit | grep -q .; then
  rm -rf "$output_dir"
  echo "frontend build produced no non-empty artifacts: $output_dir" >&2
  exit 20
fi
