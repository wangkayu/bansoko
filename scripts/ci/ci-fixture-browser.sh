#!/usr/bin/env bash
# Fixture Browser lane: fixed-Chromium Playwright over the fixture-mode
# frontend (BANSOKO_E2E_FIXTURES=1, no auth, no backend). Covers desktop + mobile
# deterministic key UX and error states. Any non-allowed skip fails the gate.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root/frontend"

# The fixture lane runs against its own local fixture-mode dev server only.
# An inherited BANSOKO_TEST_BASE_URL would redirect baseURL at some other
# (possibly live) instance, so refuse to run instead of testing the wrong
# target. playwright.config.ts ignores the variable in fixture mode too;
# this guard makes the conflict loud at the lane boundary.
if [ -n "${BANSOKO_TEST_BASE_URL:-}" ]; then
  # Never echo the value: base URLs may carry embedded credentials.
  echo "BANSOKO_TEST_BASE_URL is set; the fixture lane must never target an external server; unset it" >&2
  exit 1
fi

# Session scripts redirect the auth artifact paths (storage state + skip-auth
# marker) via these variables for live lanes. Fixture mode has no real auth,
# but auth.setup.ts still runs (it short-circuits in fixture mode) and would
# otherwise read and write the default shared playwright/.auth/ tree. Point
# both variables at a lane-private temp dir instead: inherited live-lane
# redirections are overridden, the shared artifacts are never touched, and
# stale shared state cannot leak into this lane. The EXIT trap removes the
# dir while preserving the lane's exit status.
fixture_auth_dir="$(mktemp -d /tmp/bansoko-fixture-auth.XXXXXX)"
export BANSOKO_E2E_AUTH_STATE_PATH="${fixture_auth_dir}/state.json"
export BANSOKO_E2E_AUTH_MARKER_PATH="${fixture_auth_dir}/skip-auth"
cleanup() {
  rc=$?
  rm -rf "$fixture_auth_dir"
  exit "$rc"
}
trap cleanup EXIT

if [ ! -d node_modules ]; then
  pnpm install --frozen-lockfile
fi

# When the job runs on the prebuilt CI image (BANSOKO_CI_BROWSERS_PREINSTALLED=1,
# see .github/workflows/ci.yml), system deps and browsers are baked in: the plain
# install is a fast no-op and only downloads browsers after a locked-version
# bump the image tag has not caught up with. Otherwise (local dev, plain
# node image) install browsers + system deps via apt. Fail loudly when the
# install fails instead of running the suite without browsers.
if [ "${BANSOKO_CI_BROWSERS_PREINSTALLED:-}" = "1" ]; then
  pnpm exec playwright install chromium webkit \
    || { echo "playwright browser install failed" >&2; exit 1; }
else
  pnpm exec playwright install chromium webkit --with-deps \
    || pnpm exec playwright install chromium webkit \
    || { echo "playwright browser install failed" >&2; exit 1; }
fi

echo "== fixture browser (desktop + mobile; only declared design-time viewport skips allowed) =="
# Fixture mode has no auth gate: auth.setup.ts short-circuits before reading
# any credentials and writes its empty storage state (and clears the skip
# marker) inside the lane-private temp dir exported above. No leftover shared
# skip-auth marker can silently disable specs, and inherited
# BANSOKO_E2E_USERNAME/BANSOKO_E2E_PASSWORD values are simply never consulted here.
# Design-time viewport skips are declared ONLY inside the validator below, as
# exact (spec file, test title, project) triples. There is deliberately no
# environment override: an external BANSOKO_E2E_SKIP_ALLOW could widen or replace
# the built-in set, so the built-in allowlist is the single source of truth.

# Only fixtureOnly specs run in the fixture lane. auth.spec.ts, live-smoke.spec.ts,
# catalog.spec.ts, mobile.spec.ts, custom-rss-live.spec.ts and settings-live.spec.ts
# are live/auth-gated specs (they need a real backend or login state); they are
# covered by the authenticated lanes instead. Any skip inside the fixture specs
# beyond the design-time viewport skips declared in the validator below is a
# real regression.
fixture_specs=(
  e2e/anime-grid.spec.ts
  e2e/rss-oneoff.spec.ts
  e2e/oneshot-adopt-fixture.spec.ts
  e2e/settings.spec.ts
  e2e/qb-manager.spec.ts
  e2e/execution-records.spec.ts
)

report="$(mktemp)"
status=0
BANSOKO_E2E_FIXTURES=1 pnpm exec playwright test \
  "${fixture_specs[@]}" \
  --project=desktop --project=mobile \
  --workers=1 \
  --forbid-only \
  --reporter=json \
  >"$report" || status=$?

# Fail on any skipped test that is not an explicitly declared design-time
# skip, and on any coverage gap: a fixture spec missing from the report
# entirely (wrong testDir, filter typo, crashed worker) or lacking test cases
# for a required project must fail the lane instead of silently passing as
# "no skipped tests". The validator ALWAYS runs and is fail-closed: any parse
# or schema anomaly (missing suites/spec fields, missing projectName) fails
# the lane too. Skips are matched exactly by (spec file, test title, project
# name) as a multiset — each declared triple covers at most one actual skip,
# so repeated identical undeclared skips still fail; leaf-title-only matching
# cannot distinguish desktop from mobile and is not accepted. The expected
# spec list is passed as argv from the fixture_specs array above so the
# runner and the validator cannot drift.
skip_summary="$(python3 - "$report" "${fixture_specs[@]##*/}" <<'PY'
import json
import os
import sys
from collections import Counter

# Built-in design-time viewport skips: exact (spec file, test title, project)
# triples, compared as a MULTISET: each declared triple may cover at most one
# actual skip, so repeating an identical undeclared skip still fails. The only
# permitted skips are the ones written here; there is no environment override
# by design.
ALLOWED_SKIPS = {
    # Playwright JSON reports spec.file relative to testDir (./e2e), so the
    # values are basenames rather than e2e/<file> paths.
    # anime-grid: phone-only checks skip in the desktop project and vice versa.
    ("anime-grid.spec.ts", "does not overflow at mobile width", "desktop"),
    ("anime-grid.spec.ts", "drags a phone bottom sheet down to close it, springs back on a short drag", "desktop"),
    # settings: breakpoint-specific layout forms are asserted per project;
    # the other project skips them by design.
    ("settings.spec.ts", "桌面端展示 section sidebar，十四个分区齐全", "mobile"),
    ("settings.spec.ts", "手机：先看到分区列表，进入分区后内容页带返回", "desktop"),
    ("settings.spec.ts", "手机：深链直接进入分区内容", "desktop"),
    ("settings.spec.ts", "桌面端网格几何：导航列 11rem，内容区不被挤压", "mobile"),
    ("settings.spec.ts", "provider 配置页可达：AniBT 与 Mikan", "mobile"),
    ("settings.spec.ts", "下载与入库 / 订阅数量与对齐 / 默认规则 可达并渲染 mock 数据", "mobile"),
    ("settings.spec.ts", "版本与升级默认空态与 AI Lab 可达", "mobile"),
}

# Projects the fixture lane must cover for EVERY expected spec file
# (mirrors the --project flags of the Playwright invocation above).
REQUIRED_PROJECTS = ("desktop", "mobile")


def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)


report_path = sys.argv[1]
expected_specs = sys.argv[2:]
if not expected_specs:
    fail("validator received no expected fixture spec basenames")
duplicate_specs = sorted(
    name for name, count in Counter(expected_specs).items() if count > 1
)
if duplicate_specs:
    fail(
        "duplicate expected fixture spec basenames in argv: "
        + ", ".join(duplicate_specs)
    )

try:
    with open(report_path, encoding="utf-8") as handle:
        report = json.load(handle)
except Exception as exc:
    fail(f"cannot read Playwright JSON report: {exc}")

suites = report.get("suites") if isinstance(report, dict) else None
if not isinstance(suites, list):
    fail("Playwright JSON report has no top-level suites list")

skipped = []
seen_files = set()
projects_by_file = {}
total_tests = 0


def walk(node):
    global total_tests
    if not isinstance(node, dict):
        fail("unexpected suite node in JSON report")
    for spec in node.get("specs") or []:
        if not isinstance(spec, dict):
            fail("unexpected spec node in JSON report")
        title = spec.get("title")
        file = spec.get("file")
        if not isinstance(title, str) or not isinstance(file, str) or not file:
            fail(f"spec without title/file cannot be validated: {spec!r}")
        # Normalize to a bare basename so both the ALLOWED_SKIPS lookup and
        # the expected-spec coverage check stay stable regardless of whether
        # the reporter emits testDir-relative or absolute paths.
        file = os.path.basename(file)
        tests = spec.get("tests")
        if not isinstance(tests, list):
            fail(f"spec {title!r} ({file}) has no tests list")
        for case in tests:
            if not isinstance(case, dict):
                fail(f"spec {title!r} ({file}) has a malformed test entry")
            project = case.get("projectName")
            if not isinstance(project, str) or not project:
                fail(
                    f"test {title!r} ({file}) lacks projectName; "
                    "JSON reporter schema changed?"
                )
            results = case.get("results")
            if not isinstance(results, list):
                fail(f"test {title!r} ({project}) has no results list")
            if not results:
                fail(
                    f"test {title!r} ({file}, project {project}) has an empty "
                    "results list; it never ran and must not count as coverage"
                )
            total_tests += 1
            seen_files.add(file)
            projects_by_file.setdefault(file, set()).add(project)
            result_skipped = False
            for result in results:
                if not isinstance(result, dict):
                    fail(f"test {title!r} ({project}) has a malformed result entry")
                if result.get("status") == "skipped":
                    result_skipped = True
            if case.get("status") == "skipped" or result_skipped:
                skipped.append((file, title, project))
    for child in node.get("suites") or []:
        walk(child)


for suite in suites:
    walk(suite)

# Completeness gate: zero test cases means nothing actually ran.
if total_tests == 0:
    fail("Playwright JSON report contains zero test cases; fixture specs did not run")

# Every expected fixture spec must appear in the report at all.
missing_files = [name for name in expected_specs if name not in seen_files]
if missing_files:
    print("expected fixture specs absent from the report:", file=sys.stderr)
    for name in missing_files:
        print(f"  {name}", file=sys.stderr)
    sys.exit(1)

# ...and every expected spec needs test cases for BOTH required projects.
missing_projects = [
    (name, project)
    for name in expected_specs
    for project in REQUIRED_PROJECTS
    if project not in projects_by_file.get(name, set())
]
if missing_projects:
    print("expected fixture specs missing project coverage:", file=sys.stderr)
    for name, project in missing_projects:
        print(f"  {name}: no test cases for project '{project}'", file=sys.stderr)
    sys.exit(1)

# Multiset comparison: each declared triple covers at most one actual skip,
# so a single declared entry cannot mask multiple identical undeclared skips.
excess_skips = Counter(skipped) - Counter(ALLOWED_SKIPS)
if excess_skips:
    print("disallowed skips:", file=sys.stderr)
    for (file, title, project), count in sorted(excess_skips.items()):
        print(f"  [{project}] {file}: {title} (x{count})", file=sys.stderr)
    sys.exit(1)

if not skipped:
    print("no skipped tests")
else:
    print(f"{len(skipped)} skipped test(s), all declared design-time viewport skips")
print(
    f"coverage: all {len(expected_specs)} expected specs present for projects "
    f"{'+'.join(REQUIRED_PROJECTS)} ({total_tests} test cases)"
)
PY
)" || {
  echo "Fixture browser lane: skip validation failed (diagnostics above)" >&2
  exit 1
}
echo "Fixture browser lane: ${skip_summary}"

if [ "${status}" != "0" ]; then
  echo "Fixture browser lane failed (exit ${status})" >&2
  cat "$report" >&2 || true
  exit "${status}"
fi

echo "Fixture browser lane passed (only declared design-time viewport skips)"
