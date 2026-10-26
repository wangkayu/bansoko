#!/usr/bin/env bash
# Negative-path proof for scripts/verify/filemaintenance-e2e.sh.
#
# The lane's seven scenarios pass on a healthy fixture, which alone cannot show
# that they would notice a broken one. This wrapper runs the lane twice inside
# the same disposable harness family:
#
#   1. default mode, unchanged: rc must be 0 (no regression from the switch);
#   2. BANSOKO_FM_E2E_NEGATIVE=<mode>: one owned placement file is destroyed by
#      the lane itself and rc must be non-zero, with the diagnostic naming the
#      assertion that actually failed.
#
# A run where the negative half passes, or where it fails without the expected
# diagnostic, is a failure of this script: the point is a loud, attributable
# failure, never merely a non-zero exit.
#
# Usage (on the disposable test VM, as the non-root test user):
#   filemaintenance-e2e-negative.sh                 # both halves, all modes
#   filemaintenance-e2e-negative.sh --mode MODE     # one negative mode
#   filemaintenance-e2e-negative.sh --positive-only # regression half only
#   filemaintenance-e2e-negative.sh --self-test     # static checks, no VM work
#
# Modes (BANSOKO_FM_E2E_NEGATIVE):
#   old-source-missing   destroys the superseded placement file the deletion
#                        must verify; the deletion is skipped (loser_missing),
#                        the chain cannot converge and must report that typed
#                        skip.
#   new-placement-missing destroys the file the settled chain left at the new
#                        placement path; the `-f` assertion must name it.
#
# Exit status: 0 both halves behaved as required; 1 an outcome was wrong;
# 2 the request or the environment cannot be honoured (nothing was proven).
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"
lane="$root/scripts/verify/filemaintenance-e2e.sh"
self="$root/scripts/verify/filemaintenance-e2e-negative.sh"
all_modes=(old-source-missing new-placement-missing)

usage_die() { echo "filemaintenance-e2e-negative: $*" >&2; exit 2; }

mode=""
positive_only=0
self_test=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) [[ $# -ge 2 ]] || usage_die "--mode needs a value"; mode="$2"; shift 2 ;;
    --mode=*) mode="${1#--mode=}"; shift ;;
    --positive-only) positive_only=1; shift ;;
    --self-test) self_test=1; shift ;;
    -h | --help) sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_die "unknown argument: $1 (see --help)" ;;
  esac
done

# The lane itself refuses on Darwin; refuse here first so the message names this
# script and no partial run is attempted.
static_checks() {
  local failed=0 pattern
  [[ -f "$lane" ]] || { echo "self-test: missing $lane" >&2; return 1; }
  bash -n "$lane" || { echo "self-test: $lane has a syntax error" >&2; failed=1; }
  bash -n "$self" || { echo "self-test: $self has a syntax error" >&2; failed=1; }
  # The switch must be parsed and every supported mode handled, or the negative
  # half would silently run the default path and look like a pass.
  for pattern in 'BANSOKO_FM_E2E_NEGATIVE' 'old-source-missing' 'new-placement-missing' 'negative_inject'; do
    if ! grep -qF -- "$pattern" "$lane"; then
      echo "self-test: $lane no longer mentions $pattern" >&2
      failed=1
    fi
  done
  # Scenario 8 is the scan-alignment recovery acceptance case. It must exist as
  # its own guarded block, be driven only by the explicit selector, and must
  # assert the ownership invariant the recovery confirm exists to protect.
  # Without these, a future edit could drop the scenario silently and the lane
  # would still print PASSED.
  for pattern in 'if scenario_requested 8; then' 's8_candidate_target' \
    'ANIME_FILES_RECOVER_REFUSED' 'source_evidence_type' 's8_replay_flag'; do
    if ! grep -qF -- "$pattern" "$lane"; then
      echo "self-test: $lane no longer contains the scenario 8 assertion $pattern" >&2
      failed=1
    fi
  done
  # The default run must not destroy anything: the switch has to gate the call.
  # shellcheck disable=SC2016 # the single quotes are the searched literal
  if ! grep -qF 'if [[ "$negative_mode" == old-source-missing ]]' "$lane"; then
    echo "self-test: the old-source-missing injection is not gated by the switch" >&2
    failed=1
  fi
  # shellcheck disable=SC2016 # the single quotes are the searched literal
  if ! grep -qF 'if [[ "$negative_mode" == new-placement-missing ]]' "$lane"; then
    echo "self-test: the new-placement-missing injection is not gated by the switch" >&2
    failed=1
  fi
  # The old-source-missing mode is only attributable because the lane reads the
  # cleanup owner's terminal `skipped` state: without it the run would wait out
  # the whole deadline and report a timeout instead of the typed reason.
  if ! grep -qF "reason,'') || ' (state=skipped)'" "$lane"; then
    echo "self-test: $lane no longer reads the terminal skipped cleanup state, so old-source-missing can only time out" >&2
    failed=1
  fi
  # Scenario 8 (scan-alignment recovery) must stay reachable AND stay out of
  # the default set. The selector's own guard lines are lifted out of the lane
  # and evaluated, so the check measures the shipped code rather than a copy:
  # the validation must accept "8", and the `all` alias must still expand to
  # the seven lanes the default green claim is made of.
  local selector_source
  # The block runs from the selector assignment through the `fi` that closes
  # its own `if`; the lane's remaining selector lines are not part of this
  # contract.
  selector_source="$(awk '
    /^scenario_selector="\$\{BANSOKO_FM_E2E_SCENARIOS:-\}"$/ { inside = 1 }
    inside && /^fi$/ { print; exit }
    inside { print }
  ' "$lane")"
  if [[ -z "$selector_source" ]]; then
    echo "self-test: cannot locate the scenario selector guard block in $lane" >&2
    failed=1
  else
    for probe in 'all:1,2,3,4,5,6,7' '8:8'; do
      local want="${probe#*:}" got
      got="$(BANSOKO_FM_E2E_SCENARIOS="${probe%%:*}" bash -c "$selector_source"$'\nprintf %s "$scenario_selector"')"
      if [[ "$got" != "$want" ]]; then
        echo "self-test: BANSOKO_FM_E2E_SCENARIOS=${probe%%:*} expands to [$got], want [$want]" >&2
        failed=1
      fi
    done
  fi
  if (( failed != 0 )); then return 1; fi
  echo "self-test: bash -n clean and the negative switch contract is intact ($(printf '%s ' "${all_modes[@]}"))"
}


if [[ -n "$mode" ]]; then
  case " ${all_modes[*]} " in
    *" $mode "*) ;;
    *) usage_die "unknown --mode $mode (expected one of: ${all_modes[*]})" ;;
  esac
fi

# Each half gets its own lane run and its own evidence directory. The lane's
# durable evidence already carries its own run id; this wrapper records the rc
# and the matched diagnostic beside it so the outcome is auditable later.
# Assigned here, created after the self-test: a static check must not leave a
# directory that looks like recorded evidence.
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
evidence_root="${BANSOKO_FM_E2E_NEGATIVE_EVIDENCE_DIR:-/var/tmp/bansoko-fm-e2e-negative-$run_id}"

# A lane run must never be evaluated through a pipe: `tee` would report its own
# status and swallow the lane's. Redirect to a file, keep the real exit status,
# and only then print the tail.
run_half() {
  local label="$1" out rc
  out="$evidence_root/$label.log"
  shift
  echo "== lane run: $label =="
  set +e
  "$@" >"$out" 2>&1
  rc=$?
  set -e
  echo "$rc" >"$evidence_root/$label.rc"
  echo "run $label: rc=$rc (evidence $out)"
  tail -n 25 "$out" | sed 's/^/  /'
  return "$rc"
}

# The lane's environment is whatever the caller already set (its VM session
# exports HOME/GOCACHE/GOMODCACHE/TMPDIR), minus the two variables this script
# owns: every half pins both, so a caller's BANSOKO_FM_E2E_SCENARIOS can never
# narrow a run down to a subset that would then be read as the default green.
#
#   positive  : no negative switch, full default scenario set (the lane's own
#               "all" alias), i.e. exactly the seven-scenario behaviour.
#   negative  : one negative mode, no scenario selector (the lane refuses the
#               combination anyway; clearing it here keeps one source of truth).
lane_env() {
  local selected="${1:-}" scenarios="${2:-}"
  if [[ -n "$selected" ]]; then
    env BANSOKO_FM_E2E_NEGATIVE="$selected" BANSOKO_FM_E2E_SCENARIOS= bash "$lane"
  else
    env BANSOKO_FM_E2E_NEGATIVE= BANSOKO_FM_E2E_SCENARIOS="$scenarios" bash "$lane"
  fi
}

# The one entry point the wrapper runs, parameterised by half. Both halves go
# through it so the isolation probe can drive exactly the same code path the
# real runs use, and a future edit that drops a pin anywhere on that path fails
# the self-test.
half_lane() {
  local half="$1"
  shift
  case "$half" in
    positive) lane_env '' all ;;
    negative) lane_env "$1" '' ;;
    *) return 2 ;;
  esac
}

# Proves the isolation above behaviourally, so a future edit that drops either
# pin fails here instead of silently reporting a narrowed subset as the default
# green. A stub lane records what each half actually receives while the caller's
# own environment already carries a narrowing selector.
isolation_probe() {
  local probe real_lane="$lane" positive_seen negative_seen rc=0
  probe="$(mktemp -d)"
  cat >"$probe/fake-lane" <<'FAKE'
#!/usr/bin/env bash
printf 'scenarios=[%s] negative=[%s]\n' "${BANSOKO_FM_E2E_SCENARIOS-<unset>}" "${BANSOKO_FM_E2E_NEGATIVE-<unset>}"
FAKE
  chmod +x "$probe/fake-lane"
  lane="$probe/fake-lane"
  # Drive the same entry points the real halves use, so dropping either pin in
  # the invocation (not just in lane_env) is caught here.
  positive_seen="$(BANSOKO_FM_E2E_SCENARIOS=5 BANSOKO_FM_E2E_NEGATIVE=bogus half_lane positive)"
  negative_seen="$(BANSOKO_FM_E2E_SCENARIOS=5 BANSOKO_FM_E2E_NEGATIVE=bogus half_lane negative old-source-missing)"
  lane="$real_lane"
  rm -rf "$probe"
  if [[ "$positive_seen" != 'scenarios=[all] negative=[]' ]]; then
    echo "self-test: positive half is not isolated from the caller environment: $positive_seen" >&2
    rc=1
  fi
  if [[ "$negative_seen" != 'scenarios=[] negative=[old-source-missing]' ]]; then
    echo "self-test: negative half is not isolated from the caller environment: $negative_seen" >&2
    rc=1
  fi
  # The run sites must consume those entry points. Checked by parsing the
  # invocation lines out of this script -- a plain literal search would be
  # satisfied by the search's own pattern text.
  local calls
  calls="$(grep -nE '^[[:space:]]*(if )?run_half ' "$self")"
  if ! grep -q 'if run_half positive half_lane positive; then' <<<"$calls"; then
    echo 'self-test: the positive run site no longer routes through half_lane' >&2
    rc=1
  fi
  # shellcheck disable=SC2016 # the single quotes are the searched literal
  if ! grep -q 'run_half "\$label" half_lane negative "\$selected"' <<<"$calls"; then
    echo 'self-test: the negative run site no longer routes through half_lane' >&2
    rc=1
  fi
  (( rc == 0 )) && echo 'self-test: both halves pin their own switch and scenario selector'
  return "$rc"
}

# Expected diagnostic per mode: the assertion line the run must actually print.
expected_diagnostic() {
  case "$1" in
    old-source-missing) printf '%s\n' 'cleanup skipped: loser_missing' ;;
    new-placement-missing) printf '%s\n' 'new placement file missing:' ;;
    *) return 1 ;;
  esac
}

# Self-test runs last so it can call every helper it checks (the definitions
# above must exist, and function order in bash is not hoisted).
if (( self_test == 1 )); then
  static_checks
  isolation_probe
  echo 'self-test passed: static and environment-isolation checks only, no VM evidence claimed'
  exit 0
fi

# The lane itself refuses on Darwin, so refuse here before any run starts: a
# half-run would leave evidence that looks like a negative result.
[[ "$(uname -s)" != Darwin ]] || usage_die 'the negative lane runs only on the disposable test VM (Darwin is refused by the lane itself)'
mkdir -m 700 "$evidence_root"
echo "negative evidence dir: $evidence_root"

failures=0

if (( positive_only == 0 )); then
  # 1. No regression: the default run still passes. The negative switch is pinned
  # empty and the scenario selector is pinned to the lane's full default set, so
  # an inherited selector cannot make a subset look like the seven-scenario
  # green this half claims. A failure here is a lane defect, not a negative-path
  # result.
  if run_half positive half_lane positive; then
    if ! grep -qF 'filemaintenance E2E PASSED' "$evidence_root/positive.log"; then
      echo 'positive run exited 0 without printing filemaintenance E2E PASSED' >&2
      failures=$((failures + 1))
    fi
    # The claim is the FULL lane, not whichever subset it happened to run.
    for scenario in 1 2 3 4 5 6 7; do
      if ! grep -qF "\"scenario\":$scenario" "$evidence_root/positive.log"; then
        echo "positive run exited 0 without recording scenario $scenario; the default seven-scenario lane did not run in full" >&2
        failures=$((failures + 1))
      fi
    done
  else
    echo 'positive run failed: the default lane regressed, so nothing is proven about the negative path' >&2
    failures=$((failures + 1))
  fi
fi

if [[ -n "$mode" ]]; then
  modes=("$mode")
else
  modes=("${all_modes[@]}")
fi

for selected in "${modes[@]}"; do
  label="negative-$selected"
  expected="$(expected_diagnostic "$selected")"
  rc=0
  run_half "$label" half_lane negative "$selected" || rc=$?
  if (( rc == 0 )); then
    echo "negative mode $selected PASSED the lane: a destroyed placement file must fail the run" >&2
    failures=$((failures + 1))
    continue
  fi
  if (( rc == 2 )); then
    echo "negative mode $selected exited 2: the lane rejected its configuration instead of failing on the fixture" >&2
    failures=$((failures + 1))
    continue
  fi
  if ! grep -qF "negative mode $selected: destroyed " "$evidence_root/$label.log"; then
    echo "negative mode $selected failed without recording the file it destroyed; the failure is not attributable" >&2
    failures=$((failures + 1))
    continue
  fi
  if ! grep -qF -- "$expected" "$evidence_root/$label.log"; then
    echo "negative mode $selected failed for an unexpected reason; wanted: $expected" >&2
    echo "--- last 40 lines of $label ---" >&2
    tail -n 40 "$evidence_root/$label.log" >&2
    failures=$((failures + 1))
    continue
  fi
  echo "negative mode $selected rejected loudly: rc=$rc, diagnostic: $expected"
  grep -F -- "$expected" "$evidence_root/$label.log" | head -n 3 | sed 's/^/  /'
done

if (( failures > 0 )); then
  echo "filemaintenance E2E negative proof FAILED ($failures outcome(s) wrong); evidence $evidence_root" >&2
  exit 1
fi
echo "filemaintenance E2E negative proof PASSED: default run green, destroyed-fixture runs rejected with their own diagnostic; evidence $evidence_root"
