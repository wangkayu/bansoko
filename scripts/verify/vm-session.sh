#!/usr/bin/env bash
# Disposable VM verification sessions (TEST-032).
#
# One session = one fresh directory /opt/bansoko/sessions/<name>/ holding its
# own repo copy, Go caches, tmp and evidence, plus containers named and labelled
# after the session. Sessions share only the gate lock slots (at most two lanes
# run at once; see VM_GATE_SLOTS below), so one failed attempt never pollutes
# the next: make a new session.
#
# Runs ON THE VM (as the non-root `test` user), except `sync`, which runs on the
# dev machine and reaches the VM over ssh.
#
#   vm-session.sh init   <name>
#   vm-session.sh sync   <name> <local-tree>            (dev machine)
#   vm-session.sh run    <name> <label> lane postgres|qb
#   vm-session.sh run    <name> <label> pkg <pkgs...>
#   vm-session.sh run    <name> <label> focus <regex> <pkg>
#   vm-session.sh chain  <name>                         (qb0, postgres, qb; detached;
#                                                        a rerun picks fresh -rN labels)
#   vm-session.sh status <name>
#   vm-session.sh down   <name> [--purge] [--force]
#   vm-session.sh leaks                                 (read-only)
#
# <name>  : [a-z0-9-], at most 40 characters, not starting with "-".
# <label> : evidence label, [A-Za-z0-9._-], at most 40 characters. `run` labels are
#           one-shot per session: a reuse is refused (pick another label or init
#           a new session). `chain` needs no labels: each round takes the first
#           free -rN suffix per step (qb0, then qb0-r2, qb0-r3, ...) and prints
#           the labels it uses, so a rerun never reads a previous round's
#           evidence in its failure summary.
#
# Environment (all optional):
#   VM_SESSIONS_ROOT  default /opt/bansoko/sessions
#   VM_SSH_TARGET     required for sync: ssh target of the test VM (user@host)
#                     (other subcommands run on the VM and never leave it)
#   SSHPASS           if set and sshpass is installed, ssh/rsync go through
#                     `sshpass -e`; the value is never printed or put in argv.
#   RSYNC_BIN         default rsync (sync only)
#   VM_GATE_SLOTS     default 2: a lane holds any free gate slot
#                     (/var/tmp/bansoko-gate.lock or .lock.2); 1 restores the old
#                     single-slot serial behaviour. soak-scale.sh holds every
#                     slot at once (exclusive soak runs).
#   VM_CHAIN_KEEP_GOING  default 0: a chain stops at its first failed step
#                     (chain.out ends with "chain stopped after <step> failed");
#                     1 runs every step regardless.
#
# Guarantees:
#   - containers are only ever removed by exact name AND label of the session
#     passed on the command line (see `down`); production containers
#     (bansoko-{app,postgres,migrate,qbittorrent}* and bansoko_*) are never
#     matched, and other sessions are never touched;
#   - every database is a disposable container created by this script or by
#     scripts/ci/ci-host-postgres.sh, behind the existing disposable guards;
#   - this script never changes what a lane or gate decides.
set -euo pipefail

SESSIONS_ROOT="${VM_SESSIONS_ROOT:-/opt/bansoko/sessions}"
# Resolved by `sync` only: `init` is bootstrapped over ssh from the dev machine
# and runs on the VM, where this target is meaningless.
VM_SSH_TARGET="${VM_SSH_TARGET:-}"
GATE_LOCK=/var/tmp/bansoko-gate.lock
GATE_LOCK_2=/var/tmp/bansoko-gate.lock.2
MIN_FREE_KIB=$((4 * 1024 * 1024)) # 4 GiB
# Empty when the script is read from stdin (`ssh vm bash -s -- init <name> <
# vm-session.sh`), which is how a first session is bootstrapped; init does not
# need it, and every later subcommand runs from the session's synced copy at
# <root>/<name>/repo/scripts/verify/vm-session.sh.
SELF=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
fi

die() { echo "vm-session: $*" >&2; exit 1; }
VM_GATE_SLOTS="${VM_GATE_SLOTS:-2}"
[[ "$VM_GATE_SLOTS" == 1 || "$VM_GATE_SLOTS" == 2 ]] \
  || die "VM_GATE_SLOTS must be 1 or 2 (got: $VM_GATE_SLOTS)"
usage_die() {
  if [[ -n "$SELF" ]]; then
    sed -n '2,/^set -euo/p' "$SELF" | sed '$d' | sed 's/^# \{0,1\}//' >&2
  else
    echo "vm-session: bad usage (see the header of scripts/verify/vm-session.sh)" >&2
  fi
  exit 2
}

# ---------------------------------------------------------------- validation --

validate_name() {
  local name="${1:-}"
  [[ -n "$name" ]] || die "missing <name>"
  [[ ${#name} -le 40 ]] || die "invalid session name (more than 40 characters)"
  [[ "$name" =~ ^[a-z0-9][a-z0-9-]*$ ]] \
    || die "invalid session name '$name': only [a-z0-9-] allowed, not starting with '-'"
}

validate_label() {
  local label="${1:-}"
  [[ -n "$label" ]] || die "missing <label>"
  [[ "$label" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$ ]] \
    || die "invalid label '$label': [A-Za-z0-9._-], at most 40 characters, starting alphanumeric"
}

validate_root() {
  [[ "$SESSIONS_ROOT" == /* && "$SESSIONS_ROOT" != "/" ]] || die "VM_SESSIONS_ROOT must be an absolute path other than /"
}

session_dir() { echo "$SESSIONS_ROOT/$1"; }

require_session() {
  local name="$1" dir
  validate_name "$name"
  validate_root
  dir="$(session_dir "$name")"
  [[ -d "$dir" && -f "$dir/.bansoko-session" && -d "$dir/repo" ]] \
    || die "session '$name' does not exist (run: vm-session.sh init $name)"
}

# Production containers are never candidates for anything.
is_protected_name() {
  case "$1" in
    # Production stack (deployment/maintainer/docker-compose.production.yml, project bansoko).
    bansoko-app | bansoko-postgres | bansoko-migrate | bansoko-qbittorrent* | bansoko_*) return 0 ;;
    *) return 1 ;;
  esac
}

# ---------------------------------------------------------------- busy state --

# Self-contained on purpose: `sync` ships this function's source to the VM.
# A session is busy while any run/chain holds an flock on its .busy.lock. The
# kernel drops the lock when the holder dies, and no process-name matching is
# involved (a pgrep pattern can match its own command line).
session_busy() {
  local lock="$1/.busy.lock"
  [ -e "$lock" ] || return 1
  if ( exec 9<"$lock" && flock -n 9 ); then return 1; fi
  return 0
}

acquire_session_lock() {
  local dir="$1" name="$2"
  if [[ "${VM_SESSION_LOCK_HELD:-}" == 1 ]]; then return 0; fi
  exec 9>>"$dir/.busy.lock" || die "cannot open the lock of session '$name'"
  flock -n 9 || die "session '$name' is busy (a run or chain holds its lock)"
}

ensure_gate_lock() {
  # Both slot files are created even under VM_GATE_SLOTS=1: old scripts never
  # look at slot 2, so its mere existence changes nothing for them.
  local gate
  for gate in "$GATE_LOCK" "$GATE_LOCK_2"; do
    if [[ ! -e "$gate" ]]; then
      ( umask 000; : >>"$gate" ) 2>/dev/null || die "cannot create $gate"
    fi
  done
}

# Hold any one free gate slot for the rest of the process. Slot 1 keeps the
# historical file name: sessions still running the old script only know it, so
# old and new scripts mixed together still cap the VM at two concurrent lanes
# (VM_GATE_SLOTS=1 restores the old single-slot behaviour). Both slot fds are
# opened once (fd 8 = slot 1, fd 7 = slot 2); each is tried with flock -n, and
# when every configured slot is busy, one waiting notice is printed and the
# poll (every 5 s) grabs whichever slot frees up first. The winning fd stays
# open, also across the exec of ci-host-postgres.sh, so the lane keeps its
# slot until the run finishes.
acquire_gate_slot() {
  local notified=0
  exec 8<"$GATE_LOCK"
  if (( VM_GATE_SLOTS == 2 )); then exec 7<"$GATE_LOCK_2"; fi
  while :; do
    if flock -n 8; then
      if (( VM_GATE_SLOTS == 2 )); then exec 7<&-; fi
      return 0
    fi
    if (( VM_GATE_SLOTS == 2 )) && flock -n 7; then
      exec 8<&-
      return 0
    fi
    if (( notified == 0 )); then
      echo "waiting for a gate slot ..."
      notified=1
    fi
    sleep 5
  done
}

# ----------------------------------------------------------------- preflight --

free_kib() { df -Pk "$1" 2>/dev/null | awk 'NR==2 {print $4}'; }

# Fails before any container exists, naming everything that is missing.
preflight() {
  local name="$1" dir missing="" tool kib docker_path
  dir="$(session_dir "$name")"
  export PATH="/usr/local/go/bin:/usr/local/bin:$PATH"
  for tool in go docker python3 openssl flock; do
    command -v "$tool" >/dev/null 2>&1 || missing="$missing\n  - command not found: $tool"
  done
  if [[ "$(id -u)" == 0 ]]; then
    missing="$missing\n  - running as root: lanes must run as the non-root test user"
  fi
  if command -v docker >/dev/null 2>&1 && ! docker info >/dev/null 2>&1; then
    missing="$missing\n  - docker daemon not reachable for user $(id -un)"
  fi
  [[ -f "$dir/repo/go.mod" ]] \
    || missing="$missing\n  - $dir/repo has no go.mod (run: vm-session.sh sync $name <local-tree>)"
  kib="$(free_kib "$dir/tmp")"
  if [[ -z "$kib" || "$kib" -lt "$MIN_FREE_KIB" ]]; then
    missing="$missing\n  - session tmp filesystem has ${kib:-unknown} KiB free, need >= $MIN_FREE_KIB KiB (4 GiB): $dir/tmp"
  fi
  docker_path=/var/lib/docker
  kib="$(free_kib "$docker_path")"
  if [[ -z "$kib" ]]; then docker_path=/var/lib; kib="$(free_kib "$docker_path")"; fi
  if [[ -z "$kib" || "$kib" -lt "$MIN_FREE_KIB" ]]; then
    missing="$missing\n  - $docker_path filesystem has ${kib:-unknown} KiB free, need >= $MIN_FREE_KIB KiB (4 GiB)"
  fi
  if [[ -n "$missing" ]]; then
    printf 'vm-session: preflight failed; no container was created. Missing:%b\n' "$missing" >&2
    exit 2
  fi
}

# ---------------------------------------------------------------------- init --

cmd_init() {
  local name="${1:-}" dir
  [[ $# -eq 1 ]] || usage_die
  validate_name "$name"
  validate_root
  dir="$(session_dir "$name")"
  mkdir -p "$SESSIONS_ROOT" || die "cannot create $SESSIONS_ROOT"
  [[ ! -e "$dir" ]] || die "session '$name' already exists: sessions are fresh by design; pick a new name or run: vm-session.sh down $name --purge"
  mkdir "$dir" || die "cannot create $dir (is $SESSIONS_ROOT writable by $(id -un)?)"
  mkdir "$dir/repo" "$dir/gocache" "$dir/gomodcache" "$dir/tmp" "$dir/evidence"
  printf '%s\n' "$name" >"$dir/.bansoko-session"
  echo "session '$name' ready: $dir"
  echo "next: vm-session.sh sync $name <local-tree>   (from the dev machine)"
}

# ---------------------------------------------------------------------- sync --

vm_ssh() {
  if [[ -n "${SSHPASS:-}" ]] && command -v sshpass >/dev/null 2>&1; then
    sshpass -e ssh "$VM_SSH_TARGET" "$@"
  else
    # shellcheck disable=SC2029 # the remote command is meant to expand locally
    ssh "$VM_SSH_TARGET" "$@"
  fi
}

cmd_sync() {
  [[ $# -eq 2 ]] || usage_die
  local name="$1" tree="$2" dir repo state probe head rsh rsync_bin out q_dir
  local target="${VM_SSH_TARGET:?VM_SSH_TARGET must be set to the test VM ssh target (user@host)}"
  validate_name "$name"
  validate_root
  [[ -d "$tree" ]] || die "local tree not found: $tree"
  tree="$(cd "$tree" && pwd)"
  [[ -f "$tree/go.mod" && -d "$tree/internal" && -d "$tree/scripts" ]] \
    || die "$tree does not look like the repository root (go.mod, internal/, scripts/ expected)"
  git -C "$tree" rev-parse --short HEAD >/dev/null 2>&1 || die "$tree is not a git tree: cannot record HEAD.txt"
  dir="$(session_dir "$name")"
  repo="$dir/repo"
  q_dir="$(printf '%q' "$dir")"

  # Refuse while the session is running. The check ships session_busy() to the
  # VM; it locks nothing and matches no process names.
  probe="$(declare -f session_busy)
if [ ! -f $q_dir/.bansoko-session ]; then echo MISSING
elif session_busy $q_dir; then echo BUSY
else echo IDLE
fi"
  state="$(printf '%s\n' "$probe" | vm_ssh bash -s)" || die "ssh to $target failed"
  case "$state" in
    IDLE) ;;
    MISSING) die "session '$name' does not exist on $target (run init there first)" ;;
    BUSY) die "session '$name' is running (a run or chain holds its lock); refusing to sync" ;;
    *) die "unexpected session state from VM: $state" ;;
  esac

  head="$(git -C "$tree" rev-parse --short HEAD)"
  if [[ -n "$(git -C "$tree" status --porcelain 2>/dev/null)" ]]; then head="$head-dirty"; fi

  rsync_bin="${RSYNC_BIN:-rsync}"
  rsh="ssh"
  if [[ -n "${SSHPASS:-}" ]] && command -v sshpass >/dev/null 2>&1; then rsh="sshpass -e ssh"; fi

  # Mandatory excludes: secrets, VM-local state and build output never travel.
  # A leading slash anchors a pattern at the tree root. The bare names match at
  # any depth (.git is a file in a linked worktree, a directory elsewhere).
  # Excluded paths are also protected from --delete on the VM side.
  local excludes=(
    --exclude=.git
    --exclude=.env
    --exclude=owner.env
    --exclude=.tmdb-credentials.env
    --exclude=.ci-credentials.env
    --exclude=node_modules
    --exclude=/frontend/dist
    --exclude=.local-artifacts
    --exclude=/HEAD.txt
    --exclude=.claude
    --exclude=.opencode
    --exclude=/qbittorrent/
    --exclude=/bansoko-downloads/
    --exclude=/bansoko-library/
    --exclude=/autobangumi/
    --exclude=/frontend/playwright-report/
    --exclude=/frontend/test-results/
    --exclude=/frontend/playwright/.auth/
  )

  echo "sync: $tree -> $target:$repo/ (HEAD $head)"
  "$rsync_bin" -rlc --delete "${excludes[@]}" -e "$rsh" "$tree/" "$target:$repo/" \
    || die "rsync failed"

  # HEAD.txt is written on the VM after the copy (it is excluded from rsync).
  printf '%s\n' "$head" | vm_ssh "cat > $q_dir/repo/HEAD.txt" || die "cannot write HEAD.txt"

  # Readability for the non-root test user; ownership only when ssh'd as root.
  vm_ssh "chmod -R u+rwX,go+rX $q_dir/repo && if [ \"\$(id -u)\" = 0 ]; then chown -R test:test $q_dir/repo; fi" \
    || die "cannot fix permissions on the VM copy"

  # No credential file may exist in the VM copy (names only are printed).
  out="$(vm_ssh "find $q_dir/repo \\( -name .env -o -name owner.env -o -name .tmdb-credentials.env -o -name .ci-credentials.env \\) -not -path '*/node_modules/*' 2>/dev/null")" || true
  [[ -z "$out" ]] || die "credential file(s) present in the VM copy, remove them first: $out"

  # Proof: a checksum dry-run with the same excludes must list nothing.
  out="$("$rsync_bin" -rlc -n -i --delete "${excludes[@]}" -e "$rsh" "$tree/" "$target:$repo/")" \
    || die "verification rsync failed"
  if [[ -n "$out" ]]; then
    echo "vm-session: sync verification FAILED; the VM copy differs from $tree:" >&2
    printf '%s\n' "$out" | head -n 20 >&2
    exit 1
  fi
  echo "sync verified: identical to $tree (HEAD $head); checksum dry-run is empty"
}

# ----------------------------------------------------------------------- run --

# Runs `go test` against a session-owned disposable PostgreSQL container.
# Must run inside the gate lock; the container is removed on exit.
pg_go_test() {
  local name="$1" timeout="$2"; shift 2
  local cname="bansoko-sess-${name}-pg" password port runtime_pass dsn_rest
  if docker container inspect "$cname" >/dev/null 2>&1; then
    die "container $cname already exists (a previous run was killed?): run vm-session.sh down $name"
  fi
  password="$(openssl rand -hex 18)"
  # Expanded now: the trap fires after this function has returned.
  # shellcheck disable=SC2064
  trap "docker rm -fv '$cname' >/dev/null 2>&1 || true" EXIT
  POSTGRES_PASSWORD="$password" docker run -d --name "$cname" \
    --label "bansoko.session=$name" --label bansoko.session-owned=true \
    -p 127.0.0.1::5432 -e POSTGRES_PASSWORD -e POSTGRES_USER=harness -e POSTGRES_DB=bansoko_ci_disp \
    postgres:17 >/dev/null
  for _ in $(seq 1 60); do
    docker exec "$cname" pg_isready -h 127.0.0.1 -U harness >/dev/null 2>&1 && break
    sleep 1
  done
  docker exec "$cname" pg_isready -h 127.0.0.1 -U harness >/dev/null
  port="$(docker port "$cname" 5432/tcp | head -n 1 | awk -F: '{print $NF}')"
  export BANSOKO_TEST_DATABASE_URL="postgres://harness:$password@127.0.0.1:$port/bansoko_ci_disp?sslmode=disable"
  export BANSOKO_TEST_DB_DISPOSABLE=1
  bash scripts/ci/require-disposable-db.sh
  # Segregated runtime role, exactly as test-integration.sh; the password is generated
  # here and never printed.
  runtime_pass="$(openssl rand -hex 24)"
  BANSOKO_TEST_DB_RUNTIME_USER=bansoko_test_runtime BANSOKO_TEST_DB_RUNTIME_PASSWORD="$runtime_pass" \
    go run ./cmd/disposable-db-check provision-runtime
  dsn_rest="${BANSOKO_TEST_DATABASE_URL#*://}"
  export BANSOKO_TEST_RUNTIME_DATABASE_URL="postgres://bansoko_test_runtime:${runtime_pass}@${dsn_rest##*@}"
  go test -p 1 -count=1 -timeout "${timeout}s" "$@"
}

cmd_run() {
  [[ $# -ge 3 ]] || usage_die
  local name="$1" label="$2" mode="$3" dir ev start rc arg
  shift 3
  require_session "$name"
  validate_label "$label"
  dir="$(session_dir "$name")"
  ev="$dir/evidence/$label"

  # Validate the mode and its arguments before anything is created.
  case "$mode" in
    lane)
      [[ $# -eq 1 && ( "$1" == postgres || "$1" == qb ) ]] || die "usage: run <name> <label> lane postgres|qb"
      ;;
    pkg)
      [[ $# -ge 1 ]] || die "usage: run <name> <label> pkg <pkgs...>"
      for arg in "$@"; do
        [[ "$arg" =~ ^[A-Za-z0-9./_][A-Za-z0-9./_@+-]*$ ]] || die "invalid package argument: $arg"
      done
      ;;
    focus)
      [[ $# -eq 2 ]] || die "usage: run <name> <label> focus <regex> <pkg>"
      [[ "$2" =~ ^[A-Za-z0-9./_][A-Za-z0-9./_@+-]*$ ]] || die "invalid package argument: $2"
      ;;
    *) die "unknown run mode '$mode' (lane|pkg|focus)" ;;
  esac

  preflight "$name"
  acquire_session_lock "$dir" "$name"
  mkdir "$ev" 2>/dev/null || die "evidence label '$label' already used in session '$name': labels are one-shot for explicit runs; pick another label or init a new session"
  if [[ -f "$dir/repo/HEAD.txt" ]]; then cp "$dir/repo/HEAD.txt" "$ev/head.txt"; else echo unknown >"$ev/head.txt"; fi
  ensure_gate_lock

  start="$(date +%s)"
  set +e
  (
    set -euo pipefail
    export PATH="/usr/local/go/bin:/usr/local/bin:$PATH"
    export GOCACHE="$dir/gocache" GOMODCACHE="$dir/gomodcache" GOTMPDIR="$dir/tmp" TMPDIR="$dir/tmp"
    export GOFLAGS=-buildvcs=false
    cd "$dir/repo"
    case "$mode" in
      lane)
        # scripts/ci/ci-host-postgres.sh owns the container (bansoko-ci-<name>-<lane>-<pid>)
        # and the disposable guards; each lane holds one gate slot and two lanes
        # may run at once (see acquire_gate_slot).
        export GITHUB_RUN_ID="$name"
        acquire_gate_slot
        exec bash scripts/ci/ci-host-postgres.sh "$1"
        ;;
      pkg | focus)
        acquire_gate_slot
        if [[ "$mode" == pkg ]]; then
          pg_go_test "$name" 2400 "$@"
        else
          pg_go_test "$name" 1500 -run "$1" "$2"
        fi
        ;;
    esac
  ) 2>&1 | tee "$ev/run.log"
  rc="${PIPESTATUS[0]}"
  set -e
  echo "$rc" >"$ev/run.rc"
  echo "$(( $(date +%s) - start ))" >"$ev/wall.seconds"
  echo "run $label: rc=$rc, evidence in $ev"
  exit "$rc"
}

# --------------------------------------------------------------------- chain --

# Chain rounds may repeat inside one session. Each step prefers its plain base
# label and, when that evidence directory already exists, the first free -rN
# suffix (qb0-r2, qb0-r3, ...). Explicit `run` labels stay strictly one-shot.
chain_pick_labels() {
  local dir="$1" base label n
  shift
  for base in "$@"; do
    label="$base"
    if [[ -e "$dir/evidence/$label" ]]; then
      n=2
      while [[ -e "$dir/evidence/$base-r$n" ]]; do n=$((n + 1)); done
      label="$base-r$n"
    fi
    echo "$label"
  done
}

cmd_chain() {
  [[ $# -eq 1 ]] || usage_die
  local name="$1" dir
  require_session "$name"
  dir="$(session_dir "$name")"
  # Chain rounds may repeat inside one session: each round picks fresh labels
  # (plain base, then the first free -rN suffix), so a rerun never reads or
  # overwrites a previous round's evidence. Explicit `run` labels stay one-shot.
  if [[ -e "$dir/chain.out" || -e "$dir/chain.done" ]]; then
    echo "note: session '$name' already has chain output; this round picks fresh -rN labels"
  fi
  preflight "$name"
  acquire_session_lock "$dir" "$name"
  # Resolved under the session lock: no concurrent run or chain can create
  # evidence between here and the worker's first step.
  local -a labels
  mapfile -t labels < <(chain_pick_labels "$dir" qb0 postgres qb)
  # A previous round's markers would make a waiter see this round as finished.
  rm -f "$dir/chain.done" "$dir/chain.out"
  [[ -n "$SELF" ]] || die "chain must run from a file: bash $dir/repo/scripts/verify/vm-session.sh chain $name"
  command -v setsid >/dev/null 2>&1 || die "setsid not found"
  # The detached worker inherits the lock (fd 9) and keeps the session busy
  # until it finishes; the kernel releases it even if the worker is killed.
  VM_SESSION_LOCK_HELD=1 nohup setsid bash "$SELF" _chain_worker "$name" "${labels[@]}" \
    >>"$dir/chain.console" 2>&1 </dev/null &
  echo "chain started for session '$name' (labels: ${labels[*]}); follow with: vm-session.sh status $name"
  echo "result: $dir/chain.out, finished marker: $dir/chain.done"
}

chain_step() {
  local name="$1" label="$2" lane="$3" dir ev rc last
  dir="$(session_dir "$name")"
  ev="$dir/evidence/$label"
  set +e
  VM_SESSION_LOCK_HELD=1 bash "$SELF" run "$name" "$label" lane "$lane" >>"$dir/chain.console" 2>&1
  rc=$?
  set -e
  last=""
  if [[ -f "$ev/run.log" ]]; then
    last="$(grep -E 'passed|FAIL' "$ev/run.log" | tail -n 1 || true)"
  fi
  echo "$label rc=$rc ${last:-(no passed/FAIL line)}" >>"$dir/chain.out"
  if (( rc != 0 )) && [[ -f "$ev/run.log" ]]; then
    {
      echo "--- $label failure detail"
      grep -E '^(--- FAIL|FAIL|panic)' -A6 "$ev/run.log" | head -n 60 || true
      echo "--- $label assertion lines (_test.go:N:)"
      # Only the lines logged inside a test that then failed: passing tests
      # log plenty of _test.go lines of their own.
      awk '/^=== RUN/ { buf = "" }
           /_test\.go:[0-9]+:/ { sub(/^[[:space:]]+/, ""); buf = buf $0 "\n" }
           /^[[:space:]]*--- FAIL/ { printf "%s", buf; buf = "" }' "$ev/run.log" \
        | awk '!seen[$0]++' | head -n 40 || true
    } >>"$dir/chain.out"
  fi
  return "$rc"
}

cmd_chain_worker() {
  local name="$1" dir
  require_session "$name"
  dir="$(session_dir "$name")"
  shift
  # `chain` resolves the labels under the session lock and passes them in; a
  # direct worker invocation resolves them here instead. Every summary and
  # failure detail below reads only this round's labels' evidence.
  local -a labels=("$@")
  if (( ${#labels[@]} == 0 )); then
    mapfile -t labels < <(chain_pick_labels "$dir" qb0 postgres qb)
  fi
  # Expanded now: the trap fires after this function has returned.
  # shellcheck disable=SC2064
  trap ": >\"$dir/chain.done\"" EXIT
  {
    echo "chain $name HEAD=$(cat "$dir/repo/HEAD.txt" 2>/dev/null || echo unknown) started $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "labels: ${labels[*]}"
  } >"$dir/chain.out"
  # Fail fast: a failed step stops the chain so it does not keep queueing for
  # the gate. VM_CHAIN_KEEP_GOING=1 runs every step regardless.
  local -a lanes=(qb postgres qb)
  local i
  for (( i = 0; i < ${#lanes[@]}; i++ )); do
    if ! chain_step "$name" "${labels[$i]}" "${lanes[$i]}" && [[ "${VM_CHAIN_KEEP_GOING:-0}" != 1 ]]; then
      echo "chain stopped after ${labels[$i]} failed $(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$dir/chain.out"
      return 0
    fi
  done
  echo "chain finished $(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$dir/chain.out"
}

# -------------------------------------------------------------------- status --

cmd_status() {
  [[ $# -eq 1 ]] || usage_die
  local name="$1" dir ev label rc secs
  require_session "$name"
  dir="$(session_dir "$name")"
  echo "session: $name ($dir)"
  echo "head:    $(cat "$dir/repo/HEAD.txt" 2>/dev/null || echo 'not synced')"
  if session_busy "$dir"; then echo "state:   BUSY (a run or chain holds the session lock)"; else echo "state:   idle"; fi
  if [[ -e "$dir/chain.done" ]]; then echo "chain:   finished"
  elif [[ -e "$dir/chain.out" ]]; then echo "chain:   running or interrupted (no chain.done)"
  else echo "chain:   not started"; fi
  echo "evidence:"
  for ev in "$dir"/evidence/*/; do
    [[ -d "$ev" ]] || continue
    label="$(basename "$ev")"
    rc="$(cat "$ev/run.rc" 2>/dev/null || echo running)"
    secs="$(cat "$ev/wall.seconds" 2>/dev/null || echo -)"
    echo "  $label rc=$rc wall=${secs}s"
  done
  [[ -e "$dir/chain.out" ]] && { echo "chain.out:"; sed 's/^/  /' "$dir/chain.out"; }
  if command -v docker >/dev/null 2>&1; then
    echo "containers (label bansoko.session=$name):"
    docker ps -a --filter "label=bansoko.session=$name" --format '  {{.Names}} {{.Status}}' 2>/dev/null || echo "  (docker not reachable)"
  fi
  echo "free: tmp $(free_kib "$dir/tmp") KiB"
}

# ---------------------------------------------------------------------- down --

cmd_down() {
  [[ $# -ge 1 ]] || usage_die
  local name="$1" purge=0 force=0 dir id cname removed=0 skipped=0
  shift
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --purge) purge=1 ;;
      --force) force=1 ;;
      *) die "unknown option: $1" ;;
    esac
    shift
  done
  validate_name "$name"
  validate_root
  dir="$(session_dir "$name")"
  command -v docker >/dev/null 2>&1 || die "docker not found"
  docker info >/dev/null 2>&1 || die "docker daemon not reachable"
  if [[ -d "$dir" ]] && session_busy "$dir" && (( force == 0 )); then
    die "session '$name' is busy; wait for it to finish or pass --force (containers only; the process is not killed)"
  fi

  # 1. Containers created by `run pkg|focus`: label AND exact name must match.
  while read -r id cname; do
    [[ -n "$id" ]] || continue
    if [[ "$cname" == "bansoko-sess-${name}-pg" ]] && ! is_protected_name "$cname"; then
      docker rm -fv "$id" >/dev/null || die "failed to remove $cname"
      echo "removed $cname"
      removed=$((removed + 1))
    else
      echo "skipped $cname (carries label bansoko.session=$name but not the expected name)" >&2
      skipped=$((skipped + 1))
    fi
  done < <(docker ps -a --filter "label=bansoko.session=$name" --format '{{.ID}} {{.Names}}')

  # 2. Containers of ci-host-postgres.sh lanes: label bansoko.ci-owned=true AND the
  #    exact name shape bansoko-ci-<name>-(postgres|qb)-<digits>.
  while read -r id cname; do
    [[ -n "$id" ]] || continue
    if [[ "$cname" =~ ^bansoko-ci-${name}-(postgres|qb)-[0-9]+$ ]] && ! is_protected_name "$cname"; then
      docker rm -fv "$id" >/dev/null || die "failed to remove $cname"
      echo "removed $cname"
      removed=$((removed + 1))
    fi
  done < <(docker ps -a --filter "label=bansoko.ci-owned=true" --format '{{.ID}} {{.Names}}')

  echo "down '$name': $removed container(s) removed, $skipped skipped"

  if (( purge == 1 )); then
    [[ -d "$dir" ]] || { echo "no session directory to purge"; return 0; }
    [[ ! -L "$dir" && -f "$dir/.bansoko-session" && "$(cat "$dir/.bansoko-session")" == "$name" ]] \
      || die "refusing to purge $dir: not a session directory created by init"
    # The Go module cache is read-only by design, so make the tree writable
    # first. The marker goes last: a purge that fails halfway can be re-run.
    chmod -R u+w -- "$dir" || die "cannot make $dir writable"
    find "$dir" -mindepth 1 -maxdepth 1 ! -name .bansoko-session -exec rm -rf -- {} + \
      || die "purge of $dir failed; re-run: vm-session.sh down $name --purge"
    rm -f -- "$dir/.bansoko-session"
    rmdir -- "$dir"
    echo "purged $dir"
  fi
}

# --------------------------------------------------------------------- leaks --

# Read-only. Reports (never removes):
#   - containers labelled bansoko.session=<x> whose session directory is gone;
#   - containers with a harness-shaped name (bansoko-ci-*, bansoko-sess-*) and no
#     bansoko.session label, unless they belong to a session that is running now
#     (ci-host-postgres.sh lane containers carry only bansoko.ci-owned).
cmd_leaks() {
  [[ $# -eq 0 ]] || usage_die
  validate_root
  command -v docker >/dev/null 2>&1 || die "docker not found"
  docker info >/dev/null 2>&1 || die "docker daemon not reachable"
  local cname label status found=0 owner hint
  while IFS='|' read -r cname label status; do
    [[ -n "$cname" ]] || continue
    is_protected_name "$cname" && continue
    if [[ -n "$label" ]]; then
      if [[ "$label" =~ ^[a-z0-9][a-z0-9-]{0,39}$ && -d "$SESSIONS_ROOT/$label" ]]; then continue; fi
      echo "ORPHAN   $cname ($status): labelled bansoko.session=$label but that session directory is gone"
      found=$((found + 1))
    elif [[ "$cname" =~ ^(bansoko|mikan)-(ci|sess)- ]]; then
      owner=""
      if [[ "$cname" =~ ^bansoko-ci-(.+)-(postgres|qb)-[0-9]+$ ]]; then owner="${BASH_REMATCH[1]}"; fi
      if [[ -n "$owner" && "$owner" =~ ^[a-z0-9][a-z0-9-]{0,39}$ && -d "$SESSIONS_ROOT/$owner" ]] \
        && session_busy "$SESSIONS_ROOT/$owner"; then
        continue
      fi
      hint=""
      if [[ -n "$owner" ]]; then hint=" (name suggests session $owner)"; fi
      echo "UNLABELLED $cname ($status): harness-shaped name without a bansoko.session label$hint"
      found=$((found + 1))
    fi
  done < <(docker ps -a --format '{{.Names}}|{{.Label "bansoko.session"}}|{{.Status}}')
  echo "leaks: $found candidate(s); nothing was removed. Clean up with: vm-session.sh down <name> (own session) or docker rm -fv <name> after checking."
}

# ------------------------------------------------------------------- dispatch --

main() {
  local cmd="${1:-}"
  [[ -n "$cmd" ]] || usage_die
  shift
  case "$cmd" in
    init) cmd_init "$@" ;;
    sync) cmd_sync "$@" ;;
    run) cmd_run "$@" ;;
    chain) cmd_chain "$@" ;;
    _chain_worker) cmd_chain_worker "$@" ;;
    status) cmd_status "$@" ;;
    down) cmd_down "$@" ;;
    leaks) cmd_leaks "$@" ;;
    -h | --help | help) usage_die ;;
    *) echo "vm-session: unknown subcommand '$cmd'" >&2; usage_die ;;
  esac
}

main "$@"
