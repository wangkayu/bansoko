#!/usr/bin/env python3
"""Exercise the exact transformation entrypoint used by the Docker fixture."""
from __future__ import annotations
import pathlib
import re
import subprocess
import sys
import tempfile

root = pathlib.Path(__file__).resolve().parents[2]
entry = root / "scripts/ci/transform-embedded-compose.py"
source = (root / "deployment/maintainer/docker-compose.production.yml").read_text()
image_template = "${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko-builds}@${BANSOKO_BUNDLE_IMAGE_DIGEST:?BANSOKO_BUNDLE_IMAGE_DIGEST (sha256:...) must be injected by the builder}"
sentinel = "# sentinel: registry.example.test/acme/app@must-not-change"
for broken in [source.replace("  app:\n", "  app:\n" + sentinel + "\n", 1), source.replace(image_template, image_template, 1)]:
    with tempfile.TemporaryDirectory() as td:
        src, dst = pathlib.Path(td) / "src.yml", pathlib.Path(td) / "dst.yml"
        src.write_text(broken)
        result = subprocess.run([sys.executable, str(entry), str(src), str(dst), "fixture_updates", "fixture-image:tag"], capture_output=True, text=True)
        assert result.returncode == 0, result.stderr
        rendered = dst.read_text()
        assert rendered.count("    image: fixture-image:tag\n") == 1
        if sentinel in broken:
            assert sentinel in rendered, "non-image sentinel was modified or removed"
        assert image_template not in rendered
# Missing one required image field must reject rather than silently succeeding.
with tempfile.TemporaryDirectory() as td:
    src, dst = pathlib.Path(td) / "src.yml", pathlib.Path(td) / "dst.yml"
    src.write_text(source.replace("    image: " + image_template + "\n", "    image: postgres:17-alpine\n", 1))
    result = subprocess.run([sys.executable, str(entry), str(src), str(dst), "fixture_updates", "fixture-image:tag"], capture_output=True, text=True)
    assert result.returncode != 0, "missing required image field unexpectedly passed"
# Exercise the fixture's actual second-stage patch, not just the image rewrite.
# The maintained deployment has one config bind, not a separate updates volume.
fixture = (root / "scripts/ci/embedded-update-docker-fixture.sh").read_text()
patch = re.search(r'python3 - "\$compose"[^\n]*<<\'PY\'\n(.*?)\nPY', fixture, re.S)
assert patch, "fixture compose patch was not found"
for scenario, local_cli, checkpoint, legacy_base in [("normal", "0", "", "0"), ("normal", "1", "", "0"), ("crash-after-backup", "0", "pending_backup", "0"), ("normal", "1", "", "1")]:
    with tempfile.TemporaryDirectory() as td:
        dst = pathlib.Path(td) / "compose.yml"
        subprocess.run([sys.executable, "-I", str(entry), str(root / "deployment/maintainer/docker-compose.production.yml"), str(dst), "fixture_updates", "fixture-image:tag"], check=True)
        result = subprocess.run([sys.executable, "-I", "-c", patch.group(1), str(dst), "http://host.docker.internal:12345/update-channel", scenario, checkpoint, "fixture-run", "fixture_updates", local_cli, legacy_base], capture_output=True, text=True)
        assert result.returncode == 0, result.stderr
        rendered = dst.read_text()
        assert "        target: /config\n" in rendered
        if legacy_base == "1":
            assert "BANSOKO_UPDATES_ROOT: /config/updates" in rendered
        else:
            assert "BANSOKO_UPDATES_ROOT:" not in rendered
        assert rendered.count("bansoko.update-e2e.run: fixture-run") == 3, "both services and the database volume must carry the cleanup label"
        assert "source: updates" not in rendered and "target: /var/lib/bansoko/updates" not in rendered
        app = rendered.split("  app:\n", 1)[1]
        paused_backup = local_cli == "1" or scenario == "crash-after-backup"
        assert ('    restart: "no"' in app) == paused_backup
        if paused_backup:
            # Marker consumption creates pending_migration directly, bypassing
            # journal-transition hooks. Use the real post-dump marker checkpoint.
            assert 'BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER: "pending_backup"' in app
            marker_source = (root / "internal/runtime/bootstrap/marker.go").read_text()
            assert 'updateapp.ApplyFixtureStop("pending_backup")' in marker_source
            assert 'BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER: "pending_migration"' not in app
# Run the exact legacy source export under a private runner umask. Source
# permissions are copied into the image and must remain readable after PUID drop.
archive = re.search(r'^\s*(.*?) \|\| fail_blocked "legacy base source archive failed"', fixture, re.M)
assert archive, "legacy source export command was not found"
with tempfile.TemporaryDirectory() as td:
    result = subprocess.run(["bash", "-c", 'set -euo pipefail; umask 077; root="$1"; legacy_commit=HEAD; legacy_root="$2";\n' + archive.group(1), "legacy-export-test", str(root), td], capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    migrations = pathlib.Path(td) / "db/migrations"
    assert migrations.stat().st_mode & 0o005 == 0o005, "migration directory is not readable/searchable after privilege drop"
    for path in migrations.glob("*.sql"):
        assert path.stat().st_mode & 0o004, f"migration is unreadable after privilege drop: {path.name}"
print("embedded compose transform fixture tests passed: image replacement, single config bind, backup checkpoint modes, legacy source permissions and missing image refusal")
