#!/usr/bin/env python3
"""Execute comparator counterexamples against isolated TS mutants, offline only."""
import json
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FRONTEND = ROOT / "frontend"
HELPER = FRONTEND / "e2e/disposable-db-snapshot.ts"
TEST = FRONTEND / "scripts/disposable-db-snapshot.test.ts"


def main():
    original = HELPER.read_text()
    revision_start = original.index("  const revisionsBefore =")
    revision_end = original.index('  for (const field of ["owners"', revision_start)
    mutants = {
        "ignore-revision-set": original[:revision_start] + original[revision_end:],
        "ignore-lifecycle-fence": original.replace(
            'for (const [id, row] of left) expect(right.get(id), `${field} unchanged row ${id}`).toEqual(row);',
            'for (const [id, row] of left) { const { fence_epoch: ignoredBefore, ...a } = row as OwnerRow; const { fence_epoch: ignoredAfter, ...b } = right.get(id)! as OwnerRow; expect(b).toEqual(a); }'),
        "count-only-owner-workitems": original.replace(
            'expect([...right.keys()].sort(), `${field} primary-key set`).toEqual([...left.keys()].sort());',
            'expect(right.size).toBe(left.size);').replace(
            'for (const [id, row] of left) expect(right.get(id), `${field} unchanged row ${id}`).toEqual(row);', ''),
        "ignore-candidate-pointer": original.replace(
            '["id", "version", "updated_at", "status", "activated_managed_anime_id", "current_revision", "current_revision_digest", "draft_digest"] as const',
            '["id", "version", "updated_at"] as const'),
    }
    for label, mutant in mutants.items():
        assert mutant != original, f"mutation anchor missing: {label}"
    with tempfile.TemporaryDirectory(prefix="snapshot-mutants-", dir=FRONTEND / "scripts") as td:
        temp = Path(td)
        (temp / "test.test.ts").write_text(TEST.read_text().replace('../e2e/disposable-db-snapshot', './observer'))
        config = temp / "vitest.config.ts"
        config.write_text('import { defineConfig } from "vitest/config";\nexport default defineConfig({test:{include:[' + json.dumps(str(temp / "test.test.ts")) + '],environment:"node",globals:false,poolOptions:{forks:{minForks:1,maxForks:1}}}});\n')
        for label, content in [("positive-control", original), *mutants.items()]:
            (temp / "observer.ts").write_text(content)
            result = subprocess.run(["pnpm", "exec", "vitest", "run", "--config", str(config)], cwd=FRONTEND, capture_output=True, text=True, timeout=60)
            text = result.stdout + result.stderr
            if label == "positive-control":
                assert result.returncode == 0 and "10 passed" in text, text
                print("positive-control: 10 tests passed")
            else:
                # Collection/compiler failure is not a killed behavioral mutant.
                assert result.returncode != 0 and "AssertionError" in text and "failed" in text and "Test Files" in text, text
                assert "Failed Suites" not in text, text
                print(f"rejected behavioral mutant: {label}")
    print("comparator mutation controls passed; no browser or DB execution")


if __name__ == "__main__":
    main()
