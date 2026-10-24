#!/usr/bin/env bash
# Minimal isolated embedded-update acceptance runner (AC-B1/B3/B4/B6).
#
# This script is deliberately fail-closed. It starts the real immutable base
# compose when configured, but never manufactures a journal, pointer, schema,
# fence, or application response. Scenario hooks must drive and inspect the
# product chain and must report {"passed":true,"scope":"real"}.
#
# Usage:
#   BANSOKO_EMBEDDED_E2E=1 scripts/verify/embedded-update-e2e.sh --scenario=all
#   BANSOKO_EMBEDDED_E2E=1 ... --scenario=normal
#   BANSOKO_EMBEDDED_E2E=1 ... --scenario=normal --self-test
#
# --self-test is available for normal, migrate-failure, rollback-blocked and
# min-image-version. The normal self-test runs the real cmd/bansoko-bootstrap success
# state-machine tests locally (non-Docker); it does not create Docker, WebUI,
# or process-build evidence. Without --self-test, normal still requires a real
# scenario hook and remains blocked when no hook is configured.
#
# Required for executable scenarios:
#   BANSOKO_EMBEDDED_E2E_BASE_IMAGE_DIGEST=sha256:...
#   BANSOKO_EMBEDDED_E2E_COMPOSE_FILE (defaults to production compose)
#   BANSOKO_EMBEDDED_E2E_<SCENARIO>_CMD, a command which drives/asserts the real
#     scenario. It receives E2E_PROJECT, E2E_WORKDIR, E2E_UPDATES_VOLUME,
#     E2E_COMPOSE_FILE and E2E_EVIDENCE_FILE.
#
# The hook is the only authority for product assertions. Its stdout is saved
# for diagnosis and its JSON evidence is copied only after strict validation.
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"
evidence_dir="${BANSOKO_EMBEDDED_E2E_EVIDENCE_DIR:-${TMPDIR:-/tmp}/bansoko-embedded-update-e2e}"
compose_file="${BANSOKO_EMBEDDED_E2E_COMPOSE_FILE:-$root/deployment/maintainer/docker-compose.production.yml}"
base_digest="${BANSOKO_EMBEDDED_E2E_BASE_IMAGE_DIGEST:-}"
keep="${BANSOKO_EMBEDDED_E2E_KEEP:-0}"
scenario_arg="all"
self_test=0
 docker_fixture=0
for arg in "$@"; do
  case "$arg" in
    --scenario=normal|--scenario=migrate-failure|--scenario=rollback-blocked|--scenario=min-image-version|--scenario=all)
      scenario_arg="${arg#--scenario=}" ;;
    --self-test) self_test=1 ;;
    --docker-fixture) docker_fixture=1 ;;
    --keep) keep=1 ;;
    *) echo "unknown argument: $arg (use --scenario=normal|migrate-failure|rollback-blocked|min-image-version|all [--self-test] [--docker-fixture] [--keep])" >&2; exit 2 ;;
  esac
done

if [[ "$docker_fixture" == 1 ]]; then
  [[ "$scenario_arg" == all ]] && scenario_arg=normal
  case "$scenario_arg" in
    normal|migrate-failure|rollback-blocked) ;;
    *) echo "refusing: unsupported --docker-fixture scenario" >&2; exit 2 ;;
  esac
  exec "$root/scripts/ci/embedded-update-docker-fixture.sh" "$@"
fi

if [[ "$self_test" == 1 ]]; then
  if [[ "${BANSOKO_EMBEDDED_E2E:-0}" != "1" ]]; then
    echo "refusing: BANSOKO_EMBEDDED_E2E=1 is required (isolated non-production guard)" >&2
    exit 2
  fi
  if [[ "${BANSOKO_ENVIRONMENT:-development}" == production ]]; then
    echo "refusing: embedded self-test cannot target production" >&2
    exit 2
  fi
  command -v go >/dev/null 2>&1 || { echo "go required" >&2; exit 2; }
  mkdir -p "$evidence_dir"
  self_test_workdir="$(mktemp -d /tmp/bansoko-ac-b-self-test.XXXXXX)"
  trap 'rm -rf "$self_test_workdir"' EXIT

  # Keep self-test dispatch separate from the Docker path. In particular, do
  # not invoke this script for the aggregate: doing so would recurse forever.
  self_test_one() {
    local scenario="$1" ac test_name assertion_json log
    case "$scenario" in
      normal)
        ac=ac-b1
        test_name='TestConsumePendingMigrationHappyPathSucceedsAndSettles|TestPlanLaunchTerminalStateRunsVerifiedOverlay|TestOverlayIdentityFromJournalOnlyForPromotedRelease|TestOverlayIdentityUsesDurableProvenanceAfterJournalArchive'
        assertion_json='{"migration_succeeded":true,"promotion_succeeded":true,"readiness_succeeded":true,"current_pointer":"rel-b","pointer_schema_identity_verified":true}'
        ;;
      migrate-failure)
        ac=ac-b3
        test_name=TestConsumeMigrationFailureRestoresAndRollsBackToPrevious
        assertion_json='{"migration_failed":true,"database_restored":true,"final_state":"rolled_back","previous_readiness":true}'
        ;;
      rollback-blocked)
        ac=ac-b4
        test_name='TestConsumeRestoreFailureParksRollbackBlocked|TestConsumeRollbackBlockedNeverRetriesOrReleasesFence'
        assertion_json='{"restore_failed":true,"final_state":"rollback_blocked","maintenance_parked":true,"fence_held":true}'
        ;;
      min-image-version)
        ac=ac-b6
        log="$self_test_workdir/$ac.log"
        if BANSOKO_ADMISSION_SELF_TEST=1 BANSOKO_ADMISSION_SELF_TEST_EVIDENCE_FILE="$self_test_workdir/$ac.json" \
            BANSOKO_TEST_DB_DISPOSABLE=1 go test ./internal/update/application \
            -tags admissionselftest -run '^TestRealAdmissionSelfTest$' -count=1 >"$log" 2>&1; then
          python3 - "$self_test_workdir/$ac.json" "$log" <<'PY'
import json, pathlib, sys
out, log = map(pathlib.Path, sys.argv[1:])
data = json.loads(out.read_text())
required = {
    "acceptance": "min-image-version", "scope": "application-real", "passed": True,
    "error_code": "UPDATE_REQUIRES_IMAGE", "journal_effect": False,
    "fence_effect": False, "staging_effect": False,
}
if any(data.get(key) != value for key, value in required.items()):
    raise SystemExit("admission self-test evidence contract mismatch")
data.update({"runner": "embedded-update-e2e.sh", "self_test": True,
             "e2e_mode": "non-docker", "log": str(log)})
out.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
PY
          return 0
        fi
        echo "admission self-test failed or was unavailable" >&2
        return 1
        ;;
      *) echo "unknown self-test scenario: $scenario" >&2; return 2 ;;
    esac

    log="$self_test_workdir/$ac.log"
    if BANSOKO_TEST_DB_DISPOSABLE=1 go test ./cmd/bansoko-bootstrap -run "^(${test_name})$" -count=1 >"$log" 2>&1; then
      python3 - "$self_test_workdir/$ac.json" "$ac" "$test_name" "$log" "$assertion_json" <<'PY'
import json, pathlib, sys
out = pathlib.Path(sys.argv[1])
acceptance, test_name, log, assertion_json = sys.argv[2:]
data = {
    "acceptance": acceptance, "scope": "bootstrap-state-machine-real", "passed": True,
    "self_test": True, "runner": "embedded-update-e2e.sh",
    "go_test": {"package": "./cmd/bansoko-bootstrap", "test_regex": "^(" + test_name + ")$", "exit_code": 0},
    "e2e_mode": "non-docker",
    "description": "Non-Docker E2E evidence from the real cmd/bansoko-bootstrap state-machine path.",
    "assertions": json.loads(assertion_json), "log": str(log),
}
out.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
PY
      return 0
    fi
    echo "AC-${ac#ac-b} bootstrap state-machine self-test failed; no evidence emitted (log: $log)" >&2
    return 1
  }

  # all is a real aggregate: every scenario must pass before any acceptance
  # artifact, including the summary, is published. The order is intentional.
  self_test_scenarios=(normal migrate-failure rollback-blocked min-image-version)
  if [[ "$scenario_arg" != all ]]; then self_test_scenarios=("$scenario_arg"); fi
  for stale in ac-b1 ac-b3 ac-b4 ac-b6; do rm -f "$evidence_dir/$stale.json"; done
  rm -f "$evidence_dir/step7-summary.json"
  for scenario in "${self_test_scenarios[@]}"; do
    self_test_one "$scenario" || exit 1
  done

  if [[ "$scenario_arg" == all ]]; then
    for ac in ac-b1 ac-b3 ac-b4 ac-b6; do
      [[ -s "$self_test_workdir/$ac.json" ]] || { echo "missing self-test result: $ac" >&2; exit 1; }
    done
    for ac in ac-b1 ac-b3 ac-b4 ac-b6; do cp "$self_test_workdir/$ac.json" "$evidence_dir/$ac.json"; done
    python3 - "$self_test_workdir/step7-summary.json" "$evidence_dir" <<'PY'
import json, pathlib, sys
out, evidence_dir = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
names = ("ac-b1.json", "ac-b3.json", "ac-b4.json", "ac-b6.json")
rows = [json.loads((evidence_dir / name).read_text()) for name in names]
if not all(row.get("passed") is True for row in rows):
    raise SystemExit("self-test aggregate contract mismatch")
summary = {
    "kind": "embedded-update-e2e",
    "scope": "bootstrap/application-state-machine-real",
    "e2e_mode": "non-docker",
    "docker": False,
    "webui": False,
    "ac_b1_is_not_docker_webui": True,
    "description": "Real bootstrap/application state-machine self-tests only; this is not Docker/WebUI AC-B1.",
    "requested_scenario": "all",
    "scenarios": rows,
    "passed": True,
    "blocked": [],
}
out.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n")
PY
    mv "$self_test_workdir/step7-summary.json" "$evidence_dir/step7-summary.json"
    echo "all bootstrap/application-state-machine-real self-tests passed; evidence: $evidence_dir"
    exit 0
  fi

  ac="ac-b1"
  case "$scenario_arg" in migrate-failure) ac=ac-b3 ;; rollback-blocked) ac=ac-b4 ;; min-image-version) ac=ac-b6 ;; esac
  cp "$self_test_workdir/$ac.json" "$evidence_dir/$ac.json"
  echo "$ac self-test passed; evidence: $evidence_dir/$ac.json"
  exit 0
fi

if [[ "${BANSOKO_EMBEDDED_E2E:-0}" != "1" ]]; then
  echo "refusing: BANSOKO_EMBEDDED_E2E=1 is required (isolated non-production guard)" >&2; exit 2
fi
if [[ "${BANSOKO_ENVIRONMENT:-development}" == production ]]; then
  echo "refusing: embedded E2E cannot target production" >&2; exit 2
fi
for bin in docker python3; do command -v "$bin" >/dev/null 2>&1 || { echo "$bin required" >&2; exit 2; }; done
if ! docker info >/dev/null 2>&1; then echo "refusing: Docker daemon unavailable (fail-closed)" >&2; exit 2; fi

if ! [[ "$base_digest" =~ ^sha256:[a-f0-9]{64}$ ]]; then base_digest=""; fi
if [[ ! -f "$compose_file" ]]; then echo "refusing: compose file not found: $compose_file" >&2; exit 2; fi

workdir="$(mktemp -d /tmp/bansoko-embedded-e2e.XXXXXX)"
project="bansoko-embedded-e2e-$(date -u +%Y%m%d%H%M%S)-$$"
updates_volume="${project}-updates"
mkdir -p "$evidence_dir" "$workdir/config"

cleanup() {
  if [[ "$keep" != 1 ]]; then
    docker compose -p "$project" -f "$workdir/compose.yml" down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$workdir"
  else
    echo "keeping workdir=$workdir project=$project" >&2
  fi
}
trap cleanup EXIT

# Render an isolated copy. Removing fixed container names is essential: Compose
# project names isolate resources, while container_name defeats that isolation.
python3 - "$compose_file" "$workdir/compose.yml" "$updates_volume" <<'PY'
import pathlib, sys
src, dst, updates_volume = map(pathlib.Path, sys.argv[1:])
text = src.read_text()
lines = [line for line in text.splitlines(True) if not line.lstrip().startswith("container_name:")]
text = "".join(lines).replace("BANSOKO_ENVIRONMENT: production", "BANSOKO_ENVIRONMENT: development")
# Bind the explicitly-created per-run volume instead of relying on Compose's
# project-name transformation; hooks can inspect the exact same volume name.
text = text.replace("source: updates", f"source: {updates_volume}")
if "volumes:\n  postgres_data:" not in text:
    raise SystemExit("embedded E2E compose patch failed: updates volume anchor missing")
text = text.replace("  updates:\n", f"  {updates_volume}:\n")
dst.write_text(text)
PY

docker volume create "$updates_volume" >/dev/null
export BANSOKO_BUNDLE_IMAGE_DIGEST="$base_digest" BANSOKO_UPDATE_MODE=embedded BANSOKO_CONFIG_DIR="$workdir/config"

scenarios=(normal migrate-failure rollback-blocked min-image-version)
if [[ "$scenario_arg" != all ]]; then scenarios=("$scenario_arg"); fi
# Do not let evidence from a previous invocation satisfy a partial run.
for scenario in "${scenarios[@]}"; do
  case "$scenario" in
    normal) rm -f "$evidence_dir/ac-b1.json" ;;
    migrate-failure) rm -f "$evidence_dir/ac-b3.json" ;;
    rollback-blocked) rm -f "$evidence_dir/ac-b4.json" ;;
    min-image-version) rm -f "$evidence_dir/ac-b6.json" ;;
  esac
done
run_one() {
  local scenario="$1" ac hook result log status reason
  case "$scenario" in
    normal) ac=ac-b1; hook="${BANSOKO_EMBEDDED_E2E_NORMAL_CMD:-}" ;;
    migrate-failure) ac=ac-b3; hook="${BANSOKO_EMBEDDED_E2E_MIGRATE_FAILURE_CMD:-}" ;;
    rollback-blocked) ac=ac-b4; hook="${BANSOKO_EMBEDDED_E2E_ROLLBACK_BLOCKED_CMD:-}" ;;
    min-image-version) ac=ac-b6; hook="${BANSOKO_EMBEDDED_E2E_MIN_IMAGE_VERSION_CMD:-}" ;;
  esac
  result="$workdir/$ac.json"; log="$workdir/$ac.log"
  : >"$log"
  if [[ -z "$base_digest" ]]; then
    python3 - "$result" "$scenario" <<'PY'
import json, sys
json.dump({"acceptance":sys.argv[2],"scope":"harness","blocked":True,"passed":False,
           "reason":"BANSOKO_EMBEDDED_E2E_BASE_IMAGE_DIGEST is not an immutable sha256 image digest"}, open(sys.argv[1],"w"), indent=2)
PY
  elif [[ -z "$hook" ]]; then
    python3 - "$result" "$scenario" <<'PY'
import json, sys
json.dump({"acceptance":sys.argv[2],"scope":"harness","blocked":True,"passed":False,
           "reason":"no real scenario hook configured; refusing fixture evidence"}, open(sys.argv[1],"w"), indent=2)
PY
  else
    echo "== $scenario ($project) ==" | tee -a "$log"
    E2E_PROJECT="$project" E2E_WORKDIR="$workdir" E2E_UPDATES_VOLUME="$updates_volume" \
      E2E_COMPOSE_FILE="$workdir/compose.yml" E2E_BASE_IMAGE_DIGEST="$base_digest" \
      E2E_EVIDENCE_FILE="$result" BANSOKO_UPDATE_MODE=embedded \
      bash -c "$hook" >>"$log" 2>&1 || {
        python3 - "$result" "$scenario" "$log" <<'PY'
import json, sys
json.dump({"acceptance":sys.argv[2],"scope":"real","blocked":True,"passed":False,
           "reason":"real scenario hook failed","log":sys.argv[3]}, open(sys.argv[1],"w"), indent=2)
PY
      }
  fi
  # Validate the hook contract and add runner provenance. Missing/fixture scope
  # can never become a pass merely because a command exited successfully.
  python3 - "$result" "$scenario" "$ac" "$base_digest" "$project" <<'PY'
import json, pathlib, sys
p=pathlib.Path(sys.argv[1]); scenario, ac, digest, project=sys.argv[2:]
try: data=json.loads(p.read_text())
except Exception as exc: data={"passed":False,"scope":"harness","blocked":True,"reason":f"invalid evidence JSON: {exc}"}
data.update({"acceptance":ac,"scenario":scenario,"project":project,
             "update_mode":"embedded","base_image_digest":digest})
if data.get("scope") != "real" or data.get("passed") is not True:
    data["passed"]=False; data["blocked"]=True
    data.setdefault("reason","evidence must explicitly assert scope=real and passed=true")
json.dump(data,p.open("w"),indent=2,sort_keys=True)
print(json.dumps(data,sort_keys=True))
if data.get("passed") is not True or data.get("scope") != "real":
    raise SystemExit(1)
PY
  cp "$result" "$evidence_dir/$ac.json"
}

failed=0
for scenario in "${scenarios[@]}"; do run_one "$scenario" || failed=1; done

python3 - "$evidence_dir/step7-summary.json" "$failed" "$project" "$base_digest" "$scenario_arg" <<'PY'
import json, pathlib, sys
out=pathlib.Path(sys.argv[1]); failed=int(sys.argv[2]); project=sys.argv[3]; digest=sys.argv[4]; requested=sys.argv[5]
expected = {
    'normal': ('ac-b1.json',),
    'migrate-failure': ('ac-b3.json',),
    'rollback-blocked': ('ac-b4.json',),
    'min-image-version': ('ac-b6.json',),
}
names = ('ac-b1.json','ac-b3.json','ac-b4.json','ac-b6.json') if requested == 'all' else expected[requested]
rows=[]
missing=[]
for name in names:
    p=out.parent/name
    if p.exists(): rows.append(json.loads(p.read_text()))
    else: missing.append(name)
passed=bool(rows) and not missing and failed==0 and all(r.get('passed') is True and r.get('scope')=='real' for r in rows)
summary={'kind':'embedded-update-e2e','scope':'real' if passed else 'harness','project':project,
         'base_image_digest':digest,'requested_scenario':requested,'scenarios':rows,'passed':passed,
         'blocked':[r for r in rows if r.get('blocked')]}
if missing: summary['missing_evidence']=missing
json.dump(summary,out.open('w'),indent=2,sort_keys=True)
print(json.dumps(summary,indent=2,sort_keys=True))
if not passed: raise SystemExit(1)
PY
[[ "$failed" == 0 ]] || { echo "embedded update E2E failed or blocked; evidence: $evidence_dir" >&2; exit 1; }
echo "embedded update E2E passed; evidence: $evidence_dir"
