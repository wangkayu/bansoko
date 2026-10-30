#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

build_dir="${BUILD_DIR:-builds}"
version="${VERSION:-dev}"
source_commit="$(git rev-parse HEAD 2>/dev/null || printf '%s' unknown)"
build_time="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

mkdir -p "$build_dir"

ldflags() {
  printf '%s' "-s -w -buildid= -X bansoko/internal/runtime/buildinfo.Version=${version} -X bansoko/internal/runtime/buildinfo.Commit=${source_commit} -X bansoko/internal/runtime/buildinfo.BuildID=local-${version} -X bansoko/internal/runtime/buildinfo.BuildTime=${build_time} -X bansoko/internal/runtime/buildinfo.BuildDirty=true"
}

build_server() {
  local goos="$1"
  local goarch="$2"
  local output="$3"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -buildvcs=false -ldflags="$(ldflags)" -o "$output" ./cmd/bansoko-server
}

build_support() {
  local goos="$1"
  local goarch="$2"
  # bansoko-recovery is built separately for host-owned recovery orchestration.
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "$build_dir/bansoko-recovery-${goos}-${goarch}" ./cmd/bansoko-recovery
}

clean() {
  rm -f "$build_dir"/bansoko-* "$build_dir"/bansoko-recovery-*
}

case "${1:-all}" in
  clean)
    clean
    ;;
  darwin)
    clean
    build_server darwin amd64 "$build_dir/bansoko-darwin-amd64"
    build_support darwin amd64
    ;;
  linux)
    clean
    build_server linux amd64 "$build_dir/bansoko-linux-amd64"
    build_support linux amd64
    ;;
  arm64)
    clean
    build_server linux arm64 "$build_dir/bansoko-linux-arm64"
    build_support linux arm64
    ;;
  all)
    clean
    build_server darwin amd64 "$build_dir/bansoko-darwin-amd64"
    build_server linux amd64 "$build_dir/bansoko-linux-amd64"
    build_server linux arm64 "$build_dir/bansoko-linux-arm64"
    build_support linux amd64
    build_support linux arm64
    ;;
  docker)
    image="${BANSOKO_IMAGE:-bansoko:${version}}"
    platforms="${BANSOKO_PLATFORMS:-linux/amd64}"
    # The image's /app/SOURCE names the commit it was built from, so a local
    # docker build passes the checkout's own commit; a dirty tree is still
    # named at that commit and the app reports the tree dirty. A local build is
    # not a conveyance, so the source address does not have to resolve yet.
    dirty=false
    if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then dirty=true; fi
    build_args=(--build-arg "VERSION=$version" --build-arg "SOURCE_COMMIT=$source_commit"
      --build-arg "BUILD_ID=local-$version" --build-arg "BUILD_TIME=$build_time"
      --build-arg "BUILD_DIRTY=$dirty")
    if [[ "${BANSOKO_PUSH:-0}" == "1" ]]; then
      docker buildx build --platform "$platforms" "${build_args[@]}" -t "$image" -f Dockerfile --push .
    else
      if [[ "$platforms" == "linux/amd64" ]]; then
        docker buildx build --platform "$platforms" "${build_args[@]}" -t "$image" -f Dockerfile --load .
      else
        docker buildx build --platform "$platforms" "${build_args[@]}" -t "$image" -f Dockerfile --output type=oci,dest="$build_dir/bansoko-${version}.oci.tar" .
      fi
    fi
    ;;
  *)
    echo "usage: $0 [clean|darwin|linux|arm64|all|docker]" >&2
    exit 2
    ;;
esac

if [[ "${1:-all}" != "clean" && "${1:-all}" != "docker" ]]; then
  ls -lh "$build_dir"/bansoko-* "$build_dir"/bansoko-*-* 2>/dev/null || true
fi
