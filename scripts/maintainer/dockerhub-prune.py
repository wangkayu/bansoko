#!/usr/bin/env python3
"""Prune disposable tags from the public Docker Hub release repository.

A main push publishes, to `wangkayu/bansoko`, the immutable multi-arch test
image `<X.Y.Z>-dev-<sha9>` behind the moving `:test` tag. Left alone those
accumulate one per push, forever, and bury the tags a user actually pulls.

This tool lists every tag, keeps a small, explicit set, and prints the rest.
Nothing is deleted unless --apply is given, and only names matching the known
disposable shapes are ever candidates: `latest`, `:test`, release tags and any
name this tool does not recognise are always kept.

The build repository (`wangkayu/bansoko-builds`) is off limits: ci-release.py
pushes the single-arch image there and records its digest in a published
release manifest, so a tag deleted there can take a manifest's image with it
when the registry garbage-collects. The guard below refuses that repository.

    python3 scripts/maintainer/dockerhub-prune.py                  # dry run
    python3 scripts/maintainer/dockerhub-prune.py --list           # + names
    python3 scripts/maintainer/dockerhub-prune.py --apply

--apply authenticates with the credentials `docker login` already stored
(DOCKERHUB_TOKEN is used when set, otherwise ~/.docker/config.json and its
credential helper). The token needs read and delete access to the repository.
--resolve-ip pins the API host to a literal address, which is needed where DNS
for hub.docker.com is poisoned; ask a DoH resolver for the real value, e.g.
https://dns.google/resolve?name=hub.docker.com&type=A
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass

API_HOST = "https://hub.docker.com"
API_PATH = "/v2/repositories/{owner}/{repo}/tags/"

MOVING = {"latest", "test"}
# Not a pruning target: every tag here backs a publishable manifest by digest.
BUILD_REPOSITORIES = {"bansoko-builds"}
RELEASE = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
TEST_IMAGE = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-dev-[0-9a-f]{9}\Z")
STAGING = re.compile(
    r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-test\.[0-9]+)?\Z"
)


@dataclass
class Tag:
    name: str
    updated: str
    size: int
    family: str
    keep: bool = True


def fail(message: str) -> "NoReturn":  # type: ignore[name-defined]
    print("dockerhub-prune: " + message, file=sys.stderr)
    sys.exit(1)


def curl(args: list[str]) -> subprocess.CompletedProcess[str]:
    base = ["curl", "-sS", "--max-time", "30"]
    if RESOLVE_IP:
        base += ["--resolve", "hub.docker.com:443:" + RESOLVE_IP]
    return subprocess.run(base + args, capture_output=True, text=True)


def hub_credentials(owner: str) -> tuple[str, str]:
    """Return (username, secret) the way the docker CLI would find them."""
    token = os.environ.get("DOCKERHUB_TOKEN")
    if token:
        return owner, token

    config_path = os.path.expanduser("~/.docker/config.json")
    try:
        with open(config_path, encoding="utf-8") as handle:
            config = json.load(handle)
    except OSError:
        config = {}

    auths = config.get("auths") or {}
    for host in ("https://index.docker.io/v1/", "index.docker.io", "docker.io"):
        entry = auths.get(host) or {}
        if entry.get("auth"):
            decoded = base64.b64decode(entry["auth"]).decode("utf-8", "replace")
            user, _, secret = decoded.partition(":")
            if secret:
                return user, secret
        if entry.get("identitytoken"):
            return entry.get("username") or owner, entry["identitytoken"]

    store = config.get("credsStore") or (config.get("credHelpers") or {}).get("index.docker.io")
    if store:
        helper = "docker-credential-" + store
        try:
            done = subprocess.run(
                [helper, "get"], input="https://index.docker.io/v1/", capture_output=True, text=True
            )
        except OSError as error:
            fail(f"credential helper {helper} could not run: {error}")
        if done.returncode != 0:
            fail(f"no Docker Hub login found: {helper} says {done.stderr.strip() or done.stdout.strip()}")
        stored = json.loads(done.stdout)
        return stored["Username"], stored["Secret"]

    fail("no Docker Hub login found (docker login, or set DOCKERHUB_TOKEN)")


def hub_login(user: str, secret: str) -> str:
    """Exchange credentials for the bearer token the Hub API expects.

    Basic auth is not accepted by the repository endpoints (401), so the delete
    calls carry the token this endpoint issues.
    """
    # Spelled by concatenation: the key is the literal `password` and must not
    # be written out, or a text replacement could substitute a real value here.
    payload = json.dumps({"username": user, "pass" + "word": secret})
    done = curl(
        [
            "-H",
            "Content-Type: application/json",
            "-d",
            payload,
            API_HOST + "/v2/users/login/",
        ]
    )
    if done.returncode != 0:
        fail(f"login failed: {done.stderr.strip()}")
    try:
        token = json.loads(done.stdout).get("token") or ""
    except json.JSONDecodeError:
        token = ""
    if not token:
        fail("login was refused: check that the credential is a personal access token")
    return token


def fetch_tags(owner: str, repo: str) -> list[Tag]:
    tags: list[Tag] = []
    for page in range(1, 101):
        url = API_HOST + API_PATH.format(owner=owner, repo=repo)
        url += f"?page_size=100&ordering=last_updated&page={page}"
        done = curl(["--fail", url])
        if done.returncode != 0:
            # The API's count and next link lag behind deletions, so a later
            # page can 404 once the list has shrunk; that is the end of it.
            if page > 1 and "404" in done.stderr:
                break
            fail(f"listing tags failed (page {page}): {done.stderr.strip()}")
        body = json.loads(done.stdout)
        results = body.get("results") or []
        for raw in results:
            images = raw.get("images") or []
            tags.append(
                Tag(
                    name=raw["name"],
                    updated=raw.get("last_updated", ""),
                    size=sum(image.get("size") or 0 for image in images),
                    family=family_of(raw["name"]),
                )
            )
        if len(results) < 100 or not body.get("next"):
            break
    return tags


def family_of(name: str) -> str:
    if name in MOVING:
        return "moving"
    if RELEASE.match(name):
        return "release"
    if TEST_IMAGE.match(name):
        return "test image"
    if STAGING.match(name):
        return "staging"
    return "unrecognised"


def plan(tags: list[Tag], keep_test: int, keep_staging: int) -> list[Tag]:
    counts = {"test image": keep_test, "staging": keep_staging}
    for tag in tags:
        remaining = counts.get(tag.family)
        if remaining is None:
            tag.keep = True
            continue
        tag.keep = remaining > 0
        counts[tag.family] = remaining - 1
    return tags


def human(size: int) -> str:
    return f"{size / 2**20:.1f} MiB"


def main() -> None:
    parser = argparse.ArgumentParser(description="Prune disposable Docker Hub tags.")
    parser.add_argument("--owner", default="wangkayu")
    parser.add_argument("--repo", default="bansoko")
    parser.add_argument(
        "--keep-test",
        type=int,
        default=5,
        help="multi-arch test images to keep (default 5, for rolling back :test)",
    )
    parser.add_argument(
        "--keep-staging",
        type=int,
        default=2,
        help="legacy staging images to keep (default 2; new ones live in the build repository)",
    )
    parser.add_argument(
        "--resolve-ip",
        default=os.environ.get("HUB_RESOLVE_IP", ""),
        help="pin hub.docker.com to this address (poisoned DNS workaround)",
    )
    parser.add_argument("--list", action="store_true", help="print every name in the delete set")
    parser.add_argument(
        "--apply", action="store_true", help="delete the tags; without it this is a dry run"
    )
    args = parser.parse_args()

    global RESOLVE_IP
    RESOLVE_IP = args.resolve_ip
    if args.repo in BUILD_REPOSITORIES:
        fail(
            "the build repository is not prunable: published release manifests pin "
            "its image digests, so deleting a tag there can orphan a manifest"
        )
    if args.keep_test < 0 or args.keep_staging < 0:
        fail("the keep counts cannot be negative")

    tags = fetch_tags(args.owner, args.repo)
    if not tags:
        fail("the repository reported no tags; refusing to plan a deletion from no data")

    tags = plan(tags, args.keep_test, args.keep_staging)
    doomed = [tag for tag in tags if not tag.keep]

    # A release tag is the one thing this tool must never touch, whatever the
    # family rules say; a name we do not recognise is somebody's deliberate
    # choice and stays until a human decides otherwise.
    if any(tag.family in ("release", "moving", "unrecognised") for tag in doomed):
        fail("internal error: a protected tag entered the delete set")

    families: dict[str, list[Tag]] = {}
    for tag in tags:
        families.setdefault(tag.family, []).append(tag)
    print(f"{args.owner}/{args.repo}: {len(tags)} tags")
    for family, members in sorted(families.items()):
        kept = sum(1 for tag in members if tag.keep)
        print(f"  {family:<12} {len(members):>4} total  {kept:>3} kept  {len(members) - kept:>4} to delete")
    print(
        f"\ndelete {len(doomed)} tags, {human(sum(tag.size for tag in doomed))} "
        f"of {human(sum(tag.size for tag in tags))} (sum of per-tag sizes, not deduplicated)"
    )

    if args.list or len(doomed) <= 40:
        for tag in sorted(doomed, key=lambda item: item.updated):
            print(f"  {tag.name:<30} {tag.updated[:10]}  {human(tag.size):>10}  {tag.family}")
    elif doomed:
        print("  (pass --list to print every name)")

    if not args.apply:
        print("\ndry run: nothing deleted. Re-run with --apply to delete these tags.")
        return

    user, secret = hub_credentials(args.owner)
    bearer = hub_login(user, secret)

    failures = 0
    for tag in doomed:
        url = (
            API_HOST
            + API_PATH.format(owner=args.owner, repo=args.repo)
            + tag.name
            + "/"
        )
        done = curl(
            [
                "-o",
                "/dev/null",
                "-w",
                "%{http_code}",
                "-H",
                "Authorization: Bearer " + bearer,
                "-X",
                "DELETE",
                url,
            ]
        )
        code = done.stdout.strip()
        if done.returncode != 0 or code not in ("200", "202", "204"):
            failures += 1
            print(f"  failed {tag.name}: http {code or '-'} {done.stderr.strip()}", file=sys.stderr)
        else:
            print(f"  deleted {tag.name}")
    if failures:
        fail(f"{failures} deletions failed; re-run to retry the rest")
    print(f"\ndeleted {len(doomed)} tags.")


RESOLVE_IP = ""

if __name__ == "__main__":
    main()
