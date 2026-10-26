#!/usr/bin/env bash
# Real published-image -> baked-channel update proof (AC-B1 normal hook).
#
# Drives the complete embedded-update chain against only real products: a
# published immutable base image, the baked raw.githubusercontent.com update
# channel, the real bootstrap/API surface, and the real overlay installation.
# No fixture manifest, no fixture key, no locally served payload.
#
# Run it through scripts/verify/embedded-update-e2e.sh --scenario=normal,
# which renders the isolated compose copy, creates the per-run updates volume,
# and validates this hook's evidence file. The hook must be exported as
# BANSOKO_EMBEDDED_E2E_NORMAL_CMD together with:
#   EXPECTED_RELEASE_ID       channel release the proof must land on (v<X.Y.Z>)
#   EXPECTED_MANIFEST_DIGEST  optional; check must return exactly this candidate
#   EXPECTED_SEQUENCE         optional; channel sequence the proof must land on
#   BANSOKO_EMBEDDED_E2E_PROOF_HTTP_PROXY  optional proxy for the app's
#                             outbound channel fetch (standard container vars)
#
# This hook strips the compose update-source overrides so the container
# inherits the image's baked channel — that inheritance is the point of the
# proof; an explicitly configured channel would be weaker evidence.
#
# The runner owns teardown (project-scoped down -v). Session tokens and the
# generated owner password live only in the runner's 0700 workdir and are
# never copied into the evidence file.
set -euo pipefail
umask 077

fail() { echo "proof failed: $1" >&2; exit 1; }

# Defense in depth: the caller verifies the VM identity before any Docker
# command; this guard runs after the runner has already touched Docker.
[ "$(uname -s)" = "Linux" ] || fail "must run on the Linux test VM"
[ "$(id -u)" != "0" ] || fail "must not run as root"

: "${E2E_PROJECT:?runner must set E2E_PROJECT}" \
  "${E2E_WORKDIR:?runner must set E2E_WORKDIR}" \
  "${E2E_COMPOSE_FILE:?runner must set E2E_COMPOSE_FILE}" \
  "${E2E_UPDATES_VOLUME:?runner must set E2E_UPDATES_VOLUME}" \
  "${E2E_BASE_IMAGE_DIGEST:?runner must set E2E_BASE_IMAGE_DIGEST}" \
  "${E2E_EVIDENCE_FILE:?runner must set E2E_EVIDENCE_FILE}"
expected_release="${EXPECTED_RELEASE_ID:?EXPECTED_RELEASE_ID is required}"
expected_manifest_digest="${EXPECTED_MANIFEST_DIGEST:-}"
expected_sequence="${EXPECTED_SEQUENCE:-}"
proxy="${BANSOKO_EMBEDDED_E2E_PROOF_HTTP_PROXY:-}"

compose() { docker compose -p "$E2E_PROJECT" -f "$E2E_COMPOSE_FILE" "$@"; }
api() { curl --noproxy '*' -sS "$@"; }

# 1. Point the isolated copy at the baked channel: remove the compose-level
#    update-source overrides (they would render as empty strings and shadow the
#    image ENV) and bind the app port to loopback only.
python3 - "$E2E_COMPOSE_FILE" "$proxy" <<'PY'
import pathlib, sys
path, proxy = pathlib.Path(sys.argv[1]), sys.argv[2]
lines = path.read_text().splitlines(True)
keep = [line for line in lines if not line.lstrip().startswith((
    "BANSOKO_UPDATE_MANIFEST_URL:",
    "BANSOKO_UPDATE_CHANNEL_URL:",
    "BANSOKO_UPDATE_PACKAGE_ROOT:",
))]
text = "".join(keep)
old_port = '- "${BANSOKO_APP_PORT:-8000}:8080"'
if old_port not in text:
    raise SystemExit("app port anchor missing")
text = text.replace(old_port, '- "127.0.0.1:${BANSOKO_APP_PORT:-8000}:8080"')
if proxy:
    anchor = "      BANSOKO_UPDATES_ROOT:"
    index = next((i for i, line in enumerate(text.splitlines(True)) if line.startswith(anchor)), None)
    if index is None:
        raise SystemExit("app updates-root anchor missing")
    lines = text.splitlines(True)
    lines[index + 1:index + 1] = [
        f"      HTTP_PROXY: {proxy}\n",
        f"      HTTPS_PROXY: {proxy}\n",
        "      NO_PROXY: localhost,127.0.0.1,postgres\n",
    ]
    text = "".join(lines)
path.write_text(text)
PY

# 2. Isolated credentials and free loopback ports; nothing is printed.
ports="$(python3 - <<'PY'
import socket
def free():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port
print(free(), free())
PY
)"
app_port="${ports% *}"; db_port="${ports#* }"
export BANSOKO_APP_PORT="$app_port" BANSOKO_DB_PORT="$db_port"
export BANSOKO_DB_PASSWORD="$(python3 -c 'import secrets; print(secrets.token_urlsafe(24))')"
base_url="http://127.0.0.1:${app_port}"

# 3. Start the real stack from the immutable base digest (anonymous pull).
compose up -d postgres app || fail "compose startup failed"
app_container="$(compose ps -q app)"
[ -n "$app_container" ] || fail "app container identity unavailable"

# 4. Baked-channel proof: the running image must carry exactly the GitHub raw
#    pair. Only these two variables are read, never the full environment.
baked_channel="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$app_container" \
  | grep '^BANSOKO_UPDATE_CHANNEL_URL=' | cut -d= -f2-)"
baked_root="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$app_container" \
  | grep '^BANSOKO_UPDATE_PACKAGE_ROOT=' | cut -d= -f2-)"
[ "$baked_channel" = "https://raw.githubusercontent.com/wangkayu/bansoko/update-channel/channel-v1.json" ] \
  || fail "baked channel URL is not the GitHub raw channel: $baked_channel"
[ "$baked_root" = "https://raw.githubusercontent.com/wangkayu/bansoko/update-channel/" ] \
  || fail "baked package root is not the GitHub raw prefix: $baked_root"

# 5. Wait out the first migrate, then create the owner through the real API.
wait_url() {
  local url="$1" what="$2" deadline=$((SECONDS + $3)) code=""
  while (( SECONDS < deadline )); do
    code="$(api -o /dev/null -w '%{http_code}' "$url" 2>/dev/null || true)"
    [ "$code" = "200" ] && return 0
    sleep 3
  done
  fail "$what did not become ready (last code: $code)"
}
wait_url "$base_url/api/v1/livez" "livez" 240
wait_url "$base_url/api/v1/readyz" "readyz after first migrate" 300

owner_password="$(python3 -c 'import secrets; print(secrets.token_urlsafe(24))')"
owner_body="$E2E_WORKDIR/proof-owner.json"
owner_code="$(api -o "$owner_body" -w '%{http_code}' -X POST "$base_url/api/v1/bootstrap/owner" \
  -H 'Content-Type: application/json' \
  --data '{"username":"update-proof","password":"'"$owner_password"'","session_transport":"token"}')"
case "$owner_code" in 200|201) ;; *) fail "bootstrap owner API returned $owner_code" ;; esac
session="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["session_token"])' "$owner_body")"
[ -n "$session" ] || fail "bootstrap owner returned no session token"

# 6. Check must offer exactly the anonymously verified candidate.
check_body="$E2E_WORKDIR/proof-check.json"
check_code="$(api -o "$check_body" -w '%{http_code}' -H "Authorization: Bearer $session" "$base_url/api/v1/update/check")"
[ "$check_code" = "200" ] || fail "update/check returned $check_code"
candidate_json="$E2E_WORKDIR/proof-candidate.json"
python3 - "$check_body" "$expected_release" "$expected_manifest_digest" "$expected_sequence" "$candidate_json" <<'PY'
import json, pathlib, sys
body_path, expected_release, expected_digest, expected_sequence, out_path = sys.argv[1:]
data = json.loads(pathlib.Path(body_path).read_text())
candidate = data.get("candidate")
if not isinstance(candidate, dict) or not candidate.get("release_id") or not candidate.get("manifest_digest"):
    raise SystemExit("check returned no usable candidate")
if candidate["release_id"] != expected_release:
    raise SystemExit(f"candidate {candidate['release_id']} is not the expected {expected_release}")
if expected_digest and candidate["manifest_digest"] != expected_digest:
    raise SystemExit("candidate manifest digest does not match the anonymously verified manifest")
compatibility = (data.get("compatibility") or {}).get("kind", "")
if compatibility and compatibility != "overlay":
    raise SystemExit(f"compatibility kind is {compatibility}, want overlay")
sequence = candidate.get("release_sequence") or data.get("release_sequence")
if expected_sequence and sequence and str(sequence) != expected_sequence:
    raise SystemExit(f"candidate sequence {sequence} is not the expected {expected_sequence}")
pathlib.Path(out_path).write_text(json.dumps(
    {"target_release_id": candidate["release_id"], "manifest_digest": candidate["manifest_digest"]}) + "\n")
PY

# 7. One admission with a fresh idempotency key.
admit_body="$E2E_WORKDIR/proof-admit.json"
admit_code="$(api -o "$admit_body" -w '%{http_code}' -X POST "$base_url/api/v1/update/admit" \
  -H "Authorization: Bearer $session" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(python3 -c 'import uuid; print(uuid.uuid4())')" \
  --data-binary "@$candidate_json")"
case "$admit_code" in 200|201|202) ;; *) fail "update/admit returned $admit_code" ;; esac
operation_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("operation_id",""))' "$admit_body")"

# 8. Bounded wait for a terminal step. The bootstrap maintenance window serves
#    only livez/readyz, so status may legitimately 404 in between.
final_step=""
for _ in $(seq 1 200); do
  status_code="$(api -o "$E2E_WORKDIR/proof-status.json" -w '%{http_code}' \
    -H "Authorization: Bearer $session" "$base_url/api/v1/update/status" 2>/dev/null || true)"
  if [ "$status_code" = "200" ]; then
    final_step="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("step",""))' "$E2E_WORKDIR/proof-status.json" 2>/dev/null || true)"
    case "$final_step" in
      succeeded|failed|rolled_back|rollback_blocked) break ;;
    esac
  fi
  sleep 3
done
[ "$final_step" = "succeeded" ] || fail "update chain ended at step '${final_step:-<unreachable>}'"

# 9. Readiness must describe the installed overlay on the unchanged base.
ready_body="$E2E_WORKDIR/proof-ready.json"
ready_code="$(api -o "$ready_body" -w '%{http_code}' "$base_url/api/v1/readyz")"
[ "$ready_code" = "200" ] || fail "readyz returned $ready_code after success"

evidence="$(python3 - "$ready_body" "$admit_body" "$expected_release" "$E2E_BASE_IMAGE_DIGEST" "$baked_channel" <<'PY'
import json, pathlib, sys
ready_path, admit_path, expected_release, base_digest, baked_channel = sys.argv[1:]
ready = json.loads(pathlib.Path(ready_path).read_text())
admit = json.loads(pathlib.Path(admit_path).read_text())
identity = ready.get("deployment_identity") or {}
observations = {
    "baked_channel_url": baked_channel,
    "base_image_digest": identity.get("base_image_digest", ""),
    "selected_source": identity.get("selected_source", ""),
    "selected_release_id": identity.get("selected_release_id", ""),
    "payload_digest": identity.get("payload_digest", ""),
    "update_operation_id": identity.get("update_operation_id", ""),
    "process_build_id": identity.get("process_build_id", ""),
}
if observations["selected_source"] != "overlay":
    raise SystemExit(f"selected_source is {observations['selected_source']!r}, want overlay")
if observations["selected_release_id"] != expected_release:
    raise SystemExit(f"selected_release_id is {observations['selected_release_id']!r}, want {expected_release}")
if observations["base_image_digest"] != base_digest:
    raise SystemExit("base_image_digest changed across the update")
if observations["update_operation_id"] and admit.get("operation_id") \
        and observations["update_operation_id"] != admit["operation_id"]:
    raise SystemExit("readyz operation id does not match the admission")
print(json.dumps(observations, indent=2, sort_keys=True))
PY
)"

# 10. Evidence: sanitized identities only; scope=real is the runner's contract.
python3 - "$E2E_EVIDENCE_FILE" "$evidence" "$E2E_BASE_IMAGE_DIGEST" "$expected_release" <<'PY'
import json, pathlib, sys
out, evidence, base_digest, expected_release = sys.argv[1:]
pathlib.Path(out).write_text(json.dumps({
    "acceptance": "ac-b1",
    "scope": "real",
    "passed": True,
    "description": "published immutable image updated through its baked GitHub raw channel",
    "channel_mode": "baked-image-default",
    "base_image_digest": base_digest,
    "target_release_id": expected_release,
    "final_step": "succeeded",
    "deployment_identity": json.loads(evidence),
}, indent=2, sort_keys=True) + "\n")
PY
echo "github baked-channel update proof succeeded; release $expected_release on base $E2E_BASE_IMAGE_DIGEST"
