#!/usr/bin/env python3
"""Local-only observer of an active disposable browser session; never prints DSNs.

Before/after keyed snapshots cannot prove absence of transient create/delete or
attribute concurrent worker changes to preview. Such changes fail the comparison.
No HTTP endpoint, product CLI or SQL mutation is introduced.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlsplit

SAFE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
CONTAINER_ID = re.compile(r"^[0-9a-f]{64}$")
# Closed table/column mapping, grounded in the migration schema. JSON documents,
# tokens, endpoint URLs, payloads and free-text errors are deliberately excluded.
TABLES = {
    "managed_anime": ("managed_anime", "id", "NULL::text", "version"),
    "lifecycle": ("managed_anime", "id", "lifecycle_state", "lifecycle_version"),
    "subscription": ("subscriptions", "id", "state", "version"),
    "rss_source": ("rss_sources", "id", "NULL::text", "version"),
    "metadata": ("anime_metadata", "managed_anime_id", "NULL::text", "version"),
    # S7 converged anime_config_owners (rules/subgroups), the instance_* policy
    # snapshots and instance_business_snapshots into one anime_business_configs
    # row per ManagedAnime — "One row carries every owner version, the three
    # policy axes and the frozen business, so the version vector and the
    # snapshots can never drift apart" (db/migrations/001_baseline.sql). One
    # snapshot row per axis keeps the historical per-owner identity, and every
    # axis write bumps exactly its own version (anime/ownership/patch.go,
    # parserpolicy/service.go, filemaintenance/*_policy.go,
    # anime/animeconfig/business_sync.go, anime/draft/business_apply.go), so the
    # specs' per-owner invariance assertions still see any config-owner write.
    # Nullable axes project to an integer >= 1 because the guard requires a
    # positive version and NULL there means the axis was never set.
    "config_owner_rules": ("anime_business_configs", "managed_anime_id", "'rules'", "rules_version"),
    "config_owner_subgroups": ("anime_business_configs", "managed_anime_id", "'subgroups'", "subgroups_version"),
    "config_owner_parser": ("anime_business_configs", "managed_anime_id", "'parser'", "COALESCE(parser_version, 0) + 1"),
    "config_owner_replacement": ("anime_business_configs", "managed_anime_id", "'replacement'", "COALESCE(replacement_version, 0) + 1"),
    "config_owner_file_effect": ("anime_business_configs", "managed_anime_id", "'file_effect'", "COALESCE(file_effect_version, 0) + 1"),
    "config_owner_business": ("anime_business_configs", "managed_anime_id", "'business'", "business_version + 1"),
    "rss_source_runtime": ("rss_source_runtime", "rss_source_id", "health", "version"),
    "release": ("releases", "id", "verdict", "version"),
    "download_task": ("download_tasks", "id", "state", "version"),
    "artifact": ("artifacts", "id", "state", "version"),
    "file_operation": ("file_operations", "id", "state", "version"),
    "plugin_delivery": ("plugin_deliveries", "id", "state", "version"),
    "placement": ("placements", "id", "state", "version"),
    "file_maintenance": ("reconciliation_scans", "managed_anime_id", "state", "version"),
    "alignment_operation": ("alignment_operations", "id", "state", "version"),
    "replacement_deletion": ("replacement_deletions", "id", "state", "version"),
    "offset_migration_operation": ("offset_migration_plans", "id", "state", "version"),
    "postprocessing_plan": ("postprocessing_plans", "id", "state", "version"),
    # Acquisition roots own the acquisition_settle WorkItem; they carry no
    # updated_at column, so their last transition time stands in for it.
    "acquisition": ("acquisitions", "id", "state", "version"),
}
UPDATED_AT = {"acquisition": "COALESCE(terminal_at, accepted_at)"}
SCOPES = {
    "managed_anime": "id=:'managed_id'",
    "lifecycle": "id=:'managed_id'",
    "subscription": "managed_anime_id=:'managed_id'",
    "rss_source": "subscription_id IN (SELECT id FROM subscriptions WHERE managed_anime_id=:'managed_id')",
    "metadata": "managed_anime_id=:'managed_id'",
    "config_owner_rules": "managed_anime_id=:'managed_id'",
    "config_owner_subgroups": "managed_anime_id=:'managed_id'",
    "config_owner_parser": "managed_anime_id=:'managed_id'",
    "config_owner_replacement": "managed_anime_id=:'managed_id'",
    "config_owner_file_effect": "managed_anime_id=:'managed_id'",
    "config_owner_business": "managed_anime_id=:'managed_id'",
    "rss_source_runtime": "rss_source_id=:'source_id'",
    "release": "id=:'release_id' AND rss_source_id=:'source_id'",
    "download_task": "id IN (SELECT download_task_id FROM releases WHERE id=:'release_id')",
    "artifact": "download_task_id IN (SELECT download_task_id FROM releases WHERE id=:'release_id')",
    "file_operation": "artifact_id IN (SELECT id FROM artifacts WHERE download_task_id IN (SELECT download_task_id FROM releases WHERE id=:'release_id'))",
    "plugin_delivery": "managed_anime_id=:'managed_id'",
    "placement": "managed_anime_id=:'managed_id'",
    "file_maintenance": "managed_anime_id=:'managed_id'",
    "alignment_operation": "managed_anime_id=:'managed_id'",
    "replacement_deletion": "managed_anime_id=:'managed_id'",
    "offset_migration_operation": "managed_anime_id=:'managed_id'",
    "postprocessing_plan": "artifact_id IN (SELECT id FROM artifacts WHERE download_task_id IN (SELECT download_task_id FROM releases WHERE id=:'release_id'))",
    "acquisition": "source_release_ref=:'release_id'",
}


def owner_union(scoped: bool) -> str:
    return " UNION ALL ".join(
        f"SELECT '{kind}'::text type, {pk} id, {state} state, {version} version, " + UPDATED_AT.get(kind, "updated_at") + " updated_at, "
        + ("fence_epoch" if kind == "lifecycle" else "NULL::bigint") + " fence_epoch "
        + f"FROM {table}" + (f" WHERE {SCOPES[kind]}" if scoped else "")
        for kind, (table, pk, state, version) in TABLES.items()
    )


def owner_json(scoped: bool) -> str:
    # Explicit columns: no row_to_json/table-wide projection.
    return "COALESCE((SELECT json_agg(json_build_object('type',q.type,'id',q.id,'state',q.state,'version',q.version,'updated_at',q.updated_at,'fence_epoch',q.fence_epoch) ORDER BY q.type,q.id) FROM (" + owner_union(scoped) + ") q),'[]'::json)"


WORK_JSON = "COALESCE((SELECT json_agg(json_build_object('id',w.id,'owner_type',w.owner_type,'owner_id',w.owner_id,'kind',w.kind,'state',w.state,'fencing_token',w.fencing_token,'attempt',w.attempt,'updated_at',w.updated_at,'managed_anime_id',w.managed_anime_id,'owner_lifecycle_epoch',w.owner_lifecycle_epoch,'external_effect_admitted_at',w.external_effect_admitted_at) ORDER BY w.id) FROM work_items w {where}),'[]'::json)"
SQL = """BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '10s';
SELECT json_build_object(
 'database',current_database(),
 'read_only',current_setting('transaction_read_only'),
 'isolation',current_setting('transaction_isolation'),
 'owners',%s,
 'work_items',%s,
 'candidates',COALESCE((SELECT json_agg(json_build_object('id',id,'version',version,'updated_at',updated_at,'status',status,'activated_managed_anime_id',activated_managed_anime_id,'current_revision',current_revision,'current_revision_digest',current_revision_digest,'draft_digest',draft_digest) ORDER BY id) FROM anime_config_candidates),'[]'::json),
 'candidate_revisions',COALESCE((SELECT json_agg(json_build_object('candidate_id',candidate_id,'revision',revision,'draft_digest',draft_digest,'effective_digest',effective_digest,'parser_policy_digest',parser_policy_digest,'created_at',created_at) ORDER BY candidate_id,revision) FROM anime_config_candidate_revisions),'[]'::json),
 'target',CASE WHEN :'release_id'='' THEN NULL ELSE (
   SELECT json_build_object('managed_id',s.managed_anime_id,'source_id',src.id,'release_id',r.id)
   FROM releases r JOIN rss_sources src ON src.id=r.rss_source_id
   JOIN subscriptions s ON s.id=src.subscription_id
   WHERE r.id=:'release_id' AND src.id=:'source_id' AND s.managed_anime_id=:'managed_id'
 ) END,
 'scoped_owners',CASE WHEN :'release_id'='' THEN NULL ELSE %s END,
 'scoped_work_items',CASE WHEN :'release_id'='' THEN NULL ELSE %s END
);
COMMIT;
""" % (owner_json(False), WORK_JSON.format(where=""), owner_json(True),
       WORK_JSON.format(where="WHERE (w.owner_type,w.owner_id) IN (SELECT q.type,q.id FROM (" + owner_union(True) + ") q)"))


def fail(code: str) -> None:
    raise ValueError(code)


def read_object(path: Path) -> dict:
    value = json.loads(path.read_text())
    if not isinstance(value, dict):
        fail("session_state_invalid")
    return value


def parse_env(path: Path) -> dict[str, str]:
    result = {}
    for line in path.read_text().splitlines():
        if not line or line.startswith("#"):
            continue
        key, separator, raw = line.partition("=")
        if not separator or not re.fullmatch(r"[A-Z][A-Z0-9_]*", key) or key in result:
            fail("session_env_invalid")
        # Session emits printf %q; accept ordinary escaped/quoted tokens, never
        # source/eval the credential file. ANSI-C shell quoting is not needed for
        # the generated credentials; unknown representations fail closed.
        if raw.startswith("$'"):
            fail("session_env_invalid")
        tokens = shlex.split(raw)
        if len(tokens) > 1:
            fail("session_env_invalid")
        result[key] = tokens[0] if tokens else ""
    return result


def validate_dsn(raw: str, port: int, database: str):
    dsn = urlsplit(raw)
    if (dsn.scheme not in ("postgres", "postgresql") or dsn.hostname not in ("127.0.0.1", "::1")
            or dsn.port != port or unquote(dsn.path[1:]) != database
            or not dsn.username or not dsn.password or dsn.fragment
            or parse_qs(dsn.query) != {"sslmode": ["disable"]}):
        fail("database_endpoint_mismatch")
    return dsn


def command(args: list[str], **kwargs) -> str:
    result = subprocess.run(args, capture_output=True, text=True, timeout=20, **kwargs)
    if result.returncode:
        fail("observer_command_failed")
    return result.stdout


OWNER_FIELDS = {"type", "id", "state", "version", "updated_at", "fence_epoch"}
WORK_FIELDS = {"id", "owner_type", "owner_id", "kind", "state", "fencing_token", "attempt", "updated_at", "managed_anime_id", "owner_lifecycle_epoch", "external_effect_admitted_at"}
CANDIDATE_FIELDS = {"id", "version", "updated_at", "status", "activated_managed_anime_id", "current_revision", "current_revision_digest", "draft_digest"}
REVISION_FIELDS = {"candidate_id", "revision", "draft_digest", "effective_digest", "parser_policy_digest", "created_at"}
SAFE_STATE = re.compile(r"^[a-z][a-z0-9_]{0,63}$")
SAFE_TIME = re.compile(r"^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}(?::\d{2})?)$")
SAFE_DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")


def safe_integer(value, minimum: int = 1) -> bool:
    return type(value) is int and minimum <= value <= 9007199254740991


def validate_output(value: dict, database: str, target: dict | None) -> None:
    keys = {"database", "read_only", "isolation", "owners", "work_items", "candidates", "candidate_revisions", "target", "scoped_owners", "scoped_work_items"}
    if set(value) != keys or value["database"] != database or value["read_only"] != "on" or value["isolation"] != "repeatable read":
        fail("database_identity_or_transaction_mismatch")
    if value["target"] != target:
        fail("target_identity_mismatch")
    for name, fields in (("owners", OWNER_FIELDS), ("work_items", WORK_FIELDS),
                         ("candidates", CANDIDATE_FIELDS), ("candidate_revisions", REVISION_FIELDS),
                         ("scoped_owners", OWNER_FIELDS), ("scoped_work_items", WORK_FIELDS)):
        rows = value[name]
        if name.startswith("scoped_") and target is None:
            if rows is not None:
                fail("database_result_invalid")
            continue
        if not isinstance(rows, list):
            fail("database_result_invalid")
        seen = set()
        for row in rows:
            if not isinstance(row, dict) or set(row) != fields:
                fail("database_result_invalid")
            for column, item in row.items():
                if column in ("type", "owner_type"):
                    if item not in TABLES:
                        fail("database_result_invalid")
                elif column in ("id", "owner_id", "candidate_id", "managed_anime_id", "activated_managed_anime_id"):
                    if item is None and column in ("managed_anime_id", "activated_managed_anime_id"):
                        continue
                    # Every snapshot id is a baseline-bounded owner key (the
                    # config axes use managed_anime_id, whose CHECK caps it at
                    # the 128 characters SAFE_ID allows).
                    if not isinstance(item, str) or not SAFE_ID.fullmatch(item):
                        fail("database_result_invalid")
                elif column in ("state", "status", "kind"):
                    if item is None and fields == OWNER_FIELDS:
                        continue
                    if not isinstance(item, str) or not SAFE_STATE.fullmatch(item):
                        fail("database_result_invalid")
                elif column in ("updated_at", "created_at", "external_effect_admitted_at"):
                    if item is None and column == "external_effect_admitted_at":
                        continue
                    if not isinstance(item, str) or not SAFE_TIME.fullmatch(item):
                        fail("database_result_invalid")
                elif column.endswith("digest"):
                    if item is None and column == "current_revision_digest":
                        continue
                    if not isinstance(item, str) or not SAFE_DIGEST.fullmatch(item):
                        fail("database_result_invalid")
                elif item is None and column in ("current_revision", "owner_lifecycle_epoch", "fence_epoch"):
                    if column == "fence_epoch" and row.get("type") == "lifecycle":
                        fail("lifecycle_fence_missing")
                elif not safe_integer(item, 0 if column in ("attempt", "fencing_token") else 1):
                    fail("database_result_invalid")
            key = (row["candidate_id"], row["revision"]) if fields == REVISION_FIELDS else (row.get("type"), row["id"])
            if key in seen:
                fail("database_result_invalid")
            seen.add(key)
    revisions = {(row["candidate_id"], row["revision"]): row for row in value["candidate_revisions"]}
    candidates = {row["id"]: row for row in value["candidates"]}
    if any(row["candidate_id"] not in candidates for row in revisions.values()):
        fail("candidate_revision_parent_missing")
    for row in candidates.values():
        current = row["current_revision"]
        if current is None:
            if row["current_revision_digest"] is not None:
                fail("candidate_revision_pointer_invalid")
        elif (row["id"], current) not in revisions or revisions[row["id"], current]["effective_digest"] != row["current_revision_digest"]:
            fail("candidate_revision_pointer_invalid")
    if target:
        for kind, identity in (("managed_anime", target["managed_id"]), ("lifecycle", target["managed_id"]), ("rss_source", target["source_id"]), ("release", target["release_id"])):
            if not any(row["type"] == kind and row["id"] == identity for row in value["scoped_owners"]):
                fail("scoped_owner_missing")


# The disposable PG must sit on Docker's default bridge network. Older Docker
# reports that as "default", newer releases as "bridge"; host, container and
# user-defined networks stay refused.
DEFAULT_BRIDGE_MODES = frozenset({"default", "bridge"})


def snapshot(state_dir: Path, target: dict | None) -> dict:
    state_dir = state_dir.resolve(strict=True)
    state = read_object(state_dir / "state.json")
    session = read_object(state_dir / "browser-session.json")
    env = parse_env(state_dir / "env")
    if env.get("BANSOKO_TEST_DB_DISPOSABLE") != "1" or env.get("BANSOKO_E2E_REAL_EFFECT") != "1" or env.get("BANSOKO_SKIP_AUTH", "0") != "0" or any(env.get(key) == "production" for key in ("BANSOKO_ENV", "BANSOKO_ENVIRONMENT")):
        fail("disposable_session_required")
    pg_id, port = str(state["pg_id"]), int(state["db_port"])
    if not CONTAINER_ID.fullmatch(pg_id) or not 1 <= port <= 65535:
        fail("session_identity_invalid")
    state_dsn = urlsplit(state["db_url"])
    database = unquote(state_dsn.path[1:])
    if not re.fullmatch(r"bansoko_harness_[A-Za-z0-9_-]+", database):
        fail("database_name_invalid")
    dsn = validate_dsn(env["BANSOKO_TEST_DATABASE_URL"], port, database)
    validate_dsn(state["db_url"], port, database)
    if state["db_url"] != env["BANSOKO_TEST_DATABASE_URL"] or session.get("schema") != "bansoko-browser-session/v1" or session.get("pg_id") != pg_id or session.get("db_port") != port or session.get("db_name") != database or Path(session["state_dir"]).resolve() != state_dir:
        fail("session_identity_mismatch")
    pid, pgid = (state_dir / "server.pid").read_text().strip(), (state_dir / "server.pgid").read_text().strip()
    if not pid.isdigit() or not pgid.isdigit() or int(pgid) < 2:
        fail("session_not_active")
    processes = command(["ps", "-axo", "pid=,pgid=,command="])
    if not any(parts[0] == pid and parts[1] == pgid and "cmd/bansoko-server" in parts[2] for line in processes.splitlines() if len(parts := line.split(None, 2)) == 3):
        fail("session_not_active")
    inspected = json.loads(command(["docker", "inspect", "--format", "{{json .}}", pg_id]))
    bindings = inspected["NetworkSettings"]["Ports"].get("5432/tcp")
    if inspected.get("Id") != pg_id or not inspected["State"].get("Running") or inspected["HostConfig"].get("NetworkMode") not in DEFAULT_BRIDGE_MODES or not isinstance(bindings, list) or not bindings or not all(binding.get("HostIp") in ("127.0.0.1", "::1") and binding.get("HostPort") == str(port) for binding in bindings):
        fail("database_container_mapping_mismatch")
    pg_env = {key: value for key, value in os.environ.items() if not key.startswith("PG")}
    pg_env.update(PGHOST=dsn.hostname, PGPORT=str(port), PGUSER=unquote(dsn.username), PGPASSWORD=unquote(dsn.password), PGDATABASE=database, PGSSLMODE="disable", PGCONNECT_TIMEOUT="5", PGOPTIONS="-c default_transaction_read_only=on")
    args = ["psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-f", "-"]
    for key in ("managed_id", "source_id", "release_id"):
        args += ["--set", key + "=" + (target[key] if target else "")]
    output = command(args, input=SQL, env=pg_env)
    rows = [json.loads(line) for line in output.splitlines() if line.strip()]
    if len(rows) != 1 or not isinstance(rows[0], dict):
        fail("database_result_invalid")
    validate_output(rows[0], database, target)
    return rows[0]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--state-dir", required=True)
    for key in ("managed-id", "source-id", "release-id"):
        parser.add_argument("--" + key, default="")
    args = parser.parse_args()
    target = {key: getattr(args, key) for key in ("managed_id", "source_id", "release_id")}
    try:
        if any(target.values()) and not all(SAFE_ID.fullmatch(value) for value in target.values()):
            fail("target_identity_incomplete_or_invalid")
        value = snapshot(Path(args.state_dir), target if any(target.values()) else None)
        print(json.dumps(value, sort_keys=True, separators=(",", ":")))
    except ValueError as error:
        # fail() codes are closed constants; any other ValueError text is not echoed.
        code = str(error) if re.fullmatch(r"[a-z_]{1,64}", str(error)) else "unclassified"
        print(f"FAIL: disposable snapshot guard/query rejected ({code})", file=sys.stderr)
        raise SystemExit(2)
    except (KeyError, OSError, TypeError, subprocess.SubprocessError):
        # Exceptions may contain connection details or child stderr. Never echo.
        print("FAIL: disposable snapshot guard/query rejected", file=sys.stderr)
        raise SystemExit(2)


if __name__ == "__main__":
    main()
