#!/usr/bin/env bash
# Read-only VM assertion for the authenticated local-cleanup browser session.
#
# Observes the disposable session after frontend/e2e/local-data-cleanup-live.spec.ts:
# the cleanup receipt, the removed local owner graph, the detached-and-preserved
# custom candidate, and the untouched external effects (qB torrent, downloaded
# source bytes, placed media-library file hash). It never sources credentials
# into the environment of a mutating command, runs no migrations, and performs
# no writes.
#
# Usage:
#   scripts/verify/real-effect-browser-local-cleanup-assert.sh --state-dir /tmp/bansoko-verify-local-clean-ui-94
set -euo pipefail
umask 077

state_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --state-dir)
      [[ $# -ge 2 ]] || { printf 'FAIL argument=missing_state_dir\n' >&2; exit 2; }
      state_dir="$2"
      shift 2
      ;;
    -h|--help)
      printf 'usage: %s --state-dir DIR\n' "$0"
      exit 0
      ;;
    *)
      printf 'FAIL argument=unexpected_argument\n' >&2
      exit 2
      ;;
  esac
done

[[ -n "$state_dir" ]] || { printf 'FAIL argument=state_dir_required\n' >&2; exit 2; }
state_dir="$(cd "$state_dir" 2>/dev/null && pwd -P)" || {
  printf 'FAIL input=state_dir_unreadable\n' >&2
  exit 1
}

exec python3 - "$state_dir" <<'PY'
from __future__ import annotations

import hashlib
import http.cookiejar
import json
import os
import re
import subprocess
import sys
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

STATE_DIR = Path(sys.argv[1]).resolve()


class AssertionFailure(Exception):
    def __init__(self, code: str, detail: str = "") -> None:
        self.code = code
        self.detail = detail
        super().__init__(code)


def fail(code: str, detail: str = "") -> None:
    raise AssertionFailure(code, detail)


def decode_shell_word(value: str) -> str:
    if value.startswith("$'") or value.startswith('$"'):
        fail("env_value_quoting_unsupported")
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
        inner = value[1:-1]
        if value[0] == "'":
            return inner.replace("'\\''", "'")
        return re.sub(r"\\([$`\"\\])", r"\1", inner)
    return re.sub(r"\\(.)", r"\1", value)


def parse_env(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError:
        fail("env_unreadable")
    for line in lines:
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        if "=" not in line:
            fail("env_line_invalid")
        key, value = line.split("=", 1)
        values[key.strip()] = decode_shell_word(value)
    return values


def read_json(path: Path, label: str) -> dict[str, Any]:
    try:
        with path.open(encoding="utf-8") as stream:
            value = json.load(stream)
    except (OSError, ValueError):
        fail(f"{label}_invalid")
    if not isinstance(value, dict):
        fail(f"{label}_must_be_object")
    return value


def scalar(value: Any) -> str | None:
    return value.strip() if isinstance(value, str) and value.strip() else None


def get_path(value: Any, *path: str) -> Any:
    for key in path:
        if not isinstance(value, dict):
            return None
        value = value.get(key)
    return value


def first_string(value: dict[str, Any], keys: list[str]) -> str | None:
    for key in keys:
        found = scalar(value.get(key))
        if found is not None:
            return found
    return None


def psql_rows(db_url: str, db_name: str, pg_id: str, sql: str) -> list[str]:
    args = ["-X", "-qAt", "-v", "ON_ERROR_STOP=1"]
    env = {**os.environ, "PGCONNECT_TIMEOUT": "5"}
    if which("psql"):
        parsed = urllib.parse.urlsplit(db_url)
        command = ["psql", *args, "-c", sql]
        env.update(
            {
                "PGHOST": parsed.hostname or "",
                "PGPORT": str(parsed.port or "5432"),
                "PGUSER": urllib.parse.unquote(parsed.username or ""),
                "PGPASSWORD": urllib.parse.unquote(parsed.password or ""),
                "PGDATABASE": urllib.parse.unquote(parsed.path.lstrip("/")),
            }
        )
    elif which("docker"):
        command = ["docker", "exec", pg_id, "psql", "-U", "harness", "-d", db_name, *args, "-c", sql]
    else:
        fail("psql_unavailable")
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=20, check=False, env=env)
    except (OSError, subprocess.TimeoutExpired):
        fail("database_query_failed")
    if result.returncode != 0:
        fail("database_query_failed")
    return [line.strip() for line in result.stdout.splitlines() if line.strip()]


def which(command: str) -> str | None:
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        candidate = Path(directory) / command
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate)
    return None


def count(db_url: str, db_name: str, pg_id: str, sql: str) -> int:
    rows = psql_rows(db_url, db_name, pg_id, sql)
    if len(rows) != 1:
        fail("database_result_invalid")
    try:
        return int(rows[0])
    except ValueError:
        fail("database_count_invalid")


def qb_torrents(endpoint: str, username: str, password: str) -> list[dict[str, Any]]:
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    login = urllib.parse.urlencode({"username": username, "password": password}).encode()
    try:
        opener.open(urllib.request.Request(f"{endpoint}/api/v2/auth/login", data=login), timeout=10).read()
        response = opener.open(f"{endpoint}/api/v2/torrents/info", timeout=10)
        payload = json.loads(response.read().decode("utf-8"))
    except Exception:
        fail("qb_query_failed")
    if not isinstance(payload, list):
        fail("qb_torrents_invalid")
    return payload


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def safe_temp_root(path: str, label: str) -> Path:
    # Disposable scratch only: /tmp, or the session-owned tree under
    # /opt/bansoko/sessions (test-environment §6.3.1 tells browser sessions to
    # put BANSOKO_HARNESS_MEDIA_PARENT there so publication runs on a chosen
    # filesystem). Anything else fails closed, so a user media path can
    # never pass.
    candidate = Path(path).expanduser().resolve()
    allowed = (
        Path("/tmp").resolve(),
        Path("/private/tmp").resolve(),
        Path("/opt/bansoko/sessions").resolve(),
    )
    if not candidate.is_absolute() or not any(candidate == prefix or prefix in candidate.parents for prefix in allowed):
        fail(f"{label}_not_disposable")
    return candidate


def main() -> None:
    session = read_json(STATE_DIR / "browser-session.json", "browser_session")
    env = parse_env(STATE_DIR / "env")
    evidence = read_json(STATE_DIR / "evidence" / "local-cleanup-evidence.json", "local_cleanup_evidence")

    if evidence.get("cleanup_status") != 204:
        fail("cleanup_status_not_204")
    if evidence.get("cleanup_confirmation") != "delete-local-data":
        fail("cleanup_confirmation_mismatch")
    if evidence.get("cancel_cleanup_requests") != 0:
        fail("cancel_issued_cleanup_request")

    managed_anime_id = scalar(evidence.get("managed_anime_id"))
    lifecycle_epoch = evidence.get("lifecycle_epoch")
    candidate_id = scalar(evidence.get("candidate_id"))
    if not managed_anime_id or not candidate_id or not isinstance(lifecycle_epoch, int):
        fail("evidence_identity_missing")
    if evidence.get("cleanup_expected_lifecycle_epoch") != lifecycle_epoch:
        fail("cleanup_epoch_not_exact")

    viewport = evidence.get("viewport")
    dialog_box = evidence.get("dialog_box")
    # The Base UI migration replaced the antd mask element with a hit test at the
    # viewport corner, so the spec now reports overlay_hit (position/size of the
    # element hit-tested at 2,2); older evidence carried the mask element's box.
    mask_box = evidence.get("mask_box")
    overlay_hit = evidence.get("overlay_hit")
    overlay = mask_box if isinstance(mask_box, dict) else overlay_hit
    if not isinstance(viewport, dict) or not isinstance(dialog_box, dict) or not isinstance(overlay, dict):
        fail("dialog_geometry_missing")
    vw, vh = float(viewport.get("width") or 0), float(viewport.get("height") or 0)
    dw, dh = float(dialog_box.get("width") or 0), float(dialog_box.get("height") or 0)
    dx, dy = float(dialog_box.get("x") or 0), float(dialog_box.get("y") or 0)
    if vw <= 0 or vh <= 0 or dw <= 300 or dh <= 140 or dw > vw or dh > vh:
        fail("dialog_box_unreasonable")
    if abs(dx + dw / 2 - vw / 2) >= 80 or dy < 0 or dy + dh > vh + 1:
        fail("dialog_off_center")
    if isinstance(mask_box, dict):
        if float(overlay.get("width") or 0) < vw - 2 or float(overlay.get("height") or 0) < vh - 2:
            fail("dialog_overlay_not_viewport")
    else:
        # Hit-test spelling: the corner element must itself be the fixed,
        # full-viewport backdrop, so the card underneath cannot be mis-clicked.
        if str(overlay.get("position")) != "fixed":
            fail("dialog_overlay_not_fixed")
        if float(overlay.get("width") or 0) < vw - 2 or float(overlay.get("height") or 0) < vh - 2:
            fail("dialog_overlay_not_viewport")

    pg_id = str(session["pg_id"])
    download_root = safe_temp_root(str(session["download_root"]), "download_root")
    library_root = safe_temp_root(str(session["library_root"]), "library_root")
    db_url = env.get("BANSOKO_TEST_DATABASE_URL", "").strip()
    if not db_url:
        fail("disposable_database_url_missing")
    parsed = urllib.parse.urlsplit(db_url)
    db_name = urllib.parse.unquote(parsed.path.lstrip("/"))
    if parsed.hostname not in ("127.0.0.1", "localhost"):
        fail("disposable_database_host_invalid")

    # Cleanup receipt present and bound to the exact retired epoch.
    receipt_epoch = count(
        db_url, db_name, pg_id,
        f"SELECT (result_snapshot->>'lifecycle_epoch')::int FROM idempotency_records WHERE kind = 'local_cleanup' AND owner_type = 'managed_anime' AND owner_id = '{managed_anime_id}'",
    )
    if receipt_epoch != lifecycle_epoch:
        fail("receipt_epoch_mismatch")
    if receipt_epoch == 0:
        fail("receipt_missing")

    # Owner graph removed.
    if count(db_url, db_name, pg_id, f"SELECT COUNT(*) FROM managed_anime WHERE id = '{managed_anime_id}'") != 0:
        fail("managed_anime_survived")
    if count(db_url, db_name, pg_id, f"SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id = '{managed_anime_id}'") != 0:
        fail("subscription_survived")
    for table in ["releases", "download_tasks", "artifacts", "file_operations", "placements", "work_items"]:
        if count(db_url, db_name, pg_id, f"SELECT COUNT(*) FROM {table}") != 0:
            fail(f"{table}_survived")

    # Candidate detached (not deleted) with its immutable revision preserved.
    candidate_rows = psql_rows(
        db_url, db_name, pg_id,
        f"SELECT status, COALESCE(activated_managed_anime_id, '') FROM anime_config_candidates WHERE id = '{candidate_id}'",
    )
    if len(candidate_rows) != 1:
        fail("candidate_deleted")
    status, activated = (candidate_rows[0].split("|") + [""])[:2]
    if status != "archived" or activated != "":
        fail("candidate_not_detached")
    revision_count = count(db_url, db_name, pg_id, f"SELECT COUNT(*) FROM anime_config_candidate_revisions WHERE candidate_id = '{candidate_id}'")
    if revision_count < 1:
        fail("candidate_revisions_deleted")

    # External effects unchanged: fixture artifact bytes are deterministic.
    fixture_path = Path(str(session.get("fixture_metadata_file") or STATE_DIR / "fixture.json"))
    fixture = read_json(fixture_path, "fixture_metadata")
    expected_sha = (first_string(fixture, ["artifact_sha256"]) or "").lower()
    artifact_name = first_string(fixture, ["artifact_name"]) or ""
    if not re.fullmatch(r"[0-9a-f]{64}", expected_sha) or not artifact_name:
        fail("fixture_identity_missing")

    qb = qb_torrents(env.get("BANSOKO_QB_ENDPOINT", ""), env.get("BANSOKO_QB_USERNAME", "admin"), env.get("BANSOKO_QB_PASSWORD", ""))
    if len(qb) != 1:
        fail("qb_torrent_count_changed")
    if scalar(qb[0].get("name")) != artifact_name or not scalar(qb[0].get("hash")):
        fail("qb_torrent_identity_changed")

    source_candidates = [path for path in download_root.rglob("*") if path.is_file() and path.name == artifact_name]
    if not source_candidates:
        fail("source_download_missing")
    if sha256_file(source_candidates[0]) != expected_sha:
        fail("source_download_hash_changed")

    library_hashes = {sha256_file(path) for path in library_root.rglob("*") if path.is_file()}
    if expected_sha not in library_hashes:
        fail("media_library_hash_missing_or_changed")

    print(
        json.dumps(
            {
                "result": "PASS",
                "receipt_epoch": receipt_epoch,
                "candidate_status": status,
                "candidate_revisions": revision_count,
                "qb_torrents": len(qb),
                "source_download_sha256": expected_sha,
                "media_library_files_hashed": len(library_hashes),
                "dialog_width": dw,
                "dialog_height": dh,
                "viewport_width": vw,
                "viewport_height": vh,
            },
            sort_keys=True,
            separators=(",", ":"),
        )
    )


try:
    main()
except AssertionFailure as failure:
    print(f"FAIL local_cleanup_assertion={failure.code} detail={failure.detail}"[:400])
    sys.exit(1)
PY
