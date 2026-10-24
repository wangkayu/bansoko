#!/usr/bin/env bash
# CI tooling bootstrap for golang-based lanes. Source-friendly: jobs run
# `source scripts/ci/ci-tooling.sh` so PATH/RUBYOPT exports reach the job
# shell (a child-process script's exports do not survive).
#
# Installs (idempotently):
#   - ruby: gate scripts (go-test-no-skips, check-contract, check-release,
#     release-smoke) parse JSON/YAML with ruby; golang:1.25 has none.
#   - ripgrep: check-contract.sh scans the OpenAPI source with rg.
#   - node 22 + corepack pnpm: the contract lane regenerates the TS client
#     via frontend's openapi-typescript (pnpm 11.1.2 requires node >= 22.13).
#     Prefer an image-provided native Node.js; only the Debian golang lane
#     falls back to the glibc .tar.gz distribution.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -euo pipefail
fi

if command -v ruby >/dev/null 2>&1; then
  echo "ruby already available: $(ruby --version 2>/dev/null | head -1)"
else
  echo "installing ruby for gate scripts..."
  apt-get update -qq
  apt-get install -y --no-install-recommends ruby
fi

if ! command -v rg >/dev/null 2>&1; then
  echo "installing ripgrep..."
  apt-get install -y --no-install-recommends ripgrep
fi

# Ruby parsers read UTF-8 (Chinese) docs/sources; without a locale ruby
# defaults to US-ASCII and raises invalid byte sequence errors. (The pipeline
# variables also set these; keep them here for local/source use.)
export LANG=C.UTF-8
export LC_ALL=C.UTF-8
export RUBYOPT=-EUTF-8

# Prefer an existing Node.js installation. The contract-boundary lane installs
# Alpine's musl-native nodejs package; the Node.js Linux tarball below is built
# for glibc and must never be selected on musl.
ci_node_version_supported() {
  local version="$1"
  local major minor
  if [[ "$version" =~ ^v([0-9]+)\.([0-9]+)(\.|$) ]]; then
    major=$((10#${BASH_REMATCH[1]}))
    minor=$((10#${BASH_REMATCH[2]}))
    (( major > 22 || (major == 22 && minor >= 13) ))
  else
    return 1
  fi
}

ci_node_is_musl() {
  [[ -f /etc/alpine-release ]] && return 0
  compgen -G '/lib/ld-musl-*.so.1' >/dev/null 2>&1 && return 0
  command -v ldd >/dev/null 2>&1 && ldd --version 2>&1 | grep -qi musl
}

node_bin="$(command -v node 2>/dev/null || true)"
node_version=""
if [[ -n "$node_bin" ]]; then
  node_version="$($node_bin --version 2>/dev/null || true)"
fi

if ci_node_version_supported "$node_version"; then
  echo "using existing Node.js ${node_version} (${node_bin})"
elif ci_node_is_musl; then
  found_node="${node_version:-not found}"
  echo "Node.js >= 22.13 is required, but Alpine/musl has no compatible native Node.js (found ${found_node})." >&2
  echo "Install the Alpine nodejs package (and npm), or use a prebuilt image with Node.js >= 22.13; refusing to install the glibc Node.js archive." >&2
  if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    exit 1
  else
    return 1
  fi
else
  # golang:1.25 is Debian-based and has no suitable Node.js package. It has
  # no xz for .tar.xz archives, so retain the .tar.gz Node.js distribution.
  NODE_DIR=/opt/node-v22.22.2-linux-x64
  if ! "${NODE_DIR}/bin/node" --version >/dev/null 2>&1; then
    echo "installing node 22.22.2 toolchain..."
    curl -fsSL https://nodejs.org/dist/v22.22.2/node-v22.22.2-linux-x64.tar.gz \
      | tar -xz -C /opt
  fi
  export PATH="${NODE_DIR}/bin:${PATH}"
fi
if ! command -v pnpm >/dev/null 2>&1; then
  if command -v corepack >/dev/null 2>&1; then
    echo "preparing pnpm 11.1.2 via corepack..."
    corepack enable
    corepack prepare pnpm@11.1.2 --activate
  elif command -v npm >/dev/null 2>&1; then
    # Alpine's nodejs/npm packages do not ship Corepack. Install the pinned
    # package manager with the native npm instead of downloading glibc Node.js.
    echo "preparing pnpm 11.1.2 via npm..."
    npm install --global --no-fund --no-audit pnpm@11.1.2
  else
    echo "pnpm is unavailable and neither corepack nor npm is installed" >&2
    if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
      exit 1
    else
      return 1
    fi
  fi
fi

ruby --version
node --version
echo "ci tooling ready"
