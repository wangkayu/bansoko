#!/usr/bin/env bash
# Build and start the local owner preview stack from one exact commit.
#
# The image is built from `git archive <rev>`, never from the working tree, so
# what the owner reviews is byte-for-byte the content that can be approved for
# push. The stack reads no project .env: every compose variable comes from the
# generated preview env file.
#
# Usage: scripts/maintainer/preview.sh [rev]   (default HEAD)
# State: $BANSOKO_PREVIEW_ROOT (default /tmp/bansoko-preview) keeps config,
# the database password and the last previewed commit; the database lives in
# the compose volume bansoko_postgres_data. qBittorrent is not managed here.
set -euo pipefail

repo=$(git rev-parse --show-toplevel)
sha=$(git -C "$repo" rev-parse --verify "${1:-HEAD}^{commit}")
tree=$(git -C "$repo" rev-parse "$sha^{tree}")
root=${BANSOKO_PREVIEW_ROOT:-/tmp/bansoko-preview}
src="$root/src"

# Shell BANSOKO_* values would override the env file; drop them.
for name in $(compgen -e | grep '^BANSOKO_' || true); do unset "$name"; done

mkdir -p "$root/config" "$repo/bansoko-downloads" "$repo/bansoko-library"
rm -rf "$src"
mkdir -p "$src"
git -C "$repo" archive "$sha" | tar -x -C "$src"

password_file="$root/db-password"
if [ ! -s "$password_file" ]; then
  (umask 077 && openssl rand -hex 16 >"$password_file")
fi

cat >"$root/preview.env" <<EOF
BANSOKO_DB_PASSWORD=$(cat "$password_file")
BANSOKO_DB_PORT=55028
BANSOKO_APP_PORT=8000
BANSOKO_VERSION=preview-${sha:0:9}
BANSOKO_SOURCE_COMMIT=$sha
BANSOKO_BUILD_ID=preview
BANSOKO_BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
BANSOKO_BUILD_DIRTY=false
EOF
chmod 600 "$root/preview.env"

# Same-target binds in an override replace the base file's relative ones.
cat >"$root/compose.preview.yml" <<EOF
services:
  app:
    build:
      context: $src
    environment:
      # A preview serves exactly the reviewed commit; the embedded updater
      # would replace it with the latest published release.
      BANSOKO_UPDATE_MODE: manual
    volumes:
      - $root/config:/config
      - $repo/bansoko-downloads:/downloads
      - $repo/bansoko-library:/library
EOF

docker compose \
  --project-name bansoko \
  --project-directory "$src" \
  --env-file "$root/preview.env" \
  -f "$src/deployment/maintainer/docker-compose.dev.yml" \
  -f "$root/compose.preview.yml" \
  up -d --build postgres app

for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null http://127.0.0.1:8000/api/v1/system/health; then
    printf 'commit %s\ntree %s\n' "$sha" "$tree" >"$root/last-preview"
    echo "preview ready: http://127.0.0.1:8000  commit ${sha:0:9}  tree ${tree:0:9}"
    exit 0
  fi
  sleep 2
done
echo "preview did not become healthy; see: docker logs bansoko-app" >&2
exit 1
