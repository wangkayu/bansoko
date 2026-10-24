#!/bin/sh
# The isolated test domain has one sequence source: the trusted ci.yml run.
resolve_ci_release_identity() {
  [ "${BANSOKO_RELEASE_REF_PROTECTED:-}" = true ] || { echo 'ERROR: unprotected release ref' >&2; return 1; }
  [ "${GITHUB_REF:-}" = refs/heads/main ] || { echo 'ERROR: only main publishes the isolated test domain' >&2; return 1; }
  [ "${BANSOKO_RELEASE_DEFAULT_BRANCH:-main}" = main ] || return 1
  printf '%s\n' "${GITHUB_SHA:-}" | grep -Eq '^[0-9a-f]{40}$' || { echo 'ERROR: immutable source SHA required' >&2; return 1; }
  case "${GITHUB_RUN_NUMBER:-}" in ''|*[!0-9]*) echo 'ERROR: positive run number required' >&2; return 1 ;; esac
  [ "$GITHUB_RUN_NUMBER" -gt 0 ] || return 1
  sequence="$GITHUB_RUN_NUMBER"
  version="0.0.0-test.$sequence"
  release_id="test-ci-$sequence"
}
