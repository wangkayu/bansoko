#!/usr/bin/env bash
# P2.2 production Compose digest-only gate. Asserts that the production
# compose file (deployment/maintainer/docker-compose.production.yml) has:
#   - no `build:` sections (immutable bundle only)
#   - every application image pinned via @sha256: digest (injected by the
#     P0.7 builder as BANSOKO_BUNDLE_IMAGE_DIGEST), never a mutable tag
#   - no `latest` / bare-branch tags anywhere in the production reference
#   - no entrypoint/command overrides and no migration service (the bootstrap
#     migrates before serving), and no source mounts or host-path binds
#   - a single schema-owner BANSOKO_DATABASE_URL; no runtime credentials,
#     second DSN or generated secrets in the compose file or env.example
#   - one deployment-owned state bind mount (/config via BANSOKO_CONFIG_DIR) plus
#     PUID/PGID runtime-ownership env; download/library mounts and the
#     retired library-root env must not reappear (roots are DB owner config)
#   - no include/extends of the development compose
# The development compose (docker-compose.yml) may keep build: sections but
# must never be referenced by the production manifest graph.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
compose="${COMPOSE_FILE_OVERRIDE:-$root/deployment/maintainer/docker-compose.production.yml}"
failures=0

fail() { echo "check-production-compose: $*" >&2; failures=$((failures + 1)); }

[[ -f "$compose" ]] || { echo "check-production-compose: $compose not found" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "check-production-compose: docker CLI is required (fail-closed)" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "check-production-compose: docker compose plugin is required (fail-closed)" >&2; exit 1; }
# This is a static compose check; config parsing does not require a Docker daemon.

if grep -nE '^\s*build\s*:' "$compose"; then
  fail "production compose must not contain build: sections (immutable bundle only)"
fi

if grep -nE ':\s*(latest|main|master|dev|stable)(\s|$)' "$compose"; then
  fail "production compose must not reference mutable tags"
fi

# Every `image:` entry, including infrastructure services such as PostgreSQL,
# must be digest-pinned. Keep the check generic so a newly added service cannot
# silently reintroduce a mutable image tag.
all_images="$(grep -nE '^[[:space:]]+image:' "$compose" || true)"
if [[ -z "$all_images" ]]; then
  fail "no image references found"
else
  while IFS= read -r line; do
    image_ref="${line#*: *image:}"
    if ! printf '%s' "$image_ref" | grep -qE '@(sha256:[0-9a-f]{64}|\$\{BANSOKO_BUNDLE_IMAGE_DIGEST:\?[^}]+\})'; then
      fail "every production image must be digest-pinned: $line"
    fi
  done <<< "$all_images"
fi

# Application images must be digest-pinned to the current immutable registry repo.
source "$root/scripts/ci/check-production-image-refs.sh"
if ! check_application_image_refs "$compose"; then
  fail 'application image references must use the configurable lowercase repository ${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko} and the injected immutable digest'
fi

compose_json="$(mktemp)"
trap 'rm -f "$compose_json"' EXIT
if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  fail "docker compose is required to parse the production compose file"
elif ! docker compose -f "$compose" config --no-interpolate --format json >"$compose_json" 2>/dev/null; then
  fail "docker compose could not parse the production compose file"
else
  bind_errors="$(python3 - "$compose_json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    document = json.load(handle)

services = document.get("services", {})
for service_name, service in services.items():
    for volume in service.get("volumes") or []:
        if volume.get("type") != "bind":
            continue
        source = str(volume.get("source", ""))
        target = str(volume.get("target", ""))
        # compose config resolves relative sources against the compose directory,
        # so accept that prefix while requiring the exact deployment placeholder.
        allowed = ("$" + "{" + "BANSOKO_CONFIG_DIR:?" in source) and target == "/config"
        if not allowed:
            print("invalid bind: service=%s source=%r target=%r" % (service_name, source, target))

if "migrate" in services:
    print("the migration service is retired: the app bootstrap migrates before serving")
app = services.get("app", {})
for key in ("entrypoint", "command"):
    if key in app:
        print("app must run the image ENTRYPOINT (bootstrap) without a %s override" % key)
app_env = app.get("environment") or {}
for key in ("BANSOKO_MIGRATION_DATABASE_URL", "BANSOKO_RUNTIME_DATABASE_URL", "BANSOKO_DB_RUNTIME_USER",
            "BANSOKO_DB_RUNTIME_PASSWORD", "BANSOKO_BOOTSTRAP_MIGRATE_MODE", "BANSOKO_SESSION_SECRET",
            "BANSOKO_SECRET_ENCRYPTION_KEY"):
    if key in app_env:
        print("app must not configure %s: secrets and the runtime login are derived by the bootstrap" % key)
if not str(app_env.get("BANSOKO_DATABASE_URL", "")).startswith("postgres://$" + "{BANSOKO_DB_USER"):
    print("app BANSOKO_DATABASE_URL must be the schema-owner connection (BANSOKO_DB_USER)")
PY
)"
  if [[ -n "$bind_errors" ]]; then
    printf '%s\n' "$bind_errors"
    fail "production compose service/mount semantics are invalid"
  fi
fi

# No include/extends of the development compose.
if grep -nE 'include:|extends:' "$compose" | grep -iE 'docker-compose|\.yml'; then
  fail "production compose must not include/extend other compose files"
fi

# Retired variable mounts / env must not reappear: the download root is DB
# owner configuration (the qB connection root).
if grep -nE 'source: (downloads|library)|BANSOKO_LIBRARY_ROOT|:/downloads|:/library' "$compose" \
    | grep -vE ':([[:space:]]*)#'; then
  fail "production compose must not mount or configure download/library roots via the retired variable mounts/env"
fi

# The deployment-owned state dir bind (/config) is mandatory: it carries the
# recovery-signal directory and the update/bootstrap workspace.
if ! grep -q '\${BANSOKO_CONFIG_DIR:?' "$compose"; then
  fail "production compose must require BANSOKO_CONFIG_DIR for the /config state bind mount"
fi
if ! grep -qE 'target:[[:space:]]*/config' "$compose"; then
  fail "production compose must bind-mount the BANSOKO_CONFIG_DIR source at /config"
fi

# Runtime file-effect ownership comes from PUID/PGID env with safe defaults.
for var in PUID PGID; do
  if ! grep -qE "^[[:space:]]+${var}:[[:space:]]*\\\$\{${var}:-1000\}" "$compose"; then
    fail "production compose must declare ${var} with the \${${var}:-1000} default form"
  fi
done
if grep -nE 'BANSOKO_P(U|G)ID' "$compose"; then
  fail "production compose must use the standard PUID/PGID names"
fi

# The identity injected into the running app must default to the exact image
# digest selected by the release bundle. An independently required base digest
# would allow Compose and /readyz to describe different images.
if ! grep -qE 'BANSOKO_BASE_IMAGE_DIGEST:[[:space:]]*\$\{BANSOKO_BUNDLE_IMAGE_DIGEST:\?' "$compose"; then
  fail "production compose must derive BANSOKO_BASE_IMAGE_DIGEST from BANSOKO_BUNDLE_IMAGE_DIGEST"
fi

# The image ENTRYPOINT is the embedded-update bootstrap wrapper, which
# migrates the selected tree before serving: no separate migration job and no
# entrypoint override.
if grep -nE '^[[:space:]]+(entrypoint|command):' "$compose"; then
  fail "production compose must not override the bootstrap entrypoint/command"
fi
if grep -nE 'BANSOKO_BOOTSTRAP_MIGRATE_MODE' "$compose"; then
  fail "production compose must not configure the retired base-only migration job"
fi

# The durable embedded-update workspace (journal + staging/releases trees)
# lives inside the one state mount (the config bind); /app stays immutable and
# no separate updates volume exists.
if grep -qE 'source: updates$' "$compose" || grep -qE '/var/lib/bansoko/updates' "$compose"; then
  fail "production compose must keep updates under the config mount, not a separate volume"
fi
if ! grep -qE 'BANSOKO_UPDATES_ROOT:[[:space:]]*/config/updates([[:space:]]|$)' "$compose"; then
  fail "production compose must set BANSOKO_UPDATES_ROOT=/config/updates"
fi

# Backups are downloads and uploads; the server keeps no backup storage.
if grep -qE 'BANSOKO_BACKUP_(DIR|INTERVAL_HOURS|KEEP)|source: backups' "$compose"; then
  fail "production compose must not configure server-side backup storage"
fi

# Production has one update owner: the embedded bootstrap/updater. Keep this
# explicit and reject any host-supervisor socket/token wiring if it is added to
# the production manifest in the future. The historical supervisor overlay and
# renderer are intentionally not part of this gate or the deployment graph.
if ! grep -qE '^[[:space:]]+BANSOKO_UPDATE_MODE:[[:space:]]*embedded([[:space:]]|$)' "$compose"; then
  fail "production compose must explicitly select BANSOKO_UPDATE_MODE=embedded"
fi

# Database role separation: the only database input is the schema-owner
# BANSOKO_DATABASE_URL. The bootstrap migrates with it and derives the runtime
# login (fixed role, generated password) the server serves with; production
# startup refuses an owner/superuser serving login. No compose file may carry a
# second connection, runtime credentials or the generated secrets.
if ! grep -qE '^[[:space:]]+BANSOKO_DATABASE_URL:[[:space:]]*postgres://\$\{BANSOKO_DB_USER[:-]' "$compose"; then
  fail "the app BANSOKO_DATABASE_URL must be the schema-owner connection (BANSOKO_DB_USER)"
fi
retired_env='BANSOKO_(MIGRATION_DATABASE_URL|RUNTIME_DATABASE_URL|DB_RUNTIME_USER|DB_RUNTIME_PASSWORD|SESSION_SECRET|SECRET_ENCRYPTION_KEY)'
if grep -nE "^[[:space:]]+${retired_env}:" "$compose"; then
  fail "production compose must not configure derived connections, runtime credentials or generated secrets"
fi

# The maintainer env.example mirrors that surface: the owner coordinates only, and
# no runtime role or second DSN for operators to fill in.
env_example="$root/deployment/maintainer/env.example"
if [[ ! -f "$env_example" ]]; then
  fail "maintainer env.example is required to document the database input"
else
  if ! grep -qE '^BANSOKO_DB_USER=' "$env_example"; then
    fail "env.example must declare the schema owner BANSOKO_DB_USER"
  fi
  if grep -nE "^${retired_env}=" "$env_example"; then
    fail "env.example must not ask for derived connections, runtime credentials or generated secrets"
  fi
fi


if grep -nE '^[[:space:]]+BANSOKO_SUPERVISOR_[A-Z_]+:|^[[:space:]]+target:[[:space:]]*/run/bansoko|^[[:space:]]+source:[[:space:]]*\$\{BANSOKO_SUPERVISOR_' "$compose"; then
  fail "production compose must not expose retired host-supervisor socket/token wiring"
fi

if [[ "$failures" -gt 0 ]]; then
  echo "check-production-compose: FAILED with $failures violation(s)" >&2
  exit 1
fi
echo "check-production-compose: PASSED (digest-only, no build:, no mutable tags, no migration service or entrypoint override, updates under /config, /config bind + PUID/PGID present, single owner DATABASE_URL incl. env.example)"
