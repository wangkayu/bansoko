#!/usr/bin/env bash
# Disposable VM session for running browser tests from another LAN machine.
#
# The session owns a scratch PostgreSQL/qB harness and an auth-enabled server.
# The server is LAN-reachable; PostgreSQL and qB remain loopback-only. Media
# downloads and FileOperation targets stay on the VM under /tmp.
#
# Usage on the SSH test VM:
#   BANSOKO_BROWSER_SESSION_STATE_DIR=/tmp/bansoko-browser-session \
#     scripts/verify/real-effect-browser-session.sh up
#   scripts/verify/real-effect-browser-session.sh status
#   scripts/verify/real-effect-browser-session.sh down
#
# Credential policy: env, owner.env and the harness state.json carry secrets
# and stay 0600 while active. `down` and the failed-up rollback always scrub
# env/owner.env; the harness state.json/journal are removed only after the
# harness teardown succeeded — when teardown fails they stay behind (0600 in
# the 0700 state dir) as recovery evidence. A successful down also archives
# browser-session.json under evidence/ so it stops acting as an
# active-session marker. Teardown keeps non-secret evidence (logs, fixture
# metadata) for debugging.
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
cmd="${1:-status}"
state_dir="${BANSOKO_BROWSER_SESSION_STATE_DIR:-/tmp/bansoko-browser-session}"
harness_state_file="$state_dir/state.json"
harness_journal_file="$state_dir/journal"
metadata_file="$state_dir/browser-session.json"
env_file="$state_dir/env"
owner_file="$state_dir/owner.env"
server_log="$state_dir/server.log"
fixture_script="$root/scripts/verify/real-effect-browser-fixture.py"
fixture_metadata_file="$state_dir/fixture.json"
fixture_log="$state_dir/fixture.log"
server_pid_file="$state_dir/server.pid"
fixture_pid_file="$state_dir/fixture.pid"
# Recorded process-group ids, written immediately after each setsid launch so
# teardown can identify a group even after its leader exited.
server_pgid_file="$state_dir/server.pgid"
fixture_pgid_file="$state_dir/fixture.pgid"
harness_log="$state_dir/harness.log"
# Session-private Playwright auth artifacts. auth.setup.ts /
# playwright.config.ts read these override paths (BANSOKO_E2E_AUTH_STATE_PATH /
# BANSOKO_E2E_AUTH_MARKER_PATH) so concurrent sessions never share the historical
# frontend/playwright/.auth/state.json.
playwright_auth_dir="$state_dir/playwright-auth"
playwright_auth_state="$playwright_auth_dir/state.json"
playwright_auth_marker="$playwright_auth_dir/skip-auth"

say() { printf '%s\n' "$*"; }
fail() { say "FAIL: $*" >&2; exit 1; }

require_clean_value() {
  local label="$1" value="$2"
  [[ -n "$value" ]] || fail "$label is empty"
  if [[ "$value" =~ [[:cntrl:]] ]]; then
    fail "$label contains control characters or newlines"
  fi
}

# Lexically normalize an absolute path: collapse duplicate slashes, drop '.'
# components and resolve '..' without touching the filesystem. Fails on
# relative input and on paths that escape their root.
lexical_normalize() {
  local path="$1"
  [[ "$path" == /* ]] || return 1
  local -a stack=()
  local segment
  local IFS='/'
  read -r -a segments <<<"$path"
  for segment in "${segments[@]}"; do
    case "$segment" in
      ''|.) ;;
      ..)
        (( ${#stack[@]} > 0 )) || return 1
        unset "stack[$(( ${#stack[@]} - 1 ))]"
        ;;
      *) stack+=("$segment") ;;
    esac
  done
  if (( ${#stack[@]} == 0 )); then
    printf '/\n'
  else
    printf '/%s\n' "${stack[*]}"
  fi
}

# The session state dir holds 0600 credentials and drives destructive
# teardown, so it must be a canonical, symlink-free, disposable path under
# /tmp (or the macOS /private/tmp alias) with a mikan- basename.
require_safe_state_dir() {
  [[ -n "$state_dir" ]] || fail "browser session state dir is empty"
  if [[ "$state_dir" =~ [[:cntrl:]] ]]; then
    fail "browser session state dir contains control characters"
  fi
  local lexical resolved
  lexical="$(lexical_normalize "$state_dir")" \
    || fail "browser session state dir must be an absolute path"
  if [[ "$lexical" != "$state_dir" ]]; then
    fail "browser session state dir must be canonical (no '.', '..' or duplicate slashes): $state_dir"
  fi
  case "$lexical" in
    /tmp/bansoko-*|/private/tmp/bansoko-*) ;;
    *) fail "browser session state dir must live under /tmp/bansoko-* (or /private/tmp/bansoko-*); got $state_dir" ;;
  esac
  if [[ -L "$state_dir" ]]; then
    fail "browser session state dir must not be a symlink"
  fi
  if [[ -d "$state_dir" ]]; then
    resolved="$(cd -P "$state_dir" 2>/dev/null && pwd -P)" \
      || fail "browser session state dir is not accessible"
    # /tmp is a symlink to /private/tmp on macOS; that single alias is allowed.
    if [[ "$resolved" != "$lexical" && "$resolved" != "/private$lexical" ]]; then
      fail "browser session state dir resolves outside the requested location ($state_dir -> $resolved)"
    fi
  fi
}

require_host_value() {
  local label="$1" value="$2"
  require_clean_value "$label" "$value"
  if [[ ! "$value" =~ ^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$ ]]; then
    fail "$label is not a usable host value (allowed: alphanumerics, dots, dashes, underscores)"
  fi
}

require_non_root() {
  [[ "$(id -u)" != "0" ]] || fail "browser session must run as a non-root user"
}

require_state() {
  [[ -f "$harness_state_file" && -f "$metadata_file" && -f "$env_file" ]] || fail "no browser session at $state_dir"
}

# Move browser-session.json into evidence/ under a UTC-timestamped name so a
# prior run's metadata is preserved (never silently overwritten) and stops
# acting as an active-session marker.
archive_browser_session_metadata() {
  [[ -e "$metadata_file" ]] || return 0
  mkdir -p "$state_dir/evidence"
  local stamp target
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  target="$state_dir/evidence/browser-session.$stamp.json"
  if [[ -e "$target" ]]; then
    target="$state_dir/evidence/browser-session.$stamp.$$.json"
  fi
  mv "$metadata_file" "$target"
  say "archived previous browser-session metadata: $target"
}

# Active-evidence detection for up(): only genuinely active markers block a
# new session. A leftover browser-session.json from an already torn-down
# session is stale, non-active evidence and gets archived instead.
require_no_active_session() {
  local evidence_file
  for evidence_file in "$harness_state_file" "$harness_journal_file" \
    "$env_file" "$owner_file" "$server_pid_file" "$fixture_pid_file" \
    "$server_pgid_file" "$fixture_pgid_file"; do
    if [[ -e "$evidence_file" ]]; then
      fail "active browser session evidence found ($evidence_file); run '$0 down' first"
    fi
  done
  archive_browser_session_metadata
}

fixture_value() {
  local key="$1"
  python3 - "$fixture_metadata_file" "$key" <<'PY'
import json, sys
path, key = sys.argv[1:]
value = json.load(open(path, encoding="utf-8"))[key]
print(value)
PY
}

json_value() {
  local key="$1"
  python3 - "$harness_state_file" "$key" <<'PY'
import json, sys
path, key = sys.argv[1:]
value = json.load(open(path, encoding="utf-8"))[key]
print(value)
PY
}

metadata_value() {
  local key="$1"
  python3 - "$metadata_file" "$key" <<'PY'
import json, sys
path, key = sys.argv[1:]
value = json.load(open(path, encoding="utf-8"))[key]
print(value)
PY
}

group_has_members() {
  kill -0 -- "-$1" 2>/dev/null
}

kill_process_group() {
  local pgid="$1"
  kill -TERM -- "-$pgid" 2>/dev/null || true
  local i
  for i in $(seq 1 20); do
    group_has_members "$pgid" || return 0
    sleep 0.25
  done
  # Re-check immediately before escalating: the group may have exited while
  # this process waited for the sleep, and KILLing a recycled group id would
  # hit unrelated processes.
  if group_has_members "$pgid"; then
    kill -KILL -- "-$pgid" 2>/dev/null || true
  fi
}

# Record a freshly launched leader's process group immediately after setsid.
# setsid makes the child a session/group leader, so the recorded pgid equals
# the pid; recording it lets teardown identify the group after the leader dies.
record_process_group() {
  local pid="$1" pgid_file="$2" label="$3"
  local pgid
  pgid="$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d '[:space:]')" \
    || fail "$label exited before its process group could be recorded"
  [[ "$pgid" =~ ^[0-9]+$ ]] || fail "$label process group could not be recorded"
  printf '%s\n' "$pgid" >"$pgid_file"
  [[ "$pgid" == "$pid" ]] || fail "$label is not the owner of its private process group"
}

stop_process_group() {
  local pid_file="$1" pgid_file="$2" expected="$3" label="$4"
  [[ -f "$pid_file" || -f "$pgid_file" ]] || return 0
  local pid="" recorded=""
  [[ -f "$pid_file" ]] && pid="$(tr -d '[:space:]' <"$pid_file")"
  [[ -f "$pgid_file" ]] && recorded="$(tr -d '[:space:]' <"$pgid_file")"
  # A missing/invalid pid file is not fatal when a valid pgid was recorded:
  # the leader-gone branch below can still identify and stop the group.
  if [[ "$pid" =~ ^[1-9][0-9]*$ ]]; then
    [[ -n "$recorded" ]] || recorded="$pid"
  fi
  # Sessions recorded before the pgid file existed still ran setsid, so their
  # group id equaled the leader pid.
  [[ "$recorded" =~ ^[1-9][0-9]*$ ]] || fail "$label pgid file is invalid"

  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    local live_pgid command
    live_pgid="$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d '[:space:]')" \
      || fail "$label pid $pid vanished during teardown"
    [[ "$live_pgid" == "$pid" ]] || fail "$label pid $pid is not the owner of its private process group"
    [[ "$live_pgid" == "$recorded" ]] || fail "$label pid $pid moved outside its recorded process group"
    command="$(ps -o command= -p "$pid" 2>/dev/null || true)"
    [[ "$command" == *"$expected"* ]] || fail "$label pid $pid command is not the expected process"
    kill_process_group "$live_pgid"
    rm -f "$pid_file" "$pgid_file"
    return 0
  fi

  # Leader is gone: only touch the recorded group when it still identifies as
  # ours. An empty group is stale bookkeeping — clean the files. A group whose
  # members match the expected fingerprint is ours — kill it. Anything else is
  # left in place with a warning rather than risking unrelated processes.
  if ! group_has_members "$recorded"; then
    rm -f "$pid_file" "$pgid_file"
    return 0
  fi
  local member_pids member_pid member_cmd matched=0
  member_pids="$(ps -axo pgid=,pid= 2>/dev/null | awk -v g="$recorded" '$1==g {print $2}')" || true
  for member_pid in $member_pids; do
    member_cmd="$(ps -o command= -p "$member_pid" 2>/dev/null || true)"
    [[ -n "$member_cmd" ]] || continue
    if [[ "$member_cmd" == *"$expected"* ]]; then
      matched=1
      break
    fi
  done
  if (( matched == 0 )); then
    fail "$label process group $recorded has live members but none matches the expected command; leaving pid/pgid evidence"
  fi
  kill_process_group "$recorded"
  rm -f "$pid_file" "$pgid_file"
}

stop_server() {
  stop_process_group "$server_pid_file" "$server_pgid_file" "bansoko-server" "server"
}

stop_fixture() {
  stop_process_group "$fixture_pid_file" "$fixture_pgid_file" "real-effect-browser-fixture.py" "fixture"
}

process_status_line() {
  local label="$1" pid_file="$2" pgid_file="$3"
  local pid="" recorded=""
  [[ -f "$pid_file" ]] && pid="$(tr -d '[:space:]' <"$pid_file")"
  [[ -f "$pgid_file" ]] && recorded="$(tr -d '[:space:]' <"$pgid_file")"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    say "  $label process: active"
  elif [[ -n "$recorded" ]] && group_has_members "$recorded"; then
    say "  $label process: active (leader exited; process group alive)"
  else
    say "  $label process: inactive"
  fi
}

# Run one teardown step in a subshell so a fail() inside it cannot abort the
# remaining cleanup steps; surface the failure as a warning and non-zero.
teardown_step() {
  local label="$1"; shift
  if ( "$@" ); then
    return 0
  fi
  say "WARN: $label failed; continuing teardown" >&2
  return 1
}

run_harness_down() {
  BANSOKO_HARNESS_STATE_DIR="$state_dir" "$root/scripts/ci/real-effect-harness.sh" down \
    >"$state_dir/teardown.log" 2>&1
}

# Remove pure-secret files: the sourced env files. They carry no recovery
# value — every secret in them is regenerated by the next up() — so they are
# scrubbed on every teardown path, successful or not. harness.log is kept:
# the harness redacts secrets from its output by construction, so the log is
# safe evidence — and it is the primary diagnostic when a failed-up rollback
# needs a root cause. Non-secret evidence (server/fixture/build logs, fixture
# metadata) stays too.
clean_session_secrets() {
  rm -f "$env_file" "$owner_file"
}

# Remove harness recovery records (state.json carries the db password; the
# journal maps half-built containers). These are removed ONLY after the
# harness teardown actually succeeded; when it failed they are the only map
# of still-running resources and stay behind (0600 in the 0700 state dir).
clean_harness_recovery_records() {
  rm -f "$harness_state_file" "$harness_journal_file"
}

# Rollback for a failed up(): stop whatever this session started using only
# the pid/pgid files plus the harness state file or rollback journal —
# browser-session metadata may not exist yet. Does not swallow the root error:
# the EXIT trap preserves the original exit status.
up_rollback() {
  trap - EXIT
  say "WARN: browser session setup failed; rolling back started components" >&2
  teardown_step "playwright auth cleanup" clean_playwright_auth || true
  teardown_step "server stop" stop_server || true
  teardown_step "fixture stop" stop_fixture || true
  local harness_down_ok=0
  if [[ -f "$harness_state_file" || -f "$harness_journal_file" ]]; then
    if run_harness_down; then
      harness_down_ok=1
    else
      say "WARN: harness down failed during rollback; see $state_dir/teardown.log" >&2
    fi
  else
    say "WARN: no harness state or rollback journal; unidentified harness resources (if any) were not cleaned" >&2
  fi
  teardown_step "credential cleanup" clean_session_secrets || true
  if (( harness_down_ok == 1 )); then
    teardown_step "harness record cleanup" clean_harness_recovery_records || true
  elif [[ -f "$harness_state_file" || -f "$harness_journal_file" ]]; then
    say "WARN: harness down did not succeed; state/journal kept at $state_dir as recovery evidence" >&2
  fi
}

# Remove only this session's Playwright auth artifacts (the override paths
# this script handed out). The historical shared default path
# (frontend/playwright/.auth/) may belong to another run and is never touched.
clean_playwright_auth() {
  rm -f "$playwright_auth_state" "$playwright_auth_marker"
}

up() {
  require_non_root
  require_no_active_session
  [[ -f "$fixture_script" ]] || fail "fixture script not found at $fixture_script"
  mkdir -p "$state_dir" "$state_dir/evidence"
  chmod 700 "$state_dir"
  mkdir -p "$playwright_auth_dir"
  chmod 700 "$playwright_auth_dir"
  export BANSOKO_E2E_AUTH_STATE_PATH="$playwright_auth_state"
  export BANSOKO_E2E_AUTH_MARKER_PATH="$playwright_auth_marker"

  # From here the session owns real resources (harness containers, fixture,
  # server). Any failure rolls them back best-effort via the EXIT trap while
  # the original error and exit status still propagate.
  trap up_rollback EXIT

  export BANSOKO_HARNESS_STATE_DIR="$state_dir"
  "$root/scripts/ci/real-effect-harness.sh" up >"$harness_log" 2>&1
  set -a
  # shellcheck disable=SC1090
  source "$env_file"
  set +a

  local frontend_run_dir
  frontend_run_dir="$(mktemp -d "$state_dir/frontend-build.XXXXXX")"
  local frontend_output_dir="$frontend_run_dir/dist"
  if ! "$root/scripts/verify/real-effect-frontend-build.sh" "$root/frontend" "$frontend_output_dir" >"$state_dir/frontend-build.log" 2>&1; then
    tail -80 "$state_dir/frontend-build.log" >&2 || true
    fail "frontend install/build failed; see $state_dir/frontend-build.log"
  fi

  local server_port public_host fixture_host fixture_health_host owner_pass server_url public_url
  server_port="${BANSOKO_HARNESS_SERVER_PORT:?harness did not provide BANSOKO_HARNESS_SERVER_PORT}"
  public_host="${BANSOKO_BROWSER_SESSION_PUBLIC_HOST:-$(hostname -I | awk '{print $1}')}"
  fixture_host="${BANSOKO_BROWSER_FIXTURE_PUBLIC_HOST:-host.docker.internal}"
  fixture_health_host="${BANSOKO_BROWSER_FIXTURE_BIND_HOST:-127.0.0.1}"
  require_host_value "public host" "$public_host"
  require_host_value "fixture public host" "$fixture_host"
  require_host_value "fixture bind host" "$fixture_health_host"
  require_host_value "listen host" "${BANSOKO_BROWSER_SESSION_LISTEN_HOST:-0.0.0.0}"
  owner_pass="browser-owner-$(openssl rand -hex 16)"
  # Session-scoped, attributable instance identity for the browser E2E run:
  # derived from the disposable state dir basename plus a random suffix. It
  # carries no secret and lets the spec and the SQL assertion pin exactly this
  # session's subscription (subscriptions.instance_label).
  local instance_label sanitized_base
  sanitized_base="$(printf '%s' "${state_dir##*/}" | tr -c 'A-Za-z0-9._-' '-')"
  # subscriptions.instance_label is CHECK-constrained to char_length <= 256
  # (db/migrations/001_baseline.sql). Cap the state-dir-derived middle
  # segment so the full label always fits while keeping the random suffix.
  sanitized_base="${sanitized_base:0:200}"
  instance_label="browser-e2e-${sanitized_base}-$(openssl rand -hex 4)"
  server_url="http://127.0.0.1:${server_port}"
  public_url="http://${public_host}:${server_port}"

  nohup setsid python3 "$fixture_script" \
    --bind-host "${BANSOKO_BROWSER_FIXTURE_BIND_HOST:-0.0.0.0}" \
    --url-host "$fixture_host" --port "${BANSOKO_BROWSER_FIXTURE_PORT:-0}" \
    --metadata-file "$fixture_metadata_file" >"$fixture_log" 2>&1 &
  echo "$!" >"$fixture_pid_file"
  local fixture_pid="$!"
  record_process_group "$fixture_pid" "$fixture_pgid_file" "fixture"
  local fixture_ready=0
  for _ in $(seq 1 20); do
    if [[ -f "$fixture_metadata_file" ]] && curl -sf -o /dev/null "http://${fixture_health_host}:$(fixture_value port)/health"; then
      fixture_ready=1
      break
    fi
    sleep 0.25
  done
  [[ "$fixture_ready" == "1" ]] || {
    tail -80 "$fixture_log" >&2 || true
    fail "fixture did not become ready"
  }

  export BANSOKO_SKIP_AUTH=0 BANSOKO_TEST_DB_DISPOSABLE=1
  # Two fixture host spellings are allowlisted: the qB container reaches the
  # fixture through host.docker.internal, while a saved provider connection
  # and the SSRF-guarded feed/torrent readers use "localhost". Both are
  # explicit development/test-only injections; production refuses the mapping.
  export BANSOKO_TEST_FIXTURE_HOSTS="host.docker.internal=127.0.0.1,localhost=127.0.0.1"
  export BANSOKO_LISTEN_ADDRESS="${BANSOKO_BROWSER_SESSION_LISTEN_HOST:-0.0.0.0}:${server_port}"
  export BANSOKO_FRONTEND_DIR="$frontend_output_dir"
  nohup setsid go run ./cmd/bansoko-server >"$server_log" 2>&1 &
  echo "$!" >"$server_pid_file"
  record_process_group "$!" "$server_pgid_file" "server"

  local ready=0
  for _ in $(seq 1 45); do
    if curl -sf -o /dev/null "$server_url/api/v1/livez"; then
      ready=1
      break
    fi
    sleep 1
  done
  [[ "$ready" == "1" ]] || {
    tail -80 "$server_log" >&2 || true
    fail "server did not become ready at $server_url"
  }

  local bootstrap_status code
  bootstrap_status="$(curl -sf "$server_url/api/v1/bootstrap/status" || true)"
  if [[ "$bootstrap_status" != *owner_exists* ]]; then
    code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$server_url/api/v1/bootstrap/owner" \
      -H 'Content-Type: application/json' \
      -d "{\"username\":\"owner\",\"password\":\"$owner_pass\"}")"
    [[ "$code" == "201" ]] || fail "bootstrap owner returned $code"
  fi

  export BANSOKO_BROWSER_FIXTURE_BASE_URL="$(fixture_value base_url)"
  export BANSOKO_BROWSER_FIXTURE_HEALTH_URL="$(fixture_value health_url)"
  export BANSOKO_BROWSER_FIXTURE_FEED_URL="$(fixture_value feed_url)"
  export BANSOKO_BROWSER_FIXTURE_TORRENT_URL="$(fixture_value torrent_url)"
  export BANSOKO_BROWSER_FIXTURE_ARTIFACT_URL="$(fixture_value artifact_url)"
  export BANSOKO_BROWSER_FIXTURE_TITLE="$(fixture_value managed_anime_title)"
  export BANSOKO_BROWSER_FIXTURE_GROUP="$(fixture_value group)"
  export BANSOKO_BROWSER_FIXTURE_METADATA_FILE="$fixture_metadata_file"
  export BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL="$instance_label"
  # Provider surface: a host-reachable AniBT base URL plus the exact fixture
  # identities a spec asserts on (managed identity is always the product's
  # managed_anime.id / candidate revision / snapshot identity; the BGM ref and
  # group slug are only the non-unique provider attributes).
  export BANSOKO_BROWSER_FIXTURE_PROVIDER_BASE_URL="$(fixture_value provider_base_url)"
  export BANSOKO_BROWSER_FIXTURE_COUNTERS_URL="$(fixture_value counters_url)"
  export BANSOKO_BROWSER_PROVIDER_ENTRY_REF="$(fixture_value provider_entry_ref)"
  export BANSOKO_BROWSER_PROVIDER_BGM_ID="$(fixture_value provider_bgm_id)"
  export BANSOKO_BROWSER_PROVIDER_GROUP_SLUG="$(fixture_value provider_group_slug)"
  export BANSOKO_BROWSER_PROVIDER_GROUP_NAME="$(fixture_value provider_group_name)"
  export BANSOKO_BROWSER_PROVIDER_SHOW_TITLE="$(fixture_value provider_show_title)"
  export BANSOKO_BROWSER_PROVIDER_RELEASE_TITLE="$(fixture_value provider_release_title)"
  export BANSOKO_BROWSER_PROVIDER_FILTERED_RELEASE_TITLE="$(fixture_value provider_filtered_release_title)"

  # Shell-safe assignments (%q): both files are consumed via `source` and stay
  # 0600 while the session is active.
  {
    printf 'BANSOKO_TEST_BASE_URL=%q\n' "$public_url"
    printf 'BANSOKO_E2E_USERNAME=%q\n' "owner"
    printf 'BANSOKO_E2E_PASSWORD=%q\n' "$owner_pass"
    printf 'BANSOKO_TEST_DB_DISPOSABLE=%q\n' "1"
    printf 'BANSOKO_TEST_DATABASE_URL=%q\n' "$BANSOKO_TEST_DATABASE_URL"
    printf 'BANSOKO_DATABASE_URL=%q\n' "$BANSOKO_DATABASE_URL"
    printf 'BANSOKO_SESSION_SECRET=%q\n' "$BANSOKO_SESSION_SECRET"
    printf 'BANSOKO_SECRET_ENCRYPTION_KEY=%q\n' "$BANSOKO_SECRET_ENCRYPTION_KEY"
    printf 'BANSOKO_HARNESS_LIBRARY_ROOT=%q\n' "$BANSOKO_HARNESS_LIBRARY_ROOT"
    printf 'BANSOKO_TEST_FIXTURE_HOSTS=%q\n' "$BANSOKO_TEST_FIXTURE_HOSTS"
    printf 'BANSOKO_QB_ENDPOINT=%q\n' "$(json_value qb_endpoint)"
    printf 'BANSOKO_QB_USERNAME=%q\n' "admin"
    printf 'BANSOKO_QB_PASSWORD=%q\n' "$BANSOKO_QB_PASSWORD"
    printf 'BANSOKO_QB_DOWNLOAD_ROOT=%q\n' "/downloads"
    printf 'BANSOKO_QB_DOWNLOADER_ROOT=%q\n' "$(json_value download_root)"
    printf 'BANSOKO_HARNESS_DOWNLOAD_ROOT=%q\n' "$(json_value download_root)"
    printf 'BANSOKO_TEST_FIXTURE_RSS_URL=%q\n' "$BANSOKO_BROWSER_FIXTURE_FEED_URL"
    printf 'BANSOKO_E2E_REAL_EFFECT=%q\n' "1"
    printf 'BANSOKO_E2E_AUTH_STATE_PATH=%q\n' "$playwright_auth_state"
    printf 'BANSOKO_E2E_AUTH_MARKER_PATH=%q\n' "$playwright_auth_marker"
    printf 'BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL=%q\n' "$instance_label"
    printf 'BANSOKO_BROWSER_FIXTURE_TITLE=%q\n' "$BANSOKO_BROWSER_FIXTURE_TITLE"
    printf 'BANSOKO_BROWSER_FIXTURE_GROUP=%q\n' "$BANSOKO_BROWSER_FIXTURE_GROUP"
    printf 'BANSOKO_BROWSER_FIXTURE_PROVIDER_BASE_URL=%q\n' "$BANSOKO_BROWSER_FIXTURE_PROVIDER_BASE_URL"
    printf 'BANSOKO_BROWSER_FIXTURE_COUNTERS_URL=%q\n' "$BANSOKO_BROWSER_FIXTURE_COUNTERS_URL"
    printf 'BANSOKO_BROWSER_PROVIDER_ENTRY_REF=%q\n' "$BANSOKO_BROWSER_PROVIDER_ENTRY_REF"
    printf 'BANSOKO_BROWSER_PROVIDER_BGM_ID=%q\n' "$BANSOKO_BROWSER_PROVIDER_BGM_ID"
    printf 'BANSOKO_BROWSER_PROVIDER_GROUP_SLUG=%q\n' "$BANSOKO_BROWSER_PROVIDER_GROUP_SLUG"
    printf 'BANSOKO_BROWSER_PROVIDER_GROUP_NAME=%q\n' "$BANSOKO_BROWSER_PROVIDER_GROUP_NAME"
    printf 'BANSOKO_BROWSER_PROVIDER_SHOW_TITLE=%q\n' "$BANSOKO_BROWSER_PROVIDER_SHOW_TITLE"
    printf 'BANSOKO_BROWSER_PROVIDER_RELEASE_TITLE=%q\n' "$BANSOKO_BROWSER_PROVIDER_RELEASE_TITLE"
    printf 'BANSOKO_BROWSER_PROVIDER_FILTERED_RELEASE_TITLE=%q\n' "$BANSOKO_BROWSER_PROVIDER_FILTERED_RELEASE_TITLE"
  } >"$env_file"
  chmod 600 "$env_file"
  {
    printf 'BANSOKO_TEST_BASE_URL=%q\n' "$public_url"
    printf 'BANSOKO_E2E_USERNAME=%q\n' "owner"
    printf 'BANSOKO_E2E_PASSWORD=%q\n' "$owner_pass"
    printf 'BANSOKO_TEST_DB_DISPOSABLE=%q\n' "1"
    printf 'BANSOKO_E2E_REAL_EFFECT=%q\n' "1"
    printf 'BANSOKO_E2E_AUTH_STATE_PATH=%q\n' "$playwright_auth_state"
    printf 'BANSOKO_E2E_AUTH_MARKER_PATH=%q\n' "$playwright_auth_marker"
    printf 'BANSOKO_TEST_FIXTURE_RSS_URL=%q\n' "$BANSOKO_BROWSER_FIXTURE_FEED_URL"
    # Non-secret: live specs anchor the naming templates at the session library.
    printf 'BANSOKO_HARNESS_LIBRARY_ROOT=%q\n' "$BANSOKO_HARNESS_LIBRARY_ROOT"
    printf 'BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL=%q\n' "$instance_label"
    printf 'BANSOKO_BROWSER_FIXTURE_TITLE=%q\n' "$BANSOKO_BROWSER_FIXTURE_TITLE"
    printf 'BANSOKO_BROWSER_FIXTURE_GROUP=%q\n' "$BANSOKO_BROWSER_FIXTURE_GROUP"
    printf 'BANSOKO_QB_ENDPOINT=%q\n' "$(json_value qb_endpoint)"
    printf 'BANSOKO_QB_USERNAME=%q\n' "admin"
    printf 'BANSOKO_QB_PASSWORD=%q\n' "$BANSOKO_QB_PASSWORD"
    printf 'BANSOKO_QB_DOWNLOAD_ROOT=%q\n' "/downloads"
    printf 'BANSOKO_QB_DOWNLOADER_ROOT=%q\n' "$(json_value download_root)"
    printf 'BANSOKO_HARNESS_DOWNLOAD_ROOT=%q\n' "$(json_value download_root)"
    printf 'BANSOKO_BROWSER_FIXTURE_PROVIDER_BASE_URL=%q\n' "$BANSOKO_BROWSER_FIXTURE_PROVIDER_BASE_URL"
    printf 'BANSOKO_BROWSER_FIXTURE_COUNTERS_URL=%q\n' "$BANSOKO_BROWSER_FIXTURE_COUNTERS_URL"
    printf 'BANSOKO_BROWSER_PROVIDER_ENTRY_REF=%q\n' "$BANSOKO_BROWSER_PROVIDER_ENTRY_REF"
    printf 'BANSOKO_BROWSER_PROVIDER_BGM_ID=%q\n' "$BANSOKO_BROWSER_PROVIDER_BGM_ID"
    printf 'BANSOKO_BROWSER_PROVIDER_GROUP_SLUG=%q\n' "$BANSOKO_BROWSER_PROVIDER_GROUP_SLUG"
    printf 'BANSOKO_BROWSER_PROVIDER_GROUP_NAME=%q\n' "$BANSOKO_BROWSER_PROVIDER_GROUP_NAME"
    printf 'BANSOKO_BROWSER_PROVIDER_SHOW_TITLE=%q\n' "$BANSOKO_BROWSER_PROVIDER_SHOW_TITLE"
    printf 'BANSOKO_BROWSER_PROVIDER_RELEASE_TITLE=%q\n' "$BANSOKO_BROWSER_PROVIDER_RELEASE_TITLE"
    printf 'BANSOKO_BROWSER_PROVIDER_FILTERED_RELEASE_TITLE=%q\n' "$BANSOKO_BROWSER_PROVIDER_FILTERED_RELEASE_TITLE"
  } >"$owner_file"
  chmod 600 "$owner_file"
  python3 - "$metadata_file" "$state_dir" "$server_port" "$server_url" "$public_url" "$owner_file" "$harness_state_file" "$fixture_metadata_file" "$fixture_pid_file" "$server_pid_file" "$playwright_auth_state" "$playwright_auth_marker" <<'PY'
import json, sys
path, state_dir, server_port, server_url, public_url, owner_file, harness_path, fixture_path, fixture_pid, server_pid, auth_state, auth_marker = sys.argv[1:]
harness = json.load(open(harness_path, encoding="utf-8"))
fixture = json.load(open(fixture_path, encoding="utf-8"))
metadata = {
    "schema": "bansoko-browser-session/v1", "state_dir": state_dir,
    "server_port": int(server_port), "server_url": server_url, "public_url": public_url,
    "owner": "owner", "owner_file": owner_file,
    "pg_id": harness["pg_id"], "qb_id": harness["qb_id"],
    "db_name": harness["db_url"].rsplit("/", 1)[-1].split("?", 1)[0],
    "db_port": harness["db_port"], "qb_port": harness["qb_port"],
    "download_root": harness["download_root"], "library_root": harness["library_root"],
    "fixture": fixture, "fixture_metadata_file": fixture_path,
    "fixture_pid_file": fixture_pid, "server_pid_file": server_pid,
    "playwright_auth_state": auth_state, "playwright_auth_marker": auth_marker,
}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(metadata, handle, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY
  chmod 600 "$metadata_file"

  # All resources are up and recorded in metadata; later failures are down()'s
  # job, not a rollback.
  trap - EXIT

  say "browser session ready"
  say "  base URL: $public_url"
  say "  instance label: $instance_label"
  say "  fixture feed: $(fixture_value feed_url)"
  say "  provider base URL: $(fixture_value provider_base_url)"
  say "  fixture metadata: $fixture_metadata_file"
  say "  credentials: $owner_file"
  say "  state: $state_dir"
  say "  browser command: set -a; source $owner_file; source $env_file; set +a"
  say "  cleanup: $0 down"
}

status() {
  require_state
  say "browser session: $state_dir"
  say "  base URL: $(metadata_value public_url)"
  say "  server: $(metadata_value server_url)"
  say "  postgres: $(json_value pg_id) (port $(json_value db_port))"
  say "  qB: $(json_value qb_id) (port $(json_value qb_port))"
  say "  download: $(json_value download_root)"
  say "  library: $(json_value library_root)"
  say "  fixture: $(fixture_value base_url)"
  say "  fixture feed: $(fixture_value feed_url)"
  process_status_line "server" "$server_pid_file" "$server_pgid_file"
  process_status_line "fixture" "$fixture_pid_file" "$fixture_pgid_file"
}

down() {
  # Partial sessions must stay cleanable: proceed from whatever evidence
  # exists instead of requiring the complete state trio.
  local found=0
  if [[ -f "$metadata_file" || -f "$harness_state_file" || -f "$harness_journal_file" \
    || -f "$env_file" || -f "$owner_file" || -f "$server_pid_file" || -f "$fixture_pid_file" \
    || -f "$server_pgid_file" || -f "$fixture_pgid_file" ]]; then
    found=1
  fi
  if (( found == 0 )); then
    fail "no browser session at $state_dir"
  fi
  local failed=0
  teardown_step "server stop" stop_server || failed=1
  teardown_step "fixture stop" stop_fixture || failed=1
  export BANSOKO_HARNESS_STATE_DIR="$state_dir"
  local harness_down_ok=0
  if run_harness_down; then
    harness_down_ok=1
  else
    say "WARN: harness down failed; see $state_dir/teardown.log" >&2
    failed=1
  fi
  teardown_step "credential cleanup" clean_session_secrets || failed=1
  if (( harness_down_ok == 1 )); then
    teardown_step "harness record cleanup" clean_harness_recovery_records || failed=1
  else
    say "WARN: harness state/journal kept at $state_dir as recovery evidence" >&2
  fi
  teardown_step "playwright auth cleanup" clean_playwright_auth || failed=1
  if (( failed == 0 )); then
    # Successful teardown: browser-session.json stops being an active-session
    # marker and moves to evidence/ with a UTC timestamp. With teardown
    # warnings it stays in place — the session may need a retry.
    teardown_step "session metadata archive" archive_browser_session_metadata || failed=1
  fi
  say "browser session torn down"
  say "  evidence: $state_dir"
  if [[ "$failed" != "0" ]]; then
    fail "browser session teardown completed with warnings above"
  fi
}

require_safe_state_dir

case "$cmd" in
  up) up ;;
  status) status ;;
  down) down ;;
  *) say "usage: $0 <up|status|down>" >&2; exit 2 ;;
esac
