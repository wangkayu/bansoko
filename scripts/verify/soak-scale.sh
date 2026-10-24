#!/usr/bin/env bash
# Scale soak (SCALE-034): library-size and time-span behaviour measured against a
# disposable PostgreSQL 17 container on the test VM.
#
# Runs ON THE VM, as the non-root `test` user, from inside a vm-session repo copy:
#
#   ssh test@<test-host> 'bash -s -- init <name>' < scripts/verify/vm-session.sh
#   scripts/verify/vm-session.sh sync <name> .
#   ssh test@<test-host> 'cd /opt/bansoko/sessions/<name>/repo && \
#       SOAK_PROFILE=M nohup setsid scripts/verify/soak-scale.sh run <name> <label> >/dev/null 2>&1 &'
#
#   soak-scale.sh run  <name> <label>   container up, go test -tags soakscale, evidence, container down
#   soak-scale.sh down <name>           remove this session's soak container (exact name + label)
#
# Never part of `go test ./...`, scripts/ci/test-soak.sh or any release gate: the Go
# code lives behind the `soakscale` build tag (not `soak`, which test-soak.sh
# sweeps) and this script is the only entry point. Unlike a vm-session lane it
# holds EVERY gate slot at once (an exclusive soak; can be held for hours) and
# follows the vm-session
# conventions: the container is `bansoko-sess-<name>-pg`, labelled
# `bansoko.session=<name>`, so `vm-session.sh down <name>` also removes it.
#
# Environment (all optional):
#   SOAK_PROFILE        S (smoke, 2% of M), M (default target profile), H (home profile)
#   SOAK_PHASES         comma list of: dataset,read,worker,retention (default: all)
#   SOAK_IDLE_SECONDS   worker idle window (default 600)
#   SOAK_TIMEOUT        go test timeout (default 20h)
#   SOAK_KEEP_DB=1      leave the container (and its volume) running after the run
#   SOAK_REUSE_DB=1     reuse the container kept by a previous run of this session
#                       (the dataset marker table makes the generator skip loading)
#   SOAK_RUN_PATTERN    go test -run regexp (default: all Scale tests)
#   SOAK_PG_CPUSET      docker run --cpuset-cpus for the soak container (e.g. 0-3)
#   SOAK_PG_CPUS        docker run --cpus for the soak container (e.g. 4)
#   SOAK_PG_MEMORY      docker run --memory for the soak container (e.g. 3800m)
#   SOAK_GO_CPUSET      run go test under taskset -c <list> (e.g. 0-3), so the Go
#                       side (GOMAXPROCS) sees the same cores; with SOAK_PG_CPUSET
#                       set to the same list, a run stays comparable with a
#                       smaller host
#   VM_SESSIONS_ROOT    default /opt/bansoko/sessions
#   VM_GATE_SLOTS       default 2 gate slots; 1 restores the old single-slot
#                       behaviour (a soak then holds the one slot exclusively)
#
# Evidence goes to <session>/evidence/<label>/: run.log, run.rc, results.jsonl,
# summary.txt (timings), <phase>.pgss.txt (pg_stat_statements top 20 per phase),
# tables.txt (pg_stat_user_tables sizes), pglog/ (auto_explain JSON log).
set -euo pipefail
umask 077

SESSIONS_ROOT="${VM_SESSIONS_ROOT:-/opt/bansoko/sessions}"
GATE_LOCK=/var/tmp/bansoko-gate.lock
GATE_LOCK_2=/var/tmp/bansoko-gate.lock.2
MIN_FREE_KIB=$((15 * 1024 * 1024)) # 15 GiB on the root filesystem

die() { echo "soak-scale: $*" >&2; exit 1; }

VM_GATE_SLOTS="${VM_GATE_SLOTS:-2}"
[[ "$VM_GATE_SLOTS" == 1 || "$VM_GATE_SLOTS" == 2 ]] \
  || die "VM_GATE_SLOTS must be 1 or 2 (got: $VM_GATE_SLOTS)"

# An exclusive soak holds every gate slot at once (both by default) so its
# measurements never share the VM with another lane. Slot 1 keeps the
# historical file name shared with vm-session.sh, so old and new scripts
# mixed together still cap the VM at two concurrent lanes. Busy slots are
# waited on with a blocking flock (the kernel hands a freed slot to the first
# waiter, so a soak never starves behind polling lanes); one waiting notice
# is printed in total.
acquire_gate_exclusive() {
  local notice=0
  [[ -e "$GATE_LOCK" ]] || ( umask 000; : >>"$GATE_LOCK" ) 2>/dev/null || die "cannot create $GATE_LOCK"
  if (( VM_GATE_SLOTS == 2 )); then
    [[ -e "$GATE_LOCK_2" ]] || ( umask 000; : >>"$GATE_LOCK_2" ) 2>/dev/null || die "cannot create $GATE_LOCK_2"
  fi
  exec 8<"$GATE_LOCK"
  if (( VM_GATE_SLOTS == 2 )); then exec 7<"$GATE_LOCK_2"; fi
  if ! flock -n 8; then
    echo "waiting for a gate slot ..."
    notice=1
    flock 8
  fi
  if (( VM_GATE_SLOTS == 2 )) && ! flock -n 7; then
    (( notice )) || echo "waiting for a gate slot ..."
    flock 7
  fi
}

validate_name() {
  local name="${1:-}"
  [[ -n "$name" && ${#name} -le 40 && "$name" =~ ^[a-z0-9][a-z0-9-]*$ ]] \
    || die "invalid session name '$name' ([a-z0-9-], at most 40 characters)"
}
validate_label() {
  local label="${1:-}"
  [[ "$label" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$ ]] || die "invalid label '$label'"
}

# The only container this script ever creates, inspects or removes. The name must
# have exactly this shape and the container must carry this session's label, so
# no production or other session's container can match (the production stacks do
# not use the bansoko-sess-<session>-pg shape).
session_container_ok() {
  [[ "$1" =~ ^bansoko-sess-[a-z0-9][a-z0-9-]*-pg$ ]]
}

free_kib() { df -Pk "$1" 2>/dev/null | awk 'NR==2 {print $4}'; }

session_dir() { echo "$SESSIONS_ROOT/$1"; }

require_session() {
  local dir
  validate_name "$1"
  dir="$(session_dir "$1")"
  [[ -d "$dir" && -f "$dir/.bansoko-session" && -d "$dir/repo" ]] \
    || die "session '$1' does not exist (run: vm-session.sh init $1)"
}

preflight() {
  local name="$1" dir missing="" tool kib
  dir="$(session_dir "$name")"
  export PATH="/usr/local/go/bin:/usr/local/bin:$PATH"
  for tool in go docker python3 openssl flock; do
    command -v "$tool" >/dev/null 2>&1 || missing="$missing\n  - command not found: $tool"
  done
  [[ "$(id -u)" != 0 ]] || missing="$missing\n  - running as root: must run as the non-root test user"
  if command -v docker >/dev/null 2>&1 && ! docker info >/dev/null 2>&1; then
    missing="$missing\n  - docker daemon not reachable for user $(id -un)"
  fi
  [[ -f "$dir/repo/go.mod" ]] || missing="$missing\n  - $dir/repo has no go.mod (run: vm-session.sh sync $name .)"
  kib="$(free_kib /)"
  if [[ -z "$kib" || "$kib" -lt "$MIN_FREE_KIB" ]]; then
    missing="$missing\n  - root filesystem has ${kib:-unknown} KiB free, need >= $MIN_FREE_KIB KiB (15 GiB)"
  fi
  if [[ -n "$missing" ]]; then
    printf 'soak-scale: preflight failed; no container was created. Missing:%b\n' "$missing" >&2
    exit 2
  fi
}

container_name() { echo "bansoko-sess-$1-pg"; }

remove_container() {
  local name="$1" cname id cn
  cname="$(container_name "$name")"
  session_container_ok "$cname" || die "refusing container name $cname"
  while read -r id cn; do
    [[ -n "$id" ]] || continue
    if [[ "$cn" == "$cname" ]]; then
      docker rm -fv "$id" >/dev/null && echo "removed $cname"
    fi
  done < <(docker ps -a --filter "label=bansoko.session=$name" --format '{{.ID}} {{.Names}}')
}

cmd_down() {
  [[ $# -eq 1 ]] || die "usage: soak-scale.sh down <name>"
  validate_name "$1"
  command -v docker >/dev/null 2>&1 || die "docker not found"
  remove_container "$1"
}

# Starts the soak PostgreSQL and exports the connection variables. Docker default
# configuration (as deployment/maintainer/docker-compose.production.yml) plus the two
# extensions, test container only; auto_explain writes through the logging
# collector into the data directory so the log survives, is size-rotated and is
# copied out as evidence.
start_database() {
  local name="$1" dir="$2" cname password port runtime_pass dsn_rest
  cname="$(container_name "$name")"
  session_container_ok "$cname" || die "refusing container name $cname"
  if docker container inspect "$cname" >/dev/null 2>&1; then
    die "container $cname already exists: bash scripts/verify/soak-scale.sh down $name (or set SOAK_REUSE_DB=1)"
  fi
  password="$(openssl rand -hex 18)"
  local -a limits=()
  [[ -n "${SOAK_PG_CPUSET:-}" ]] && limits+=(--cpuset-cpus "$SOAK_PG_CPUSET")
  [[ -n "${SOAK_PG_CPUS:-}" ]] && limits+=(--cpus "$SOAK_PG_CPUS")
  [[ -n "${SOAK_PG_MEMORY:-}" ]] && limits+=(--memory "$SOAK_PG_MEMORY")
  POSTGRES_PASSWORD="$password" docker run -d --name "$cname" "${limits[@]}" \
    --label "bansoko.session=$name" --label bansoko.session-owned=true --label bansoko.soak-scale=true \
    -p 127.0.0.1::5432 -e POSTGRES_PASSWORD -e POSTGRES_USER=harness -e POSTGRES_DB=bansoko_ci_disp \
    postgres:17 \
    -c shared_preload_libraries=pg_stat_statements,auto_explain \
    -c pg_stat_statements.track=all -c pg_stat_statements.max=10000 \
    -c auto_explain.log_min_duration=50ms -c auto_explain.log_analyze=on \
    -c auto_explain.log_buffers=on -c auto_explain.log_format=json \
    -c auto_explain.log_nested_statements=on \
    -c logging_collector=on -c log_directory=log -c log_filename=soak-%Y%m%d-%H%M%S.log \
    -c log_rotation_size=512MB -c log_rotation_age=0 >/dev/null
  for _ in $(seq 1 90); do
    docker exec "$cname" pg_isready -U harness -d bansoko_ci_disp >/dev/null 2>&1 && break
    sleep 1
  done
  docker exec "$cname" pg_isready -U harness -d bansoko_ci_disp >/dev/null || die "postgres did not become ready"
  # The extension lives in its own schema: the dataset code never drops it.
  docker exec "$cname" psql -v ON_ERROR_STOP=1 -U harness -d bansoko_ci_disp -c \
    "CREATE SCHEMA soak_ext; CREATE EXTENSION pg_stat_statements SCHEMA soak_ext;" >/dev/null
  port="$(docker port "$cname" 5432/tcp | head -n 1 | awk -F: '{print $NF}')"
  runtime_pass="$(openssl rand -hex 24)"
  {
    printf 'export BANSOKO_SOAK_OWNER_DSN=%q\n' "postgres://harness:$password@127.0.0.1:$port/bansoko_ci_disp?sslmode=disable"
    # Runtime role password is generated here and never printed.
    printf 'export BANSOKO_SOAK_RUNTIME_PASSWORD=%q\n' "$runtime_pass"
  } >"$dir/soak-db.env"
  chmod 600 "$dir/soak-db.env"
  # shellcheck disable=SC1091
  source "$dir/soak-db.env"
  # Runtime login role, exactly as test-integration.sh, through the existing
  # guarded provisioning command (needs the disposable declaration).
  BANSOKO_TEST_DATABASE_URL="$BANSOKO_SOAK_OWNER_DSN" BANSOKO_TEST_DB_DISPOSABLE=1 \
    bash scripts/ci/require-disposable-db.sh
  BANSOKO_TEST_DATABASE_URL="$BANSOKO_SOAK_OWNER_DSN" BANSOKO_TEST_DB_DISPOSABLE=1 \
    BANSOKO_TEST_DB_RUNTIME_USER=bansoko_soak_runtime BANSOKO_TEST_DB_RUNTIME_PASSWORD="$runtime_pass" \
    go run ./cmd/disposable-db-check provision-runtime
  dsn_rest="${BANSOKO_SOAK_OWNER_DSN#*://}"
  printf 'export BANSOKO_SOAK_RUNTIME_DSN=%q\n' "postgres://bansoko_soak_runtime:${runtime_pass}@${dsn_rest##*@}" >>"$dir/soak-db.env"
}

cmd_run() {
  [[ $# -eq 2 ]] || die "usage: soak-scale.sh run <name> <label>"
  local name="$1" label="$2" dir ev cname rc start profile
  require_session "$name"
  validate_label "$label"
  dir="$(session_dir "$name")"
  ev="$dir/evidence/$label"
  cname="$(container_name "$name")"
  profile="${SOAK_PROFILE:-M}"
  [[ "$profile" =~ ^[SMH]$ ]] || die "SOAK_PROFILE must be S, M or H"
  preflight "$name"

  # One run per session at a time (same busy lock as vm-session.sh). The soak
  # is exclusive: it holds every gate slot at once, so no lane can start (or
  # keep running) on the VM while measurements are taken; it can hold the
  # slots for hours.
  exec 9>>"$dir/.busy.lock" || die "cannot open the session lock"
  flock -n 9 || die "session '$name' is busy"
  acquire_gate_exclusive
  mkdir "$ev" 2>/dev/null || die "evidence label '$label' already used in session '$name'"
  cp "$dir/repo/HEAD.txt" "$ev/head.txt" 2>/dev/null || echo unknown >"$ev/head.txt"
  start="$(date +%s)"

  if [[ "${SOAK_KEEP_DB:-}" != 1 ]]; then
    # Expanded now: the trap fires after this function body has finished.
    # shellcheck disable=SC2064
    trap "docker rm -fv '$cname' >/dev/null 2>&1 || true" EXIT
  fi
  export PATH="/usr/local/go/bin:/usr/local/bin:$PATH"
  export GOCACHE="$dir/gocache" GOMODCACHE="$dir/gomodcache" GOTMPDIR="$dir/tmp" TMPDIR="$dir/tmp"
  export GOFLAGS=-buildvcs=false
  cd "$dir/repo"

  set +e
  (
    set -euo pipefail
    if [[ "${SOAK_REUSE_DB:-}" == 1 ]]; then
      docker container inspect "$cname" >/dev/null 2>&1 || die "SOAK_REUSE_DB=1 but $cname does not exist"
      # shellcheck disable=SC1091
      source "$dir/soak-db.env"
    else
      start_database "$name" "$dir"
      # shellcheck disable=SC1091
      source "$dir/soak-db.env"
    fi
    echo "soak postgres: $(docker exec "$cname" psql -U harness -d bansoko_ci_disp -Atc 'select version()')"
    echo "root free before: $(free_kib / ) KiB; profile=$profile phases=${SOAK_PHASES:-all}"
    export BANSOKO_SOAK_DISPOSABLE=1 SOAK_PROFILE="$profile" SOAK_EVIDENCE_DIR="$ev"
    export SOAK_PHASES="${SOAK_PHASES:-dataset,read,worker,retention}"
    local -a pin=()
    [[ -n "${SOAK_GO_CPUSET:-}" ]] && pin=(taskset -c "$SOAK_GO_CPUSET")
    echo "limits: pg cpuset=${SOAK_PG_CPUSET:-none} cpus=${SOAK_PG_CPUS:-none} memory=${SOAK_PG_MEMORY:-none}; go cpuset=${SOAK_GO_CPUSET:-none}; host cpus=$(nproc --all)"
    "${pin[@]}" go test -tags soakscale -p 1 -count=1 -v -timeout "${SOAK_TIMEOUT:-20h}" \
      -run "${SOAK_RUN_PATTERN:-^TestScale}" ./internal/soak/
  ) 2>&1 | tee "$ev/run.log"
  rc="${PIPESTATUS[0]}"
  set -e

  # Evidence that only the database container can give: the auto_explain log and
  # the final table sizes.
  if docker container inspect "$cname" >/dev/null 2>&1; then
    docker cp "$cname:/var/lib/postgresql/data/log" "$ev/pglog" >/dev/null 2>&1 || echo "pg log copy failed" >&2
    docker exec "$cname" psql -U harness -d bansoko_ci_disp -c \
      "SELECT relname, n_live_tup, n_dead_tup, pg_size_pretty(pg_total_relation_size(relid)) AS total, pg_size_pretty(pg_relation_size(relid)) AS heap FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 60" \
      >"$ev/tables.txt" 2>&1 || true
    docker exec "$cname" psql -U harness -d bansoko_ci_disp -Atc "SELECT pg_size_pretty(pg_database_size(current_database()))" \
      >"$ev/database-size.txt" 2>&1 || true
  fi
  echo "$rc" >"$ev/run.rc"
  echo "$(( $(date +%s) - start ))" >"$ev/wall.seconds"
  echo "root free after: $(free_kib /) KiB"
  if [[ "${SOAK_KEEP_DB:-}" == 1 ]]; then
    echo "container $cname kept (SOAK_KEEP_DB=1); remove with: bash scripts/verify/soak-scale.sh down $name"
  fi
  echo "soak $label: rc=$rc, evidence in $ev"
  exit "$rc"
}

main() {
  local cmd="${1:-}"
  [[ -n "$cmd" ]] || die "usage: soak-scale.sh run|down ..."
  shift
  case "$cmd" in
    run) cmd_run "$@" ;;
    down) cmd_down "$@" ;;
    *) die "unknown subcommand '$cmd'" ;;
  esac
}

main "$@"
