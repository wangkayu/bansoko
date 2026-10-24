#!/usr/bin/env bash
# Explicit opt-in provider lane. Public-network AniBT/provider smoke tests are
# NOT part of the default Unit/Static or PostgreSQL required lanes (PRD R3).
# This script is the documented way to run them explicitly; it is never part
# of the required gate and must not be used to endorse the core gate.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

BANSOKO_PROVIDER_OPTIN=1 go test ./internal/acquisition/application \
  -run '^TestAniBTRealTorrentDownload$' -count=1 -v

echo "Provider opt-in lane passed (explicitly invoked, not a required gate)"
