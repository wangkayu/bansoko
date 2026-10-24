#!/usr/bin/env python3
"""Exercise the exact transformation entrypoint used by the Docker fixture."""
from __future__ import annotations
import pathlib
import subprocess
import sys
import tempfile

root = pathlib.Path(__file__).resolve().parents[2]
entry = root / "scripts/ci/transform-embedded-compose.py"
source = (root / "deployment/maintainer/docker-compose.production.yml").read_text()
image_template = "${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko}@${BANSOKO_BUNDLE_IMAGE_DIGEST:?BANSOKO_BUNDLE_IMAGE_DIGEST (sha256:...) must be injected by the builder}"
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
print("embedded compose transform fixture tests passed: service-scoped app replacement, non-image sentinel preserved, missing image rejected")
