#!/usr/bin/env bash
# UPDATE-029 disposable real Docker acceptance matrix. --self-test is static only.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/../.." && pwd)"
fixture="$root/scripts/ci/embedded-update-docker-fixture.sh"
evidence_dir="${BANSOKO_CRASH_MATRIX_EVIDENCE_DIR:-${TMPDIR:-/tmp}/bansoko-update-crash-matrix}"
run_daemon=0
self_test=0
list_only=0
for arg in "$@"; do
  case "$arg" in
    --self-test) self_test=1 ;;
    --list) list_only=1 ;;
    --daemon-restart) run_daemon=1 ;;
    *) echo "unknown argument: $arg (supported: --self-test, --list, --daemon-restart)" >&2; exit 2 ;;
  esac
done
crash_scenarios=(staging marker-before-backup after-backup migrate)
# crash-staging is post-publish/pre-marker only: no deterministic in-download
# kill hook exists. Rows are scenarios, not evidence that a VM run occurred.
# --self-test outputs explicitly label real_evidence=false.
regressions=(normal migrate-failure readiness-failure rollback-blocked)
matrix_rows() {
  local scenario
  printf 'disposable|crash-staging|resume|none|container\n'
  for scenario in marker-before-backup after-backup migrate; do printf 'disposable|crash-%s|resume|none|container\n' "$scenario"; done
  printf 'disposable|daemon-restart-bootstrap|resume|none|daemon\n'
  for scenario in "${regressions[@]}"; do printf 'regression|%s|resume|none|container\n' "$scenario"; done
}
if (( list_only == 1 )); then
  printf '%s\n' 'Scenario listing only; no VM evidence is run. crash-staging covers post-publish/pre-marker, not in-download termination; no deterministic download hook is available.' >&2
  matrix_rows
  exit 0
fi
self_test_matrix() {
  local rows scenario
  rows="$(matrix_rows)"
  [[ "$(wc -l <<<"$rows" | tr -d '[:space:]')" == 9 ]]
  for scenario in staging marker-before-backup after-backup migrate; do grep -qx "disposable|crash-$scenario|resume|none|container" <<<"$rows"; done
  for scenario in "${regressions[@]}"; do grep -qx "regression|$scenario|resume|none|container" <<<"$rows"; done
  grep -qx 'disposable|daemon-restart-bootstrap|resume|none|daemon' <<<"$rows"
  grep -q 'pending.json' "$fixture"
  grep -q 'release_sequence' "$fixture"
  grep -q 'previous_release_id' "$fixture"
  grep -q 'resources_clean' "$fixture"
  python3 - "$rows" <<'PY'
import sys
rows=sys.argv[1].splitlines()
assert all(len(row.split("|")) == 5 for row in rows)
assert not any("token" in row.lower() or "secret" in row.lower() or "/" in row for row in rows)
print('{"kind":"embedded-crash-matrix-self-test","passed":true,"real_evidence":false}')
PY
}
if (( self_test == 1 )); then self_test_matrix; exit 0; fi
[[ "${BANSOKO_CRASH_MATRIX:-0}" == 1 && "${BANSOKO_EMBEDDED_E2E:-0}" == 1 ]] || { echo "refusing: BANSOKO_CRASH_MATRIX=1 and BANSOKO_EMBEDDED_E2E=1 are required" >&2; exit 2; }
[[ "${BANSOKO_ENVIRONMENT:-development}" != production ]] || { echo "refusing: crash matrix cannot target production" >&2; exit 2; }
source_commit="${BANSOKO_CRASH_MATRIX_SOURCE_COMMIT:-}"
head_commit="$(git -C "$root" rev-parse HEAD 2>/dev/null || true)"
[[ "$source_commit" =~ ^[0-9a-f]{40}$ && "$source_commit" == "$head_commit" ]] || { echo "refusing: BANSOKO_CRASH_MATRIX_SOURCE_COMMIT must equal git rev-parse HEAD" >&2; exit 2; }
[[ -x "$fixture" ]] || { echo "fixture is not executable" >&2; exit 2; }
if (( run_daemon == 1 )) && [[ -z "${BANSOKO_EMBEDDED_DAEMON_RESTART_CMD:-}" ]]; then echo "refusing: daemon hook required" >&2; exit 2; fi
for tool in docker python3 git; do command -v "$tool" >/dev/null || exit 2; done
docker info >/dev/null 2>&1 || { echo "refusing: Docker daemon unavailable" >&2; exit 2; }
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$-$RANDOM"
[[ "$run_id" =~ ^[A-Za-z0-9._-]+$ ]] || exit 2
mkdir -p "$evidence_dir"
summary="$evidence_dir/update-029-docker-matrix-summary.json"
rm -f "$summary" "$summary.tmp"
raw_base="${BANSOKO_CRASH_MATRIX_RAW_DIR:-$(mktemp -d /tmp/bansoko-update-matrix.XXXXXX)}"
raw_dir="$raw_base/$run_id"; mkdir -p "$raw_dir"
python3 - "$root" "$evidence_dir" "$raw_dir" <<'PY'
import pathlib,sys
root,evidence,raw=(pathlib.Path(x).resolve() for x in sys.argv[1:])
if raw == root or root in raw.parents or raw == evidence or evidence in raw.parents: raise SystemExit("raw logs must remain outside repository/evidence")
PY
failed=0; index=0
matrix_image="bansoko:embedded-e2e-$run_id"
cleanup() {
  docker image rm "$matrix_image" >>"$raw_dir/image-cleanup-trap.log" 2>&1 || true
}
trap cleanup EXIT
reuse_image=0
while IFS='|' read -r family scenario action negative restart; do
  index=$((index+1)); row_key="$family|$scenario|$action|$negative|$restart"
  row_dir="$raw_dir/$(printf '%02d' "$index")"; mkdir -p "$row_dir"; printf '%s\n' "$row_key" >"$row_dir/row-key"
  args=(--docker-fixture "--scenario=$scenario")
  if [[ "$scenario" == crash-* || "$scenario" == daemon-restart-bootstrap ]]; then args+=("--action=$action" "--negative=$negative" "--restart=$restart"); fi
  # calls inside it attach stdin, and the Docker CLI then drains this
  # process-substitution stream, silently truncating the matrix after the first
  # row.
  if ! BANSOKO_EMBEDDED_E2E_REUSE_IMAGE="$reuse_image" BANSOKO_EMBEDDED_E2E_DOCKER_IMAGE="$matrix_image" BANSOKO_EMBEDDED_E2E_EVIDENCE_DIR="$row_dir" BANSOKO_EMBEDDED_E2E_SOURCE_COMMIT="$source_commit" BANSOKO_EMBEDDED_E2E_RUN_ID="$run_id" "$fixture" "${args[@]}" >"$row_dir/stdout.log" 2>"$row_dir/stderr.log" </dev/null; then failed=1; fi
  reuse_image=1
done < <(matrix_rows)
mapfile -t leaked_containers < <(docker ps -aq --filter "label=bansoko.update-e2e.run=$run_id")
mapfile -t leaked_volumes < <(docker volume ls -q --filter "label=bansoko.update-e2e.run=$run_id")
if (( ${#leaked_containers[@]} > 0 || ${#leaked_volumes[@]} > 0 )); then
  failed=1
  (( ${#leaked_containers[@]} == 0 )) || docker rm -f "${leaked_containers[@]}" >/dev/null 2>&1 || true
  (( ${#leaked_volumes[@]} == 0 )) || docker volume rm "${leaked_volumes[@]}" >/dev/null 2>&1 || true
fi
if docker ps -aq --filter "label=bansoko.update-e2e.run=$run_id" | grep -q . || docker volume ls -q --filter "label=bansoko.update-e2e.run=$run_id" | grep -q .; then resources_clean=false; else resources_clean=true; fi
if docker image inspect "$matrix_image" >/dev/null 2>&1; then
  if docker image rm "$matrix_image" >"$raw_dir/image-cleanup.log" 2>&1; then image_cleanup=removed; else image_cleanup=failed; failed=1; fi
else
  image_cleanup=not_created
fi
python3 - "$raw_dir" "$summary.tmp" "$summary" "$failed" "$run_id" "$source_commit" "$resources_clean" "$image_cleanup" "$(matrix_rows)" <<'PY'
import json,os,pathlib,re,sys
raw,tmp,out,failed,run_id,source_commit,clean,image_cleanup,expected_text=sys.argv[1:]
raw=pathlib.Path(raw); expected=expected_text.splitlines(); rows=[]; image_ids=set(); source_docs=[]
for directory in sorted(p for p in raw.iterdir() if p.is_dir()):
    key=(directory/"row-key").read_text().strip(); docs=[]
    for path in directory.glob("*.json"):
        try:
            data=json.loads(path.read_text())
            if data.get("scope")=="docker-fixture": docs.append(data)
        except Exception: pass
    data=docs[-1] if docs else {}
    recovery=data.get("recovery") or {}
    row={"row_key":key,"passed":data.get("passed") is True,"blocked":data.get("blocked") is True,"scenario":data.get("scenario",key.split("|")[1]),"crash_checkpoint":data.get("crash_checkpoint"),"terminal_step":data.get("terminal_step"),"pointer_role":data.get("pointer_role"),"ready":data.get("ready"),"crash_exit_code_observed":data.get("crash_exit_code_observed"),"daemon_restart_observed":data.get("daemon_restart_observed"),"resources_clean":data.get("resources_clean"),"pending_marker_present":(data.get("artifacts") or {}).get("pending_marker_present"),"temporary_files":(data.get("artifacts") or {}).get("temporary_files")}
    if data.get("image_id"): image_ids.add(data["image_id"])
    if isinstance(data.get("source"),dict): source_docs.append(data["source"])
    rows.append(row)
observed=[r["row_key"] for r in rows]
source_identities={(doc.get("base_head"),doc.get("dirty"),doc.get("tree_hash")) for doc in source_docs}
source_identity=None
if len(source_identities)==1:
    base_head,dirty,tree_hash=next(iter(source_identities))
    if base_head==source_commit and isinstance(dirty,bool) and isinstance(tree_hash,str) and re.fullmatch(r"[0-9a-f]{64}",tree_hash): source_identity={"base_head":base_head,"dirty":dirty,"tree_hash":tree_hash}
passed=(int(failed)==0 and sorted(observed)==sorted(expected) and all(r["passed"] and r["resources_clean"] is True for r in rows) and clean=="true" and image_cleanup=="removed" and len(image_ids)==1 and len(source_docs)==len(rows) and source_identity is not None)
summary={"kind":"update-029-disposable-real-docker-matrix","evidence_kind":"real_docker","self_test":False,"run_id":run_id,"source_commit":source_commit,"source_identity":source_identity,"image_id":next(iter(image_ids)) if len(image_ids)==1 else None,"image_cleanup":image_cleanup,"passed":passed,"rows_expected":len(expected),"rows_observed":len(rows),"required_checkpoints":["staged","pending_marker","pending_backup","migration_succeeded"],"resources_clean":clean=="true","download_crash_hook":"unavailable_no_deterministic_inflight_download_boundary"}
encoded=json.dumps(summary,sort_keys=True)
if re.search(r"(?i)(postgres(?:ql)?://|bearer\s+\S+|password|passwd|secret|token|private[_-]?key|database_url|dsn|/tmp/|/var/|/run/)",encoded): raise SystemExit("sanitized evidence contains forbidden material")
pathlib.Path(tmp).write_text(json.dumps(summary,indent=2,sort_keys=True)+"\n"); os.replace(tmp,out); print(json.dumps(summary,indent=2,sort_keys=True))
if not passed: raise SystemExit(1)
PY
