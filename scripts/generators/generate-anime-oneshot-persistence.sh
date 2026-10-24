#!/usr/bin/env bash
# Compatibility alias; use generate-anime-persistence.sh for the canonical entry.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
exec "$root/scripts/generators/generate-anime-persistence.sh" "$@"
