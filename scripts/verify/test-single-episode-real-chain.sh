#!/usr/bin/env bash
# Single-episode real-effect scenario: one authenticated real chain
# (RSS -> evaluation -> qB -> artifact -> file operation) against the
# real-effect base harness environment.
#
# Prefer the harness: `scripts/ci/real-effect-harness.sh up` writes an env file
# (BANSOKO_HARNESS_STATE_DIR or default /tmp/bansoko-harness.*/env) that supplies
# BANSOKO_TEST_DATABASE_URL, BANSOKO_QB_* and BANSOKO_SKIP_AUTH=0. Without a harness, the
# caller must provide the same variables explicitly (legacy mode; the harness
# is the authoritative entry).
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
export GOCACHE="${GOCACHE:-/tmp/bansoko-gocache}"

# Discover harness env file if none of the harness variables are set.
if [[ -z "${BANSOKO_TEST_DATABASE_URL:-}" && -z "${BANSOKO_QB_ENDPOINT:-}" ]]; then
  harness_env="${BANSOKO_HARNESS_ENV_FILE:-}"
  if [[ -z "$harness_env" ]]; then
    state_dir="${BANSOKO_HARNESS_STATE_DIR:-}"
    if [[ -n "$state_dir" && -f "$state_dir/env" ]]; then
      harness_env="$state_dir/env"
    else
      harness_env="$(ls -t /tmp/bansoko-harness.*/env 2>/dev/null | head -1 || true)"
    fi
  fi
  if [[ -z "$harness_env" || ! -f "$harness_env" ]]; then
    echo "single-episode-real-chain: no harness env found; run scripts/ci/real-effect-harness.sh up first" >&2
    exit 1
  fi
  echo "single-episode-real-chain: sourcing harness env $harness_env" >&2
  # shellcheck disable=SC1090
  set -a; source "$harness_env"; set +a
fi

: "${BANSOKO_TEST_DATABASE_URL:?set BANSOKO_TEST_DATABASE_URL to an isolated PostgreSQL database}"
: "${BANSOKO_QB_ENDPOINT:?set BANSOKO_QB_ENDPOINT to the real qBittorrent endpoint}"
: "${BANSOKO_QB_USERNAME:?set BANSOKO_QB_USERNAME to the real qBittorrent username}"
: "${BANSOKO_QB_PASSWORD:?set BANSOKO_QB_PASSWORD to the real qBittorrent password}"

# The harness is auth-enabled by construction; refuse the skip-auth shortcut.
if [[ "${BANSOKO_SKIP_AUTH:-0}" == "1" ]]; then
  echo "single-episode-real-chain: BANSOKO_SKIP_AUTH=1 is forbidden; the real-effect chain requires auth" >&2
  exit 1
fi

# The chain downloads a real Matroska file and reads its resolution, codecs and
# container with the real mediainfo binary; a missing tool fails, never skips.
if ! command -v mediainfo >/dev/null 2>&1; then
  echo "single-episode-real-chain: mediainfo is required on PATH" >&2
  exit 1
fi

"$root/scripts/ci/go-test-no-skips.sh" single-episode-real-chain -p 1 ./internal/integration \
  -run '^TestSingleEpisodeRealPostgresQBRSSFileOperation$' \
  -count=1
