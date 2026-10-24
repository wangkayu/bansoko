#!/usr/bin/env bash
# Known-vulnerability scan (SEC-033).
#
#   scripts/ci/check-vulns.sh            # both
#   scripts/ci/check-vulns.sh go         # govulncheck: Go modules + standard library
#   scripts/ci/check-vulns.sh frontend   # pnpm audit --prod: shipped frontend dependencies
#
# govulncheck reports only vulnerabilities whose code the binaries can reach.
# The standard library verdict depends on the toolchain, so the scan runs with
# the Go version the release image builds with (the Dockerfile go-builder
# stage), downloaded through GOTOOLCHAIN when the local Go differs. pnpm audit covers runtime ("dependencies") packages only;
# build tooling never ships. Advisories reviewed and accepted are listed in
# frontend/pnpm-workspace.yaml (auditConfig.ignoreGhsas) with the reason.
#
# Exit status is non-zero when either scan finds something or cannot run (for
# example, the advisory database is unreachable). CI runs this as a
# non-blocking job; a clean run is a public-release checklist item.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# Pinned scanner: bump deliberately (requires Go >= 1.26 to build).
GOVULNCHECK_VERSION=v1.9.0
target="${1:-all}"
failed=()

scan_go() {
  local shipped
  # The builder image is pinned by digest too, so the tag match must tolerate
  # "@sha256:..." after the tag.
  shipped="$(sed -n 's/^FROM \(--platform=[^ ]* \)\{0,1\}golang:\([0-9][0-9.]*\)-alpine\(@sha256:[0-9a-f]*\)\{0,1\} AS go-builder$/\2/p' "$root/Dockerfile")"
  if [[ -z "$shipped" ]]; then
    echo "cannot read the go-builder Go version from Dockerfile" >&2
    failed+=(go)
    return
  fi
  echo "== govulncheck $GOVULNCHECK_VERSION with go$shipped (the release image toolchain)"
  if ! (cd "$root" && GOTOOLCHAIN="go$shipped" go run "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION" ./...); then
    failed+=(go)
  fi
}

scan_frontend() {
  echo "== pnpm audit --prod"
  if ! (cd "$root/frontend" && pnpm audit --prod); then
    failed+=(frontend)
  fi
}

case "$target" in
  all) scan_go; scan_frontend ;;
  go) scan_go ;;
  frontend) scan_frontend ;;
  *) echo "usage: check-vulns.sh [all|go|frontend]" >&2; exit 2 ;;
esac

if (( ${#failed[@]} > 0 )); then
  echo "vulnerability scan failed: ${failed[*]}" >&2
  exit 1
fi
echo "vulnerability scan passed ($target)"
