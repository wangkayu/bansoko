#!/usr/bin/env python3
"""Exercise the release validator's actual Ruby production-compose block."""
import pathlib
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]


class ReleaseComposeTests(unittest.TestCase):
    def test_single_app_service_owns_migration_and_derived_secrets(self):
        script = (ROOT / "scripts/ci/check-release.sh").read_text()
        body = script.split("ruby -ryaml -e '", 1)[1].split("' \"$compose\"", 1)[0]
        source = (ROOT / "deployment/maintainer/docker-compose.production.yml").read_text()

        def check(text):
            with tempfile.TemporaryDirectory() as temp:
                path = pathlib.Path(temp) / "compose.yml"
                path.write_text(text)
                return subprocess.run(["ruby", "-ryaml", "-e", body, str(path)], capture_output=True, text=True)

        positive = check(source)
        self.assertEqual(positive.returncode, 0, positive.stderr)
        mode = "      BANSOKO_UPDATE_MODE: embedded\n"
        self.assertEqual(source.count(mode), 1)
        migrate = ("  migrate:\n"
                   "    image: registry.example.test/owner/bansoko@sha256:" + "a" * 64 + "\n"
                   "    entrypoint: [\"/app/bootstrap\", \"migrate\"]\n")
        for name, mutated, reason in [
            ("migrate service", source.replace("  app:\n", migrate + "  app:\n", 1), "retired migrate service"),
            ("manual mode", source.replace(mode, "      BANSOKO_UPDATE_MODE: manual\n"), "BANSOKO_UPDATE_MODE=embedded"),
            ("session secret", source.replace(mode, mode + "      BANSOKO_SESSION_SECRET: x\n"), "generated secrets"),
            ("second dsn", source.replace(mode, mode + "      BANSOKO_MIGRATION_DATABASE_URL: x\n"), "derived connections"),
            ("entrypoint", source.replace("    container_name: bansoko-app\n",
                                          "    container_name: bansoko-app\n    entrypoint: [\"/app/bansoko-server\"]\n", 1), "ENTRYPOINT"),
        ]:
            self.assertNotEqual(mutated, source, name)
            result = check(mutated)
            with self.subTest(name=name):
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(reason, result.stderr)


if __name__ == "__main__":
    unittest.main()
