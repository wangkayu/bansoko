#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
. "$root/scripts/ci/ci-release-preflight.sh"
. "$root/scripts/ci/ci-release-http.sh"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ci-release-preflight-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

expect_digest_rejected() {
  if ci_release_read_digest "$1" >/dev/null 2>&1; then
    echo "expected digest rejection: $1" >&2
    exit 1
  fi
}
printf '%064d\n' 0 | sed 's/0/a/g' | sed 's/^/sha256:/' >"$tmp/valid"
[ "$(ci_release_read_digest "$tmp/valid")" = "sha256:$(printf '%064d' 0 | sed 's/0/a/g')" ]
: >"$tmp/empty"
expect_digest_rejected "$tmp/empty"
printf '   \n' >"$tmp/blank"
expect_digest_rejected "$tmp/blank"
printf '%s\n' 'sha256:BAD' >"$tmp/invalid"
expect_digest_rejected "$tmp/invalid"
printf '%s\n' 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ' >"$tmp/trailing-space"
expect_digest_rejected "$tmp/trailing-space"
printf '%s' 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' >"$tmp/no-lf"
[ "$(ci_release_read_digest "$tmp/no-lf")" = 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' ]
printf '%s\n%s\n' 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' 'junk' >"$tmp/extra-line"
expect_digest_rejected "$tmp/extra-line"
printf '%s\r\n' 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' >"$tmp/crlf"
expect_digest_rejected "$tmp/crlf"
printf '%s\000' 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' >"$tmp/nul"
expect_digest_rejected "$tmp/nul"
printf '%s\n%s\n' 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' >"$tmp/multiple"
expect_digest_rejected "$tmp/multiple"

# Stub curl locally: no network access. Preserve the actual authorization header
# for an assertion and emulate HTTP statuses plus a transport failure.
curl() {
  out='' header='' writeout='' status="${HTTP_FIXTURE_STATUS:-200}"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --output) out=$2; shift 2 ;;
      --write-out) writeout=$2; shift 2 ;;
      --header) header=$2; shift 2 ;;
      *) shift ;;
    esac
  done
  [ "$header" = 'Authorization: token test-pat' ] || return 90
  [ "$writeout" = '%{http_code}' ] || return 91
  [ -n "$out" ] || return 92
  printf 'fixture' >"$out"
  case "$status" in
    network) return 28 ;;
    *) printf '%s' "$status" ;;
  esac
}
assert_http_status() {
  expected=$1
  fixture_status=$2
  set +e
  HTTP_FIXTURE_STATUS="$fixture_status" ci_release_http_fetch "$tmp/body" https://fixture.invalid/pkg Authorization 'token test-pat' >/dev/null 2>&1
  actual=$?
  set -e
  [ "$actual" -eq "$expected" ] || { echo "HTTP $fixture_status returned $actual; expected $expected" >&2; exit 1; }
}
assert_http_status 0 200
[ "$(cat "$tmp/body")" = fixture ]
assert_http_status 4 404
for status in 401 403 500 503 network; do
  assert_http_status 1 "$status"
done
printf '%s\n' 'CI release preflight and HTTP classification tests passed'
