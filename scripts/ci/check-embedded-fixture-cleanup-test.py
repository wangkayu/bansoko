#!/usr/bin/env python3
"""Execute the real cleanup function across pre-result failures (mock Docker only)."""
import pathlib
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]


class CleanupTests(unittest.TestCase):
    def test_no_result_failure_cleans_auth_and_keeps_diagnostics(self):
        source = (ROOT / "scripts/ci/embedded-update-docker-fixture.sh").read_text()
        body = source.split("cleanup() {", 1)[1].split("\ntrap cleanup EXIT", 1)[0]
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            work = root / "work"
            work.mkdir()
            (work / "owner.json").write_text("test-only-secret")
            (work / "compose.log").write_text("failure diagnostics")
            evidence = root / "evidence"
            evidence.mkdir()
            shell = f'''set -euo pipefail
workdir={work}
evidence_dir={evidence}
result={work}/missing.json
log={work}/compose.log
compose={work}/compose.yml
project=test-fixture
volume=test-volume
keep=0
docker() {{ case "$1 $2" in "volume inspect") return 1;; *) return 0;; esac; }}
cleanup() {{{body}
cleanup
'''
            subprocess.run(["bash", "-c", shell], check=True)
            self.assertFalse(work.exists())
            self.assertEqual((evidence / "fixture-last.log").read_text(), "failure diagnostics")


if __name__ == "__main__":
    unittest.main()
