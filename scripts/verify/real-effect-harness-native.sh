#!/usr/bin/env bash
# Real-effect native harness — same scratch environment as real-effect-harness.sh
# but WITHOUT Docker: local PostgreSQL service + native qbittorrent-nox.
#
# Contract is identical to the Docker harness:
#   real-effect-harness-native.sh up|down|status|check
# and it writes the same artifacts:
#   $BANSOKO_HARNESS_STATE_DIR/env        (sourced by test-single-episode-real-chain.sh etc.)
#   $BANSOKO_HARNESS_STATE_DIR/state.json (identity + path mapping)
#
# Host requirements:
#   - PostgreSQL 17 listening on 127.0.0.1:5432, root can run
#     `su postgres -c psql` to create/drop a disposable role + database
#   - qbittorrent-nox installed (Debian: apt-get install qbittorrent-nox)
#   - mediainfo installed (Debian: apt-get install mediainfo); the single-episode
#     chain reads the downloaded file's media facts with it
#   - python3 (PBKDF2 WebUI password seeding via scripts/ci/seed-qb-password.sh)
#
# Never touches production databases, production qB, or user media paths.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cmd="${1:-check}"
state_dir="${BANSOKO_HARNESS_STATE_DIR:-$(mktemp -d /tmp/bansoko-harness.XXXXXX)}"
state_file="$state_dir/state.json"
env_file="$state_dir/env"

require_pg() {
  local pg_client_version pg_server_version_num
  if ! pg_client_version="$(su postgres -c 'psql --version' 2>/dev/null)"; then
    echo "real-effect-harness-native: unable to execute PostgreSQL client as postgres" >&2
    exit 1
  fi
  if [[ "$(printf '%s\n' "$pg_client_version" | awk 'match($0, /[0-9]+[.][0-9]+/) { v=substr($0, RSTART, RLENGTH); sub(/[.].*$/, "", v); print v; exit }')" != "17" ]]; then
    echo "real-effect-harness-native: su postgres psql client must be PostgreSQL major 17: $pg_client_version" >&2
    exit 1
  fi
  if ! pg_server_version_num="$(su postgres -c 'psql -tAc "SHOW server_version_num"' 2>/dev/null)"; then
    echo "real-effect-harness-native: unable to read PostgreSQL server version" >&2
    exit 1
  fi
  if [[ "$(printf '%s\n' "$pg_server_version_num" | awk '{ print int($1 / 10000); exit }')" != "17" ]]; then
    echo "real-effect-harness-native: PostgreSQL server must be major 17: $pg_server_version_num" >&2
    exit 1
  fi
  PG_CLIENT_VERSION="$pg_client_version"
  PG_SERVER_VERSION_NUM="$pg_server_version_num"
}

check() {
  if [[ "${BANSOKO_SKIP_AUTH:-0}" == "1" ]]; then
    echo "real-effect-harness-native: BANSOKO_SKIP_AUTH=1 is forbidden; harness requires auth enabled" >&2
    exit 1
  fi
  for var in BANSOKO_HARNESS_DOWNLOAD_ROOT BANSOKO_HARNESS_LIBRARY_ROOT; do
    val="${!var:-}"
    if [[ -n "$val" ]]; then
      case "$val" in
        /Users/*/bansoko-library|/media/*|/mnt/*|/home/*/mikan-*|/home/*/bansoko-*|/*/Downloads*)
          echo "real-effect-harness-native: path $val looks like a user/production media path" >&2
          exit 1
          ;;
      esac
    fi
  done
  echo "real-effect-harness-native check passed"
}

up() {
  check
  require_pg
  if ! command -v qbittorrent-nox >/dev/null 2>&1; then
    echo "real-effect-harness-native: qbittorrent-nox is required (apt-get install qbittorrent-nox)" >&2
    exit 1
  fi
  mkdir -p "$state_dir"

  local port_lib=$(( 20000 + RANDOM % 20000 ))
  local db_port=5432
  local qb_port=$(( port_lib + 1 ))
  local db_name="bansoko_harness_$RANDOM"
  local db_pass="harness-$RANDOM-$RANDOM"
  local qb_password="qb-$RANDOM-$RANDOM"

  # Disposable role + database on the local PostgreSQL service.
  su postgres -c "psql -c \"CREATE ROLE $db_name LOGIN PASSWORD '$db_pass'\"" >/dev/null
  su postgres -c "createdb -O $db_name $db_name" >/dev/null
  local db_url="postgres://${db_name}:${db_pass}@127.0.0.1:${db_port}/${db_name}?sslmode=disable"

  # Temp download/library roots on the host.
  local download_root="$state_dir/downloads"
  local library_root="$state_dir/library"
  mkdir -p "$download_root" "$library_root"

  # Machine-wide mutex shared with ci-qb-real-chain.sh: the global /downloads
  # symlink must not be re-pointed while another lane owns it. Held until this
  # process exits.
  local downloads_lock=/var/tmp/bansoko-downloads.lock
  if [[ ! -e "$downloads_lock" ]]; then
    ( umask 000; : >>"$downloads_lock" ) 2>/dev/null || true
  fi
  if ! exec 6<"$downloads_lock"; then
    echo "real-effect-harness-native: cannot open $downloads_lock" >&2
    exit 1
  fi
  if ! flock -n 6; then
    echo "real-effect-harness-native: waiting for the /downloads lock held by another lane ..." >&2
    flock 6
  fi
  # Native-box equivalents of the Docker harness container topology:
  # - real-effect fixture torrents use host.docker.internal webseed URLs, so
  #   point that hostname at loopback for this box.
  # - the qB client submits save_path=/downloads (the Docker convention);
  #   on the native box that path must resolve to this run's download root.
  if ! grep -qs 'host.docker.internal' /etc/hosts; then
    printf '127.0.0.1 host.docker.internal\n' >> /etc/hosts
  fi
  if [[ -e /downloads && ! -L /downloads ]]; then
    echo "real-effect-harness-native: /downloads exists as a non-symlink; refusing to replace" >&2
    exit 1
  fi
  ln -sfn "$download_root" /downloads

  # Controlled qbittorrent-nox with a temp config dir. Seed the WebUI admin
  # password BEFORE first start (qB rewrites its config on shutdown, which
  # would discard the seeded hash), then pin the ports/interface.
  #
  # Note: qB 4.4+ stores its config under <profile>/qBittorrent/config/
  # (new layout), NOT <profile>/qBittorrent/qBittorrent.conf (old layout);
  # writing to the old path silently leaves the seeded password unused.
  local qb_config_root="$state_dir/qb"
  local qb_conf_dir="$qb_config_root/qBittorrent/config"
  BANSOKO_QB_PASSWORD="$qb_password" "$root/scripts/ci/seed-qb-password.sh" \
    "$qb_conf_dir/qBittorrent.conf" >/dev/null
  {
    printf '[BitTorrent]\nSession\\Port=%d\nSession\\QueueingSystemEnabled=false\n' "$(( qb_port + 1 ))"
    printf 'Session\\DefaultSavePath=%s\nSession\\TempPath=%s\n\n' "$download_root" "$download_root"
    printf '[Meta]\nMigrationVersion=6\n\n'
    printf '[Network]\nCookies=@Invalid()\n\n'
    printf '[Downloads]\nSavePath=%s\n\n' "$download_root"
    printf '[Preferences]\n'
    printf 'WebUI\\Port=%d\n' "$qb_port"
    printf 'WebUI\\Address=127.0.0.1\n'
    printf 'WebUI\\Username=admin\n'
    grep 'WebUI\\Password_PBKDF2' "$qb_conf_dir/qBittorrent.conf" || true
  } > "$qb_conf_dir/qBittorrent.conf.new"
  mv "$qb_conf_dir/qBittorrent.conf.new" "$qb_conf_dir/qBittorrent.conf"
  chmod 600 "$qb_conf_dir/qBittorrent.conf"

  qbittorrent-nox --profile="$qb_config_root" \
    >"$state_dir/qbittorrent.log" 2>&1 &
  local qb_pid=$!

  # Wait for the WebUI to answer via the same login flow the Go qB client
  # uses (form POST + session cookie). Basic-auth is rejected by qB 4.5.
  local qb_ready=0
  for _ in $(seq 1 60); do
    if curl -fsS -c "$state_dir/qb.cookies" -d "username=admin&password=$qb_password" \
        "http://127.0.0.1:${qb_port}/api/v2/auth/login" 2>/dev/null | grep -q '^Ok\.$'; then
      qb_ready=1
      break
    fi
    if ! kill -0 "$qb_pid" 2>/dev/null; then
      echo "real-effect-harness-native: qbittorrent-nox exited early; log:" >&2
      tail -n 20 "$state_dir/qbittorrent.log" >&2 || true
      exit 1
    fi
    sleep 1
  done
  if [[ "$qb_ready" != "1" ]]; then
    echo "real-effect-harness-native: qB WebUI not ready in 60s; log:" >&2
    tail -n 20 "$state_dir/qbittorrent.log" >&2 || true
    exit 1
  fi

  # Scratch auth-enabled server env (random session secret + encryption key).
  local session_secret="$(openssl rand -hex 32)"
  local encryption_key="$(openssl rand -hex 32)"
  local server_port=$(( qb_port + 1 ))

  cat > "$env_file" <<EOF
BANSOKO_TEST_DATABASE_URL=$db_url
BANSOKO_DATABASE_URL=$db_url
BANSOKO_QB_ENDPOINT=http://127.0.0.1:${qb_port}
BANSOKO_QB_USERNAME=admin
BANSOKO_QB_PASSWORD=$qb_password
BANSOKO_SKIP_AUTH=0
BANSOKO_SESSION_SECRET=$session_secret
BANSOKO_SECRET_ENCRYPTION_KEY=$encryption_key
BANSOKO_HARNESS_DOWNLOAD_ROOT=$download_root
BANSOKO_HARNESS_LIBRARY_ROOT=$library_root
BANSOKO_HARNESS_SERVER_PORT=$server_port
EOF

  cat > "$state_file" <<EOF
{"pg_id":"native-pg","qb_id":"native-qb","pg_client_version":"$PG_CLIENT_VERSION","pg_server_version":"$PG_SERVER_VERSION_NUM","qb_pid":$qb_pid,"db_role":"$db_name","db_name":"$db_name","qb_config_root":"$qb_config_root","db_port":$db_port,"qb_port":$qb_port,"server_port":$server_port,"db_url":"redacted","qb_endpoint":"http://127.0.0.1:${qb_port}","download_root":"$download_root","library_root":"$library_root","state_dir":"$state_dir"}
EOF
  chmod 600 "$env_file" "$state_file"

  echo "real-effect-harness-native up:"
  echo "  state:     $state_file"
  echo "  env:       $env_file (redacted)"
  echo "  postgres:  $db_url (native service, port $db_port, role/db $db_name)"
  echo "  qB:        http://127.0.0.1:${qb_port} (pid $qb_pid)"
  echo "  download:  $download_root"
  echo "  library:   $library_root"
  echo "  server:    start with: source $env_file && go run ./cmd/bansoko-server --listen 127.0.0.1:${server_port}"
}

down() {
  if [[ ! -f "$state_file" ]]; then
    echo "real-effect-harness-native: no state file at $state_file" >&2
    exit 1
  fi
  local qb_pid db_role db_name download_root library_root
  qb_pid="$(python3 -c "import json;print(json.load(open('$state_file'))['qb_pid'])")"
  db_role="$(python3 -c "import json;print(json.load(open('$state_file'))['db_role'])")"
  db_name="$(python3 -c "import json;print(json.load(open('$state_file'))['db_name'])")"
  download_root="$(python3 -c "import json;print(json.load(open('$state_file'))['download_root'])")"
  library_root="$(python3 -c "import json;print(json.load(open('$state_file'))['library_root'])")"
  if [[ -n "$qb_pid" ]] && kill -0 "$qb_pid" 2>/dev/null; then
    kill "$qb_pid" >/dev/null 2>&1 || true
    sleep 1
    kill -9 "$qb_pid" >/dev/null 2>&1 || true
  fi
  su postgres -c "psql -c 'DROP DATABASE IF EXISTS $db_name'" >/dev/null 2>&1 || true
  su postgres -c "psql -c 'DROP ROLE IF EXISTS $db_role'" >/dev/null 2>&1 || true
  # Same machine-wide mutex as up: never race another lane's /downloads claim.
  local downloads_lock=/var/tmp/bansoko-downloads.lock
  if [[ ! -e "$downloads_lock" ]]; then
    ( umask 000; : >>"$downloads_lock" ) 2>/dev/null || true
  fi
  if ! exec 6<"$downloads_lock"; then
    echo "real-effect-harness-native: cannot open $downloads_lock" >&2
    exit 1
  fi
  flock 6
  if [[ -L /downloads ]] && [[ "$(readlink /downloads)" == "$download_root" ]]; then
    rm -f /downloads
  fi
  rm -rf "$download_root" "$library_root"
  echo "real-effect-harness-native down: qB pid $qb_pid stopped, role/db $db_name dropped, temp roots removed"
}

status() {
  if [[ ! -f "$state_file" ]]; then
    echo "real-effect-harness-native: no state file (run up first)" >&2
    exit 1
  fi
  python3 -c "import json; s=json.load(open('$state_file')); print('pg: redacted'); print('pg_client_version:', s['pg_client_version']); print('pg_server_version:', s['pg_server_version']); print('qB:', s['qb_endpoint'], 'pid', s['qb_pid']); print('download:', s['download_root']); print('library:', s['library_root']); print('server port:', s['server_port']); print('state dir:', s['state_dir'])"
}

case "$cmd" in
  up) up ;;
  down) down ;;
  status) status ;;
  check) check ;;
  *)
    echo "usage: $0 [up|down|status|check]" >&2
    exit 2
    ;;
esac
