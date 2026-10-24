#!/usr/bin/env python3
"""Authenticated run results must bind the exact event, not similarly named statuses."""

import importlib.util
import os
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "trust", Path(__file__).with_name("ci-release-trust.py")
)
assert spec and spec.loader
trust = importlib.util.module_from_spec(spec)
spec.loader.exec_module(trust)

SHA = "a" * 40
ENV = {
    "GITHUB_REPOSITORY": "wangkayu/bansoko",
    "GITHUB_SHA": SHA,
    "GITHUB_RUN_ID": "71",
    "GITHUB_RUN_NUMBER": "53",
    "BANSOKO_RELEASE_READ_TOKEN": "sentinel",
}
JOBS = [
    {"name": name, "status": "completed", "conclusion": "success", "head_sha": SHA, "run_id": 71}
    for name in sorted(trust.REQUIRED_JOBS)
]
RUN = {"head_sha": SHA, "event": "push", "head_branch": "main", "run_number": 53}


def api(repo=None, collaborators=None, branch=None, run=None, jobs=None, statuses=None):
    """Fake GET keyed by path; statuses overrides the HTTP status per path."""
    bodies = {
        "": repo if repo is not None else {"full_name": "wangkayu/bansoko", "private": True},
        "/collaborators?per_page=100": collaborators if collaborators is not None else [{"login": "wangkayu"}],
        "/branches/main": branch if branch is not None else {"protected": True},
        "/actions/runs/71": run if run is not None else RUN,
        "/actions/runs/71/jobs?per_page=100": {"jobs": jobs if jobs is not None else JOBS},
    }

    def get(path, token):
        assert token == "sentinel"
        status = (statuses or {}).get(path, 200)
        return status, bodies[path] if status == 200 else None

    return get


class TrustTests(unittest.TestCase):
    def verify(self, get, env=ENV):
        with patch.dict(os.environ, env, clear=True), patch.object(trust, "get", side_effect=get):
            trust.verify()

    def refuses(self, get, env=ENV):
        with self.assertRaises(RuntimeError):
            self.verify(get, env)

    def test_private_owner_only_passes(self):
        self.verify(api())

    def test_public_requires_branch_protection(self):
        public = {"full_name": "wangkayu/bansoko", "private": False}
        self.verify(api(repo=public))
        self.refuses(api(repo=public, branch={"protected": False}))

    def test_private_with_another_collaborator_refused(self):
        self.refuses(api(collaborators=[{"login": "wangkayu"}, {"login": "someone"}]))
        self.refuses(api(collaborators=[]))

    def test_repository_identity(self):
        self.refuses(api(repo={"full_name": "fork/bansoko", "private": True}))
        self.refuses(api(), env={**ENV, "GITHUB_REPOSITORY": "fork/bansoko"})

    def test_run_identity(self):
        for field, value in [("head_sha", "b" * 40), ("event", "pull_request"), ("head_branch", "dev"), ("run_number", 52)]:
            self.refuses(api(run={**RUN, field: value}))

    def test_each_job_field(self):
        for field, value in [("status", "queued"), ("conclusion", "failure"), ("head_sha", "b" * 40), ("run_id", 70)]:
            altered = [dict(j) for j in JOBS]
            altered[0][field] = value
            self.refuses(api(jobs=altered))
        self.refuses(api(jobs=JOBS[1:]))
        self.refuses(api(jobs=JOBS + [dict(JOBS[0])]))

    def test_api_failures_refused(self):
        for path in ["", "/collaborators?per_page=100", "/actions/runs/71", "/actions/runs/71/jobs?per_page=100"]:
            for status in [401, 403, 404, 500]:
                self.refuses(api(statuses={path: status}))

    def test_missing_environment_refused(self):
        for key in ENV:
            env = dict(ENV)
            del env[key]
            self.refuses(api(), env=env)


if __name__ == "__main__":
    unittest.main()
