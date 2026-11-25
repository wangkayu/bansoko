#!/usr/bin/env bash
# build-mediainfo-wasm.sh - reproducibly build internal/mediaprobe/mediainfo.wasm.gz
#
# Bansoko embeds MediaInfoLib compiled to a WASI reactor module and executes it
# with the pure-Go wazero runtime, so released binaries no longer need an
# external `mediainfo` executable. This script is the single documented way to
# regenerate that artifact:
#
#   scripts/build/build-mediainfo-wasm.sh
#
# Reproducibility contract:
#   - every upstream input (MediaInfoLib, ZenLib, zlib tarballs and the
#     wasi-sdk toolchain) is pinned by URL + SHA-256 in
#     scripts/build/mediainfo-wasm/manifest.json; verified bytes are the only
#     bytes compiled.
#   - the same pinned inputs produce the same artifact SHA-256 on the same
#     host, because compilation uses a pinned toolchain with deterministic
#     flags, all build paths are normalized with -ffile-prefix-map, and the
#     gzip trailer carries no timestamp (-n). Cross-host byte identity is
#     limited by upstream: the per-platform wasi-sdk 27.0 tarballs ship
#     different prebuilt sysroot objects, so rebuilds on another host are
#     functionally identical but not bit-identical; the committed artifact is
#     the canonical one and its hash is recorded in
#     internal/mediaprobe/mediainfo.wasm.json.
#   - WASI_SDK_PATH may point at an existing wasi-sdk 27.0 installation to
#     skip the toolchain download; its layout must be the official one
#     (bin/clang++, share/wasi-sysroot).
#
# The build downloads into ${MEDIAINFO_WASM_CACHE:-~/.cache/bansoko/mediainfo-wasm}
# and compiles in a self-cleaning temp directory. No Docker, no root.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
manifest="$root/scripts/build/mediainfo-wasm/manifest.json"
bridge="$root/scripts/build/mediainfo-wasm/bridge.cpp"
cache="${MEDIAINFO_WASM_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/bansoko/mediainfo-wasm}"
mkdir -p "$cache"

die() { echo "build-mediainfo-wasm: $*" >&2; exit 1; }
command -v python3 >/dev/null || die "python3 is required to read the manifest"
command -v curl >/dev/null || die "curl is required to fetch pinned tarballs"

# --- manifest ---------------------------------------------------------------
# Emits one shell assignment per line for the pinned component set.
eval "$(python3 - "$manifest" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
sdk = m["wasi_sdk"]
print(f"WASI_SDK_VERSION={sdk['version']}")
for key, asset in sdk["assets"].items():
    k = key.replace("-", "_").upper()
    print(f"WASI_SDK_URL_{k}={asset['url']}")
    print(f"WASI_SDK_SHA256_{k}={asset['sha256']}")
names = []
for i, c in enumerate(m["components"]):
    names.append(c["name"])
    print(f"COMPONENT_URL_{i}={c['url']}")
    print(f"COMPONENT_SHA256_{i}={c['sha256']}")
    print(f"COMPONENT_TARBALL_{i}={c['tarball']}")
    print(f"COMPONENT_NAME_{i}={c['name']}")
print(f"COMPONENT_COUNT={len(names)}")
PY
)"

sha256_of() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

fetch() { # fetch <url> <sha256> <dest-name> -> prints cached file path
  local url="$1" want="$2" name="$3"
  local dest="$cache/$name" got
  if [ -f "$dest" ]; then
    got="$(sha256_of "$dest")"
    if [ "$got" = "$want" ]; then
      printf '%s\n' "$dest"
      return 0
    fi
    echo "build-mediainfo-wasm: cache entry $name failed SHA-256, refetching" >&2
    rm -f "$dest"
  fi
  curl -fL --retry 3 --connect-timeout 20 -o "$dest.part" "$url" >&2
  got="$(sha256_of "$dest.part")"
  if [ "$got" != "$want" ]; then
    rm -f "$dest.part"
    die "SHA-256 mismatch for $url (got $got, want $want); refusing to build from unverified bytes"
  fi
  mv "$dest.part" "$dest"
  printf '%s\n' "$dest"
}

# --- toolchain --------------------------------------------------------------
host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
host_arch="$(uname -m)"
asset_key="${host_os}-${host_arch}"
asset_var="$(printf '%s' "$asset_key" | tr '[:lower:]-' '[:upper:]_')"

if [ -n "${WASI_SDK_PATH:-}" ]; then
  wasi="$WASI_SDK_PATH"
  [ -x "$wasi/bin/clang++" ] || die "WASI_SDK_PATH=$wasi has no bin/clang++"
  [ -d "$wasi/share/wasi-sysroot" ] || die "WASI_SDK_PATH=$wasi has no share/wasi-sysroot"
  echo "build-mediainfo-wasm: using WASI_SDK_PATH=$wasi" >&2
else
  url_var="WASI_SDK_URL_${asset_var}"
  sha_var="WASI_SDK_SHA256_${asset_var}"
  sdk_url="${!url_var:-}"
  sdk_sha="${!sha_var:-}"
  [ -n "$sdk_url" ] || die "no pinned wasi-sdk asset for host $asset_key; set WASI_SDK_PATH instead"
  sdk_tarball="$(fetch "$sdk_url" "$sdk_sha" "wasi-sdk-${WASI_SDK_VERSION}-${asset_key}.tar.gz")"
  wasi="$cache/wasi-sdk-${WASI_SDK_VERSION}-${asset_key}"
  if [ ! -x "$wasi/bin/clang++" ]; then
    rm -rf "$wasi.extract" "$wasi"
    mkdir -p "$wasi.extract"
    tar -xf "$sdk_tarball" -C "$wasi.extract" --strip-components=1
    mv "$wasi.extract" "$wasi"
  fi
fi

cxx="$wasi/bin/clang++"
cc="$wasi/bin/clang"
sysroot="$wasi/share/wasi-sysroot"

# --- sources ----------------------------------------------------------------
work="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-mediainfo-wasm-build.XXXXXX")"
trap 'rm -rf "$work"' EXIT INT TERM

for i in $(seq 0 $((COMPONENT_COUNT - 1))); do
  url_var="COMPONENT_URL_$i"; sha_var="COMPONENT_SHA256_$i"; tar_var="COMPONENT_TARBALL_$i"; name_var="COMPONENT_NAME_$i"
  tarball="$(fetch "${!url_var}" "${!sha_var}" "${!tar_var}")"
  case "${!tar_var}" in
    *.tar.xz) tar -xJf "$tarball" -C "$work" ;;
    *.tar.bz2) tar -xjf "$tarball" -C "$work" ;;
    *.tar.gz) tar -xzf "$tarball" -C "$work" ;;
    *) die "unsupported tarball format: ${!tar_var}" ;;
  esac
  echo "build-mediainfo-wasm: unpacked ${!name_var} (${!tar_var})" >&2
done

mi_src="$work/MediaInfoLib"
zen_src="$work/ZenLib"
zlib_src="$(find "$work" -maxdepth 1 -type d -name 'zlib-*' | head -1)"
[ -d "$mi_src/Source/MediaInfo" ] || die "MediaInfoLib tree missing after extraction"
[ -d "$zen_src/Source/ZenLib" ] || die "ZenLib tree missing after extraction"
[ -n "$zlib_src" ] || die "zlib tree missing after extraction"

# WASI is ILP32 and has no process model; two upstream files assume LP64 or a
# full POSIX surface. Both fixes are exact-string, idempotent patches applied
# to the verified tarball contents, documented here:
#   1. File_DvDif.h uses MediaInfo_int8u, a typedef only pulled in when
#      MEDIAINFO_EVENTS is on; we build buffer-only (events off), so map it to
#      ZenLib's identical int8u.
dvdif_h="$mi_src/Source/MediaInfo/Multiple/File_DvDif.h"
grep -q 'MediaInfo_int8u BlockStatus' "$dvdif_h" \
  && sed -i.bak 's/MediaInfo_int8u BlockStatus/int8u BlockStatus/' "$dvdif_h" \
  && rm -f "$dvdif_h.bak"
grep -q 'int8u BlockStatus' "$dvdif_h" || die "failed to patch $dvdif_h"

#   2. ZenLib/Thread.cpp calls pthread_cancel, which wasi-libc does not
#      provide. MediaInfo never spawns threads in buffer-only mode; the call
#      sits in Thread::ForceTerminate, so dropping it mirrors the upstream
#      Android path, which already compiles it out.
thread_cpp="$zen_src/Source/ZenLib/Thread.cpp"
grep -q 'pthread_cancel((pthread_t)ThreadPointer);' "$thread_cpp" \
  && sed -i.bak 's/    pthread_cancel((pthread_t)ThreadPointer);/    (void)ThreadPointer; \/\/ WASI: no pthread_cancel (buffer-only build never spawns threads)/' "$thread_cpp" \
  && rm -f "$thread_cpp.bak"
grep -q 'WASI: no pthread_cancel' "$thread_cpp" || die "failed to patch $thread_cpp"

# --- compile ----------------------------------------------------------------
tp="$mi_src/Source/ThirdParty"
defs="-DUNIX -DUNICODE -D_UNICODE -DSIZE_T_IS_LONG -DZ_HAVE_UNISTD_H"
defs="$defs -DMEDIAINFO_LIBCURL_NO -DMEDIAINFO_LIBMMS_NO -DMEDIAINFO_EVENTS_NO"
defs="$defs -DMEDIAINFO_AES_NO -DMEDIAINFO_FILE_NO -DMEDIAINFO_FIXITY_NO -DMEDIAINFO_GRAPH_NO"
defs="$defs -DMEDIAINFO_DVDIF_ANALYZE_NO"
inc="-I$mi_src/Source -I$tp -I$tp/tinyxml2 -I$tp/md5 -I$tp/sha1-gladman -I$tp/sha2-gladman"
inc="$inc -I$tp/aes-gladman -I$tp/hmac-gladman -I$zen_src/Source -I$zlib_src"
# -ffile-prefix-map normalizes the build-host-specific absolute paths (__FILE__
# and friends), so the module is bit-for-bit reproducible across hosts and build
# directories given the pinned toolchain.
common_flags="--target=wasm32-wasi --sysroot=$sysroot -O2 -DNDEBUG -fno-exceptions"
common_flags="$common_flags -ffile-prefix-map=$work=/build -ffile-prefix-map=$(dirname "$bridge")=/bridge"
common_flags="$common_flags $defs $inc"

mkdir -p "$work/objs"
cfiles="$work/cfiles.txt"
{
  ls "$zlib_src"/*.c | grep -vE '/(example|minigzip)\.c$'
  find "$zen_src/Source/ZenLib" -maxdepth 1 -name '*.cpp' \
    ! -name 'HTTP_Client.cpp' ! -name 'PreComp.cpp'
  find "$mi_src/Source/MediaInfo" -name '*.cpp' \
    ! -name 'Reader_libcurl.cpp' ! -name 'Reader_libmms.cpp' \
    ! -name 'File_DvDif_Analysis.cpp' ! -name 'ExternalCommandHelpers.cpp'
  echo "$tp/tinyxml2/tinyxml2.cpp"
  echo "$tp/fmt/format.cc"
  echo "$tp/tfsxml/tfsxml.c"
  echo "$tp/md5/md5.c"
  echo "$tp/sha1-gladman/sha1.c"
  echo "$tp/sha2-gladman/sha2.c"
  echo "$bridge"
} > "$cfiles"

jobs="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"
compile_one() { # compile_one <file>
  local f="$1" o
  o="$work/objs/$(echo "$f" | sed 's|[/.]|_|g').o"
  case "$f" in
    *.c) "$cc" $common_flags -c "$f" -o "$o" ;;
    *) "$cxx" $common_flags -std=c++17 -c "$f" -o "$o" ;;
  esac
}
export -f compile_one
export cc cxx common_flags work
xargs -P "$jobs" -I{} bash -c 'compile_one "$@"' _ {} < "$cfiles"

# --- link -------------------------------------------------------------------
# Reactor model: no _start; wazero runs _initialize once and then calls the
# mi_* exports per probe.
wasm="$work/mediainfo.wasm"
"$cxx" --target=wasm32-wasi --sysroot="$sysroot" -O2 -fno-exceptions -mexec-model=reactor \
  -Wl,--gc-sections -Wl,--strip-all -o "$wasm" "$work"/objs/*.o

for sym in _initialize mi_new mi_delete mi_set_output_json mi_open_buffer_init \
  mi_open_buffer_continue mi_open_buffer_goto_get mi_open_buffer_finalize \
  mi_inform mi_info_version mi_last_ptr mi_alloc mi_free; do
  grep -aq "$sym" "$wasm" || die "linked module is missing export $sym"
done

# --- artifact ---------------------------------------------------------------
artifact="$root/internal/mediaprobe/mediainfo.wasm.gz"
mkdir -p "$(dirname "$artifact")"
gzip -9 -n -c "$wasm" > "$artifact"

wasm_sha="$(sha256_of "$wasm")"
gz_sha="$(sha256_of "$artifact")"
gz_size="$(wc -c < "$artifact" | tr -d ' ')"
python3 - "$manifest" "$root/internal/mediaprobe/mediainfo.wasm.json" "$wasm_sha" "$gz_sha" "$gz_size" <<'PY'
import json, sys
manifest, out, wasm_sha, gz_sha, gz_size = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], int(sys.argv[5])
m = json.load(open(manifest))
doc = {
    "artifact": m["artifact"],
    "sha256": gz_sha,
    "size_bytes": gz_size,
    "uncompressed_wasm_sha256": wasm_sha,
    "exec_model": "wasi reactor (wasm32-wasi, -mexec-model=reactor)",
    "rebuild": "scripts/build/build-mediainfo-wasm.sh",
    "toolchain": m["wasi_sdk"],
    "components": m["components"],
}
json.dump(doc, open(out, "w"), indent=2)
open(out, "a").write("\n")
PY

echo "build-mediainfo-wasm: wrote $artifact" >&2
echo "build-mediainfo-wasm:   gzip size $gz_size bytes, sha256 $gz_sha" >&2
echo "build-mediainfo-wasm:   wasm sha256 $wasm_sha" >&2
