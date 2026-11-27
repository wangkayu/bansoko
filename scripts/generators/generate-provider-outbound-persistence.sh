#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
config="$root/internal/provider/outbound/persistence/sqlc.yaml"
bin="${SQLC_BIN:-$(command -v sqlc || true)}"
if [[ -z "$bin" ]]; then
  bin="$(go env GOBIN)/sqlc"
  if [[ ! -x "$bin" ]]; then
    gopath="$(go env GOPATH)"
    bin="${gopath%%:*}/bin/sqlc"
  fi
fi
if [[ "$("$bin" version)" != v1.31.1 ]]; then
  echo 'Provider outbound persistence requires sqlc v1.31.1' >&2
  exit 1
fi
case "$#:${1:-}" in
  0:) "$bin" generate --no-remote -f "$config" ;;
  1:--check) ;;
  *) echo "Usage: $0 [--check]" >&2; exit 2 ;;
esac
"$bin" diff --no-remote -f "$config"
echo 'Provider outbound persistence sqlc freshness passed'
