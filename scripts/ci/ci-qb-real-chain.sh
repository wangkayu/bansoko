#!/usr/bin/env bash
# qB contract lane on the self-hosted runner: disposable PostgreSQL service plus a native,
# short-lived qBittorrent process sharing a temporary download root with Go.
#
# This lane runs the shared-harness real-chain set: every test that needs one
# qB + one filesystem root and can share that harness serially. It proves the
# qB submit/sync contract, the application handoff, the artifact/file-effect
# chain, filesystem fault handling and the reconciliation/alignment loop. The
# VM real-effect lane remains authoritative for deployment and media evidence.
#
# The required set is the FINAL-109 shared-harness target list plus the
# reconciliation chain (covered only by an ungated supplementary pass there, so
# its exact-revision subscribe fix adds it to this required lane). Scenarios
# that need their own isolated fresh qB harness (cross-owner adoption,
# ambiguous-hash fail-closed, retirement recovery) are deliberately NOT here:
# they run as separate harnesses in the VM lane, not in one shared lane.
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

: "${BANSOKO_TEST_DATABASE_URL:?set BANSOKO_TEST_DATABASE_URL to the CI PostgreSQL service}"
if [[ "${BANSOKO_TEST_DB_DISPOSABLE:-}" != "1" ]]; then
  echo "ci-qb-real-chain: BANSOKO_TEST_DB_DISPOSABLE=1 is required" >&2
  exit 1
fi

"$root/scripts/ci/require-disposable-db.sh"
go run ./cmd/disposable-db-check inventory --expect-file "$root/scripts/ci/postgres-packages.txt"

# Segregated runtime role, exactly as test-integration.sh: application code under test
# runs with the real runtime privilege boundary (ResetDisposableSchema returns
# the runtime handle); migration/reset and fixture arrangement keep the owner
# DSN (disposabledb.Owner). The random password is generated in-process and
# never printed.
if [[ -z "${BANSOKO_TEST_RUNTIME_DATABASE_URL:-}" ]]; then
  runtime_user="bansoko_test_runtime"
  runtime_pass="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  BANSOKO_TEST_DB_RUNTIME_USER="$runtime_user" \
  BANSOKO_TEST_DB_RUNTIME_PASSWORD="$runtime_pass" \
    go run "$root/cmd/disposable-db-check" provision-runtime
  dsn_prefix="${BANSOKO_TEST_DATABASE_URL%%://*}"
  dsn_rest="${BANSOKO_TEST_DATABASE_URL#*://}"
  dsn_tail="${dsn_rest##*@}"
  export BANSOKO_TEST_RUNTIME_DATABASE_URL="${dsn_prefix}://${runtime_user}:${runtime_pass}@${dsn_tail}"
  unset runtime_user runtime_pass
fi

artifact_root="$root/.local-artifacts/ci-qb-real-chain"
run_dir="$artifact_root/$(date -u +%Y%m%d-%H%M%S)-$$"
mkdir -p "$run_dir"
qb_log="$run_dir/qbittorrent.log"
go_test_log="$run_dir/go-test.log"
summary="$run_dir/summary.txt"

download_root="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-qb-ci-downloads.XXXXXX")"
qb_config_root="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-qb-ci-config.XXXXXX")"
qb_conf_dir="$qb_config_root/qBittorrent/config"
qb_password="qb-ci-$(openssl rand -hex 16)"
qb_port="$(python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
qb_pid=""
remote_root=/downloads
created_download_link=0

cleanup() {
  local rc=$?
  trap - EXIT
  if [[ -n "$qb_pid" ]] && kill -0 "$qb_pid" 2>/dev/null; then
    kill "$qb_pid" >/dev/null 2>&1 || true
    for _ in $(seq 1 10); do
      kill -0 "$qb_pid" 2>/dev/null || break
      sleep 1
    done
    kill -9 "$qb_pid" >/dev/null 2>&1 || true
  fi
  if [[ "$created_download_link" == "1" ]] && [[ -L "$remote_root" ]] && [[ "$(readlink "$remote_root")" == "$download_root" ]]; then
    if [[ -w / ]]; then rm -f "$remote_root"; else sudo -n rm -f "$remote_root"; fi
  fi
  rm -rf "$download_root" "$qb_config_root"
  if [[ "$rc" == "0" ]]; then
    printf 'ci-qb-real-chain: passed; evidence=%s\n' "$run_dir"
  else
    printf 'ci-qb-real-chain: failed; evidence=%s\n' "$run_dir" >&2
  fi
  exit "$rc"
}
trap cleanup EXIT
# A cancelled job arrives as INT/TERM and would otherwise skip the EXIT trap and
# strand /downloads; turn both into a normal exit so cleanup always runs.
trap 'exit 130' INT
trap 'exit 143' TERM

if ! command -v qbittorrent-nox >/dev/null 2>&1; then
  echo "ci-qb-real-chain: qbittorrent-nox is required" >&2
  exit 1
fi
# The single-episode chain downloads a real Matroska file and requires the real
# mediainfo binary to read its resolution, codecs and container (the runtime image
# installs the same Alpine package). A missing tool fails the lane; it never
# skips the media facts proof.
if ! command -v mediainfo >/dev/null 2>&1; then
  echo "ci-qb-real-chain: mediainfo is required" >&2
  exit 1
fi
# The global /downloads symlink is a machine-wide resource: with two gate
# slots, two qb lanes could otherwise claim or steal it concurrently.
# Serialize every lane that touches it on this lock (blocking wait; the
# kernel releases it when this process exits, after the EXIT trap has
# removed the link).
downloads_lock=/var/tmp/bansoko-downloads.lock
if [[ ! -e "$downloads_lock" ]]; then
  ( umask 000; : >>"$downloads_lock" ) 2>/dev/null || true
fi
if ! exec 6<"$downloads_lock"; then
  echo "ci-qb-real-chain: cannot open $downloads_lock" >&2
  exit 1
fi
if ! flock -n 6; then
  echo "ci-qb-real-chain: waiting for the /downloads lock held by another lane ..." >&2
  flock 6
fi
if [[ -e "$remote_root" && ! -L "$remote_root" ]]; then
  echo "ci-qb-real-chain: $remote_root exists as a non-symlink; refusing to replace it" >&2
  exit 1
fi
if [[ -L "$remote_root" ]]; then
  # The lock is held, so no other lane can be alive: a link pointing anywhere
  # other than this run's root is debris from a run that died before its EXIT
  # trap could remove it (SIGKILL, or a runner restart). Reclaim only the
  # lane's own link shape; a link someone else put there still fails closed.
  existing="$(readlink "$remote_root")"
  if [[ "$existing" != "$download_root" ]]; then
    if [[ "$existing" != "${TMPDIR:-/tmp}"/bansoko-qb-ci-downloads.* ]]; then
      echo "ci-qb-real-chain: existing $remote_root symlink is not owned by this lane" >&2
      exit 1
    fi
    echo "ci-qb-real-chain: reclaiming the stale $remote_root symlink of an interrupted run" >&2
    if [[ -w / ]]; then rm -f "$remote_root"; else sudo -n rm -f "$remote_root"; fi
    rm -rf "$existing"
  fi
fi
if [[ ! -L "$remote_root" ]]; then
  # Host-runner tests remain non-root; elevate only this guarded link setup.
  if [[ -w / ]]; then ln -s "$download_root" "$remote_root"; else sudo -n ln -s "$download_root" "$remote_root"; fi
  created_download_link=1
fi

BANSOKO_QB_PASSWORD="$qb_password" "$root/scripts/ci/seed-qb-password.sh" \
  "$qb_conf_dir/qBittorrent.conf" >/dev/null
{
  printf '[BitTorrent]\nSession\\Port=%d\nSession\\QueueingSystemEnabled=false\n' "$((qb_port + 1))"
  printf 'Session\\DefaultSavePath=%s\nSession\\TempPath=%s\n\n' "$remote_root" "$remote_root"
  printf '[Meta]\nMigrationVersion=6\n\n'
  printf '[Network]\nCookies=@Invalid()\n\n'
  printf '[Downloads]\nSavePath=%s\n\n' "$remote_root"
  printf '[Preferences]\nWebUI\\Port=%d\n' "$qb_port"
  printf 'WebUI\\Address=127.0.0.1\nWebUI\\Username=admin\n'
  grep 'WebUI\\Password_PBKDF2' "$qb_conf_dir/qBittorrent.conf"
} > "$qb_conf_dir/qBittorrent.conf.new"
mv "$qb_conf_dir/qBittorrent.conf.new" "$qb_conf_dir/qBittorrent.conf"
chmod 600 "$qb_conf_dir/qBittorrent.conf"

qbittorrent-nox --profile="$qb_config_root" >"$qb_log" 2>&1 &
qb_pid=$!
qb_endpoint="http://127.0.0.1:${qb_port}"
qb_cookie="$run_dir/qb.cookies"
qb_ready=0
for _ in $(seq 1 60); do
  if curl -fsS -c "$qb_cookie" -d "username=admin&password=$qb_password" \
      "$qb_endpoint/api/v2/auth/login" 2>/dev/null | grep -q '^Ok\.$'; then
    qb_ready=1
    break
  fi
  if ! kill -0 "$qb_pid" 2>/dev/null; then
    echo "ci-qb-real-chain: qbittorrent-nox exited before WebUI became ready" >&2
    break
  fi
  sleep 1
done
if [[ "$qb_ready" != "1" ]]; then
  echo "ci-qb-real-chain: qB WebUI did not become ready; log follows" >&2
  tail -n 40 "$qb_log" >&2 || true
  exit 1
fi

export BANSOKO_QB_ENDPOINT="$qb_endpoint"
# The qbittorrent-nox above is this lane's own throwaway instance; tests may
# delete its tasks with data only when this marker is set.
export BANSOKO_QB_LANE_DISPOSABLE=1
export BANSOKO_QB_USERNAME=admin
export BANSOKO_QB_PASSWORD="$qb_password"
export BANSOKO_HARNESS_DOWNLOAD_ROOT="$download_root"
if ! grep -Eq '(^|[[:space:]])v2-fixture\.test([[:space:]]|$)' /etc/hosts; then
  printf '127.0.0.1 v2-fixture.test\n' >> /etc/hosts
fi
export BANSOKO_REAL_CHAIN_FIXTURE_HOST=v2-fixture.test

# One shared qB + filesystem harness, run serially. Order follows the FINAL-109
# shared-harness report; each test resets the disposable schema itself.
required_tests=(
  TestSingleEpisodeRealPostgresQBRSSFileOperation
  TestMultiFileRealPostgresChain
  TestMovieBindingRealChainPostgres
  TestOneShotMovieQBRealChain
  TestOneShotQBRealChain
  TestOneShotQBConcurrentSameKeyAdmitsOnce
  TestReplacementRealChainActiveAndStopped
  TestReplacementRealChainBatchReplacesPerFile
  TestReplacementFallbackOnDownloaderRemovalRealQB
  TestReplacementFallbackAfterSuccessWashesRealQB
  TestMultiEpisodeFileRealChain
  TestRealEffectPermissionDeniedSettlesTerminalWithOwnerReason
  TestRealEffectWritableTargetCompletes
  TestRecoveryRealEffectQBIsReadOnlyForCommittedSubmit
  TestReconciliationRealChain
  TestCompanionScanRealChain
  TestCompanionAlignmentRealChain
  TestCompanionAlignmentUnitRealChain
  TestCompanionReplacementCleanupRealChain
  TestManagedOverwriteRealChain
  TestVersionedCompanionRederivesRealChain
  TestLocalCleanupPreservesRealExternalEffects
  TestDownloadCompletionRealQB
)
required_regex="^($(IFS='|'; echo "${required_tests[*]}"))$"

printf '%s\n' \
  "commit=$(git rev-parse HEAD)" \
  "database_host=postgres (CI disposable service)" \
  "database_runtime_role=bansoko_test_runtime (segregated, as test-integration)" \
  "qB_endpoint=$qb_endpoint" \
  "qB_pid=$qb_pid" \
  "qB_remote_download_root=$remote_root" \
  "application_download_root=$download_root" \
  "fixture_host=$BANSOKO_REAL_CHAIN_FIXTURE_HOST" \
  "required_integration_tests=${#required_tests[@]}" \
  "required_integration_test_names=${required_tests[*]}" \
  "teardown=process and temporary roots removed by EXIT trap" \
  > "$summary"

"$root/scripts/ci/go-test-no-skips.sh" qb-real-chain -p 1 ./internal/integration \
  -run "$required_regex" \
  -count=1 -timeout 900s 2>&1 | tee "$go_test_log"

# Every required name must prove it ran. `go test -run` matches an unknown name
# as the empty set instead of failing, so a test that was deleted or renamed
# while its entry stayed in required_tests silently shrinks this lane: nothing
# skips, the skip guard above sees no SKIP event, and the lane still reports its
# full required count. Each required test therefore has to show its own result
# line. The pattern anchors at the start of the line and requires the space and
# parenthesis of a top-level result, so neither a subtest (whose name continues
# with "/" after the parent's) nor a longer name that merely starts with the
# required one can stand in for it — `go test -v` prints subtest results indented
# and this lane's JSON-to-text stream does not, and the anchor holds either way.
missing_tests=()
for name in "${required_tests[@]}"; do
  if ! grep -q "^--- PASS: ${name} (" "$go_test_log"; then
    missing_tests+=("$name")
  fi
done
if (( ${#missing_tests[@]} > 0 )); then
  printf 'ci-qb-real-chain: required tests did not run: %s\n' "${missing_tests[*]}" >&2
  exit 1
fi

echo "ci-qb-real-chain: qB contract lane passed (${#required_tests[@]} required tests)"
