#!/usr/bin/env bash
# Fail-closed guard for destructive PostgreSQL targets. Every script that runs
# `DROP SCHEMA`, restore `--clean`, or any equivalent destructive mutation must
# call this first. Rejection happens before any mutation.
#
# The rules live once in internal/testutil/disposabledb and are shared by
# the Go test helpers (MustDisposable) and this shell guard (via the
# cmd/disposable-db-check CLI), so the two cannot drift.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"

if [[ "${BANSOKO_TEST_DB_DISPOSABLE:-}" != "1" ]]; then
  echo "require-disposable-db: BANSOKO_TEST_DB_DISPOSABLE=1 must be set before any destructive PostgreSQL mutation" >&2
  exit 1
fi

go run "$root/cmd/disposable-db-check" check
