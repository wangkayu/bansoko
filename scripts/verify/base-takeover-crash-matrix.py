#!/usr/bin/env python3
"""Exercise bootstrap's real, immutable base and overlay binaries against isolated PG17."""

import hashlib
import json
import os
import secrets
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PG_IMAGE = "postgres:17-alpine"
OVERLAY = "fixture-overlay"
BASE = "fixture-base"
NEXT_MIGRATION = "999_fixture_takeover"


def run(*args, timeout=120, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, text=True, timeout=timeout, **kwargs).stdout


def output(*args, **kwargs):
    return run(*args, stdout=subprocess.PIPE, **kwargs).strip()



def command_json(*args):
    try:
        return json.loads(output(*args))
    except json.JSONDecodeError as err:
        raise AssertionError(f"{args[0]} did not return a JSON document") from err

def await_value(read, timeout=40):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        value = read()
        if value:
            return value
        time.sleep(0.15)
    raise AssertionError("fixture did not reach the expected state")


def build_fixture(work, image):
    commit = output("git", "rev-parse", "HEAD")
    arch = output("docker", "version", "--format", "{{.Server.Arch}}")
    source = ROOT / "db/migrations"
    old_schema = sorted(source.glob("*.sql"))[-1].stem
    context = work / "image"
    context.mkdir()
    shutil.copytree(source, context / "migrations")
    (context / "migrations" / f"{NEXT_MIGRATION}.sql").write_text(
        "CREATE TABLE public.fixture_added_by_takeover (id bigint PRIMARY KEY);\n"
    )
    frontend = context / "frontend"
    frontend.mkdir()
    (frontend / "index.html").write_text("<html><title>Takeover fixture</title></html>\n")
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch)
    # The base binary knows the added relation; the overlay binary does not.
    # Only the fixture compiler sees this replacement; the worktree stays intact.
    runner_name = ROOT / "internal/runtime/migrations/runner.go"
    runner = work / "fixture_runner.go"
    inventory = "var knownRelations = map[string]string{"
    original_runner = runner_name.read_text()
    assert original_runner.count(inventory) == 1
    runner.write_text(original_runner.replace(inventory, inventory + '\n\t"fixture_added_by_takeover": "r",'))
    build_overlay = work / "go-overlay.json"
    probe_name = ROOT / "internal/runtime/bootstrap/private_probe.go"
    probe = work / "fixture_probe.go"
    probe_entry = "func RunPrivateReadiness(report func(context.Context) readiness.Report) int {"
    original_probe = probe_name.read_text()
    assert original_probe.count(probe_entry) == 1
    probe.write_text(original_probe.replace(probe_entry, probe_entry + '''
    if os.Getenv("BANSOKO_FIXTURE_HOLD_PROBE") == "1" {
        os.Stderr.WriteString("fixture: private probe waiting\\n")
        for { time.Sleep(time.Second) }
    }'''))
    build_overlay.write_text(json.dumps({"Replace": {
        str(runner_name): str(runner), str(probe_name): str(probe),
    }}))
    for identity, sequence, schema, target in [
        (OVERLAY, 2, old_schema, work / "overlay"),
        (BASE, 3, NEXT_MIGRATION, context / "bansoko-server"),
    ]:
        fields = {"Version": f"1.0.{sequence}", "Commit": commit, "BuildID": identity,
                  "APIVersion": "v2", "SchemaVersion": schema, "Series": "gh:wangkayu/bansoko:v1",
                  "Lane": "stable", "ReleaseSequence": str(sequence), "BuildDirty": "false"}
        if identity == BASE:
            fields["ContainerAnchor"] = "docker-v1"
        flags = "-s -w " + " ".join(f"-X bansoko/internal/runtime/buildinfo.{k}={v}" for k, v in fields.items())
        options = ["-overlay", str(build_overlay)] if identity == BASE else []
        run("go", "build", *options, "-trimpath", "-ldflags", flags, "-o", str(target),
            "./cmd/bansoko-server", env=env)
    run("docker", "build", "-q", "-t", image, "-f", "-", str(context), input=f"""FROM {PG_IMAGE}
COPY --chmod=0555 bansoko-server /app/bansoko-server
COPY --chmod=0755 migrations /app/db/migrations
COPY --chmod=0755 frontend /app/frontend/dist
ENTRYPOINT [\"/app/bansoko-server\"]
""")
    attested = command_json("docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}",
                            "--entrypoint", "/usr/bin/env", image, "-i", "/app/bansoko-server", "version", "--json")
    assert attested["build_id"] == BASE and attested["release_sequence"] == 3, attested
    payload = work / ".embedded-payload.tar.gz"
    packed = command_json(
        "go", "run", "./cmd/bansoko-release", "pack-payload",
        "--server-binary", str(work / "overlay"), "--migrations-dir", str(source),
        "--defaults-dir", str(ROOT / "deployment/defaults"), "--frontend-dist", str(frontend),
        "--output", str(payload), "--source-date-epoch", "1756000000",
        "--release-id", OVERLAY, "--build-id", OVERLAY, "--version", "1.0.2",
        "--source-commit", commit, "--target-schema-version", old_schema,
        "--platform", "linux", "--architecture", arch,
        "--series", "gh:wangkayu/bansoko:v1", "--lane", "stable", "--release-sequence", "2",
    )
    updates = work / "seed"
    tree = updates / "releases" / OVERLAY
    tree.mkdir(parents=True)
    with tarfile.open(payload) as archive:
        archive.extractall(tree, filter="data")
    shutil.copy2(payload, tree / payload.name)
    binary = work / "overlay"
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    (tree / ".release-manifest.json").write_text(json.dumps({
        "schema_version": "release-manifest-1", "series": "gh:wangkayu/bansoko:v1", "channel": "stable",
        "release_sequence": 2, "release_id": OVERLAY, "version": "1.0.2", "source_commit": commit,
        "target_migration_version": old_schema, "api_contract_version": "v2", "artifact_digest": digest,
        "artifacts": [{"platform": "linux", "architecture": arch, "digest": digest, "size": binary.stat().st_size}],
        "embedded_update": {"format_version": "embedded-update-v1", "platform": "linux", "architecture": arch,
                            "payload_digest": packed["digest"], "payload_size": packed["size"],
                            "bootstrap_min_version": "embedded-bootstrap-unified", "min_image_version": "base-v1",
                            "target_release_id": OVERLAY, "target_version": "1.0.2",
                            "target_schema_version": old_schema, "release_sequence": 2},
    }))
    (updates / "provenance").mkdir()
    (updates / "provenance" / f"{OVERLAY}.json").write_text(json.dumps({
        "schema_version": 1, "release_id": OVERLAY, "payload_digest": packed["digest"],
        "producing_operation_id": "fixture-overlay-install", "expected_process_build_id": OVERLAY,
    }))
    (updates / "current").write_text(OVERLAY + "\n")
    return updates


def scenario(image, seed, checkpoint, mode, rollback, signal=None):
    name = image + "-" + checkpoint.replace("_", "-")
    pg, app = name + "-pg", name + "-app"
    volume = name + "-config"
    uid_gid = f"{os.getuid()}:{os.getgid()}"
    mount = ["-v", f"{volume}:/config"]
    db_secret = secrets.token_hex(24)
    db_url = f"postgres://fixture:{db_secret}@{pg}/takeover_fixture?sslmode=disable"
    extra_env = dict(os.environ, POSTGRES_PASSWORD=db_secret, BANSOKO_DATABASE_URL=db_url)
    app_args = ["--network", name, *mount, "--user", uid_gid,
                "-e", "BANSOKO_DATABASE_URL", "-e", "BANSOKO_CONFIG_DIR=/config", "-e", "BANSOKO_ENVIRONMENT=test",
                "-e", f"BANSOKO_UPDATE_MODE={mode}",
                "-e", "BANSOKO_FRONTEND_DIR=/app/frontend/dist"]

    def config_command(*args):
        return output("docker", "run", "--rm", *mount, "--entrypoint", args[0], image, *args[1:])

    def journal():
        return command_json("docker", "run", "--rm", *mount, "--entrypoint", "cat", image,
                            "/config/updates/journal.json")["record"]

    def sql(query, database="takeover_fixture"):
        return output("docker", "exec", pg, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "fixture", "-d", database, "-c", query)

    def start(stop="", hold_probe=False):
        run("docker", "run", "-d", "--name", app, *app_args, "-p", "127.0.0.1::8080",
            "-e", f"BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER={stop}",
            "-e", "BANSOKO_EMBEDDED_FIXTURE_FAIL_READINESS=" + ("1" if rollback else "0"),
            "-e", "BANSOKO_FIXTURE_HOLD_PROBE=" + ("1" if hold_probe else "0"),
            image, "bootstrap", env=extra_env,
            stdout=subprocess.DEVNULL)
        return output("docker", "port", app, "8080/tcp")

    def await_ready(address):
        def probe():
            try:
                with urllib.request.urlopen(f"http://{address}/api/v1/readyz", timeout=2) as resp:
                    report = json.load(resp)
                    return report if report["ready"] else None
            except urllib.error.HTTPError as err:
                assert err.code == 503, err.code
                assert json.load(err)["reason"] == "not_ready:maintenance"
            except (OSError, urllib.error.URLError):
                pass
            if output("docker", "inspect", "--format", "{{.State.Running}}", app) == "false":
                raise AssertionError("bootstrap exited before becoming ready")
        return await_value(probe)

    try:
        run("docker", "network", "create", name, stdout=subprocess.DEVNULL)
        run("docker", "volume", "create", volume, stdout=subprocess.DEVNULL)
        run("docker", "run", "--rm", *mount, "-v", f"{seed}:/seed:ro", "--entrypoint", "sh", image,
            "-ec", 'cp -a /seed /config/updates; chown -R "$1" /config', "sh", uid_gid)
        run("docker", "run", "-d", "--name", pg, "--network", name, "-e", "POSTGRES_PASSWORD",
            "-e", "POSTGRES_USER=fixture", "-e", "POSTGRES_DB=takeover_fixture", PG_IMAGE,
            env=extra_env, stdout=subprocess.DEVNULL)
        await_value(lambda: subprocess.run(["docker", "exec", pg, "pg_isready", "-h", "127.0.0.1", "-U", "fixture"],
                                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5).returncode == 0)
        old = f"/config/updates/releases/{OVERLAY}"
        run("docker", "run", "--rm", *app_args, "-e", f"BANSOKO_SCHEMA_DIR={old}/db/migrations",
            "--entrypoint", f"{old}/bansoko-server", image, "migrate", env=extra_env)
        install_id = sql("SELECT install_id FROM instance_bootstrap")
        sql("INSERT INTO managed_anime (id,title) VALUES ('takeover-fixture','Preserved title')")
        address = start(checkpoint if signal is None else "", hold_probe=signal is not None)
        if signal or checkpoint == "base_ready":
            def maintenance():
                try:
                    with urllib.request.urlopen(f"http://{address}/api/v1/readyz", timeout=2):
                        raise AssertionError("public readiness succeeded before takeover promotion")
                except urllib.error.HTTPError as err:
                    assert err.code == 503, err.code
                    report = json.load(err)
                    assert report["reason"] == "not_ready:maintenance"
                    return report.get("deployment_identity", {}).get("takeover_operation_id")
                except OSError:
                    return False
            await_value(maintenance, timeout=5)
        def logs():
            return subprocess.run(["docker", "logs", app], check=True, text=True,
                                  capture_output=True, timeout=10).stderr
        if signal:
            await_value(lambda: "fixture: private probe waiting" in logs())
            assert journal()["step"] == "migration_succeeded"
            sockets = output("docker", "exec", app, "cat", "/proc/net/unix")
            assert "@bsk-probe-" in sockets
            run("docker", "kill", "--signal", signal, app, stdout=subprocess.DEVNULL)
            assert output("docker", "wait", app, timeout=10) == "1"
        else:
            assert output("docker", "wait", app, timeout=55) == "70"
            assert f'durable checkpoint "{checkpoint}"' in logs()
        interrupted = journal()
        assert interrupted["operation_kind"] == "base_takeover"
        if signal:
            assert interrupted["step"] == "migration_succeeded"
            assert not config_command("find", "/config", "-type", "s")
        if checkpoint == "restore_live_quarantined":
            assert sql("SELECT count(*) FROM pg_database WHERE datname='takeover_fixture'", "postgres") == "0"
        run("docker", "rm", "-v", app, stdout=subprocess.DEVNULL)
        report = await_ready(start())
        record = journal()
        operation = record["operation_id"]
        assert operation == interrupted["operation_id"]
        assert record["step"] == ("rolled_back" if rollback else "succeeded"), record["step"]
        selected = report["deployment_identity"]
        assert selected["selected_source"] == ("overlay" if rollback else "base"), selected
        assert selected["takeover_operation_id"] == operation, selected
        config_command("sh", "-ec", "test ! -e /config/updates/pending.json")
        assert sql("SELECT install_id FROM instance_bootstrap") == install_id
        assert sql("SELECT title FROM managed_anime WHERE id='takeover-fixture'") == "Preserved title"
        assert sql("SELECT to_regclass('public.fixture_added_by_takeover') IS NOT NULL") == ("f" if rollback else "t")
        assert sql("SELECT op_id || '|' || admitted_by || '|' || sequence || '|' || manifest_digest || '|' || state "
                   "FROM embedded_update_locks WHERE released_at IS NOT NULL") == (
            f"{operation}|embedded|3|{record['manifest_digest']}|" + ("failed" if rollback else "settled"))
        if not rollback:
            config_command("sh", "-ec", 'test ! -e /config/updates/current; test -d "$1/releases/$2"; test -f "$1/provenance/$2.json"',
                           "sh", f"/config/updates/retired/{operation}", OVERLAY)
        else:
            assert config_command("cat", "/config/updates/current") == OVERLAY
            run("docker", "restart", app, stdout=subprocess.DEVNULL)
            steady = await_ready(output("docker", "port", app, "8080/tcp"))
            assert steady["deployment_identity"]["selection_reason_code"] == "takeover_rolled_back"
            assert journal()["operation_id"] == operation
        print(json.dumps({"checkpoint": checkpoint, "mode": mode, "step": record["step"], "passed": True}), flush=True)
    except Exception:
        subprocess.run(["docker", "logs", "--tail", "35", app], check=False, timeout=10)
        raise
    finally:
        for cmd in [("rm", "-fv", app), ("rm", "-fv", pg), ("network", "rm", name), ("volume", "rm", volume)]:
            subprocess.run(["docker", *cmd], check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=20)
        for container in (app, pg):
            if output("docker", "ps", "-aq", "--filter", f"name=^/{container}$"):
                raise AssertionError(f"fixture leaked container {container}")
        assert not output("docker", "network", "ls", "-q", "--filter", f"name=^{name}$")
        assert not output("docker", "volume", "ls", "-q", "--filter", f"name=^{volume}$")


def main():
    if os.environ.get("BANSOKO_CRASH_MATRIX") != "1" or os.environ.get("BANSOKO_ENVIRONMENT") == "production":
        raise SystemExit("refusing: set BANSOKO_CRASH_MATRIX=1 outside production")
    image = "bansoko-takeover-" + secrets.token_hex(6)
    with tempfile.TemporaryDirectory(prefix="bsk-takeover-") as directory:
        work = Path(directory)
        try:
            seed = build_fixture(work, image)
            for checkpoint, mode, rollback in [
                ("takeover_fence_inserted", "embedded", False),
                ("takeover_migration_committed", "manual", False),
                ("base_ready", "manual", False),
                ("takeover_pointer_unlinked", "embedded", False),
                ("takeover_fence_closed", "manual", False),
                ("succeeded", "embedded", False),
                ("restore_before_create", "manual", True),
                ("restore_live_quarantined", "embedded", True),
                ("restore_ready_renamed", "manual", True),
                ("database_restored", "embedded", True),
            ]:
                scenario(image, seed, checkpoint, mode, rollback)
            scenario(image, seed, "signal_term", "manual", False, signal="TERM")
            scenario(image, seed, "signal_int", "embedded", False, signal="INT")
        finally:
            subprocess.run(["docker", "image", "rm", image], check=False,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=20)


if __name__ == "__main__":
    main()
