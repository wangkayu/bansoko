#!/usr/bin/env python3
"""Offline real-entry tests: no PostgreSQL, Docker daemon or remote invocation."""
from __future__ import annotations
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "scripts/ci/real-effect-browser-db-snapshot.py"


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="db-snapshot-test-") as td:
        root = Path(td)
        state, fake = root / "session", root / "bin"
        state.mkdir(); fake.mkdir()
        pg_id = "a" * 64
        database = "bansoko_harness_123"
        dsn = f"postgres://harness:SECRET_SENTINEL@127.0.0.1:55432/{database}?sslmode=disable"
        state_value = {"pg_id": pg_id, "db_port": 55432, "db_url": dsn}
        session_value = {"schema": "bansoko-browser-session/v1", "state_dir": str(state), "pg_id": pg_id, "db_port": 55432, "db_name": database}
        env_value = f"BANSOKO_TEST_DATABASE_URL={dsn}\nBANSOKO_TEST_DB_DISPOSABLE=1\nBANSOKO_E2E_REAL_EFFECT=1\n"
        mapping = {"Id": pg_id, "State": {"Running": True}, "HostConfig": {"NetworkMode": "default"}, "NetworkSettings": {"Ports": {"5432/tcp": [{"HostIp": "127.0.0.1", "HostPort": "55432"}]}}}
        release = {"type": "release", "id": "release-1", "state": "waiting_metadata", "version": 1, "updated_at": "2026-09-26T00:00:00Z", "fence_epoch": None}
        owners = [release, {**release, "type": "managed_anime", "id": "anime-1", "state": None}, {**release, "type": "lifecycle", "id": "anime-1", "state": "active", "fence_epoch": 1}, {**release, "type": "rss_source", "id": "source-1", "state": None}]
        target = {"managed_id": "anime-1", "source_id": "source-1", "release_id": "release-1"}
        digest = "sha256:" + "a" * 64
        candidate = {"id": "candidate-1", "version": 1, "updated_at": release["updated_at"], "status": "working", "activated_managed_anime_id": None, "current_revision": 1, "current_revision_digest": digest, "draft_digest": digest}
        revision = {"candidate_id": "candidate-1", "revision": 1, "draft_digest": digest, "effective_digest": digest, "parser_policy_digest": digest, "created_at": release["updated_at"]}
        result = {"database": database, "read_only": "on", "isolation": "repeatable read", "owners": owners, "work_items": [], "candidates": [candidate], "candidate_revisions": [revision], "target": target, "scoped_owners": owners, "scoped_work_items": []}
        (state / "server.pid").write_text("123\n"); (state / "server.pgid").write_text("123\n")
        events = root / "events"
        def exe(name, code):
            p = fake / name; p.write_text(code); p.chmod(0o700)
        exe("ps", "#!/bin/sh\nprintf '123 123 go run ./cmd/bansoko-server\\n'\n")
        exe("docker", "#!/bin/sh\ncat \"$INSPECT_FILE\"\n")
        exe("psql", '''#!/usr/bin/env python3
import os,sys
from pathlib import Path
sql=sys.stdin.read()
assert 'REPEATABLE READ READ ONLY' in sql and 'COMMIT;' in sql
assert 'SELECT *' not in sql and 'row_to_json' not in sql
assert ":'managed_id'" in sql and ":'release_id'" in sql
assert 'plugin_deliveries p' not in sql  # closed mapping, no whole-row projection
assert "'fence_epoch',q.fence_epoch" in sql
assert 'FROM anime_config_candidate_revisions' in sql
assert "'owner_lifecycle_epoch',w.owner_lifecycle_epoch" in sql
assert "'current_revision',current_revision" in sql
assert "SELECT 'alignment_operation'" in sql and "SELECT 'offset_migration_operation'" in sql
assert os.environ['PGOPTIONS']=='-c default_transaction_read_only=on'
assert 'SECRET_SENTINEL' not in ' '.join(sys.argv)
assert '-f' in sys.argv and '-' in sys.argv
Path(os.environ['EVENTS']).write_text('queried')
if os.environ.get('FAKE_ERROR'):
 print('postgres://SECRET_SENTINEL',file=sys.stderr);sys.exit(19)
print(Path(os.environ['RESULT_FILE']).read_text())
''')
        env = {**os.environ, "PATH": f"{fake}:{os.environ['PATH']}", "INSPECT_FILE": str(root / "inspect"), "RESULT_FILE": str(root / "result"), "EVENTS": str(events)}
        cmd = [sys.executable, str(HELPER), "--state-dir", str(state), "--managed-id", "anime-1", "--source-id", "source-1", "--release-id", "release-1"]
        def reset():
            (state / "state.json").write_text(json.dumps(state_value)); (state / "browser-session.json").write_text(json.dumps(session_value)); (state / "env").write_text(env_value)
            (root / "inspect").write_text(json.dumps(mapping)); (root / "result").write_text(json.dumps(result))
            events.unlink(missing_ok=True)
        def run(ok=False, query=False, args=None, extra=None):
            r = subprocess.run(args or cmd, env={**env, **(extra or {})}, capture_output=True, text=True)
            assert (r.returncode == 0) == ok, (r.returncode, r.stderr)
            assert "SECRET_SENTINEL" not in r.stdout + r.stderr
            assert events.exists() == query
            return r
        reset(); assert json.loads(run(True, True).stdout)["target"] == target
        # Newer Docker names the same default bridge network "bridge".
        reset(); (root / "inspect").write_text(json.dumps({**mapping, "HostConfig": {"NetworkMode": "bridge"}}))
        assert json.loads(run(True, True).stdout)["target"] == target
        # Each guard must reject before query, not merely because fake SQL fails.
        reset(); (state / "env").write_text(env_value.replace("DISPOSABLE=1", "DISPOSABLE=0")); run()
        reset(); (state / "env").write_text(env_value + "BANSOKO_ENVIRONMENT=production\n"); run()
        reset(); (state / "env").write_text(env_value.replace("127.0.0.1", "remote.test")); run()
        reset(); (state / "env").write_text(env_value.replace("55432", "55433")); run()
        reset(); (state / "browser-session.json").write_text(json.dumps({**session_value, "db_name": "different"})); run()
        reset(); (state / "server.pid").write_text("999"); run(); (state / "server.pid").write_text("123")
        for changed in (
            {**mapping, "Id": "b" * 64},
            {**mapping, "State": {"Running": False}},
            {**mapping, "HostConfig": {"NetworkMode": "host"}},
            {**mapping, "HostConfig": {"NetworkMode": "container:" + "c" * 64}},
            {**mapping, "HostConfig": {"NetworkMode": "bansoko-user-network"}},
            {**mapping, "NetworkSettings": {"Ports": {"5432/tcp": [{"HostIp": "0.0.0.0", "HostPort": "55432"}]}}},
            {**mapping, "NetworkSettings": {"Ports": {"5432/tcp": [{"HostIp": "127.0.0.1", "HostPort": "55433"}]}}},
        ):
            reset(); (root / "inspect").write_text(json.dumps(changed)); run()
        reset(); run(args=cmd[:-1] + ["x';DELETE FROM secrets;--"])
        reset(); run(args=cmd[:4] + ["--managed-id", "anime-1"])
        for changed in (
            {**result, "target": None}, {**result, "database": "production"},
            {**result, "read_only": "off"}, {**result, "isolation": "read committed"},
            {**result, "scoped_owners": []},
            {**result, "owners": [{**release, "payload": "SECRET_SENTINEL"}]},
            {**result, "owners": [{**release, "state": "postgres://SECRET_SENTINEL"}]},
            {**result, "owners": [*owners[:2], {**owners[2], "fence_epoch": None}, owners[3]]},
            {**result, "candidate_revisions": []},
            {**result, "candidate_revisions": [revision, revision]},
            {**result, "candidates": [{**candidate, "current_revision": 2}]},
            {**result, "candidate_revisions": [{**revision, "draft_digest": "SECRET_SENTINEL"}]},
        ):
            reset(); (root / "result").write_text(json.dumps(changed)); run(query=True)
        reset(); run(query=True, extra={"FAKE_ERROR": "1"})
        reset(); unscoped = {**result, "target": None, "scoped_owners": None, "scoped_work_items": None}
        (root / "result").write_text(json.dumps(unscoped)); run(True, True, args=cmd[:4])
        # Independently mutate one guard and prove its real-entry counterexample
        # turns green, so rejection is not an unrelated missing prerequisite.
        mutant = root / "mutant.py"
        code = HELPER.read_text().replace('env.get("BANSOKO_TEST_DB_DISPOSABLE") != "1"', 'False')
        mutant.write_text(code)
        reset(); (state / "env").write_text(env_value.replace("DISPOSABLE=1", "DISPOSABLE=0"))
        m = subprocess.run([sys.executable, str(mutant), *cmd[2:]], env=env, capture_output=True, text=True)
        assert m.returncode == 0, m.stderr
    # Ground plugin and owner columns in actual migration text (not invented names).
    baseline = (ROOT / "db/migrations/001_baseline.sql").read_text()
    plugin = baseline.split("CREATE TABLE plugin_deliveries (", 1)[1].split("CREATE TABLE", 1)[0]
    # The converged baseline declares the columns and the primary key separately.
    for column in ("id TEXT NOT NULL", "managed_anime_id TEXT", "state TEXT", "version BIGINT", "updated_at TIMESTAMPTZ"):
        assert column in plugin, column
    assert "CONSTRAINT plugin_deliveries_pkey PRIMARY KEY (id)" in plugin, "plugin_deliveries primary key"
    # The schema the snapshot reads is the baseline plus every forward migration.
    schema = "\n".join(path.read_text() for path in sorted((ROOT / "db/migrations").glob("*.sql")))
    assert_tables_match_baseline(schema)
    print("snapshot offline real-entry guards, transaction/output whitelist, baseline table/column "
          "consistency and mutation control passed; PG SQL execution still required")


KEYWORDS = frozenset({
    "null", "text", "bigint", "integer", "coalesce", "true", "false", "select", "from", "where",
    "and", "or", "in", "as", "case", "when", "then", "else", "end",
})


def baseline_columns(baseline: str) -> dict[str, dict[str, bool]]:
    """table -> column -> NOT NULL, from the frozen baseline migration."""
    tables: dict[str, dict[str, bool]] = {}
    for match in re.finditer(r"CREATE TABLE (?:IF NOT EXISTS )?([a-z_][a-z0-9_]*) \((.*?)\n\);", baseline, re.S):
        columns = tables.setdefault(match.group(1), {})
        for line in match.group(2).splitlines():
            line = line.strip()
            if re.match(r"^(CONSTRAINT|PRIMARY|FOREIGN|UNIQUE|CHECK|LIKE)\b", line, re.I):
                continue
            column = re.match(r"([a-z_][a-z0-9_]*)\s", line)
            if column:
                columns[column.group(1)] = "NOT NULL" in line
    for match in re.finditer(r"ALTER TABLE (?:IF EXISTS )?([a-z_][a-z0-9_]*)\s+ADD COLUMN (?:IF NOT EXISTS )?([a-z_][a-z0-9_]*)", baseline):
        tables.setdefault(match.group(1), {}).setdefault(match.group(2), False)
    # Forward migrations rename columns and tables in order; apply them so the
    # check sees the installed schema, not the frozen baseline names.
    for match in re.finditer(r"ALTER TABLE (?:IF EXISTS )?([a-z_][a-z0-9_]*)\s+RENAME (?:COLUMN ([a-z_][a-z0-9_]*) TO ([a-z_][a-z0-9_]*)|TO ([a-z_][a-z0-9_]*))\s*;", baseline):
        table, old, new, new_table = match.groups()
        if new_table:
            if table in tables:
                tables[new_table] = tables.pop(table)
        elif table in tables and old in tables[table]:
            tables[table][new] = tables[table].pop(old)
    return tables


def referenced_columns(expression: str) -> set[str]:
    """Column names in an expression: psql variables and string literals excluded."""
    without_vars = re.sub(r":'[a-z_]+'", " NULL ", expression)
    without_literals = re.sub(r"'[^']*'", " ", without_vars)
    return {name for name in re.findall(r"[a-z_][a-z0-9_]*", without_literals.lower()) if name not in KEYWORDS}


def assert_tables_match_baseline(baseline: str) -> None:
    """Every snapshot table, column and scope must exist in the current schema.

    Schema convergences (S7 collapsed anime_config_owners into
    anime_business_configs) otherwise surface only as an opaque
    observer_command_failed inside a VM live spec.
    """
    spec = importlib.util.spec_from_file_location("db_snapshot_helper", HELPER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    tables = baseline_columns(baseline)
    for kind, (table, pk, state, version) in module.TABLES.items():
        columns = tables.get(table)
        assert columns is not None, f"{kind}: table {table} is not in the baseline"
        expressions = {"id": pk, "state": state, "version": version,
                       "updated_at": module.UPDATED_AT.get(kind, "updated_at")}
        if kind == "lifecycle":
            expressions["fence_epoch"] = "fence_epoch"
        for role, expression in expressions.items():
            for column in referenced_columns(expression):
                assert column in columns, f"{kind}: {table}.{column} ({role}) is not in the baseline"
        # The output guard requires a positive integer version, so an axis whose
        # column is nullable must project it instead of selecting it raw.
        if referenced_columns(version) and not all(columns[c] for c in referenced_columns(version)):
            assert "coalesce" in version.lower(), f"{kind}: nullable version {version} must be projected"
        # updated_at must never be NULL: the guard requires a timestamp.
        updated = referenced_columns(module.UPDATED_AT.get(kind, "updated_at"))
        assert updated and any(columns[column] for column in updated), f"{kind}: updated_at can be NULL"
    for kind, scope in module.SCOPES.items():
        table = module.TABLES[kind][0]
        columns = tables[table]
        if "select" in scope.lower():
            continue
        for column in referenced_columns(scope):
            assert column in columns, f"scope {kind}: {table}.{column} is not in the baseline"
    for table, expected in {
        "anime_config_candidates": ("id", "version", "updated_at", "status", "activated_managed_anime_id", "current_revision", "current_revision_digest", "draft_digest"),
        "anime_config_candidate_revisions": ("candidate_id", "revision", "draft_digest", "effective_digest", "parser_policy_digest", "created_at"),
        "work_items": ("id", "owner_type", "owner_id", "kind", "state", "fencing_token", "attempt", "updated_at", "managed_anime_id", "owner_lifecycle_epoch", "external_effect_admitted_at"),
        "releases": ("id", "rss_source_id"),
        "rss_sources": ("id", "subscription_id"),
        "subscriptions": ("id", "managed_anime_id"),
    }.items():
        columns = tables.get(table)
        assert columns is not None, f"SQL body: table {table} is not in the baseline"
        for column in expected:
            assert column in columns, f"SQL body: {table}.{column} is not in the baseline"


if __name__ == "__main__":
    main()
