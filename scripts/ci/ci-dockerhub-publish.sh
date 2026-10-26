#!/usr/bin/env bash
# Build the image for linux/amd64 + linux/arm64, prove the published artifact
# boots, and only then point the moving tag at it. Runs only after
# ci-release-trust.py. Go cross-compiles in the build-platform stages; the
# target stages of the foreign arch run under the host's QEMU binfmt
# registration.
#
# Two modes:
#   test     (main push)   immutable `<VERSION>-<sha9>`, moving `:test`
#   release  (vX.Y.Z tag)  immutable `X.Y.Z`, moving `:latest`; runs only after
#                          the signed release job of the same run succeeded,
#                          so `:latest` never moves to a version the channel
#                          refused
#
# The immutable tag is pushed first and the moving tag is promoted from it
# afterwards, so a user never pulls an image that has not served a request.
set -euo pipefail

: "${DOCKERHUB_TOKEN:?DOCKERHUB_TOKEN must be set}"
: "${GITHUB_SHA:?GITHUB_SHA must be set}"

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

image="wangkayu/bansoko"
user="wangkayu"
platforms="linux/amd64,linux/arm64"
short="$(git rev-parse --short=9 HEAD)"
mode="${1:-test}"
case "$mode" in
  test)
    version="$(tr -d '[:space:]' < VERSION)"
    immutable_tag="$version-$short"
    moving_tag="test"
    build_id="$short"
    ;;
  release)
    if [[ ! "${GITHUB_REF:-}" =~ ^refs/tags/v((0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*))$ ]]; then
      echo "release mode publishes only from a vX.Y.Z tag (got ${GITHUB_REF:-<unset>})" >&2
      exit 1
    fi
    version="${BASH_REMATCH[1]}"
    immutable_tag="$version"
    moving_tag="latest"
    build_id="v$version"
    ;;
  *)
    echo "unknown mode: $mode (test|release)" >&2
    exit 1
    ;;
esac
# Same PostgreSQL the quickstart bundles: the smoke proves the published image
# against the database a tester actually gets.
postgres_image="postgres@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73"
test "$(git rev-parse HEAD)" = "$GITHUB_SHA"
if [[ -n "$(git status --porcelain)" ]]; then
  echo "refusing to publish a dirty tree" >&2
  exit 1
fi

# The image tells every recipient where the source of that exact commit is
# (GPL-3.0-only section 6). A pointer a stranger cannot open is not an offer,
# so the address is proven reachable before anything is built: the archive URL
# anonymously, which also covers the repository URL it derives from. The check
# is skipped only when the repository is still private, where a 404 is expected
# and the owner has already accepted that publishing stays closed until the flip.
source_archive="https://github.com/wangkayu/bansoko/archive/$GITHUB_SHA.tar.gz"
repo_visibility="$(curl -fsS -H 'Accept: application/vnd.github+json' \
  "https://api.github.com/repos/$GITHUB_REPOSITORY" 2>/dev/null \
  | python3 -c 'import json,sys;print(json.load(sys.stdin).get("visibility",""))' 2>/dev/null || true)"
if [[ "$repo_visibility" == "public" ]]; then
  code="$(curl -sSL -o /dev/null -w '%{http_code}' "$source_archive")"
  if [[ "$code" != "200" ]]; then
    echo "refusing to publish: $source_archive answers $code anonymously, so the image's source offer would be dead" >&2
    exit 1
  fi
  echo "source offer reachable: $source_archive"
else
  echo "source offer not proven: the repository reports [${repo_visibility:-unknown}], not public; the image still names the commit" >&2
fi

# Credentials and the builder live in a private config dir removed on exit,
# so nothing persists on the shared host runner. The smoke's network and
# containers are named per run and removed the same way.
DOCKER_CONFIG="$(mktemp -d)"
export DOCKER_CONFIG
builder="bansoko-hub-${GITHUB_RUN_ID:-local}"
suffix="${GITHUB_RUN_ID:-local}-$$"
smoke_net="bansoko-smoke-$suffix"
smoke_pg="bansoko-smoke-pg-$suffix"
smoke_apps=()
cleanup() {
  if (( ${#smoke_apps[@]} > 0 )); then
    docker rm -f "${smoke_apps[@]}" >/dev/null 2>&1 || true
  fi
  docker rm -f "$smoke_pg" >/dev/null 2>&1 || true
  docker network rm "$smoke_net" >/dev/null 2>&1 || true
  # The per-run tag is registry state; the local copy only costs disk.
  docker image rm -f "$image:$immutable_tag" >/dev/null 2>&1 || true
  docker buildx rm "$builder" >/dev/null 2>&1 || true
  rm -rf "$DOCKER_CONFIG"
}
trap cleanup EXIT

printf '%s' "$DOCKERHUB_TOKEN" | docker login --username "$user" --password-stdin >/dev/null

# A release version tag is written once. A rerun of the same run finds its own
# commit there and rebuilds the same source; a different commit under the same
# version is refused (the signed channel gate refuses it as well).
if [[ "$mode" == "release" ]]; then
  existing="$(docker buildx imagetools inspect "$image:$immutable_tag" --format '{{json .Image}}' 2>/dev/null || true)"
  if [[ -n "$existing" ]]; then
    revisions="$(python3 -c 'import json,sys
d=json.loads(sys.stdin.read())
images=d.values() if all(isinstance(v,dict) and "config" in v for v in d.values()) else [d]
print(" ".join(sorted({(i.get("config") or {}).get("Labels",{}).get("org.opencontainers.image.revision","") for i in images})))' <<<"$existing")"
    if [[ "$revisions" != "$GITHUB_SHA" ]]; then
      echo "refusing to overwrite $image:$immutable_tag: it was built from [$revisions], not $GITHUB_SHA" >&2
      exit 1
    fi
    echo "$image:$immutable_tag already names $GITHUB_SHA; rebuilding the same source"
  fi
fi
docker buildx create --name "$builder" --driver docker-container --bootstrap >/dev/null

docker buildx build \
  --builder "$builder" \
  --platform "$platforms" \
  --build-arg VERSION="$version" \
  --build-arg SOURCE_COMMIT="$GITHUB_SHA" \
  --build-arg BUILD_ID="$build_id" \
  --build-arg BUILD_DIRTY=false \
  --build-arg BUILD_TIME="$(git show -s --format=%cI HEAD)" \
  --label org.opencontainers.image.source=https://github.com/wangkayu/bansoko \
  --label org.opencontainers.image.revision="$GITHUB_SHA" \
  --label org.opencontainers.image.version="$immutable_tag" \
  --tag "$image:$immutable_tag" \
  --push \
  .

docker buildx imagetools inspect "$image:$immutable_tag" --format '{{json .Manifest}}' \
  | python3 -c 'import json,sys
m=json.load(sys.stdin)
got=sorted(p["platform"]["architecture"] for p in m["manifests"] if p.get("platform",{}).get("os")=="linux")
assert got==["amd64","arm64"], got
print(sys.argv[1], "for", ", ".join(got))' "pushed $image:$immutable_tag"

# A manifest check proves the image was built; it says nothing about whether it
# starts. Boot each architecture exactly as a tester would — the image's own
# defaults plus only BANSOKO_DATABASE_URL — and require the health endpoint.
# The foreign architecture runs under the host's QEMU binfmt registration, which
# is slower, so its poll window is wider.
boot_smoke() {
  local platform="$1" polls="$2"
  local app="bansoko-smoke-app-$platform-$suffix" logs port ready=0
  smoke_apps+=("$app")
  logs="$(mktemp)"
  docker run -d --name "$app" --platform "linux/$platform" \
    --network "$smoke_net" -p 127.0.0.1::8080 \
    -e "BANSOKO_DATABASE_URL=postgres://bansoko:bansoko@$smoke_pg:5432/bansoko?sslmode=disable" \
    "$image:$immutable_tag" >/dev/null
  port="$(docker port "$app" 8080/tcp 2>/dev/null | head -1 | sed 's/.*://')" || port=""
  if [[ -z "$port" ]]; then
    docker logs "$app" >&2 2>&1 || true
    echo "smoke: the $platform container published no port" >&2
    exit 1
  fi
  for _ in $(seq 1 "$polls"); do
    docker ps --format '{{.Names}}' | grep -qx "$app" || break
    if curl -fsS "http://127.0.0.1:$port/api/v1/livez" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 1
  done
  docker logs "$app" >"$logs" 2>&1 || true
  if [[ "$ready" != "1" ]] || grep -q 'startup_fatal' "$logs"; then
    tail -n 40 "$logs" >&2
    echo "smoke: the $platform image did not serve /api/v1/livez" >&2
    rm -f "$logs"
    exit 1
  fi
  echo "boot smoke: $image:$immutable_tag ($platform) serves /api/v1/livez from its own defaults"
  # The GPL source offer has to name the commit this very image was built from;
  # a missing or empty file would mean SOURCE_COMMIT never reached the runtime
  # stage, which is exactly the failure a per-stage ARG makes silent.
  if ! docker exec "$app" grep -qx "    $GITHUB_SHA" /app/SOURCE >/dev/null 2>&1; then
    docker exec "$app" cat /app/SOURCE >&2 2>&1 || echo "smoke: /app/SOURCE is missing" >&2
    echo "smoke: the $platform image does not offer the source of $GITHUB_SHA" >&2
    exit 1
  fi
  if ! docker exec "$app" grep -qi 'Copyright (C) 2026 wangkayu' /app/LICENSE >/dev/null 2>&1; then
    echo "smoke: the $platform image carries no copyright notice at /app/LICENSE" >&2
    exit 1
  fi
  # The base image is a second and third distribution: busybox is GPL-2.0-only
  # and the PostgreSQL client tools are the upstream project's licence, and
  # neither is in the Go module graph the notice list is built from. Their rows
  # are appended in the runtime stage, where the package databases are; a smoke
  # that finds neither the rows nor their texts means that step silently went
  # unlisted.
  if ! docker exec "$app" grep -q '^| busybox |' /app/THIRD_PARTY_NOTICES.md >/dev/null 2>&1 ||
     ! docker exec "$app" grep -q '^| postgresql |' /app/THIRD_PARTY_NOTICES.md >/dev/null 2>&1; then
    echo "smoke: the $platform image does not list its base image components" >&2
    exit 1
  fi
  if ! docker exec "$app" grep -q '^PostgreSQL$' /app/THIRD_PARTY_LICENSES.txt >/dev/null 2>&1 ||
     ! docker exec "$app" grep -q '^GPL-2.0-only$' /app/THIRD_PARTY_LICENSES.txt >/dev/null 2>&1; then
    echo "smoke: the $platform image carries no base image licence texts" >&2
    exit 1
  fi
  echo "source offer: $image:$immutable_tag ($platform) names $GITHUB_SHA"
  docker rm -f "$app" >/dev/null
  rm -f "$logs"
}

docker network create "$smoke_net" >/dev/null
docker run -d --name "$smoke_pg" --network "$smoke_net" \
  -e POSTGRES_DB=bansoko -e POSTGRES_USER=bansoko -e POSTGRES_PASSWORD=bansoko \
  "$postgres_image" >/dev/null
pg_ready=0
for _ in $(seq 1 60); do
  if docker exec "$smoke_pg" pg_isready -U bansoko -d bansoko >/dev/null 2>&1; then
    pg_ready=1
    break
  fi
  sleep 1
done
if [[ "$pg_ready" != "1" ]]; then
  docker logs "$smoke_pg" >&2 2>&1 || true
  echo "smoke: the throwaway PostgreSQL did not become ready" >&2
  exit 1
fi

boot_smoke amd64 120
boot_smoke arm64 300

# Only a proven build becomes the moving tag.
docker buildx imagetools create --tag "$image:$moving_tag" "$image:$immutable_tag" >/dev/null
docker buildx imagetools inspect "$image:$moving_tag" --format '{{json .Manifest}}' \
  | python3 -c 'import json,sys
m=json.load(sys.stdin)
got=sorted(p["platform"]["architecture"] for p in m["manifests"] if p.get("platform",{}).get("os")=="linux")
assert got==["amd64","arm64"], got
print(sys.argv[1], "from", sys.argv[2], "for", ", ".join(got))' "promoted $image:$moving_tag" "$image:$immutable_tag"
