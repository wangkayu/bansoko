#!/usr/bin/env python3
"""Make this repository public and protect main in the same step.

Publishing the source is an owner act: this script performs it and nothing
else does. The two halves cannot be separated in time. `ci-release-trust.py`
requires a public main to be platform-protected, and GitHub refuses to create
branch protection on a private free-plan repository, so the moment between the
visibility change and the protection write is a window in which every release
job would fail closed. If the protection write does not succeed, the script
flips the repository back to private rather than leave an unprotected public
main behind.

Usage:
    scripts/maintainer/make-repository-public.py --dry-run     # show the plan and checks
    scripts/maintainer/make-repository-public.py --confirm     # perform it

The token comes from `gh auth token` (or GITHUB_TOKEN); it needs admin on the
repository, which the owner has.
"""

import json
import os
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

API = "https://api.github.com"
REPO = "wangkayu/bansoko"
BRANCH = "main"

# Everything the incident window must not be missing. Force pushes stay
# allowed for admins so a history rewrite is still possible; deletions never,
# because a deleted main takes the protected-write boundary with it.
PROTECTION = {
    "required_status_checks": None,
    "enforce_admins": False,
    "required_pull_request_reviews": None,
    "restrictions": None,
    "allow_force_pushes": False,
    "allow_deletions": False,
    "required_conversation_resolution": False,
}


def fail(message):
    print("FAIL: " + message, file=sys.stderr)
    sys.exit(1)


def token():
    value = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if value:
        return value.strip()
    try:
        return subprocess.run(
            ["gh", "auth", "token"], capture_output=True, text=True, check=True
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        fail("no token: set GITHUB_TOKEN or sign in with gh")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def call(method, path, body=None):
    url = API + "/repos/" + REPO + path
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != "https" or parsed.netloc != "api.github.com":
        fail("unsafe API URL")
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(
        url,
        data=data,
        method=method,
        headers={
            "Authorization": "Bearer " + token(),
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "Content-Type": "application/json",
        },
    )
    opener = urllib.request.build_opener(NoRedirect)
    try:
        with opener.open(req, timeout=60) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as err:
        raw = err.read()
        try:
            return err.code, json.loads(raw) if raw else None
        except ValueError:
            return err.code, None


def state():
    status, repo = call("GET", "")
    if status != 200 or not repo:
        fail("repository unavailable (status %s)" % status)
    if repo.get("full_name") != REPO:
        fail("repository mismatch: " + str(repo.get("full_name")))
    status, branch = call("GET", "/branches/" + BRANCH)
    protected = status == 200 and bool((branch or {}).get("protected"))
    return repo, protected


def main():
    dry_run = "--dry-run" in sys.argv
    confirmed = "--confirm" in sys.argv
    if not dry_run and not confirmed:
        fail("pass --dry-run to inspect the plan, or --confirm to perform it")

    repo, protected = state()
    private = bool(repo.get("private"))
    print("repository: %s (%s)" % (REPO, "private" if private else "public"))

    if not private and protected:
        print("already public and protected; nothing to do")
        return
    if not private and not protected:
        # Never the intended state, and the riskiest one. Fixing it does not
        # need a visibility flip, so it is handled on its own terms.
        print("public without protection: enabling protection")
        if dry_run:
            return
        enable_protection()
        return

    # Preconditions for a flip that cannot be half-done.
    status, permissions = call("GET", "/actions/permissions")
    if status != 200:
        fail("cannot read Actions permissions (status %s)" % status)
    if (permissions or {}).get("enabled") is not True:
        fail("Actions are disabled; release jobs could not run after the flip")
    status, default_permissions = call("GET", "/actions/permissions/workflow")
    if status != 200:
        fail("cannot read the default workflow token scope")
    print(
        "actions: enabled=%s allowed=%s default-token=%s"
        % (
            permissions.get("enabled"),
            permissions.get("allowed_actions"),
            (default_permissions or {}).get("default_workflow_permissions"),
        )
    )
    status, secrets = call("GET", "/actions/secrets")
    if status != 200:
        fail("cannot list Actions secrets (status %s)" % status)
    names = sorted(s.get("name") for s in (secrets or {}).get("secrets", []))
    # The single destination names its public values in the workflow itself;
    # only the Hub push token and the signing key remain as secrets.
    missing = [
        name
        for name in (
            "DOCKERHUB_TOKEN",
            "RELEASE_SIGNING_KEY",
        )
        if name not in names
    ]
    if missing:
        fail("release secrets missing: " + ", ".join(missing))
    print("release secrets: %d present" % len(names))

    print("plan: set private=false, then immediately PUT the main protection")
    print("      protection: force pushes %s, deletions %s, admins enforced %s"
          % (
              "allowed" if PROTECTION["allow_force_pushes"] else "denied",
              "allowed" if PROTECTION["allow_deletions"] else "denied",
              PROTECTION["enforce_admins"],
          ))
    if dry_run:
        return

    status, body = call("PATCH", "", {"private": False})
    if status not in (200, 201):
        fail("visibility change failed (status %s): %s" % (status, body))
    print("visibility: public")

    status, body = call("PUT", "/branches/" + BRANCH + "/protection", PROTECTION)
    if status != 200:
        print("FAIL: protection write failed (status %s): %s" % (status, body),
              file=sys.stderr)
        revert = call(
            "PATCH",
            "",
            {"private": True, "visibility": "private"},
        )
        print(
            "reverted to private (status %s); main is never left public without "
            "protection" % revert[0],
            file=sys.stderr,
        )
        sys.exit(1)
    print("protection: enabled")

    repo, protected = state()
    if repo.get("private") or not protected:
        fail("post-check disagrees: private=%s protected=%s"
             % (repo.get("private"), protected))
    status, branch = call("GET", "/branches/" + BRANCH)
    print(
        "verified: public main protected=%s force-push=%s deletion=%s"
        % (
            branch.get("protected"),
            branch.get("allow_force_pushes"),
            branch.get("allow_deletions"),
        )
    )


def enable_protection():
    status, body = call("PUT", "/branches/" + BRANCH + "/protection", PROTECTION)
    if status != 200:
        fail("protection write failed (status %s): %s" % (status, body))
    repo, protected = state()
    if not protected:
        fail("protection reported enabled but main still reads unprotected")
    print("protection: enabled")


if __name__ == "__main__":
    main()
