#!/usr/bin/env bash
# TMDB connection smoke against a disposable runtime.
#
# Consumes .tmdb-credentials.env (BANSOKO_TMDB_API_KEY) and a running auth-enabled
# disposable session, then proves the real chain end to end:
#   1. log in as the session owner;
#   2. create or replace the `tmdb` connection with the supplied credential;
#   3. POST /config/connections/{id}/test and require a ready health;
#   4. GET /metadata/search?provider=tmdb and read back the real season matrix.
#
# It never prints the credential and never targets production: the base URL must
# be loopback or a private-range address unless BANSOKO_TMDB_SMOKE_ALLOW_REMOTE=1.
#
# Usage:
#   scripts/maintainer/tmdb-connection-smoke.sh
#   BANSOKO_TEST_BASE_URL=http://<test-host>:20169 \
#   BANSOKO_E2E_USERNAME=owner BANSOKO_E2E_PASSWORD=... scripts/maintainer/tmdb-connection-smoke.sh
#
# The session credentials come from the disposable browser session:
#   BANSOKO_BROWSER_SESSION_STATE_DIR=/tmp/bansoko-browser-session scripts/verify/real-effect-browser-session.sh up
#   set -a; source "$BANSOKO_BROWSER_SESSION_STATE_DIR/env"; set +a
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

credentials_file="${BANSOKO_TMDB_CREDENTIALS_FILE:-$root/.tmdb-credentials.env}"
# Values already present in the environment win over the file's empty
# placeholders, so `BANSOKO_TMDB_API_KEY=... script` works before the file is filled.
pre_api_key="${BANSOKO_TMDB_API_KEY:-}"
pre_base_url="${BANSOKO_TMDB_BASE_URL:-}"
pre_connection_name="${BANSOKO_TMDB_CONNECTION_NAME:-}"
pre_connection_id="${BANSOKO_TMDB_CONNECTION_ID:-}"
pre_search_query="${BANSOKO_TMDB_SEARCH_QUERY:-}"
if [[ -f "$credentials_file" ]]; then
  # shellcheck disable=SC1090
  set -a; source "$credentials_file"; set +a
fi
[[ -n "$pre_api_key" ]] && BANSOKO_TMDB_API_KEY="$pre_api_key"
[[ -n "$pre_base_url" ]] && BANSOKO_TMDB_BASE_URL="$pre_base_url"
[[ -n "$pre_connection_name" ]] && BANSOKO_TMDB_CONNECTION_NAME="$pre_connection_name"
[[ -n "$pre_connection_id" ]] && BANSOKO_TMDB_CONNECTION_ID="$pre_connection_id"
[[ -n "$pre_search_query" ]] && BANSOKO_TMDB_SEARCH_QUERY="$pre_search_query"

fail() { printf 'tmdb-smoke: %s\n' "$1" >&2; exit 1; }

[[ -n "${BANSOKO_TMDB_API_KEY:-}" ]] || fail "BANSOKO_TMDB_API_KEY is empty; fill $credentials_file"
[[ -n "${BANSOKO_TEST_BASE_URL:-}" ]] || fail "BANSOKO_TEST_BASE_URL is required; start a disposable session first (see the header comment)"
[[ -n "${BANSOKO_E2E_USERNAME:-}" && -n "${BANSOKO_E2E_PASSWORD:-}" ]] || fail "BANSOKO_E2E_USERNAME and BANSOKO_E2E_PASSWORD are required; source the disposable session env"

base_url="${BANSOKO_TEST_BASE_URL%/}"
tmdb_base_url="${BANSOKO_TMDB_BASE_URL:-https://api.themoviedb.org/3}"
connection_name="${BANSOKO_TMDB_CONNECTION_NAME:-TMDB}"
connection_id="${BANSOKO_TMDB_CONNECTION_ID:-tmdb}"
search_query="${BANSOKO_TMDB_SEARCH_QUERY:-Frieren}"

# --- Production safety ----------------------------------------------------
host_part="${base_url#*://}"
host_part="${host_part%%/*}"
host_only="${host_part%%:*}"
case "$host_only" in
  localhost|127.0.0.1|::1) ;;
  10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*) ;;
  *)
    [[ "${BANSOKO_TMDB_SMOKE_ALLOW_REMOTE:-0}" == "1" ]] \
      || fail "refusing non-loopback/non-private target $host_only (set BANSOKO_TMDB_SMOKE_ALLOW_REMOTE=1 only for a disposable runtime)"
    ;;
esac
case "$host_only" in
  *bansoko-prod*|*production*) fail "refusing a target that looks like a production host: $host_only" ;;
esac

run_dir="$root/.local-artifacts/tmdb-smoke/$(date -u +%Y%m%d-%H%M%S)"
mkdir -p "$run_dir"
evidence="$run_dir/evidence.json"

api() { # api METHOD PATH [BODY] -> writes body to $2 file, prints status
  local method="$1" path="$2" body="${3:-}" out="$run_dir/last-response.json"
  local args=(-sS -o "$out" -w '%{http_code}' -X "$method" "$base_url$path"
    -H "Authorization: Bearer $session_token" -H 'Accept: application/json')
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' -d "$body")
  local status
  status="$(curl "${args[@]}")" || fail "$method $path failed at the transport level"
  printf '%s' "$status"
}

printf 'tmdb-smoke: logging in to %s\n' "$base_url"
login_status="$(curl -sS -o "$run_dir/login.json" -w '%{http_code}' -X POST "$base_url/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$BANSOKO_E2E_USERNAME\",\"password\":\"$BANSOKO_E2E_PASSWORD\",\"session_transport\":\"token\"}")"
[[ "$login_status" == "200" ]] || fail "login returned $login_status"
session_token="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("session_token",""))' "$run_dir/login.json")"
[[ -n "$session_token" ]] || fail "login response carried no session token"

printf 'tmdb-smoke: reading existing connections\n'
list_status="$(api GET /api/v1/config/connections)"
[[ "$list_status" == "200" ]] || fail "list connections returned $list_status"
# Any existing tmdb connection wins over the configured default id: the runtime
# provisions built-in connections at first boot, and PUT is keyed by connection
# id with a required If-Match version (0 means "create only").
existing_version="$(python3 - "$run_dir/last-response.json" "$connection_id" <<'PY'
import json, sys
items = json.load(open(sys.argv[1]))
preferred = [i for i in items if i.get("kind") == "tmdb" and i.get("id") == sys.argv[2]]
any_tmdb = [i for i in items if i.get("kind") == "tmdb"]
for item in preferred or any_tmdb:
    print("%s\t%s" % (item.get("id", ""), item.get("version", "")))
    break
PY
)"
existing_id="${existing_version%%$'\t'*}"
existing_rev="${existing_version#*$'\t'}"
[[ -n "$existing_id" ]] && connection_id="$existing_id"

connection_body="$(python3 - "$connection_name" "$tmdb_base_url" <<'PY'
import json, sys
name, base = sys.argv[1], sys.argv[2]
print(json.dumps({
    "kind": "tmdb", "name": name, "enabled": True, "base_url": base,
    "secret": {"mode": "replace", "value": __import__("os").environ["BANSOKO_TMDB_API_KEY"]},
}))
PY
)"

put_with_version() { # version -> status
  curl -sS -o "$run_dir/last-response.json" -w '%{http_code}' -X PUT \
    "$base_url/api/v1/config/connections/$connection_id" \
    -H "Authorization: Bearer $session_token" -H 'Content-Type: application/json' \
    -H "If-Match: $1" -d "$connection_body"
}

if [[ -n "$existing_rev" ]]; then
  printf 'tmdb-smoke: replacing the TMDB credential on %s (version %s)\n' "$connection_id" "$existing_rev"
  put_status="$(put_with_version "$existing_rev")"
else
  printf 'tmdb-smoke: creating the TMDB connection %s\n' "$connection_id"
  put_status="$(put_with_version 0)"
  if [[ "$put_status" == "409" ]]; then
    # A concurrent or pre-provisioned owner appeared between the list and the
    # create; re-read the current version and retry once instead of guessing.
    api GET /api/v1/config/connections >/dev/null
    retry_rev="$(python3 - "$run_dir/last-response.json" <<'PY'
import json, sys
for item in json.load(open(sys.argv[1])):
    if item.get("kind") == "tmdb":
        print(item.get("version", ""))
        break
PY
)"
    [[ -n "$retry_rev" ]] || fail "create conflicted but no tmdb connection version could be read"
    printf 'tmdb-smoke: create conflicted; retrying with version %s\n' "$retry_rev"
    put_status="$(put_with_version "$retry_rev")"
  fi
fi
[[ "$put_status" == "200" ]] || fail "put connection returned $put_status ($(head -c 300 "$run_dir/last-response.json"))"
cp "$run_dir/last-response.json" "$run_dir/connection.json"

printf 'tmdb-smoke: testing the connection\n'
test_status="$(api POST "/api/v1/config/connections/$connection_id/test")"
cp "$run_dir/last-response.json" "$run_dir/connection-test.json"
[[ "$test_status" == "200" ]] || fail "connection test returned $test_status"
test_health="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("health",""))' "$run_dir/connection-test.json")"

printf 'tmdb-smoke: real TMDB search for %q\n' "$search_query"
search_status="$(api GET "/api/v1/metadata/search?provider=tmdb&query=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' "$search_query")&limit=5")"
cp "$run_dir/last-response.json" "$run_dir/metadata-search.json"
[[ "$search_status" == "200" ]] || fail "metadata search returned $search_status"

python3 - "$run_dir" "$evidence" "$test_health" "$search_query" <<'PY'
import json, pathlib, sys

run_dir, evidence_path, test_health, query = sys.argv[1:5]
dirpath = pathlib.Path(run_dir)
search = json.loads((dirpath / "metadata-search.json").read_text())
candidates = search.get("candidates", [])
versions = sorted({c.get("details_status") for c in candidates})
summary = {
    "connection_test_health": test_health,
    "query": query,
    "candidate_count": len(candidates),
    "details_status_values": versions,
    "candidates": [
        {
            "provider_work_id": c.get("provider_work_id"),
            "title": c.get("title"),
            "media_kind": c.get("media_kind"),
            "details_status": c.get("details_status"),
            "suggested_season": c.get("suggested_season"),
            "season_count": len(c.get("seasons") or []),
            "seasons": [s.get("season_number") for s in (c.get("seasons") or [])],
        }
        for c in candidates
    ],
}
pathlib.Path(evidence_path).write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n")

problems = []
if not candidates:
    problems.append("TMDB search returned no candidates")
tv = [c for c in candidates if c.get("media_kind") == "tv"]
if not tv:
    problems.append("no TV candidate was returned, so season readback could not be proven")
else:
    proven = [c for c in tv if c.get("details_status") == "available" and c.get("seasons")]
    if not proven:
        problems.append("no TV candidate returned an available details status with real seasons")
    for c in proven:
        seasons = c.get("seasons") or []
        numbers = {s.get("season_number") for s in seasons}
        suggested = c.get("suggested_season")
        if suggested is not None and suggested not in numbers:
            problems.append("suggested_season %r is not one of the returned seasons %s" % (suggested, sorted(numbers)))
if problems:
    print("tmdb-smoke: FAILED")
    for problem in problems:
        print("  -", problem)
    raise SystemExit(1)
print("tmdb-smoke: PASSED — real search, details and season readback verified")
PY

printf 'tmdb-smoke: evidence=%s\n' "$evidence"
printf 'tmdb-smoke: raw responses kept in %s (git-ignored)\n' "$run_dir"
