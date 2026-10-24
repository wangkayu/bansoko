#!/usr/bin/env python3
"""Independent generator for the acquisition DTO golden vectors.

`internal/acquisition/terminal/domain/testdata/dto-golden-vectors.json` carries the
input values (its `definitions`), the vectors that name one definition plus a digest
domain, and the negative vectors of the parse layer. The expected byte length and
SHA-256 of every vector are *computed* here, by an encoder that shares no code with
the Go package it checks (`internal/acquisition/terminal/domain`). The Go test then
re-encodes the same definitions with its own encoder and refuses a mismatch, so the
two implementations have to agree byte for byte (DV01: not only Python
self-consistent).

The encoding is the one `canonical.go` documents:

* objects: keys in ascending UTF-8 byte order, no whitespace, `{}` when empty;
* strings: raw UTF-8, escaping only `"`, `\\` and U+0000..U+001F as lower-case
  `\\u00xx`; U+2028/U+2029 and non-ASCII are written literally;
* integers: `-?(0|[1-9][0-9]*)` as decimal text; the protocol has no floats;
* `null`, `true`, `false` literally.

A digest is `"sha256:" + hex(SHA256(domain || 0x00 || canonicalBytes))`.

Usage:
  scripts/generators/generate-dto-golden-vectors.py --check   # recompute and compare
  scripts/generators/generate-dto-golden-vectors.py --write   # recompute and rewrite the file
"""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = ROOT / "internal/acquisition/terminal/domain/testdata/dto-golden-vectors.json"

CANONICAL_VERSION = "acquisition-canonical-v1"

# The vector table: id, the registered type the definition validates as, the digest
# domain, and the definition member that holds the value. The definitions themselves
# stay in the fixture file, so a schema change edits the data and re-runs --write.
VECTOR_TABLE = [
    ("GV01", "SourceV1", "acquisition-input:source:v1", "source"),
    ("GV02", "SelectionV1", "acquisition-input:selection:v1", "selection"),
    ("GV03", "ConfigV1", "acquisition-input:config:v1", "config"),
    ("GV04", "MediaV1", "acquisition-input:media:v1", "media"),
    ("GV05", "DeliveryV1", "acquisition-input:delivery:v1", "delivery"),
    ("GV06", "LimitsV1", "acquisition-input:limits:v1", "limits"),
    ("GV07", "PlacementV1", "acquisition-input:placement:v1", "placement"),
    ("GV08", "AcquisitionInputV1", "acquisition-input:v1", "input"),
    ("GV09", "FileFactV1", "placement-file-facts:v1", "file_fact"),
    ("GV10", "ClosureV1", "acquisition-effect-closure:v1", "closure"),
]

# The negative vectors of the lexical/duplicate-key layer. They are fixed strings:
# the vectors of the typed layers live where those types live.
NEGATIVE_TABLE = [
    ("NV01", "PlacementV1", '{"mode":"none","mode":"none"}', "DUPLICATE_KEY"),
    ("NV02", "PlacementV1", "{}", "MISSING_KEY"),
    (
        "NV03",
        "FrozenValue",
        '{"state":"absent","value":null,"reason_code":null,"extra":null}',
        "UNKNOWN_KEY",
    ),
    (
        "NV04",
        "FrozenValueInt64",
        '{"state":"known","value":1e0,"reason_code":null}',
        "INTEGER_TOKEN_INVALID",
    ),
    ("NV05", "PlacementV1", '{"mode":"none"}{}', "TRAILING_TOKEN"),
    (
        "NV06",
        "FrozenValue",
        '{"state":"known","value":"\\u0000","reason_code":null}',
        "NUL_FORBIDDEN",
    ),
    (
        "NV07",
        "PlacementV1",
        '{"mode":"none","managed_anime_ref":null,"expected_placement":null}',
        "UNION_BRANCH_INVALID",
    ),
    (
        "NV08",
        "QualityFieldString",
        '{"state":"value","value":null,"reason_code":null,"raw_digest":null}',
        "NULL_NOT_ALLOWED",
    ),
]

_HEX = "0123456789abcdef"
MAX_FIXTURE_DEPTH = 16


def canonical(value) -> bytes:
    """The one byte encoding of a decoded JSON value."""
    if value is None:
        return b"null"
    if value is True:
        return b"true"
    if value is False:
        return b"false"
    if isinstance(value, int):
        return str(value).encode("ascii")
    if isinstance(value, float):
        raise ValueError("the canonical protocol has no floats")
    if isinstance(value, str):
        return _canonical_string(value)
    if isinstance(value, list):
        return b"[" + b",".join(canonical(item) for item in value) + b"]"
    if isinstance(value, dict):
        keys = sorted(value.keys(), key=lambda k: k.encode("utf-8"))
        parts = [
            _canonical_string(key) + b":" + canonical(value[key]) for key in keys
        ]
        return b"{" + b",".join(parts) + b"}"
    raise ValueError(f"unsupported JSON value: {value!r}")


def _canonical_string(text: str) -> bytes:
    out = bytearray(b'"')
    for byte in text.encode("utf-8"):
        if byte == 0x22:
            out += b'\\"'
        elif byte == 0x5C:
            out += b"\\\\"
        elif byte < 0x20:
            out += b"\\u00" + _HEX[byte >> 4].encode("ascii") + _HEX[byte & 0xF].encode("ascii")
        else:
            out.append(byte)
    out += b'"'
    return bytes(out)


def digest(domain: str, canonical_bytes: bytes) -> str:
    return "sha256:" + hashlib.sha256(
        domain.encode("ascii") + b"\x00" + canonical_bytes
    ).hexdigest()


def expand(definitions: dict, value, stack: tuple[str, ...] = ()):
    """Replace {"$fixture": name} with that definition, bounded and acyclic."""
    if isinstance(value, dict) and len(value) == 1 and "$fixture" in value:
        name = value["$fixture"]
        if not isinstance(name, str):
            raise ValueError("$fixture must name a definition")
        if len(stack) >= MAX_FIXTURE_DEPTH:
            raise ValueError(f"fixture reference depth exceeds {MAX_FIXTURE_DEPTH}")
        if name in stack:
            raise ValueError(f"fixture reference cycle: {stack} -> {name}")
        if name not in definitions:
            raise ValueError(f"unknown fixture definition {name!r}")
        return expand(definitions, definitions[name], stack + (name,))
    if isinstance(value, dict):
        return {key: expand(definitions, member, stack) for key, member in value.items()}
    if isinstance(value, list):
        return [expand(definitions, item, stack) for item in value]
    return value


def build(definitions: dict) -> dict:
    vectors = []
    for ident, type_name, domain, definition in VECTOR_TABLE:
        if definition not in definitions:
            raise ValueError(f"{ident}: definition {definition!r} is missing")
        expanded = expand(definitions, definitions[definition], (definition,))
        encoded = canonical(expanded)
        vectors.append(
            {
                "id": ident,
                "type": type_name,
                "domain": domain,
                "definition": definition,
                "expected_sha256": digest(domain, encoded)[len("sha256:") :],
                "expected_bytes": len(encoded),
            }
        )
    negatives = [
        {
            "id": ident,
            "type": type_name,
            "raw": raw,
            "expected_reason": reason,
        }
        for ident, type_name, raw, reason in NEGATIVE_TABLE
    ]
    return {
        "fixture_schema_version": 1,
        "canonical_version": CANONICAL_VERSION,
        "definitions": definitions,
        "vectors": vectors,
        "negative_vectors": negatives,
    }


def render(document: dict) -> str:
    return json.dumps(document, ensure_ascii=False, indent=2) + "\n"


def load() -> dict:
    with FIXTURE.open(encoding="utf-8") as handle:
        return json.load(handle)


def check() -> int:
    current = load()
    computed = build(current["definitions"])
    failures = []
    for want, got in zip(computed["vectors"], current["vectors"]):
        for field in ("id", "type", "domain", "definition"):
            if want[field] != got[field]:
                failures.append(f"{got.get('id')}: {field} = {got[field]!r}, want {want[field]!r}")
        if want["expected_sha256"] != got["expected_sha256"]:
            failures.append(
                f"{got['id']}: sha256 = {got['expected_sha256']}, want {want['expected_sha256']}"
            )
        if want["expected_bytes"] != got["expected_bytes"]:
            failures.append(
                f"{got['id']}: bytes = {got['expected_bytes']}, want {want['expected_bytes']}"
            )
    if computed["negative_vectors"] != current["negative_vectors"]:
        failures.append("negative_vectors differ from the generator's fixed table")
    if computed["fixture_schema_version"] != current["fixture_schema_version"]:
        failures.append("fixture_schema_version differs")
    if computed["canonical_version"] != current["canonical_version"]:
        failures.append("canonical_version differs")
    if failures:
        for failure in failures:
            print(f"golden vectors stale: {failure}", file=sys.stderr)
        return 1
    print(f"golden vectors reproduce: {len(computed['vectors'])} vectors, {len(NEGATIVE_TABLE)} negatives")
    return 0


def write() -> int:
    current = load()
    document = build(current["definitions"])
    FIXTURE.write_text(render(document), encoding="utf-8")
    print(f"wrote {FIXTURE.relative_to(ROOT)}: {len(document['vectors'])} vectors")
    return 0


def main(argv: list[str]) -> int:
    if argv == ["--check"]:
        return check()
    if argv == ["--write"]:
        return write()
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
