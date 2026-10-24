#!/usr/bin/env bash
# Generate and verify the bounded WorkItem sqlc persistence slice.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
expected_version="v1.31.1"
config="$root/sqlc.yaml"

usage() {
  printf 'Usage: %s [--check]\n' "${0##*/}" >&2
}

mode="generate"
case "$#" in
  0)
    ;;
  1)
    if [[ "$1" == "--check" ]]; then
      mode="check"
    else
      usage
      echo "unknown argument: $1" >&2
      exit 2
    fi
    ;;
  *)
    usage
    echo "unknown arguments" >&2
    exit 2
    ;;
esac

if [[ ! -f "$config" ]]; then
  echo "sqlc config not found: $config" >&2
  exit 2
fi

sqlc_bin="${SQLC_BIN:-$(command -v sqlc || true)}"
if [[ -z "$sqlc_bin" ]]; then
  gobin="$(go env GOBIN 2>/dev/null || true)"
  gopath="$(go env GOPATH 2>/dev/null || true)"
  for candidate in "${gobin:+$gobin/sqlc}" "${gopath%%:*}/bin/sqlc"; do
    if [[ -n "$candidate" && -x "$candidate" ]]; then
      sqlc_bin="$candidate"
      break
    fi
  done
fi
if [[ -z "$sqlc_bin" ]]; then
  echo "sqlc not found; install github.com/sqlc-dev/sqlc/cmd/sqlc@$expected_version" >&2
  exit 2
fi

actual_version="$("$sqlc_bin" version 2>/dev/null || true)"
if [[ "$actual_version" != "$expected_version" ]]; then
  echo "sqlc version mismatch (expected $expected_version, found ${actual_version:-unknown})" >&2
  exit 2
fi

cd "$root"
if [[ "$mode" == "check" ]]; then
  "$sqlc_bin" diff --no-remote -f "$config"
else
  "$sqlc_bin" generate --file "$config" --no-remote
  "$sqlc_bin" diff --no-remote -f "$config"
fi

echo "WorkItem sqlc ${mode} passed with $actual_version"
