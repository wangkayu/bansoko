#!/usr/bin/env bash
# Read-only VM assertion for the authenticated custom-RSS real-effect session.
#
# This script deliberately does not source env files, run migrations, mutate
# PostgreSQL/qBittorrent, retry work, or create files in the business roots.
# It observes the disposable session through psql, qB's read-only WebUI APIs,
# and the session's temporary download/library roots.
#
# Usage:
#   scripts/verify/real-effect-browser-assert.sh --state-dir /tmp/bansoko-browser-session
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
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

STATE_DIR = Path(sys.argv[1]).resolve()
PHASE = "input"


class AssertionFailure(Exception):
    def __init__(self, code: str, detail: str = "") -> None:
        self.code = code
        self.detail = detail
        super().__init__(code)


def fail(code: str, detail: str = "") -> None:
    raise AssertionFailure(code, detail)


def read_json(path: Path, label: str) -> dict[str, Any]:
    try:
        with path.open(encoding="utf-8") as stream:
            value = json.load(stream)
    except (OSError, ValueError):
        fail(f"{label}_invalid")
    if not isinstance(value, dict):
        fail(f"{label}_must_be_object")
    return value


def decode_shell_word(value: str) -> str:
    """Decode one shell word the way `printf %q` (and `source`) would read it.

    Session env files are written with %q quoting, so values may arrive as a
    bare word with backslash-escaped metacharacters (`\\\\?`), a single-quoted
    word (embedded `'` becomes `'\\\\''`), or a double-quoted word with
    backslash escapes. Values are validated upstream to contain no control
    characters, which keeps this decoder total. ANSI-C quoting (`$'...'`)
    would need full escape decoding; mis-decoding it silently would corrupt
    comparisons, so it is refused instead.
    """
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
        key = key.strip()
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key):
            fail("env_key_invalid")
        values[key] = decode_shell_word(value)
    return values


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


def fixture_path(state: dict[str, Any], session: dict[str, Any]) -> Path:
    configured = first_string(session, ["fixture_metadata", "fixture_metadata_path"])
    if configured is None:
        configured = first_string(state, ["fixture_metadata", "fixture_metadata_path"])
    candidates: list[Path] = []
    if configured:
        candidate = Path(configured)
        candidates.append(candidate if candidate.is_absolute() else STATE_DIR / candidate)
    else:
        candidates.extend(
            STATE_DIR / name
            for name in ("fixture-metadata.json", "fixture_metadata.json", "fixture.json")
        )
        discovered = sorted(STATE_DIR.glob("*fixture*.json"))
        candidates.extend(path for path in discovered if path not in candidates)
    existing: list[Path] = []
    for candidate in candidates:
        candidate = candidate.resolve()
        try:
            candidate.relative_to(STATE_DIR)
        except ValueError:
            fail("fixture_metadata_outside_state_dir")
        if candidate.is_file() and candidate not in existing:
            existing.append(candidate)
    if len(existing) > 1 and not configured:
        fail("fixture_metadata_ambiguous")
    if existing:
        return existing[0]
    fail("fixture_metadata_missing")


def fixture_metadata(state: dict[str, Any], session: dict[str, Any]) -> dict[str, Any]:
    embedded = session.get("fixture_metadata")
    if isinstance(embedded, dict):
        return embedded
    embedded = session.get("fixture")
    if isinstance(embedded, dict):
        return embedded
    return read_json(fixture_path(state, session), "fixture_metadata")


def extract_fixture(metadata: dict[str, Any]) -> tuple[str, str, str | None, int | None, str | None]:
    title = first_string(metadata, ["custom_rss_title", "managed_anime_title", "title"])
    if title is None:
        for branch in ("custom_rss", "managed_anime", "anime"):
            title = scalar(get_path(metadata, branch, "title"))
            if title is not None:
                break
    stable = first_string(metadata, ["stable_item_key", "stable_key"])
    if stable is None:
        stable = scalar(get_path(metadata, "release", "stable_item_key"))
    if stable is None:
        stable = scalar(get_path(metadata, "release", "stable_key"))
    if title is None:
        fail("fixture_title_missing")
    if stable is None:
        fail("fixture_stable_item_key_missing")
    if any(ord(char) < 32 for char in title + stable):
        fail("fixture_identity_contains_control_character")

    expected_sha = first_string(
        metadata, ["source_sha256", "artifact_sha256", "sha256"]
    )
    if expected_sha is None:
        for branch in ("artifact", "source", "release"):
            expected_sha = first_string(
                metadata.get(branch, {}) if isinstance(metadata.get(branch), dict) else {},
                ["sha256", "source_sha256", "artifact_sha256"],
            )
            if expected_sha:
                break
    if expected_sha:
        expected_sha = expected_sha.removeprefix("sha256:").lower()
        if not re.fullmatch(r"[0-9a-f]{64}", expected_sha):
            fail("fixture_sha256_invalid")

    expected_size: int | None = None
    raw_size: Any = first_string(
        metadata, ["source_size_bytes", "artifact_size_bytes", "size_bytes"]
    )
    if raw_size is None:
        for branch in ("artifact", "source", "release"):
            branch_value = metadata.get(branch)
            if isinstance(branch_value, dict):
                raw_size = branch_value.get("size_bytes")
                if raw_size is not None:
                    break
    if raw_size is not None:
        try:
            expected_size = int(raw_size)
        except (TypeError, ValueError):
            fail("fixture_size_invalid")
        if expected_size < 0:
            fail("fixture_size_invalid")

    expected_target = first_string(metadata, ["target_path", "expected_target_path"])
    if expected_target is None:
        target_value = metadata.get("target")
        if isinstance(target_value, dict):
            expected_target = first_string(target_value, ["target_path", "path"])
        elif isinstance(target_value, str):
            expected_target = scalar(target_value)
    return title, stable, expected_sha, expected_size, expected_target


def short_id(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()[:12]


def safe_reason(value: Any) -> str:
    if not isinstance(value, str):
        return ""
    value = re.sub(r"https?://[^\s]+", "[url]", value)
    value = re.sub(r"postgres(?:ql)?://[^\s]+", "[dsn]", value, flags=re.I)
    value = re.sub(r"(?:password|secret|token|cookie)\s*[:=]\s*[^\s,;]+", lambda match: "[redacted]", value, flags=re.I)
    value = re.sub(r"/[^\s,;]+", "[path]", value)
    return " ".join(value.split())[:180]


def require_same(label: str, *values: Any) -> str:
    strings = [str(value) for value in values if value not in (None, "")]
    if not strings:
        fail(f"{label}_missing")
    if len(set(strings)) != 1:
        fail(f"{label}_mismatch")
    return strings[0]


def safe_temp_root(path: str, label: str) -> Path:
    candidate = Path(path).expanduser().resolve()
    allowed_prefixes = (Path("/tmp").resolve(), Path("/private/tmp").resolve())
    if not candidate.is_absolute() or not any(
        candidate == prefix or prefix in candidate.parents for prefix in allowed_prefixes
    ):
        fail(f"{label}_not_disposable")
    if any(part in ("", ".", "..") for part in candidate.parts):
        fail(f"{label}_invalid")
    return candidate


def validate_session(state: dict[str, Any], session: dict[str, Any], env: dict[str, str]) -> tuple[str, str, Path, Path, str, str, str, str]:
    if env.get("BANSOKO_SKIP_AUTH", "0") == "1":
        fail("skip_auth_forbidden")
    for key in ("pg_id", "qb_id", "db_port", "qb_port", "download_root", "library_root"):
        if key not in state:
            fail(f"state_{key}_missing")
    for key in ("pg_id", "qb_id", "download_root", "library_root"):
        if key in session and str(session[key]) != str(state[key]):
            fail(f"session_{key}_mismatch")

    pg_id = str(state["pg_id"])
    qb_id = str(state["qb_id"])
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.:-]{2,127}", pg_id):
        fail("state_pg_identity_invalid")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.:-]{2,127}", qb_id):
        fail("state_qb_identity_invalid")

    download_root = safe_temp_root(str(state["download_root"]), "download_root")
    library_root = safe_temp_root(str(state["library_root"]), "library_root")
    if env.get("BANSOKO_HARNESS_DOWNLOAD_ROOT"):
        if Path(env["BANSOKO_HARNESS_DOWNLOAD_ROOT"]).expanduser().resolve() != download_root:
            fail("download_root_env_mismatch")
    if env.get("BANSOKO_HARNESS_LIBRARY_ROOT"):
        if Path(env["BANSOKO_HARNESS_LIBRARY_ROOT"]).expanduser().resolve() != library_root:
            fail("library_root_env_mismatch")

    db_url = env.get("BANSOKO_TEST_DATABASE_URL", "").strip()
    if not db_url:
        fail("disposable_database_url_missing")
    parsed_db = urllib.parse.urlsplit(db_url)
    if parsed_db.scheme not in ("postgres", "postgresql"):
        fail("disposable_database_scheme_invalid")
    if parsed_db.hostname not in ("127.0.0.1", "localhost"):
        fail("disposable_database_host_invalid")
    if parsed_db.port != int(state["db_port"]):
        fail("disposable_database_port_mismatch")
    db_name = urllib.parse.unquote(parsed_db.path.lstrip("/"))
    if not re.fullmatch(r"bansoko_harness_[A-Za-z0-9_-]+", db_name):
        fail("disposable_database_name_invalid")

    endpoint = env.get("BANSOKO_QB_ENDPOINT", "").strip().rstrip("/")
    parsed_qb = urllib.parse.urlsplit(endpoint)
    if parsed_qb.scheme not in ("http", "https") or parsed_qb.hostname not in ("127.0.0.1", "localhost"):
        fail("qb_endpoint_not_loopback")
    if parsed_qb.port != int(state["qb_port"]):
        fail("qb_endpoint_port_mismatch")
    username = env.get("BANSOKO_QB_USERNAME", "")
    password = env.get("BANSOKO_QB_PASSWORD", "")
    if not username or not password:
        fail("qb_credentials_missing")
    # Destructive-session markers must be present and explicit: this script
    # only observes sessions the real-effect harness booted in disposable
    # mode, so a missing marker means we are not looking at the right session.
    if env.get("BANSOKO_TEST_DB_DISPOSABLE") != "1":
        fail("disposable_marker_missing")
    if env.get("BANSOKO_E2E_REAL_EFFECT") != "1":
        fail("real_effect_marker_missing")
    return db_url, endpoint, download_root, library_root, username, password, pg_id, qb_id


SQL = r"""
SELECT COALESCE(json_agg(json_build_object(
  'managed_anime', json_build_object(
    'id', ma.id, 'title', ma.title, 'title_override', ma.title_override, 'version', ma.version
  ),
  'subscription', json_build_object(
    'id', sub.id, 'managed_anime_id', sub.managed_anime_id, 'origin', sub.origin,
    'instance_label', sub.instance_label, 'state', sub.state, 'version', sub.version
  ),
  'rss_source', json_build_object(
    'id', src.id, 'subscription_id', src.subscription_id, 'source_kind', src.source_kind,
    'group_name', src.group_name, 'title', src.title, 'version', src.version
  ),
  'runtime', json_build_object(
    'rss_source_id', rt.rss_source_id, 'health', rt.health, 'reason_code', rt.reason_code,
    'reason_detail', rt.reason_detail, 'version', rt.version
  ),
  'releases', COALESCE((SELECT json_agg(json_build_object(
    'id', rel.id, 'rss_source_id', rel.rss_source_id, 'stable_item_key', rel.stable_item_key,
    'verdict', rel.verdict, 'reason_code', rel.reason_code, 'reason_detail', rel.reason_detail,
    'download_task_id', rel.download_task_id, 'version', rel.version,
    'raw_title', rel.raw_facts->>'title', 'source_identity', rel.source_facts->>'identity_value'
  ) ORDER BY rel.id) FROM releases rel
    WHERE rel.rss_source_id = src.id AND rel.stable_item_key = :'expected_stable_key'), '[]'::json),
  'download_tasks', COALESCE((SELECT json_agg(json_build_object(
    'id', dt.id, 'release_id', dt.release_id, 'state', dt.state, 'version', dt.version,
    'failure_code', dt.failure_code, 'failure_reason', dt.failure_reason,
    'provider_correlation', dt.provider_correlation, 'frozen_input', dt.frozen_input
  ) ORDER BY dt.id) FROM download_tasks dt
    JOIN releases rel2 ON rel2.download_task_id = dt.id
    WHERE rel2.rss_source_id = src.id AND rel2.stable_item_key = :'expected_stable_key'), '[]'::json),
  'artifacts', COALESCE((SELECT json_agg(json_build_object(
    'id', art.id, 'download_task_id', art.download_task_id, 'state', art.state,
    'failure_code', art.failure_code, 'failure_reason', art.failure_reason,
    'verified_at', art.verified_at, 'version', art.version,
    'files', COALESCE((SELECT json_agg(json_build_object(
      'id', af.id, 'file_ref', af.file_ref, 'relative_path', af.relative_path,
      'source_path', af.source_path, 'media_kind', af.media_kind, 'size_bytes', af.size_bytes,
      'fingerprint', af.fingerprint
    ) ORDER BY af.id) FROM artifact_files af WHERE af.artifact_id = art.id AND af.file_ref = 'primary'), '[]'::json)
  ) ORDER BY art.id) FROM artifacts art
    JOIN download_tasks dt2 ON dt2.id = art.download_task_id
    JOIN releases rel3 ON rel3.download_task_id = dt2.id
    WHERE rel3.rss_source_id = src.id AND rel3.stable_item_key = :'expected_stable_key'), '[]'::json),
  'file_operations', COALESCE((SELECT json_agg(json_build_object(
    'id', fo.id, 'artifact_id', fo.artifact_id, 'artifact_file_id', fo.artifact_file_id,
    'operation', fo.operation, 'source_path', fo.source_path, 'target_path', fo.target_path,
    'source_fingerprint', fo.source_fingerprint, 'target_fingerprint', fo.target_fingerprint,
    'expected_size_bytes', fo.expected_size_bytes, 'observed_target_size_bytes', fo.observed_target_size_bytes,
    'state', fo.state, 'failure_code', fo.failure_code, 'failure_reason', fo.failure_reason,
    'observed_at', fo.observed_at, 'version', fo.version, 'frozen_render_snapshot', fo.frozen_render_snapshot
  ) ORDER BY fo.id) FROM file_operations fo
    JOIN artifacts art2 ON art2.id = fo.artifact_id
    JOIN download_tasks dt3 ON dt3.id = art2.download_task_id
    JOIN releases rel4 ON rel4.download_task_id = dt3.id
    WHERE rel4.rss_source_id = src.id AND rel4.stable_item_key = :'expected_stable_key'), '[]'::json)
) ORDER BY ma.id, src.id), '[]'::json)
FROM managed_anime ma
JOIN subscriptions sub ON sub.managed_anime_id = ma.id
JOIN rss_sources src ON src.subscription_id = sub.id
JOIN rss_source_runtime rt ON rt.rss_source_id = src.id
WHERE src.source_kind = 'custom_rss'
  AND sub.instance_label = :'expected_instance_label'
  AND (ma.title = :'expected_title' OR ma.title_override = :'expected_title' OR src.title = :'expected_title');
"""


def run_psql(db_url: str, db_name: str, pg_id: str, title: str, stable: str, instance_label: str) -> list[dict[str, Any]]:
    args = [
        "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "--set", f"expected_title={title}",
        "--set", f"expected_stable_key={stable}",
        "--set", f"expected_instance_label={instance_label}",
    ]
    command_env = {**os.environ, "PGCONNECT_TIMEOUT": "5"}
    if shutil_which("psql"):
        # Keep the credential-bearing DSN out of argv and therefore out of
        # process listings. psql reads the connection tuple from its PG*
        # environment instead.
        parsed = urllib.parse.urlsplit(db_url)
        command = ["psql", *args]
        command_env.update(
            {
                "PGHOST": parsed.hostname or "",
                "PGPORT": str(parsed.port or "5432"),
                "PGUSER": urllib.parse.unquote(parsed.username or ""),
                "PGPASSWORD": urllib.parse.unquote(parsed.password or ""),
                "PGDATABASE": urllib.parse.unquote(parsed.path.lstrip("/")),
            }
        )
    elif shutil_which("docker"):
        command = ["docker", "exec", pg_id, "psql", "-U", "harness", "-d", db_name, *args]
    else:
        fail("psql_unavailable")
    try:
        result = subprocess.run(
            command, input=SQL, capture_output=True, text=True, timeout=15, check=False,
            env=command_env,
        )
    except (OSError, subprocess.TimeoutExpired):
        fail("database_query_failed")
    if result.returncode != 0:
        fail("database_query_failed")
    output = [line.strip() for line in result.stdout.splitlines() if line.strip()]
    if len(output) != 1:
        fail("database_result_invalid")
    try:
        value = json.loads(output[0])
    except ValueError:
        fail("database_result_invalid")
    if not isinstance(value, list):
        fail("database_result_invalid")
    return value


def shutil_which(command: str) -> str | None:
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        candidate = Path(directory) / command
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate)
    return None


def owner_summary(row: dict[str, Any]) -> str:
    runtime = row.get("runtime") or {}
    releases = row.get("releases") or []
    tasks = row.get("download_tasks") or []
    artifacts = row.get("artifacts") or []
    operations = row.get("file_operations") or []
    release_state = releases[0].get("verdict") if len(releases) == 1 else "missing"
    task_state = tasks[0].get("state") if len(tasks) == 1 else "missing"
    artifact_state = artifacts[0].get("state") if len(artifacts) == 1 else "missing"
    completed = [item for item in operations if item.get("state") == "completed"]
    operation_state = "completed" if len(completed) == 1 else ("pending" if operations else "missing")
    return (
        f"runtime={runtime.get('health','missing')} release={release_state} "
        f"download={task_state} artifact={artifact_state} file_operation={operation_state}"
    )


def validate_chain(rows: list[dict[str, Any]], title: str, stable: str, expected_instance_label: str) -> dict[str, Any] | None:
    if len(rows) > 1:
        fail("owner_chain_not_unique", f"candidates={len(rows)}")
    if not rows:
        return None
    row = rows[0]
    anime = row.get("managed_anime") or {}
    subscription = row.get("subscription") or {}
    source = row.get("rss_source") or {}
    runtime = row.get("runtime") or {}
    if anime.get("title") != title or source.get("title") != title:
        fail("custom_rss_title_mismatch")
    if subscription.get("managed_anime_id") != anime.get("id") or subscription.get("origin") != "custom":
        fail("subscription_owner_mismatch")
    # Defense in depth: the SQL already filters on the session-scoped label;
    # this explicit check keeps a mismatch legible instead of silent.
    if subscription.get("instance_label") != expected_instance_label:
        fail("subscription_instance_label_mismatch")
    if source.get("subscription_id") != subscription.get("id") or source.get("source_kind") != "custom_rss":
        fail("rss_source_owner_mismatch")
    if runtime.get("rss_source_id") != source.get("id"):
        fail("rss_runtime_owner_mismatch")
    releases = row.get("releases") or []
    if len(releases) > 1:
        fail("release_not_unique", f"count={len(releases)}")
    if not releases:
        return None
    release = releases[0]
    if release.get("stable_item_key") != stable:
        fail("stable_item_key_mismatch")
    tasks = row.get("download_tasks") or []
    if len(tasks) > 1:
        fail("download_task_not_unique", f"count={len(tasks)}")
    if not tasks:
        return None
    task = tasks[0]
    artifacts = row.get("artifacts") or []
    if len(artifacts) > 1:
        fail("artifact_not_unique", f"count={len(artifacts)}")
    if not artifacts:
        return None
    artifact = artifacts[0]
    files = artifact.get("files") or []
    if len(files) != 1:
        if len(files) == 0:
            return None
        fail("primary_artifact_file_not_unique", f"count={len(files)}")
    operations = row.get("file_operations") or []
    completed = [item for item in operations if item.get("state") == "completed"]
    if len(completed) > 1:
        fail("completed_file_operation_not_unique", f"count={len(completed)}")
    if task.get("state") != "completed" or artifact.get("state") != "verified" or len(completed) != 1:
        return None
    if release.get("verdict") != "submitted":
        return None
    if runtime.get("health") != "ready":
        return None
    return {
        "row": row,
        "anime": anime,
        "subscription": subscription,
        "source": source,
        "runtime": runtime,
        "release": release,
        "task": task,
        "artifact": artifact,
        "artifact_file": files[0],
        "operation": completed[0],
    }


def json_object(value: Any) -> dict[str, Any]:
    return value if isinstance(value, dict) else {}


def digest_value(value: Any) -> str | None:
    if not isinstance(value, dict):
        return None
    for key in ("value", "sha256", "digest", "hash"):
        candidate = scalar(value.get(key))
        if candidate:
            candidate = candidate.removeprefix("sha256:").lower()
            if re.fullmatch(r"[0-9a-f]{64}", candidate):
                return candidate
    return None


def fingerprint_size(value: Any) -> int | None:
    if not isinstance(value, dict):
        return None
    raw = value.get("size_bytes")
    try:
        return int(raw) if raw is not None else None
    except (TypeError, ValueError):
        return None


def snapshot_target(snapshot: Any, library_root: Path) -> Path | None:
    if not isinstance(snapshot, dict):
        return None
    value = snapshot.get("target_path")
    if value is None:
        value = snapshot.get("target")
    if value is None:
        target = snapshot.get("rendered_target")
        if isinstance(target, dict):
            value = target.get("target_path") or target.get("path")
        elif isinstance(target, str):
            value = target
    if value is None:
        target = snapshot.get("target_intent")
        if isinstance(target, dict):
            value = target.get("target_path") or target.get("path")
    value = scalar(value)
    if value is None:
        return None
    path = Path(value)
    return (path if path.is_absolute() else library_root / path).resolve()


def resolve_target(operation: dict[str, Any], library_root: Path) -> Path:
    raw = scalar(operation.get("target_path"))
    snapshot_path = snapshot_target(operation.get("frozen_render_snapshot"), library_root)
    if raw is None:
        if snapshot_path is None:
            fail("frozen_render_snapshot_target_path_missing")
        return snapshot_path
    path = Path(raw)
    resolved = (path if path.is_absolute() else library_root / path).resolve()
    if snapshot_path is not None and resolved != snapshot_path:
        fail("frozen_render_snapshot_target_path_mismatch")
    return resolved


def hash_file(path: Path) -> tuple[int, str]:
    digest = hashlib.sha256()
    size = 0
    try:
        with path.open("rb") as stream:
            while True:
                block = stream.read(1024 * 1024)
                if not block:
                    break
                size += len(block)
                digest.update(block)
    except OSError:
        fail("file_read_failed")
    return size, digest.hexdigest()


def inside(root: Path, path: Path, code: str) -> None:
    try:
        path.relative_to(root)
    except ValueError:
        fail(code)


def check_files(chain: dict[str, Any], download_root: Path, library_root: Path, expected_sha: str | None, expected_size: int | None, expected_target: str | None) -> dict[str, Any]:
    artifact_file = chain["artifact_file"]
    operation = chain["operation"]
    source_path = Path(str(artifact_file.get("source_path", ""))).resolve()
    if not source_path.is_absolute():
        fail("source_path_not_absolute")
    inside(download_root, source_path, "source_path_outside_download_root")
    target_path = resolve_target(operation, library_root)
    inside(library_root, target_path, "target_path_outside_library_root")
    if not target_path.is_file():
        fail("target_file_missing")
    if not source_path.is_file():
        # A completed move legitimately removes the source. Its immutable
        # artifact/source fingerprint and the target reread still prove the
        # effect; a copy/hardlink must retain a readable source for comparison.
        if operation.get("operation") != "move":
            fail("source_file_missing")
        target_size, target_sha = hash_file(target_path)
        source_size, source_sha = target_size, target_sha
    else:
        source_size, source_sha = hash_file(source_path)
    target_size, target_sha = hash_file(target_path)
    if source_size != target_size or source_sha != target_sha:
        fail("source_target_fingerprint_mismatch")
    if expected_sha and source_sha != expected_sha:
        fail("fixture_source_sha256_mismatch")
    if expected_size is not None and source_size != expected_size:
        fail("fixture_source_size_mismatch")

    artifact_fp = artifact_file.get("fingerprint")
    source_fp = operation.get("source_fingerprint")
    target_fp = operation.get("target_fingerprint")
    for label, fingerprint in (("artifact", artifact_fp), ("source", source_fp), ("target", target_fp)):
        if digest_value(fingerprint) != source_sha:
            fail(f"{label}_fingerprint_mismatch")
        if fingerprint_size(fingerprint) not in (None, source_size):
            fail(f"{label}_fingerprint_size_mismatch")

    stored_size = operation.get("expected_size_bytes")
    observed_size = operation.get("observed_target_size_bytes")
    if stored_size is None or observed_size is None:
        fail("file_operation_size_evidence_missing")
    if int(stored_size) != source_size or int(observed_size) != target_size:
        fail("file_operation_size_evidence_mismatch")

    if expected_target:
        expected_path = Path(expected_target)
        expected_resolved = (expected_path if expected_path.is_absolute() else library_root / expected_path).resolve()
        if expected_resolved != target_path:
            fail("fixture_target_path_mismatch")
    relative_target = target_path.relative_to(library_root)
    return {
        "source_size": source_size,
        "source_sha": source_sha,
        "target_size": target_size,
        "target_sha": target_sha,
        "source_relative": source_path.relative_to(download_root),
        "target_relative": relative_target,
    }


def qB_request(opener: urllib.request.OpenerDirector, endpoint: str, path: str, data: bytes | None = None) -> Any:
    url = endpoint + path
    request = urllib.request.Request(url, data=data, method="POST" if data is not None else "GET")
    if data is not None:
        request.add_header("Content-Type", "application/x-www-form-urlencoded")
    try:
        with opener.open(request, timeout=10) as response:
            body = response.read(2 * 1024 * 1024)
            if response.status < 200 or response.status >= 300:
                fail("qb_http_status")
    except (urllib.error.URLError, TimeoutError, OSError):
        fail("qb_request_failed")
    if path.endswith("/auth/login"):
        return body
    try:
        return json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        fail("qb_response_invalid")


def check_qb(chain: dict[str, Any], endpoint: str, username: str, password: str, source_size: int) -> dict[str, Any]:
    correlation = json_object(chain["task"].get("provider_correlation"))
    frozen = json_object(chain["task"].get("frozen_input"))
    stable_tag = scalar(correlation.get("stable_tag")) or scalar(correlation.get("external_id"))
    info_hash = scalar(correlation.get("info_hash"))
    if not stable_tag and not info_hash:
        fail("qb_correlation_missing")
    tag = stable_tag or ""
    if tag.startswith("tag:"):
        tag = tag[4:]
    if tag:
        query = "tag=" + urllib.parse.quote(tag, safe="")
    else:
        query = "hashes=" + urllib.parse.quote(info_hash or "", safe="")
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor())
    login_data = urllib.parse.urlencode({"username": username, "password": password}).encode()
    qB_request(opener, endpoint, "/api/v2/auth/login", login_data)
    items = qB_request(opener, endpoint, "/api/v2/torrents/info?" + query)
    if not isinstance(items, list):
        fail("qb_torrent_list_invalid")
    if len(items) > 1:
        fail("qb_corresponding_task_not_unique", f"count={len(items)}")
    if not items:
        # qB may have already forgotten a torrent after a successful effect. The
        # terminal DB owner plus the independently hashed files is the completion
        # record in that case; a missing qB row alone is never success.
        if chain["task"].get("state") == "completed":
            return {"presence": "db_completed_record", "task_count": 0}
        fail("qb_corresponding_task_missing")
    item = items[0]
    if not isinstance(item, dict):
        fail("qb_torrent_record_invalid")
    progress = float(item.get("progress", 0) or 0)
    size = int(item.get("size", 0) or 0)
    downloaded = int(item.get("downloaded", 0) or 0)
    if progress < 0.999 or downloaded < source_size:
        fail("qb_torrent_not_complete")
    if size and size != source_size:
        fail("qb_torrent_size_mismatch")
    state = scalar(item.get("state")) or "unknown"
    if state.lower() in {"error", "missingfiles", "unknown"}:
        fail("qb_torrent_failed")
    torrent_hash = scalar(item.get("hash"))
    if not torrent_hash:
        fail("qb_torrent_hash_missing")
    files = qB_request(opener, endpoint, "/api/v2/torrents/files?hash=" + urllib.parse.quote(torrent_hash, safe=""))
    if not isinstance(files, list) or not files:
        fail("qb_torrent_files_missing")
    complete_files = [file for file in files if isinstance(file, dict) and float(file.get("progress", 0) or 0) >= 0.999]
    if not complete_files:
        fail("qb_torrent_files_incomplete")
    return {
        "presence": "api_task",
        "task_count": 1,
        "hash_digest": short_id(torrent_hash),
        "state": state,
        "progress": round(progress, 4),
        "size": size,
        "downloaded": downloaded,
        "complete_file_count": len(complete_files),
        "path_snapshot_present": bool(frozen.get("path_snapshot")),
    }


def main() -> int:
    global PHASE
    state = read_json(STATE_DIR / "state.json", "state")
    session = read_json(STATE_DIR / "browser-session.json", "browser_session")
    env = parse_env(STATE_DIR / "env")
    # Session-scoped attribution: without the label this session's owner rows
    # cannot be distinguished from any other run, so fail closed.
    expected_instance_label = env.get("BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL", "").strip()
    if not expected_instance_label:
        fail("expected_instance_label_missing")
    if len(expected_instance_label) > 256 or any(ord(char) < 32 for char in expected_instance_label):
        fail("expected_instance_label_invalid")
    metadata = fixture_metadata(state, session)
    title, stable, expected_sha, expected_size, expected_target = extract_fixture(metadata)
    db_url, endpoint, download_root, library_root, username, password, pg_id, qb_id = validate_session(state, session, env)
    db_name = urllib.parse.unquote(urllib.parse.urlsplit(db_url).path.lstrip("/"))
    timeout_raw = os.environ.get("BANSOKO_REAL_EFFECT_BROWSER_ASSERT_TIMEOUT", "180")
    try:
        timeout = int(timeout_raw)
    except ValueError:
        fail("timeout_invalid")
    if timeout < 1 or timeout > 3600:
        fail("timeout_invalid")

    print(
        f"ASSERT session={STATE_DIR.name} pg={short_id(pg_id)} qb={short_id(qb_id)} "
        f"instance={expected_instance_label} "
        f"title={title!r} stable_key_sha256={hashlib.sha256(stable.encode()).hexdigest()}",
        flush=True,
    )
    deadline = time.monotonic() + timeout
    last_status = ""
    terminal_chain: dict[str, Any] | None = None
    while time.monotonic() < deadline:
        PHASE = "database"
        rows = run_psql(db_url, db_name, pg_id, title, stable, expected_instance_label)
        if not rows:
            status = "owners=0"
        else:
            status = "owners=" + str(len(rows)) + " " + owner_summary(rows[0])
        if status != last_status:
            print("OBSERVE " + status, flush=True)
            last_status = status
        terminal_chain = validate_chain(rows, title, stable, expected_instance_label)
        if terminal_chain is not None:
            break
        time.sleep(1)

    if terminal_chain is None:
        fail("owner_chain_timeout", last_status or "owners=0")

    PHASE = "filesystem"
    files = check_files(terminal_chain, download_root, library_root, expected_sha, expected_size, expected_target)
    PHASE = "qbittorrent"
    qb = check_qb(terminal_chain, endpoint, username, password, files["source_size"])

    row = terminal_chain["row"]
    print(
        "EVIDENCE "
        f"managed_anime={short_id(str(terminal_chain['anime']['id']))} "
        f"subscription={short_id(str(terminal_chain['subscription']['id']))} "
        f"rss_source={short_id(str(terminal_chain['source']['id']))} "
        f"runtime={terminal_chain['runtime'].get('health')} "
        f"release={terminal_chain['release'].get('verdict')} "
        f"download_task={terminal_chain['task'].get('state')} "
        f"artifact={terminal_chain['artifact'].get('state')} "
        f"file_operation={terminal_chain['operation'].get('state')}",
    )
    for label, owner in (
        ("runtime", terminal_chain["runtime"]),
        ("release", terminal_chain["release"]),
        ("download_task", terminal_chain["task"]),
        ("artifact", terminal_chain["artifact"]),
        ("file_operation", terminal_chain["operation"]),
    ):
        reason = owner.get("reason_code") or owner.get("failure_code") or "none"
        detail = safe_reason(owner.get("reason_detail") or owner.get("failure_reason"))
        state_value = owner.get("state") or owner.get("verdict") or owner.get("health") or "unknown"
        print(f"OWNER {label} state={state_value} version={owner.get('version')} reason={reason} detail={detail!r}")
    print(
        "FILES "
        f"source_rel={files['source_relative']} target_rel={files['target_relative']} "
        f"size={files['source_size']} sha256={files['source_sha']} "
        f"source_target_match=true library_root=true",
    )
    print("QB " + " ".join(f"{key}={value}" for key, value in qb.items()))
    print("PASS real-effect-browser-assert")
    return 0


try:
    raise SystemExit(main())
except AssertionFailure as error:
    detail = safe_reason(error.detail)
    print(f"FAIL phase={PHASE} code={error.code} detail={detail!r}", file=sys.stderr)
    raise SystemExit(1)
except Exception:
    print(f"FAIL phase={PHASE} code=unexpected_assertion_error", file=sys.stderr)
    raise SystemExit(1)
PY
