#!/usr/bin/env bash
# Build standalone archives from the already-built frontend and the pinned Go/WASI sources.
set -euo pipefail
export LC_ALL=C
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
version="${BANSOKO_RELEASE_VERSION:?set BANSOKO_RELEASE_VERSION}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "invalid release version" >&2; exit 2; }
release_id="${BANSOKO_RELEASE_ID:-v$version}"
[[ "$release_id" =~ ^[0-9A-Za-z._-]+$ ]] || { echo "invalid release id" >&2; exit 2; }
commit="$(git rev-parse HEAD)"
epoch="${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct "$commit")}"
output="${BANSOKO_NATIVE_OUTPUT_DIR:-$root/.local-artifacts/native-release}"
dirty=false
if [[ -n "$(git status --porcelain)" ]]; then
  [[ "${BANSOKO_ALLOW_DIRTY_RELEASE:-0}" == 1 ]] || { echo "native release requires a clean worktree" >&2; exit 2; }
  dirty=true
fi
[[ -f frontend/dist/index.html ]] || { echo 'run pnpm --dir frontend build first' >&2; exit 2; }
mkdir -p "$output"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
stage="$tmp/stage"
mkdir "$stage"
cp LICENSE "$stage/"
pnpm --dir frontend licenses list --prod --json > "$tmp/frontend-licenses.json"
python3 - "$commit" "$epoch" "$dirty" "$stage/SOURCE" <<'PY'
import json
import sys
from pathlib import Path

commit, epoch, dirty, destination = sys.argv[1:]
manifest = json.loads(Path('scripts/build/mediainfo-wasm/manifest.json').read_text())
sdk = manifest['wasi_sdk']
lines = [
    'Bansoko corresponding source (GPL-3.0-only, section 6)',
    f'Source commit: {commit}',
    f'https://github.com/wangkayu/bansoko/tree/{commit}',
    f'https://github.com/wangkayu/bansoko/archive/{commit}.tar.gz',
]
if dirty == 'true':
    lines.append('Local rehearsal build with uncommitted changes. Do not distribute.')
lines.extend([
    '',
    'Build the frontend with pnpm --dir frontend install --frozen-lockfile and pnpm --dir frontend build.',
    'Rebuild the archives with scripts/ci/build-native-release.sh and the release version and ID.',
    f'SOURCE_DATE_EPOCH={epoch}',
    '',
    'Embedded MediaInfo WASI module sources:',
])
for component in manifest['components']:
    lines.append(f"{component['name']} {component['version']}: {component['url']}")
lines.extend([
    f"wasi-libc: the pinned submodule in wasi-sdk {sdk['version']} (https://github.com/WebAssembly/wasi-sdk/tree/wasi-sdk-{sdk['version'].split('.')[0]}/src/wasi-libc)",
    'https://github.com/WebAssembly/wasi-libc/tree/3f7eb4c7d6ede4dde3c4bffa6ed14e8d656fe93f',
    'Exact inputs and checksums: scripts/build/mediainfo-wasm/manifest.json',
    'Rebuild: bash scripts/build/build-mediainfo-wasm.sh',
])
Path(destination).write_text('\n'.join(lines) + '\n')
PY
build_time="$(python3 -c 'import datetime, sys; print(datetime.datetime.fromtimestamp(int(sys.argv[1]), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))' "$epoch")"
schema_version="$(find db/migrations -name '*.sql' -exec basename {} .sql \; | sort -n | tail -n 1)"
[[ -n "$schema_version" ]] || { echo 'no migrations found' >&2; exit 2; }

for platform in linux/amd64 linux/arm64 darwin/arm64; do
  goos="${platform%/*}"
  goarch="${platform#*/}"
  name="bansoko-$version-$goos-$goarch.tar.gz"
  buildinfo=bansoko/internal/runtime/buildinfo
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -tags embed_frontend -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid= -X $buildinfo.Version=$version -X $buildinfo.Commit=$commit -X $buildinfo.BuildID=$release_id -X $buildinfo.BuildTime=$build_time -X $buildinfo.BuildDirty=$dirty -X $buildinfo.SchemaVersion=$schema_version -X $buildinfo.BaseCompatibilityVersion=${BANSOKO_BASE_IMAGE_MIN_VERSION:-base-v1}" \
    -o "$stage/bansoko-server" ./cmd/bansoko-server
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" bash scripts/generators/generate-third-party-notices.sh \
    "$stage/THIRD_PARTY_NOTICES.md" "$tmp/frontend-licenses.json"
  {
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go list -tags embed_frontend -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Dir}}{{end}}{{end}}' ./cmd/bansoko-server | sort -u |
      while IFS='|' read -r module dir; do
        [[ -n "$module" ]] || continue
        found=0
        for file in "$dir"/LICENSE* "$dir"/COPYING* "$dir"/NOTICE*; do
          [[ -f "$file" ]] || continue
          found=1
          printf '\n=== %s / %s ===\n\n' "$module" "${file##*/}"
          cat "$file"
        done
        [[ "$found" == 1 ]] || { echo "missing license text for $module" >&2; exit 1; }
      done
    python3 - "$tmp/frontend-licenses.json" <<'PY'
import json
import sys
from pathlib import Path

groups = json.loads(Path(sys.argv[1]).read_text())
packages = sorted((item['name'], path) for rows in groups.values() for item in rows for path in item['paths'])
for name, path in packages:
    package = Path(path)
    version = json.loads((package / 'package.json').read_text())['version']
    texts = sorted(p for p in package.iterdir() if p.is_file() and p.name.lower().startswith(('license', 'licence', 'copying', 'notice')))
    if not texts and (name, version) == ('next-themes', '0.3.0'):
        # The npm package omits https://github.com/pacocoursey/next-themes/blob/v0.3.0/license.md.
        texts = [Path('deployment/licenses/next-themes-0.3.0.txt')]
    if not texts:
        raise SystemExit(f'missing license text for {name}@{version}')
    for text in texts:
        print(f'\n=== {name}@{version} / {text.name} ===\n')
        print(text.read_text(encoding='utf-8'))
PY
    for license in BSD-2-Clause Zlib Apache-2.0 MIT Apache-2.0-with-LLVM-exception; do
      printf '\n=== Embedded MediaInfo WASI components / %s ===\n\n' "$license"
      cat "deployment/licenses/$license.txt"
    done
  } > "$stage/THIRD_PARTY_LICENSES.txt"
  python3 - "$stage" "$tmp/$name" "$epoch" <<'PY'
import gzip
import sys
import tarfile
from pathlib import Path

stage, output, epoch = sys.argv[1:]
with open(output, 'wb') as stream, gzip.GzipFile(filename='', fileobj=stream, mode='wb', mtime=0) as zipped:
    with tarfile.open(fileobj=zipped, mode='w', format=tarfile.USTAR_FORMAT) as archive:
        for path in sorted(Path(stage).iterdir()):
            entry = tarfile.TarInfo(path.name)
            entry.size = path.stat().st_size
            entry.mode = 0o755 if path.name == 'bansoko-server' else 0o644
            entry.mtime = int(epoch)
            with path.open('rb') as contents:
                archive.addfile(entry, contents)
PY
  mv "$tmp/$name" "$output/$name"
done
(
  cd "$output"
  shasum -a 256 "bansoko-$version-darwin-arm64.tar.gz" "bansoko-$version-linux-amd64.tar.gz" "bansoko-$version-linux-arm64.tar.gz" > SHA256SUMS.txt
)
