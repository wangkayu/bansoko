#!/usr/bin/env python3
"""Independent refusal tests invoke the real publisher, not a duplicated policy."""

import base64
import importlib.util
import json
import os
import tempfile
import unittest
from email.message import Message
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "release", Path(__file__).with_name("ci-test-release.py")
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
        # The publication destination comes from the environment; keep the
        # harness values neutral so the suite never pins a real deployment.
        self.destination = {
            "BANSOKO_RELEASE_API_BASE": "https://release.example.test",
            "BANSOKO_RELEASE_REPO": "owner/repo",
            "BANSOKO_RELEASE_IMAGE": "registry.example.test/owner/bansoko",
            "BANSOKO_RELEASE_REGISTRY_USER": "release-user",
            # The GitHub destination names its repository separately: the Gitea
            # one above belongs to a different owner, so a shared value would
            # write the channel to the wrong repository.
            "GITHUB_REPOSITORY": "gh-owner/gh-repo",
        }
        destination = patch.dict(os.environ, self.destination)
        destination.start()
        self.addCleanup(destination.stop)
        self.env = {
            "GITHUB_EVENT_NAME": "push",
            "GITHUB_REF": "refs/heads/main",
            "GITHUB_SHA": self.sha,
            "GITHUB_RUN_ID": "71",
            "GITHUB_RUN_NUMBER": "53",
            "BANSOKO_RELEASE_REF_PROTECTED": "true",
            "BANSOKO_REQUIRED_RESULTS": json.dumps(self.gates),
            **self.destination,
        }

    def test_destination_config_is_derived_and_fails_closed(self):
        self.assertEqual(release.api_base(), "https://release.example.test")
        self.assertEqual(release.repository(), "owner/repo")
        self.assertEqual(
            release.image_repository(),
            ("registry.example.test/owner/bansoko", "registry.example.test"),
        )
        self.assertEqual(
            release.package(),
            "https://release.example.test/api/packages/owner/generic/bansoko-test",
        )
        self.assertEqual(
            release.content_url(),
            "https://release.example.test/api/v1/repos/owner/repo/contents/channel-v1.json",
        )
        resolvers = {
            "BANSOKO_RELEASE_API_BASE": release.api_base,
            "BANSOKO_RELEASE_REPO": release.repository,
            "BANSOKO_RELEASE_IMAGE": release.image_repository,
            "BANSOKO_RELEASE_REGISTRY_USER": release.registry_user,
        }
        for name, resolver in resolvers.items():
            with (
                self.subTest(name=name, state="unset"),
                patch.dict(os.environ, {}, clear=True),
                self.assertRaises(RuntimeError),
            ):
                resolver()
        for key, value in [
            ("BANSOKO_RELEASE_API_BASE", "http://release.example.test"),
            ("BANSOKO_RELEASE_API_BASE", "https://release"),
            ("BANSOKO_RELEASE_REPO", "owner"),
            ("BANSOKO_RELEASE_REPO", "owner/repo/extra"),
            ("BANSOKO_RELEASE_REPO", "https://owner/repo"),
            ("BANSOKO_RELEASE_IMAGE", "bansoko"),
            ("BANSOKO_RELEASE_IMAGE", "registry.example.test/"),
            ("BANSOKO_RELEASE_IMAGE", "https://registry.example.test/owner/bansoko"),
            ("BANSOKO_RELEASE_REGISTRY_USER", "user:password"),
        ]:
            with (
                self.subTest(key=key, value=value),
                patch.dict(os.environ, {key: value}),
                self.assertRaises(RuntimeError),
            ):
                resolvers[key]()

    def test_event_identity_and_each_gate(self):
        with (
            patch.dict(os.environ, self.env, clear=True),
            patch.object(release, "run", side_effect=[self.sha, ""]),
        ):
            ident = release.identity()
            self.assertEqual(ident["release_id"], "test-ci-53")
            self.assertEqual(ident["version"], "0.0.0-test.53")
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
        for key, value in [
            ("GITHUB_EVENT_NAME", "pull_request"),
            ("GITHUB_REF", "refs/tags/v1.0.0"),
            ("BANSOKO_RELEASE_REF_PROTECTED", "false"),
            ("GITHUB_SHA", "invalid"),
        ]:
            with (
                patch.dict(os.environ, dict(self.env, **{key: value}), clear=True),
                self.assertRaises(RuntimeError),
            ):
                release.identity()
        with (
            patch.dict(os.environ, self.env, clear=True),
            patch.object(release, "run", return_value="b" * 40),
            self.assertRaises(RuntimeError),
        ):
            release.identity()

    def test_immutable_status_and_bytes(self):
        url = release.package() + "/version/manifest.json"
        for code in [401, 403, 500, 302]:
            with patch.object(release, "request", return_value=(code, b"")) as request:
                with self.assertRaises(RuntimeError):
                    release.immutable_upload(url, "sentinel", b"expected")
                self.assertEqual(request.call_count, 1)
        with (
            patch.object(release, "request", return_value=(200, b"wrong")),
            self.assertRaises(RuntimeError),
        ):
            release.immutable_upload(url, "sentinel", b"expected")
        with patch.object(
            release, "request", side_effect=[(404, b""), (201, b""), (200, b"expected")]
        ) as request:
            release.immutable_upload(url, "sentinel", b"expected")
            self.assertEqual(request.call_count, 3)
        with (
            patch.object(
                release,
                "request",
                side_effect=[(404, b""), (409, b""), (200, b"wrong")],
            ),
            self.assertRaises(RuntimeError),
        ):
            release.immutable_upload(url, "sentinel", b"expected")

    def test_registry_authenticated_absence_and_refusals(self):
        import urllib.error

        for code in [401, 403, 500, 302, 404]:
            error = urllib.error.HTTPError(
                "https://registry.example.test/", code, "refused", Message(), None
            )
            with patch.object(release.urllib.request, "build_opener") as opener:
                opener.return_value.open.side_effect = error
                if code == 404:
                    self.assertIsNone(
                        release.registry_digest("test-ci-53", "sentinel")
                    )
                else:
                    with self.assertRaises(RuntimeError):
                        release.registry_digest("test-ci-53", "sentinel")
        import hashlib

        body = b"registry-manifest"
        digest = "sha256:" + hashlib.sha256(body).hexdigest()
        with patch.object(release.urllib.request, "build_opener") as opener:
            response = opener.return_value.open.return_value.__enter__.return_value
            response.status = 200
            response.read.return_value = body
            response.headers = {"Docker-Content-Digest": digest}
            self.assertEqual(
                release.registry_digest("test-ci-53", "sentinel"), digest
            )
            response.headers = {"Docker-Content-Digest": "sha256:" + "0" * 64}
            with self.assertRaises(RuntimeError):
                release.registry_digest("test-ci-53", "sentinel")

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

    def test_channel_sha_and_refusal_boundaries(self):
        ident = {
            "sha": self.sha,
            "run_id": "71",
            "sequence": 53,
            "version": "0.0.0-test.53",
            "release_id": "test-ci-53",
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
                Path(args[args.index("--output") + 1]).write_bytes(
                    b"candidate-signed-bytes"
                )
            return ""

        for status in [401, 403, 500, 302]:
            with (
                patch.dict(
                    os.environ,
                    {
                        "BANSOKO_RELEASE_PACKAGE_TOKEN": "sentinel",
                        "BANSOKO_RELEASE_CHANNEL_TOKEN": "sentinel",
                        "BANSOKO_RELEASE_SIGNING_KEY": "fake",
                    },
                ),
                patch.object(release, "run", side_effect=command),
                patch.object(
                    release,
                    "request",
                    side_effect=[(200, manifest), (200, b"payload"), (status, b"")],
                ) as request,
            ):
                with self.assertRaises(RuntimeError):
                    release.channel(ident)
                self.assertEqual(request.call_count, 3)
        old = json.dumps({"sha": "old-blob", "content": "b2xk"}).encode()
        with (
            patch.dict(
                os.environ,
                {
                    "BANSOKO_RELEASE_PACKAGE_TOKEN": "sentinel",
                    "BANSOKO_RELEASE_CHANNEL_TOKEN": "sentinel",
                    "BANSOKO_RELEASE_SIGNING_KEY": "fake",
                },
            ),
            patch.object(release, "run", side_effect=command),
            patch.object(
                release,
                "request",
                side_effect=[
                    (200, manifest),
                    (200, b"payload"),
                    (200, old),
                    (422, b""),
                ],
            ) as request,
        ):
            with self.assertRaises(RuntimeError):
                release.channel(ident)
            call = request.call_args_list[-1]
            self.assertEqual(call.args[2], "PUT")
            self.assertEqual(call.args[3]["sha"], "old-blob")
            # This is the Gitea write, and the Gitea branch is live under the
            # name images in the field resolve against.
            self.assertEqual(call.args[3]["branch"], "update-channel-test")
            self.assertNotIn("force_push", call.args[3])
            self.assertEqual(request.call_count, 4)

    def test_each_destination_keeps_its_own_channel_branch(self):
        # The Gitea branch is live: every image in the field resolves its update
        # URL against `update-channel-test`, so renaming the constant that the
        # GitHub destination uses must not move it. One branch name per
        # destination, and neither may drift into the other's.
        self.assertEqual(release.GITEA_BRANCH, "update-channel-test")
        self.assertEqual(release.BRANCH, "update-channel")
        self.assertNotEqual(release.GITEA_BRANCH, release.BRANCH)
        self.assertIn(release.GITEA_BRANCH, release.raw_url())
        self.assertNotIn(release.BRANCH + "/", release.raw_url())
        self.assertIn("/" + release.BRANCH + "/", release.github_raw_url("channel-v1.json"))

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


    def test_destinations_are_declared_not_inferred(self):
        # The default must stay Gitea alone: a run that names no destination is
        # the behaviour every existing deployment was built on.
        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop("BANSOKO_RELEASE_DESTINATIONS", None)
            self.assertEqual(release.destinations(), ["gitea"])
            self.assertTrue(release.destination_enabled("gitea"))
            self.assertFalse(release.destination_enabled("github"))
        for value, expected in [
            ("gitea,github", ["gitea", "github"]),
            ("github", ["github"]),
            (" github , gitea ", ["github", "gitea"]),
        ]:
            with self.subTest(value=value), patch.dict(
                os.environ, {"BANSOKO_RELEASE_DESTINATIONS": value}
            ):
                self.assertEqual(release.destinations(), expected)
        for value in ["", "  ", "bogus", "gitea,gitea", "github,gitea,github"]:
            with (
                self.subTest(value=value),
                patch.dict(os.environ, {"BANSOKO_RELEASE_DESTINATIONS": value}),
                self.assertRaises(RuntimeError),
            ):
                release.destinations()

    def test_github_addresses_avoid_the_redirect_and_query(self):
        # The image bakes github.com/<o>/<r>/raw/..., which is a cross-host 302,
        # and release assets, which are a 302 with a signed query. The update
        # client refuses both, so the published address must be neither.
        raw = release.github_raw_url("channel-v1.json")
        self.assertEqual(
            raw,
            "https://raw.githubusercontent.com/gh-owner/gh-repo/update-channel/channel-v1.json",
        )
        asset = release.github_file_url("0.0.0-test.53", "release-manifest.json")
        self.assertEqual(
            asset,
            "https://raw.githubusercontent.com/gh-owner/gh-repo/update-channel/"
            "0.0.0-test.53/release-manifest.json",
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
        # Passing hosts= must not widen to anything else: the default stays the
        # configured API origin, and a declared host still refuses a near miss.
        with patch.object(release.urllib.request, "build_opener") as opener:
            with self.assertRaises(RuntimeError):
                release.request("https://api.github.com/x", "sentinel")
            opener.assert_not_called()
            with self.assertRaises(RuntimeError):
                release.request(
                    "https://evil.example.test/x",
                    "sentinel",
                    hosts=("api.github.com",),
                )
            opener.assert_not_called()
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

    def test_github_repository_is_the_workflow_one_not_the_gitea_config(self):
        # BANSOKO_RELEASE_REPO is the Gitea destination and names a different
        # owner. If the GitHub files were addressed through it, the channel would
        # be written somewhere the workflow token has no access to.
        with patch.dict(os.environ, self.destination):
            self.assertEqual(release.repository(), "owner/repo")
            self.assertEqual(release.github_repository(), "gh-owner/gh-repo")
        for value in ["", "owner", "owner/repo/extra", "own er/repo", "owner//repo"]:
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

    def test_immutable_files_are_compared_by_blob_sha_not_inline_content(self):
        # Above 1 MB the contents API returns `"encoding": "none"` with an empty
        # `content`, and the embedded payload is ~27 MB, so a comparison against
        # inline base64 would fail on every real publication. `sha` is the git
        # blob id and is present at every size.
        payload = b"x" * 2_000_000
        blob = release.git_blob_sha(payload)
        existing = (200, json.dumps({"sha": blob, "encoding": "none", "content": ""}).encode())
        with patch.object(release, "request", side_effect=[existing]) as request:
            release.github_put_file("0.0.0-test.53", "embedded-update-v1.tar.gz", payload, "sentinel")
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
            release.github_put_file("0.0.0-test.53", "embedded-update-v1.tar.gz", payload, "sentinel")

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
                patch.object(release, "github_repository_public", return_value=True),
                patch.object(release, "run"),
                patch.dict(os.environ, {"BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel"}),
            ):
                release.github_channel(candidate, {"release_id": "test.53"}, temp)
            write = [call for call in request.call_args_list if call.args[2:3] == ("PUT",)]
            self.assertEqual(len(write), 1, "channel creation must be a PUT")
            self.assertNotIn("sha", write[0].args[3])

    def test_private_repository_holds_the_github_destination_back(self):
        # raw.githubusercontent.com answers 404 for every path in a private
        # repository, so the unauthenticated readback this lane requires cannot
        # pass there. Failing would deadlock "green CI before going public".
        with (
            patch.dict(os.environ, {"BANSOKO_RELEASE_DESTINATIONS": "gitea,github"}),
            patch.dict(os.environ, {"BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel"}),
            patch.object(release, "github_repository_public", return_value=False),
        ):
            self.assertFalse(release.github_destination_active())
        with (
            patch.dict(os.environ, {"BANSOKO_RELEASE_DESTINATIONS": "gitea,github"}),
            patch.dict(os.environ, {"BANSOKO_RELEASE_GITHUB_TOKEN": "sentinel"}),
            patch.object(release, "github_repository_public", return_value=True),
        ):
            self.assertTrue(release.github_destination_active())
        # Declaring only Gitea must not read the GitHub repository at all.
        with (
            patch.dict(os.environ, {"BANSOKO_RELEASE_DESTINATIONS": "gitea"}),
            patch.object(release, "github_repository_public") as probe,
            patch.object(release, "required") as token,
        ):
            self.assertFalse(release.github_destination_active())
            probe.assert_not_called()
            token.assert_not_called()

    def test_github_repository_public_reads_the_flag(self):
        with patch.object(
            release, "request", side_effect=[(200, json.dumps({"private": True}).encode())]
        ):
            self.assertFalse(release.github_repository_public("sentinel"))
        with patch.object(
            release, "request", side_effect=[(200, json.dumps({"private": False}).encode())]
        ):
            self.assertTrue(release.github_repository_public("sentinel"))
        # An unreadable repository is not an assumption of either state.
        with (
            patch.object(release, "request", side_effect=[(403, b"")]),
            self.assertRaises(RuntimeError),
        ):
            release.github_repository_public("sentinel")


if __name__ == "__main__":
    unittest.main()
