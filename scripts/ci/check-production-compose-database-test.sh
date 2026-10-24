#!/usr/bin/env bash
# The production compose has one application service and one database input:
# the schema-owner BANSOKO_DATABASE_URL. The bootstrap migrates before serving
# and derives the runtime login and secrets itself. This proves the checker
# accepts the real file and rejects each way of reintroducing the retired
# migration job, a second connection, runtime credentials or stored secrets.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT
bash "$root/scripts/ci/check-production-compose.sh" >"$temp/positive.log" 2>&1
for mutant in migrate-service entrypoint session-secret runtime-dsn runtime-owner; do
  python3 - "$root/deployment/maintainer/docker-compose.production.yml" "$temp/$mutant.yml" "$mutant" <<'PY'
import pathlib, sys
source, out, mutant = sys.argv[1:]
text = pathlib.Path(source).read_text()
anchor = '      BANSOKO_UPDATE_MODE: embedded\n'
assert text.count(anchor) == 1
owner = '      BANSOKO_DATABASE_URL: postgres://${BANSOKO_DB_USER:-bansoko}:'
assert text.count(owner) == 1
if mutant == 'migrate-service':
    service = ('  migrate:\n'
               '    image: ${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko}@${BANSOKO_BUNDLE_IMAGE_DIGEST:?x}\n'
               '    entrypoint: ["/app/bootstrap", "migrate"]\n')
    text = text.replace('  app:\n', service + '  app:\n', 1)
elif mutant == 'entrypoint':
    text = text.replace('    container_name: bansoko-app\n', '    container_name: bansoko-app\n    entrypoint: ["/app/bansoko-server"]\n', 1)
elif mutant == 'session-secret':
    text = text.replace(anchor, anchor + '      BANSOKO_SESSION_SECRET: ${BANSOKO_SESSION_SECRET:?x}\n')
elif mutant == 'runtime-dsn':
    text = text.replace(anchor, anchor + '      BANSOKO_RUNTIME_DATABASE_URL: postgres://bansoko_app:x@postgres:5432/bansoko\n')
elif mutant == 'runtime-owner':
    text = text.replace(owner, '      BANSOKO_DATABASE_URL: postgres://${BANSOKO_DB_RUNTIME_USER:-bansoko_app}:')
else:
    raise AssertionError(mutant)
pathlib.Path(out).write_text(text)
PY
  if COMPOSE_FILE_OVERRIDE="$temp/$mutant.yml" bash "$root/scripts/ci/check-production-compose.sh" >"$temp/$mutant.log" 2>&1; then
    echo "production checker accepted database mutant: $mutant" >&2
    exit 1
  fi
  case "$mutant" in
    migrate-service) grep -q 'the migration service is retired' "$temp/$mutant.log" ;;
    entrypoint) grep -q 'must not override the bootstrap entrypoint' "$temp/$mutant.log" ;;
    session-secret|runtime-dsn) grep -q 'must not configure derived connections, runtime credentials or generated secrets' "$temp/$mutant.log" ;;
    runtime-owner) grep -q 'must be the schema-owner connection' "$temp/$mutant.log" ;;
  esac
done
echo 'production compose database surface: positive and five refusal cases passed'
