#!/bin/sh
# Shared HTTP status handling for the Gitea release package registry.
# Return 0 for 2xx, 4 only for an explicit 404, and 1 for all other failures.
ci_release_http_fetch() {
  target=$1
  url=$2
  auth_header=$3
  auth_value=$4
  status_file=$(mktemp "${TMPDIR:-/tmp}/ci-release-http-status.XXXXXX") || return 1
  if curl --silent --show-error --output "$target" --write-out '%{http_code}' \
    --header "$auth_header: $auth_value" "$url" >"$status_file"; then
    status=$(cat "$status_file")
    rm -f "$status_file"
    case "$status" in
      2[0-9][0-9]) return 0 ;;
      404) return 4 ;;
      *) echo "ERROR: package registry returned HTTP ${status:-unknown}" >&2; return 1 ;;
    esac
  else
    rm -f "$status_file"
    echo "ERROR: package registry request failed (network or curl error)" >&2
    return 1
  fi
}
