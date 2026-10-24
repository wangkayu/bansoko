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
BRANCH = "update-channel"
# The channel branch is created from this one on first publication. GitHub has
# no "empty branch" concept, so the branch has to start somewhere, and main is
# the only ref this run has already proved it is building.
GITHUB_BASE_BRANCH = "main"
OUTPUT = ROOT / ".local-artifacts/release-ci"

# One publication destination (GitHub) and one image registry (Docker Hub).
# The repository and image path are public, so they are named here rather than
# carried as configuration: a secret would mask them in the log for no gain.
# The registry speaks OCI with a Bearer token minted by its auth realm; the
# docker CLI does the same exchange from a static login entry.
GITHUB_TOKEN_ENV = "BANSOKO_RELEASE_GITHUB_TOKEN"
IMAGE_REPOSITORY = "docker.io/wangkayu/bansoko"
IMAGE_PATH = "wangkayu/bansoko"
HUB_REGISTRY_HOST = "index.docker.io"
HUB_AUTH_URL = "https://auth.docker.io/token"
# The key `docker login` writes for Docker Hub; the docker CLI exchanges it
# for a registry token exactly like hub_token() below.
HUB_DOCKER_AUTH_KEY = "https://index.docker.io/v1/"
HUB_USER = "wangkayu"
HUB_TOKEN_ENV = "DOCKERHUB_TOKEN"


def fail(message) -> "NoReturn":
    raise RuntimeError(message)


def required(name):
    value = os.environ.get(name, "")
    if not value:
        fail("missing " + name)
    return value


def github_api_base():
    return "https://api.github.com"


def github_raw_base():
    # The update client refuses a cross-host redirect and a query string. A
    # github.com/<o>/<r>/raw/... URL is a 302 to raw.githubusercontent.com, so
    # the channel is published and consumed at the final host directly.
    return "https://raw.githubusercontent.com"


def github_repository():
    """The GitHub repository this run publishes to.

    GitHub's identity is the repository the workflow runs in, which Actions
    already provides, so the channel files can never be aimed at a path other
    than the one this run was started for.
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
    comes from the same place it always comes from: identical bytes for one
    release_sequence, a signature over the manifest, and a client that only
    accepts a larger sequence.
    """
    return github_raw_url(version + "/" + name)


def hub_token():
    """One registry Bearer token, minted from the static Hub credential.

    index.docker.io answers only tokens issued by its auth realm, so every
    registry call starts here. The scope is exactly this image repository: the
    token cannot read or write any other namespace even if it leaks.
    """
    query = urllib.parse.urlencode(
        {"service": "registry.docker.io", "scope": "repository:" + IMAGE_PATH + ":pull,push"}
    )
    auth = base64.b64encode(
        (HUB_USER + ":" + required(HUB_TOKEN_ENV)).encode()
    ).decode()
    req = urllib.request.Request(
        HUB_AUTH_URL + "?" + query, headers={"Authorization": "Basic " + auth}
    )
    try:
        with urllib.request.build_opener(NoRedirect()).open(req, timeout=120) as response:
            token = decode_json(response.read()).get("token", "")
    except urllib.error.HTTPError as error:
        fail("registry auth refused: HTTP " + str(error.code))
    except (urllib.error.URLError, TimeoutError):
        fail("registry auth network failure")
    if not token:
        fail("registry auth returned no token")
    return token


def run(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env).decode().strip()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def request(url, token, method="GET", data=None, hosts=None):
    """One HTTPS call, to an allowlisted host only.

    `hosts` defaults to the GitHub API host. The registry calls build their own
    requests (Bearer token, Accept header), so nothing else can be aimed at a
    host the deployment did not choose.
    """
    parsed = urllib.parse.urlsplit(url)
    if hosts is None:
        hosts = (urllib.parse.urlsplit(github_api_base()).netloc,)
    if (
        parsed.scheme != "https"
        or parsed.netloc not in hosts
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


def registry_digest(tag, jwt):
    """Only authenticated absence permits a build; reuse a verified tag on retries.

    The registry's API answers a Bearer token from hub_token(); the returned
    digest is computed over the exact manifest bytes and cross-checked against
    the Docker-Content-Digest header, so a lying proxy cannot split the two.
    """
    url = (
        "https://" + HUB_REGISTRY_HOST + "/v2/" + IMAGE_PATH + "/manifests/" + tag
    )
    req = urllib.request.Request(
        url,
        headers={
            "Authorization": "Bearer " + jwt,
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
    token = required(HUB_TOKEN_ENV)
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
        auth = base64.b64encode((HUB_USER + ":" + token).encode()).decode()
        p = config / "config.json"
        p.write_text(json.dumps({"auths": {HUB_DOCKER_AUTH_KEY: {"auth": auth}}}))
        p.chmod(0o600)
        env = dict(os.environ, DOCKER_CONFIG=str(config))
        tag = IMAGE_REPOSITORY + ":" + ident["release_id"]
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
        digest = registry_digest(ident["release_id"], hub_token())
        if digest is None:
            subprocess.run(args + [str(context)], env=env, check=True, cwd=ROOT)
            digest = decode_json((temp / "metadata.json").read_text())[
                "containerimage.digest"
            ]
            if registry_digest(ident["release_id"], hub_token()) != digest:
                fail("registry tag readback differs from BuildKit digest")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            fail("image digest artifact invalid")
        old_config = os.environ.get("DOCKER_CONFIG")
        os.environ["DOCKER_CONFIG"] = str(config)
        try:
            bootstrap = inspect_image(IMAGE_REPOSITORY + "@" + digest, ident, temp)
        finally:
            if old_config is None:
                os.environ.pop("DOCKER_CONFIG", None)
            else:
                os.environ["DOCKER_CONFIG"] = old_config
        OUTPUT.mkdir(parents=True, exist_ok=True)
        handoff = dict(
            ident,
            image=IMAGE_REPOSITORY,
            digest=digest,
            bootstrap=bootstrap,
            public_key_sha256=hashlib.sha256(
                (ROOT / "deployment/public.key").read_bytes()
            ).hexdigest(),
        )
        (OUTPUT / "image-handoff.json").write_text(json.dumps(handoff, sort_keys=True))
        print("verified image", digest, bootstrap)


def publish(ident):
    handoff = decode_json((OUTPUT / "image-handoff.json").read_text())
    if (
        any(handoff.get(k) != v for k, v in ident.items())
        or handoff.get("image") != IMAGE_REPOSITORY
    ):
        fail("cross-run/source image handoff rejected")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", handoff.get("digest", "")):
        fail("image digest artifact invalid")
    with tempfile.TemporaryDirectory(prefix="bansoko-sign-") as temp:
        temp = Path(temp)
        # Auth/readback from the actual registry must occur again before signing.
        config = temp / "config.json"
        config.write_text(
            json.dumps(
                {
                    "auths": {
                        HUB_DOCKER_AUTH_KEY: {
                            "auth": base64.b64encode(
                                (HUB_USER + ":" + required(HUB_TOKEN_ENV)).encode()
                            ).decode()
                        }
                    }
                }
            )
        )
        config.chmod(0o600)
        os.environ["DOCKER_CONFIG"] = str(temp)
        bootstrap = inspect_image(
            IMAGE_REPOSITORY + "@" + handoff["digest"], ident, temp
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
        require_public_github(required(GITHUB_TOKEN_ENV))
        github_publish(ident, published_files)
        # The same signature verification the signed files must survive, run
        # over the public copies: the bytes are read back from the raw host and
        # verified, not reused from the local build. These files were written
        # moments ago, so the readback waits out the raw host's five-minute
        # cache rather than treating a stale copy as a broken publication.
        readback = temp / "readback"
        readback.mkdir()
        for name in ["release-manifest.json", "embedded-update-v1.tar.gz"]:
            status, content = public_readback(
                github_file_url(ident["version"], name), (OUTPUT / name).read_bytes()
            )
            if status != 200:
                fail("github public readback failed")
            (readback / name).write_bytes(content)
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

    The contents API takes the current blob `sha` as the precondition, so a
    write only lands on the version this call read. An existing file with identical bytes is success
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


def require_public_github(token):
    """Refuse to publish unless the channel can be proved publicly readable.

    raw.githubusercontent.com answers only anonymously, and it answers 404 for
    every path in a private repository. The update client always fetches
    anonymously — there is no credential surface — so a channel published into
    a private repository is unreachable by the instances it is published for,
    and the unauthenticated readback this lane requires cannot pass either.
    Unlike the earlier dual-destination arrangement there is no second channel
    to fall back to, so this is a hard failure rather than a skipped half.
    """
    status, body = request(
        github_api_base() + "/repos/" + github_repository(),
        token,
        hosts=(urllib.parse.urlsplit(github_api_base()).netloc,),
    )
    if status != 200:
        fail("github repository read refused: HTTP " + str(status))
    if decode_json(body).get("private") is not False:
        fail(
            "github destination unavailable: the repository is not public, and "
            "raw.githubusercontent.com answers 404 for every path in a private "
            "one, so the anonymous readback this lane requires cannot pass"
        )


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

    The files are read from the public raw host — the address an instance
    actually fetches — not reused from the local build.
    """
    for name in ["release-manifest.json", "embedded-update-v1.tar.gz"]:
        status, body = public_fetch(github_file_url(ident["version"], name))
        if status != 200:
            fail("channel github readback refused")
        (temp / name).write_bytes(body)
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
        manifest_url = github_file_url(ident["version"], "release-manifest.json")
        candidate = temp / "candidate.json"
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
        require_public_github(required(GITHUB_TOKEN_ENV))
        github_channel(candidate, ident, temp)


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

    The read returns the sha the write must still find. The readback afterwards
    is unauthenticated against the raw host, because that is the URL an
    instance uses.
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
