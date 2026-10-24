#!/usr/bin/env python3
"""Trusted isolated test publication. Secrets remain outside the build context."""

import base64
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import NoReturn

ROOT = Path(__file__).resolve().parents[2]
# The Gitea channel branch is live: images in the field resolve their update
# URL against it, so it keeps the name it was published under. The GitHub
# channel is a port of the same scheme to a different host, not a test copy of
# the Gitea one, so it carries the current name.
GITEA_BRANCH = "update-channel-test"
BRANCH = "update-channel"
# The channel branch is created from this one on first publication. GitHub has
# no "empty branch" concept, so the branch has to start somewhere, and main is
# the only ref this run has already proved it is building.
GITHUB_BASE_BRANCH = "main"
OUTPUT = ROOT / ".local-artifacts/release-ci"

# Publication destinations. Gitea is the one the lane was built on: a generic
# package store for the immutable files plus a branch blob for the channel.
# GitHub publishes the same files, but as ordinary files on the channel branch
# rather than release assets — see github_file_url() for why.
# The GitHub destination writes with the workflow's own token, which Actions
# issues scoped to this repository; there is no separate credential to manage,
# and naming it here keeps the secret scope visible in one place.
GITHUB_TOKEN_ENV = "BANSOKO_RELEASE_GITHUB_TOKEN"
DESTINATION_GITEA = "gitea"
DESTINATION_GITHUB = "github"
KNOWN_DESTINATIONS = (DESTINATION_GITEA, DESTINATION_GITHUB)


def fail(message) -> "NoReturn":
    raise RuntimeError(message)


def required(name):
    value = os.environ.get(name, "")
    if not value:
        fail("missing " + name)
    return value


# The publication destination is deployment configuration, never a source
# default: these are resolved per call so importing the module or running a
# single stage does not demand values the other stages do not use.
def destinations():
    """The destinations this run publishes to, in declared order.

    Dual publication is the default while the move is in progress: an installed
    instance may still read the Gitea channel, so an extra write keeps in-app
    update working there while the GitHub names start existing. A destination is
    never inferred from a credential being present — that would let an unrelated
    secret silently enable a publication.
    """
    raw = os.environ.get("BANSOKO_RELEASE_DESTINATIONS", DESTINATION_GITEA)
    names = [part.strip() for part in raw.split(",") if part.strip()]
    if not names:
        fail("BANSOKO_RELEASE_DESTINATIONS names no destination")
    unknown = [name for name in names if name not in KNOWN_DESTINATIONS]
    if unknown:
        fail("unknown release destination: " + ", ".join(sorted(unknown)))
    if len(set(names)) != len(names):
        fail("duplicate release destination")
    return names


def destination_enabled(name):
    return name in destinations()


def github_api_base():
    return "https://api.github.com"


def github_raw_base():
    # The update client refuses a cross-host redirect and a query string. A
    # github.com/<o>/<r>/raw/... URL is a 302 to raw.githubusercontent.com, so
    # the channel is published and consumed at the final host directly.
    return "https://raw.githubusercontent.com"


def github_repository():
    """The GitHub repository this run is published by, which is not the Gitea one.

    BANSOKO_RELEASE_REPO is deployment configuration for the Gitea destination and
    names a different owner there. GitHub's own identity is the repository the
    workflow runs in, which Actions already provides, so the GitHub files can
    never be aimed at a path the Gitea config happens to spell.
    """
    value = required("GITHUB_REPOSITORY")
    parts = value.split("/")
    if len(parts) != 2 or not all(parts) or any(
        character.isspace() for character in value
    ):
        fail("invalid GITHUB_REPOSITORY (owner/repo)")
    return value


def github_raw_url(name=""):
    path = "/" + github_repository() + "/" + BRANCH
    return github_raw_base() + (path + "/" + name if name else path)


def github_content_url():
    return (
        github_api_base()
        + "/repos/"
        + github_repository()
        + "/contents/channel-v1.json"
    )


def github_file_url(version, name):
    """One immutable release file, addressed on the channel branch.

    GitHub release assets are not usable for this: their download URL is a 302
    to release-assets.githubusercontent.com carrying a signed query string with
    roughly an hour of validity, and the update client refuses both the host
    change and the query. A file on the channel branch is fetched from
    raw.githubusercontent.com with no redirect and no query, and immutability
    comes from the same place it comes from on Gitea: identical bytes for one
    release_sequence, a signature over the manifest, and a client that only
    accepts a larger sequence.
    """
    return github_raw_url(version + "/" + name)


def api_base():
    value = required("BANSOKO_RELEASE_API_BASE").rstrip("/")
    parsed = urllib.parse.urlsplit(value)
    if (
        parsed.scheme != "https"
        or "." not in (parsed.hostname or "")
        or parsed.username
        or parsed.path
        or parsed.query
        or parsed.fragment
    ):
        fail("invalid BANSOKO_RELEASE_API_BASE (https origin with a dotted host)")
    return value


def repository():
    value = required("BANSOKO_RELEASE_REPO")
    parts = value.split("/")
    if (
        len(parts) != 2
        or not all(parts)
        or "://" in value
        or any(character.isspace() for character in value)
    ):
        fail("invalid BANSOKO_RELEASE_REPO (owner/repo)")
    return value


def image_repository():
    """The full image repository and the registry host it splits at the first /."""
    value = required("BANSOKO_RELEASE_IMAGE")
    host, separator, path = value.partition("/")
    if (
        not separator
        or not path
        or "://" in value
        or any(character.isspace() for character in value)
    ):
        fail("invalid BANSOKO_RELEASE_IMAGE (registry host and image path)")
    return value, host


def registry_user():
    value = required("BANSOKO_RELEASE_REGISTRY_USER")
    if ":" in value or any(character.isspace() for character in value):
        fail("invalid BANSOKO_RELEASE_REGISTRY_USER")
    return value


def package():
    # Gitea packages are owned by the repository owner, so derive it from REPO
    # rather than repeating an owner name that the destination may not share.
    return (
        api_base()
        + "/api/packages/"
        + repository().split("/")[0]
        + "/generic/bansoko-test"
    )


def content_url():
    return api_base() + "/api/v1/repos/" + repository() + "/contents/channel-v1.json"


def raw_url():
    return (
        api_base()
        + "/api/v1/repos/"
        + repository()
        + "/raw/"
        + GITEA_BRANCH
        + "/channel-v1.json"
    )


def run(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env).decode().strip()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def request(url, token, method="GET", data=None, hosts=None):
    """One HTTPS call, to an allowlisted host only.

    `hosts` defaults to the configured API origin. GitHub needs two more: its
    API host and the raw file host, both fixed names rather than configuration,
    so a call can never be aimed at a host the deployment did not choose.
    """
    parsed = urllib.parse.urlsplit(url)
    allowed = hosts if hosts is not None else (urllib.parse.urlsplit(api_base()).netloc,)
    if (
        parsed.scheme != "https"
        or parsed.netloc not in allowed
        or parsed.username
        or parsed.fragment
    ):
        fail("unsafe release URL")
    headers = {"Authorization": "token " + token}
    if isinstance(data, dict):
        data = json.dumps(data).encode()
        headers["Content-Type"] = "application/json"
    elif data is not None:
        headers["Content-Type"] = "application/octet-stream"
    req = urllib.request.Request(url, headers=headers, method=method, data=data)
    try:
        with urllib.request.build_opener(NoRedirect()).open(
            req, timeout=120
        ) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()
    except (urllib.error.URLError, TimeoutError):
        fail("release HTTP network failure")


def identity():
    if (
        os.environ.get("GITHUB_EVENT_NAME") != "push"
        or os.environ.get("GITHUB_REF") != "refs/heads/main"
    ):
        fail("only trusted main push can publish")
    if os.environ.get("BANSOKO_RELEASE_REF_PROTECTED") != "true":
        fail("protected ref proof required")
    sha = required("GITHUB_SHA")
    n = required("GITHUB_RUN_NUMBER")
    run_id = required("GITHUB_RUN_ID")
    if (
        not re.fullmatch(r"[a-f0-9]{40}", sha)
        or not re.fullmatch(r"[1-9][0-9]*", n)
        or not run_id.isdecimal()
    ):
        fail("invalid immutable event identity")
    if run("git", "rev-parse", "HEAD") != sha or run("git", "status", "--porcelain"):
        fail("release source must be exact clean event SHA")
    needs = decode_json(required("BANSOKO_REQUIRED_RESULTS"))
    gates = {
        name: value.get("result")
        for name, value in needs.items()
        if isinstance(value, dict)
    }
    expected = {
        "contract-boundary",
        "go-unit-static",
        "frontend-quality",
        "postgres-integration-shard-1",
        "postgres-integration-shard-2",
        "postgres-integration-shard-3",
        "qb-real-chain",
        "fixture-browser",
    }
    if set(gates) != expected or any(x != "success" for x in gates.values()):
        fail("required gate missing or not successful")
    return {
        "sha": sha,
        "run_id": run_id,
        "sequence": int(n),
        "version": "0.0.0-test." + n,
        "release_id": "test-ci-" + n,
    }


def decode_json(value):
    try:
        return json.loads(value)
    except (ValueError, TypeError):
        fail("malformed release JSON")


def inspect_image(ref, ident, scratch):
    run("docker", "pull", "--platform", "linux/amd64", ref)
    info = decode_json(run("docker", "image", "inspect", ref))[0]
    labels = info["Config"].get("Labels", {})
    expected = {
        "org.opencontainers.image.revision": ident["sha"],
        "org.opencontainers.image.version": ident["version"],
        "bansoko.run-id": ident["run_id"],
    }
    if (
        any(labels.get(k) != v for k, v in expected.items())
        or info["Architecture"] != "amd64"
        or info["Os"] != "linux"
    ):
        fail("registry image identity/platform differs")
    cid = run("docker", "create", ref)
    try:
        for file in ["bootstrap", "public.key", "bansoko-server"]:
            run("docker", "cp", cid + ":/app/" + file, str(scratch / file))
    finally:
        run("docker", "rm", cid)
    if (scratch / "public.key").read_text().strip() != (
        ROOT / "deployment/public.key"
    ).read_text().strip():
        fail("image signing trust differs")
    source = (ROOT / "cmd/bansoko-bootstrap/main.go").read_text()
    versions = set(re.findall(r"embedded-bootstrap-v[0-9]+", source))
    if len(versions) != 1:
        fail("ambiguous bootstrap source version")
    bootstrap = versions.pop()
    binary = (scratch / "bootstrap").read_bytes()
    if bootstrap.encode() not in binary or {
        x.decode() for x in re.findall(rb"embedded-bootstrap-v[0-9]+", binary)
    } != {bootstrap}:
        fail("image bootstrap bytes differ")
    server = (scratch / "bansoko-server").read_bytes()
    if ident["sha"].encode() not in server or ident["version"].encode() not in server:
        fail("image server bytes lack frozen build identity")
    return bootstrap


def registry_digest(tag, token):
    """Only authenticated absence permits a build; reuse a verified tag on retries."""
    repository_value, host = image_repository()
    url = (
        "https://"
        + host
        + "/v2/"
        + repository_value.split("/", 1)[1]
        + "/manifests/"
        + tag
    )
    auth = base64.b64encode((registry_user() + ":" + token).encode()).decode()
    req = urllib.request.Request(
        url,
        headers={
            "Authorization": "Basic " + auth,
            "Accept": "application/vnd.oci.image.index.v1+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json",
        },
    )
    try:
        with urllib.request.build_opener(NoRedirect()).open(
            req, timeout=120
        ) as response:
            body = response.read()
            digest = "sha256:" + hashlib.sha256(body).hexdigest()
            if (
                response.status != 200
                or response.headers.get("Docker-Content-Digest") != digest
            ):
                fail("registry manifest digest mismatch")
            return digest
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        fail("registry read refused: HTTP " + str(error.code))
    except (urllib.error.URLError, TimeoutError):
        fail("registry read network failure")


def image(ident):
    token = required("BANSOKO_RELEASE_PACKAGE_TOKEN")
    repository_value, registry_host = image_repository()
    # git archive provides only tracked source, never local keys or token files.
    with tempfile.TemporaryDirectory(prefix="bansoko-image-") as temp:
        temp = Path(temp)
        context = temp / "source"
        context.mkdir()
        archive = subprocess.Popen(
            ["git", "archive", ident["sha"]], cwd=ROOT, stdout=subprocess.PIPE
        )
        assert archive.stdout is not None
        subprocess.run(
            ["tar", "-xf", "-", "-C", str(context)], stdin=archive.stdout, check=True
        )
        archive.stdout.close()
        if archive.wait() != 0:
            fail("source archive failed")
        config = temp / "docker"
        config.mkdir(mode=0o700)
        auth = base64.b64encode((registry_user() + ":" + token).encode()).decode()
        p = config / "config.json"
        p.write_text(json.dumps({"auths": {registry_host: {"auth": auth}}}))
        p.chmod(0o600)
        env = dict(os.environ, DOCKER_CONFIG=str(config))
        tag = repository_value + ":" + ident["release_id"]
        epoch = run("git", "show", "-s", "--format=%ct", ident["sha"])
        from datetime import datetime, timezone

        timestamp = (
            datetime.fromtimestamp(int(epoch), timezone.utc)
            .isoformat()
            .replace("+00:00", "Z")
        )
        args = [
            "docker",
            "buildx",
            "build",
            "--platform",
            "linux/amd64",
            "--push",
            "--provenance=false",
            "--tag",
            tag,
            "--metadata-file",
            str(temp / "metadata.json"),
        ]
        for key, value in {
            "VERSION": ident["version"],
            "SOURCE_COMMIT": ident["sha"],
            "BUILD_ID": ident["release_id"],
            "BUILD_TIME": timestamp,
            "BUILD_DIRTY": "false",
        }.items():
            args += ["--build-arg", key + "=" + value]
        for key, value in {
            "org.opencontainers.image.revision": ident["sha"],
            "org.opencontainers.image.version": ident["version"],
            "bansoko.run-id": ident["run_id"],
        }.items():
            args += ["--label", key + "=" + value]
        digest = registry_digest(ident["release_id"], token)
        if digest is None:
            subprocess.run(args + [str(context)], env=env, check=True, cwd=ROOT)
            digest = decode_json((temp / "metadata.json").read_text())[
                "containerimage.digest"
            ]
            if registry_digest(ident["release_id"], token) != digest:
                fail("registry tag readback differs from BuildKit digest")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            fail("image digest artifact invalid")
        old_config = os.environ.get("DOCKER_CONFIG")
        os.environ["DOCKER_CONFIG"] = str(config)
        try:
            bootstrap = inspect_image(repository_value + "@" + digest, ident, temp)
        finally:
            if old_config is None:
                os.environ.pop("DOCKER_CONFIG", None)
            else:
                os.environ["DOCKER_CONFIG"] = old_config
        OUTPUT.mkdir(parents=True, exist_ok=True)
        handoff = dict(
            ident,
            image=repository_value,
            digest=digest,
            bootstrap=bootstrap,
            public_key_sha256=hashlib.sha256(
                (ROOT / "deployment/public.key").read_bytes()
            ).hexdigest(),
        )
        (OUTPUT / "image-handoff.json").write_text(json.dumps(handoff, sort_keys=True))
        print("verified image", digest, bootstrap)


def immutable_upload(url, token, content):
    status, existing = request(url, token)
    if status == 200:
        if existing != content:
            fail("immutable object conflict")
        return
    if status != 404:
        fail("immutable read refused: HTTP " + str(status))
    status, _ = request(url, token, "PUT", content)
    if status not in (201, 409):
        fail("immutable upload refused: HTTP " + str(status))
    status, actual = request(url, token)
    if status != 200 or actual != content:
        fail("immutable readback mismatch")


def publish(ident):
    handoff = decode_json((OUTPUT / "image-handoff.json").read_text())
    repository_value, registry_host = image_repository()
    if (
        any(handoff.get(k) != v for k, v in ident.items())
        or handoff.get("image") != repository_value
    ):
        fail("cross-run/source image handoff rejected")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", handoff.get("digest", "")):
        fail("image digest artifact invalid")
    token = required("BANSOKO_RELEASE_PACKAGE_TOKEN")
    with tempfile.TemporaryDirectory(prefix="bansoko-sign-") as temp:
        temp = Path(temp)
        # Auth/readback from actual registry must occur again before signing.
        config = temp / "config.json"
        config.write_text(
            json.dumps(
                {
                    "auths": {
                        registry_host: {
                            "auth": base64.b64encode(
                                (registry_user() + ":" + token).encode()
                            ).decode()
                        }
                    }
                }
            )
        )
        config.chmod(0o600)
        os.environ["DOCKER_CONFIG"] = str(temp)
        bootstrap = inspect_image(
            repository_value + "@" + handoff["digest"], ident, temp
        )
        if (
            bootstrap != handoff["bootstrap"]
            or handoff["public_key_sha256"]
            != hashlib.sha256(
                (ROOT / "deployment/public.key").read_bytes()
            ).hexdigest()
        ):
            fail("handoff trust/bootstrap mismatch")
        key = temp / "private.key"
        key.write_text(required("BANSOKO_RELEASE_SIGNING_KEY"))
        key.chmod(0o600)
        subprocess.run(
            ["pnpm", "--dir", "frontend", "install", "--frozen-lockfile"],
            cwd=ROOT,
            check=True,
        )
        subprocess.run(["pnpm", "--dir", "frontend", "build"], cwd=ROOT, check=True)
        env = dict(
            os.environ,
            BANSOKO_RELEASE_VERSION=ident["version"],
            BANSOKO_RELEASE_ID=ident["release_id"],
            BANSOKO_RELEASE_SEQUENCE=str(ident["sequence"]),
            BANSOKO_RELEASE_CHANNEL="nightly",
            BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION=bootstrap,
            BANSOKO_BASE_IMAGE_MIN_VERSION="base-v1",
            BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION=bootstrap,
            BANSOKO_RELEASE_PRIVATE_KEY=str(key),
            BANSOKO_RELEASE_PUBLIC_KEY=str(ROOT / "deployment/public.key"),
            BANSOKO_RELEASE_IMAGE_DIGEST=handoff["digest"],
            BANSOKO_RELEASE_OUTPUT_DIR=str(OUTPUT),
            SOURCE_DATE_EPOCH=run("git", "show", "-s", "--format=%ct", ident["sha"]),
        )
        subprocess.run(
            ["bash", "scripts/ci/build-release.sh"], cwd=ROOT, env=env, check=True
        )
        repeated = temp / "repeat-bundle"
        repeat_env = dict(env, BANSOKO_RELEASE_OUTPUT_DIR=str(repeated))
        subprocess.run(
            ["bash", "scripts/ci/build-release.sh"],
            cwd=ROOT,
            env=repeat_env,
            check=True,
        )
        for name in [
            "release-manifest.json",
            "embedded-update-v1.tar.gz",
            "sbom.spdx.json",
            "provenance.json",
            "bansoko-server-linux-amd64",
        ]:
            if (OUTPUT / name).read_bytes() != (repeated / name).read_bytes():
                fail("repeat bundle is not byte-identical: " + name)
        env.update(
            BANSOKO_RELEASE_MANIFEST=str(OUTPUT / "release-manifest.json"),
            BANSOKO_RELEASE_ARTIFACT=str(OUTPUT / "bansoko-server-linux-amd64"),
            BANSOKO_RELEASE_EMBEDDED_PAYLOAD=str(OUTPUT / "embedded-update-v1.tar.gz"),
        )
        subprocess.run(
            ["bash", "scripts/ci/check-release.sh"], cwd=ROOT, env=env, check=True
        )
        published_files = [
            "release-manifest.json",
            "embedded-update-v1.tar.gz",
            "sbom.spdx.json",
            "provenance.json",
        ]
        if destination_enabled(DESTINATION_GITEA):
            for name in published_files:
                immutable_upload(
                    package() + "/" + ident["version"] + "/" + name,
                    token,
                    (OUTPUT / name).read_bytes(),
                )
        readback = temp / "readback"
        readback.mkdir()
        if destination_enabled(DESTINATION_GITEA):
            for name in ["release-manifest.json", "embedded-update-v1.tar.gz"]:
                status, content = request(
                    package() + "/" + ident["version"] + "/" + name, token
                )
                if status != 200:
                    fail("package readback failed")
                (readback / name).write_bytes(content)
        if github_destination_active():
            github_publish(ident, published_files)
            # The same signature verification the Gitea path performs, run over
            # the GitHub copies: the two destinations must not differ in what
            # they prove, so the bytes are read back from the public address and
            # verified, not reused from the local build.
            # These files were written moments ago, so the readback waits out
            # the raw host's five-minute cache rather than treating a stale copy
            # as a broken publication.
            for name in ["release-manifest.json", "embedded-update-v1.tar.gz"]:
                status, content = public_readback(
                    github_file_url(ident["version"], name), (OUTPUT / name).read_bytes()
                )
                if status != 200:
                    fail("github public readback failed")
                (readback / name).write_bytes(content)
        if not readback.exists() or not any(readback.iterdir()):
            fail("no destination published")
        subprocess.run(
            [
                "go",
                "run",
                "./cmd/bansoko-release",
                "verify-embedded",
                "--manifest",
                str(readback / "release-manifest.json"),
                "--payload",
                str(readback / "embedded-update-v1.tar.gz"),
                "--public-key",
                str(ROOT / "deployment/public.key"),
            ],
            cwd=ROOT,
            check=True,
        )
        (OUTPUT / "release-summary.json").write_text(
            json.dumps(
                dict(ident, digest=handoff["digest"], readback_verified=True),
                sort_keys=True,
            )
        )
        print("immutable package readback verified", ident["release_id"])


def public_fetch(url):
    """An unauthenticated GET of a raw-host file: exactly what an instance does.

    Only the raw host is allowed, and a redirect is a failure rather than
    something to follow — the whole point of publishing to raw.githubusercontent
    is that this URL answers 200 directly.
    """
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != "https" or parsed.netloc != urllib.parse.urlsplit(github_raw_base()).netloc:
        fail("unsafe public readback URL")
    req = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.build_opener(NoRedirect()).open(req, timeout=120) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()
    except (urllib.error.URLError, TimeoutError):
        fail("public readback network failure")


def public_readback(url, expected, deadline=420):
    """Read a just-written file back from the raw host, waiting out the CDN.

    The raw host fronts a CDN that keeps serving the previous file for its
    `cache-control: max-age=300` after the write, while the API already reports
    the new blob `sha`. Measured on a scratch repository: the commit landed at
    17:00:47Z and the raw host first returned the new document at 17:05:47Z,
    exactly 300 s, and remained on the old one for every request in between. The
    store is correct immediately; only this cache lags, so the readback waits
    rather than failing, and a genuinely wrong byte still fails at the deadline.
    420 s leaves margin over the measured 300 s.
    """
    deadline_at = time.monotonic() + deadline
    while True:
        status, body = public_fetch(url)
        if status == 200 and body == expected:
            return status, body
        if time.monotonic() >= deadline_at:
            return status, body
        time.sleep(15)


def git_blob_sha(content):
    """The object id GitHub reports for a file: sha1 over git's blob header.

    The contents API returns `sha` at every size, but only inlines base64
    `content` below 1 MB — an embedded payload is ~27 MB, so it comes back with
    `"encoding": "none"` and no content at all. Comparing the blob id is the
    size-independent identity, and it is also the compare-and-swap token.
    """
    return hashlib.sha1(b"blob %d\0" % len(content) + content).hexdigest()


def github_put_file(version, name, content, token):
    """Write one immutable release file to the channel branch, refusing a rewrite.

    The contents API takes the current blob `sha` as the precondition, which is
    the same compare-and-swap Gitea's blob SHA gives: a write only lands on the
    version this call read. An existing file with identical bytes is success
    (a re-run); different bytes for the same version is a refusal, which is
    where immutability comes from.
    """
    hosts = (urllib.parse.urlsplit(github_api_base()).netloc,)
    # The branch is a `ref` parameter, never a path segment: `.../contents/
    # <branch>/<path>` would create an ordinary directory named after the
    # branch, which no raw URL addresses.
    url = github_api_base() + "/repos/" + github_repository() + "/contents/" + version + "/" + name
    status, body = request(url + "?ref=" + BRANCH, token, hosts=hosts)
    options = {
        "branch": BRANCH,
        "content": base64.b64encode(content).decode(),
        "message": "Publish " + version + "/" + name,
    }
    if status == 200:
        if decode_json(body).get("sha") != git_blob_sha(content):
            fail("immutable release file conflict: " + version + "/" + name)
        return
    if status != 404:
        fail("immutable release file read refused: HTTP " + str(status))
    status, _ = request(url, token, "PUT", options, hosts=hosts)
    if status not in (200, 201, 409):
        fail("immutable release file upload refused: HTTP " + str(status))
    status, body = request(url + "?ref=" + BRANCH, token, hosts=hosts)
    if status != 200 or decode_json(body).get("sha") != git_blob_sha(content):
        fail("immutable release file readback mismatch: " + version + "/" + name)


def github_repository_public(token):
    """Whether the channel can be proved publicly readable yet.

    raw.githubusercontent.com answers only anonymously, and it answers 404 for
    every path in a private repository. The update client fetches with
    BANSOKO_UPDATE_AUTH_MODE=none — no credential — so a channel published into
    a private repository is unreachable by the instances it is published for,
    and the unauthenticated readback this lane requires cannot pass either.
    Publishing is therefore held back until the repository is public, and the
    reason is printed rather than left to a confusing 404 further down.
    """
    status, body = request(
        github_api_base() + "/repos/" + github_repository(),
        token,
        hosts=(urllib.parse.urlsplit(github_api_base()).netloc,),
    )
    if status != 200:
        fail("github repository read refused: HTTP " + str(status))
    return decode_json(body).get("private") is False


def github_destination_active():
    """Whether the GitHub destination should run on this trigger.

    Declared is not the same as publishable: raw.githubusercontent.com serves
    nothing from a private repository, so while the repository is private the
    GitHub half is skipped with the reason printed, and the Gitea half (which
    the deployed instance actually reads) still publishes. Once the repository
    is public this returns True with no workflow change, which is the moment
    the branch files become the update channel an instance can read.
    """
    if not destination_enabled(DESTINATION_GITHUB):
        return False
    if github_repository_public(required(GITHUB_TOKEN_ENV)):
        return True
    print(
        "github destination skipped: the repository is private, and "
        "raw.githubusercontent.com answers 404 for every path in one, so the "
        "anonymous readback this lane requires cannot pass"
    )
    return False


def github_ensure_branch(token):
    """Create the channel branch from main's tip when it does not exist yet.

    The contents API cannot write to a branch that is not there, and the first
    GitHub publication is also the first time this branch appears. An existing
    branch is left alone: if it is unreachable for another reason the write
    below still has to prove itself, so this only ever adds.
    """
    hosts = (urllib.parse.urlsplit(github_api_base()).netloc,)
    repository_url = github_api_base() + "/repos/" + github_repository()
    status, _ = request(repository_url + "/branches/" + BRANCH, token, hosts=hosts)
    if status == 200:
        return
    if status != 404:
        fail("channel branch read refused: HTTP " + str(status))
    status, body = request(
        repository_url + "/git/ref/heads/" + GITHUB_BASE_BRANCH, token, hosts=hosts
    )
    if status != 200:
        fail("channel base branch ref unavailable: HTTP " + str(status))
    sha = decode_json(body).get("object", {}).get("sha", "")
    if not re.fullmatch(r"[a-f0-9]{40}", sha):
        fail("channel base branch ref names no commit")
    status, _ = request(
        repository_url + "/git/refs",
        token,
        "POST",
        {"ref": "refs/heads/" + BRANCH, "sha": sha},
        hosts=hosts,
    )
    if status not in (201, 422):
        # 422 is a concurrent creation of the same branch, which is the state
        # this call wanted; anything else leaves the branch absent.
        fail("channel branch create refused: HTTP " + str(status))


def github_publish(ident, files):
    """Upload this version's immutable files and prove they are publicly readable."""
    token = required(GITHUB_TOKEN_ENV)
    github_ensure_branch(token)
    for name in files:
        github_put_file(ident["version"], name, (OUTPUT / name).read_bytes(), token)
    # Read every file back from the raw host, unauthenticated: that is the
    # address an instance actually fetches, so a readback through the API would
    # prove less than this does.
    for name in files:
        expected = (OUTPUT / name).read_bytes()
        code, body = public_readback(github_file_url(ident["version"], name), expected)
        if code != 200 or body != expected:
            fail("public readback failed: " + name)
    print("github release files published and publicly read back", ident["release_id"])


def channel_files(ident, temp):
    """Fetch this version's signed files and prove they name this event.

    Which files depends on where `publish` put them. With dual publication the
    two destinations hold byte-identical copies, so the Gitea copy is read when
    Gitea is enabled and the GitHub copy otherwise.
    """
    if destination_enabled(DESTINATION_GITEA):
        token = required("BANSOKO_RELEASE_PACKAGE_TOKEN")
        package_root = package()
        for name in ["release-manifest.json", "embedded-update-v1.tar.gz"]:
            status, body = request(
                package_root + "/" + ident["version"] + "/" + name, token
            )
            if status != 200:
                fail("channel package readback refused")
            (temp / name).write_bytes(body)
    elif github_destination_active():
        for name in ["release-manifest.json", "embedded-update-v1.tar.gz"]:
            status, body = public_fetch(github_file_url(ident["version"], name))
            if status != 200:
                fail("channel github readback refused")
            (temp / name).write_bytes(body)
    else:
        fail("no destination published")
    manifest = decode_json((temp / "release-manifest.json").read_bytes())
    if any(
        manifest.get(field) != value
        for field, value in {
            "source_commit": ident["sha"],
            "release_id": ident["release_id"],
            "version": ident["version"],
            "release_sequence": ident["sequence"],
        }.items()
    ):
        fail("channel package identity differs from event")


def channel(ident):
    with tempfile.TemporaryDirectory(prefix="bansoko-channel-") as temp:
        temp = Path(temp)
        channel_files(ident, temp)
        key = temp / "private.key"
        key.write_text(required("BANSOKO_RELEASE_SIGNING_KEY"))
        key.chmod(0o600)
        for target, manifest_url, advance in (
            (
                DESTINATION_GITEA,
                package() + "/" + ident["version"] + "/release-manifest.json",
                gitea_channel,
            ),
            (
                DESTINATION_GITHUB,
                github_file_url(ident["version"], "release-manifest.json"),
                github_channel,
            ),
        ):
            active = (
                github_destination_active()
                if target == DESTINATION_GITHUB
                else destination_enabled(target)
            )
            if not active:
                continue
            # One signature per destination: the channel names the manifest URL
            # its own readers can fetch, so a Gitea instance is not sent to a
            # GitHub address and vice versa.
            candidate = temp / ("candidate-" + target + ".json")
            run(
                "go",
                "run",
                "./cmd/bansoko-release",
                "channel-sign",
                "--manifest",
                str(temp / "release-manifest.json"),
                "--payload",
                str(temp / "embedded-update-v1.tar.gz"),
                "--manifest-url",
                manifest_url,
                "--channel",
                "test",
                "--expect-release-sequence",
                str(ident["sequence"]),
                "--public-key",
                str(ROOT / "deployment/public.key"),
                "--private-key",
                str(key),
                "--output",
                str(candidate),
            )
            advance(candidate, ident, temp)


def gitea_channel(candidate, ident, temp):
    write_token = required("BANSOKO_RELEASE_CHANNEL_TOKEN")
    status, body = request(content_url() + "?ref=" + GITEA_BRANCH, write_token)
    published = temp / "published.json"
    options = {
        "branch": GITEA_BRANCH,
        "content": base64.b64encode(candidate.read_bytes()).decode(),
        "message": "Advance signed test to " + ident["release_id"],
    }
    if status == 200:
        info = decode_json(body)
        published.write_bytes(base64.b64decode(info["content"]))
        options["sha"] = info["sha"]
        if published.read_bytes() == candidate.read_bytes():
            status, raw = request(raw_url(), write_token)
            if status != 200 or raw != candidate.read_bytes():
                fail("identical channel raw readback mismatch")
            print("gitea channel already identical and raw readback verified")
            return
    elif status == 404:
        # Must distinguish absent file from inaccessible/missing branch.
        branch_status, _ = request(
            api_base()
            + "/api/v1/repos/"
            + repository()
            + "/branches/"
            + GITEA_BRANCH,
            write_token,
        )
        if branch_status != 200:
            fail("channel branch not accessible")
    else:
        fail("channel read refused: HTTP " + str(status))
    run(
        "go",
        "run",
        "./cmd/bansoko-release",
        "channel-gate",
        "--published",
        str(published) if published.exists() else "",
        "--candidate",
        str(candidate),
        "--public-key",
        str(ROOT / "deployment/public.key"),
    )
    status, _ = request(
        content_url(), write_token, "PUT" if "sha" in options else "POST", options
    )
    if status not in (200, 201):
        fail("channel conditional write rejected: HTTP " + str(status))
    status, body = request(raw_url(), write_token)
    if status != 200 or body != candidate.read_bytes():
        fail("channel raw readback mismatch")
    (temp / "raw.json").write_bytes(body)
    run(
        "go",
        "run",
        "./cmd/bansoko-release",
        "channel-verify",
        "--document",
        str(temp / "raw.json"),
        "--public-key",
        str(ROOT / "deployment/public.key"),
    )
    print("gitea signed channel conditional write/readback verified", ident["release_id"])


def github_content_bytes(info, token):
    """The committed bytes behind a contents-API entry, at any size.

    Below 1 MB the entry carries base64 `content`; above it the entry carries
    `"encoding": "none"` and no content at all. The git blob API answers at
    every size, so it is the fallback rather than a size assumption.
    """
    content = info.get("content") or ""
    if info.get("encoding") == "base64" and content:
        return base64.b64decode(content)
    status, body = request(
        github_api_base() + "/repos/" + github_repository() + "/git/blobs/" + info["sha"],
        token,
        hosts=(urllib.parse.urlsplit(github_api_base()).netloc,),
    )
    if status != 200:
        fail("github blob read refused: HTTP " + str(status))
    return base64.b64decode(decode_json(body)["content"])


def github_channel(candidate, ident, temp):
    """Advance the channel branch blob with the blob sha as its precondition.

    Same compare-and-swap shape as Gitea's: the read returns the sha the write
    must still find. The readback afterwards is unauthenticated against the raw
    host, because that is the URL an instance uses.
    """
    token = required(GITHUB_TOKEN_ENV)
    hosts = (urllib.parse.urlsplit(github_api_base()).netloc,)
    url = github_content_url()
    status, body = request(url + "?ref=" + BRANCH, token, hosts=hosts)
    options = {
        "branch": BRANCH,
        "content": base64.b64encode(candidate.read_bytes()).decode(),
        "message": "Advance signed test to " + ident["release_id"],
    }
    published = temp / "github-published.json"
    if status == 200:
        info = decode_json(body)
        published.write_bytes(github_content_bytes(info, token))
        options["sha"] = info["sha"]
        if published.read_bytes() == candidate.read_bytes():
            expected = candidate.read_bytes()
            code, raw = public_readback(github_raw_url("channel-v1.json"), expected)
            if code != 200 or raw != expected:
                fail("identical github channel raw readback mismatch")
            print("github channel already identical and raw readback verified")
            return
    elif status == 404:
        branch_status, _ = request(
            github_api_base() + "/repos/" + github_repository() + "/branches/" + BRANCH,
            token,
            hosts=hosts,
        )
        if branch_status != 200:
            fail("github channel branch not accessible")
    else:
        fail("github channel read refused: HTTP " + str(status))
    run(
        "go",
        "run",
        "./cmd/bansoko-release",
        "channel-gate",
        "--published",
        str(published) if published.exists() else "",
        "--candidate",
        str(candidate),
        "--public-key",
        str(ROOT / "deployment/public.key"),
    )
    # PUT creates the file when it is absent and updates it when `sha` names the
    # blob that was just read; the contents API has no POST form (a POST there
    # answers 404), so the method is always PUT and `sha` carries the
    # compare-and-swap.
    status, _ = request(url, token, "PUT", options, hosts=hosts)
    if status not in (200, 201):
        fail("github channel conditional write rejected: HTTP " + str(status))
    expected = candidate.read_bytes()
    code, raw = public_readback(github_raw_url("channel-v1.json"), expected)
    if code != 200 or raw != expected:
        fail("github channel raw readback mismatch")
    (temp / "github-raw.json").write_bytes(raw)
    run(
        "go",
        "run",
        "./cmd/bansoko-release",
        "channel-verify",
        "--document",
        str(temp / "github-raw.json"),
        "--public-key",
        str(ROOT / "deployment/public.key"),
    )
    print("github signed channel conditional write/readback verified", ident["release_id"])


if __name__ == "__main__":
    try:
        ident = identity()
        {"image": image, "publish": publish, "channel": channel}[sys.argv[1]](ident)
    except (
        RuntimeError,
        ValueError,
        KeyError,
        OSError,
        subprocess.SubprocessError,
    ) as error:
        # Never dump requests, credentials or arbitrary server response bodies.
        print(
            "ERROR: test release refused:",
            type(error).__name__,
            str(error)
            if isinstance(error, RuntimeError)
            else "operation failed; inspect non-secret build log",
            file=sys.stderr,
        )
        sys.exit(1)
