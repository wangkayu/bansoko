#!/usr/bin/env python3
"""Verify main's write boundary, run identity and completed required jobs.

Two publishing events exist. A main push (test image and the signed test
update lane) binds the run to main. A vX.Y.Z tag push (formal release) binds the run to that tag and
additionally proves the tagged commit is already on main: a release is always
a commit that went through main's write boundary, never a side branch.
"""

import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

API = "https://api.github.com"
REPO = "wangkayu/bansoko"
OWNER = "wangkayu"
REQUIRED_JOBS = {
    "contract + boundary",
    "go unit + static",
    "frontend quality",
    "postgres integration (shard 1/3)",
    "postgres integration (shard 2/3)",
    "postgres integration (shard 3/3)",
    "qB real chain",
    "browser fixture lane",
}


def fail(message):
    raise RuntimeError(message)


def required(name):
    value = os.environ.get(name, "")
    if not value:
        fail("missing " + name)
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def get(path, token):
    url = API + "/repos/" + REPO + path
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != "https" or parsed.netloc != "api.github.com":
        fail("unsafe API URL")
    req = urllib.request.Request(
        url,
        headers={
            "Authorization": "Bearer " + token,
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    try:
        with urllib.request.build_opener(NoRedirect).open(req, timeout=30) as resp:
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as err:
        return err.code, None


def verify_write_boundary(token):
    # A public repository relies on platform branch protection. GitHub Free
    # offers none for private repositories, so while private the boundary is
    # the collaborator list: the owner is the only account that can push main.
    status, repo = get("", token)
    if status != 200:
        fail("repository unavailable")
    if repo.get("full_name") != REPO:
        fail("repository mismatch")
    if not repo.get("private"):
        status, branch = get("/branches/main", token)
        if status != 200 or not branch.get("protected"):
            fail("public main is not protected by platform")
        return "platform-protected main"
    status, collaborators = get("/collaborators?per_page=100", token)
    if status != 200:
        fail("collaborators unavailable")
    if [c.get("login") for c in collaborators] != [OWNER]:
        fail("private repository has collaborators besides the owner")
    return "private repository, owner-only writes"


RELEASE_TAG = re.compile(r"refs/tags/(v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))")


def event_ref():
    """The branch or tag name this run must be bound to."""
    ref = required("GITHUB_REF")
    if ref == "refs/heads/main":
        return "main", None
    tag = RELEASE_TAG.fullmatch(ref)
    if tag:
        return tag.group(1), tag.group(1)
    fail("only main or a vX.Y.Z tag may publish")


def verify_tag_on_main(tag, sha, token):
    """The tag names exactly this commit, and main already contains it."""
    status, ref = get("/git/ref/tags/" + tag, token)
    if status != 200:
        fail("release tag unavailable")
    target = ref.get("object", {})
    if target.get("type") == "tag":
        # An annotated tag points at a tag object; the commit is one step on.
        status, annotated = get("/git/tags/" + target.get("sha", ""), token)
        if status != 200:
            fail("annotated release tag unavailable")
        target = annotated.get("object", {})
    if target.get("type") != "commit" or target.get("sha") != sha:
        fail("release tag does not name the event commit")
    status, comparison = get("/compare/" + sha + "...main", token)
    if status != 200:
        fail("main ancestry unavailable")
    # base...head with base = the tag commit: "identical" or "ahead" means
    # main is the commit itself or descends from it.
    if (
        comparison.get("status") not in ("identical", "ahead")
        or comparison.get("merge_base_commit", {}).get("sha") != sha
    ):
        fail("release tag commit is not on main")


def verify():
    if os.environ.get("GITHUB_REPOSITORY") != REPO:
        fail("workflow repository mismatch")
    sha = required("GITHUB_SHA")
    run_id = required("GITHUB_RUN_ID")
    token = required("BANSOKO_RELEASE_READ_TOKEN")
    head, tag = event_ref()
    boundary = verify_write_boundary(token)
    status, run = get("/actions/runs/" + run_id, token)
    if status != 200:
        fail("trusted run unavailable")
    if (
        run.get("head_sha") != sha
        or run.get("event") != "push"
        or run.get("head_branch") != head
        or str(run.get("run_number")) != required("GITHUB_RUN_NUMBER")
    ):
        fail("run/event/source mismatch")
    if tag:
        verify_tag_on_main(tag, sha, token)
        boundary += ", tag " + tag + " on main"
    status, body = get("/actions/runs/" + run_id + "/jobs?per_page=100", token)
    if status != 200:
        fail("trusted job results unavailable")
    jobs = body["jobs"]
    # Names are matched only INSIDE the authenticated exact run, never commit statuses.
    for name in REQUIRED_JOBS:
        matching = [j for j in jobs if j.get("name") == name]
        if (
            len(matching) != 1
            or matching[0].get("conclusion") != "success"
            or matching[0].get("status") != "completed"
            or matching[0].get("head_sha") != sha
            or str(matching[0].get("run_id")) != run_id
        ):
            fail("required platform job not successful: " + name)
    print(
        boundary
        + ", exact run/SHA and "
        + str(len(REQUIRED_JOBS))
        + " required results verified"
    )


if __name__ == "__main__":
    try:
        verify()
    except (RuntimeError, ValueError, KeyError, OSError) as err:
        print("ERROR: trusted publication precondition refused: " + str(err), file=sys.stderr)
        sys.exit(1)
