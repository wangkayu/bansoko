#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-reproducible.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

: "${BANSOKO_RELEASE_VERSION:?BANSOKO_RELEASE_VERSION is required}"
: "${BANSOKO_RELEASE_PRIVATE_KEY:?BANSOKO_RELEASE_PRIVATE_KEY is required}"
: "${BANSOKO_RELEASE_PUBLIC_KEY:?BANSOKO_RELEASE_PUBLIC_KEY is required}"
export BANSOKO_RELEASE_ID="${BANSOKO_RELEASE_ID:-reproducible-check}"
export BANSOKO_RELEASE_OUTPUT_DIR="$tmp/first"
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$root" show -s --format=%ct HEAD)}"
# Reproducibility of the signed content is proven on the binary + manifest +
# SBOM + provenance. Docker image layer digests include non-deterministic
# timestamps, so the double-build check runs binary-only (BANSOKO_RELEASE_SKIP_IMAGES)
# and the manifest's images↔artifact consistency is asserted separately.
export BANSOKO_RELEASE_SKIP_IMAGES="${BANSOKO_RELEASE_SKIP_IMAGES:-1}"

scripts/ci/build-release.sh >/dev/null
artifact_name="bansoko-server-${BANSOKO_RELEASE_GOOS:-linux}-${BANSOKO_RELEASE_GOARCH:-amd64}"
first_digest="$(shasum -a 256 "$tmp/first/$artifact_name" | awk '{print $1}')"
test -x "$tmp/first/$artifact_name"
export BANSOKO_RELEASE_OUTPUT_DIR="$tmp/second"
scripts/ci/build-release.sh >/dev/null
second_digest="$(shasum -a 256 "$tmp/second/$artifact_name" | awk '{print $1}')"
test -x "$tmp/second/$artifact_name"
test "$first_digest" = "$second_digest"
cmp "$tmp/first/release-manifest.json" "$tmp/second/release-manifest.json"
cmp "$tmp/first/sbom.spdx.json" "$tmp/second/sbom.spdx.json"
cmp "$tmp/first/provenance.json" "$tmp/second/provenance.json"
echo "Reproducible build check passed: $first_digest"
