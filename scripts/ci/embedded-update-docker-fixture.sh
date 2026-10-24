#!/usr/bin/env bash
# Bounded real-Docker fixture for AC-B1. This is intentionally not a fixture
# state-machine test: all positive assertions come from Docker processes,
# Compose state, HTTP responses, or files in the named updates volume.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/../.." && pwd)"
evidence_dir="${BANSOKO_EMBEDDED_E2E_EVIDENCE_DIR:-}"
scenario=normal
recovery_action="${BANSOKO_EMBEDDED_RECOVERY_ACTION:-resume}"
negative_case="${BANSOKO_EMBEDDED_NEGATIVE_CASE:-none}"
restart_kind="${BANSOKO_EMBEDDED_RESTART_KIND:-container}"
daemon_restart_cmd="${BANSOKO_EMBEDDED_DAEMON_RESTART_CMD:-}"
readmission_check=0
higher_sequence_admission=0
channel_discovery=0
keep=0
for arg in "$@"; do
  case "$arg" in
    --docker-fixture) ;;
    --self-test) echo "refusing: fixture self-test is not real Docker evidence; use embedded-crash-matrix.sh --self-test" >&2; exit 2 ;;
    --scenario=normal|--scenario=migrate-failure|--scenario=readiness-failure|--scenario=rollback-blocked|--scenario=min-image-version) scenario="${arg#--scenario=}" ;;
    --scenario=crash-staging|--scenario=crash-marker-before-backup|--scenario=crash-after-backup|--scenario=crash-migrate|--scenario=daemon-restart-bootstrap) scenario="${arg#--scenario=}" ;;
    --action=resume|--action=abort) recovery_action="${arg#--action=}" ;;
    --negative=none) negative_case="${arg#--negative=}" ;;
    --restart=container|--restart=daemon) restart_kind="${arg#--restart=}" ;;
    --readmission-check) readmission_check=1 ;;
    --higher-sequence-admission) higher_sequence_admission=1 ;;
    --channel-discovery) channel_discovery=1 ;;
    --scenario=*) echo "refusing: unsupported Docker fixture scenario" >&2; exit 2 ;;
    --action=*|--negative=*|--restart=*) echo "refusing: unsupported fixture option" >&2; exit 2 ;;
    --keep) keep=1 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done
if [[ "$restart_kind" == daemon && -z "$daemon_restart_cmd" ]]; then
  echo "refusing: --restart=daemon requires BANSOKO_EMBEDDED_DAEMON_RESTART_CMD" >&2
  exit 2
fi
if [[ "$scenario" != crash-* && "$scenario" != daemon-restart-bootstrap && ( "$negative_case" != none || "$recovery_action" != resume || "$restart_kind" != container || "$readmission_check" != 0 || "$higher_sequence_admission" != 0 ) ]]; then
  echo "refusing: recovery options require a crash scenario" >&2
  exit 2
fi
if (( higher_sequence_admission == 1 )) && [[ "$recovery_action" != abort || "$readmission_check" != 1 || "$negative_case" != none ]]; then
  echo "refusing: higher-sequence admission requires a successful abort plus --readmission-check" >&2
  exit 2
fi
[[ "${BANSOKO_EMBEDDED_E2E:-0}" == 1 ]] || { echo "refusing: BANSOKO_EMBEDDED_E2E=1 is required" >&2; exit 2; }
[[ "${BANSOKO_ENVIRONMENT:-development}" != production ]] || { echo "refusing: fixture cannot target production" >&2; exit 2; }
for tool in docker python3 curl shasum go git; do command -v "$tool" >/dev/null || { echo "$tool required" >&2; exit 2; }; done
docker compose version >/dev/null 2>&1 || { echo "docker compose required" >&2; exit 2; }
docker buildx version >/dev/null 2>&1 || { echo "docker buildx required" >&2; exit 2; }
docker info >/dev/null 2>&1 || { echo "refusing: Docker daemon unavailable" >&2; exit 2; }
workdir="$(mktemp -d /tmp/bansoko-embedded-docker.XXXXXX)"
if [[ -z "$evidence_dir" ]]; then evidence_dir="$workdir/evidence"; fi
mkdir -p "$evidence_dir"
python3 - "$root" "$evidence_dir" <<'PY'
import pathlib, sys
root, evidence = (pathlib.Path(value).resolve() for value in sys.argv[1:])
if evidence == root or root in evidence.parents:
    raise SystemExit("refusing: fixture evidence must stay outside the repository; only the matrix sanitizer may write task evidence")
PY
project="bansoko-embedded-docker-$(date -u +%Y%m%d%H%M%S)-$$"
volume="${project}-updates"
image="${BANSOKO_EMBEDDED_E2E_DOCKER_IMAGE:-bansoko:embedded-e2e}"
result="$workdir/ac-${scenario/normal/b1}.json"
case "$scenario" in
  crash-*) result="$workdir/ac-b2-${scenario#crash-}.json" ;;
esac
# UPDATE-029 disposable crash points have no pre-handoff journal or recovery CLI.
crash_window=""
crash_target_step=""
crash_window_kind="disposable"
case "$scenario" in
  crash-staging) crash_window=staged; crash_target_step=staged ;;
  crash-marker-before-backup) crash_window=pending_marker; crash_target_step=pending_marker ;;
  crash-after-backup) crash_window=pending_backup; crash_target_step=pending_backup ;;
  crash-migrate) crash_window=migration_succeeded; crash_target_step=migration_succeeded ;;
  daemon-restart-bootstrap) crash_window=pending_marker; crash_target_step=pending_marker ;;
esac
crash_injection_line="FIXTURE INJECTION: durable checkpoint \"$crash_target_step\""
crash_injection_line=""
if [[ -n "$crash_target_step" ]]; then crash_injection_line="FIXTURE INJECTION: durable checkpoint \"$crash_target_step\""; fi
# No deterministic in-flight payload download hook exists yet; crash-staging
# covers the app-provided post-publish/pre-marker boundary only.

journal_document() {
  docker run --rm -v "$volume":/updates alpine:3.20 sh -c \
    'file=/updates/journal.json; if [ ! -f "$file" ]; then file="$(ls -1t /updates/archive/journal-*.json 2>/dev/null | head -1)"; fi; [ -n "$file" ] && cat "$file"' 2>/dev/null || true
}

journal_field() {
  local field="$1"
  journal_document | python3 -c 'import json,sys; data=json.load(sys.stdin); record=data.get("record") or data; value=record.get(sys.argv[1], ""); print(value if value is not None else "")' "$field" 2>/dev/null || true
}

journal_step() { journal_field step; }

app_log_count() { docker compose -p "$project" -f "$compose" logs --no-color app 2>/dev/null | grep -cF "$1" || true; }

case "$scenario" in
  migrate-failure) result="$workdir/ac-b3-docker.json" ;;
  readiness-failure|rollback-blocked) result="$workdir/ac-b4-docker.json" ;;
esac
compose="$workdir/compose.yml"
log="$workdir/compose.log"
cleanup() {
  if [[ -n "${artifact_pid:-}" ]]; then kill "$artifact_pid" 2>/dev/null || true; fi
  if [[ -n "${crash_events_pid:-}" ]]; then kill "$crash_events_pid" 2>/dev/null || true; fi
  if [[ -n "${daemon_monitor_pid:-}" ]]; then kill "$daemon_monitor_pid" 2>/dev/null || true; fi
  if [[ "$keep" != 1 ]]; then
    docker compose -p "$project" -f "$compose" down -v --remove-orphans >/dev/null 2>&1 || true
    docker volume rm "$volume" >/dev/null 2>&1 || true
    :
  else
    echo "keeping workdir=$workdir project=$project volume=$volume" >&2
  fi
  if [[ -f "$result" ]]; then
    python3 - "$result" "$evidence_dir/${result##*/}" "$project" "$volume" "$keep" <<'PY'
import json,pathlib,subprocess,sys
result,evidence,project,volume,keep=sys.argv[1:]
clean=keep!="1"
if clean:
    containers=subprocess.run(["docker","ps","-aq","--filter",f"label=com.docker.compose.project={project}"],capture_output=True,text=True,check=False).stdout.strip()
    mounted=subprocess.run(["docker","volume","inspect",volume],capture_output=True,text=True,check=False).returncode==0
    clean=not containers and not mounted
path=pathlib.Path(result)
try:
    data=json.loads(path.read_text()); data["resources_clean"]=clean
    if not clean: data["passed"]=False; data["blocked"]=True; data["reason"]="fixture Docker resources were retained"
    path.write_text(json.dumps(data,indent=2,sort_keys=True)+"\n")
    pathlib.Path(evidence).write_text(path.read_text())
except Exception: pass
PY
  fi
  if [[ "$keep" != 1 ]]; then
    # Unexpected shell/Python errors may precede the result document. Preserve
    # diagnostics without keeping owner/login tokens in an orphan temp root.
    cp "$log" "$evidence_dir/fixture-last.log" 2>/dev/null || true
    if [[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$project")" ]] && ! docker volume inspect "$volume" >/dev/null 2>&1; then
      rm -rf "$workdir"
    else
      echo "cleanup incomplete: retained workdir=$workdir" >&2
    fi
  fi
}
trap cleanup EXIT
fail_blocked() {
  local reason="$1" detail="${2:-}"
  printf '%s\n' "$detail" >>"$log" 2>/dev/null || true
  python3 - "$result" "$reason" "$scenario" "$recovery_action" "$negative_case" "$restart_kind" <<'PY'
import json, pathlib, sys
out, reason, scenario, action, negative, restart = sys.argv[1:]
acceptance = "ac-b1" if scenario == "normal" else ("ac-b2" if scenario.startswith("crash-") else ("ac-b3" if scenario == "migrate-failure" else "ac-b4"))
data = {"acceptance":acceptance, "scenario":scenario, "scope":"docker-fixture", "evidence_kind":"real_docker", "self_test":False, "passed":False, "blocked":True, "reason":reason, "recovery_action":action, "negative_case":negative, "restart_kind":restart}
pathlib.Path(out).write_text(json.dumps(data, indent=2, sort_keys=True)+"\n")
print(json.dumps(data, indent=2, sort_keys=True))
PY
  cp "$result" "$evidence_dir/${result##*/}"
  exit 1
}

source_commit="${BANSOKO_EMBEDDED_E2E_SOURCE_COMMIT:-}"
head_commit="$(git -C "$root" rev-parse HEAD 2>/dev/null || true)"
[[ "$source_commit" =~ ^[0-9a-f]{40}$ && "$source_commit" == "$head_commit" ]] || fail_blocked "source commit is not the checkout HEAD" "BANSOKO_EMBEDDED_E2E_SOURCE_COMMIT must equal git rev-parse HEAD"
source_manifest="$workdir/source-tree-manifest.json"
read -r source_dirty source_tree_hash < <(python3 - "$root" "$source_commit" "$source_manifest" <<'PY'
import hashlib, json, os, pathlib, stat, subprocess, sys
root=pathlib.Path(sys.argv[1]); commit=sys.argv[2]; out=pathlib.Path(sys.argv[3])
def git(*args): return subprocess.check_output(["git","-C",str(root),*args])
paths=sorted(filter(None, git("ls-files","-z","--cached","--others","--exclude-standard").split(b"\0")))
files=[]
for raw in paths:
    relative=os.fsdecode(raw); path=root/relative
    try: info=os.lstat(path)
    except FileNotFoundError:
        files.append({"path":relative,"type":"missing","mode":None,"size":0,"sha256":""}); continue
    if stat.S_ISLNK(info.st_mode): payload=os.fsencode(os.readlink(path)); kind="symlink"
    elif stat.S_ISREG(info.st_mode): payload=path.read_bytes(); kind="file"
    else: payload=b""; kind="other"
    files.append({"path":relative,"type":kind,"mode":stat.S_IMODE(info.st_mode),"size":len(payload),"sha256":hashlib.sha256(payload).hexdigest()})
encoded=(json.dumps({"schema":"source-tree-v1","base_head":commit,"files":files},ensure_ascii=True,sort_keys=True,separators=(",",":"))+"\n").encode()
out.write_bytes(encoded)
dirty=bool(git("status","--porcelain=v1","-z","--untracked-files=all"))
print(str(dirty).lower(), hashlib.sha256(encoded).hexdigest())
PY
)
[[ "$source_dirty" == true || "$source_dirty" == false ]] || fail_blocked "source dirty state unavailable"
[[ "$source_tree_hash" =~ ^[0-9a-f]{64}$ ]] || fail_blocked "source tree hash unavailable"
cp "$source_manifest" "$evidence_dir/source-tree-manifest.json"
if [[ "${BANSOKO_EMBEDDED_E2E_REUSE_IMAGE:-0}" == "1" ]]; then
  docker image inspect "$image" >/dev/null 2>&1 || fail_blocked "requested fixture image is unavailable" "$image"
else
  docker buildx build --load --platform "linux/${BANSOKO_EMBEDDED_E2E_GOARCH:-amd64}" --build-arg VERSION=embedded-e2e --build-arg SOURCE_COMMIT="$source_commit" --build-arg BUILD_ID=fixture-base --build-arg BUILD_TIME=embedded-fixture --build-arg BUILD_DIRTY="$source_dirty" --build-arg BASE_COMPATIBILITY_VERSION=base-v1 -t "$image" "$root" >"$log" 2>&1 || fail_blocked "local image build failed" "$log"
fi
image_id="$(docker image inspect --format '{{.Id}}' "$image")"
[[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || fail_blocked "local image has no immutable config identity"
# The app drops to PUID:PGID; use the invoking account so cleanup needs no root.
image_uid="$(id -u)"; image_gid="$(id -g)"
[[ "$image_uid" =~ ^[0-9]+$ && "$image_gid" =~ ^[0-9]+$ ]] || fail_blocked "fixture image user identity is not numeric"
docker volume create --label "bansoko.update-e2e.run=${BANSOKO_EMBEDDED_E2E_RUN_ID:-standalone}" "$volume" >/dev/null
pick_port() { python3 - "${1:-0}" <<'PY'
import socket,sys
requested=int(sys.argv[1])
for port in (requested,0):
    sock=socket.socket()
    try: sock.bind(("127.0.0.1",port)); print(sock.getsockname()[1]); break
    except OSError:
        if port==0: raise
    finally: sock.close()
PY
}
app_port="$(pick_port "${BANSOKO_APP_PORT:-18080}")"; artifact_port="$(pick_port "${BANSOKO_EMBEDDED_E2E_ARTIFACT_PORT:-0}")"; db_port="$(pick_port "${BANSOKO_DB_PORT:-15432}")"
export BANSOKO_BUNDLE_IMAGE_DIGEST="sha256:${image_id#sha256:}" BANSOKO_UPDATE_MODE=embedded BANSOKO_CONFIG_DIR="$workdir/config" BANSOKO_ENVIRONMENT=test
export PUID="$image_uid" PGID="$image_gid"
unset BANSOKO_DATABASE_URL BANSOKO_DB_HOST BANSOKO_DB_NAME BANSOKO_DB_USER BANSOKO_DB_PASSWORD BANSOKO_DB_PORT
export BANSOKO_DB_HOST=postgres BANSOKO_DB_NAME=bansoko_e2e_embedded BANSOKO_DB_USER=bansoko BANSOKO_DB_PASSWORD=embedded-e2e-password BANSOKO_APP_PORT="$app_port" BANSOKO_DB_PORT="$db_port"
export BANSOKO_EMBEDDED_FIXTURE_RESTORE_FAILURE=0
[[ "$scenario" != readiness-failure ]] || export BANSOKO_EMBEDDED_FIXTURE_FAIL_READINESS=1
mkdir -p "$workdir/config"; chmod 0755 "$workdir"; chmod 0777 "$workdir/config"
alpine_image="alpine:3.20"
docker image inspect "$alpine_image" >/dev/null 2>&1 || fail_blocked "fixture alpine image unavailable" "$alpine_image"
docker run --rm --user 0:0 --mount "type=bind,source=$workdir/config,target=/config" "$alpine_image" chown -R "$image_uid:$image_gid" /config || fail_blocked "fixture config ownership preparation failed"
private_key="${BANSOKO_EMBEDDED_E2E_PRIVATE_KEY:-$root/.local-artifacts/release/private.key}"; public_key="$root/deployment/public.key"
[[ -f "$private_key" && -f "$public_key" ]] || fail_blocked "fixture signing key unavailable"
target_id="${BANSOKO_EMBEDDED_E2E_TARGET_RELEASE_ID:-embedded-e2e-rc10}"; target_version="${BANSOKO_EMBEDDED_E2E_TARGET_VERSION:-0.9.0-rc.10}"
schema_version="$(python3 - "$root/db/migrations" <<'PY'
import pathlib,re,sys
versions=[]
for path in pathlib.Path(sys.argv[1]).glob('[0-9]*_*.sql'):
    match=re.match(r'^(\d+)_',path.name)
    if match: versions.append((int(match.group(1)),path.stem))
if not versions: raise SystemExit("no numbered migration files found")
print(max(versions)[1])
PY
)"
target_server="$workdir/target-server"; target_migrate="$workdir/target-migrate"
CGO_ENABLED=0 GOOS=linux GOARCH="${BANSOKO_EMBEDDED_E2E_GOARCH:-amd64}" go build -C "$root" -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X bansoko/internal/runtime/buildinfo.Version=$target_version -X bansoko/internal/runtime/buildinfo.Commit=$source_commit -X bansoko/internal/runtime/buildinfo.BuildID=$target_id -X bansoko/internal/runtime/buildinfo.BuildTime=embedded-fixture -X bansoko/internal/runtime/buildinfo.APIVersion=v2 -X bansoko/internal/runtime/buildinfo.SchemaVersion=$schema_version -X bansoko/internal/runtime/buildinfo.BuildDirty=$source_dirty -X bansoko/internal/runtime/buildinfo.BaseCompatibilityVersion=base-v1" -o "$target_server" ./cmd/bansoko-server >>"$log" 2>&1 || fail_blocked "target server build failed" "$log"
CGO_ENABLED=0 GOOS=linux GOARCH="${BANSOKO_EMBEDDED_E2E_GOARCH:-amd64}" go build -C "$root" -trimpath -buildvcs=false -ldflags "-s -w -buildid=" -o "$target_migrate" ./cmd/bansoko-migrate >>"$log" 2>&1 || fail_blocked "target migrate build failed" "$log"
chmod 0755 "$target_server" "$target_migrate"
if [[ "$scenario" == migrate-failure || "$scenario" == rollback-blocked ]]; then mv "$target_migrate" "$workdir/target-migrate-real"; printf '#!/bin/sh\nprintf "%%s\\n" "embedded fixture: injected migrate failure" >&2\nexit 73\n' >"$target_migrate"; chmod 0755 "$target_migrate"; fi
payload="$workdir/embedded-update-v1.tar.gz"
pack_json="$(go -C "$root" run ./cmd/bansoko-release pack-payload --server-binary "$target_server" --migrate-binary "$target_migrate" --migrations-dir "$root/db/migrations" --defaults-dir "$root/deployment/defaults" --frontend-dist "$root/frontend/dist" --output "$payload" --source-date-epoch 1756000000 --format-version embedded-update-v1 --release-id "$target_id" --version "$target_version" --source-commit "$source_commit" --build-id "$target_id" --target-schema-version "$schema_version" --platform linux --architecture "${BANSOKO_EMBEDDED_E2E_GOARCH:-amd64}")" || fail_blocked "signed payload build failed" "$log"
payload_digest="$(python3 -c 'import json,sys; print(json.loads(sys.stdin.read())["digest"])' <<<"$pack_json")"; payload_size="$(python3 -c 'import json,sys; print(json.loads(sys.stdin.read())["size"])' <<<"$pack_json")"
payload_signature="$(go -C "$root" run ./cmd/bansoko-release artifact-sign --digest "$payload_digest" --private-key "$private_key")"; artifact_digest="$(shasum -a 256 "$target_server" | awk '{print $1}')"; artifact_size="$(wc -c < "$target_server" | tr -d '[:space:]')"; artifact_signature="$(go -C "$root" run ./cmd/bansoko-release artifact-sign --digest "$artifact_digest" --private-key "$private_key")"
python3 - "$workdir/manifest-unsigned.json" "$target_id" "$target_version" "$source_commit" "$schema_version" "$payload_digest" "$payload_size" "$payload_signature" "$artifact_digest" "$artifact_size" "$artifact_signature" "$root" "${BANSOKO_EMBEDDED_E2E_GOARCH:-amd64}" <<'PY'
import hashlib,json,pathlib,sys
out,rid,version,commit,schema,pdigest,psize,psig,adigest,asize,asig,root,architecture=sys.argv[1:]
migrations=[{"id":p.name,"checksum":hashlib.sha256(p.read_bytes()).hexdigest(),"reversible":False,"compatibility":"forward-only"} for p in sorted((pathlib.Path(root)/"db/migrations").glob("*.sql"))]
d={"schema_version":"release-manifest-1","release_sequence":2,"release_id":rid,"version":version,"channel":"stable","published_at":"2026-08-28T00:00:00Z","source_commit":commit,"api_contract_version":"v2","target_migration_version":schema,"compatible_from_versions":[],"required_downtime":True,"backup_required":True,"rollback_class":"restore_required","migrations":migrations,"images":[],"compose_ref":"deployment/maintainer/docker-compose.production.yml","frontend_ref":"","artifacts":[{"platform":"linux","architecture":architecture,"digest":adigest,"size":int(asize),"signature":asig}],"sbom_ref":"sha256:"+"0"*64,"provenance_ref":"sha256:"+"0"*64,"artifact_digest":adigest,"signature":"","immutable":True,"embedded_update":{"format_version":"embedded-update-v1","platform":"linux","architecture":architecture,"payload_digest":pdigest,"payload_size":int(psize),"payload_signature":psig,"bootstrap_min_version":"embedded-bootstrap-v1","min_image_version":"base-v1","target_release_id":rid,"target_version":version,"target_schema_version":schema,"release_sequence":2}}
pathlib.Path(out).write_text(json.dumps(d)+"\n")
PY
if [[ "$scenario" == min-image-version ]]; then
  python3 - "$workdir/manifest-unsigned.json" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1]); d=json.loads(p.read_text()); d['embedded_update']['min_image_version']='base-v999'; p.write_text(json.dumps(d)+'\n')
PY
fi
manifest="$workdir/release-manifest.json"
go -C "$root" run ./cmd/bansoko-release sign --input "$workdir/manifest-unsigned.json" --output "$manifest" --private-key "$private_key" >>"$log" 2>&1 || fail_blocked "manifest signing failed" "$log"
go -C "$root" run ./cmd/bansoko-release verify-embedded --manifest "$manifest" --public-key "$public_key" --payload "$payload" >>"$log" 2>&1 || fail_blocked "fixture artifact verification failed" "$log"
channel_url=""; package_root=""
python3 -m http.server "$artifact_port" --bind 0.0.0.0 --directory "$workdir" >"$workdir/artifact-http.log" 2>&1 & artifact_pid=$!
manifest_url="http://host.docker.internal:${artifact_port}/release-manifest.json"
for _ in $(seq 1 20); do curl -fsS "http://127.0.0.1:${artifact_port}/release-manifest.json" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "http://127.0.0.1:${artifact_port}/release-manifest.json" >/dev/null 2>&1 || fail_blocked "artifact HTTP server did not become ready" "$workdir/artifact-http.log"
python3 "$root/scripts/ci/transform-embedded-compose.py" "$root/deployment/maintainer/docker-compose.production.yml" "$compose" "$volume" "$image"
python3 - "$compose" "$manifest_url" "$scenario" "$crash_target_step" "${BANSOKO_EMBEDDED_E2E_RUN_ID:-standalone}" "$channel_url" "$package_root" "$volume" <<'PY'
import pathlib,sys
dst=pathlib.Path(sys.argv[1]); manifest_url,scenario,crash_target_step,run_id,channel_url,package_root,volume=sys.argv[2:]
text=dst.read_text()
def set_env(text,values):
    prefixes={key:f"      {key}:" for key in values}; found=set(); out=[]
    for line in text.splitlines(True):
        key=next((key for key,prefix in prefixes.items() if line.startswith(prefix)),None)
        if key is None: out.append(line)
        elif key not in found: out.append(f"      {key}: {values[key]}\n"); found.add(key)
    missing=[key for key in values if key not in found]
    if missing:
        index=next((i for i,line in enumerate(out) if line.startswith("      BANSOKO_UPDATES_ROOT:")),None)
        if index is None: raise SystemExit("app update environment anchor missing")
        out[index+1:index+1]=[f"      {key}: {values[key]}\n" for key in missing]
    return "".join(out)
if scenario=="rollback-blocked": text=set_env(text,{"BANSOKO_EMBEDDED_FIXTURE_RESTORE_FAILURE":'"1"'})
if scenario=="readiness-failure": text=set_env(text,{"BANSOKO_EMBEDDED_FIXTURE_FAIL_READINESS":'"1"'})
if scenario=="crash-staging": text=set_env(text,{"BANSOKO_EMBEDDED_FIXTURE_STOP_BEFORE_MARKER":'"1"'})
if scenario in ("crash-marker-before-backup","daemon-restart-bootstrap"): text=set_env(text,{"BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER":'"pending_marker"'})
if scenario=="crash-after-backup": text=set_env(text,{"BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER":'"pending_backup"'})
if scenario=="crash-migrate": text=set_env(text,{"BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER":'"migration_succeeded"'})
for service in ("postgres","app"):
    anchor=f"  {service}:\n"
    if anchor not in text: raise SystemExit(f"service anchor missing: {service}")
anchor="  postgres_data:\n"
if anchor not in text: raise SystemExit("postgres volume anchor missing")
text=text.replace(anchor,anchor+f"    labels:\n      bansoko.update-e2e.run: {run_id}\n",1)
text=set_env(text,{"BANSOKO_UPDATE_MANIFEST_URL":manifest_url,"BANSOKO_UPDATE_CHANNEL_URL":'""',"BANSOKO_UPDATE_PACKAGE_ROOT":'""'})
text=text.replace("    ports:\n      - \"${BANSOKO_APP_PORT:-8000}:8080\"","    extra_hosts:\n      - host.docker.internal:host-gateway\n    ports:\n      - \"${BANSOKO_APP_PORT:-8000}:8080\"").replace('      - "127.0.0.1:29000:29000"\n','')
if f"  {volume}:\n" not in text: raise SystemExit("updates volume anchor missing")
dst.write_text(text)
PY
docker compose -p "$project" -f "$compose" up -d postgres app >>"$log" 2>&1 || fail_blocked "real Compose startup failed" "$log"
for _ in $(seq 1 60); do curl -fsS "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/livez" >/dev/null 2>&1 && break; sleep 2; done
curl -fsS "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/livez" >/dev/null 2>&1 || fail_blocked "real app process did not become live" "$log"
inspect="$(docker compose -p "$project" -f "$compose" exec -T app sh -c 'printf "BANSOKO_UPDATE_MODE=%s\\nBANSOKO_EMBEDDED_FIXTURE_STOP_AFTER=%s\\nBANSOKO_EMBEDDED_FIXTURE_STOP_BEFORE_MARKER=%s\\nBANSOKO_EMBEDDED_FIXTURE_FAIL_READINESS=%s\\n" "$BANSOKO_UPDATE_MODE" "$BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER" "$BANSOKO_EMBEDDED_FIXTURE_STOP_BEFORE_MARKER" "$BANSOKO_EMBEDDED_FIXTURE_FAIL_READINESS"; test -x /app/bootstrap; test -x /app/bansoko-server; test -x /usr/local/bin/pg_dump')" || fail_blocked "container runtime contract failed" "$log"
app_container_id="$(docker compose -p "$project" -f "$compose" ps -q app)"; [[ -n "$app_container_id" ]] || fail_blocked "app container identity unavailable" "$log"
restart_count_before="$(docker inspect --format '{{.RestartCount}}' "$app_container_id")"; events_since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
crash_events_file="$workdir/app-die-events.jsonl"; docker events --filter "container=$app_container_id" --filter event=die --format '{{json .}}' >"$crash_events_file" 2>/dev/null & crash_events_pid=$!
seed_id="embedded-fixture-business-row"; seed_sql="INSERT INTO managed_anime (id,title) VALUES ('$seed_id','Embedded fixture business row')"
docker compose -p "$project" -f "$compose" exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "$1"' sh "$seed_sql" >>"$log" 2>&1 || fail_blocked "real PostgreSQL business seed failed" "$log"
seed_rows_before="$(docker compose -p "$project" -f "$compose" exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -At -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT count(*) FROM managed_anime WHERE id='\''embedded-fixture-business-row'\''"' || true)"
schema_before="$(docker compose -p "$project" -f "$compose" exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -At -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT max(version) FROM schema_migrations"' || true)"
pointer_before="$(docker run --rm -v "$volume":/updates alpine:3.20 sh -c 'cat /updates/current 2>/dev/null || true')"
owner_body="$workdir/owner.json"; curl -fsS -X POST "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/bootstrap/owner" -H 'Content-Type: application/json' --data '{"username":"fixture-owner","password":"correct horse battery","session_transport":"token"}' >"$owner_body" || fail_blocked "bootstrap owner API failed"
session="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["session_token"])' "$owner_body")"
login_body="$workdir/login.json"; curl -fsS -X POST "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/auth/login" -H 'Content-Type: application/json' --data '{"username":"fixture-owner","password":"correct horse battery","session_transport":"token"}' >"$login_body" || fail_blocked "owner login API failed"
session="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["session_token"])' "$login_body")"
check_body="$workdir/check.json"; check_code="$(curl -sS -o "$check_body" -w '%{http_code}' -H "Authorization: Bearer $session" "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/update/check" || true)"
[[ "$check_code" == 200 ]] || fail_blocked "real embedded update check failed" "http=$check_code"
candidate_json="$workdir/candidate.json"; python3 - "$check_body" "$candidate_json" <<'PY'
import json,pathlib,sys
candidate=json.loads(pathlib.Path(sys.argv[1]).read_text()).get("candidate")
if not isinstance(candidate,dict) or not candidate.get("release_id") or not candidate.get("manifest_digest"): raise SystemExit("candidate identity missing")
pathlib.Path(sys.argv[2]).write_text(json.dumps({"target_release_id":candidate["release_id"],"manifest_digest":candidate["manifest_digest"]})+"\n")
PY
admit_body="$workdir/admit.json"; admit_code="$(curl -sS -o "$admit_body" -w '%{http_code}' -X POST "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/update/admit" -H "Authorization: Bearer $session" -H 'Content-Type: application/json' -H 'Idempotency-Key: embedded-fixture-update029' --data-binary "@$candidate_json" || true)"
if [[ "$scenario" == min-image-version ]]; then
  [[ "$admit_code" == 409 ]] || fail_blocked "incompatible image admission was not rejected" "http=$admit_code"
  docker compose -p "$project" -f "$compose" exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -At -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT to_jsonb(t) FROM embedded_update_locks t"' >"$workdir/fence-negative.json"
  docker run --rm -v "$volume":/updates alpine:3.20 sh -c 'test ! -e /updates/pending.json && test ! -e /updates/journal.json && test ! -e /updates/current && test ! -d /updates/staging' || fail_blocked "incompatible admission wrote update effects"
  curl -fsS "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/readyz" >"$workdir/negative-ready.json" || fail_blocked "incompatible admission changed readiness"
  result="$workdir/ac-b6-docker.json"
  python3 - "$admit_body" "$workdir/fence-negative.json" "$result" "$source_commit" "$image_id" <<'PY'
import json,pathlib,sys
admit,fence,out,commit,image=sys.argv[1:]
body=json.loads(pathlib.Path(admit).read_text()); f=[json.loads(x) for x in pathlib.Path(fence).read_text().splitlines()]
assert 'UPDATE_REQUIRES_IMAGE' in json.dumps(body), body
assert f == [], f
pathlib.Path(out).write_text(json.dumps({'acceptance':'ac-b6','passed':True,'blocked':False,'scope':'docker-fixture','evidence_kind':'real_docker','self_test':False,'error_code':'UPDATE_REQUIRES_IMAGE','journal_effect':False,'fence_effect':False,'staging_effect':False,'ready':True,'source_commit':commit,'image_id':image},indent=2)+'\n')
PY
  cat "$result"
  exit 0
fi
[[ "$admit_code" == 200 || "$admit_code" == 201 || "$admit_code" == 202 ]] || fail_blocked "real embedded update admission failed" "http=$admit_code"
operation_id="$(python3 - "$admit_body" <<'PY'
import json,sys
try: print(json.load(open(sys.argv[1])).get("operation_id", ""))
except Exception: print("")
PY
)"
if [[ -z "$operation_id" && -f "$volume/pending.json" ]]; then operation_id=""; fi
if [[ "$scenario" == crash-staging ]]; then operation_id="staging-only"; fi
crash_record="not_requested"; crash_observed_step=""; restart_count_after="$restart_count_before"; daemon_restart_observed="not_requested"
if [[ "$scenario" == daemon-restart-bootstrap ]]; then
  for _ in $(seq 1 90); do [[ -f "$workdir/pending-marker-seen" ]] && break; docker run --rm -v "$volume":/updates alpine:3.20 sh -c '[ -f /updates/pending.json ]' >/dev/null 2>&1 && touch "$workdir/pending-marker-seen"; sleep 2; done
  [[ -f "$workdir/pending-marker-seen" ]] || fail_blocked "pending marker was not created"
  docker info --format '{{json .}}' >"$workdir/daemon-before.json" || fail_blocked "daemon unavailable before restart"
  # Bound each client probe: dockerd may hold an established socket open while
  # restarting; an unbounded `docker info` can wait through reconnect and miss
  # the only disconnect interval entirely.
  (seen=0; for _ in $(seq 1 120); do timeout 1 docker info >/dev/null 2>&1 || seen=1; printf '%s\n' "$seen" >"$workdir/daemon-disconnect-observed"; sleep 1; done) & daemon_monitor_pid=$!
  BANSOKO_EMBEDDED_SCENARIO="$scenario" bash -c "$daemon_restart_cmd" >>"$log" 2>&1 || fail_blocked "operator daemon restart hook failed"
  for _ in $(seq 1 120); do docker info --format '{{json .}}' >"$workdir/daemon-after.json" 2>/dev/null && break; sleep 1; done
  kill "$daemon_monitor_pid" 2>/dev/null || true; wait "$daemon_monitor_pid" 2>/dev/null || true; daemon_monitor_pid=""
  [[ "$(cat "$workdir/daemon-disconnect-observed" 2>/dev/null || true)" == 1 && -s "$workdir/daemon-after.json" ]] || fail_blocked "daemon restart disconnect/reconnect not observed"
  daemon_restart_observed="disconnect_and_reconnect"
fi
if [[ -n "$crash_target_step" ]]; then
  crashed=0
  for _ in $(seq 1 120); do
    if [[ "$scenario" == crash-staging ]]; then
      if (( $(app_log_count 'FIXTURE INJECTION: before marker') > 0 )); then crash_observed_step="$crash_target_step"; crashed=1; break; fi
    elif [[ "$scenario" == crash-migrate ]]; then
      # The injection fires at migration_succeeded, but the container restart
      # policy may already have replayed the flow past it before this poll runs,
      # so any step at or beyond the injected checkpoint also evidences it.
      case "$(journal_step)" in
        migration_succeeded|promoted|readiness_failed|succeeded|rolled_back|rollback_blocked|failed) crash_observed_step="$crash_target_step"; crashed=1; break ;;
      esac
    elif docker run --rm -v "$volume":/updates alpine:3.20 sh -c 'test -f /updates/pending.json || test -f /updates/journal.json' >/dev/null 2>&1; then
      # The bootstrap may consume the marker (and delete it) between this check
      # and the read, so the read is best-effort: the boundary is the durable
      # marker OR the journal it becomes, not the marker surviving the poll.
      marker="$(docker run --rm -v "$volume":/updates alpine:3.20 sh -c 'cat /updates/pending.json 2>/dev/null' || true)"
      crash_observed_step="$crash_target_step"; crashed=1; break
    fi
    sleep 2
  done
  [[ "$crashed" == 1 ]] || fail_blocked "disposable crash boundary was not reached" "$scenario"
  if [[ "$scenario" != daemon-restart-bootstrap ]]; then
    docker restart "$app_container_id" >/dev/null 2>&1 || fail_blocked "bootstrap crash recovery restart failed" "$scenario"
  fi
  for _ in $(seq 1 120); do
    step="$(journal_step)"; ready_code="$(curl -sS -o "$workdir/ready.json" -w '%{http_code}' "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/readyz" || true)"
    if [[ "$step" == succeeded && "$ready_code" == 200 ]]; then break; fi
    if [[ "$scenario" == crash-staging && "$ready_code" == 200 ]]; then break; fi
    sleep 2
  done
  restart_count_after="$(docker inspect --format '{{.RestartCount}}' "$app_container_id" 2>/dev/null || true)"
  crash_record="fixture termination/restart at $crash_target_step"
else
  # No crash window was armed, but the marker handoff still moves the work out
  # of the server: the running application stages, marks, stops, and the
  # bootstrap performs backup, migration, promotion and readiness afterwards. The
  # terminal evidence below is only meaningful once that chain reaches its own
  # terminal state, so every non-crash row waits for it here.
  for _ in $(seq 1 150); do
    step="$(journal_step)"
    ready_code="$(curl -sS -o "$workdir/ready.json" -w '%{http_code}' "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/readyz" || true)"
    case "$scenario" in
      normal) [[ "$step" == succeeded && "$ready_code" == 200 ]] && break ;;
      migrate-failure|readiness-failure) [[ "$step" == rolled_back && "$ready_code" == 200 ]] && break ;;
      rollback-blocked) [[ "$step" == rollback_blocked ]] && break ;;
      *) break ;;
    esac
    sleep 2
  done
fi
status_code="$(curl -sS -o "$workdir/status.json" -w '%{http_code}' -H "Authorization: Bearer $session" "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/update/status" || true)"
final_status="$(cat "$workdir/status.json" 2>/dev/null || true)"; ready_after="$(curl -sS -w '\n%{http_code}' "http://127.0.0.1:${BANSOKO_APP_PORT}/api/v1/readyz" || true)"
pointer="$(docker run --rm -v "$volume":/updates alpine:3.20 sh -c 'cat /updates/current 2>/dev/null || true')"
volume_listing="$(docker run --rm -v "$volume":/updates alpine:3.20 sh -c 'find /updates -maxdepth 3 -type d -o -type f | sort' 2>&1 || true)"
marker_json="$(docker run --rm -v "$volume":/updates alpine:3.20 sh -c 'cat /updates/pending.json 2>/dev/null || true')"
backup_sidecars="$workdir/backup-sidecars.json"; docker run --rm -v "$volume":/updates alpine:3.20 sh -c 'for dump in /updates/backups/*.dump; do [ -f "$dump" ] || continue; side="$dump.sha256"; [ -f "$side" ] || exit 2; actual="$(sha256sum "$dump" | cut -d" " -f1)"; stated="$(tr -d "[:space:]" < "$side")"; [ "$actual" = "$stated" ] || exit 3; printf "%s|%s\n" "${dump##*/}" "$actual"; done' >"$backup_sidecars" || fail_blocked "backup sidecar verification failed"
db_evidence="$workdir/database.txt"; db_query="SELECT i.identity,i.baseline_version,(SELECT max(version) FROM schema_migrations),EXISTS(SELECT 1 FROM schema_migrations WHERE version='${schema_version}.sql'),EXISTS(SELECT 1 FROM schema_migrations WHERE version=i.baseline_version),(SELECT count(*) FROM managed_anime WHERE id='$seed_id'),${seed_rows_before} FROM schema_identity i WHERE i.singleton_id=1"
docker compose -p "$project" -f "$compose" exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -At -F "|" -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "$1"' sh "$db_query" >"$db_evidence" 2>>"$log" || fail_blocked "database identity query failed"
db_diagnostics="$workdir/database-diagnostics.txt"; cat "$db_evidence" >"$db_diagnostics"
journal_raw="$(journal_document)"; fence_evidence="$workdir/fence.json"; printf '' >"$fence_evidence"
if [[ -n "$operation_id" ]]; then docker compose -p "$project" -f "$compose" exec -T postgres sh -c "psql -v ON_ERROR_STOP=1 -At -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -c \"SELECT COALESCE(row_to_json(t)::text, 'null') FROM (SELECT op_id, admitted_by, sequence, manifest_digest, state, created_at, released_at FROM embedded_update_locks WHERE op_id='$operation_id') t\"" >"$fence_evidence" 2>>"$log" || true; fi
compose_status="$workdir/compose-status.txt"; docker compose -p "$project" -f "$compose" ps -a >"$compose_status" 2>&1 || true
bootstrap_app_logs="$workdir/bootstrap-app.log"; docker compose -p "$project" -f "$compose" logs --no-color --timestamps app >"$bootstrap_app_logs" 2>&1 || true
if [[ -n "${crash_events_pid:-}" ]]; then kill "$crash_events_pid" 2>/dev/null || true; wait "$crash_events_pid" 2>/dev/null || true; crash_events_pid=""; fi
events_until="$(date -u +%Y-%m-%dT%H:%M:%SZ)"; docker events --since "$events_since" --until "$events_until" --filter "container=$app_container_id" --filter event=die --format '{{json .}}' >"$workdir/app-die-events-replay.jsonl" 2>/dev/null || true; cat "$workdir/app-die-events-replay.jsonl" >>"$crash_events_file"
crash_die_exit_codes="$(python3 - "$crash_events_file" <<'PY'
import json,pathlib,sys
codes=[]
for line in pathlib.Path(sys.argv[1]).read_text(errors="replace").splitlines():
    try: codes.append(str(((json.loads(line).get("Actor") or {}).get("Attributes") or {}).get("exitCode","")))
    except Exception: pass
print(",".join(c for c in codes if c))
PY
)"
restart_count_final="$(docker inspect --format '{{.RestartCount}}' "$app_container_id" 2>/dev/null || true)"
mapfile -t row_containers < <(docker ps -aq --filter "label=bansoko.update-e2e.run=${BANSOKO_EMBEDDED_E2E_RUN_ID:-standalone}")
mapfile -t row_volumes < <(docker volume ls -q --filter "label=bansoko.update-e2e.run=${BANSOKO_EMBEDDED_E2E_RUN_ID:-standalone}")
if (( ${#row_containers[@]} > 0 || ${#row_volumes[@]} > 0 )); then
  docker compose -p "$project" -f "$compose" down -v --remove-orphans >/dev/null 2>&1 || true
  (( ${#row_containers[@]} == 0 )) || docker rm -f "${row_containers[@]}" >/dev/null 2>&1 || true
  (( ${#row_volumes[@]} == 0 )) || docker volume rm "${row_volumes[@]}" >/dev/null 2>&1 || true
fi
if docker ps -aq --filter "label=bansoko.update-e2e.run=${BANSOKO_EMBEDDED_E2E_RUN_ID:-standalone}" | grep -q . || docker volume ls -q --filter "label=bansoko.update-e2e.run=${BANSOKO_EMBEDDED_E2E_RUN_ID:-standalone}" | grep -q .; then resources_clean=false; else resources_clean=true; fi
python3 - "$result" "$project" "$volume" "$image" "$operation_id" "$final_status" "$ready_after" "$pointer" "$volume_listing" "$db_evidence" "$db_diagnostics" "$bootstrap_app_logs" "$compose_status" "$target_id" "$target_version" "$schema_version" "$seed_id" "$scenario" "$journal_raw" "$fence_evidence" "$pointer_before" "$schema_before" "$seed_rows_before" "$marker_json" "$crash_record" "$crash_observed_step" "$crash_die_exit_codes" "$restart_count_before" "$restart_count_after" "$daemon_restart_observed" "$source_commit" "$source_dirty" "$source_tree_hash" "$image_id" "$inspect" "$resources_clean" <<'PY'
import json,pathlib,re,sys
(out,project,volume,image,op,status_raw,ready_raw,pointer,listing,db_path,diagnostics_path,logs_path,compose_path,target_id,target_version,target_schema,seed_id,scenario,journal_raw,fence_path,pointer_before,schema_before,seed_rows_before,marker_raw,crash_record,crash_checkpoint,die_codes,restart_before,restart_after,daemon_restart,source_commit,source_dirty,source_tree_hash,image_id,runtime_env,resources_clean)=sys.argv[1:]
def blocked(reason):
    pathlib.Path(out).write_text(json.dumps({"acceptance":"ac-b2" if scenario.startswith("crash-") else ("ac-b1" if scenario=="normal" else "ac-b3" if scenario=="migrate-failure" else "ac-b4"),"scenario":scenario,"scope":"docker-fixture","evidence_kind":"real_docker","self_test":False,"passed":False,"blocked":True,"reason":reason,"resources_clean":None},indent=2,sort_keys=True)+"\n"); raise SystemExit(reason)
try:
    status=json.loads(status_raw) if status_raw.strip() else {}
except Exception as exc:
    # rollback_blocked intentionally exposes only the maintenance responder;
    # its non-health routes are plain-text 404s, not application JSON.
    if scenario == "rollback-blocked" and status_raw.strip() == "404 page not found": status={}
    else: blocked(f"invalid terminal status evidence: {exc}")
try:
    ready_text,code=ready_raw.rsplit("\n",1); ready=json.loads(ready_text) if code!="000" and ready_text else {}
except Exception as exc: blocked(f"invalid terminal readiness evidence: {exc}")
if resources_clean!="true": blocked("fixture Docker containers or volumes leaked")
try: journal=json.loads(journal_raw) if journal_raw.strip() else {}; record=journal.get("record") or journal
except Exception as exc: blocked(f"invalid journal: {exc}")
try: db=pathlib.Path(db_path).read_text().strip().split("|")
except Exception as exc: blocked(f"database evidence missing: {exc}")
if resources_clean!="true": blocked("fixture Docker containers or volumes leaked")
if len(db)!=7: blocked("database identity output malformed")
identity,baseline,migration,target_exists,baseline_exists,seed_after,seed_before=db
paths=[entry.split("/updates/",1)[-1] for entry in listing.splitlines()]
temporary=[p for p in paths if re.search(r"(^|/)(\.|.*\.tmp|.*\.partial|.*\.inprogress)$",p)]
if temporary: blocked("updates volume contains temporary files")
if seed_after!="1" or seed_before!="1" or identity!="bansoko:baseline" or not baseline or not baseline_exists.lower()=="t": blocked("database identity/baseline/business seed mismatch")
terminal=str(record.get("step","")); expected_ac="ac-b1" if scenario=="normal" else "ac-b2" if scenario.startswith("crash-") else "ac-b3" if scenario=="migrate-failure" else "ac-b4"
if scenario in ("normal","crash-marker-before-backup","crash-after-backup","crash-migrate"):
    if terminal!="succeeded" or status.get("step")!="succeeded" or pointer.strip()!=target_id or code!="200" or ready.get("ready") is not True: blocked("update replay did not succeed with target pointer and ready service")
    if migration!=f"{target_schema}.sql" or target_exists.lower()!="t": blocked("target schema migration absent")
if scenario=="daemon-restart-bootstrap":
    # A daemon restart also restarts the serving containers, so the supervised
    # target can lose its readiness window through no fault of the release. The
    # PRD requires the bootstrap phase to converge (row 5: "completes or rolls
    # back cleanly"), not to force success, so both terminal outcomes are
    # accepted with the serving state each one implies.
    if daemon_restart!="disconnect_and_reconnect": blocked("daemon restart transition was not recorded in evidence")
    if terminal not in ("succeeded","rolled_back"): blocked("daemon restart during the bootstrap phase did not converge")
    if code!="200" or ready.get("ready") is not True: blocked("daemon restart left no serving release")
    if terminal=="succeeded" and pointer.strip()!=target_id: blocked("daemon restart converged to succeeded without the target pointer")
    if terminal=="rolled_back" and pointer.strip()!=pointer_before.strip(): blocked("daemon restart rollback did not restore the previous pointer")
if scenario in ("readiness-failure","migrate-failure"):
    if terminal!="rolled_back" or pointer.strip()!=pointer_before.strip() or code!="200" or ready.get("ready") is not True: blocked(scenario+" did not restore old release")
if scenario=="rollback-blocked":
    if terminal!="rollback_blocked" or pointer.strip()!=pointer_before.strip() or code not in ("000","503"): blocked("rollback-blocked did not remain fail-closed")
if scenario.startswith("crash-"):
    if not crash_checkpoint: blocked("crash boundary was not evidenced")
    if "70" not in die_codes: blocked("Docker did not observe fixture crash exit code 70")
if scenario=="daemon-restart-bootstrap" and not crash_checkpoint: blocked("daemon restart boundary was not evidenced")
if scenario=="crash-staging":
    # A pre-marker crash is never resumed: the next start discards the
    # unreferenced work and keeps serving the release that was already current.
    if "pending.json" in paths: blocked("pre-marker crash left pending marker")
    if any(p.startswith(f"releases/{target_id}/") for p in paths): blocked("staging crash did not remove unreferenced release tree")
    if pointer.strip()==target_id or code!="200" or ready.get("ready") is not True: blocked("pre-marker crash did not keep the old release serving")
    try:
        staged_status=json.loads(status_raw) if status_raw.strip() else {}
    except Exception: staged_status={}
    if staged_status.get("step")=="succeeded": blocked("pre-marker crash reported a completed update")
if marker_raw:
    try: marker=json.loads(marker_raw)
    except Exception as exc: blocked(f"pending marker invalid: {exc}")
    required={"operation_id","release_id","manifest_digest","release_sequence","previous_release_id"}
    if set(marker)!=required: blocked("pending marker fields differ from exact contract")
if scenario.startswith("crash-") and scenario!="crash-staging" and marker_raw: blocked("bootstrap did not consume disposable pending marker")
if pointer.strip()==target_id and any(p.startswith(f"releases/{target_id}/") for p in paths) and f"provenance/{target_id}.json" not in paths: blocked("current release provenance missing")
if scenario=="crash-after-backup" and "backups/" not in "\n".join(paths): blocked("verified backup missing")
try: fence=json.loads(pathlib.Path(fence_path).read_text().strip() or "null")
except Exception: fence=None
if terminal=="succeeded" and op and (not isinstance(fence,dict) or fence.get("op_id")!=op or fence.get("sequence")!=2 or fence.get("manifest_digest")!=record.get("manifest_digest") or fence.get("state")!="settled" or not fence.get("released_at")): blocked("terminal operation fence identity mismatch")
if terminal=="rolled_back" and op and (not isinstance(fence,dict) or fence.get("op_id")!=op or fence.get("manifest_digest")!=record.get("manifest_digest") or fence.get("state")!="failed" or not fence.get("released_at")): blocked("rolled-back operation fence identity mismatch")
if scenario=="rollback-blocked" and (not isinstance(fence,dict) or fence.get("op_id")!=op or fence.get("sequence")!=2 or fence.get("manifest_digest")!=record.get("manifest_digest") or fence.get("state")!="active" or fence.get("released_at") is not None): blocked("rollback-blocked exact fence was not retained open")
data={"acceptance":expected_ac,"scenario":scenario,"scope":"docker-fixture","evidence_kind":"real_docker","self_test":False,"passed":True,"blocked":False,"operation_id":op,"status":status,"source":{"base_head":source_commit,"dirty":source_dirty=="true","tree_hash":source_tree_hash},"image_id":image_id,"crash_checkpoint":crash_checkpoint or None,"crash_exit_code_observed":"70" in die_codes if scenario.startswith("crash-") else None,"daemon_restart_observed":daemon_restart,"terminal_step":terminal,"pointer_role":"target" if pointer.strip()==target_id else "previous_or_base","ready":code=="200" and ready.get("ready") is True,"artifacts":{"pending_marker_present":bool(marker_raw),"backup_dump_count":sum(p.startswith("backups/") and p.endswith(".dump") for p in paths),"archive_present":any(p.startswith("archive/journal-") for p in paths),"temporary_files":len(temporary)},"resources_clean":resources_clean=="true"}
data["resources_clean"]=resources_clean=="true"
pathlib.Path(out).write_text(json.dumps(data,indent=2,sort_keys=True)+"\n"); print(json.dumps(data,indent=2,sort_keys=True))
PY
fixture_status=$?
set -e
cp "$result" "$evidence_dir/${result##*/}" 2>/dev/null || true
exit "$fixture_status"
