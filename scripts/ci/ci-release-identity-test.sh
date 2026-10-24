#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
. "$root/scripts/ci/ci-release-identity.sh"
export GITHUB_RUN_NUMBER=231 GITHUB_SHA=0000000000000000000000000000000000000001
export BANSOKO_RELEASE_REF_PROTECTED=true GITHUB_REF=refs/heads/main BANSOKO_RELEASE_DEFAULT_BRANCH=main
resolve_ci_release_identity
[ "$version:$release_id:$sequence" = '0.0.0-test.231:test-ci-231:231' ]
reject() { if resolve_ci_release_identity >/dev/null 2>&1; then echo 'identity counterexample survived' >&2; exit 1; fi; }
for ref in refs/tags/v0.9.1 refs/heads/other ''; do GITHUB_REF="$ref"; reject; done
GITHUB_REF=refs/heads/main
for n in 0 -1 run-231 ''; do GITHUB_RUN_NUMBER="$n"; reject; done
GITHUB_RUN_NUMBER=231
BANSOKO_RELEASE_REF_PROTECTED=false; reject
BANSOKO_RELEASE_REF_PROTECTED=true
for sha in '' unknown 1234; do GITHUB_SHA="$sha"; reject; done
printf '%s\n' 'Isolated test identity positive and negative tests passed'
