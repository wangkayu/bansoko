#!/usr/bin/env bash
# Fresh attributable PG for the non-root repository-scoped host runner.
set -euo pipefail
umask 077
[[ "$(id -u)" != 0 ]] || { echo 'host CI must run non-root' >&2; exit 1; }
mode=${1:?qb or postgres}
case "$mode" in qb|postgres) ;; *) exit 2 ;; esac
name="bansoko-ci-${GITHUB_RUN_ID:-local}-${mode}-$$"
password=$(openssl rand -hex 18)
cleanup() { docker rm -fv "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
printf '%s\n' "$password" | docker run -d --name "$name" --label bansoko.ci-owned=true -p 127.0.0.1::5432 -e POSTGRES_PASSWORD="$password" -e POSTGRES_USER=harness -e POSTGRES_DB=bansoko_ci_disp postgres:17 >/dev/null
# Probe over TCP: the image's init runs a socket-only temporary server and
# restarts it, so a socket probe can pass once and then refuse.
for _ in $(seq 1 60); do
  docker exec "$name" pg_isready -h 127.0.0.1 -U harness >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$name" pg_isready -h 127.0.0.1 -U harness >/dev/null
port=$(docker port "$name" 5432/tcp | cut -d: -f2)
export BANSOKO_TEST_DATABASE_URL="postgres://harness:$password@127.0.0.1:$port/bansoko_ci_disp?sslmode=disable"
export BANSOKO_TEST_DB_DISPOSABLE=1
if [[ "$mode" == qb ]]; then bash scripts/ci/ci-qb-real-chain.sh; else bash scripts/ci/test-integration.sh; fi
