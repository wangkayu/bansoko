#!/usr/bin/env bash
# FileMaintenance end-to-end rehearsal on the disposable real-effect harness.
# It owns the acquisition/effect path; FILE-022's final slice extends this
# existing lane with offset and alignment real-file scenarios.
#
# Guards: uses scripts/ci/real-effect-harness.sh (disposable PG/qB/temp dirs,
# auth enabled); BANSOKO_SKIP_AUTH is forbidden by the harness.
set -euo pipefail
umask 077
# Negative path (see scripts/verify/filemaintenance-e2e-negative.sh):
# destroying one owned placement file must make this lane fail loudly instead of
# passing. The switch is explicit and unset in every normal run; the seven
# scenarios and their assertions keep their default behaviour.
# Parsed before the platform/tool guards so the refusal is checkable anywhere.
negative_mode="${BANSOKO_FM_E2E_NEGATIVE:-}"
case "$negative_mode" in
  ''|old-source-missing|new-placement-missing) ;;
  *) echo "filemaintenance-e2e: unknown BANSOKO_FM_E2E_NEGATIVE: $negative_mode (expected old-source-missing or new-placement-missing)" >&2; exit 2 ;;
esac
if [[ -n "$negative_mode" && -n "${BANSOKO_FM_E2E_SCENARIOS:-}" ]]; then
  echo 'filemaintenance-e2e: BANSOKO_FM_E2E_NEGATIVE owns the run; BANSOKO_FM_E2E_SCENARIOS must be unset' >&2
  exit 2
fi

for tool in bash go python3 curl docker; do
  if ! command -v "$tool" >/dev/null 2>&1; then echo "filemaintenance-e2e: required command missing before harness setup: $tool" >&2; exit 2; fi
done
if [[ "$(uname -s)" == Darwin ]]; then
  echo "filemaintenance-e2e: forbidden on Darwin; run this disposable real-effect lane on the authorized VM" >&2
  exit 2
fi

scenario_selector="${BANSOKO_FM_E2E_SCENARIOS:-}"
if [[ -z "$scenario_selector" && "${BANSOKO_FM_E2E_OFFSET_ALIGNMENT:-0}" == "1" ]]; then scenario_selector=all; fi
if [[ -n "$scenario_selector" ]]; then
  # Scenario 8 (scan-alignment recovery) has its own selector entry and is
  # deliberately NOT part of the default set: the default `all` alias stays the
  # seven scenarios the negative-proof wrapper pins and reports on. Run the
  # recovery scenario explicitly with BANSOKO_FM_E2E_SCENARIOS=8.
  [[ "$scenario_selector" != all ]] || scenario_selector="1,2,3,4,5,6,7"
  case ",$scenario_selector," in *,,*|*,*[!1-8,]*) echo "invalid BANSOKO_FM_E2E_SCENARIOS: $scenario_selector" >&2; exit 2 ;; esac
  requested_selector="$scenario_selector"
  if [[ ",$requested_selector," == *,5,* ]]; then requested_selector="$requested_selector,1,2,3,4"; fi
  if [[ ",$requested_selector," == *,3,* ]]; then requested_selector="$requested_selector,1,2"; fi
  if [[ ",$requested_selector," == *,2,* ]]; then requested_selector="$requested_selector,1"; fi
  if [[ ",$requested_selector," == *,4,* ]]; then requested_selector="$requested_selector,1,2,3"; fi
  # 6 occupies the path the alignment planner will use, which it learns from the
  # same preview 5 pins, so 6 implies 5.
  if [[ ",$requested_selector," == *,6,* ]]; then requested_selector="$requested_selector,1,2,3,4,5"; fi
  scenario_selector="$(printf '%s' "$requested_selector" | tr ',' '\n' | sort -u | paste -sd, -)"
  export BANSOKO_FM_E2E_OFFSET_ALIGNMENT=1
fi
scenario_requested() { [[ ",$scenario_selector," == *",$1,"* ]]; }
root="$(cd "$(dirname "$0")/../.." && pwd)"
export BANSOKO_HARNESS_STATE_DIR="$(mktemp -d /tmp/bansoko-harness-fm.XXXXXX)"
export GOCACHE="${GOCACHE:-/tmp/test-gocache}"
export GOMODCACHE="${GOMODCACHE:-/tmp/test-gomodcache}"
state_dir="$BANSOKO_HARNESS_STATE_DIR"
summary_dir="$(mktemp -d /tmp/bansoko-fm-e2e.XXXXXX)"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
durable_evidence_dir="/var/tmp/bansoko-fm-e2e-$run_id"
mkdir -m 700 "$durable_evidence_dir"
echo "durable evidence dir: $durable_evidence_dir"
echo "summary dir: $summary_dir"
cleanup() {
  local rc=$?
  trap - EXIT
  if [[ -d "${summary_dir:-}" ]]; then cp -R "$summary_dir/." "$durable_evidence_dir/"; fi
  if (( rc != 0 )) && [[ -n "${PSQL:-}" ]]; then
    {
      echo '== offset changes =='
      $PSQL -t -A -F $'\t' -c "SELECT id, rss_source_id, source_version, old_offset, new_offset, rekeyed_count, deleted_count, state FROM offset_alignment_changes ORDER BY created_at" || true
      echo '== alignment requests =='
      $PSQL -t -A -F $'\t' -c "SELECT r.placement_id, COALESCE(r.offset_change_id,''), COALESCE(r.outcome,''), r.requested_at FROM alignment_requests r JOIN placements p ON p.id=r.placement_id WHERE p.managed_anime_id='anime-e2e' ORDER BY r.requested_at" || true
      echo '== alignment operations =='
      $PSQL -t -A -F $'\t' -c "SELECT id, scan_generation, source_path, target_path, state, COALESCE(failure_code,''), COALESCE(reason,''), version FROM alignment_operations WHERE managed_anime_id='anime-e2e' ORDER BY created_at" || true
      echo '== calibration rows =='
      $PSQL -t -A -F $'\t' -c "SELECT generation, file_path, disposition, coverage::text FROM reconciliation_files WHERE managed_anime_id='anime-e2e' AND disposition IS NOT NULL ORDER BY generation, file_path" || true
      echo '== placements =='
      $PSQL -t -A -F $'\t' -c "SELECT id, physical_path, source_evidence_type, source_owner_ref, state, verification_state FROM placements WHERE managed_anime_id='anime-e2e' ORDER BY created_at" || true
      echo '== render lineage for active file-operation placements =='
      $PSQL -t -A -F $'\t' -c "SELECT p.id, p.physical_path, rel.id, rel.raw_facts->>'title', src.group_name, af.file_ref, mf.file_evidence, mf.effective_facts, snap.release_facts, fo.frozen_config_snapshot FROM placements p LEFT JOIN file_operations fo ON fo.id=p.source_owner_ref AND p.source_evidence_type='file_operation' LEFT JOIN artifact_files af ON af.id=fo.artifact_file_id LEFT JOIN artifacts art ON art.id=fo.artifact_id LEFT JOIN download_tasks dt ON dt.id=art.download_task_id LEFT JOIN releases rel ON rel.id=dt.release_id LEFT JOIN rss_sources src ON src.id=rel.rss_source_id LEFT JOIN LATERAL (SELECT id,release_facts FROM media_fact_snapshots WHERE release_id=rel.id ORDER BY created_at DESC,id DESC LIMIT 1) snap ON true LEFT JOIN media_fact_files mf ON mf.snapshot_id=snap.id AND mf.file_ref=af.file_ref WHERE p.managed_anime_id='anime-e2e' AND p.state='active'" || true
    } >>"$durable_evidence_dir/offset-failure-diagnostics.txt" 2>&1
  fi
  # The harness teardown does not know this script's own server; stop it first
  # so no orphaned server outlives its disposable database.
  if [[ -n "${server_pid:-}" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
    server_pid=""
  fi
  if [[ "${BANSOKO_FM_E2E_DEBUG_KEEP_HARNESS:-0}" == "1" && -f "$state_dir/state.json" ]]; then
    echo "DEBUG ONLY: preserving disposable harness state at $state_dir for inspection" >&2
    echo "DEBUG ONLY: teardown with BANSOKO_HARNESS_STATE_DIR='$state_dir' scripts/ci/real-effect-harness.sh down" >&2
    exit "$rc"
  fi
  if "${root}/scripts/ci/real-effect-harness.sh" down >/dev/null 2>&1; then
    rm -rf "$BANSOKO_HARNESS_STATE_DIR" "$summary_dir" 2>/dev/null || true
    exit "$rc"
  fi
  echo "FileMaintenance E2E teardown failed; credential-bearing harness state preserved at $BANSOKO_HARNESS_STATE_DIR" >&2
  echo "FileMaintenance E2E evidence retained at $durable_evidence_dir" >&2
  exit 1
}
trap cleanup EXIT

echo "== build frontend and controllable server binary =="
pnpm --dir "$root/frontend" build >"$summary_dir/frontend-build.log" 2>&1
server_bin="$root/.local-artifacts/e2e/bansoko-server"
mkdir -p "$(dirname "$server_bin")"
go build -o "$server_bin" ./cmd/bansoko-server
# vm-session.sh sync excludes .git and writes HEAD.txt instead, so a synced
# session has no checkout to ask. The commit is evidence, not a build input.
commit="$(git -C "$root" rev-parse HEAD 2>/dev/null || cat "$root/HEAD.txt" 2>/dev/null || true)"
[[ -n "$commit" ]] || { echo "filemaintenance-e2e: cannot determine the source commit (no git checkout and no HEAD.txt)" >&2; exit 2; }
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
server_sha="$(shasum -a 256 "$server_bin" | awk '{print $1}')"
frontend_dist_sha="$(shasum -a 256 "$root/frontend/dist/index.html" | awk '{print $1}')"
frontend_assets="$(printf '%s\n' "$root"/frontend/dist/assets/* | xargs -n1 basename | sort | paste -sd, -)"
ui_spec_sha="$(shasum -a 256 "$root/frontend/e2e/file-maintenance-alignment-live.spec.ts" | awk '{print $1}')"
echo "== harness up =="
"$root/scripts/ci/real-effect-harness.sh" up >"$summary_dir/harness.log" 2>&1 || {
  echo "harness up failed; see $summary_dir/harness.log" >&2
  exit 1
}
state_dir="${BANSOKO_HARNESS_STATE_DIR:-$(ls -dt /tmp/bansoko-harness.* | head -1)}"
# shellcheck disable=SC1091
source "$state_dir/env"
for required_harness_var in BANSOKO_HARNESS_LIBRARY_ROOT BANSOKO_HARNESS_DOWNLOAD_ROOT; do
  if [[ -z "${!required_harness_var:-}" ]]; then echo "filemaintenance-e2e: harness env is missing $required_harness_var" >&2; exit 2; fi
done
if [[ -z "${BANSOKO_HARNESS_STATE_DIR:-}" ]]; then echo 'filemaintenance-e2e: harness env is missing BANSOKO_HARNESS_STATE_DIR' >&2; exit 2; fi
if [[ -z "${BANSOKO_DATABASE_URL:-}" ]]; then echo 'filemaintenance-e2e: harness env is missing BANSOKO_DATABASE_URL' >&2; exit 2; fi
if [[ "${BANSOKO_TEST_DB_DISPOSABLE:-}" != 1 ]]; then echo 'filemaintenance-e2e: harness env must set BANSOKO_TEST_DB_DISPOSABLE=1' >&2; exit 2; fi
for harness_root in "$BANSOKO_HARNESS_LIBRARY_ROOT" "$BANSOKO_HARNESS_DOWNLOAD_ROOT"; do
  if [[ ! -d "$harness_root" ]]; then echo "filemaintenance-e2e: harness root is missing: $harness_root" >&2; exit 2; fi
done
library_root="$(cd "$BANSOKO_HARNESS_LIBRARY_ROOT" && pwd -P)"
download_root="$(cd "$BANSOKO_HARNESS_DOWNLOAD_ROOT" && pwd -P)"
echo "== start auth server (workers enabled) =="
listen="127.0.0.1:$(( 29000 + RANDOM % 1000 ))"
start_server() {
  (
    cd "$root"
    export BANSOKO_DATABASE_URL BANSOKO_TEST_DB_DISPOSABLE BANSOKO_SESSION_SECRET="e2e-session-secret-0123456789abcdef0123456789abcdef"
    export BANSOKO_SECRET_ENCRYPTION_KEY="e2e-enc-key-0123456789abcdef0123456789abcdef"
    export BANSOKO_ENVIRONMENT=development BANSOKO_SKIP_AUTH=false BANSOKO_LISTEN_ADDRESS="$listen"
    export BANSOKO_FRONTEND_DIR="$root/frontend/dist" BANSOKO_CONFIG_DIR
    export BANSOKO_DOWNLOAD_ROOT="$BANSOKO_HARNESS_DOWNLOAD_ROOT"
    exec "$server_bin"
  ) >>"$summary_dir/server.log" 2>&1 &
  server_pid=$!
}
start_server

live_ok=""
for _ in $(seq 1 60); do
  if curl -sf "http://$listen/api/v1/livez" >/dev/null 2>&1; then live_ok=1; break; fi
  sleep 1
done
if [[ -z "$live_ok" ]]; then
  echo "server did not start; see $summary_dir/server.log" >&2
  exit 1
fi
echo "server on $listen"

echo "== bootstrap owner + login =="
curl -s -X POST "http://$listen/api/v1/bootstrap/owner" \
  -H 'Content-Type: application/json' \
  -d '{"username":"owner","password":"e2e-owner-password","session_transport":"token"}' >/dev/null 2>&1 || true
login="$(curl -s -X POST "http://$listen/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"owner","password":"e2e-owner-password","session_transport":"token"}')"
token="$(printf '%s' "$login" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("session_token",""))' 2>/dev/null || true)"
if [[ -z "$token" ]]; then
  echo "owner login failed: $login" >&2
  exit 1
fi
echo "owner authenticated"

echo "== seed the anime chain + real source file =="
source_file="$BANSOKO_HARNESS_DOWNLOAD_ROOT/EP01.mkv"
# macOS /tmp is a symlink to /private/tmp; the inspector resolves symlinks
# (EvalSymlinks) while the target keeps the logical path, so both must agree
# on the physical path.
source_file="$(cd "$(dirname "$source_file")" && pwd -P)/EP01.mkv"
download_root="$(dirname "$source_file")"
head -c 65536 /dev/urandom >"$source_file"
source_hex="$(shasum -a 256 "$source_file" | awk '{print $1}')"
source_fp='{"algorithm":"sha256","value":"'$source_hex'","size_bytes":65536}'
# The superseded old placement must exist on disk for its deletion.
old_file="$BANSOKO_HARNESS_LIBRARY_ROOT/Old/EP01.mkv"
mkdir -p "$(dirname "$old_file")"
head -c 32768 /dev/urandom >"$old_file"
old_hex="$(shasum -a 256 "$old_file" | awk '{print $1}')"
# The typed fingerprint records the size: the deletion verifies path + size.
old_fp='{"algorithm":"sha256","value":"'$old_hex'","size_bytes":32768}'
PGID="$(python3 -c "import json; print(json.load(open('$state_dir/state.json'))['pg_id'])" 2>/dev/null || true)"
if [[ -z "$PGID" ]]; then
  echo "harness postgres container not found" >&2
  exit 1
fi
db_name="$(printf '%s' "$BANSOKO_DATABASE_URL" | sed -E 's#.*/([^/?]+).*#\1#')"
PSQL="docker exec -i $PGID psql -U harness -d $db_name -v ON_ERROR_STOP=1"
repeat_hex() { printf "%0.s$1" $(seq 64); }
# File identity inside one acquisition: the manifest seals one row per observed
# file, and the inspection commit compares the observed set against it exactly.
# The ref is the catalog's own torrent-file derivation, so the fixture binds the
# artifact, the media facts and the acquisition manifest to the same identity.
torrent_file_ref() {
  python3 - "$1" "$2" <<'PY'
import hashlib, sys
print("sha256:" + hashlib.sha256(b"torrent-file-v1\x00" + sys.argv[1].encode() + b"\x00" + sys.argv[2].encode()).hexdigest())
PY
}
acq_payload_digest="sha256:$(repeat_hex c)"
acq_manifest_digest="sha256:$(repeat_hex 8)"
acq_file_ref="$(torrent_file_ref "$acq_payload_digest" 'EP01.mkv')"
# The acceptance input is the golden registry vector, not a hand-written subset:
# the plan stage reads its frozen config, media, delivery and limits, and the
# admission digest is recomputed from these very bytes by the table's CHECK.
fixture_json="$(go run ./internal/testutil/frozenpolicy/cmd \
  --payload-digest "$acq_payload_digest" \
  --manifest-digest "$acq_manifest_digest" \
  --route-root "$library_root" \
  --title '[示例字幕组] 进击的巨人 - 01 [1080p][chs]')"
printf '%s' "$fixture_json" >"$summary_dir/acquisition-fixture.json"
python3 - "$summary_dir/acquisition-fixture.json" <<'PY'
import json,sys
fixture=json.load(open(sys.argv[1], encoding="utf-8"))
sections=json.loads(fixture["frozen_policy"]).get("effective_sections", {})
if not sections.get("parser"):
    raise SystemExit("frozenpolicy fixture missing effective_sections.parser")
if not json.loads(fixture["acquisition_input"]).get("config"):
    raise SystemExit("frozenpolicy fixture missing the accepted config")
PY
snapshot_identity="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["identity"]))' <<<"$fixture_json")"
snapshot_release_facts="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["release_facts"]))' <<<"$fixture_json")"
snapshot_file_evidence="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["file_evidence"]))' <<<"$fixture_json")"
snapshot_effective_facts="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["effective_facts"]))' <<<"$fixture_json")"
frozen_policy_sql="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["frozen_policy"])' <<<"$fixture_json" | sed "s/'/''/g")"
# The offset/alignment scenarios re-render with the anime's CURRENT rules, so
# each seeded instance must store all six live sections. Emit them from the
# same fixture the frozen policy came from; live and frozen then cannot drift.
config_overrides_for() {
  local anime_id="$1" fixture_file="$2"
  python3 - "$anime_id" "$fixture_file" <<'PY'
import json, sys
anime_id, fixture_path = sys.argv[1], sys.argv[2]
fixture = json.load(open(fixture_path, encoding="utf-8"))
sections = json.loads(fixture["acquisition_input"])["config"]["sections"]
lines = []
for entry in sections:
    key = entry["section_key"]
    owner = "subgroups" if key == "subgroups" else "rules"
    value = json.dumps(entry["value"], ensure_ascii=False, separators=(",", ":")).replace("'", "''")
    lines.append(
        "INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value) "
        f"VALUES ('{anime_id}', '{owner}', '{key}', '{entry['schema_version']}', '{value}'::jsonb);"
    )
print("\n".join(lines))
PY
}
config_overrides_sql="$(config_overrides_for anime-e2e "$summary_dir/acquisition-fixture.json")"
# The release-under-download hangs off an admission root (PRUNE-046): the task
# row requires its acquisition_id, and download_task_budget_guard() derives the
# whole budget from the root's frozen input->'limits' on every task write. The
# digest is the domain-separated hash the table's own CHECK recomputes from the
# canonical bytes, so the fixture cannot drift from it.
acq_input_sql="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["acquisition_input"])' <<<"$fixture_json")"
acq_input_sql="${acq_input_sql//\'/\'\'}"
if ! $PSQL -q -e >"$summary_dir/seed.log" 2>&1 <<E2ESQL
-- Retired work items are archived out of work_items quickly; the lane reads
-- live rows and archived receipts through one view so a finished chain is
-- still observable. work_item_history is already the union of the live queue
-- and the terminal outcomes, so reading it alone returns each row once; adding
-- work_items on top would duplicate every live row.
CREATE SCHEMA IF NOT EXISTS e2e_aux;
CREATE OR REPLACE VIEW e2e_aux.work_items_all AS
SELECT id, owner_type, owner_id, kind, state, attempt, max_attempts, last_error_code, last_error_reason, idempotency_key, created_at
FROM work_item_history;
-- The title must match the fixture's media fact (进击的巨人): offset/alignment
-- renders with the anime's CURRENT managed_anime title, and the scenario
-- asserts the move stays inside the same directory. Both bindings are what
-- the import gate needs; without them the import waits for configuration.
-- The owner season is the one the fixtures' frozen render named (Season 00),
-- so the current render keeps the imported files' directory.
INSERT INTO managed_anime(id, title) VALUES ('anime-e2e', '进击的巨人');
INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, season)
VALUES ('anime-e2e', 'e2e-bgm-work', 'e2e-tv-work', 'tv', 1, 0);
-- One anime_business_configs row carries every owner version and the three
-- policy axes; the S7 convergence replaced anime_config_owners,
-- anime_config_overrides and the instance_* policy snapshots with it.
INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-e2e');
$config_overrides_sql
-- Instances created through a draft always carry a tag_filters section (the
-- 042 backfill value is the canonical empty preset); a SQL-seeded instance
-- must store it too or the current-rules render fails closed.
INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
VALUES ('anime-e2e', 'rules', 'tag_filters', 'tag-filters-v2', '{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]}}'::jsonb);
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-e2e', 'anime-e2e', 'custom', 'E2E', 'active');
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-e2e', 'sub-e2e', 'custom_rss', 'https://feed.example.test/e2e', 'https://feed.example.test/e2e', '示例字幕组', '进击的巨人');
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('rel-e2e', 'src-e2e', 'guid:e2e', '{"title":"[示例字幕组] 进击的巨人 - 01 [1080p][chs]"}', '{"group_name":"示例字幕组"}',
        '{"episode":1,"group_name":"示例字幕组","group":"示例字幕组","resolution":"1080p","quality":"web-dl","revision":"v2","source":"web-dl","language":"chs","size_bytes":65536}', 'accepted_pending_download', 'accepted');
INSERT INTO release_candidate_facts(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys, resolution, revision, source, language, "group", size_bytes)
VALUES ('rel-e2e', 1, 'parser-v1', 'fp-e2e', '{"title":"[示例字幕组] 进击的巨人 - 01 [1080p][chs]"}', '{"episode":1,"group_name":"示例字幕组","group":"示例字幕组","resolution":"1080p","quality":"web-dl","revision":"v2","source":"web-dl","language":"chs","size_bytes":65536}',
        '["tv:e1"]', '1080p', 'v2', 'web-dl', 'chs', '示例字幕组', 65536);
-- Older generation evidence for the current placement: same coverage, lower
-- quality (720p < 1080p), so the frozen replacement chain must pick the new
-- file_operation placement as winner and supersede plc-old.
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('rel-old', 'src-e2e', 'guid:old', '{"title":"[示例字幕组] 进击的巨人 - 01 [720p][chs]"}', '{"group_name":"示例字幕组"}',
        '{"episode":1,"group_name":"示例字幕组","group":"示例字幕组","resolution":"720p","quality":"web-dl","revision":"v1","source":"web-dl","language":"chs","size_bytes":32768}', 'accepted_pending_download', 'accepted');
INSERT INTO release_candidate_facts(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys, resolution, revision, source, language, "group", size_bytes)
VALUES ('rel-old', 1, 'parser-v1', 'fp-old', '{"title":"[示例字幕组] 进击的巨人 - 01 [720p][chs]"}', '{"episode":1,"group_name":"示例字幕组","group":"示例字幕组","resolution":"720p","quality":"web-dl","revision":"v1","source":"web-dl","language":"chs","size_bytes":32768}',
        '["tv:e1"]', '720p', 'v1', 'web-dl', 'chs', '示例字幕组', 32768);
BEGIN;
-- The finished download's admission root, accepted two hours ago and still
-- running: the root terminalizes only once its file work settles, so every
-- stage after the download (inspection, plan, file operations) runs while it is
-- running and checks that here. retention_anchor_at = accepted_at, and the
-- receipt window is the accept-based one the CHECKs recompute.
INSERT INTO acquisitions(
  id, execution_branch, entry_kind, access_scope, request_namespace, request_key, request_hash,
  canonical_request, admission_kind, admission_http_status, admission_receipt,
  input_schema_version, admission_digest, origin_managed_anime_ref, source_release_ref,
  display_title, accepted_at, retention_anchor_at, detail_retention_seconds,
  receipt_after_accept_seconds, receipt_after_terminal_seconds, receipt_until, detail_until,
  state, terminal_at, terminal_schema, terminal_receipt, record_state,
  preparation_mode, vars_evidence_version,
  input_canonical_utf8, payload_digest, manifest_digest, manifest_item_count, manifest_sealed_at)
VALUES (
  'acq-e2e', 'download', 'automatic', 'deployment_owner_v1', 'auto_acquire_v1', 'auto:rel-e2e',
  'sha256:' || repeat('a', 64),
  '{"schema_version":1,"kind":"automatic","release_ref":"rel-e2e"}'::jsonb,
  'download_queued', 202,
  '{"schema_version":1,"acquisition_id":"acq-e2e","state":"queued"}'::jsonb,
  1,
  'sha256:' || encode(sha256(convert_to('acquisition-input:v1', 'UTF8') || decode('00', 'hex')
     || convert_to('$acq_input_sql', 'UTF8')), 'hex'),
  'anime-e2e', 'rel-e2e',
  '{"state":"known","value":"进击的巨人 - 01","reason_code":null}'::jsonb,
  now() - interval '2 hours', now() - interval '2 hours', 2592000, 2592000, 5184000,
  now() + interval '60 days',
  NULL,
  'running', NULL, NULL, NULL,
  'full', 'none', 1,
  '$acq_input_sql',
  '$acq_payload_digest',
  '$acq_manifest_digest', 1, now() - interval '2 hours');
-- The sealed manifest artifact inspection commits against: exactly the observed
-- file set, same ref derivation and relative path the plan reports.
INSERT INTO acquisition_files(acquisition_id, file_ref, ordinal, relative_path, exact_size, media_kind, identity_evidence)
VALUES ('acq-e2e', '$acq_file_ref', 0, 'EP01.mkv', 65536, 'video', '{}'::jsonb);
-- A task may only be inserted in the zero-count queued shape (the guard derives
-- its whole budget from the root's limits); reaching "completed" is the normal
-- update that follows.
INSERT INTO download_tasks(id, release_id, acquisition_id, frozen_input, frozen_policy, state, idempotency_key)
-- The frozen manifest carries torrent-relative paths: the planner classifies
-- first-level files from this manifest, never from the local download path.
VALUES ('dl-e2e', 'rel-e2e', 'acq-e2e', '{"release_stable_item_key":"guid:e2e","release_files":[{"source_path":"EP01.mkv","filename":"EP01.mkv","size_bytes":65536,"media_kind":"video"}]}', '$frozen_policy_sql'::jsonb, 'queued', 'download:e2e');
UPDATE download_tasks SET state = 'completed' WHERE id = 'dl-e2e';
UPDATE releases SET download_task_id = 'dl-e2e', verdict = 'submitted' WHERE id = 'rel-e2e';
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state)
VALUES ('art-e2e', 'dl-e2e', 'acq-e2e', '{}', '{}', 'pending');
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('af-e2e', 'art-e2e', 'acq-e2e', '$acq_file_ref', 'EP01.mkv', '$source_file', 'video', 65536, '$source_fp', '{"resolution":"1080p"}', 'not_applicable');
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snap-e2e', 'rel-e2e', 'media-facts-v1', 'episode-identity-v1', 'regex', 'digest-e2e', '$snapshot_identity'::jsonb, '$snapshot_release_facts'::jsonb);
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ('snap-e2e', '$acq_file_ref', 'video', 65536, NULL, '$snapshot_file_evidence'::jsonb, '$snapshot_effective_facts'::jsonb);
-- Ownership is the generic (owner_type, owner_id) pair now; fence_work_item_retirement
-- derives the acquisition root and the managed anime from it on insert. The
-- frozen manifest paths are torrent-relative, so the inspect input carries the
-- local download root they resolve against.
INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version, available_at)
VALUES ('wi-inspect', 'artifact', 'art-e2e', 'artifact_inspect', 'inspect:e2e', 5, jsonb_build_object('local_root', '$download_root'), 1, CURRENT_TIMESTAMP);
-- Old placement + evidence for the settlement stage: verified, active,
-- referencing the lower-quality facts (rel-old). The new file_operation
-- placement references rel-e2e (1080p) and must win the single_copy pool.
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, version, created_at, updated_at)
VALUES ('plc-old', '$BANSOKO_HARNESS_LIBRARY_ROOT/Old/EP01.mkv', 'anime-e2e', 'reconciliation', 'obs-old', '$old_fp', '$old_fp', 'rel-old', 'parser-v1', 'verified', 'active', 1, now() - interval '7 days', now() - interval '7 days');
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ('plc-old', 'tv:e1', 0, 'parser-v1', 'obs-old');
-- Complete retention axis (single_copy, version 1) on the same business row:
-- the frozen evaluate/settle chain requires it and revalidates its version at
-- settle; without it evaluate fails closed. The quality profile (profile-v1)
-- ranks release.resolution 2160p > 1080p > 720p > 480p, so the 1080p candidate
-- beats the old file (whose release froze no comparison values and ranks last).
UPDATE anime_business_configs SET
  admission_mode = 'single_copy',
  quality_profile = '{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}'::jsonb,
  replacement_source_default_version = 1,
  replacement_version = 1,
  -- The file-effect axis is the second policy the provisioning writes on the
  -- same row (ProvisionBusinessAxesTx): every reader of the unknown-file route
  -- fails closed on a NULL, and a fresh install records provenance 0 because
  -- it has no persisted file_effect_policy default. The RSS list read and the
  -- target plan both need it, not only the settle chain.
  unclassified_route = 'skip',
  file_effect_source_default_version = 0,
  file_effect_version = 1
WHERE managed_anime_id = 'anime-e2e';
COMMIT;
E2ESQL
then
  # A failed seed must stop the run here: continuing would let the chain wait
  # out its deadline and report a timeout that says nothing about the cause.
  echo "seed failed; see $summary_dir/seed.log" >&2
  sed -n '1,40p' "$summary_dir/seed.log" >&2 || true
  exit 1
fi
echo "seeded"
seeded_frozen_policy="$($PSQL -tAc "SELECT frozen_policy FROM download_tasks WHERE id='dl-e2e'")"
printf '%s' "$seeded_frozen_policy" | python3 -c 'import json,sys; sections=json.load(sys.stdin).get("effective_sections", {}); sys.exit(0 if sections.get("parser") else "seeded DownloadTask frozen policy missing effective_sections.parser")'

if scenario_requested 7; then
  # Scenario 7 seeds a parallel movie chain: its own anime pinned to the movie
  # media kind (the current-config render's movie branch reads it), so the
  # movie identity (movie:0, no episode) flows through
  # inspect -> plan -> execute -> settle -> cleanup next to the TV chain.
  echo "== seed the movie chain + real source file =="
  movie_download="$download_root/进击的巨人 剧场版.mkv"
  head -c 65536 /dev/urandom >"$movie_download"
  movie_hex="$(shasum -a 256 "$movie_download" | awk '{print $1}')"
  movie_fp='{"algorithm":"sha256","value":"'$movie_hex'","size_bytes":65536}'
  movie_old_file="$BANSOKO_HARNESS_LIBRARY_ROOT/Old/进击的巨人 剧场版.mkv"
  mkdir -p "$(dirname "$movie_old_file")"
  head -c 32768 /dev/urandom >"$movie_old_file"
  movie_old_hex="$(shasum -a 256 "$movie_old_file" | awk '{print $1}')"
  movie_old_fp='{"algorithm":"sha256","value":"'$movie_old_hex'","size_bytes":32768}'
  movie_payload_digest="sha256:$(repeat_hex e)"
  movie_manifest_digest="sha256:$(repeat_hex 7)"
  movie_file_ref="$(torrent_file_ref "$movie_payload_digest" '进击的巨人 剧场版.mkv')"
  movie_fixture_json="$(go run ./internal/testutil/frozenpolicy/cmd \
    --media-kind movie \
    --payload-digest "$movie_payload_digest" \
    --manifest-digest "$movie_manifest_digest" \
    --route-root "$library_root" \
    --file '进击的巨人 剧场版.mkv' \
    --title '[示例字幕组] 进击的巨人 剧场版 [1080p][chs]')"
  printf '%s' "$movie_fixture_json" >"$summary_dir/movie-acquisition-fixture.json"
  python3 - "$summary_dir/movie-acquisition-fixture.json" <<'PY'
import json,sys
fixture=json.load(open(sys.argv[1], encoding="utf-8"))
identity=fixture["identity"]
if identity.get("Kind") != "movie" or identity.get("CoverageKey") != "movie:0":
    raise SystemExit("movie fixture identity is not the movie short-circuit")
plan=fixture["release_facts"]["batch_plan"]
if plan["media_kind"] != "movie" or plan["coverage_keys"] != ["movie:0"]:
    raise SystemExit("movie fixture batch plan does not resolve movie:0")
if fixture["file_evidence"]["name"] != "进击的巨人 剧场版.mkv":
    raise SystemExit("movie fixture file evidence is not the movie file")
PY
  movie_acq_input_sql="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["acquisition_input"])' <<<"$movie_fixture_json")"
  movie_acq_input_sql="${movie_acq_input_sql//\'/\'\'}"
  movie_frozen_policy_sql="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["frozen_policy"])' <<<"$movie_fixture_json" | sed "s/'/''/g")"
  movie_identity_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["identity"]))' <<<"$movie_fixture_json")"
  movie_release_facts_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["release_facts"]))' <<<"$movie_fixture_json")"
  movie_file_evidence_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["file_evidence"]))' <<<"$movie_fixture_json")"
  movie_effective_facts_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["effective_facts"]))' <<<"$movie_fixture_json")"
  movie_config_overrides_sql="$(config_overrides_for anime-e2e-m "$summary_dir/movie-acquisition-fixture.json")"
  if ! $PSQL -q -e >"$summary_dir/movie-seed.log" 2>&1 <<E2EMOVIE
INSERT INTO managed_anime(id, title) VALUES ('anime-e2e-m', '进击的巨人');
-- The atomic tmdb CHECK requires work id and season beside the kind. The
-- current-config render selects the movie branch from the owner media kind,
-- and the import gate needs both bindings. The instance title matches the
-- fixture's frozen binding (the work title), like the TV instance; the 剧场版
-- marker lives in the file name, as a real movie release would.
INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, media_kind_override)
VALUES ('anime-e2e-m', 'e2e-bgm-movie-work', 'e2e-movie-work', 'movie', 0, 'movie');
INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-e2e-m');
$movie_config_overrides_sql
INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
VALUES ('anime-e2e-m', 'rules', 'tag_filters', 'tag-filters-v2', '{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]}}'::jsonb);
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-e2e-m', 'anime-e2e-m', 'custom', '剧场版', 'active');
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-e2e-m', 'sub-e2e-m', 'custom_rss', 'https://feed.example.test/e2e-movie', 'https://feed.example.test/e2e-movie', '示例字幕组', '进击的巨人 剧场版');
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('rel-e2e-m', 'src-e2e-m', 'guid:e2e-m', '{"title":"[示例字幕组] 进击的巨人 剧场版 [1080p][chs]"}', '{"group_name":"示例字幕组"}',
        '{"group_name":"示例字幕组","group":"示例字幕组","resolution":"1080p","quality":"web-dl","source":"web-dl","language":"chs","size_bytes":65536}', 'accepted_pending_download', 'accepted');
INSERT INTO release_candidate_facts(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys, resolution, revision, source, language, "group", size_bytes)
VALUES ('rel-e2e-m', 1, 'parser-v1', 'fp-e2e-m', '{"title":"[示例字幕组] 进击的巨人 剧场版 [1080p][chs]"}', '{"group_name":"示例字幕组","group":"示例字幕组","resolution":"1080p","quality":"web-dl","source":"web-dl","language":"chs","size_bytes":65536}',
        '["movie:0"]', '1080p', 'v1', 'web-dl', 'chs', '示例字幕组', 65536);
-- Older generation evidence for the movie placement: same movie:0 coverage,
-- lower quality, so the settle stage must pick the new file_operation
-- placement as winner and supersede plc-old-m.
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('rel-old-m', 'src-e2e-m', 'guid:old-m', '{"title":"[示例字幕组] 进击的巨人 剧场版 [720p][chs]"}', '{"group_name":"示例字幕组"}',
        '{"group_name":"示例字幕组","group":"示例字幕组","resolution":"720p","quality":"web-dl","source":"web-dl","language":"chs","size_bytes":32768}', 'accepted_pending_download', 'accepted');
INSERT INTO release_candidate_facts(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys, resolution, revision, source, language, "group", size_bytes)
VALUES ('rel-old-m', 1, 'parser-v1', 'fp-old-m', '{"title":"[示例字幕组] 进击的巨人 剧场版 [720p][chs]"}', '{"group_name":"示例字幕组","group":"示例字幕组","resolution":"720p","quality":"web-dl","source":"web-dl","language":"chs","size_bytes":32768}',
        '["movie:0"]', '720p', 'v1', 'web-dl', 'chs', '示例字幕组', 32768);
BEGIN;
INSERT INTO acquisitions(
  id, execution_branch, entry_kind, access_scope, request_namespace, request_key, request_hash,
  canonical_request, admission_kind, admission_http_status, admission_receipt,
  input_schema_version, admission_digest, origin_managed_anime_ref, source_release_ref,
  display_title, accepted_at, retention_anchor_at, detail_retention_seconds,
  receipt_after_accept_seconds, receipt_after_terminal_seconds, receipt_until, detail_until,
  state, terminal_at, terminal_schema, terminal_receipt, record_state,
  preparation_mode, vars_evidence_version,
  input_canonical_utf8, payload_digest, manifest_digest, manifest_item_count, manifest_sealed_at)
VALUES (
  'acq-e2e-m', 'download', 'automatic', 'deployment_owner_v1', 'auto_acquire_v1', 'auto:rel-e2e-m',
  'sha256:' || repeat('f', 64),
  '{"schema_version":1,"kind":"automatic","release_ref":"rel-e2e-m"}'::jsonb,
  'download_queued', 202,
  '{"schema_version":1,"acquisition_id":"acq-e2e-m","state":"queued"}'::jsonb,
  1,
  'sha256:' || encode(sha256(convert_to('acquisition-input:v1', 'UTF8') || decode('00', 'hex')
     || convert_to('$movie_acq_input_sql', 'UTF8')), 'hex'),
  'anime-e2e-m', 'rel-e2e-m',
  '{"state":"known","value":"进击的巨人 剧场版","reason_code":null}'::jsonb,
  now() - interval '2 hours', now() - interval '2 hours', 2592000, 2592000, 5184000,
  now() + interval '60 days',
  NULL,
  'running', NULL, NULL, NULL,
  'full', 'none', 1,
  '$movie_acq_input_sql',
  '$movie_payload_digest',
  '$movie_manifest_digest', 1, now() - interval '2 hours');
INSERT INTO acquisition_files(acquisition_id, file_ref, ordinal, relative_path, exact_size, media_kind, identity_evidence)
VALUES ('acq-e2e-m', '$movie_file_ref', 0, '进击的巨人 剧场版.mkv', 65536, 'video', '{}'::jsonb);
INSERT INTO download_tasks(id, release_id, acquisition_id, frozen_input, frozen_policy, state, idempotency_key)
VALUES ('dl-e2e-m', 'rel-e2e-m', 'acq-e2e-m', '{"release_stable_item_key":"guid:e2e-m","release_files":[{"source_path":"进击的巨人 剧场版.mkv","filename":"进击的巨人 剧场版.mkv","size_bytes":65536,"media_kind":"video"}]}', '$movie_frozen_policy_sql'::jsonb, 'queued', 'download:e2e-m');
UPDATE download_tasks SET state = 'completed' WHERE id = 'dl-e2e-m';
UPDATE releases SET download_task_id = 'dl-e2e-m', verdict = 'submitted' WHERE id = 'rel-e2e-m';
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state)
VALUES ('art-e2e-m', 'dl-e2e-m', 'acq-e2e-m', '{}', '{}', 'pending');
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('af-e2e-m', 'art-e2e-m', 'acq-e2e-m', '$movie_file_ref', '进击的巨人 剧场版.mkv', '$movie_download', 'video', 65536, '$movie_fp', '{"resolution":"1080p"}', 'not_applicable');
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snap-e2e-m', 'rel-e2e-m', 'media-facts-v1', 'episode-identity-v1', 'regex', 'digest-e2e-m', '$movie_identity_sql'::jsonb, '$movie_release_facts_sql'::jsonb);
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ('snap-e2e-m', '$movie_file_ref', 'video', 65536, NULL, '$movie_file_evidence_sql'::jsonb, '$movie_effective_facts_sql'::jsonb);
INSERT INTO work_items(id, owner_type, owner_id, kind, idempotency_key, max_attempts, input_snapshot, expected_owner_version, available_at)
VALUES ('wi-inspect-m', 'artifact', 'art-e2e-m', 'artifact_inspect', 'inspect:e2e-m', 5, jsonb_build_object('local_root', '$download_root'), 1, CURRENT_TIMESTAMP);
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, version, created_at, updated_at)
VALUES ('plc-old-m', '$BANSOKO_HARNESS_LIBRARY_ROOT/Old/进击的巨人 剧场版.mkv', 'anime-e2e-m', 'reconciliation', 'obs-old-m', '$movie_old_fp', '$movie_old_fp', 'rel-old-m', 'parser-v1', 'verified', 'active', 1, now() - interval '7 days', now() - interval '7 days');
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ('plc-old-m', 'movie:0', 0, 'parser-v1', 'obs-old-m');
UPDATE anime_business_configs SET
  admission_mode = 'single_copy',
  quality_profile = '{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}'::jsonb,
  replacement_source_default_version = 1,
  replacement_version = 1,
  unclassified_route = 'skip',
  file_effect_source_default_version = 0,
  file_effect_version = 1
WHERE managed_anime_id = 'anime-e2e-m';
COMMIT;
E2EMOVIE
  then
    echo "movie seed failed; see $summary_dir/movie-seed.log" >&2
    sed -n '1,40p' "$summary_dir/movie-seed.log" >&2 || true
    exit 1
  fi
  echo "movie chain seeded"
fi

# Negative runs destroy one owned placement file, then let the unchanged
# assertions decide. The destroy step is guarded by the explicit switch parsed at
# the top of this script, records the file it removed, and refuses to continue if
# the file survived: a negative run whose fixture is intact would prove nothing.
negative_inject() {
  local label="$1" path="$2"
  if [[ ! -e "$path" ]]; then
    echo "negative mode ${negative_mode}: $label is already absent before injection: $path" >&2
    exit 1
  fi
  printf '{"negative_mode":"%s","label":"%s","path":"%s","sha256":"%s"}\n' \
    "$negative_mode" "$label" "$path" "$(shasum -a 256 "$path" | awk '{print $1}')" \
    >>"$summary_dir/negative-injection.jsonl"
  rm -f -- "$path"
  if [[ -e "$path" ]]; then
    echo "negative mode ${negative_mode}: failed to destroy $label at $path" >&2
    exit 1
  fi
  echo "negative mode ${negative_mode}: destroyed $label at $path"
}

if [[ "$negative_mode" == old-source-missing ]]; then
  # Destroy the superseded source the deletion must verify. The deletion is
  # then skipped (loser_missing), the chain cannot converge and the unchanged
  # assertions below must reject it.
  negative_inject superseded-source "$old_file"
fi
echo "== waiting for the automatic chain =="
wait_chain() {
  local deadline=$(( $(date +%s) + 1100 ))
  while (( $(date +%s) < deadline )); do
    local placed settled cleanup_deleted delete_succeeded
    local failed_items available_placement attempt_line
    failed_items="$($PSQL -tAc "SELECT count(*) FROM e2e_aux.work_items_all WHERE owner_type='file_operation' AND kind='file_execute' AND state='failed' AND owner_id IN (SELECT id FROM file_operations WHERE acquisition_id='acq-e2e')")"
    if [[ "$failed_items" -gt 0 ]]; then
      echo "automatic chain reached terminal file-operation WorkItem failure" >&2
      return 1
    fi
    available_placement="$($PSQL -tAc "SELECT count(*) FROM work_items WHERE owner_type='file_operation' AND kind='file_execute' AND state='available'")"
    if [[ "$available_placement" -gt 0 ]]; then
      attempt_line="$($PSQL -tAc "SELECT 'attempt ' || attempt || ' of ' || max_attempts || ', still available' FROM work_items WHERE owner_type='file_operation' AND kind='file_execute' AND state='available' ORDER BY created_at DESC LIMIT 1")"
      if [[ "$attempt_line" != "${last_attempt_line:-}" ]]; then echo "placement handoff: $attempt_line"; last_attempt_line="$attempt_line"; fi
    fi
    placed="$($PSQL -tAc "SELECT count(*) FROM placements WHERE managed_anime_id='anime-e2e' AND source_evidence_type='file_operation'")"
    settled="$($PSQL -tAc "SELECT count(*) FROM replacement_decisions WHERE managed_anime_id='anime-e2e' AND verdict='replace' AND state='settled'")"
    # Receipt-backed maintenance cleanup deletes the superseded file in one
    # replacement_delete claim; a blocked cleanup is a terminal failure here.
    if [[ "$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE managed_anime_id='anime-e2e' AND state='blocked'")" -gt 0 ]]; then
      echo "cleanup blocked: $($PSQL -tAc "SELECT failure_code || ': ' || coalesce(reason,'') FROM replacement_deletions WHERE managed_anime_id='anime-e2e' AND state='blocked'")" >&2
      return 1
    fi
    # A cleanup that exhausted its attempt budget lands in `failed`, not in
    # `blocked`; without this read the run would wait out the deadline and
    # report a timeout instead of the typed reason that actually applies.
    if [[ "$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE managed_anime_id='anime-e2e' AND state='failed'")" -gt 0 ]]; then
      echo "cleanup failed: $($PSQL -tAc "SELECT failure_code || ': ' || coalesce(reason,'') || ' (state=failed)' FROM replacement_deletions WHERE managed_anime_id='anime-e2e' AND state='failed'")" >&2
      return 1
    fi
    # A skipped deletion is a typed zero-effect outcome (the loser was missing
    # or no longer the replaced file); this chain must delete, so it fails.
    if [[ "$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE managed_anime_id='anime-e2e' AND state='skipped'")" -gt 0 ]]; then
      echo "cleanup skipped: $($PSQL -tAc "SELECT failure_code || ': ' || coalesce(reason,'') || ' (state=skipped)' FROM replacement_deletions WHERE managed_anime_id='anime-e2e' AND state='skipped'")" >&2
      return 1
    fi
    cleanup_deleted="$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE managed_anime_id='anime-e2e' AND state='deleted'")"
    # Scoped to this chain: the movie chain is seeded beside it and writes its
    # own succeeded deletion, so an unscoped count is 2. Exactly one deletion
    # belongs to this chain, so a second one is a real duplicate-work regression.
    delete_succeeded="$($PSQL -tAc "SELECT count(*) FROM e2e_aux.work_items_all WHERE owner_type='replacement_deletion' AND kind='replacement_delete' AND state='succeeded' AND owner_id IN (SELECT id FROM replacement_deletions WHERE managed_anime_id='anime-e2e')")"
    if [[ "$placed" -ge 1 && "$settled" -ge 1 && "$cleanup_deleted" -eq 1 && "$delete_succeeded" -eq 1 ]]; then
      echo "chain complete: placed=$placed settled=$settled cleanup_deleted=$cleanup_deleted delete_succeeded=$delete_succeeded"
      return 0
    fi
    sleep 5
  done
  $PSQL -c "SELECT state, count(*) FROM e2e_aux.work_items_all WHERE owner_type IN ('artifact','file_operation','replacement_deletion') GROUP BY state" | tee -a "$durable_evidence_dir/failure-diagnostics.txt" >&2
  $PSQL -c "SELECT id, owner_type, owner_id, kind, state, last_error_code, last_error_reason FROM e2e_aux.work_items_all WHERE owner_id IN ('art-e2e','op-e2e','anime-e2e') OR owner_type IN ('artifact','file_operation','replacement_deletion') ORDER BY created_at" | tee -a "$durable_evidence_dir/failure-diagnostics.txt" >&2
  $PSQL -c "SELECT id, artifact_id, artifact_file_id, state, source_path, target_path, failure_code, failure_reason FROM file_operations WHERE artifact_id='art-e2e'" | tee -a "$durable_evidence_dir/failure-diagnostics.txt" >&2
  $PSQL -c "SELECT id, state, failure_code, failure_reason FROM artifacts WHERE id='art-e2e'" | tee -a "$durable_evidence_dir/failure-diagnostics.txt" >&2
  $PSQL -c "SELECT id, state, verification_state FROM placements" | tee -a "$durable_evidence_dir/failure-diagnostics.txt" >&2
  if [[ -f "$summary_dir/server.log" ]]; then
    printf '\n--- server runtime events for anime-e2e ---\n' >>"$durable_evidence_dir/failure-diagnostics.txt"
    python3 - "$summary_dir/server.log" <<'PY' >>"$durable_evidence_dir/failure-diagnostics.txt"
import json,sys
for line in open(sys.argv[1], encoding="utf-8"):
    try: event=json.loads(line)
    except Exception: continue
    if event.get("component") in {"workitems", "file_operation", "filemaintenance"}:
        print(json.dumps(event, ensure_ascii=False, sort_keys=True))
PY
  fi
  return 1
}
wait_chain || exit 1

echo "== assertions =="
old_state="$($PSQL -tAc "SELECT state FROM placements WHERE id='plc-old'")"
new_path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE source_evidence_type='file_operation' LIMIT 1")"
capture_path="$($PSQL -tAc "SELECT coalesce(attempt->>'capture_path','') FROM replacement_deletions WHERE managed_anime_id='anime-e2e'" | tr -d '\r')"
if [[ "$negative_mode" == new-placement-missing ]]; then
  # Destroy the file the settled chain left at its new placement path. The
  # `-f "$new_path"` assertion below must name it, not time out elsewhere.
  negative_inject new-placement "$new_path"
fi
[[ "$old_state" == "superseded" ]] || { echo "old placement not superseded: $old_state" >&2; exit 1; }
[[ -f "$new_path" ]] || { echo "new placement file missing: $new_path" >&2; exit 1; }
[[ ! -e "$old_file" ]] || { echo "superseded source still present after deletion: $old_file" >&2; exit 1; }
[[ -n "$capture_path" && ! -e "$capture_path" ]] || { echo "captured object not purged: $capture_path" >&2; exit 1; }
echo "replacement settled; old placement superseded and deleted; new placement retained at $new_path"
if [[ "${BANSOKO_FM_E2E_OFFSET_ALIGNMENT:-0}" == "1" ]]; then
  # The production subscription bootstrap materializes this CAS-owned policy;
  # the direct SQL fixture fills the equivalent parser axis on the business row.
  $PSQL -q -c "UPDATE anime_business_configs SET parser_kind='regex', parser_source_default_version=1, parser_version=1 WHERE managed_anime_id='anime-e2e' AND parser_kind IS NULL"
  # This synthetic file body has no probeable codec. Facts are explicit fixture inputs,
  # not a claim about artifact_inspect output.
  $PSQL -q -c "UPDATE artifact_files SET facts = facts || '{\"resolution\":\"1080p\",\"video_codec\":\"HEVC\",\"container\":\"mkv\"}'::jsonb WHERE artifact_id='art-e2e'"
fi

if [[ "${BANSOKO_FM_E2E_OFFSET_ALIGNMENT:-0}" == "1" ]]; then
  echo "== FILE-022 offset/alignment scenarios =="
  api="http://$listen/api/v1"
  auth=(-H "Authorization: Bearer $token" -H 'Content-Type: application/json')
  # A refused request is the most common failure in these scenarios and the
  # response body names the field and code; `-f` alone prints only "curl: (22)
  # The requested URL returned error: 422", which says nothing actionable.
  api_call() {
    local method="$1" path="$2" body="${3:-}"
    local out status
    out="$(mktemp "$summary_dir/api-body.XXXXXX")"
    if [[ -n "$body" ]]; then
      status="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "${auth[@]}" -d "$body" "$api$path")" || status=000
    else
      status="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "${auth[@]}" "$api$path")" || status=000
    fi
    if [[ "$status" -lt 200 || "$status" -ge 300 ]]; then
      echo "api $method $path -> HTTP $status" >&2
      echo "--- request body ---" >&2
      printf '%s\n' "${body:-<none>}" >&2
      echo "--- response body ---" >&2
      cat "$out" >&2
      echo >&2
      rm -f "$out"
      return 1
    fi
    cat "$out"
    rm -f "$out"
  }
  api_get() { api_call GET "$1"; }
  api_put() { api_call PUT "$1" "$2"; }
  api_patch() { api_call PATCH "$1" "$2"; }
  api_post() { api_call POST "$1" "$2"; }
  # scenario, the drifted file the preview must name, and the placement the
  # browser drives. The files dialog lists every imported file of the anime, so
  # the spec scopes its assertions to the placement rather than to a file name
  # the alignment itself can change.
  run_alignment_browser() {
    local scenario="$1" drift_path="$2" browser_placement="$3"
    mkdir -p "$summary_dir/playwright"
    if BANSOKO_TEST_BASE_URL="http://$listen" \
      BANSOKO_E2E_USERNAME=owner BANSOKO_E2E_PASSWORD=e2e-owner-password \
      BANSOKO_E2E_REAL_EFFECT=1 BANSOKO_E2E_AUTH_STATE_PATH="$summary_dir/auth-state.json" \
      BANSOKO_E2E_AUTH_MARKER_PATH="$summary_dir/auth-skip" \
      BANSOKO_E2E_OUTPUT_DIR="$summary_dir/playwright" \
      BANSOKO_BROWSER_SESSION_STATE_DIR="$summary_dir/playwright" \
      BANSOKO_FM_E2E_UI_SCENARIO="$scenario" BANSOKO_FM_E2E_UI_DRIFT_PATH="$drift_path" \
      BANSOKO_FM_E2E_UI_PLACEMENT_ID="$browser_placement" \
        pnpm --dir "$root/frontend" exec playwright test e2e/file-maintenance-alignment-live.spec.ts --project=desktop --workers=1; then
      return 0
    fi
    local artifact
    shopt -s globstar nullglob dotglob
    for artifact in "$summary_dir/playwright"/**/*; do
      if [[ -f "$artifact" ]]; then
        echo "scenario $scenario Playwright failure artifacts retained in $summary_dir/playwright" >&2
        return 1
      fi
    done
    echo "scenario $scenario failed without retained Playwright diagnostics" >&2
    return 1
  }
  fingerprint() {
    if [[ -f "$1" ]]; then
      shasum -a 256 "$1" | awk '{print $1}'
    else
      printf 'MISSING'
    fi
  }
  # The path-diff preview reads the calibration rows of a generation, and a
  # separate consumer commits them after the scan row itself reaches
  # `completed`; reading the preview earlier returns an empty array.
  calibration_generation() {
    $PSQL -tAc "SELECT COALESCE(MAX(generation),0) FROM reconciliation_files WHERE managed_anime_id='anime-e2e' AND disposition IS NOT NULL" | tr -d '[:space:]'
  }
  # Wait for the calibration of ONE specific scan generation, never merely for
  # "the newest calibrated generation moved". A previous scenario's scan can
  # still be calibrating when this one is submitted: waiting for any advance
  # returns as soon as THAT older generation lands, and the preview then reads
  # the previous generation's rows (observed: scenario 6's drift preview showed
  # the pre-move paths of scenario 5's scan).
  wait_calibration_generation() {
    local want="$1" deadline=$(( $(date +%s) + 180 )) current
    while (( $(date +%s) < deadline )); do
      current="$(calibration_generation)"
      if [[ -n "$current" && "$current" -ge "$want" ]]; then return 0; fi
      sleep 2
    done
    echo "calibration generation $want was not reached within 180s (last=$(calibration_generation))" >&2
    return 1
  }
  # Expected target of the single drifting row for one file path, read from the
  # same live response the UI renders. Fails loudly when the preview does not
  # show that drift, so a scenario can never record a claim it did not observe.
  path_diff_expected() {
    printf '%s' "$1" | python3 -c 'import json,sys
rows=json.load(sys.stdin)
matches=[r for r in rows if r.get("file_path")==sys.argv[1] and r.get("differs")]
assert len(matches)==1, f"expected one drifting path-diff for {sys.argv[1]}, got {rows!r}"
print(matches[0].get("expected_path") or "")' "$2"
  }
  # Wait for this scenario's own alignment operation to settle, and print the
  # settled row so a failure carries its typed reason instead of only a timeout.
  wait_alignment_terminal() {
    local source_path="$1" deadline=$(( $(date +%s) + 240 )) row=""
    while (( $(date +%s) < deadline )); do
      row="$($PSQL -tAc "SELECT state || '|' || COALESCE(failure_code,'') || '|' || target_path || '|' || COALESCE(reason,'') FROM alignment_operations WHERE managed_anime_id='anime-e2e' AND source_path='$source_path' ORDER BY created_at DESC LIMIT 1" | tr -d '\r')"
      case "${row%%|*}" in
        completed|blocked|failed|canceled) printf '%s\n' "$row"; return 0 ;;
      esac
      sleep 2
    done
    echo "alignment for $source_path did not settle within 240s (last=$row)" >&2
    return 1
  }
  # One manual scan plus the calibration generation it produces, so the preview
  # reflects the drift the scenario just created on disk.
  scan_and_calibrate() {
    local label="$1" submitted request
    # A scan owner admits one generation at a time. Scenario 5's browser step
    # leaves its own rescan behind when it settles, so submitting here without
    # waiting first races that scan. A refused submit surfaces as an untyped 500
    # rather than a typed conflict, so it names nothing about the fixture: wait
    # for the owner to leave `running` and retry instead of failing the lane on a
    # scheduling race.
    local attempt existing
    for attempt in 1 2 3 4 5 6; do
      # Only wait when an earlier generation actually exists: a run whose first
      # scan is this one has no row to wait for, and waiting 300s on an empty
      # read would report a timeout that says nothing about the fixture.
      existing="$($PSQL -tAc "SELECT count(*) FROM reconciliation_scans WHERE managed_anime_id='anime-e2e'" | tr -d '[:space:]')"
      if [[ "$existing" != 0 ]]; then
        wait_sql "SELECT state FROM reconciliation_scans WHERE managed_anime_id='anime-e2e' ORDER BY generation DESC LIMIT 1" completed 300 || true
      fi
      request="file022-$label-$(date +%s)-$RANDOM"
      if api_post /reconciliation/scans "{\"managed_anime_id\":\"anime-e2e\",\"tree_top\":\"$library_root\",\"trigger\":\"manual\",\"request_id\":\"$request\"}" >"$summary_dir/$label-scan-submit.json" 2>"$summary_dir/$label-scan-submit.err"; then
        break
      fi
      if (( attempt == 6 )); then
        echo "scan submit for $label was refused 6 times while no other scan was running" >&2
        cat "$summary_dir/$label-scan-submit.err" >&2 || true
        exit 1
      fi
      echo "scan submit for $label was refused; waiting for the running scan to settle before retrying" >&2
      sleep 5
    done
    # Bind both waits to the generation THIS submit returned. Waiting on the
    # newest scan/calibration row lets an earlier scenario's in-flight work
    # satisfy the wait, and the preview then describes the wrong generation.
    submitted="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["generation"])' <"$summary_dir/$label-scan-submit.json")"
    [[ "$submitted" =~ ^[0-9]+$ ]] || { echo "scan submit for $label returned no usable generation: $(cat "$summary_dir/$label-scan-submit.json")" >&2; exit 1; }
    wait_sql "SELECT state FROM reconciliation_scans WHERE managed_anime_id='anime-e2e' AND generation=$submitted" completed
    wait_calibration_generation "$submitted"
  }
  observe_disk() {
    local label="$1" path="$2"
    {
      printf 'scenario=%s path=%s\n' "$label" "$path"
      if [[ -f "$path" ]]; then ls -l "$path"; else printf 'MISSING %s\n' "$path"; fi
      printf 'sha256=%s\n' "$(fingerprint "$path")"
    } | tee -a "$summary_dir/disk-observations.txt"
  }
  wait_sql() {
    local query="$1" expected="$2" deadline=$(( $(date +%s) + 240 )) value
    while (( $(date +%s) < deadline )); do
      value="$($PSQL -tAc "$query" | tr -d '[:space:]')"
      [[ "$value" == "$expected" ]] && return 0
      sleep 2
    done
    echo "timed out waiting for SQL value $expected: $query (last=$value)" >&2
    return 1
  }
  # An offset change re-keys the link's files in the PATCH transaction; the
  # scan scheduler's tick (once a minute) compares the keys through retention,
  # submits the scan whose alignment routes the files, and records the change
  # once every request is resolved. The file has moved when its placement path
  # changed after the change was recorded.
  wait_offset_moved() {
    local source_id="$1" placement_id="$2" before_path="$3" deadline=$(( $(date +%s) + 300 )) state path
    while (( $(date +%s) < deadline )); do
      state="$($PSQL -tAc "SELECT state FROM offset_alignment_changes WHERE rss_source_id='$source_id' ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]')"
      path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
      [[ "$state" == recorded && "$path" != "$before_path" ]] && return 0
      sleep 2
    done
    $PSQL -c "SELECT id, rss_source_id, source_version, old_offset, new_offset, rekeyed_count, deleted_count, state FROM offset_alignment_changes WHERE rss_source_id='$source_id' ORDER BY created_at" >&2
    $PSQL -c "SELECT placement_id, offset_change_id, outcome FROM alignment_requests WHERE placement_id='$placement_id'" >&2
    $PSQL -c "SELECT id, source_path, target_path, state, failure_code, reason FROM alignment_operations WHERE managed_anime_id='anime-e2e' ORDER BY created_at" >&2
    echo "offset change of $source_id did not move placement $placement_id within 300 seconds (change=$state)" >&2
    return 1
  }
  scenario_json="$summary_dir/file022-scenarios.jsonl"
  commit="$(git -C "$root" rev-parse HEAD 2>/dev/null || cat "$root/HEAD.txt" 2>/dev/null || unknown)"
  build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  server_sha="$(shasum -a 256 "$server_bin" | awk '{print $1}')"
  frontend_dist_sha="$(shasum -a 256 "$root/frontend/dist/index.html" | awk '{print $1}')"
  frontend_assets="$(printf '%s\n' "$root"/frontend/dist/assets/* | xargs -n1 basename | sort | paste -sd, -)"
  ui_spec_sha="$(shasum -a 256 "$root/frontend/e2e/file-maintenance-alignment-live.spec.ts" | awk '{print $1}')"
  harness_session="$(basename "$BANSOKO_HARNESS_STATE_DIR")"
  environment="$(hostname -f 2>/dev/null || hostname)"
  printf '{"session":"%s","commit":"%s","environment":"%s","library_root":"%s","server_sha256":"%s","frontend_dist_sha256":"%s","frontend_assets":"%s","ui_spec_sha256":"%s","build_time_utc":"%s"}\n' \
    "$harness_session" "$commit" "$environment" "$library_root" "$server_sha" "$frontend_dist_sha" "$frontend_assets" "$ui_spec_sha" "$build_time" >"$scenario_json"

  # The existing successful acquisition chain supplies a verified, active,
  # file_operation-lineaged placement with frozen media facts. All additional
  # effects remain inside the disposable harness library root.
  placement_id="$($PSQL -tAc "SELECT id FROM placements WHERE managed_anime_id='anime-e2e' AND source_evidence_type='file_operation' AND state='active' AND verification_state='verified' ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]')"
  [[ -n "$placement_id" ]] || { echo 'offset/alignment requires active verified file_operation placement' >&2; exit 1; }
  original_path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
  original_hash="$(fingerprint "$original_path")"
  [[ "$original_hash" != MISSING ]] || { echo "fixture file missing: $original_path" >&2; exit 1; }
  [[ -x "$server_bin" ]] || { echo "scenario runner requires executable server binary: $server_bin" >&2; exit 2; }
  [[ -f "$root/frontend/dist/index.html" ]] || { echo "scenario runner requires built frontend index: $root/frontend/dist/index.html" >&2; exit 2; }
  [[ -d "$root/frontend/dist/assets" ]] && compgen -G "$root/frontend/dist/assets/*" >/dev/null || { echo "scenario runner requires built frontend assets: $root/frontend/dist/assets" >&2; exit 2; }
  [[ -f "$BANSOKO_HARNESS_STATE_DIR/state.json" ]] || { echo "scenario runner requires active harness state: $BANSOKO_HARNESS_STATE_DIR" >&2; exit 2; }
  placement_id="$($PSQL -tAc "SELECT id FROM placements WHERE managed_anime_id='anime-e2e' AND source_evidence_type='file_operation' AND state='active' AND verification_state='verified' ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]')"
  original_path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
  second_basename="$(basename "$original_path")"
  second_basename="${second_basename/E01/E05}"
  second_path="$(dirname "$original_path")/$second_basename"
  second_download="$download_root/EP05.mkv"
  head -c 65536 /dev/urandom >"$second_download"
  cp "$second_download" "$second_path"
  second_hash="$(fingerprint "$second_path")"
  second_fp='{"algorithm":"sha256","value":"'$second_hash'","size_bytes":65536}'
  second_payload_digest="sha256:$(repeat_hex d)"
  second_manifest_digest="sha256:$(repeat_hex 9)"
  second_file_ref="$(torrent_file_ref "$second_payload_digest" 'EP05.mkv')"
  second_fixture_json="$(go run ./internal/testutil/frozenpolicy/cmd \
    --payload-digest "$second_payload_digest" \
    --manifest-digest "$second_manifest_digest" \
    --route-root "$library_root" \
    --file EP05.mkv \
    --title '[示例字幕组] 进击的巨人 - 05 [1080p][chs]')"
  acq_input_sql_2="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["acquisition_input"])' <<<"$second_fixture_json")"
  acq_input_sql_2="${acq_input_sql_2//\'/\'\'}"
  second_identity_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["identity"]))' <<<"$second_fixture_json")"
  second_release_facts_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["release_facts"]))' <<<"$second_fixture_json")"
  second_file_evidence_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["file_evidence"]))' <<<"$second_fixture_json")"
  second_effective_facts_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["effective_facts"]))' <<<"$second_fixture_json")"
  printf '%s' "$second_fixture_json" >"$summary_dir/second-acquisition-fixture.json"
  python3 - "$summary_dir/second-acquisition-fixture.json" <<'PY'
import json,sys
fixture=json.load(open(sys.argv[1], encoding="utf-8"))
plan=fixture["release_facts"]["batch_plan"]
if plan["coverage_keys"] != ["tv:e5"]:
    raise SystemExit("second fixture batch plan does not cover tv:e5")
if fixture["file_evidence"]["name"] != "EP05.mkv":
    raise SystemExit("second fixture file evidence is not EP05.mkv")
PY
  $PSQL -q -e >"$summary_dir/second-source-seed.log" 2>&1 <<E2ESECOND
BEGIN;
INSERT INTO rss_sources(id,subscription_id,source_kind,locator,locator_display,group_name,title,episode_offset)
VALUES ('src-e2e-2','sub-e2e','custom_rss','https://feed.example.test/e2e-2','https://feed.example.test/e2e-2','示例字幕组','进击的巨人',0);
INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,parsed_facts,internal_facts,verdict,reason_code,download_task_id,version)
SELECT 'rel-e2e-2','src-e2e-2','guid:e2e-2',jsonb_set(raw_facts,'{title}','"[示例字幕组] 进击的巨人 - 05 [1080p][chs]"'),source_facts,parsed_facts,internal_facts,'submitted','submitted','dl-e2e-2',1 FROM releases WHERE id='rel-e2e';
INSERT INTO acquisitions(
  id, execution_branch, entry_kind, access_scope, request_namespace, request_key, request_hash,
  canonical_request, admission_kind, admission_http_status, admission_receipt,
  input_schema_version, admission_digest, origin_managed_anime_ref, source_release_ref,
  display_title, accepted_at, retention_anchor_at, detail_retention_seconds,
  receipt_after_accept_seconds, receipt_after_terminal_seconds, receipt_until, detail_until,
  state, terminal_at, terminal_schema, terminal_receipt, record_state,
  preparation_mode, vars_evidence_version,
  input_canonical_utf8, payload_digest, manifest_digest, manifest_item_count, manifest_sealed_at)
VALUES (
  'acq-e2e-2', 'download', 'automatic', 'deployment_owner_v1', 'auto_acquire_v1', 'auto:rel-e2e-2',
  'sha256:' || repeat('b', 64),
  '{"schema_version":1,"kind":"automatic","release_ref":"rel-e2e-2"}'::jsonb,
  'download_queued', 202,
  '{"schema_version":1,"acquisition_id":"acq-e2e-2","state":"queued"}'::jsonb,
  1,
  'sha256:' || encode(sha256(convert_to('acquisition-input:v1', 'UTF8') || decode('00', 'hex')
     || convert_to('$acq_input_sql_2', 'UTF8')), 'hex'),
  'anime-e2e', 'rel-e2e-2',
  '{"state":"known","value":"进击的巨人 - 05","reason_code":null}'::jsonb,
  now() - interval '2 hours', now() - interval '2 hours', 2592000, 2592000, 5184000,
  now() + interval '60 days',
  NULL,
  'running', NULL, NULL, NULL,
  'full', 'none', 1,
  '$acq_input_sql_2',
  '$second_payload_digest',
  '$second_manifest_digest', 1, now() - interval '2 hours');
-- The seeded completed operation references requirement='required', so the
-- manifest row must carry the same requirement shape the declarations stage
-- would have written (reason code + evidence are shape-checked, never read).
INSERT INTO acquisition_files(acquisition_id, file_ref, ordinal, relative_path, exact_size, media_kind, identity_evidence, requirement, requirement_reason_code, selection_evidence)
VALUES ('acq-e2e-2', '$second_file_ref', 0, 'EP05.mkv', 65536, 'video', '{}'::jsonb, 'required', 'e2e_seed_required', '{"source":"filemaintenance-e2e seed"}'::jsonb);
INSERT INTO download_tasks(id,release_id,acquisition_id,frozen_input,frozen_policy,state,idempotency_key)
VALUES ('dl-e2e-2','rel-e2e-2','acq-e2e-2','{}','$frozen_policy_sql'::jsonb,'queued','download:e2e:2');
UPDATE download_tasks SET state='completed' WHERE id='dl-e2e-2';
INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,verified_at)
VALUES ('art-e2e-2','dl-e2e-2','acq-e2e-2','{}','{}','verified',CURRENT_TIMESTAMP);
INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state)
VALUES ('af-e2e-2','art-e2e-2','acq-e2e-2','$second_file_ref','EP05.mkv','$second_download','video',65536,'$second_fp'::jsonb,'{"resolution":"1080p","video_codec":"HEVC","container":"mkv"}','not_applicable');
-- The plan root is per-acquisition (postprocessing_plans(id, acquisition_id)),
-- so the second source gets its own never-executed queued plan instead of the
-- first chain's plan id.
INSERT INTO postprocessing_plans(id,artifact_id,artifact_version,state,input_digest,acquisition_id)
VALUES ('plan-e2e-2','art-e2e-2',1,'queued','sha256:' || repeat('2', 64),'acq-e2e-2');
INSERT INTO file_operations(id,artifact_id,acquisition_id,artifact_version,artifact_file_id,file_ref,operation,source_path,target_path,source_fingerprint,target_fingerprint,collision_policy,frozen_render_snapshot,frozen_config_snapshot,state,observed_at,idempotency_key,correlation_id,causation_type,causation_id,postprocessing_plan_id,file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds)
SELECT 'op-e2e-2','art-e2e-2','acq-e2e-2',1,'af-e2e-2','$second_file_ref','move','$second_download','$second_path','$second_fp'::jsonb,'$second_fp'::jsonb,'block','{}',frozen_config_snapshot,'completed',CURRENT_TIMESTAMP,'op-e2e-2','corr-e2e-2','artifact','art-e2e-2','plan-e2e-2',file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds FROM file_operations WHERE id=(SELECT source_owner_ref FROM placements WHERE id='$placement_id');
-- The snapshot is built from the EP05 fixture itself so release_facts' frozen
-- batch plan matches the placement's tv:e5 coverage instead of the first
-- chain's EP01 plan.
INSERT INTO media_fact_snapshots(id,release_id,schema_version,resolver_version,parser_kind,input_digest,release_identity,release_facts)
VALUES ('snap-e2e-2','rel-e2e-2','media-facts-v1','episode-identity-v1','regex','digest-e2e-2','$second_identity_sql'::jsonb,'$second_release_facts_sql'::jsonb);
INSERT INTO media_fact_files(snapshot_id,file_ref,media_kind,size_bytes,source_fingerprint,file_evidence,effective_facts)
VALUES ('snap-e2e-2','$second_file_ref','video',65536,'$second_hash','$second_file_evidence_sql'::jsonb,'$second_effective_facts_sql'::jsonb);
INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,quality_facts_ref,facts_version,correlation_id,causation_type,causation_id,replacement_eligible,verification_state,state,version)
VALUES ('plc-e2e-2','$second_path','anime-e2e','file_operation','op-e2e-2','$second_fp'::jsonb,'$second_fp'::jsonb,'rel-e2e-2','media-facts-v1','corr-e2e-2','file_operation','op-e2e-2',true,'verified','active',1);
INSERT INTO placement_coverage(placement_id,observation_key,coverage_ordinal,facts_version,evidence_ref)
VALUES ('plc-e2e-2','tv:e5',0,'media-facts-v1','op-e2e-2');
COMMIT;
E2ESECOND
  observe_disk baseline "$original_path"

  if scenario_requested 7; then
    # 7. The movie chain (seeded beside the TV one) settles into the movie
    # library row (M1), and an episode offset edit on its source is a typed
    # no-op (M4): the movie identity is not affected by episode offset.
    $PSQL -q -c "UPDATE anime_business_configs SET parser_kind='regex', parser_source_default_version=1, parser_version=1 WHERE managed_anime_id='anime-e2e-m' AND parser_kind IS NULL"
    movie_deadline=$(( $(date +%s) + 300 ))
    while :; do
      movie_failed="$($PSQL -tAc "SELECT count(*) FROM e2e_aux.work_items_all WHERE owner_type='file_operation' AND kind='file_execute' AND state='failed' AND owner_id IN (SELECT id FROM file_operations WHERE acquisition_id='acq-e2e-m')" | tr -d '[:space:]')"
      if [[ "$movie_failed" -gt 0 ]]; then
        echo "scenario 7 movie chain reached terminal file_execute failure" >&2
        $PSQL -c "SELECT id, artifact_id, state, source_path, target_path, failure_code, failure_reason FROM file_operations WHERE acquisition_id='acq-e2e-m'" | tee -a "$durable_evidence_dir/failure-diagnostics.txt" >&2
        exit 1
      fi
      movie_cleanup_blocked="$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE managed_anime_id='anime-e2e-m' AND state='blocked'" | tr -d '[:space:]')"
      if [[ "$movie_cleanup_blocked" -gt 0 ]]; then
        echo "scenario 7 movie cleanup blocked: $($PSQL -tAc "SELECT failure_code || ': ' || coalesce(reason,'') FROM replacement_deletions WHERE managed_anime_id='anime-e2e-m' AND state='blocked'")" >&2
        exit 1
      fi
      movie_placed="$($PSQL -tAc "SELECT count(*) FROM placements WHERE managed_anime_id='anime-e2e-m' AND source_evidence_type='file_operation' AND state='active' AND verification_state='verified'" | tr -d '[:space:]')"
      movie_settled="$($PSQL -tAc "SELECT count(*) FROM replacement_decisions WHERE managed_anime_id='anime-e2e-m' AND verdict='replace' AND state='settled'" | tr -d '[:space:]')"
      movie_deleted="$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE managed_anime_id='anime-e2e-m' AND state='deleted'" | tr -d '[:space:]')"
      movie_delete_done="$($PSQL -tAc "SELECT count(*) FROM e2e_aux.work_items_all WHERE owner_type='replacement_deletion' AND kind='replacement_delete' AND state='succeeded' AND owner_id IN (SELECT id FROM replacement_deletions WHERE managed_anime_id='anime-e2e-m')" | tr -d '[:space:]')"
      if [[ "$movie_placed" -ge 1 && "$movie_settled" -ge 1 && "$movie_deleted" -eq 1 && "$movie_delete_done" -eq 1 ]]; then
        echo "movie chain complete: placed=$movie_placed settled=$movie_settled cleanup_deleted=$movie_deleted delete_succeeded=$movie_delete_done"
        break
      fi
      if (( $(date +%s) >= movie_deadline )); then
        $PSQL -c "SELECT state, count(*) FROM e2e_aux.work_items_all WHERE owner_type IN ('artifact','file_operation','replacement_deletion') AND (owner_id='art-e2e-m' OR owner_id IN (SELECT id FROM file_operations WHERE acquisition_id='acq-e2e-m') OR owner_id IN (SELECT id FROM replacement_deletions WHERE managed_anime_id='anime-e2e-m')) GROUP BY state" | tee -a "$durable_evidence_dir/failure-diagnostics.txt" >&2
        $PSQL -c "SELECT id, artifact_id, state, source_path, target_path, failure_code, failure_reason FROM file_operations WHERE acquisition_id='acq-e2e-m'" | tee -a "$durable_evidence_dir/failure-diagnostics.txt" >&2
        echo 'scenario 7 movie chain did not settle within 300s' >&2
        exit 1
      fi
      sleep 5
    done
    # M1: the movie renders under the movie library row with no season level
    # (${kind}/${ny} + ${fn}); the file keeps its content fingerprint.
    movie_placement_id="$($PSQL -tAc "SELECT id FROM placements WHERE managed_anime_id='anime-e2e-m' AND source_evidence_type='file_operation' AND state='active' AND verification_state='verified' ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]')"
    movie_path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$movie_placement_id'" | tr -d '\r')"
    movie_expected="$library_root/Movie/进击的巨人/进击的巨人 剧场版.mkv"
    [[ "$movie_path" == "$movie_expected" && -f "$movie_path" ]] || { echo "scenario 7 movie placement is $movie_path, want $movie_expected" >&2; exit 1; }
    [[ "$(fingerprint "$movie_path")" == "$movie_hex" ]] || { echo 'scenario 7 movie placement fingerprint mismatch' >&2; exit 1; }
    [[ "$($PSQL -tAc "SELECT count(*) FROM placement_coverage WHERE placement_id='$movie_placement_id' AND observation_key='movie:0'" | tr -d '[:space:]')" == 1 ]] || { echo 'scenario 7 movie placement coverage is not movie:0' >&2; exit 1; }
    [[ "$($PSQL -tAc "SELECT state FROM placements WHERE id='plc-old-m'" | tr -d '[:space:]')" == superseded ]] || { echo 'scenario 7 old movie placement not superseded' >&2; exit 1; }
    [[ ! -e "$movie_old_file" ]] || { echo 'scenario 7 superseded movie source still present after deletion' >&2; exit 1; }
    observe_disk scenario-7-movie-placement "$movie_path"
    # M4: the offset edit re-keys nothing (a movie does not move with an
    # offset): the change is recorded at once and the file never moves.
    m_src_version="$($PSQL -tAc "SELECT version FROM rss_sources WHERE id='src-e2e-m'" | tr -d '[:space:]')"
    api_patch "/anime/anime-e2e-m/rss-sources/src-e2e-m/offset" "{\"expected_version\":$m_src_version,\"episode_offset\":1}" >"$summary_dir/scenario-7-offset.json"
    m_change_row="$($PSQL -tAc "SELECT state || '|' || rekeyed_count FROM offset_alignment_changes WHERE rss_source_id='src-e2e-m' ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]')"
    [[ "$m_change_row" == "recorded|0" ]] || { echo "scenario 7 offset change is [$m_change_row], want recorded|0" >&2; exit 1; }
    [[ "$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$movie_placement_id'" | tr -d '\r')" == "$movie_path" && -f "$movie_path" && "$(fingerprint "$movie_path")" == "$movie_hex" ]] || { echo 'scenario 7 movie_noop moved or rewrote the placement' >&2; exit 1; }
    observe_disk scenario-7-after-offset "$movie_path"
    printf '{"scenario":7,"m1":"movie chain settled at the movie row","m4":"offset no-op","db_state":"recorded","rekeyed":0,"placement":"%s","sha256":"%s"}\n' "$movie_path" "$movie_hex" >>"$scenario_json"
    cat "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"
    cat "$summary_dir/disk-observations.txt" | tee -a "$durable_evidence_dir/disk-observations.txt"
  fi

  if scenario_requested 1; then
    # 1. An RSS link offset edit on src-e2e moves only that link's placement.
    source_version="$($PSQL -tAc "SELECT version FROM rss_sources WHERE id='src-e2e'" | tr -d '[:space:]')"
    old_offset="$($PSQL -tAc "SELECT episode_offset FROM rss_sources WHERE id='src-e2e'" | tr -d '[:space:]')"
    new_offset=$((old_offset + 1))
    before_base="$(basename "$original_path")"
    before_dir="$(dirname "$original_path")"
    second_before="$second_path"
    second_before_hash="$(fingerprint "$second_before")"
    api_patch "/anime/anime-e2e/rss-sources/src-e2e/offset" "{\"expected_version\":$source_version,\"episode_offset\":$new_offset}" >"$summary_dir/scenario-1-source.json"
    [[ "$(fingerprint "$second_before")" == "$second_before_hash" ]] || { echo 'scenario 1 touched another link file' >&2; exit 1; }
    [[ "$($PSQL -tAc "SELECT rekeyed_count FROM offset_alignment_changes WHERE rss_source_id='src-e2e' ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]')" == 1 ]] || { echo 'scenario 1 offset change did not re-key the link file' >&2; exit 1; }
    wait_offset_moved src-e2e "$placement_id" "$original_path"
    offset_target="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
    [[ "$offset_target" != "$original_path" && -f "$offset_target" && ! -e "$original_path" && "$(dirname "$offset_target")" == "$before_dir" && "$(basename "$offset_target")" != "$before_base" && "$(fingerprint "$offset_target")" == "$original_hash" ]] || { echo 'scenario 1 disk/name/fingerprint mismatch' >&2; exit 1; }
    observe_disk scenario-1-before "$original_path"; observe_disk scenario-1-after "$offset_target"; observe_disk scenario-1-other-source-unchanged "$second_before"
    printf '{"scenario":1,"db_state":"recorded","before":"%s","after":"%s","other_source":"%s","other_source_sha256":"%s","sha256":"%s"}\n' "$original_path" "$offset_target" "$second_before" "$second_before_hash" "$original_hash" >>"$scenario_json"
    cat "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"
    cat "$summary_dir/disk-observations.txt" | tee -a "$durable_evidence_dir/disk-observations.txt"
  else
    offset_target="$original_path"; new_offset=0
  fi

  if scenario_requested 2; then
    # 2. A link offset edit on src-e2e-2 moves only its own E05 file.
    first_before="$offset_target"; first_before_hash="$(fingerprint "$first_before")"
    source_version="$($PSQL -tAc "SELECT version FROM rss_sources WHERE id='src-e2e-2'" | tr -d '[:space:]')"
    api_patch "/anime/anime-e2e/rss-sources/src-e2e-2/offset" "{\"expected_version\":$source_version,\"episode_offset\":$((new_offset + 1))}" >"$summary_dir/scenario-2-source.json"
    wait_offset_moved src-e2e-2 plc-e2e-2 "$second_path"
    source_after="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='plc-e2e-2'" | tr -d '\r')"
    [[ "$source_after" != "$second_path" && -f "$source_after" && ! -e "$second_path" && "$(fingerprint "$source_after")" == "$second_hash" ]] || { echo 'scenario 2 source placement did not move with preserved fingerprint' >&2; exit 1; }
    [[ "$(fingerprint "$first_before")" == "$first_before_hash" ]] || { echo 'scenario 2 moved another source' >&2; exit 1; }
    observe_disk scenario-2-before "$second_path"; observe_disk scenario-2-after "$source_after"; observe_disk scenario-2-other-source-unchanged "$first_before"
    printf '{"scenario":2,"source_id":"src-e2e-2","db_state":"recorded","before":"%s","after":"%s","other_source":"%s","other_source_sha256":"%s","sha256":"%s"}\n' "$second_path" "$source_after" "$first_before" "$first_before_hash" "$second_hash" >>"$scenario_json"
    cat "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"
    cat "$summary_dir/disk-observations.txt" | tee -a "$durable_evidence_dir/disk-observations.txt"
  fi
  if scenario_requested 3; then
    # 3. SIGKILL right after the offset edit commits: the re-keyed file and its
    # alignment request are durable, so the restarted server compares, scans
    # and moves it.
    offset_for_restart=$((new_offset + 1))
    before_restart_path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
    source_version="$($PSQL -tAc "SELECT version FROM rss_sources WHERE id='src-e2e'" | tr -d '[:space:]')"
    api_patch "/anime/anime-e2e/rss-sources/src-e2e/offset" "{\"expected_version\":$source_version,\"episode_offset\":$offset_for_restart}" >"$summary_dir/scenario-3-source.json"
    kill -KILL "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
    server_pid=""
    change_before="$($PSQL -tAc "SELECT id || '|' || state FROM offset_alignment_changes WHERE rss_source_id='src-e2e' ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]')"
    [[ "$change_before" == *"|comparing" || "$change_before" == *"|aligning" ]] || { echo "scenario 3 offset change before restart is [$change_before], want comparing or aligning" >&2; exit 1; }
    observe_disk scenario-3-before-restart "$before_restart_path"
    [[ -f "$before_restart_path" ]] || { echo 'scenario 3 file moved before the restart' >&2; exit 1; }
    start_server
    live_ok=""
    for _ in $(seq 1 60); do if curl -sf "http://$listen/api/v1/livez" >/dev/null 2>&1; then live_ok=1; break; fi; sleep 1; done
    [[ -n "$live_ok" ]] || { echo 'scenario 3 recovery server failed to start' >&2; exit 1; }
    wait_offset_moved src-e2e "$placement_id" "$before_restart_path"
    recovery_path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
    if [[ ! -f "$recovery_path" || -e "$before_restart_path" || "$(fingerprint "$recovery_path")" != "$original_hash" ]]; then
      echo "scenario 3 mismatch: recovery=$recovery_path recovery_exists=$([[ -f "$recovery_path" ]] && echo yes || echo no) source_still_there=$([[ -e "$before_restart_path" ]] && echo yes || echo no)" >&2
      exit 1
    fi
    observe_disk scenario-3-after-restart "$recovery_path"
    printf '{"scenario":3,"offset_change":"%s","db_state":"recorded_after_crash_restart","before":"%s","after":"%s","sha256":"%s"}\n' "${change_before%%|*}" "$before_restart_path" "$recovery_path" "$(fingerprint "$recovery_path")" >>"$scenario_json"
    cat "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"
    cat "$summary_dir/disk-observations.txt" | tee -a "$durable_evidence_dir/disk-observations.txt"
  fi
  if scenario_requested 4 || scenario_requested 5 || scenario_requested 6; then
  # 4. Live naming-rule edit while alignment is off is inert; the scenario's
  # real drift stimulus is physical relocation of the already-owned file.
  global_policy="$(api_get /file-maintenance/policy)"
  global_version="$(printf '%s' "$global_policy" | python3 -c 'import json,sys;print(json.load(sys.stdin)["version"])')"
  api_put /file-maintenance/policy "{\"enabled\":true,\"alignment_enabled\":false,\"expected_version\":$global_version}" >"$summary_dir/scenario-4-policy.json"
  rules_workspace="$(api_get /anime/anime-e2e/workspace)"
  printf '%s' "$rules_workspace" | python3 -c 'import json,sys;json.dump(json.load(sys.stdin)["rules"],open(sys.argv[1],"w"))' "$summary_dir/scenario-4-rules-before.json"
  rules="$(cat "$summary_dir/scenario-4-rules-before.json")"
  # The edited template must render an absolute directory: save-time validation
  # renders against the fixed sample and refuses a relative result with
  # NAMING_TARGET_NOT_ABSOLUTE. A "Rule Drift" literal would therefore be a
  # rejected save, not the inert-but-stored edit this scenario is about.
  api_patch_rules_payload="$(python3 - "$summary_dir/scenario-4-rules-before.json" "$library_root/Scenario Rule Drift" <<'PY'
import json,sys
r=json.load(open(sys.argv[1])); s=r['sections']; n=s['naming']; drift=sys.argv[2]
if n.get('mode')=='replace':
    n['value']['tv']['directory_template']=drift
else:
    n={'mode':'replace','value':{'language':'js-naming-1','tv':{'directory_template':drift,'video_filename_template':"${sanitize(anime.title)} - E${pad(ep.display, 2)}.${file.ext}"},'movie':{'directory_template':drift,'video_filename_template':"${sanitize(anime.title)}.${file.ext}"},'subtitle_suffix_template':'und'}}
s['naming']=n
print(json.dumps({'expected_version':r['version'],'sections':s},separators=(',',':')))
PY
)"
  api_patch "/anime/anime-e2e/rules" "$api_patch_rules_payload" >"$summary_dir/scenario-4-rules-save.json"
  drift_source="$recovery_path"
  drift_hash="$(fingerprint "$drift_source")"
  drift_path="$library_root/Scenario Drift/$(basename "$drift_source")"
  mkdir -p "$(dirname "$drift_path")"
  observe_disk scenario-4-before "$drift_source"
  mv "$drift_source" "$drift_path"
  $PSQL -q -c "UPDATE placements SET physical_path='${drift_path//\'/\'\'}', version=version+1, updated_at=now() WHERE id='$placement_id'"
  scan_and_calibrate scenario-4
  diffs="$(api_get '/reconciliation/scans/anime-e2e/path-diffs?generation=0')"
  printf '%s' "$diffs" >"$summary_dir/scenario-4-path-diffs.json"
  drift_expected="$(path_diff_expected "$diffs" "$drift_path")"
  [[ -n "$drift_expected" && "$drift_expected" != "$drift_path" ]] || { echo "scenario 4 path-diff does not show the drift for $drift_path" >&2; exit 1; }
  if scenario_requested 4; then
    printf '{"scenario":4,"alignment_enabled":false,"path_diff_differs":true,"differs_observed":true,"expected_path":"%s","rule_save":"saved without moving file","before":"%s","after":"%s","sha256":"%s"}\n' "$drift_expected" "$drift_source" "$drift_path" "$drift_hash" >>"$scenario_json"
    cat "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"
    cat "$summary_dir/disk-observations.txt" | tee -a "$durable_evidence_dir/disk-observations.txt"
  fi
  fi
  if scenario_requested 5; then
    current_path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
    ui_drift_path="$library_root/Scenario UI Drift/$(basename "$current_path")"
    mkdir -p "$(dirname "$ui_drift_path")"
    mv "$current_path" "$ui_drift_path"
    $PSQL -q -c "UPDATE placements SET physical_path='${ui_drift_path//\'/\'\'}',version=version+1,updated_at=now() WHERE id='$placement_id'"
    observe_disk scenario-5-before "$ui_drift_path"
    # The confirm dialog renders the latest path-diff, so the drift this
    # scenario just produced must be in the calibration the preview reads. The
    # scan runs while alignment is still off, exactly like a user who checks
    # the preview before enabling it.
    scan_and_calibrate scenario-5-preview
    preview_diffs="$(api_get '/reconciliation/scans/anime-e2e/path-diffs?generation=0')"
    printf '%s' "$preview_diffs" >"$summary_dir/scenario-5-path-diffs.json"
    canonical_target="$(path_diff_expected "$preview_diffs" "$ui_drift_path")"
    [[ -n "$canonical_target" && "$canonical_target" != "$ui_drift_path" ]] || { echo "scenario 5 preview does not show a target for $ui_drift_path" >&2; exit 1; }
    run_alignment_browser 5 "$ui_drift_path" "$placement_id"
    # Scope the wait to this scenario's own source path: other rows may plan
    # independently, and an aggregate count would wait for a value that never
    # settles to exactly one.
    alignment_row="$(wait_alignment_terminal "$ui_drift_path")" || exit 1
    [[ "${alignment_row%%|*}" == "completed" ]] || { echo "scenario 5 alignment ended $alignment_row" >&2; exit 1; }
    alignment_target="$(printf '%s' "$alignment_row" | cut -d'|' -f3)"
    canonical_path="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
    # Preview and execution consume the same frozen Placement coverage. An
    # internally consistent owner path is not enough if the user saw another.
    if [[ "$canonical_path" != "$alignment_target" || "$canonical_target" != "$alignment_target" || ! -f "$canonical_path" || -e "$ui_drift_path" ]]; then
      echo "scenario 5 mismatch: placement=$canonical_path alignment_target=$alignment_target drift=$ui_drift_path drift_exists=$([[ -e "$ui_drift_path" ]] && echo yes || echo no)" >&2
      exit 1
    fi
    preview_matches_alignment=false
    [[ "$canonical_target" == "$alignment_target" ]] && preview_matches_alignment=true
    placement_count="$($PSQL -tAc "SELECT count(*) FROM placements p JOIN placement_coverage c ON c.placement_id=p.id WHERE p.id='$placement_id' AND p.physical_path='$canonical_path'" | tr -d '[:space:]')"
    [[ "$placement_count" == 1 ]] || { echo 'scenario 5 placement coverage/file cardinality not exactly one' >&2; exit 1; }
    observe_disk scenario-5-after "$canonical_path"
    printf '{"scenario":5,"ui":"enable-confirm-preview-and-manual-scan","preview_expected_path":"%s","alignment_target":"%s","preview_matches_alignment":%s,"before":"%s","after":"%s","sha256":"%s","placement_count":%s}\n' "$canonical_target" "$alignment_target" "$preview_matches_alignment" "$ui_drift_path" "$canonical_path" "$(fingerprint "$canonical_path")" "$placement_count" >>"$scenario_json"
    cat "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"
    cat "$summary_dir/disk-observations.txt" | tee -a "$durable_evidence_dir/disk-observations.txt"
  fi
  if scenario_requested 6; then
    # Scenario 6 owns its own real file and occupied canonical target. Keep
    # effective alignment off until the browser's explicit per-anime confirm.
    global_policy="$(api_get /file-maintenance/policy)"
    global_version="$(printf '%s' "$global_policy" | python3 -c 'import json,sys;print(json.load(sys.stdin)["version"])')"
    api_put /file-maintenance/policy "{\"enabled\":true,\"alignment_enabled\":false,\"expected_version\":$global_version}" >"$summary_dir/scenario-6-policy-off.json"
    # Scenario 5 enabled this anime's own override, which outranks the global
    # switch: clear it back to inherit before this scenario's drift, so effective
    # alignment stays off until the browser confirms it again.
    anime_policy="$(api_get /file-maintenance/policy/anime/anime-e2e)"
    anime_version="$(printf '%s' "$anime_policy" | python3 -c 'import json,sys;print(json.load(sys.stdin)["version"])')"
    api_put /file-maintenance/policy/anime/anime-e2e "{\"enabled\":true,\"alignment_enabled\":null,\"expected_version\":$anime_version}" >"$summary_dir/scenario-6-anime-policy-inherit.json"
    [[ "$(api_get /file-maintenance/policy/anime/anime-e2e | python3 -c 'import json,sys;print(json.load(sys.stdin)["alignment_enabled"])')" == "None" ]] || { echo 'scenario 6 failed to clear the anime alignment override' >&2; exit 1; }
    collision_source="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')"
    collision_drift="$library_root/Scenario Collision/$(basename "$collision_source")"
    mkdir -p "$(dirname "$collision_drift")"
    mv "$collision_source" "$collision_drift"
    $PSQL -q -c "UPDATE placements SET physical_path='${collision_drift//\'/\'\'}',version=version+1,updated_at=now() WHERE id='$placement_id'"
    collision_source="$collision_drift"
    scan_and_calibrate scenario-6-preview
    collision_diffs="$(api_get '/reconciliation/scans/anime-e2e/path-diffs?generation=0')"
    printf '%s' "$collision_diffs" >"$summary_dir/scenario-6-path-diffs.json"
    collision_preview="$(path_diff_expected "$collision_diffs" "$collision_source")"
    [[ -n "$collision_preview" && "$collision_preview" != "$collision_source" ]] || { echo 'scenario 6 preview does not show the drift' >&2; exit 1; }
    # The path to occupy is THIS file's own preview target: the same expected
    # path the browser's confirmation dialog names, so the obstacle sits exactly
    # where the alignment would write. Taking the newest completed operation for
    # the anime instead named whichever file aligned last (another placement),
    # which left this target free and let a legitimate move succeed.
    target_expected="$collision_preview"
    # The preview names a destination the alignment has not created yet, so the
    # obstacle's directory is made here rather than borrowed from an earlier
    # scenario's side effects.
    mkdir -p "$(dirname "$target_expected")"
    [[ ! -e "$target_expected" ]] || { echo "scenario 6 occupied target already holds a file: $target_expected" >&2; exit 1; }
    printf 'occupied-target-object' >"$target_expected"
    collision_hash="$(fingerprint "$target_expected")"
    source_hash="$(fingerprint "$collision_source")"
    observe_disk scenario-6-source-before "$collision_source"; observe_disk scenario-6-target-before "$target_expected"
    run_alignment_browser 6 "$collision_source" "$placement_id"
    collision_row="$(wait_alignment_terminal "$collision_source")" || exit 1
    [[ "$collision_row" == "blocked|target_occupied|"* ]] || { echo "scenario 6 alignment ended $collision_row, want blocked/target_occupied" >&2; exit 1; }
    [[ "$(fingerprint "$collision_source")" == "$source_hash" && "$(fingerprint "$target_expected")" == "$collision_hash" && "$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$placement_id'" | tr -d '\r')" == "$collision_source" ]] || { echo 'scenario 6 collision mutated its fresh source, occupied target, or placement' >&2; exit 1; }
    observe_disk scenario-6-source-after "$collision_source"; observe_disk scenario-6-target-after "$target_expected"
    printf '{"scenario":6,"db_state":"blocked","failure_code":"target_occupied","source":"%s","source_sha256":"%s","target":"%s","target_sha256":"%s","preview_expected_path":"%s"}\n' "$collision_source" "$source_hash" "$target_expected" "$collision_hash" "$collision_preview" >>"$scenario_json"
    cat "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"
    cat "$summary_dir/disk-observations.txt" | tee -a "$durable_evidence_dir/disk-observations.txt"
  fi

  if scenario_requested 8; then
  # ---------------------------------------------------------------------
  # 8. Scan-alignment recovery (design: .trellis/tasks/10-25-scan-alignment-recovery).
  #    A recovery candidate is an OBSERVATION, not ownership: when an owned
  #    placement is missing at its current path AND at its frozen provenance
  #    locator, but the COMPLETE unit (video + every companion) already sits
  #    at the path the CURRENT naming rules render, the scan freezes a
  #    candidate and keeps the disposition `missing`. Only the explicit
  #    confirm (POST /anime/{id}/files/recover) re-attaches the rows.
  #    This scenario owns a dedicated placement: scenarios 4/5/6 rewrite the
  #    path of $placement_id, so reusing it here would make the assertions
  #    unreadable.
  echo "== scenario 8: scan-alignment recovery =="
  s8_placement_id='plc-e2e-8'
  s8_companion_id='cmp-e2e-8'
  s8_companion_size=1024
  # Alignment stays off for the whole scenario: the only writer that may move
  # ownership here is the recovery confirm. The global switch alone is not
  # enough — an anime override (scenario 5 writes one) outranks it — so the
  # anime scope is cleared back to inherit first.
  s8_global_policy="$(api_get /file-maintenance/policy)"
  s8_global_version="$(printf '%s' "$s8_global_policy" | python3 -c 'import json,sys;print(json.load(sys.stdin)["version"])')"
  api_put /file-maintenance/policy "{\"enabled\":true,\"alignment_enabled\":false,\"expected_version\":$s8_global_version}" >"$summary_dir/scenario-8-policy.json"
  s8_anime_policy="$(api_get /file-maintenance/policy/anime/anime-e2e)"
  s8_anime_version="$(printf '%s' "$s8_anime_policy" | python3 -c 'import json,sys;print(json.load(sys.stdin)["version"])')"
  api_put /file-maintenance/policy/anime/anime-e2e "{\"enabled\":true,\"alignment_enabled\":null,\"expected_version\":$s8_anime_version}" >"$summary_dir/scenario-8-anime-policy.json"
  [[ "$(api_get /file-maintenance/policy/anime/anime-e2e | python3 -c 'import json,sys;print(json.load(sys.stdin)["alignment_enabled"])')" == "None" ]] || { echo 'scenario 8 failed to clear the anime alignment override' >&2; exit 1; }

  # -- seed one independent file_operation placement under anime-e2e -------
  # The recovery probe renders through RenderPlacementTargets, which requires
  # file-operation lineage (frozen release/source/snapshot facts), so this
  # chain carries its own acquisition -> artifact -> plan -> operation rows
  # exactly like the EP05 source seeded above.
  s8_download="$download_root/EP08.mkv"
  head -c 65536 /dev/urandom >"$s8_download"
  s8_hex="$(shasum -a 256 "$s8_download" | awk '{print $1}')"
  s8_fp='{"algorithm":"sha256","value":"'$s8_hex'","size_bytes":65536}'
  s8_source_path="$library_root/Scenario 8 Source/EP08.mkv"
  s8_companion_source="$library_root/Scenario 8 Source/EP08.zh.ass"
  mkdir -p "$(dirname "$s8_source_path")"
  cp "$s8_download" "$s8_source_path"
  head -c "$s8_companion_size" /dev/urandom >"$s8_companion_source"
  # The companion's registered identity is the sha256 of its bytes, exactly as
  # registration stores it from the sidecar operation / artifact file.
  s8_companion_digest="$(shasum -a 256 "$s8_companion_source" | awk '{print $1}')"
  s8_payload_digest="sha256:$(repeat_hex 3)"
  s8_manifest_digest="sha256:$(repeat_hex 4)"
  s8_file_ref="$(torrent_file_ref "$s8_payload_digest" 'EP08.mkv')"
  s8_fixture_json="$(go run ./internal/testutil/frozenpolicy/cmd \
    --payload-digest "$s8_payload_digest" \
    --manifest-digest "$s8_manifest_digest" \
    --route-root "$library_root" \
    --file EP08.mkv \
    --title '[示例字幕组] 进击的巨人 - 08 [1080p][chs]')"
  printf '%s' "$s8_fixture_json" >"$summary_dir/scenario-8-acquisition-fixture.json"
  python3 - "$summary_dir/scenario-8-acquisition-fixture.json" <<'PY'
import json,sys
fixture=json.load(open(sys.argv[1], encoding="utf-8"))
plan=fixture["release_facts"]["batch_plan"]
if plan["coverage_keys"] != ["tv:e8"]:
    raise SystemExit("scenario 8 fixture batch plan does not cover tv:e8: %r" % (plan["coverage_keys"],))
if fixture["file_evidence"]["name"] != "EP08.mkv":
    raise SystemExit("scenario 8 fixture file evidence is not EP08.mkv")
PY
  s8_acq_input_sql="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["acquisition_input"])' <<<"$s8_fixture_json")"
  s8_acq_input_sql="${s8_acq_input_sql//\'/\'\'}"
  s8_identity_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["identity"]))' <<<"$s8_fixture_json")"
  s8_release_facts_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["release_facts"]))' <<<"$s8_fixture_json")"
  s8_file_evidence_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["file_evidence"]))' <<<"$s8_fixture_json")"
  s8_effective_facts_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["effective_facts"]))' <<<"$s8_fixture_json")"
  if ! $PSQL -q -e >"$summary_dir/scenario-8-seed.log" 2>&1 <<E2ETHIRD
BEGIN;
INSERT INTO rss_sources(id,subscription_id,source_kind,locator,locator_display,group_name,title,episode_offset)
VALUES ('src-e2e-8','sub-e2e','custom_rss','https://feed.example.test/e2e-8','https://feed.example.test/e2e-8','示例字幕组','进击的巨人',0);
INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,parsed_facts,internal_facts,verdict,reason_code,download_task_id,version)
SELECT 'rel-e2e-8','src-e2e-8','guid:e2e-8',jsonb_set(raw_facts,'{title}','"[示例字幕组] 进击的巨人 - 08 [1080p][chs]"'),source_facts,parsed_facts,internal_facts,'submitted','submitted','dl-e2e-8',1 FROM releases WHERE id='rel-e2e';
INSERT INTO acquisitions(
  id, execution_branch, entry_kind, access_scope, request_namespace, request_key, request_hash,
  canonical_request, admission_kind, admission_http_status, admission_receipt,
  input_schema_version, admission_digest, origin_managed_anime_ref, source_release_ref,
  display_title, accepted_at, retention_anchor_at, detail_retention_seconds,
  receipt_after_accept_seconds, receipt_after_terminal_seconds, receipt_until, detail_until,
  state, terminal_at, terminal_schema, terminal_receipt, record_state,
  preparation_mode, vars_evidence_version,
  input_canonical_utf8, payload_digest, manifest_digest, manifest_item_count, manifest_sealed_at)
VALUES (
  'acq-e2e-8', 'download', 'automatic', 'deployment_owner_v1', 'auto_acquire_v1', 'auto:rel-e2e-8',
  'sha256:' || repeat('c', 64),
  '{"schema_version":1,"kind":"automatic","release_ref":"rel-e2e-8"}'::jsonb,
  'download_queued', 202,
  '{"schema_version":1,"acquisition_id":"acq-e2e-8","state":"queued"}'::jsonb,
  1,
  'sha256:' || encode(sha256(convert_to('acquisition-input:v1', 'UTF8') || decode('00', 'hex')
     || convert_to('$s8_acq_input_sql', 'UTF8')), 'hex'),
  'anime-e2e', 'rel-e2e-8',
  '{"state":"known","value":"进击的巨人 - 08","reason_code":null}'::jsonb,
  now() - interval '2 hours', now() - interval '2 hours', 2592000, 2592000, 5184000,
  now() + interval '60 days',
  NULL,
  'running', NULL, NULL, NULL,
  'full', 'none', 1,
  '$s8_acq_input_sql',
  '$s8_payload_digest',
  '$s8_manifest_digest', 1, now() - interval '2 hours');
INSERT INTO acquisition_files(acquisition_id, file_ref, ordinal, relative_path, exact_size, media_kind, identity_evidence, requirement, requirement_reason_code, selection_evidence)
VALUES ('acq-e2e-8', '$s8_file_ref', 0, 'EP08.mkv', 65536, 'video', '{}'::jsonb, 'required', 'e2e_seed_required', '{"source":"filemaintenance-e2e scenario 8"}'::jsonb);
INSERT INTO download_tasks(id,release_id,acquisition_id,frozen_input,frozen_policy,state,idempotency_key)
VALUES ('dl-e2e-8','rel-e2e-8','acq-e2e-8','{}','$frozen_policy_sql'::jsonb,'queued','download:e2e:8');
UPDATE download_tasks SET state='completed' WHERE id='dl-e2e-8';
INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,verified_at)
VALUES ('art-e2e-8','dl-e2e-8','acq-e2e-8','{}','{}','verified',CURRENT_TIMESTAMP);
INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state)
VALUES ('af-e2e-8','art-e2e-8','acq-e2e-8','$s8_file_ref','EP08.mkv','$s8_download','video',65536,'$s8_fp'::jsonb,'{"resolution":"1080p","video_codec":"HEVC","container":"mkv"}','not_applicable');
INSERT INTO postprocessing_plans(id,artifact_id,artifact_version,state,input_digest,acquisition_id)
VALUES ('plan-e2e-8','art-e2e-8',1,'queued','sha256:' || repeat('3', 64),'acq-e2e-8');
INSERT INTO file_operations(id,artifact_id,acquisition_id,artifact_version,artifact_file_id,file_ref,operation,source_path,target_path,source_fingerprint,target_fingerprint,collision_policy,frozen_render_snapshot,frozen_config_snapshot,state,observed_at,idempotency_key,correlation_id,causation_type,causation_id,postprocessing_plan_id,file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds)
SELECT 'op-e2e-8','art-e2e-8','acq-e2e-8',1,'af-e2e-8','$s8_file_ref','move','$s8_download','$s8_source_path','$s8_fp'::jsonb,'$s8_fp'::jsonb,'block','{}',frozen_config_snapshot,'completed',CURRENT_TIMESTAMP,'op-e2e-8','corr-e2e-8','artifact','art-e2e-8','plan-e2e-8',file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds FROM file_operations WHERE id=(SELECT source_owner_ref FROM placements WHERE id='$placement_id');
INSERT INTO release_candidate_facts(release_id,release_version,parser_version,input_fingerprint,raw_evidence,facts,coverage_keys,resolution,revision,source,language,"group",size_bytes)
VALUES ('rel-e2e-8',1,'parser-v1','fp-e2e-8','{"title":"[示例字幕组] 进击的巨人 - 08 [1080p][chs]"}','{"episode":8,"group_name":"示例字幕组","group":"示例字幕组","resolution":"1080p","quality":"web-dl","revision":"v1","source":"web-dl","language":"chs","size_bytes":65536}',
        '["tv:e8"]','1080p','v1','web-dl','chs','示例字幕组',65536);
INSERT INTO media_fact_snapshots(id,release_id,schema_version,resolver_version,parser_kind,input_digest,release_identity,release_facts)
VALUES ('snap-e2e-8','rel-e2e-8','media-facts-v1','episode-identity-v1','regex','digest-e2e-8','$s8_identity_sql'::jsonb,'$s8_release_facts_sql'::jsonb);
INSERT INTO media_fact_files(snapshot_id,file_ref,media_kind,size_bytes,source_fingerprint,file_evidence,effective_facts)
VALUES ('snap-e2e-8','$s8_file_ref','video',65536,'$s8_hex','$s8_file_evidence_sql'::jsonb,'$s8_effective_facts_sql'::jsonb);
-- replacement_eligible stays false: this placement exists to be observed, and
-- keeping it out of the single_copy pool makes the two seeded episodes
-- independent of every replacement decision the lane already settled.
INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,quality_facts_ref,facts_version,correlation_id,causation_type,causation_id,replacement_eligible,verification_state,state,version)
VALUES ('$s8_placement_id','$s8_source_path','anime-e2e','file_operation','op-e2e-8','$s8_fp'::jsonb,'$s8_fp'::jsonb,'rel-e2e-8','media-facts-v1','corr-e2e-8','file_operation','op-e2e-8',false,'verified','active',1);
INSERT INTO placement_coverage(placement_id,observation_key,coverage_ordinal,facts_version,evidence_ref)
VALUES ('$s8_placement_id','tv:e8',0,'media-facts-v1','op-e2e-8');
-- One active subtitle companion. `fingerprint` is the registration-time
-- content digest of the file (hex, as the column CHECK demands); recovery
-- keeps it, and the loser cleanup handoff freezes and checks it before moving
-- the file. The recovery candidate freezes the METADATA fingerprint it
-- observes, never this digest.
INSERT INTO placement_companions(id,placement_id,managed_anime_id,source_ref,registration,media_kind,suffix,original_extension,current_path,size_bytes,fingerprint,state,version)
VALUES ('$s8_companion_id','$s8_placement_id','anime-e2e','e2e-scenario-8-companion','adopted','subtitle','zh','ass',
        '$s8_companion_source','$s8_companion_size',
        '$s8_companion_digest','active',1);
COMMIT;
E2ETHIRD
  then
    echo "scenario 8 seed failed; see $summary_dir/scenario-8-seed.log" >&2
    sed -n '1,40p' "$summary_dir/scenario-8-seed.log" >&2 || true
    exit 1
  fi
  observe_disk scenario-8-source "$s8_source_path"

  # -- step 1a: learn the CURRENT rendered target from the preview --------
  # The rule set is the anime's live one (scenarios 4/5 may have edited it),
  # so the target is read from the same preview the UI renders instead of
  # being composed here.
  scan_and_calibrate scenario-8-preview
  s8_diffs_file="$summary_dir/scenario-8-path-diffs.json"
  api_get '/reconciliation/scans/anime-e2e/path-diffs?generation=0' >"$s8_diffs_file"
  s8_target="$(path_diff_expected "$(cat "$s8_diffs_file")" "$s8_source_path")"
  [[ -n "$s8_target" && "$s8_target" != "$s8_source_path" ]] || { echo "scenario 8 preview does not render a different target for $s8_source_path (got [$s8_target])" >&2; exit 1; }
  # Companion diffs carry no id in the path-diff contract (they are keyed by
  # file_path), so the unit's target is derived from the video target through
  # the frozen companion suffix/extension the re-render uses, and then checked
  # against the shipped preview. A disagreement stops the run before any move.
  s8_companion_suffix="$($PSQL -tAc "SELECT suffix FROM placement_companions WHERE id='$s8_companion_id'" | tr -d '\r')"
  s8_companion_extension="$($PSQL -tAc "SELECT original_extension FROM placement_companions WHERE id='$s8_companion_id'" | tr -d '\r')"
  s8_companion_target="$(python3 - "$s8_target" "$s8_companion_suffix" "$s8_companion_extension" <<'PY'
import os,sys
video,suffix,extension=sys.argv[1],sys.argv[2],sys.argv[3]
stem=os.path.splitext(os.path.basename(video))[0]
name=stem + ("." + suffix if suffix else "")
print(os.path.join(os.path.dirname(video), name + "." + extension))
PY
)"
  s8_declared_companion_target="$(python3 - "$s8_diffs_file" "$s8_source_path" "$s8_companion_source" <<'PY'
import json,sys
view=json.load(open(sys.argv[1], encoding="utf-8"))
path,companion_path=sys.argv[2],sys.argv[3]
rows=[r for r in view if r.get("file_path")==path]
if len(rows)!=1:
    raise SystemExit("scenario 8: expected one path-diff row for %s, got %d" % (path,len(rows)))
companions=[c for c in (rows[0].get("companions") or []) if c.get("file_path")==companion_path]
if len(companions)!=1:
    raise SystemExit("scenario 8: expected one companion diff for %s, got %r" % (companion_path,rows[0].get("companions")))
expected=companions[0].get("expected_path")
if not expected:
    raise SystemExit("scenario 8: companion %s renders no target: %r" % (companion_path,companions[0]))
print(expected)
PY
)"
  [[ -n "$s8_companion_target" && "$s8_declared_companion_target" == "$s8_companion_target" ]] || { echo "scenario 8: derived companion target [$s8_companion_target] disagrees with the preview [$s8_declared_companion_target]" >&2; exit 1; }
  echo "scenario 8 rendered target: $s8_target (companion $s8_companion_target)"

  # -- step 1b: move the owned file to that target, drop both locators ------
  # physical_path and the frozen provenance source_path must BOTH be gone:
  # that is the only state in which the scan probes the rendered target.
  s8_source_hash="$(fingerprint "$s8_source_path")"
  # SQL integers, read as text and normalised so arithmetic below is safe.
  s8_count() { printf '%s' "$1" | tr -d '[:space:]'; }
  s8_placement_version="$(s8_count "$($PSQL -tAc "SELECT version FROM placements WHERE id='$s8_placement_id'")")"
  s8_companion_version="$(s8_count "$($PSQL -tAc "SELECT version FROM placement_companions WHERE id='$s8_companion_id'")")"
  [[ "$s8_source_hash" != MISSING ]] || { echo "scenario 8 fixture file missing before the move: $s8_source_path" >&2; exit 1; }
  mkdir -p "$(dirname "$s8_target")" "$(dirname "$s8_companion_target")"
  [[ ! -e "$s8_target" && ! -e "$s8_companion_target" ]] || { echo "scenario 8 rendered target already exists before the move: $s8_target / $s8_companion_target" >&2; exit 1; }
  # The user moved the WHOLE unit by hand: video and subtitle together, out of
  # the old directory. Both old locators and the download provenance are gone.
  s8_companion_hash="$(fingerprint "$s8_companion_source")"
  mv "$s8_source_path" "$s8_target"
  mv "$s8_companion_source" "$s8_companion_target"
  rm -f -- "$s8_download"
  [[ "$(fingerprint "$s8_companion_target")" == "$s8_companion_hash" ]] || { echo "scenario 8 companion move changed the bytes: $s8_companion_source($s8_companion_hash) -> $s8_companion_target($(fingerprint "$s8_companion_target"))" >&2; exit 1; }
  s8_target_hash="$(fingerprint "$s8_target")"
  [[ "$s8_target_hash" == "$s8_source_hash" ]] || { echo "scenario 8 move changed the bytes: $s8_source_path($s8_source_hash) -> $s8_target($s8_target_hash)" >&2; exit 1; }
  [[ ! -e "$s8_source_path" && ! -e "$s8_download" ]] || { echo "scenario 8 locators still present: source=$s8_source_path provenance=$s8_download" >&2; exit 1; }
  observe_disk scenario-8-target "$s8_target"

  # -- step 1c: the scan only OFFERS; nothing is recovered ---------------
  scan_and_calibrate scenario-8-candidate
  # The recovery read model and the confirm both refuse while a scan or its
  # calibration is still in flight (`scan_running`), and calibration commits
  # before its work item retires. Drain that window so the assertions below
  # read the settled generation instead of racing it.
  wait_sql "SELECT count(*) FROM work_items WHERE owner_type='file_maintenance' AND owner_id='anime-e2e' AND kind IN ('reconciliation_scan','reconciliation_calibrate') AND state IN ('available','leased')" 0
  s8_generation="$(calibration_generation)"
  s8_disposition="$($PSQL -tAc "SELECT COALESCE(disposition,'') FROM reconciliation_files WHERE managed_anime_id='anime-e2e' AND generation=$s8_generation AND file_path='$s8_source_path'" | tr -d '[:space:]')"
  [[ "$s8_disposition" == missing ]] || { echo "scenario 8: the observation row for $s8_source_path is [$s8_disposition], want missing" >&2; exit 1; }
  s8_candidates="$($PSQL -tAc "SELECT COALESCE(facts->'recovery_candidates','[]')::text FROM reconciliation_files WHERE managed_anime_id='anime-e2e' AND generation=$s8_generation AND file_path='$s8_source_path'")"
  s8_candidate_field() {
    printf '%s' "$1" | python3 -c '
import json,sys
documents=json.load(sys.stdin)
placement_id,path=sys.argv[1],sys.argv[2]
if not isinstance(documents,list):
    print("no_candidate"); raise SystemExit(0)
matches=[c for c in documents if isinstance(c,dict) and c.get("placement_id")==placement_id]
if not matches:
    print("no_candidate"); raise SystemExit(0)
value=matches[-1]
for key in path.split("."):
    if not isinstance(value,dict) or key not in value:
        print("null"); raise SystemExit(0)
    value=value[key]
print("null" if value is None else (json.dumps(value, ensure_ascii=False, sort_keys=True) if isinstance(value,(dict,list)) else value))
' "$s8_placement_id" "$2"
  }
  s8_candidate_target="$(s8_candidate_field "$s8_candidates" target_path)"
  if [[ "$s8_candidate_target" != "$s8_target" ]]; then
    # Everything an operator needs to see WHY: where the unit actually is, what
    # the scan observed, and whether the recovery endpoints exist at all.
    s8_files_probe="$(api_get /anime/anime-e2e/files 2>/dev/null || echo '<unreadable>')"
    {
      echo '== scenario 8: no recovery candidate =='
      echo "generation: $s8_generation"
      echo "placement: $s8_placement_id (physical_path=$s8_source_path)"
      echo "companion: $s8_companion_id (current_path=$s8_companion_source)"
      echo "rendered target: $s8_target"
      echo "companion target: $s8_companion_target"
      echo "facts->recovery_candidates: $s8_candidates"
      echo '-- API files row for the placement --'
      printf '%s' "$s8_files_probe" | python3 -c 'import json,sys
try:
    view=json.load(sys.stdin)
except Exception as exc:
    print("unreadable files view: %r" % (exc,)); raise SystemExit(0)
for row in view.get("files") or []:
    if row.get("placement_id")==sys.argv[1]:
        print(json.dumps(row, ensure_ascii=False, indent=2))' "$s8_placement_id"
      echo '-- recover endpoint probe --'
      curl -sS -o /dev/null -w 'POST /anime/anime-e2e/files/recover -> HTTP %{http_code}\n' \
        -X POST "${auth[@]}" -d '{"placement_id":"","generation":0,"target_path":"","fingerprint":"","placement_version":0}' \
        "$api/anime/anime-e2e/files/recover" || true
    } | tee -a "$durable_evidence_dir/scenario-8-diagnostics.txt" >&2
    echo "scenario 8: generation $s8_generation froze no recovery candidate for placement $s8_placement_id at $s8_target (facts->recovery_candidates=$s8_candidates)." >&2
    echo "scenario 8: the probe must freeze a candidate here (both locators missing, rendered unit on disk at the frozen size, no other owner). No candidate is a real backend failure: read the diagnostics above for the probe, renderer, occupancy and size evidence." >&2
    exit 1
  fi
  s8_candidate_version="$(s8_candidate_field "$s8_candidates" placement_version)"
  s8_candidate_fingerprint="$(s8_candidate_field "$s8_candidates" target_fingerprint)"
  s8_candidate_size="$(s8_candidate_field "$s8_candidates" size_bytes)"
  [[ "$s8_candidate_version" == "$s8_placement_version" ]] || { echo "scenario 8: candidate froze placement_version $s8_candidate_version, want $s8_placement_version" >&2; exit 1; }
  [[ "$s8_candidate_size" == 65536 ]] || { echo "scenario 8: candidate froze size_bytes $s8_candidate_size, want 65536" >&2; exit 1; }
  [[ "$s8_candidate_fingerprint" == meta:size=65536* ]] || { echo "scenario 8: candidate fingerprint [$s8_candidate_fingerprint] is not a metadata fingerprint of the target" >&2; exit 1; }
  s8_companion_frozen="$(s8_candidate_field "$s8_candidates" companions)"
  [[ "$s8_companion_frozen" != no_candidate && "$s8_companion_frozen" != "[]" && "$s8_companion_frozen" != null ]] || { echo "scenario 8: the candidate froze no companion unit for $s8_companion_id ($s8_companion_frozen)" >&2; exit 1; }

  # The API row must expose that frozen token and offer the confirm.
  api_get /anime/anime-e2e/files >"$summary_dir/scenario-8-files.json"
  s8_row="$(python3 - "$summary_dir/scenario-8-files.json" "$s8_placement_id" <<'PY'
import json,sys
view=json.load(open(sys.argv[1], encoding="utf-8"))
placement_id=sys.argv[2]
rows=[r for r in view.get("files") or [] if r.get("placement_id")==placement_id]
if len(rows)!=1:
    raise SystemExit("scenario 8: expected one files row for placement %s, got %d (rows: %r)" % (placement_id,len(rows),[r.get("placement_id") for r in view.get("files") or []]))
print(json.dumps(rows[0], ensure_ascii=False))
PY
)"
  s8_field() {
    printf '%s' "$1" | python3 -c '
import json,sys
value=json.load(sys.stdin)
for key in sys.argv[1].split("."):
    if not isinstance(value,dict) or key not in value:
        print("null"); raise SystemExit(0)
    value=value[key]
if value is None:
    print("null")
elif isinstance(value,bool):
    print("true" if value else "false")
else:
    print(value)
' "$2"
  }
  s8_row_status="$(s8_field "$s8_row" status)"
  s8_token_target="$(s8_field "$s8_row" recovery.target_path)"
  s8_token_generation="$(s8_field "$s8_row" recovery.generation)"
  s8_token_fingerprint="$(s8_field "$s8_row" recovery.fingerprint)"
  s8_token_version="$(s8_field "$s8_row" recovery.placement_version)"
  s8_recover_available="$(s8_field "$s8_row" actions.recover.available)"
  [[ "$s8_row_status" == missing ]] || { echo "scenario 8: the files row of $s8_placement_id has status [$s8_row_status], want missing (the candidate is an observation, the disposition must not change)" >&2; exit 1; }
  if [[ "$s8_token_target" != "$s8_target" || "$s8_token_generation" != "$s8_generation" || "$s8_token_version" != "$s8_placement_version" || "$s8_recover_available" != true ]]; then
    echo "scenario 8: the files row does not expose the frozen confirm token: recovery.target_path=[$s8_token_target] (want $s8_target), recovery.generation=[$s8_token_generation] (want $s8_generation), recovery.placement_version=[$s8_token_version] (want $s8_placement_version), recovery.fingerprint=[$s8_token_fingerprint], actions.recover.available=[$s8_recover_available] (want true), actions.recover.reason_code=[$(s8_field "$s8_row" actions.recover.reason_code)]" >&2
    exit 1
  fi
  # The scan is an observer: no ownership row, version or byte moved.
  s8_path_after_scan="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$s8_placement_id'" | tr -d '\r')"
  s8_version_after_scan="$(s8_count "$($PSQL -tAc "SELECT version FROM placements WHERE id='$s8_placement_id'")")"
  s8_companion_path_after_scan="$($PSQL -tAc "SELECT current_path FROM placement_companions WHERE id='$s8_companion_id'" | tr -d '\r')"
  s8_companion_version_after_scan="$(s8_count "$($PSQL -tAc "SELECT version FROM placement_companions WHERE id='$s8_companion_id'")")"
  [[ "$s8_path_after_scan" == "$s8_source_path" ]] || { echo "scenario 8: the scan moved the placement physical_path to [$s8_path_after_scan], want the unchanged $s8_source_path" >&2; exit 1; }
  [[ "$s8_version_after_scan" == "$s8_placement_version" ]] || { echo "scenario 8: the scan bumped the placement version $s8_placement_version -> $s8_version_after_scan" >&2; exit 1; }
  [[ "$s8_companion_path_after_scan" == "$s8_companion_source" ]] || { echo "scenario 8: the scan moved the companion current_path to [$s8_companion_path_after_scan], want the unchanged $s8_companion_source" >&2; exit 1; }
  [[ "$s8_companion_version_after_scan" == "$s8_companion_version" ]] || { echo "scenario 8: the scan bumped the companion version $s8_companion_version -> $s8_companion_version_after_scan" >&2; exit 1; }
  [[ "$(fingerprint "$s8_target")" == "$s8_target_hash" ]] || { echo "scenario 8: the scan rewrote the bytes at $s8_target ($s8_target_hash -> $(fingerprint "$s8_target"))" >&2; exit 1; }
  s8_journal_count() { s8_count "$($PSQL -tAc "SELECT count(*) FROM subscription_log_entries WHERE subject_id='$s8_placement_id' AND reason_code='file_recovered'")"; }
  s8_journal_before="$(s8_journal_count)"

  # -- step 5 (before any confirm): a changed target refuses loudly --------
  # The confirm is refused with a typed 409, so the body must be captured: a
  # bare `curl -f` would print only the HTTP status. The status is echoed (not
  # assigned) because the caller runs this in a command substitution.
  s8_post_recover() {
    curl -sS -o "$2" -w '%{http_code}' -X POST "${auth[@]}" -d "$1" "$api/anime/anime-e2e/files/recover" || true
  }
  s8_recover_body() {
    printf '{"placement_id":"%s","generation":%s,"target_path":"%s","fingerprint":"%s","placement_version":%s}' \
      "$s8_placement_id" "$1" "$2" "$3" "$4"
  }
  printf 'x' >>"$s8_target"
  s8_tampered_size="$(wc -c <"$s8_target" | tr -d '[:space:]')"
  [[ "$s8_tampered_size" == 65537 ]] || { echo "scenario 8: appending one byte left the target at $s8_tampered_size bytes, want 65537" >&2; exit 1; }
  s8_http_code="$(s8_post_recover "$(s8_recover_body "$s8_token_generation" "$s8_token_target" "$s8_token_fingerprint" "$s8_token_version")" "$summary_dir/scenario-8-refusal.json")"
  s8_refusal_body="$(cat "$summary_dir/scenario-8-refusal.json")"
  s8_refusal_http="$s8_http_code"
  [[ "$s8_http_code" == 409 ]] || { echo "scenario 8: the confirm of a size-changed target answered HTTP $s8_http_code, want 409 (body: $s8_refusal_body)" >&2; exit 1; }
  s8_refusal_code="$(printf '%s' "$s8_refusal_body" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("code",""))')"
  s8_refusal_reason="$(printf '%s' "$s8_refusal_body" | python3 -c 'import json,sys;d=json.load(sys.stdin);print((d.get("details") or [{}])[0].get("code",""))')"
  [[ "$s8_refusal_code" == ANIME_FILES_RECOVER_REFUSED ]] || { echo "scenario 8: refusal code [$s8_refusal_code], want ANIME_FILES_RECOVER_REFUSED (body: $s8_refusal_body)" >&2; exit 1; }
  [[ "$s8_refusal_reason" == target_changed ]] || { echo "scenario 8: refusal reason [$s8_refusal_reason], want target_changed (body: $s8_refusal_body)" >&2; exit 1; }
  s8_version_after_refusal="$(s8_count "$($PSQL -tAc "SELECT version FROM placements WHERE id='$s8_placement_id'")")"
  s8_companion_version_after_refusal="$(s8_count "$($PSQL -tAc "SELECT version FROM placement_companions WHERE id='$s8_companion_id'")")"
  s8_path_after_refusal="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$s8_placement_id'" | tr -d '\r')"
  s8_journal_after_refusal="$(s8_journal_count)"
  [[ "$s8_version_after_refusal" == "$s8_placement_version" && "$s8_path_after_refusal" == "$s8_source_path" && "$s8_companion_version_after_refusal" == "$s8_companion_version" && "$s8_journal_after_refusal" == "$s8_journal_before" ]] || {
    echo "scenario 8: the refusal wrote ownership: placement $s8_path_after_refusal v$s8_version_after_refusal (want $s8_source_path v$s8_placement_version), companion v$s8_companion_version_after_refusal (want v$s8_companion_version), file_recovered journal $s8_journal_after_refusal (want $s8_journal_before)" >&2
    exit 1
  }
  # Restore the frozen identity: truncating back keeps the inode, so the
  # metadata fingerprint matches the frozen candidate again and the bytes are
  # exactly the ones the scan observed.
  truncate -s 65536 "$s8_target"
  [[ "$(fingerprint "$s8_target")" == "$s8_target_hash" ]] || { echo "scenario 8: restoring the target after the refusal did not reproduce $s8_target_hash (now $(fingerprint "$s8_target"))" >&2; exit 1; }
  echo "scenario 8 refusal: HTTP 409 $s8_refusal_code/$s8_refusal_reason with ownership untouched"

  # -- step 2: the explicit confirm is what recovers -----------------------
  s8_confirm_body="$(s8_recover_body "$s8_token_generation" "$s8_token_target" "$s8_token_fingerprint" "$s8_token_version")"
  s8_http_code="$(s8_post_recover "$s8_confirm_body" "$summary_dir/scenario-8-confirm.json")"
  s8_confirm_response="$(cat "$summary_dir/scenario-8-confirm.json")"
  [[ "$s8_http_code" == 200 ]] || { echo "scenario 8: the confirm answered HTTP $s8_http_code, want 200 (body: $s8_confirm_response)" >&2; exit 1; }
  s8_confirm_replay="$(printf '%s' "$s8_confirm_response" | python3 -c 'import json,sys;d=json.load(sys.stdin);print("true" if d.get("replay") else "false")')"
  s8_confirm_target="$(printf '%s' "$s8_confirm_response" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("target_path",""))')"
  s8_confirm_companions="$(printf '%s' "$s8_confirm_response" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("companions",-1))')"
  [[ "$s8_confirm_replay" == false ]] || { echo "scenario 8: the first confirm reported replay=$s8_confirm_replay" >&2; exit 1; }
  [[ "$s8_confirm_target" == "$s8_target" ]] || { echo "scenario 8: the confirm reports target [$s8_confirm_target], want $s8_target" >&2; exit 1; }
  [[ "$s8_confirm_companions" == 1 ]] || { echo "scenario 8: the confirm reports $s8_confirm_companions companions, want 1" >&2; exit 1; }
  s8_path_after_confirm="$($PSQL -tAc "SELECT physical_path FROM placements WHERE id='$s8_placement_id'" | tr -d '\r')"
  s8_version_after_confirm="$(s8_count "$($PSQL -tAc "SELECT version FROM placements WHERE id='$s8_placement_id'")")"
  s8_evidence_after_confirm="$($PSQL -tAc "SELECT source_evidence_type FROM placements WHERE id='$s8_placement_id'" | tr -d '[:space:]')"
  s8_companion_path_after_confirm="$($PSQL -tAc "SELECT current_path FROM placement_companions WHERE id='$s8_companion_id'" | tr -d '\r')"
  s8_companion_version_after_confirm="$(s8_count "$($PSQL -tAc "SELECT version FROM placement_companions WHERE id='$s8_companion_id'")")"
  s8_journal_after_confirm="$(s8_journal_count)"
  [[ "$s8_path_after_confirm" == "$s8_target" ]] || { echo "scenario 8: after the confirm the placement owns [$s8_path_after_confirm], want $s8_target" >&2; exit 1; }
  [[ "$s8_version_after_confirm" == "$((s8_placement_version + 1))" ]] || { echo "scenario 8: after the confirm the placement version is $s8_version_after_confirm, want $((s8_placement_version + 1))" >&2; exit 1; }
  # Recovery re-points bytes; it never rewrites where the placement came
  # from. An evidence type of 'recovery' would strand the row: every lineage
  # reader joins on source_evidence_type='file_operation'.
  [[ "$s8_evidence_after_confirm" == file_operation ]] || { echo "scenario 8: the confirm rewrote source_evidence_type to [$s8_evidence_after_confirm], want file_operation" >&2; exit 1; }
  [[ "$s8_companion_path_after_confirm" == "$s8_companion_target" ]] || { echo "scenario 8: after the confirm the companion owns [$s8_companion_path_after_confirm], want $s8_companion_target" >&2; exit 1; }
  [[ "$s8_companion_version_after_confirm" == "$((s8_companion_version + 1))" ]] || { echo "scenario 8: after the confirm the companion version is $s8_companion_version_after_confirm, want $((s8_companion_version + 1))" >&2; exit 1; }
  [[ "$s8_journal_after_confirm" == "$((s8_journal_before + 1))" ]] || { echo "scenario 8: the confirm wrote $s8_journal_after_confirm file_recovered journal rows, want $((s8_journal_before + 1))" >&2; exit 1; }
  [[ "$(fingerprint "$s8_target")" == "$s8_target_hash" && "$(fingerprint "$s8_companion_target")" != MISSING ]] || { echo "scenario 8: the confirm moved or rewrote bytes (target $(fingerprint "$s8_target") vs $s8_target_hash)" >&2; exit 1; }
  observe_disk scenario-8-after-confirm "$s8_target"

  # -- step 4: the same token replays idempotently ------------------------
  s8_http_code="$(s8_post_recover "$s8_confirm_body" "$summary_dir/scenario-8-replay.json")"
  s8_replay_response="$(cat "$summary_dir/scenario-8-replay.json")"
  [[ "$s8_http_code" == 200 ]] || { echo "scenario 8: replaying the confirm answered HTTP $s8_http_code, want 200 (body: $s8_replay_response)" >&2; exit 1; }
  s8_replay_flag="$(printf '%s' "$s8_replay_response" | python3 -c 'import json,sys;d=json.load(sys.stdin);print("true" if d.get("replay") else "false")')"
  s8_version_after_replay="$(s8_count "$($PSQL -tAc "SELECT version FROM placements WHERE id='$s8_placement_id'")")"
  s8_companion_version_after_replay="$(s8_count "$($PSQL -tAc "SELECT version FROM placement_companions WHERE id='$s8_companion_id'")")"
  s8_journal_after_replay="$(s8_journal_count)"
  [[ "$s8_replay_flag" == true ]] || { echo "scenario 8: replaying the committed confirm reported replay=$s8_replay_flag, want true (body: $s8_replay_response)" >&2; exit 1; }
  [[ "$s8_version_after_replay" == "$s8_version_after_confirm" ]] || { echo "scenario 8: the replay bumped the placement version $s8_version_after_confirm -> $s8_version_after_replay" >&2; exit 1; }
  [[ "$s8_companion_version_after_replay" == "$s8_companion_version_after_confirm" ]] || { echo "scenario 8: the replay bumped the companion version $s8_companion_version_after_confirm -> $s8_companion_version_after_replay" >&2; exit 1; }
  [[ "$s8_journal_after_replay" == "$s8_journal_after_confirm" ]] || { echo "scenario 8: the replay wrote a second journal row ($s8_journal_after_confirm -> $s8_journal_after_replay)" >&2; exit 1; }

  # -- step 3: the next scan is aligned and offers nothing ----------------
  scan_and_calibrate scenario-8-aligned
  wait_sql "SELECT count(*) FROM work_items WHERE owner_type='file_maintenance' AND owner_id='anime-e2e' AND kind IN ('reconciliation_scan','reconciliation_calibrate') AND state IN ('available','leased')" 0
  s8_generation_after="$(calibration_generation)"
  s8_disposition_after="$($PSQL -tAc "SELECT COALESCE(disposition,'') FROM reconciliation_files WHERE managed_anime_id='anime-e2e' AND generation=$s8_generation_after AND file_path='$s8_target'" | tr -d '[:space:]')"
  [[ "$s8_disposition_after" == present ]] || { echo "scenario 8: after the recovery the scan of $s8_target is [$s8_disposition_after], want present (generation $s8_generation_after)" >&2; exit 1; }
  # Scoped to THIS placement: a candidate for any other placement (a
  # different, legitimately missing row) is not this scenario's business.
  s8_candidates_after="$($PSQL -tAc "SELECT count(*) FROM reconciliation_files WHERE managed_anime_id='anime-e2e' AND generation=$s8_generation_after AND facts->'recovery_candidates' @> jsonb_build_array(jsonb_build_object('placement_id','$s8_placement_id'))" | tr -d '[:space:]')"
  [[ "$s8_candidates_after" == 0 ]] || { echo "scenario 8: generation $s8_generation_after still carries $s8_candidates_after recovery candidate row(s) after the recovery" >&2; exit 1; }
  s8_diffs_after="$(api_get '/reconciliation/scans/anime-e2e/path-diffs?generation=0')"
  printf '%s' "$s8_diffs_after" >"$summary_dir/scenario-8-path-diffs-after.json"
  s8_differs_after="$(printf '%s' "$s8_diffs_after" | python3 -c '
import json,sys
path=sys.argv[1]
rows=[r for r in json.load(sys.stdin) if r.get("file_path")==path]
if len(rows)!=1:
    print("no_row(%d)" % len(rows)); raise SystemExit(0)
print("true" if rows[0].get("differs") else "false")
' "$s8_target")"
  [[ "$s8_differs_after" == false ]] || { echo "scenario 8: the path-diff of the recovered file $s8_target is [$s8_differs_after], want differs=false" >&2; exit 1; }
  api_get /anime/anime-e2e/files >"$summary_dir/scenario-8-files-after.json"
  s8_row_after="$(python3 - "$summary_dir/scenario-8-files-after.json" "$s8_placement_id" <<'PY'
import json,sys
view=json.load(open(sys.argv[1], encoding="utf-8"))
placement_id=sys.argv[2]
rows=[r for r in view.get("files") or [] if r.get("placement_id")==placement_id]
if len(rows)!=1:
    raise SystemExit("scenario 8: expected one files row for placement %s after the recovery, got %d" % (placement_id,len(rows)))
print(json.dumps(rows[0], ensure_ascii=False))
PY
)"
  s8_row_after_status="$(s8_field "$s8_row_after" status)"
  s8_row_after_recovery="$(s8_field "$s8_row_after" recovery)"
  s8_row_after_recover_available="$(s8_field "$s8_row_after" actions.recover.available)"
  [[ "$s8_row_after_status" == aligned ]] || { echo "scenario 8: after the recovery the files row of $s8_placement_id is [$s8_row_after_status], want aligned" >&2; exit 1; }
  [[ "$s8_row_after_recovery" == null ]] || { echo "scenario 8: after the recovery the files row still exposes a recovery candidate: $s8_row_after_recovery" >&2; exit 1; }
  [[ "$s8_row_after_recover_available" == false ]] || { echo "scenario 8: after the recovery the recover action is still available" >&2; exit 1; }

  observe_disk scenario-8-final "$s8_target"
  printf '{"scenario":8,"db_state":"recovered_after_explicit_confirm","placement_id":"%s","companion_id":"%s","scan_generation":%s,"recovery_generation":%s,"source":"%s","target":"%s","companion_target":"%s","sha256":"%s","candidate_fingerprint":"%s","placement_version_before":%s,"placement_version_after":%s,"companion_version_before":%s,"companion_version_after":%s,"source_evidence_type":"%s","refusal":{"http":%s,"code":"%s","reason":"%s"},"replay":%s,"rescan_aligned":true,"candidates_after_recovery":%s}\n' \
    "$s8_placement_id" "$s8_companion_id" "$s8_generation" "$s8_generation_after" "$s8_source_path" "$s8_target" "$s8_companion_target" "$s8_target_hash" \
    "$s8_candidate_fingerprint" "$s8_placement_version" "$s8_version_after_replay" "$s8_companion_version" "$s8_companion_version_after_replay" \
    "$s8_evidence_after_confirm" "$s8_refusal_http" "$s8_refusal_code" "$s8_refusal_reason" "$s8_replay_flag" "$s8_candidates_after" >>"$scenario_json"
  cat "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"

  # -- step 6: the recovered placement loses a replacement and is cleaned ---
  # Recovery rewrites the placement's target fingerprint to the typed metadata
  # envelope. Deletion of that placement verifies its path-level identity and
  # then the captured object AFTER a rename, which updates the inode's ctime;
  # on a mount without statx birth time (FUSE/mergerfs, Unraid shfs, some NFS
  # clients) an identity check that leaned on ctime would block here with the
  # file already captured. This step drives the real chain: a better
  # file_operation placement for the same episode, the generation-completed
  # replacement trigger, settle, then the replacement_delete of the recovered file
  # on the real library mount.
  echo "== scenario 8b: recovered placement as replacement loser =="
  s8b_target_dir="$(dirname "$s8_target")"
  # Record whether the target mount reports a birth time. GNU stat prints %W
  # as 0 (or '-') when statx has no btime; BSD stat reports %B.
  s8b_btime="$(stat -c '%W' "$s8_target" 2>/dev/null || stat -f '%B' "$s8_target" 2>/dev/null || echo unknown)"
  s8b_fstype="$(stat -f -c '%T' "$s8b_target_dir" 2>/dev/null || stat -f '%T' "$s8b_target_dir" 2>/dev/null || echo unknown)"
  s8b_mount="$(df -P "$s8b_target_dir" 2>/dev/null | awk 'NR==2{print $1" on "$6}' || echo unknown)"
  case "$s8b_btime" in 0|-|unknown|'') s8b_has_btime=false ;; *) s8b_has_btime=true ;; esac
  echo "scenario 8b target mount: fstype=$s8b_fstype mount=[$s8b_mount] btime=$s8b_btime has_btime=$s8b_has_btime"
  s8b_target_inode_before="$(ls -i "$s8_target" | awk '{print $1}')"
  # The companion cleanup checks the companion's registered digest against
  # the file before moving it; a seed whose digest is not the file's would
  # block that cleanup for a reason unrelated to recovery.
  s8b_companion_digest="$($PSQL -tAc "SELECT fingerprint FROM placement_companions WHERE id='$s8_companion_id'" | tr -d '[:space:]')"
  [[ "$s8b_companion_digest" == "$(fingerprint "$s8_companion_target")" ]] || { echo "scenario 8b: companion $s8_companion_id registered digest [$s8b_companion_digest] is not the sha256 of $s8_companion_target" >&2; exit 1; }
  s8b_target_fp="$($PSQL -tAc "SELECT target_fingerprint::text FROM placements WHERE id='$s8_placement_id'" | tr -d '\r')"
  [[ "$s8b_target_fp" == *'"algorithm": "metadata"'* || "$s8b_target_fp" == *'"algorithm":"metadata"'* ]] || { echo "scenario 8b: the recovered placement's target fingerprint is not the metadata envelope: $s8b_target_fp" >&2; exit 1; }
  # The scenario seeded the placement out of the replacement pool so the
  # earlier scans could not admit decisions holding its paths; return it to
  # the production default (eligible) now that the recovery committed.
  $PSQL -q -c "UPDATE placements SET replacement_eligible = true WHERE id='$s8_placement_id'" >/dev/null

  s8b_download="$download_root/EP08-2160p.mkv"
  head -c 65536 /dev/urandom >"$s8b_download"
  s8b_hex="$(shasum -a 256 "$s8b_download" | awk '{print $1}')"
  s8b_fp='{"algorithm":"sha256","value":"'$s8b_hex'","size_bytes":65536}'
  s8b_path="$library_root/Scenario 8 Better/EP08.mkv"
  mkdir -p "$(dirname "$s8b_path")"
  cp "$s8b_download" "$s8b_path"
  s8b_payload_digest="sha256:$(repeat_hex 5)"
  s8b_manifest_digest="sha256:$(repeat_hex 6)"
  s8b_file_ref="$(torrent_file_ref "$s8b_payload_digest" 'EP08.mkv')"
  s8b_fixture_json="$(go run ./internal/testutil/frozenpolicy/cmd \
    --payload-digest "$s8b_payload_digest" \
    --manifest-digest "$s8b_manifest_digest" \
    --route-root "$library_root" \
    --file EP08.mkv \
    --title '[示例字幕组] 进击的巨人 - 08 [2160p][chs]')"
  printf '%s' "$s8b_fixture_json" >"$summary_dir/scenario-8b-acquisition-fixture.json"
  s8b_acq_input_sql="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["acquisition_input"])' <<<"$s8b_fixture_json")"
  s8b_acq_input_sql="${s8b_acq_input_sql//\'/\'\'}"
  s8b_identity_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["identity"]))' <<<"$s8b_fixture_json")"
  s8b_release_facts_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["release_facts"]))' <<<"$s8b_fixture_json")"
  s8b_file_evidence_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["file_evidence"]))' <<<"$s8b_fixture_json")"
  s8b_effective_facts_sql="$(python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["effective_facts"]))' <<<"$s8b_fixture_json")"
  # The winner is a completed file_operation placement of a 2160p release for
  # the same episode (tv:e8): the anime's quality profile ranks it above the
  # recovered 1080p file, so the single_copy pool must supersede the recovered
  # placement. It is created last, so the generation trigger picks its release.
  if ! $PSQL -q -e >"$summary_dir/scenario-8b-seed.log" 2>&1 <<E2EEIGHTB
BEGIN;
INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,parsed_facts,internal_facts,verdict,reason_code,download_task_id,version)
SELECT 'rel-e2e-8b','src-e2e-8','guid:e2e-8b',jsonb_set(raw_facts,'{title}','"[示例字幕组] 进击的巨人 - 08 [2160p][chs]"'),source_facts,parsed_facts,internal_facts,'submitted','submitted','dl-e2e-8b',1 FROM releases WHERE id='rel-e2e';
INSERT INTO acquisitions(
  id, execution_branch, entry_kind, access_scope, request_namespace, request_key, request_hash,
  canonical_request, admission_kind, admission_http_status, admission_receipt,
  input_schema_version, admission_digest, origin_managed_anime_ref, source_release_ref,
  display_title, accepted_at, retention_anchor_at, detail_retention_seconds,
  receipt_after_accept_seconds, receipt_after_terminal_seconds, receipt_until, detail_until,
  state, terminal_at, terminal_schema, terminal_receipt, record_state,
  preparation_mode, vars_evidence_version,
  input_canonical_utf8, payload_digest, manifest_digest, manifest_item_count, manifest_sealed_at)
VALUES (
  'acq-e2e-8b', 'download', 'automatic', 'deployment_owner_v1', 'auto_acquire_v1', 'auto:rel-e2e-8b',
  'sha256:' || repeat('5', 64),
  '{"schema_version":1,"kind":"automatic","release_ref":"rel-e2e-8b"}'::jsonb,
  'download_queued', 202,
  '{"schema_version":1,"acquisition_id":"acq-e2e-8b","state":"queued"}'::jsonb,
  1,
  'sha256:' || encode(sha256(convert_to('acquisition-input:v1', 'UTF8') || decode('00', 'hex')
     || convert_to('$s8b_acq_input_sql', 'UTF8')), 'hex'),
  'anime-e2e', 'rel-e2e-8b',
  '{"state":"known","value":"进击的巨人 - 08","reason_code":null}'::jsonb,
  now() - interval '1 hour', now() - interval '1 hour', 2592000, 2592000, 5184000,
  now() + interval '60 days',
  NULL,
  'running', NULL, NULL, NULL,
  'full', 'none', 1,
  '$s8b_acq_input_sql',
  '$s8b_payload_digest',
  '$s8b_manifest_digest', 1, now() - interval '1 hour');
INSERT INTO acquisition_files(acquisition_id, file_ref, ordinal, relative_path, exact_size, media_kind, identity_evidence, requirement, requirement_reason_code, selection_evidence)
VALUES ('acq-e2e-8b', '$s8b_file_ref', 0, 'EP08.mkv', 65536, 'video', '{}'::jsonb, 'required', 'e2e_seed_required', '{"source":"filemaintenance-e2e scenario 8b"}'::jsonb);
INSERT INTO download_tasks(id,release_id,acquisition_id,frozen_input,frozen_policy,state,idempotency_key)
VALUES ('dl-e2e-8b','rel-e2e-8b','acq-e2e-8b','{}','$frozen_policy_sql'::jsonb,'queued','download:e2e:8b');
UPDATE download_tasks SET state='completed' WHERE id='dl-e2e-8b';
INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,verified_at)
VALUES ('art-e2e-8b','dl-e2e-8b','acq-e2e-8b','{}','{}','verified',CURRENT_TIMESTAMP);
INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state)
VALUES ('af-e2e-8b','art-e2e-8b','acq-e2e-8b','$s8b_file_ref','EP08.mkv','$s8b_download','video',65536,'$s8b_fp'::jsonb,'{"resolution":"2160p","video_codec":"HEVC","container":"mkv"}','not_applicable');
INSERT INTO postprocessing_plans(id,artifact_id,artifact_version,state,input_digest,acquisition_id)
VALUES ('plan-e2e-8b','art-e2e-8b',1,'queued','sha256:' || repeat('5', 64),'acq-e2e-8b');
INSERT INTO file_operations(id,artifact_id,acquisition_id,artifact_version,artifact_file_id,file_ref,operation,source_path,target_path,source_fingerprint,target_fingerprint,collision_policy,frozen_render_snapshot,frozen_config_snapshot,state,observed_at,idempotency_key,correlation_id,causation_type,causation_id,postprocessing_plan_id,file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds)
SELECT 'op-e2e-8b','art-e2e-8b','acq-e2e-8b',1,'af-e2e-8b','$s8b_file_ref','move','$s8b_download','$s8b_path','$s8b_fp'::jsonb,'$s8b_fp'::jsonb,'block','{}',frozen_config_snapshot,'completed',CURRENT_TIMESTAMP,'op-e2e-8b','corr-e2e-8b','artifact','art-e2e-8b','plan-e2e-8b',file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds FROM file_operations WHERE id='op-e2e-8';
INSERT INTO release_candidate_facts(release_id,release_version,parser_version,input_fingerprint,raw_evidence,facts,coverage_keys,resolution,revision,source,language,"group",size_bytes)
VALUES ('rel-e2e-8b',1,'parser-v1','fp-e2e-8b','{"title":"[示例字幕组] 进击的巨人 - 08 [2160p][chs]"}','{"episode":8,"group_name":"示例字幕组","group":"示例字幕组","resolution":"2160p","quality":"web-dl","revision":"v1","source":"web-dl","language":"chs","size_bytes":65536}',
        '["tv:e8"]','2160p','v1','web-dl','chs','示例字幕组',65536);
INSERT INTO media_fact_snapshots(id,release_id,schema_version,resolver_version,parser_kind,input_digest,release_identity,release_facts)
VALUES ('snap-e2e-8b','rel-e2e-8b','media-facts-v1','episode-identity-v1','regex','digest-e2e-8b','$s8b_identity_sql'::jsonb,'$s8b_release_facts_sql'::jsonb);
INSERT INTO media_fact_files(snapshot_id,file_ref,media_kind,size_bytes,source_fingerprint,file_evidence,effective_facts)
VALUES ('snap-e2e-8b','$s8b_file_ref','video',65536,'$s8b_hex','$s8b_file_evidence_sql'::jsonb,'$s8b_effective_facts_sql'::jsonb);
INSERT INTO placements(id,physical_path,managed_anime_id,source_evidence_type,source_owner_ref,source_fingerprint,target_fingerprint,quality_facts_ref,facts_version,correlation_id,causation_type,causation_id,replacement_eligible,verification_state,state,version)
VALUES ('plc-e2e-8b','$s8b_path','anime-e2e','file_operation','op-e2e-8b','$s8b_fp'::jsonb,'$s8b_fp'::jsonb,'rel-e2e-8b','media-facts-v1','corr-e2e-8b','file_operation','op-e2e-8b',true,'verified','active',1);
INSERT INTO placement_coverage(placement_id,observation_key,coverage_ordinal,facts_version,evidence_ref)
VALUES ('plc-e2e-8b','tv:e8',0,'media-facts-v1','op-e2e-8b');
COMMIT;
E2EEIGHTB
  then
    echo "scenario 8b seed failed; see $summary_dir/scenario-8b-seed.log" >&2
    sed -n '1,40p' "$summary_dir/scenario-8b-seed.log" >&2 || true
    exit 1
  fi
  rm -f -- "$s8b_download"

  # The completed generation's replacement consumer evaluates the newest
  # file_operation release (rel-e2e-8b); settle supersedes the recovered
  # placement and registers its deletion in the same transaction.
  scan_and_calibrate scenario-8b-replacement
  # Every cleanup row of the loser (the video and its bound companion) must
  # reach deleted; any blocked/failed/skipped row is the failure this step
  # guards.
  s8b_cleanup_state=''
  s8b_cleanup_where="managed_anime_id='anime-e2e' AND placement_id='$s8_placement_id'"
  s8b_deadline=$(( $(date +%s) + 300 ))
  while (( $(date +%s) < s8b_deadline )); do
    s8b_bad="$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE $s8b_cleanup_where AND state IN ('blocked','failed','skipped')" | tr -d '[:space:]')"
    if [[ "$s8b_bad" != 0 ]]; then
      {
        echo "scenario 8b: cleanup of the recovered placement ended blocked/failed/skipped (target mount btime=$s8b_btime fstype=$s8b_fstype)"
        $PSQL -c "SELECT id, companion_id, state, failure_code, reason, source_path, source_fingerprint, attempt->>'capture_path' AS capture_path FROM replacement_deletions WHERE $s8b_cleanup_where"
        $PSQL -c "SELECT id, kind, state, last_error_code, last_error_reason FROM e2e_aux.work_items_all WHERE owner_type='replacement_deletion' AND owner_id IN (SELECT id FROM replacement_deletions WHERE $s8b_cleanup_where) ORDER BY created_at"
      } | tee -a "$durable_evidence_dir/scenario-8b-diagnostics.txt" >&2
      exit 1
    fi
    s8b_cleanup_state="$($PSQL -tAc "SELECT state FROM replacement_deletions WHERE $s8b_cleanup_where AND companion_id IS NULL ORDER BY created_at DESC LIMIT 1" | tr -d '[:space:]')"
    s8b_undeleted="$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE $s8b_cleanup_where AND state <> 'deleted'" | tr -d '[:space:]')"
    [[ "$s8b_cleanup_state" == deleted && "$s8b_undeleted" == 0 ]] && break
    sleep 2
  done
  if [[ "$s8b_cleanup_state" != deleted || "$s8b_undeleted" != 0 ]]; then
    {
      echo "scenario 8b: the recovered placement's cleanup did not reach deleted within 300s (video=[$s8b_cleanup_state], undeleted rows=$s8b_undeleted)"
      $PSQL -c "SELECT id, companion_id, state, failure_code, reason, source_path FROM replacement_deletions WHERE $s8b_cleanup_where"
      $PSQL -c "SELECT id, state, verdict FROM replacement_decisions WHERE managed_anime_id='anime-e2e' ORDER BY created_at DESC LIMIT 5"
      $PSQL -c "SELECT id, state, replacement_eligible, physical_path FROM placements WHERE id IN ('$s8_placement_id','plc-e2e-8b')"
      $PSQL -c "SELECT id, kind, state, last_error_code, last_error_reason FROM e2e_aux.work_items_all WHERE owner_id='anime-e2e' OR owner_type IN ('replacement_deletion','replacement_decision') ORDER BY created_at DESC LIMIT 20"
    } | tee -a "$durable_evidence_dir/scenario-8b-diagnostics.txt" >&2
    exit 1
  fi
  s8b_loser_state="$($PSQL -tAc "SELECT state FROM placements WHERE id='$s8_placement_id'" | tr -d '[:space:]')"
  s8b_winner_state="$($PSQL -tAc "SELECT state FROM placements WHERE id='plc-e2e-8b'" | tr -d '[:space:]')"
  s8b_capture="$($PSQL -tAc "SELECT COALESCE(attempt->>'capture_path','') FROM replacement_deletions WHERE $s8b_cleanup_where AND companion_id IS NULL ORDER BY created_at DESC LIMIT 1" | tr -d '\r')"
  s8b_cleanup_fp="$($PSQL -tAc "SELECT source_fingerprint::text FROM replacement_deletions WHERE $s8b_cleanup_where AND companion_id IS NULL ORDER BY created_at DESC LIMIT 1" | tr -d '\r')"
  s8b_cleanup_rows="$($PSQL -tAc "SELECT count(*) FROM replacement_deletions WHERE $s8b_cleanup_where" | tr -d '[:space:]')"
  [[ "$s8b_loser_state" == superseded ]] || { echo "scenario 8b: the recovered placement is [$s8b_loser_state], want superseded" >&2; exit 1; }
  [[ "$s8b_winner_state" == active && "$(fingerprint "$s8b_path")" == "$s8b_hex" ]] || { echo "scenario 8b: the winner placement is [$s8b_winner_state] or its file changed" >&2; exit 1; }
  [[ ! -e "$s8_target" ]] || { echo "scenario 8b: the recovered file is still at $s8_target after deletion" >&2; exit 1; }
  [[ ! -e "$s8_companion_target" ]] || { echo "scenario 8b: the recovered companion is still at $s8_companion_target after deletion" >&2; exit 1; }
  [[ -n "$s8b_capture" && ! -e "$s8b_capture" ]] || { echo "scenario 8b: captured object not purged: [$s8b_capture]" >&2; exit 1; }
  observe_disk scenario-8b-winner "$s8b_path"
  # The cleanup's frozen source fingerprint is recorded so the evidence shows
  # the metadata-envelope path (not a SHA-256 read) was the one exercised.
  python3 - "$s8_placement_id" "$s8b_loser_state" "$s8b_cleanup_state" "$s8b_cleanup_rows" "$s8b_capture" "$s8_target" \
    "$s8b_target_inode_before" "$s8b_cleanup_fp" "$s8b_fstype" "$s8b_mount" "$s8b_btime" "$s8b_has_btime" >>"$scenario_json" <<'PY'
import json,sys
(loser, loser_state, cleanup_state, cleanup_rows, capture, target, inode,
 cleanup_fp, fstype, mount, btime, has_btime) = sys.argv[1:13]
print(json.dumps({
    "scenario": "8b",
    "db_state": "recovered_loser_deleted",
    "loser": loser,
    "loser_state": loser_state,
    "winner": "plc-e2e-8b",
    "cleanup_state": cleanup_state,
    "cleanup_rows_deleted": int(cleanup_rows),
    "cleanup_source_fingerprint": cleanup_fp,
    "capture_path": capture,
    "target": target,
    "target_inode": inode,
    "target_mount": {"fstype": fstype, "mount": mount, "statx_btime": btime, "has_btime": has_btime == "true"},
}, ensure_ascii=False))
PY
  tail -n 1 "$scenario_json" | tee -a "$durable_evidence_dir/file022-scenarios.jsonl"
  cat "$summary_dir/disk-observations.txt" | tee -a "$durable_evidence_dir/disk-observations.txt"
  fi

fi

echo "filemaintenance E2E PASSED"
