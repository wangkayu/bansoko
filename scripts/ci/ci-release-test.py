#!/usr/bin/env python3
"""Independent refusal tests invoke the real publisher, not a duplicated policy."""

import base64
import hashlib
import importlib.util
import json
import os
import tempfile
import unittest
import urllib.error
from email.message import Message
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "release", Path(__file__).with_name("ci-release.py")
)
assert spec and spec.loader
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.sha = "a" * 40
        self.gates = {
            x: {"result": "success"}
            for x in [
                "contract-boundary",
                "go-unit-static",
                "frontend-quality",
                "postgres-integration-shard-1",
                "postgres-integration-shard-2",
                "postgres-integration-shard-3",
                "qb-real-chain",
                "fixture-browser",
            ]
        }
        # The one repository identity the publisher still reads from the
        # environment is the repository the workflow runs in; the image
        # repository is a pinned constant, not configuration.
        self.destination = {"GITHUB_REPOSITORY": "gh-owner/gh-repo"}
        destination = patch.dict(os.environ, self.destination)
        destination.start()
        self.addCleanup(destination.stop)
        self.env = {
            "GITHUB_EVENT_NAME": "push",
            "GITHUB_REF": "refs/tags/v0.1.0",
            "GITHUB_SHA": self.sha,
            "GITHUB_RUN_ID": "71",
            "GITHUB_RUN_NUMBER": "53",
            "BANSOKO_RELEASE_REF_PROTECTED": "true",
            "BANSOKO_REQUIRED_RESULTS": json.dumps(self.gates),
            **self.destination,
        }

    def test_single_destination_constants_are_pinned(self):
        # One destination (GitHub) and one registry (Docker Hub), named in the
        # open: the repository and image path are public values, and carrying
        # them as secrets would only mask them in the log for no gain. The
        # published manifest names IMAGE_REPOSITORY, so drift here breaks the
        # pull on every deployed instance at once — the pin is the contract.
        self.assertEqual(release.GITHUB_TOKEN_ENV, "BANSOKO_RELEASE_GITHUB_TOKEN")
        self.assertEqual(release.IMAGE_REPOSITORY, "docker.io/wangkayu/bansoko")
        self.assertEqual(release.IMAGE_PATH, "wangkayu/bansoko")
        self.assertTrue(release.IMAGE_REPOSITORY.endswith("/" + release.IMAGE_PATH))
        self.assertEqual(release.HUB_REGISTRY_HOST, "index.docker.io")
        self.assertEqual(release.HUB_AUTH_URL, "https://auth.docker.io/token")
        # The docker CLI exchanges this static-login key for a registry token
        # exactly like hub_token(); the two halves must never drift.
        self.assertEqual(release.HUB_DOCKER_AUTH_KEY, "https://index.docker.io/v1/")
        self.assertEqual(release.HUB_USER, "wangkayu")
        self.assertEqual(release.HUB_TOKEN_ENV, "DOCKERHUB_TOKEN")

    def test_github_repository_is_validated(self):
        # The channel is addressed with the repository the workflow runs in; a
        # malformed value must be refused, never guessed around.
        self.assertEqual(release.github_repository(), "gh-owner/gh-repo")
        for value in [
            "",
            "owner",
            "owner/repo/extra",
            "own er/repo",
            "owner//repo",
            "/repo",
            "owner/",
        ]:
            with (
                self.subTest(value=value),
                patch.dict(os.environ, {"GITHUB_REPOSITORY": value}),
                self.assertRaises(RuntimeError),
            ):
                release.github_repository()
        with (
            patch.dict(os.environ, {}, clear=True),
            self.assertRaises(RuntimeError),
        ):
            release.github_repository()

    def test_event_identity_and_each_gate(self):
        with (
            patch.dict(os.environ, self.env, clear=True),
            patch.object(release, "run", side_effect=[self.sha, ""]),
        ):
            ident = release.identity()
            self.assertEqual(ident["release_id"], "v0.1.0")
            self.assertEqual(ident["version"], "0.1.0")
        for name in self.gates:
            for state in ["failure", "skipped", "cancelled", "missing"]:
                gates = dict(self.gates, **{name: {"result": state}})
                with (
                    self.subTest(name=name, state=state),
                    patch.dict(
                        os.environ,
                        dict(self.env, BANSOKO_REQUIRED_RESULTS=json.dumps(gates)),
                        clear=True,
                    ),
                    patch.object(release, "run", side_effect=[self.sha, ""]),
                    self.assertRaises(RuntimeError),
                ):
                    release.identity()
        # A main push builds only the test image; it never publishes. Neither
        # does any tag that is not a plain vX.Y.Z.
        refs = [
            "refs/heads/main",
            "refs/heads/v0.1.0",
            "refs/tags/0.1.0",
            "refs/tags/v0.1",
            "refs/tags/v0.1.0-rc.1",
            "refs/tags/v0.1.0+build",
            "refs/tags/v01.0.0",
            "refs/tags/v0.1.0/x",
            "",
        ]
        for key, value in [("GITHUB_EVENT_NAME", "pull_request"), ("GITHUB_EVENT_NAME", "workflow_dispatch")] + [
            ("GITHUB_REF", ref) for ref in refs
        ] + [
            ("BANSOKO_RELEASE_REF_PROTECTED", "false"),
            ("GITHUB_SHA", "invalid"),
        ]:
            with (
                self.subTest(key=key, value=value),
                patch.dict(os.environ, dict(self.env, **{key: value}), clear=True),
                patch.object(release, "run", side_effect=[self.sha, ""]),
                self.assertRaises(RuntimeError),
            ):
                release.identity()
        with (
            patch.dict(os.environ, dict(self.env, GITHUB_REF="refs/tags/v12.3.40"), clear=True),
            patch.object(release, "run", side_effect=[self.sha, ""]),
        ):
            ident = release.identity()
            self.assertEqual((ident["version"], ident["release_id"], ident["sequence"]), ("12.3.40", "v12.3.40", 53))
        with (
            patch.dict(os.environ, self.env, clear=True),
            patch.object(release, "run", return_value="b" * 40),
            self.assertRaises(RuntimeError),
        ):
            release.identity()

    def test_hub_token_is_scoped_and_fails_closed(self):
        # index.docker.io answers only Bearer tokens minted by its auth realm,
        # and the mint must be scoped to exactly this image repository: the
        # token cannot touch another namespace even if it leaks. No network may
        # happen without the static credential.
        with (
            patch.dict(os.environ, {}, clear=True),
            patch.object(release.urllib.request, "build_opener") as opener,
        ):
            with self.assertRaises(RuntimeError):
                release.hub_token()
            opener.assert_not_called()
        with (
            patch.dict(os.environ, {"DOCKERHUB_TOKEN": "hub-static"}),
            patch.object(release.urllib.request, "build_opener") as opener,
        ):
            response = opener.return_value.open.return_value.__enter__.return_value
            response.read.return_value = json.dumps({"token": "jwt"}).encode()
            self.assertEqual(release.hub_token(), "jwt")
            req = opener.return_value.open.call_args.args[0]
            self.assertTrue(req.full_url.startswith(release.HUB_AUTH_URL + "?"))
            self.assertIn("service=registry.docker.io", req.full_url)
            self.assertIn(
                "scope=repository%3Awangkayu%2Fbansoko%3Apull%2Cpush",
                req.full_url,
            )
            self.assertEqual(
                req.get_header("Authorization"),
                "Basic " + base64.b64encode(b"wangkayu:hub-static").decode(),
            )
            # An empty token is a refusal, not a None handed to Bearer.
            response.read.return_value = json.dumps({"token": ""}).encode()
            with self.assertRaises(RuntimeError):
                release.hub_token()
            error = urllib.error.HTTPError(
                release.HUB_AUTH_URL, 401, "refused", Message(), None
            )
            opener.return_value.open.side_effect = error
            with self.assertRaises(RuntimeError):
                release.hub_token()

    def test_registry_authenticated_absence_and_refusals(self):
        for code in [401, 403, 500, 302, 404]:
            error = urllib.error.HTTPError(
                "https://index.docker.io/", code, "refused", Message(), None
            )
            with patch.object(release.urllib.request, "build_opener") as opener:
                opener.return_value.open.side_effect = error
                if code == 404:
                    self.assertIsNone(
                        release.registry_digest("v0.1.0", "sentinel")
                    )
                else:
                    with self.assertRaises(RuntimeError):
                        release.registry_digest("v0.1.0", "sentinel")
        body = b"registry-manifest"
        digest = "sha256:" + hashlib.sha256(body).hexdigest()
        with patch.object(release.urllib.request, "build_opener") as opener:
            response = opener.return_value.open.return_value.__enter__.return_value
            response.status = 200
            response.read.return_value = body
            response.headers = {"Docker-Content-Digest": digest}
            self.assertEqual(
                release.registry_digest("v0.1.0", "sentinel"), digest
            )
            req = opener.return_value.open.call_args.args[0]
            self.assertEqual(
                req.full_url,
                "https://" + release.HUB_REGISTRY_HOST
                + "/v2/" + release.IMAGE_PATH + "/manifests/v0.1.0",
            )
            self.assertEqual(req.get_header("Authorization"), "Bearer sentinel")
            self.assertIn(
                "application/vnd.oci.image.index.v1+json", req.get_header("Accept")
            )
            response.headers = {"Docker-Content-Digest": "sha256:" + "0" * 64}
            with self.assertRaises(RuntimeError):
                release.registry_digest("v0.1.0", "sentinel")

    def test_cross_run_handoff_refuses_before_effects(self):
        with tempfile.TemporaryDirectory() as temp:
            p = Path(temp)
            (p / "image-handoff.json").write_text(
                json.dumps({"sha": self.sha, "run_id": "old"})
            )
            with (
                patch.object(release, "OUTPUT", p),
                patch.object(release, "inspect_image") as inspect,
                patch.object(release, "request") as request,
            ):
                with self.assertRaises(RuntimeError):
                    release.publish({"sha": self.sha, "run_id": "71"})
                inspect.assert_not_called()
                request.assert_not_called()

    def test_handoff_naming_a_foreign_image_is_refused_before_effects(self):
        # The manifest may only ever name the Hub repository. A handoff left
        # over from the retired dual-destination arrangement — or any other
        # registry — must stop the run before a byte is signed or written.
        ident = {
            "sha": self.sha,
            "run_id": "71",
            "sequence": 53,
            "version": "0.1.0",
            "release_id": "v0.1.0",
        }
        handoff = dict(
            ident,
            image="registry.example.test/owner/bansoko",
            digest="sha256:" + "1" * 64,
        )
        with tempfile.TemporaryDirectory() as temp:
            p = Path(temp)
            (p / "image-handoff.json").write_text(json.dumps(handoff))
            with (
                patch.object(release, "OUTPUT", p),
                patch.object(release, "inspect_image") as inspect,
                patch.object(release, "request") as request,
            ):
                with self.assertRaises(RuntimeError):
                    release.publish(ident)
                inspect.assert_not_called()
                request.assert_not_called()

    def test_unsafe_url_has_no_request(self):
        for url in [
            "http://example.test/x",
            "https://outside.invalid/x",
            "https://secret@example.test/x",
            "https://example.test/x#secret",
        ]:
            with patch.object(release.urllib.request, "build_opener") as opener:
                with self.assertRaises(RuntimeError):
                    release.request(url, "sentinel")
                opener.assert_not_called()

    def test_the_baked_channel_url_names_the_same_branch_the_publisher_writes(self):
        # The image fetches the channel from the URL baked at build time; the
        # publisher signs manifest URLs onto its own branch. If those names
        # drift, every published manifest lands outside the baked package root
        # and the resolver refuses it: fail-closed, but updates quietly stop.
        # (internal/update/application/image_defaults_test.go pins the baked
        # half's shape; nothing else joins the two, so this does.)
        dockerfile = (release.ROOT / "Dockerfile").read_text()
        baked = [
            line.split("=", 1)[1].rstrip("\\").strip()
            for line in dockerfile.splitlines()
            if line.lstrip().startswith("BANSOKO_UPDATE_CHANNEL_URL=")
        ]
        self.assertEqual(len(baked), 1, "the image must bake exactly one channel URL")
        self.assertIn("/" + release.BRANCH + "/", baked[0])

    def test_github_addresses_avoid_the_redirect_and_query(self):
        # The image bakes github.com/<o>/<r>/raw/..., which is a cross-host 302,
        # and release assets, which are a 302 with a signed query. The update
        # client refuses both, so the published address must be neither.
        raw = release.github_raw_url("channel-v1.json")
        self.assertEqual(
            raw,
            "https://raw.githubusercontent.com/gh-owner/gh-repo/update-channel/channel-v1.json",
        )
        asset = release.github_file_url("0.1.0", "release-manifest.json")
        self.assertEqual(
            asset,
            "https://raw.githubusercontent.com/gh-owner/gh-repo/update-channel/"
            "0.1.0/release-manifest.json",
        )
        for url in (raw, asset):
            parsed = release.urllib.parse.urlsplit(url)
            self.assertEqual(parsed.scheme, "https")
            self.assertEqual(parsed.netloc, "raw.githubusercontent.com")
            # The client's fences: no query, no userinfo, no fragment.
            self.assertEqual(parsed.query, "")
            self.assertEqual(parsed.username, None)
            self.assertEqual(parsed.fragment, "")

    def test_request_host_allowlist_is_explicit(self):
        # The default allowlist is exactly the GitHub API host: a raw-host,
        # registry or arbitrary URL never goes out through request() (those
        # callers build their own requests), and a declared host still refuses
        # a near miss.
        with patch.object(release.urllib.request, "build_opener") as opener:
            release.request("https://api.github.com/x", "sentinel")
            self.assertTrue(opener.return_value.open.called)
        for url in [
            "https://raw.githubusercontent.com/o/r/f",
            "https://index.docker.io/v2/wangkayu/bansoko/manifests/x",
            "https://evil.example.test/x",
        ]:
            with patch.object(release.urllib.request, "build_opener") as opener:
                with self.assertRaises(RuntimeError):
                    release.request(url, "sentinel")
                opener.assert_not_called()
            with patch.object(release.urllib.request, "build_opener") as opener:
                with self.assertRaises(RuntimeError):
                    # A declared host is not a licence to drop the other fences.
                    release.request(
                        "https://api.github.com/x#frag",
                        "sentinel",
                        hosts=("api.github.com",),
                    )
                opener.assert_not_called()

    def test_public_readback_refuses_a_redirect(self):
        # A 302 here would mean the published address is not the one an instance
        # can use, which is the whole reason the channel moved to raw.
        with patch.object(
            release.urllib.request, "build_opener"
        ) as opener:
            with self.assertRaises(RuntimeError):
                release.public_fetch("https://github.com/owner/repo/raw/x.json")
            opener.assert_not_called()

    def test_readback_waits_out_the_raw_host_cache(self):
        # Measured: raw serves `cache-control: max-age=300`, and 160 s after a
        # commit it still returned the previous document while the API already
        # reported the new blob sha. A stale 200 is therefore not a broken
        # publication; it is the CDN, and the readback must wait for it.
        calls = []
        def fetch(url):
            calls.append(url)
            return (200, b"old") if len(calls) < 3 else (200, b"new")
        with (
            patch.object(release, "public_fetch", side_effect=fetch),
            patch.object(release.time, "sleep"),
        ):
            status, body = release.public_readback("https://raw.githubusercontent.com/o/r/b/f", b"new")
        self.assertEqual((status, body), (200, b"new"))
        self.assertEqual(len(calls), 3, "a stale 200 must be retried, not accepted")
        # A wrong byte still fails once the deadline passes: the wait is bounded.
        with (
            patch.object(release, "public_fetch", return_value=(200, b"wrong")),
            patch.object(release.time, "sleep"),
            patch.object(release.time, "monotonic", side_effect=[0, 0, 10_000]),
        ):
            status, body = release.public_readback("https://raw.githubusercontent.com/o/r/b/f", b"new")
        self.assertEqual(body, b"wrong")
        # And a fetch that never reaches 200 terminates rather than looping.
        with (
            patch.object(release, "public_fetch", return_value=(404, b"")),
            patch.object(release.time, "sleep"),
            patch.object(release.time, "monotonic", side_effect=[0, 0, 10_000]),
        ):
            status, _ = release.public_readback("https://raw.githubusercontent.com/o/r/b/f", b"new")
        self.assertEqual(status, 404)

    def test_channel_branch_is_created_only_when_absent(self):
        hosts = ("api.github.com",)
        existing = [(200, b"{}")]
        with patch.object(release, "request", side_effect=existing) as request:
            release.github_ensure_branch("sentinel")
            # An existing branch is never re-created or moved.
            self.assertEqual(request.call_count, 1)
            self.assertEqual(request.call_args.args[0], "https://api.github.com/repos/gh-owner/gh-repo/branches/update-channel")
        created = [
            (404, b""),
            (200, json.dumps({"object": {"sha": self.sha}}).encode()),
            (201, b"{}"),
        ]
        with patch.object(release, "request", side_effect=created) as request:
            release.github_ensure_branch("sentinel")
            create = request.call_args_list[2]
            self.assertEqual(create.args[2], "POST")
            self.assertEqual(create.args[3]["ref"], "refs/heads/update-channel")
            self.assertEqual(create.args[3]["sha"], self.sha)
            # The API host is still declared, not inferred from the call site.
            self.assertEqual(create.kwargs["hosts"], hosts)
        # A refused creation must not fall through to a publish that would then
        # fail on a missing branch with a misleading error.
        with (
            patch.object(
                release,
                "request",
                side_effect=[
                    (404, b""),
                    (200, json.dumps({"object": {"sha": self.sha}}).encode()),
                    (403, b""),
                ],
            ),
            self.assertRaises(RuntimeError),
        ):
            release.github_ensure_branch("sentinel")
        # A base ref with no commit cannot start a branch.
        with (
            patch.object(release, "request", side_effect=[(404, b""), (200, b"{}")]),
            self.assertRaises(RuntimeError),
        ):
            release.github_ensure_branch("sentinel")
        # An unreadable branch state is a refusal, not an assumption of absence.
        with (
            patch.object(release, "request", side_effect=[(403, b"")]),
            self.assertRaises(RuntimeError),
        ):
            release.github_ensure_branch("sentinel")

    def test_channel_bytes_are_read_at_any_size(self):
        # The channel document is small today, but a reader that assumes inline
        # base64 stops working the moment it crosses 1 MB — the same defect that
        # broke the payload readback. Above that size the blob API answers.
        payload = b'{"sequence":7}'
        info = {"sha": "a" * 40, "encoding": "base64",
                "content": base64.b64encode(payload).decode()}
        with patch.object(release, "request") as request:
            self.assertEqual(release.github_content_bytes(info, "sentinel"), payload)
            request.assert_not_called()
        info = {"sha": "b" * 40, "encoding": "none", "content": ""}
        with patch.object(
            release,
            "request",
            return_value=(200, json.dumps({"content": base64.b64encode(payload).decode()}).encode()),
        ) as request:
            self.assertEqual(release.github_content_bytes(info, "sentinel"), payload)
            self.assertIn("/git/blobs/" + "b" * 40, request.call_args.args[0])
        with (
            patch.object(release, "request", return_value=(403, b"")),
            self.assertRaises(RuntimeError),
        ):
            release.github_content_bytes(info, "sentinel")

    def test_immutable_files_are_compared_by_blob_sha_not_inline_content(self):
        # Above 1 MB the contents API returns `"encoding": "none"` with an empty
        # `content`, and the embedded payload is ~27 MB, so a comparison against
        # inline base64 would fail on every real publication. `sha` is the git
        # blob id and is present at every size.
        payload = b"x" * 2_000_000
        blob = release.git_blob_sha(payload)
        existing = (200, json.dumps({"sha": blob, "encoding": "none", "content": ""}).encode())
        with patch.object(release, "request", side_effect=[existing]) as request:
            release.github_put_file("0.1.0", "embedded-update-v1.tar.gz", payload, "sentinel")
            # Identical bytes are a successful re-run: no write at all.
            self.assertEqual(request.call_count, 1)
        # The read the sha is compared against must be pinned to the branch.
        self.assertIn("?ref=update-channel", request.call_args.args[0])
        # Different bytes for the same version is the immutability refusal.
        with (
            patch.object(
                release,
                "request",
                side_effect=[(200, json.dumps({"sha": "f" * 40}).encode())],
            ),
            self.assertRaises(RuntimeError),
        ):
            release.github_put_file("0.1.0", "embedded-update-v1.tar.gz", payload, "sentinel")

    def test_immutable_write_is_proved_by_the_readback_after_it(self):
        # A refused or ambiguous PUT must never be reported as published. The
        # readback after the write is what makes a lying status harmless: 409 is
        # a concurrent write only if the readback shows these exact bytes.
        payload = b"immutable-payload"
        blob = release.git_blob_sha(payload)
        proven = (200, json.dumps({"sha": blob}).encode())
        with patch.object(
            release, "request", side_effect=[(404, b""), (201, b""), proven]
        ) as request:
            release.github_put_file("0.1.0", "release-manifest.json", payload, "sentinel")
            self.assertEqual(request.call_count, 3)
        with patch.object(
            release, "request", side_effect=[(404, b""), (409, b""), proven]
        ):
            release.github_put_file("0.1.0", "release-manifest.json", payload, "sentinel")
        with (
            patch.object(
                release,
                "request",
                side_effect=[(404, b""), (409, b""), (200, json.dumps({"sha": "f" * 40}).encode())],
            ),
            self.assertRaises(RuntimeError),
        ):
            release.github_put_file("0.1.0", "release-manifest.json", payload, "sentinel")
        with (
            patch.object(release, "request", side_effect=[(404, b""), (503, b"")]),
            self.assertRaises(RuntimeError),
        ):
            release.github_put_file("0.1.0", "release-manifest.json", payload, "sentinel")

    def test_git_blob_sha_matches_gits_object_id(self):
        # Anchor the identity against a value git itself computes, so the helper
        # cannot drift into "some hash of the bytes".
        self.assertEqual(
            release.git_blob_sha(b"hello\n"),
            "ce013625030ba8dba906f756967f9e9ca394464a",
        )
        self.assertEqual(release.git_blob_sha(b""), "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391")

    def test_channel_creation_uses_put_because_the_contents_api_has_no_post(self):
        # A POST to /contents/{path} answers 404 on GitHub: creating a file is a
        # PUT without `sha`. The first GitHub publication is also the first time
        # channel-v1.json exists, so this is the path every fresh repository
        # takes.
        with tempfile.TemporaryDirectory() as temp:
            temp = Path(temp)
            candidate = temp / "candidate.json"
            candidate.write_bytes(b'{"signed":true}')
            with (
                patch.object(
                    release,
                    "request",
                    # channel read 404 (absent) → branch probe 200 → the PUT
                    side_effect=[(404, b""), (200, b"{}"), (201, b"{}")],
                ) as request,
                patch.object(release, "public_fetch", side_effect=[(200, candidate.read_bytes())]),
                patch.object(release, "run"),
                patch.dict(os.environ, {"BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel"}),
            ):
                release.github_channel(candidate, {"release_id": "test.53"}, temp)
            write = [call for call in request.call_args_list if call.args[2:3] == ("PUT",)]
            self.assertEqual(len(write), 1, "channel creation must be a PUT")
            self.assertNotIn("sha", write[0].args[3])

    def test_github_channel_swaps_the_blob_with_the_read_sha(self):
        # The channel advance is a compare-and-swap: the write carries the sha
        # the read returned, so a concurrent advance between read and write is
        # refused by the API instead of silently landing on a stale base.
        ident = {"release_id": "v0.1.0"}
        with tempfile.TemporaryDirectory() as temp:
            temp = Path(temp)
            candidate = temp / "candidate.json"
            candidate.write_bytes(b'{"sequence":53}')
            old = b'{"sequence":52}'
            info = {
                "sha": "c" * 40,
                "encoding": "base64",
                "content": base64.b64encode(old).decode(),
            }
            with (
                patch.object(
                    release,
                    "request",
                    side_effect=[(200, json.dumps(info).encode()), (200, b"{}")],
                ) as request,
                patch.object(release, "public_fetch", side_effect=[(200, candidate.read_bytes())]),
                patch.object(release, "run"),
                patch.dict(os.environ, {"BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel"}),
            ):
                release.github_channel(candidate, ident, temp)
            put = [call for call in request.call_args_list if call.args[2:3] == ("PUT",)]
            self.assertEqual(len(put), 1)
            self.assertEqual(put[0].args[3]["sha"], "c" * 40)
            self.assertEqual(put[0].args[3]["branch"], "update-channel")

    def test_github_channel_identical_advance_proves_the_raw_host_and_stops(self):
        # A re-run that would write the same document must not consume the
        # compare-and-swap: it proves the raw host already serves these bytes
        # and stops without a write. The raw host lagging behind is still a
        # failure once the deadline passes — the fast path is proof, not intent.
        ident = {"release_id": "v0.1.0"}
        with tempfile.TemporaryDirectory() as temp:
            temp = Path(temp)
            candidate = temp / "candidate.json"
            candidate.write_bytes(b'{"sequence":53}')
            info = {
                "sha": release.git_blob_sha(candidate.read_bytes()),
                "encoding": "base64",
                "content": base64.b64encode(candidate.read_bytes()).decode(),
            }
            with (
                patch.object(
                    release, "request", side_effect=[(200, json.dumps(info).encode())]
                ) as request,
                patch.object(release, "public_fetch", side_effect=[(200, candidate.read_bytes())]),
                patch.object(release, "run"),
                patch.dict(os.environ, {"BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel"}),
            ):
                release.github_channel(candidate, ident, temp)
            self.assertEqual(request.call_count, 1, "identical bytes must not write")
        with tempfile.TemporaryDirectory() as temp:
            temp = Path(temp)
            candidate = temp / "candidate.json"
            candidate.write_bytes(b'{"sequence":53}')
            with (
                patch.object(
                    release, "request", side_effect=[(200, json.dumps(info).encode())]
                ),
                patch.object(release, "public_fetch", return_value=(200, b'{"sequence":52}')),
                patch.object(release.time, "sleep"),
                patch.object(release.time, "monotonic", side_effect=[0, 0, 10_000]),
                patch.dict(os.environ, {"BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel"}),
                self.assertRaises(RuntimeError),
            ):
                release.github_channel(candidate, ident, temp)

    def test_require_public_github_fail_closed(self):
        # There is no second channel to fall back to: raw.githubusercontent
        # answers 404 for every path in a private repository, and instances
        # fetch anonymously, so a private repository is unreachable by
        # construction. The absence of the flag is also a refusal, not an
        # assumption of public.
        with patch.object(
            release, "request", return_value=(200, json.dumps({"private": True}).encode())
        ) as request:
            with self.assertRaises(RuntimeError):
                release.require_public_github("sentinel")
            self.assertIn("/repos/gh-owner/gh-repo", request.call_args.args[0])
        with patch.object(
            release, "request", return_value=(200, json.dumps({"private": False}).encode())
        ):
            release.require_public_github("sentinel")
        for body in [b"{}", json.dumps({"private": None}).encode()]:
            with (
                self.subTest(body=body),
                patch.object(release, "request", return_value=(200, body)),
                self.assertRaises(RuntimeError),
            ):
                release.require_public_github("sentinel")
        with (
            patch.object(release, "request", return_value=(403, b"")),
            self.assertRaises(RuntimeError),
        ):
            release.require_public_github("sentinel")

    def test_channel_refuses_a_private_repository_before_any_write(self):
        # The public check is the last gate before the write, and it must stay
        # ahead of it: a private repository never sees a channel attempt.
        ident = {
            "sha": self.sha,
            "run_id": "71",
            "sequence": 53,
            "version": "0.1.0",
            "release_id": "v0.1.0",
        }
        manifest = json.dumps(
            {
                "source_commit": ident["sha"],
                "release_id": ident["release_id"],
                "version": ident["version"],
                "release_sequence": ident["sequence"],
            }
        ).encode()

        def command(*args, **kwargs):
            if "channel-sign" in args:
                Path(args[args.index("--output") + 1]).write_bytes(b'{"signed":true}')
            return ""

        with tempfile.TemporaryDirectory() as temp:
            with (
                patch.object(
                    release,
                    "public_fetch",
                    side_effect=[(200, manifest), (200, b"payload")],
                ),
                patch.object(release, "run", side_effect=command),
                patch.object(
                    release, "require_public_github", side_effect=RuntimeError("private")
                ) as guard,
                patch.object(release, "github_channel") as write,
                patch.dict(
                    os.environ,
                    {
                        "BANSOKO_RELEASE_SIGNING_KEY": "fake",
                        "BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel",
                    },
                ),
                self.assertRaises(RuntimeError),
            ):
                release.channel(ident)
            guard.assert_called_once()
            write.assert_not_called()

    def test_the_channel_is_signed_as_the_formal_stable_channel(self):
        ident = {"sha": self.sha, "run_id": "71", "sequence": 53, "version": "0.1.0", "release_id": "v0.1.0"}
        seen = []

        def command(*args, **kwargs):
            seen.append(args)
            if "channel-sign" in args:
                Path(args[args.index("--output") + 1]).write_bytes(b'{"signed":true}')
            return ""

        with (
            patch.object(release, "channel_files"),
            patch.object(release, "run", side_effect=command),
            patch.object(release, "require_public_github"),
            patch.object(release, "github_channel"),
            patch.dict(os.environ, {"BANSOKO_RELEASE_SIGNING_KEY": "fake", "BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel"}),
        ):
            release.channel(ident)
        sign = next(args for args in seen if "channel-sign" in args)
        self.assertEqual(sign[sign.index("--channel") + 1], "stable")
        self.assertEqual(sign[sign.index("--expect-release-sequence") + 1], "53")


class FormalReleaseTests(unittest.TestCase):
    def setUp(self):
        self.ident = {"sha": "a" * 40, "run_id": "71", "sequence": 53, "version": "0.2.0", "release_id": "v0.2.0"}
        destination = patch.dict(
            os.environ,
            {"GITHUB_REPOSITORY": "gh-owner/gh-repo", "BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel", "GH_TOKEN": "sentinel"},
        )
        destination.start()
        self.addCleanup(destination.stop)

    def test_preflight_refuses_missing_notes_before_any_request(self):
        with (
            patch.object(release, "run", side_effect=release.subprocess.CalledProcessError(1, "notes")),
            patch.object(release, "request") as request,
            self.assertRaises(release.subprocess.SubprocessError),
        ):
            release.preflight(self.ident)
        request.assert_not_called()

    def test_preflight_gates_the_version_against_the_published_channel(self):
        commands = []

        def command(*args, **kwargs):
            commands.append(args)
            if "version-gate" in args:
                published = args[args.index("--published") + 1]
                self.assertTrue(published.endswith("published-channel.json"))
                self.assertEqual(Path(published).read_bytes(), b'{"channel":"stable"}')
                self.assertEqual(args[args.index("--version") + 1], "0.2.0")
            return ""

        info = {"sha": "f" * 40, "encoding": "base64", "content": base64.b64encode(b'{"channel":"stable"}').decode()}
        with (
            patch.object(release, "run", side_effect=command),
            patch.object(release, "require_public_github"),
            patch.object(release, "request", return_value=(200, json.dumps(info).encode())) as request,
        ):
            release.preflight(self.ident)
        self.assertIn("?ref=update-channel", request.call_args[0][0])
        self.assertEqual([args[3] for args in commands], ["release-notes", "version-gate"])

    def test_preflight_first_publication_and_read_failures(self):
        commands = []

        def command(*args, **kwargs):
            commands.append(args)
            return ""

        with (
            patch.object(release, "run", side_effect=command),
            patch.object(release, "require_public_github"),
            patch.object(release, "request", return_value=(404, b"")),
        ):
            release.preflight(self.ident)
        gate = commands[-1]
        self.assertEqual(gate[gate.index("--published") + 1], "")
        for status in [401, 403, 500]:
            with (
                self.subTest(status=status),
                patch.object(release, "run", side_effect=command),
                patch.object(release, "require_public_github"),
                patch.object(release, "request", return_value=(status, b"")),
                self.assertRaises(RuntimeError),
            ):
                release.preflight(self.ident)

    def test_preflight_propagates_a_refused_version(self):
        def command(*args, **kwargs):
            if "version-gate" in args:
                raise release.subprocess.CalledProcessError(1, "version-gate")
            return ""

        with (
            patch.object(release, "run", side_effect=command),
            patch.object(release, "require_public_github"),
            patch.object(release, "request", return_value=(404, b"")),
            self.assertRaises(release.subprocess.SubprocessError),
        ):
            release.preflight(self.ident)

    def _github_release(self, view):
        calls = []

        def notes(*args, **kwargs):
            Path(args[args.index("--output") + 1]).write_text("# 简体中文\n\n## 修复\n- 一项\n")
            return ""

        def gh(args, **kwargs):
            calls.append(args)
            if args[:3] == ["gh", "release", "view"]:
                return release.subprocess.CompletedProcess(args, view[0], stdout=view[1], stderr=b"")
            return release.subprocess.CompletedProcess(args, 0)

        with tempfile.TemporaryDirectory() as temp, patch.object(release, "OUTPUT", Path(temp)), patch.object(release, "run", side_effect=notes), patch.object(release.subprocess, "run", side_effect=gh):
            release.github_release(self.ident)
        return calls

    def test_github_release_is_created_from_the_verified_tag_with_the_notes_file(self):
        calls = self._github_release((1, b""))
        create = calls[-1]
        self.assertEqual(create[:4], ["gh", "release", "create", "v0.2.0"])
        self.assertIn("--verify-tag", create)
        self.assertTrue(create[create.index("--notes-file") + 1].endswith(release.GITHUB_NOTES))
        self.assertEqual(create[create.index("--repo") + 1], "gh-owner/gh-repo")

    def test_github_release_rerun_accepts_only_identical_notes(self):
        same = json.dumps({"tagName": "v0.2.0", "body": "# 简体中文\n\n## 修复\n- 一项"}).encode()
        calls = self._github_release((0, same))
        self.assertFalse(any(args[:3] == ["gh", "release", "create"] for args in calls))
        different = json.dumps({"tagName": "v0.2.0", "body": "edited"}).encode()
        with self.assertRaises(RuntimeError):
            self._github_release((0, different))

    def test_github_release_requires_its_token(self):
        with patch.dict(os.environ, {"GH_TOKEN": ""}), self.assertRaises(RuntimeError):
            release.github_release(self.ident)


if __name__ == "__main__":
    unittest.main()
