#!/usr/bin/env python3
"""Deterministic HTTP fixture for the authenticated browser session.

The fixture has no external dependencies and never emits credentials, database
URLs, request headers, or request bodies. Its artifact bytes and RSS identity
are fixed; only the advertised host and ephemeral port vary per session.

Besides the custom-RSS feed it serves the AniBT provider surface a real saved
provider connection needs: the season list, the per-anime group discovery and
the per-group RSS feed. Every served fixture path (except the health/counter
probes themselves) is counted so a browser run can prove that re-loading an
already-saved revision + snapshot does NOT issue another live provider fetch.
"""

from __future__ import annotations

import argparse
import hashlib
import html
import json
import signal
import tempfile
import threading
import time
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


FIXTURE_ID = "browser-fixture-v1"
GROUP = "BrowserFixture"
SHOW_TITLE = "Browser Fixture Show"
TITLE = "[BrowserFixture] Browser Fixture Show - 01 [1080p]"
ARTIFACT_NAME = "Browser Fixture Show - 01 [1080p].mkv"
ARTIFACT_BYTES = b"bansoko-browser-fixture-artifact-v1\n"
GUID = "browser-fixture-v1-stable-guid"
PIECE_LENGTH = 16384

# AniBT provider surface. The contract mirrors the public AniBT endpoints the
# composition root reads: /api/seasons/anime (season list), /api/anime/groups
# (group discovery by BGM id) and /rss/anime.xml?bgmId=&groupSlug= (the feed).
PROVIDER_BGM_ID = 910001
PROVIDER_ANIME_ID = "browser-fixture-provider-anime"
PROVIDER_GROUP_SLUG = "browserfixture"
PROVIDER_GROUP_NAME = "BrowserFixture"
PROVIDER_SHOW_TITLE = "Browser Provider Show"
PROVIDER_ORIGINAL_TITLE = "ブラウザプロバイダーショー"
PROVIDER_RELEASE_TITLE = "[BrowserFixture] Browser Provider Show - 01 [1080p]"
PROVIDER_GUID = "browser-provider-v1-stable-guid"
# A second provider release exists so a filter rule can be shown to hide exactly
# one row: it is a real feed item with its own guid and torrent link, and it
# carries a title the filter spec excludes by keyword.
PROVIDER_FILTERED_RELEASE_TITLE = "[BrowserFixture] Browser Provider Show - 02 [1080p]"
PROVIDER_FILTERED_GUID = "browser-provider-v1-filtered-guid"
PROVIDER_SEASON = "2026-summer"

# Ops surfaces (downloader manager, logs, execution records). A separate custom
# RSS feed whose torrents are unique per `run` query value, so independent live
# specs never share a qBittorrent task: a slow single-file episode (throttled web
# seed, so a task stays visibly in progress for a while) and a two-file batch
# whose second video carries no episode number (an unknown file). The existing
# feeds above are unchanged.
OPS_SHOW = "Browser Ops Show"
OPS_SLOW_TITLE = f"[{GROUP}] {OPS_SHOW} - 02 [1080p]"
OPS_BATCH_TITLE = f"[{GROUP}] {OPS_SHOW} - 03 [1080p]"
OPS_PIECE_LENGTH = 65536
OPS_SLOW_BYTES = (hashlib.sha256(b"bansoko-ops-slow").digest() * (2 * 1024 * 1024 // 32))
# Bytes per second per web-seed request: about 85 seconds for the whole file.
OPS_SLOW_RATE = 24 * 1024
OPS_BATCH_EPISODE = f"{OPS_SHOW} - 03 [1080p].mkv"
OPS_BATCH_UNKNOWN = f"{OPS_SHOW} - NCOP [1080p].mkv"
OPS_BATCH_FILES = (
    (OPS_BATCH_EPISODE, b"bansoko-ops-batch-episode-03\n"),
    (OPS_BATCH_UNKNOWN, b"bansoko-ops-batch-unknown\n"),
)

# Paths whose hits are counted (the health and counter probes are excluded so
# observing the counters never perturbs them).
COUNTED_PATHS = (
    "/feed.xml",
    "/artifact.torrent",
    "/artifact.mkv",
    "/api/seasons/anime",
    "/api/anime/groups",
    "/rss/anime.xml",
    "/api/bgm/search",
)


def url_host(host: str) -> str:
    """Format an IPv6 host for an HTTP URL without changing normal hostnames."""
    if ":" in host and not host.startswith("["):
        return f"[{host}]"
    return host


def bencode(value: Any) -> bytes:
    if isinstance(value, int):
        return f"i{value}e".encode("ascii")
    if isinstance(value, bytes):
        return str(len(value)).encode("ascii") + b":" + value
    if isinstance(value, str):
        return bencode(value.encode("utf-8"))
    if isinstance(value, list):
        return b"l" + b"".join(bencode(item) for item in value) + b"e"
    if isinstance(value, dict):
        encoded = []
        for key in sorted(value, key=lambda item: item if isinstance(item, bytes) else item.encode("utf-8")):
            encoded.append(bencode(key))
            encoded.append(bencode(value[key]))
        return b"d" + b"".join(encoded) + b"e"
    raise TypeError(f"unsupported bencode value: {type(value)!r}")


def torrent_info() -> dict[bytes, Any]:
    return {
        b"length": len(ARTIFACT_BYTES),
        b"name": ARTIFACT_NAME.encode("utf-8"),
        b"piece length": PIECE_LENGTH,
        b"pieces": hashlib.sha1(ARTIFACT_BYTES).digest(),
    }


def build_torrent(artifact_url: str) -> bytes:
    return bencode({b"info": torrent_info(), b"url-list": [artifact_url]})


# The v1 infohash of the fixture torrent. The web-seed URL sits outside the info
# dictionary, so the hash is the same for every advertised host and port. Both
# feeds publish it the way the real providers do (AniBT in its <torrent>
# block). A seed's identity is that published infohash, else the infohash of
# its expanded torrent; the provider feed's extra item publishes none, so the
# live lanes also cover a seed whose identity comes from expansion.
INFO_HASH = hashlib.sha1(bencode(torrent_info())).hexdigest()
TORRENT_NS = "https://anibt.moe/xmlns/0.1/"


def ops_tag(run: str) -> str:
    """Eight upper-case hex digits derived from the run value (a CRC-like name suffix)."""
    return hashlib.sha256(run.encode("utf-8")).hexdigest()[:8].upper()


def pieces_of(data: bytes, piece_length: int) -> bytes:
    return b"".join(hashlib.sha1(data[i:i + piece_length]).digest() for i in range(0, len(data), piece_length))


def ops_slow_name(tag: str) -> str:
    return f"{OPS_SHOW} - 02 [1080p][{tag}].mkv"


def ops_batch_name(tag: str) -> str:
    return f"{OPS_SHOW} - 03 [1080p][{tag}]"


def ops_torrent(kind: str, tag: str, base_url: str) -> bytes:
    """Build the per-run ops torrent. The web seed is a base URL ending in '/';
    the client appends the torrent name (and, for the batch, the file path)."""
    if kind == "slow":
        info: dict[bytes, Any] = {
            b"length": len(OPS_SLOW_BYTES),
            b"name": ops_slow_name(tag).encode("utf-8"),
            b"piece length": OPS_PIECE_LENGTH,
            b"pieces": pieces_of(OPS_SLOW_BYTES, OPS_PIECE_LENGTH),
        }
    else:
        content = b"".join(data for _, data in OPS_BATCH_FILES)
        info = {
            b"files": [{b"length": len(data), b"path": [name.encode("utf-8")]} for name, data in OPS_BATCH_FILES],
            b"name": ops_batch_name(tag).encode("utf-8"),
            b"piece length": OPS_PIECE_LENGTH,
            b"pieces": pieces_of(content, OPS_PIECE_LENGTH),
        }
    return bencode({b"info": info, b"url-list": [f"{base_url}/ops/{kind}/"]})


def build_ops_feed(base_url: str, run: str, items: list[str]) -> bytes:
    tag = ops_tag(run)
    quoted = urllib.parse.quote(run, safe="")
    entries = []
    for kind, title in (("slow", OPS_SLOW_TITLE), ("batch", OPS_BATCH_TITLE)):
        if kind not in items:
            continue
        link = html.escape(f"{base_url}/ops/{kind}.torrent?run={quoted}", quote=True)
        entries.append(
            f"<item><guid>browser-ops-{kind}-{tag}</guid><title>{html.escape(title, quote=True)}</title>"
            f"<link>{link}</link><pubDate>Sat, 11 Jul 2026 04:00:00 +0000</pubDate></item>"
        )
    return (
        '<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel>'
        "<title>Bansoko Browser Ops Fixture</title>" + "".join(entries) + "</channel></rss>"
    ).encode("utf-8")


def ops_content(path: str) -> tuple[bytes, int] | None:
    """Web-seed content for an /ops/<kind>/... request: (bytes, throttle rate or 0)."""
    name = urllib.parse.unquote(path.rsplit("/", 1)[-1])
    if path.startswith("/ops/slow/") and name.startswith(f"{OPS_SHOW} - 02 [1080p]["):
        return OPS_SLOW_BYTES, OPS_SLOW_RATE
    if path.startswith("/ops/batch/"):
        for file_name, data in OPS_BATCH_FILES:
            if name == file_name:
                return data, 0
    return None


# AniBT structured tags (anibt-rss-1.0 namespace) carried by the provider feed
# only, so the browser can show tag badges and a tag-filtered seed. Empty
# default tag rows let every release through, so existing specs are unaffected.
ANIBT_NS = "https://anibt.net/xmlns/rss/1.0/"
PROVIDER_TAGS = {
    "browser-provider-v1-stable-guid": (("resolution", "1080p"), ("subtitle", "INTERNAL"), ("language", "CHS"), ("format", "MKV"), ("customTag", "HEVC")),
    "browser-provider-v1-filtered-guid": (("resolution", "1080p"), ("subtitle", "INTERNAL"), ("language", "CHT"), ("format", "MP4"), ("customTag", "AVC")),
}


def build_feed(
    torrent_url: str,
    *,
    title: str,
    guid: str,
    extra_items: tuple[tuple[str, str], ...] = (),
    tags: dict[str, tuple[tuple[str, str], ...]] | None = None,
) -> bytes:
    escaped_torrent_url = html.escape(torrent_url, quote=True)

    def item(item_guid: str, item_title: str, *, infohash: bool = True) -> str:
        tag_xml = "".join(
            f"<anibt:{name}>{html.escape(value, quote=True)}</anibt:{name}>" for name, value in (tags or {}).get(item_guid, ())
        )
        return (
            f"<item><guid>{item_guid}</guid><title>{html.escape(item_title, quote=True)}</title>"
            f"<link>{escaped_torrent_url}</link>{tag_xml}"
            + (f'<torrent xmlns="{TORRENT_NS}"><infohash>{INFO_HASH}</infohash></torrent>' if infohash else "")
            + "<pubDate>Sat, 11 Jul 2026 03:00:00 +0000</pubDate></item>"
        )

    entries = item(guid, title) + "".join(item(extra_guid, extra_title, infohash=False) for extra_guid, extra_title in extra_items)
    namespace = f' xmlns:anibt="{ANIBT_NS}"' if tags else ""
    return (
        '<?xml version="1.0" encoding="UTF-8"?>'
        f'<rss version="2.0"{namespace}><channel>'
        "<title>Bansoko Browser Fixture</title>"
        f"{entries}"
        "</channel></rss>"
    ).encode("utf-8")


class Fixture:
    def __init__(self, bind_host: str, advertised_host: str, port: int) -> None:
        host = url_host(advertised_host)
        self.lock = threading.Lock()
        self.counters: dict[str, int] = {path: 0 for path in COUNTED_PATHS}
        self.metadata = {
            "schema": "bansoko-browser-fixture/v1",
            "redacted": True,
            "fixture_id": FIXTURE_ID,
            "bind_host": bind_host,
            "url_host": advertised_host,
            "port": port,
            "base_url": f"http://{host}:{port}",
            "health_url": f"http://{host}:{port}/health",
            "feed_url": f"http://{host}:{port}/feed.xml",
            "torrent_url": f"http://{host}:{port}/artifact.torrent",
            "artifact_url": f"http://{host}:{port}/artifact.mkv",
            "guid": GUID,
            "stable_item_key": GUID,
            "show_title": SHOW_TITLE,
            "managed_anime_title": SHOW_TITLE,
            "title": TITLE,
            "group": GROUP,
            "artifact_name": ARTIFACT_NAME,
            "artifact_bytes": len(ARTIFACT_BYTES),
            "artifact_sha256": hashlib.sha256(ARTIFACT_BYTES).hexdigest(),
            # Host-reachable provider endpoints. The saved provider connection
            # uses the "localhost" spelling: the default resolver already maps
            # it to loopback for the catalog/group readers, and the fixture
            # host allowlist maps it explicitly for the SSRF-guarded feed and
            # torrent readers. The qB container keeps reaching the fixture
            # through the advertised host instead.
            "provider_base_url": f"http://localhost:{port}",
            "provider_season_url": f"http://localhost:{port}/api/seasons/anime",
            "provider_groups_url": f"http://localhost:{port}/api/anime/groups",
            "provider_feed_url": f"http://localhost:{port}/rss/anime.xml",
            "counters_url": f"http://localhost:{port}/__counters",
            "provider_bgm_id": PROVIDER_BGM_ID,
            "provider_entry_ref": str(PROVIDER_BGM_ID),
            "provider_anime_id": PROVIDER_ANIME_ID,
            "provider_group_slug": PROVIDER_GROUP_SLUG,
            "provider_group_name": PROVIDER_GROUP_NAME,
            "provider_show_title": PROVIDER_SHOW_TITLE,
            "provider_original_title": PROVIDER_ORIGINAL_TITLE,
            "provider_release_title": PROVIDER_RELEASE_TITLE,
            "provider_guid": PROVIDER_GUID,
            "provider_stable_item_key": PROVIDER_GUID,
            "provider_filtered_guid": PROVIDER_FILTERED_GUID,
            "provider_filtered_release_title": PROVIDER_FILTERED_RELEASE_TITLE,
            # Ops surfaces feed: `?run=<value>` makes its torrents unique per run;
            # `&items=slow,batch` (default both) selects the items.
            "ops_feed_url": f"http://{host}:{port}/ops/feed.xml",
            "ops_show_title": OPS_SHOW,
            "ops_slow_release_title": OPS_SLOW_TITLE,
            "ops_batch_release_title": OPS_BATCH_TITLE,
            "ops_batch_unknown_file": OPS_BATCH_UNKNOWN,
        }
        self.torrent = build_torrent(self.metadata["artifact_url"])
        self.feed = build_feed(self.metadata["torrent_url"], title=TITLE, guid=GUID)
        self.provider_feed = build_feed(
            self.metadata["torrent_url"],
            title=PROVIDER_RELEASE_TITLE,
            guid=PROVIDER_GUID,
            extra_items=((PROVIDER_FILTERED_GUID, PROVIDER_FILTERED_RELEASE_TITLE),),
            tags=PROVIDER_TAGS,
        )
        self.metadata["torrent_bytes"] = len(self.torrent)
        self.metadata["torrent_sha256"] = hashlib.sha256(self.torrent).hexdigest()

    # -- counters -----------------------------------------------------------

    def count(self, path: str) -> None:
        with self.lock:
            if path in self.counters:
                self.counters[path] += 1

    def snapshot_counters(self) -> dict[str, int]:
        with self.lock:
            return dict(self.counters)

    # -- provider payloads --------------------------------------------------

    def season_payload(self, season: str) -> bytes:
        entry = {
            "bgmId": PROVIDER_BGM_ID,
            "animeId": PROVIDER_ANIME_ID,
            "cover": None,
            "title": {
                "chinese": PROVIDER_SHOW_TITLE,
                "japanese": PROVIDER_ORIGINAL_TITLE,
            },
            "format": "TV",
            "kind": "anime",
            "episodes": 12,
            "rating": 8.5,
            "airingAt": 1780000000,
            "weekday": 6,
            "weekdayLabel": "周六",
            "scheduleStatus": "airing",
            "rssReleaseCount": 2,
            "hasRelease": True,
        }
        payload = {
            "ok": True,
            "data": {
                "requestedSeason": season or PROVIDER_SEASON,
                "total": 1,
                "byWeekday": [{"animes": [entry]}],
            },
        }
        return json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8")

    def groups_payload(self, bgm_id: str) -> tuple[bytes, int]:
        if bgm_id != str(PROVIDER_BGM_ID):
            return json.dumps({"ok": False, "error": "unknown bgmId"}).encode("utf-8"), 404
        payload = {
            "ok": True,
            "data": {
                "bgmId": PROVIDER_BGM_ID,
                "total": 1,
                "groups": [
                    {
                        "slug": PROVIDER_GROUP_SLUG,
                        "name": PROVIDER_GROUP_NAME,
                        "status": "active",
                        "totalReleases": 2,
                        "lastUpdatedAt": 1780000000,
                    }
                ],
            },
        }
        return json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8"), 200

    def search_payload(self, query: str) -> bytes:
        # The public AniBT keyword search: case-insensitive substring match over
        # the entry's names, the same fields the real endpoint returns.
        needle = query.strip().casefold()
        names = (PROVIDER_SHOW_TITLE, PROVIDER_ORIGINAL_TITLE)
        data = []
        if needle and any(needle in name.casefold() for name in names):
            data.append(
                {
                    "bgmId": PROVIDER_BGM_ID,
                    "name": PROVIDER_ORIGINAL_TITLE,
                    "nameCn": PROVIDER_SHOW_TITLE,
                    "date": "2026-07-11",
                    "image": None,
                    "rating": 8.5,
                    "url": f"https://bgm.tv/subject/{PROVIDER_BGM_ID}",
                }
            )
        return json.dumps({"ok": True, "data": data}, sort_keys=True, separators=(",", ":")).encode("utf-8")

    # -- routing ------------------------------------------------------------

    def body(self, raw_path: str) -> tuple[bytes, str, int] | None:
        parsed = urllib.parse.urlsplit(raw_path)
        path = parsed.path
        query = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
        if path in ("/health", "/__counters"):
            if path == "/health":
                payload = dict(self.metadata)
                payload["status"] = "ok"
                payload["counters"] = self.snapshot_counters()
                return (
                    json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8"),
                    "application/json",
                    200,
                )
            return (
                json.dumps(self.snapshot_counters(), sort_keys=True, separators=(",", ":")).encode("utf-8"),
                "application/json",
                200,
            )
        self.count(path)
        if path == "/feed.xml":
            return self.feed, "application/rss+xml; charset=utf-8", 200
        if path == "/artifact.torrent":
            return self.torrent, "application/x-bittorrent", 200
        if path == "/artifact.mkv":
            return ARTIFACT_BYTES, "video/x-matroska", 200
        if path == "/api/seasons/anime":
            return self.season_payload((query.get("season") or [""])[0]), "application/json", 200
        if path == "/api/anime/groups":
            body, status = self.groups_payload((query.get("bgmId") or [""])[0])
            return body, "application/json", status
        if path == "/api/bgm/search":
            return self.search_payload((query.get("q") or [""])[0]), "application/json", 200
        if path == "/rss/anime.xml":
            if (query.get("bgmId") or [""])[0] != str(PROVIDER_BGM_ID):
                return json.dumps({"ok": False}).encode("utf-8"), "application/json", 404
            return self.provider_feed, "application/rss+xml; charset=utf-8", 200
        if path == "/ops/feed.xml":
            run = (query.get("run") or [""])[0]
            items = ",".join(query.get("items") or ["slow,batch"]).split(",")
            return build_ops_feed(self.metadata["base_url"], run, items), "application/rss+xml; charset=utf-8", 200
        if path in ("/ops/slow.torrent", "/ops/batch.torrent"):
            kind = path[len("/ops/"):-len(".torrent")]
            tag = ops_tag((query.get("run") or [""])[0])
            return ops_torrent(kind, tag, self.metadata["base_url"]), "application/x-bittorrent", 200
        return None


def write_metadata(path: Path, metadata: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(
        mode="w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", delete=False
    ) as handle:
        json.dump(metadata, handle, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
        temporary = Path(handle.name)
    temporary.chmod(0o600)
    temporary.replace(path)
    path.chmod(0o600)


class FixtureHandler(BaseHTTPRequestHandler):
    server: "FixtureHTTPServer"

    def _respond(self, include_body: bool) -> None:
        path = urllib.parse.urlsplit(self.path).path
        if path.startswith("/ops/slow/") or path.startswith("/ops/batch/"):
            self._respond_content(path, include_body)
            return
        result = self.server.fixture.body(self.path)
        if result is None:
            self.send_error(404)
            return
        body, content_type, status = result
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if include_body:
            self.wfile.write(body)

    def _respond_content(self, path: str, include_body: bool) -> None:
        """Serve ops web-seed bytes with single-range support, throttled for the slow file."""
        content = ops_content(path)
        if content is None:
            self.send_error(404)
            return
        data, rate = content
        start, end, status = 0, len(data) - 1, 200
        header = self.headers.get("Range", "")
        if header.startswith("bytes=") and "," not in header:
            first, _, last = header[len("bytes="):].partition("-")
            try:
                if first:
                    start = int(first)
                    end = min(int(last), len(data) - 1) if last else len(data) - 1
                else:
                    start = max(len(data) - int(last), 0)
                status = 206
            except ValueError:
                start, end, status = 0, len(data) - 1, 200
            if start > end or start >= len(data):
                self.send_error(416)
                return
        body = data[start:end + 1]
        self.send_response(status)
        self.send_header("Content-Type", "video/x-matroska")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Accept-Ranges", "bytes")
        if status == 206:
            self.send_header("Content-Range", f"bytes {start}-{end}/{len(data)}")
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if not include_body:
            return
        chunk = 8192
        try:
            for offset in range(0, len(body), chunk):
                self.wfile.write(body[offset:offset + chunk])
                if rate:
                    time.sleep(chunk / rate)
        except (BrokenPipeError, ConnectionResetError):
            return

    def do_GET(self) -> None:  # noqa: N802 - stdlib handler API
        self._respond(True)

    def do_HEAD(self) -> None:  # noqa: N802 - stdlib handler API
        self._respond(False)

    def log_message(self, _format: str, *_args: Any) -> None:
        # Avoid request metadata in the session log; the fixture is intentionally quiet.
        return


class FixtureHTTPServer(ThreadingHTTPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, address: tuple[str, int], fixture: Fixture) -> None:
        super().__init__(address, FixtureHandler)
        self.fixture = fixture


def start_server(bind_host: str, advertised_host: str, port: int) -> tuple[FixtureHTTPServer, Fixture]:
    # Bind first so an ephemeral port can be included in every generated URL.
    server = FixtureHTTPServer((bind_host, port), Fixture(bind_host, advertised_host, 0))
    fixture = Fixture(bind_host, advertised_host, server.server_address[1])
    server.fixture = fixture
    return server, fixture


def fetch(url: str) -> bytes:
    with urllib.request.urlopen(url, timeout=5) as response:
        if response.status != 200:
            raise AssertionError(f"{url} returned HTTP {response.status}")
        return response.read()


def bdecode(data: bytes, offset: int = 0) -> tuple[Any, int]:
    marker = data[offset:offset + 1]
    if marker == b"i":
        end = data.index(b"e", offset)
        return int(data[offset + 1:end]), end + 1
    if marker == b"l":
        values = []
        offset += 1
        while data[offset:offset + 1] != b"e":
            value, offset = bdecode(data, offset)
            values.append(value)
        return values, offset + 1
    if marker == b"d":
        values: dict[bytes, Any] = {}
        offset += 1
        while data[offset:offset + 1] != b"e":
            key, offset = bdecode(data, offset)
            value, offset = bdecode(data, offset)
            values[key] = value
        return values, offset + 1
    if marker.isdigit():
        separator = data.index(b":", offset)
        length = int(data[offset:separator])
        start = separator + 1
        return data[start:start + length], start + length
    raise AssertionError(f"invalid bencode marker at offset {offset}: {marker!r}")


def self_check() -> None:
    server, fixture = start_server("127.0.0.1", "127.0.0.1", 0)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        metadata = fixture.metadata
        health = json.loads(fetch(metadata["health_url"]))
        if health["status"] != "ok" or health["fixture_id"] != FIXTURE_ID or not health["redacted"]:
            raise AssertionError(f"unexpected health metadata: {health}")
        feed = fetch(metadata["feed_url"])
        if metadata["guid"].encode() not in feed or metadata["torrent_url"].encode() not in feed:
            raise AssertionError("feed does not contain the fixed item and torrent URL")
        artifact = fetch(metadata["artifact_url"])
        if artifact != ARTIFACT_BYTES or hashlib.sha256(artifact).hexdigest() != metadata["artifact_sha256"]:
            raise AssertionError("artifact bytes or digest are not deterministic")
        torrent = fetch(metadata["torrent_url"])
        decoded, end = bdecode(torrent)
        if end != len(torrent) or decoded[b"info"][b"length"] != len(ARTIFACT_BYTES):
            raise AssertionError("torrent length is inconsistent with artifact")
        info = decoded[b"info"]
        if info[b"name"] != ARTIFACT_NAME.encode() or info[b"pieces"] != hashlib.sha1(artifact).digest():
            raise AssertionError("torrent info does not match artifact")
        if decoded[b"url-list"] != [metadata["artifact_url"].encode()]:
            raise AssertionError("torrent web-seed URL is inconsistent with artifact URL")

        season = json.loads(fetch(metadata["provider_season_url"] + "?season=" + PROVIDER_SEASON))
        if not season.get("ok") or season["data"]["total"] != 1:
            raise AssertionError("provider season payload is not the single fixture entry")
        entry = season["data"]["byWeekday"][0]["animes"][0]
        if entry["bgmId"] != PROVIDER_BGM_ID or entry["title"]["chinese"] != PROVIDER_SHOW_TITLE:
            raise AssertionError("provider season entry identity drifted")
        groups = json.loads(fetch(metadata["provider_groups_url"] + f"?bgmId={PROVIDER_BGM_ID}"))
        if not groups.get("ok") or groups["data"]["total"] != 1:
            raise AssertionError("provider group payload is not the single fixture group")
        if groups["data"]["groups"][0]["slug"] != PROVIDER_GROUP_SLUG:
            raise AssertionError("provider group slug drifted")
        provider_feed = fetch(
            metadata["provider_feed_url"] + f"?bgmId={PROVIDER_BGM_ID}&groupSlug={PROVIDER_GROUP_SLUG}"
        )
        if PROVIDER_GUID.encode() not in provider_feed or PROVIDER_RELEASE_TITLE.encode() not in provider_feed:
            raise AssertionError("provider feed does not carry the fixed provider release")
        if f"<infohash>{INFO_HASH}</infohash>".encode() not in provider_feed or f"<infohash>{INFO_HASH}</infohash>".encode() not in feed:
            raise AssertionError("feeds do not publish the fixture torrent infohash")
        if provider_feed.count(b"<infohash>") != 1:
            raise AssertionError("the provider feed's extra item must publish no infohash")
        if hashlib.sha1(bencode(info)).hexdigest() != INFO_HASH:
            raise AssertionError("published infohash does not match the served torrent")
        found = json.loads(fetch(metadata["provider_base_url"] + "/api/bgm/search?q=browser"))
        if not found.get("ok") or [item["bgmId"] for item in found["data"]] != [PROVIDER_BGM_ID]:
            raise AssertionError(f"provider search does not find the fixture entry: {found}")
        missing = json.loads(fetch(metadata["provider_base_url"] + "/api/bgm/search?q=nothing-here"))
        if not missing.get("ok") or missing["data"] != []:
            raise AssertionError("provider search matched an unrelated query")
        ops_feed = fetch(metadata["ops_feed_url"] + "?run=self-check")
        tag = ops_tag("self-check")
        if OPS_SLOW_TITLE.encode() not in ops_feed or OPS_BATCH_TITLE.encode() not in ops_feed:
            raise AssertionError("ops feed does not carry both ops items")
        if fetch(metadata["ops_feed_url"] + "?run=self-check&items=slow").count(b"<item>") != 1:
            raise AssertionError("ops feed item selection is not honored")
        for kind in ("slow", "batch"):
            ops_decoded, ops_end = bdecode(fetch(f"{metadata['base_url']}/ops/{kind}.torrent?run=self-check"))
            ops_info = ops_decoded[b"info"]
            if ops_decoded[b"url-list"] != [f"{metadata['base_url']}/ops/{kind}/".encode()]:
                raise AssertionError(f"ops {kind} web-seed URL drifted")
            if kind == "slow":
                if ops_info[b"name"] != ops_slow_name(tag).encode() or ops_info[b"pieces"] != pieces_of(OPS_SLOW_BYTES, OPS_PIECE_LENGTH):
                    raise AssertionError("ops slow torrent does not match its content")
                request = urllib.request.Request(
                    f"{metadata['base_url']}/ops/slow/{urllib.parse.quote(ops_slow_name(tag))}",
                    headers={"Range": f"bytes=0-{OPS_PIECE_LENGTH - 1}"},
                )
                with urllib.request.urlopen(request, timeout=10) as response:
                    if response.status != 206 or response.read() != OPS_SLOW_BYTES[:OPS_PIECE_LENGTH]:
                        raise AssertionError("ops slow web seed does not serve the requested range")
            else:
                content = b"".join(data for _, data in OPS_BATCH_FILES)
                if ops_info[b"name"] != ops_batch_name(tag).encode() or ops_info[b"pieces"] != pieces_of(content, OPS_PIECE_LENGTH):
                    raise AssertionError("ops batch torrent does not match its content")
                for file_name, data in OPS_BATCH_FILES:
                    served = fetch(
                        f"{metadata['base_url']}/ops/batch/{urllib.parse.quote(ops_batch_name(tag))}/{urllib.parse.quote(file_name)}"
                    )
                    if served != data:
                        raise AssertionError(f"ops batch web seed does not serve {file_name}")
        counters = json.loads(fetch(metadata["counters_url"]))
        for path in ("/api/seasons/anime", "/api/anime/groups", "/rss/anime.xml"):
            if counters.get(path, 0) < 1:
                raise AssertionError(f"provider counter did not record {path}: {counters}")
        if counters.get("/__counters") is not None:
            raise AssertionError("counter probe must not be counted")
        print(json.dumps({**metadata, "self_check": "passed", "counters": counters}, sort_keys=True))
    finally:
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bind-host", default="0.0.0.0")
    parser.add_argument("--url-host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--metadata-file", type=Path)
    parser.add_argument("--self-check", action="store_true")
    args = parser.parse_args()

    if args.self_check:
        self_check()
        return 0

    server, fixture = start_server(args.bind_host, args.url_host, args.port)
    if args.metadata_file is not None:
        write_metadata(args.metadata_file, fixture.metadata)
    print(json.dumps(fixture.metadata, sort_keys=True), flush=True)

    def request_stop(_signum: int, _frame: Any) -> None:
        # shutdown() must run outside serve_forever's thread to avoid a deadlock.
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, request_stop)
    signal.signal(signal.SIGINT, request_stop)
    try:
        server.serve_forever(poll_interval=0.2)
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
