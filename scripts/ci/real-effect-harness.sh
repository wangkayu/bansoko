#!/usr/bin/env bash
# Real-effect base harness — platform-agnostic scratch environment.
#
# Usage:
#   real-effect-harness.sh up      create disposable PG + migrate + controlled
#                                  qB + temp download/library + auth-enabled
#                                  scratch server; writes state + env files
#   real-effect-harness.sh down    stop/remove everything from the state file
#                                  (or, after a failed up, from the rollback
#                                  journal)
#   real-effect-harness.sh status  print resource identity + path mapping
#   real-effect-harness.sh check   refuse unsafe env (BANSOKO_SKIP_AUTH=1, prod
#                                  paths, prod DB/qB credentials)
#
# Never touches production databases, production qB, workspace user media
# directories, or long-lived credentials. All data is disposable/scratch.
#
# Credential policy: env file and state.json carry secrets (db password,
# session secret, encryption key) and stay 0600 while active; `down` and the
# failed-up rollback remove them once cleanup fully succeeded — after a
# failed cleanup the state file stays behind as recovery evidence. Harness
# stdout/stderr never prints secret values — db_url passwords are redacted,
# including migrate-driver errors.
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"
cmd="${1:-check}"
state_dir="${BANSOKO_HARNESS_STATE_DIR:-$(mktemp -d /tmp/bansoko-harness.XXXXXX)}"
state_file="$state_dir/state.json"
env_file="$state_dir/env"
# Rollback journal: appended immediately as up() creates each resource, so a
# mid-setup failure (migration, qB, config, env/state write) can still tear
# down everything already created. Removed once state.json is complete.
journal_file="$state_dir/journal"

die() {
  echo "real-effect-harness: $*" >&2
  exit 1
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

require_safe_state_dir() {
  [[ -n "$state_dir" ]] || die "state dir is empty"
  if [[ "$state_dir" =~ [[:cntrl:]] ]]; then
    die "state dir contains control characters"
  fi
  local lexical resolved
  lexical="$(lexical_normalize "$state_dir")" \
    || die "state dir must be an absolute path without escaping '..' components"
  if [[ "$lexical" != "$state_dir" ]]; then
    die "state dir must be canonical (no '.', '..' or duplicate slashes): $state_dir"
  fi
  case "$lexical" in
    /tmp/bansoko-*|/private/tmp/bansoko-*) ;;
    *) die "state dir must live under /tmp/bansoko-* (or /private/tmp/bansoko-*); got $state_dir" ;;
  esac
  if [[ -L "$state_dir" ]]; then
    die "state dir must not be a symlink"
  fi
  if [[ -d "$state_dir" ]]; then
    resolved="$(cd -P "$state_dir" 2>/dev/null && pwd -P)" || die "state dir is not accessible"
    # /tmp is a symlink to /private/tmp on macOS; that single alias is allowed.
    if [[ "$resolved" != "$lexical" && "$resolved" != "/private$lexical" ]]; then
      die "state dir resolves outside the requested location ($state_dir -> $resolved)"
    fi
  fi
}

# Destructive filesystem targets must be harness-created temp roots under the
# disposable /tmp namespace. Anything else is refused instead of deleted.
is_harness_temp_path() {
  case "$1" in
    /tmp/bansoko-harness-*|/private/tmp/bansoko-harness-*) ;;
    # Download/library roots may live under BANSOKO_HARNESS_MEDIA_PARENT (a
    # session prefix, e.g. an NFS or SMB mount); only their exact mktemp basenames are accepted there.
    /*/bansoko-harness-downloads.??????|/*/bansoko-harness-library.??????) ;;
    *) return 1 ;;
  esac
  case "$1" in
    *..*|*//*|*/.|*/./*|*/) return 1 ;;
  esac
  return 0
}

validate_docker_id() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$ ]]
}

# Best-effort removal of one recorded docker container. Succeeds only when
# the container is verifiably gone: either the removal succeeded or docker
# reports the object missing. Daemon-level ambiguity counts as failure so the
# caller keeps the recovery record.
remove_container_verified() {
  local id="$1"
  validate_docker_id "$id" || return 1
  if docker rm -f -v "$id" >/dev/null 2>&1; then
    return 0
  fi
  local inspect_output
  if inspect_output="$(docker inspect "$id" 2>&1)"; then
    return 1
  fi
  case "$inspect_output" in
    *"No such object"*) return 0 ;;
    *) return 1 ;;
  esac
}

require_docker() {
  if ! docker info >/dev/null 2>&1; then
    echo "real-effect-harness: Docker is required (docker info failed)" >&2
    exit 1
  fi
}

check() {
  # Refuse BANSOKO_SKIP_AUTH=1: the harness must always prove the auth-enabled story.
  if [[ "${BANSOKO_SKIP_AUTH:-0}" == "1" ]]; then
    echo "real-effect-harness: BANSOKO_SKIP_AUTH=1 is forbidden; harness requires auth enabled" >&2
    exit 1
  fi
  # Refuse production/user media paths for download/library roots.
  for var in BANSOKO_HARNESS_DOWNLOAD_ROOT BANSOKO_HARNESS_LIBRARY_ROOT; do
    val="${!var:-}"
    if [[ -n "$val" ]]; then
      case "$val" in
        /Users/*/bansoko-library|/media/*|/mnt/*|/home/*/mikan-*|/home/*/bansoko-*|/*/Downloads*)
          echo "real-effect-harness: path $val looks like a user/production media path" >&2
          exit 1
          ;;
      esac
    fi
  done
  echo "real-effect-harness check passed"
}

json_field() {
  python3 - "$state_file" "$1" <<'PY'
import json, sys
path, key = sys.argv[1:]
print(json.load(open(path, encoding="utf-8"))[key])
PY
}

json_field_or_empty() {
  python3 - "$state_file" "$1" <<'PY'
import json, sys
path, key = sys.argv[1:]
print(json.load(open(path, encoding="utf-8")).get(key, ""))
PY
}

journal_add() {
  local kind="$1" value="$2"
  printf '%s %s\n' "$kind" "$value" >>"$journal_file"
}

# Consume the rollback journal: remove every recorded docker container and
# temp path. Entries whose cleanup cannot be verified are KEPT (rewritten
# back into the journal) as recovery evidence; the journal file is deleted
# only when every entry was consumed or verifiably no longer exists. Returns
# non-zero when entries were retained. Safe to call when the journal is
# absent.
consume_journal() {
  [[ -f "$journal_file" ]] || return 0
  local retained=0 kind value
  local retained_journal="$journal_file.retained"
  : >"$retained_journal"
  while read -r -u 3 kind value; do
    case "$kind" in
      docker)
        if remove_container_verified "$value"; then
          continue
        fi
        echo "real-effect-harness: journal container '$value' could not be removed; keeping journal entry" >&2
        ;;
      path)
        if ! is_harness_temp_path "$value"; then
          echo "real-effect-harness: journal path '$value' is outside the harness temp namespace; refusing to remove; keeping journal entry" >&2
        else
          # Best-effort cleanup: /tmp is sticky (1777), so a container-chowned
          # root dir can only be removed by its owner or root. Leftovers are
          # harmless scratch dirs; they must never fail the teardown phase.
          rm -rf "$value" 2>/dev/null || true
          continue
        fi
        ;;
      *)
        # Unknown journal kinds are never silently dropped or executed.
        echo "real-effect-harness: journal entry has unknown kind '$kind'; keeping journal entry" >&2
        ;;
    esac
    printf '%s %s\n' "$kind" "$value" >>"$retained_journal"
    retained=1
  done 3<"$journal_file"
  if (( retained == 1 )); then
    mv "$retained_journal" "$journal_file"
    return 1
  fi
  rm -f "$retained_journal" "$journal_file"
  return 0
}

# env file and state.json both carry credentials; they are removed on every
# completed teardown path (down or failed-up rollback).
remove_credential_files() {
  rm -f "$env_file" "$state_file"
}

# EXIT trap armed by up(): if setup failed before state.json was written, roll
# back every resource recorded in the journal plus any credential-bearing
# leftover. Disarmed right after state.json lands successfully.
harness_rollback() {
  local rc=$?
  trap - EXIT
  if [[ -f "$state_file" ]]; then
    # Complete state was recorded; teardown belongs to `down`.
    exit "$rc"
  fi
  echo "real-effect-harness: setup failed; rolling back recorded resources" >&2
  if ! consume_journal; then
    echo "real-effect-harness: some resources could not be rolled back; journal kept at $journal_file" >&2
  fi
  remove_credential_files
  exit "$rc"
}

up() {
  check
  require_docker
  require_safe_state_dir
  # Refuse to stack a second harness onto a state dir that still tracks one:
  # creating resources before this check would orphan the previous set.
  if [[ -e "$state_file" || -e "$journal_file" ]]; then
    die "harness state already present at $state_dir (state.json/journal); run 'real-effect-harness.sh down' first"
  fi
  mkdir -p "$state_dir"
  trap harness_rollback EXIT
  local port_lib
  port_lib=$(( 20000 + RANDOM % 20000 ))
  local db_port=$port_lib
  local qb_port=$(( port_lib + 1 ))
  local db_name="bansoko_harness_$RANDOM"
  local db_pass="harness-$RANDOM-$RANDOM"

  # Disposable PostgreSQL container.
  local pg_id
  pg_id="$(docker run -d --rm \
    -e POSTGRES_USER=harness -e POSTGRES_PASSWORD="$db_pass" -e POSTGRES_DB="$db_name" \
    -p "127.0.0.1:${db_port}:5432" \
    postgres:17)"
  journal_add docker "$pg_id"
  local db_url="postgres://harness:${db_pass}@127.0.0.1:${db_port}/${db_name}?sslmode=disable"

  echo "waiting for postgres..." >&2
  local deadline=$(( $(date +%s) + 60 ))
  until docker exec "$pg_id" pg_isready -h 127.0.0.1 -U harness -d "$db_name" >/dev/null 2>&1; do
    if (( $(date +%s) > deadline )); then
      # EXIT trap rolls the recorded resources back.
      die "postgres not ready in 60s"
    fi
    sleep 1
  done

  # Full migration. Output is piped through a redactor so a failing driver can
  # never print the DSN password into this script's stderr; pipefail keeps the
  # migration's failure status authoritative. '#' is the sed delimiter, so the
  # replacement is '#***#' — a '/' here would stay part of the REGEX pattern
  # and make '***' an invalid repetition operator.
  BANSOKO_DATABASE_URL="$db_url" go run ./cmd/bansoko-migrate 2>&1 | sed -e "s#${db_pass}#***#g"

  # Temp download/library roots on the host. qB's container-side /downloads is
  # the same tree the bansoko-server host sees at $download_root — the save_root /
  # downloader_root mapping the real-effect chain requires.
  # BANSOKO_HARNESS_MEDIA_PARENT (optional) puts the download/library roots on a
  # chosen filesystem, e.g. an NFS or SMB session prefix on the VM so
  # publication runs on a network filesystem; the default stays /tmp (B8b).
  local media_parent="${BANSOKO_HARNESS_MEDIA_PARENT:-/tmp}"
  case "$media_parent" in
    /*) ;;
    *) die "BANSOKO_HARNESS_MEDIA_PARENT must be absolute; got $media_parent" ;;
  esac
  case "$media_parent" in
    *..*|/media/*|/mnt/*|/home/*|/Users/*|*/Downloads*) die "BANSOKO_HARNESS_MEDIA_PARENT looks unsafe or like a user media path: $media_parent" ;;
  esac
  [[ -d "$media_parent" ]] || die "BANSOKO_HARNESS_MEDIA_PARENT does not exist: $media_parent"
  local download_root
  download_root="$(mktemp -d "$media_parent/bansoko-harness-downloads.XXXXXX")"
  journal_add path "$download_root"
  local library_root
  library_root="$(mktemp -d "$media_parent/bansoko-harness-library.XXXXXX")"
  journal_add path "$library_root"
  local config_root
  config_root="$(mktemp -d /tmp/bansoko-harness-config.XXXXXX)"
  journal_add path "$config_root"
  # Harness roots are shared between the lane user and the qB container, which
  # chowns its /downloads mount to the container PUID/PGID on first start.
  # World-writable keeps the chain usable no matter which user launched the
  # harness (the container changes ownership, not mode bits).
  chmod 0777 "$download_root" "$library_root" "$config_root"
  # Controlled qBittorrent container with a temp download root. The
  # linuxserver image ignores WEBUI_PASSWORD, so seed the WebUI admin password
  # into a temp config dir while the container is still stopped, then mount it.
  local qb_password="qb-$RANDOM-$RANDOM"
  local qb_config_root
  qb_config_root="$(mktemp -d /tmp/bansoko-harness-qbconfig.XXXXXX)"
  journal_add path "$qb_config_root"
  # Same reasoning as the download/library roots: qB chowns /config to the
  # container PUID/PGID, so keep the host tree world-writable for the lane user.
  chmod 0777 "$qb_config_root"
  # Seed a complete qBittorrent.conf: qB rewrites-and-resets an incomplete
  # config on first start (dropping the seeded password), so the template must
  # carry the same keys qB itself writes (MigrationVersion, Cookies, Port).
  # The WebUI password is derived with the repo's PBKDF2 seeder.
  BANSOKO_QB_PASSWORD="$qb_password" "$root/scripts/ci/seed-qb-password.sh" \
    "$qb_config_root/qBittorrent/qBittorrent.conf" >/dev/null
  {
    # Session\Port is the BitTorrent listen port and must differ from the
    # WebUI port; use the next free port in the harness range.
    printf '[BitTorrent]\nSession\\Port=%d\nSession\\QueueingSystemEnabled=false\n\n' "$(( qb_port + 1 ))"
    printf '[Meta]\nMigrationVersion=6\n\n'
    printf '[Network]\nCookies=@Invalid()\n\n'
    printf '[Preferences]\n'
    printf 'WebUI\\Port=%d\n' "$qb_port"
    printf 'WebUI\\Username=admin\n'
    grep 'WebUI\\Password_PBKDF2' "$qb_config_root/qBittorrent/qBittorrent.conf" || true
  } > "$qb_config_root/qBittorrent/qBittorrent.conf.new"
  mv "$qb_config_root/qBittorrent/qBittorrent.conf.new" "$qb_config_root/qBittorrent/qBittorrent.conf"
  chmod 600 "$qb_config_root/qBittorrent/qBittorrent.conf"

  local qb_id
  qb_id="$(docker run -d --rm \
    -e PUID="$(id -u)" -e PGID="$(id -g)" -e TZ=Asia/Shanghai -e WEBUI_PORT="$qb_port" \
    --add-host host.docker.internal:host-gateway \
    -v "$qb_config_root:/config" \
    -v "$download_root:/downloads" \
    -p "127.0.0.1:${qb_port}:${qb_port}" \
    ghcr.io/linuxserver/qbittorrent:4.6.7)"
  journal_add docker "$qb_id"

  # Scratch auth-enabled server env (random session secret + encryption key).
  local session_secret="$(openssl rand -hex 32)"
  local encryption_key="$(openssl rand -hex 32)"
  local server_port=$(( qb_port + 1 ))

  # Shell-safe assignments (%q): the file is consumed via `source`.
  {
    printf 'BANSOKO_TEST_DATABASE_URL=%q\n' "$db_url"
    printf 'BANSOKO_TEST_DB_DISPOSABLE=%q\n' "1"
    printf 'BANSOKO_DATABASE_URL=%q\n' "$db_url"
    printf 'BANSOKO_QB_ENDPOINT=%q\n' "http://127.0.0.1:${qb_port}"
    printf 'BANSOKO_QB_USERNAME=%q\n' "admin"
    printf 'BANSOKO_QB_PASSWORD=%q\n' "$qb_password"
    printf 'BANSOKO_SKIP_AUTH=%q\n' "0"
    printf 'BANSOKO_SESSION_SECRET=%q\n' "$session_secret"
    printf 'BANSOKO_SECRET_ENCRYPTION_KEY=%q\n' "$encryption_key"
    printf 'BANSOKO_CONFIG_DIR=%q\n' "$config_root"
    printf 'BANSOKO_HARNESS_DOWNLOAD_ROOT=%q\n' "$download_root"
    printf 'BANSOKO_HARNESS_LIBRARY_ROOT=%q\n' "$library_root"
    printf 'BANSOKO_HARNESS_SERVER_PORT=%q\n' "$server_port"
  } > "$env_file"
  chmod 600 "$env_file"

  python3 - "$state_file" "$pg_id" "$qb_id" "$qb_config_root" "$db_port" "$qb_port" \
    "$server_port" "$db_url" "http://127.0.0.1:${qb_port}" "$download_root" \
    "$library_root" "$config_root" "$state_dir" <<'PY'
import json, sys
(path, pg_id, qb_id, qb_config_root, db_port, qb_port, server_port,
 db_url, qb_endpoint, download_root, library_root, config_root, state_dir) = sys.argv[1:]
data = {
    "pg_id": pg_id, "qb_id": qb_id, "qb_config_root": qb_config_root,
    "db_port": int(db_port), "qb_port": int(qb_port), "server_port": int(server_port),
    "db_url": db_url, "qb_endpoint": qb_endpoint,
    "download_root": download_root, "library_root": library_root,
    "config_root": config_root, "state_dir": state_dir,
}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(data, handle, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY
  chmod 600 "$state_file"

  # State is complete; the journal only existed for partial-setup rollback.
  rm -f "$journal_file"
  trap - EXIT

  echo "real-effect-harness up:"
  echo "  state:     $state_file"
  echo "  env:       $env_file (0600; secret values are not printed)"
  echo "  postgres:  127.0.0.1:${db_port}/${db_name} (container ${pg_id})"
  echo "  qB:        http://127.0.0.1:${qb_port} (container ${qb_id})"
  echo "  download:  $download_root"
  echo "  library:   $library_root"
  echo "  server:    start with: source $env_file && go run ./cmd/bansoko-server --listen 127.0.0.1:${server_port}"
}

down() {
  require_safe_state_dir
  local had_state=0 had_journal=0 cleanup_failed=0
  if [[ -f "$state_file" ]]; then
    had_state=1
  fi
  if [[ -f "$journal_file" ]]; then
    had_journal=1
  fi
  if (( had_state == 0 && had_journal == 0 )); then
    die "no state file or rollback journal at $state_dir"
  fi
  if (( had_state == 1 )); then
    local pg_id qb_id download_root library_root config_root qb_config_root
    pg_id="$(json_field pg_id)"
    qb_id="$(json_field qb_id)"
    download_root="$(json_field download_root)"
    library_root="$(json_field library_root)"
    config_root="$(json_field_or_empty config_root)"
    qb_config_root="$(json_field_or_empty qb_config_root)"
    if ! remove_container_verified "$pg_id"; then
      echo "real-effect-harness: postgres container '$pg_id' could not be removed; keeping $state_file for recovery" >&2
      cleanup_failed=1
    fi
    if ! remove_container_verified "$qb_id"; then
      echo "real-effect-harness: qB container '$qb_id' could not be removed; keeping $state_file for recovery" >&2
      cleanup_failed=1
    fi
    local temp_root
    for temp_root in "$download_root" "$library_root" "$config_root" "$qb_config_root"; do
      if [[ -z "$temp_root" ]]; then
        continue
      fi
      if ! is_harness_temp_path "$temp_root"; then
        echo "real-effect-harness: refusing to remove non-harness path '$temp_root'; keeping $state_file for recovery" >&2
        cleanup_failed=1
        continue
      fi
      # Best-effort cleanup: /tmp is sticky (1777), so a container-chowned
      # root dir can only be removed by its owner or root. Leftovers are
      # harmless scratch dirs; they must never fail the teardown phase.
      rm -rf "$temp_root" 2>/dev/null || true
    done
  fi
  # A journal next to (or instead of) a complete state file means a previous
  # up() died mid-setup; consume whatever it recorded.
  if (( had_journal == 1 )); then
    if ! consume_journal; then
      cleanup_failed=1
    fi
  fi
  if (( cleanup_failed != 0 )); then
    echo "real-effect-harness down: cleanup incomplete; recovery evidence kept at $state_dir (fix the cause, then run down again)" >&2
    exit 1
  fi
  remove_credential_files
  echo "real-effect-harness down: containers, temp roots and credential files removed"
}

status() {
  require_safe_state_dir
  if [[ ! -f "$state_file" ]]; then
    if [[ -f "$journal_file" ]]; then
      echo "real-effect-harness: incomplete setup at $state_dir (rollback journal present; run down to clean)" >&2
      exit 1
    fi
    die "no active harness (no state file)"
  fi
  python3 - "$state_file" <<'PY'
import json, sys
s = json.load(open(sys.argv[1], encoding="utf-8"))
print('harness status:')
print('  postgres container:', s['pg_id'])
print('  qB container:      ', s['qb_id'])
# Redacted: host/port/db name only, never credentials.
print('  db url:            ', s['db_url'].split('@')[-1])
print('  qB endpoint:       ', s['qb_endpoint'])
print('  download root:     ', s['download_root'])
print('  library root:      ', s['library_root'])
PY
}

case "$cmd" in
  up) up ;;
  down) down ;;
  status) status ;;
  check) check ;;
  *) echo "usage: real-effect-harness.sh <up|down|status|check>" >&2; exit 2 ;;
esac
