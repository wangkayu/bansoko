#!/usr/bin/env bash
# Legacy-name guard for the Bansoko rename (RENAME-036).
#
# Reports every in-scope hit of the naming families the rename retires
# (product `v2` / old product naming, own format-version suffixes, `slim` and
# other transitional names, rewrite/legacy wording). The keep rules below are
# the whole classification.
#
# Usage:
#   scripts/ci/check-legacy-names.sh              # summary table, exit 0
#   scripts/ci/check-legacy-names.sh --list F     # every hit of family F
#   scripts/ci/check-legacy-names.sh --allowed    # allowlisted hits of strict families
#   scripts/ci/check-legacy-names.sh --paths      # tracked paths that must move
#   scripts/ci/check-legacy-names.sh --strict     # exit 1 on any non-allowlisted hit
#                                              # of a strict family
#
# Strict families: dir-v2, go-module, api-path, env-prefix, kebab-v2,
# mikan-product, slim (renamed in phase 2), db-prefix (B14 baseline) and
# ident-v2 (S1). mikan-product has no exceptions: any mention of the old
# product identities (the old product name in any spelling, the old processor
# binary, the archived first-implementation module) fails the gate. The live
# subscription provider `mikan` is never matched by its patterns. The only
# exceptions are the ALLOW entries below, each tagged with one category:
#   keep      names that are not ours to rename (test fixture hosts);
#   deferred  the acquisition-input registry's V2 type family (IdentityV2,
#             FileFactV2, ...), kept until the registry collapse (Q11) runs.
# version-suffix and bare-v2 stay report-only: their hits are the
# accepted-input registry versions (Q11), the retained -v1 labels (Q12),
# BitTorrent v2 prose and generic "version 2" fixtures, which would need a
# path-by-path allowlist far larger than the guard itself.
# rewrite-wording and legacy-word are report-only triage lists.
#
# Scope: tracked files (git ls-files) minus lockfiles and binary
# assets. Generated files are included (they change through regeneration) and
# are tagged [gen] in --list output.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 -I - "$root" "$@" <<'PY'
import os, re, subprocess, sys
from collections import Counter, defaultdict

root, args = sys.argv[1], sys.argv[2:]
strict = "--strict" in args
show_allowed = "--allowed" in args
paths_mode = "--paths" in args
list_family = args[args.index("--list") + 1] if "--list" in args else None

EXCLUDE = [
    "**/pnpm-lock.yaml", "go.sum", "skills-lock.json",
    "*.png", "*.jpg", "*.jpeg", "*.gif", "*.webp", "*.ico", "*.woff", "*.woff2",
    "*.torrent", "*.key",
    # this guard documents the patterns it searches for
    "scripts/ci/check-legacy-names.sh",
]
out = subprocess.run(
    ["git", "-C", root, "ls-files", "-z", "--", "."] + [":!:" + e for e in EXCLUDE],
    check=True, capture_output=True).stdout
files = [f for f in out.decode().split("\0") if f]

GENERATED = re.compile(
    r"(/generated/|\.gen\.go$|wire_gen\.go$|\.generated\.ts$|frontend/src/lib/api/generated\.ts$"
    r"|\.agents/skills/bansoko-agent/SKILL\.md$)")

# qBittorrent WebUI files: their /api/v2/auth/login is qB's, not ours.
QB_FILES = re.compile(
    r"^(internal/download/qbittorrent/|internal/download/(slim/)?application/admission_test\.go"
    r"|internal/configuration/application/(service|service_test|qb_management_postgres_test)\.go"
    r"|internal/integration/(recovery_real_effect|downloader_deletion_real)_test\.go|internal/downloadcompletion/service_test\.go"
    r"|scripts/verify/real-effect-browser-local-cleanup-assert\.sh$)")

# Each family: (name, [compiled patterns], keep(path, line, match) -> bool, note)
def keep_none(p, l, m): return False

def keep_dir(p, l, m):
    # qBittorrent WebUI paths (/api/v2/...) are qB's API, not a directory of ours.
    return m == "api/v2" and bool(re.search(r"/api/v2(/|\b)", l)) and (
        QB_FILES.match(p) or bool(re.search(r"/api/v2/(torrents|app|sync|transfer|auth)\b", l)))

def keep_api(p, l, m):
    if re.match(r"/api/v2/(torrents|app|sync|transfer)(?:/|$)", m): return True       # qB WebUI API
    if m.startswith("/api/v2/auth/log") and (QB_FILES.match(p) or re.search(r"(?i)qb|qbit|8087", l)):
        return True
    return False

BT_V2 = re.compile(r"(?i)info_?hash_?v2|hash_?v2|TorrentVersionV2|V2Tree|V2FileTree|fullV2Hash|V1V2|v1/v2 hybrid|BEP ?52")
def keep_ident(p, l, m): return bool(BT_V2.search(m))

EXTERNAL_V = re.compile(
    r"github\.com/|golang\.org/|gopkg\.in/|/api/v[0-9]|registry\.[a-z0-9.]+/v2/|open-apis/bot/v2"
    r"|pgx/v5|jsonschema/v6|uritemplate/v3|puddle/v2|regexp2/v2|oapi-codegen/v2|go-jsonmerge/v2"
    r"|@v[0-9]|SPDX-|draft-0[0-9]|json-schema\.org")
def keep_version(p, l, m):
    if re.match(r"(internal|api|migrations|deployment|scripts|src|v[0-9]+)/v[0-9]+$", m): return True  # dir-v2
    if BT_V2.search(m) or re.search(r"(?i)info_?hash_v1|hash_v1|yaml\.v[0-9]|^vnd\.|^lean-v1$", m): return True  # BEP 3/52, gopkg, OCI media types, Trellis workflow id
    return bool(EXTERNAL_V.search(l)) and "/" in m

RELEASE_REV = re.compile(r"(?i)\d+v2\b|\[v2\]|v3 (?:beats|好于) v2|v2 (?:beats|好于)")
def keep_bare(p, l, m):
    if RELEASE_REV.search(l) or BT_V2.search(l): return True                   # release revision / BEP 52
    if re.search(r"/api/v2/(torrents|app|sync|transfer)/|qBittorrent|WebUI", l): return True
    if EXTERNAL_V.search(l): return True
    return False

def P(*pats, flags=0): return [re.compile(x, flags) for x in pats]

FAMILIES = [
    ("dir-v2", P(r"\b(?:internal|api|db/migrations|deployment|scripts|frontend/src|src)/v2\b",
                 r"\bcmd/v2-[a-z]+"), keep_dir,
     "directory level v2 -> dropped (git mv + path rewrite)"),
    ("go-module", P(r"\bmikan(?:plus)/(?=[a-z])"), keep_none, "old Go module path -> bansoko"),
    ("api-path", P(r"/api/v2(?:/[A-Za-z0-9_{}.-]+)*"), keep_api,
     "our HTTP base path /api/v2 -> /api/v1 (qB WebUI /api/v2 kept)"),
    ("env-prefix", P(r"\b(?:VITE_)?V2_[A-Z0-9_]*", r"\bMIKAN(?:CTL)?_(?!REF_INVALID\b)[A-Z][A-Z0-9_]*"),
     keep_none, "V2_* / VITE_V2_* / MIKANCTL_* / MIKAN_*_CHILD -> BANSOKO_*"),
    ("db-prefix", P(r"\bv2_[a-z0-9_]+"), keep_none,
     "v2_ table/index/function/volume prefix -> dropped (B14 baseline)"),
    ("ident-v2", P(r"\bV2[A-Z][A-Za-z0-9_]*", r"\b[A-Za-z0-9_]*[a-z0-9]V2(?:[A-Z0-9][A-Za-z0-9_]*)?\b"),
     keep_ident, "Go/TS identifiers carrying V2 (BitTorrent v2 info-hash names kept)"),
    ("kebab-v2", P(r"(?<![A-Za-z0-9_/.@-])\.?v2-[a-z0-9][a-z0-9-]*"), lambda p, l, m: m == "v2-only",
     "v2-* CSS classes, binaries, compose services, spec file names"),
    ("mikan-product", P(
        r"(?i)mikan[-_ ]?plus(?!/)|mikan\+|\bmikanctl\b|mikan-agent|mikan-v2|mikan://",
        r"\bmikan\.(?:session|ci-owned|run-id|update029|candidate_fixture|retirement_owner)",
        r"(?<![A-Za-z])\.?mikan[-_](?:ci|sess|harness|gate|browser|release|cli|fm|soak|slim|image|sign"
        r"|channel|embedded|verify|user-network|cleanup|test|server|downloads|library|update|s7|permit"
        r"|naming-vars|session|delivery)\b",
        r"(?:addgroup -S|adduser -S -G|chown -R|chown root:|USER) mikan\b|\bmikan:mikan\b",
        r"\banimebd/mikan",
        r"(?i)torrent[-_]processor",
        r"(?<![\w/])archived/(?:v1|python-reference)"), keep_none,
     "product uses of mikan (provider mikan is never matched by these patterns)"),
    ("version-suffix", P(r"\b[a-z][a-z0-9]*(?:[-_.][a-z0-9]+)*[-_.]v[0-9]+\b(?![-.0-9])",
                         r"\b[a-z][a-z0-9-]*/v[0-9]+\b(?![-./0-9])"), keep_version,
     "own format/schema versions (-vN, _vN, .vN, /vN); each needs a reset/drop decision"),
    ("bare-v2", P(r"(?<![A-Za-z0-9_/.@-])[vV]2(?![A-Za-z0-9_-])"), keep_bare,
     "prose/strings 'V2' as product name (release revisions, qB, BEP 52 kept)"),
    ("slim", P(r"slim(?!e)|Slim|SLIM"), keep_none, "transitional 'slim' naming -> responsibility names"),
    ("rewrite-wording", P(r"(?i)clean[- ]rewrite|\bV2 rewrite\b|python[- ](?:parity|reference|era|implementation)"
                          r"|second version|第二版|重写|旧版",
                          r"\bV1\b"), keep_none,
     "rewrite / Python-era / V1 wording -> neutral"),
    ("legacy-word", P(r"(?i)\blegacy"), keep_none,
     "'legacy' identifiers/prose: triage (dead-after-B14 reader vs rename)"),
]

def path_family(f):
    hits = []
    if re.search(r"(^|/)v2(/|$)|(^|/)v2-|cmd/v2-", f): hits.append("dir-v2")
    if re.search(r"V2[A-Z]|[a-z]V2|_v[0-9]+[._]|-v[0-9]+[._]|\.v2\.", f): hits.append("version/ident")
    if re.search(r"slim|Slim", f): hits.append("slim")
    if re.search(r"mikanctl|mikan-agent|mikan[-_]?plus|torrent[-_]processor", f): hits.append("mikan-product")
    if re.search(r"(?i)(^|/)(m1|s[0-9]+[a-z]?|c[0-9]|todo[0-9]+)[_-]|oracle[0-9]*_|_s[0-9][a-z]?_", f):
        hits.append("task-code")
    return hits

if paths_mode:
    for f in files:
        h = path_family(f)
        if h: print(f"{','.join(h)}\t{f}")
    sys.exit(0)

STRICT = {"dir-v2", "go-module", "api-path", "env-prefix", "kebab-v2", "mikan-product", "slim", "db-prefix", "ident-v2"}

# (category, family, path regex, match regex, optional line regex). A strict-family
# hit is allowed when every given regex matches (path: re.search on the path;
# match: re.fullmatch on the hit; line: re.search on the line).
ALLOW = [
    # --- keep: test fixture hosts ---
    ("keep", "kebab-v2", r"", r"v2-fixture", r"v2-fixture\.test|v2-fixture"),
    # --- deferred: acquisition-input registry V2 family until Q11 collapses it ---
    ("deferred", "ident-v2",
     r"^(internal/acquisition/terminal/|internal/testutil/nativeinput/|internal/artifact/application/service\.go$"
     r"|internal/fileoperation/application/typed_satisfaction_filefact\.go$)",
     r"(?:Test|register|to)?(?:AcquisitionInput|Delivery|FileFact|Identity|ParserEnvelope|ParserProvenance"
     r"|ParserSection|Quality|Route|SectionEntry|UnclassifiedRoute|VarsInput|HistoricalInput|Input|schemas|valueSchema)V2[A-Za-z]*",
     None),
]
ALLOW = [(c, fam, re.compile(pp), re.compile(mp), re.compile(lp) if lp else None) for c, fam, pp, mp, lp in ALLOW]

def allowed_by(name, f, line, m):
    for cat, fam, pp, mp, lp in ALLOW:
        if fam is not None and fam != name: continue
        if pp.pattern and not pp.search(f): continue
        if not mp.fullmatch(m): continue
        if lp is not None and not lp.search(line): continue
        return cat
    return None

counts, filecounts, areas = Counter(), defaultdict(set), defaultdict(Counter)
allowed = defaultdict(Counter)
listing, allowed_listing = [], []
for f in files:
    try:
        text = open(os.path.join(root, f), encoding="utf-8").read()
    except (UnicodeDecodeError, IsADirectoryError, FileNotFoundError):
        continue
    gen = bool(GENERATED.search(f))
    area = "/".join(f.split("/")[:2]) if "/" in f else f
    for lineno, line in enumerate(text.splitlines(), 1):
        for name, pats, keep, _ in FAMILIES:
            for pat in pats:
                for mo in pat.finditer(line):
                    m = mo.group(0)
                    if keep(f, line, m): continue
                    if name in STRICT:
                        cat = allowed_by(name, f, line, m.lstrip("."))
                        if cat:
                            allowed[name][cat] += 1
                            if show_allowed:
                                allowed_listing.append(f"{cat}\t{f}:{lineno}:{m}")
                            continue
                    counts[name] += 1; filecounts[name].add(f); areas[name][area] += 1
                    if list_family == name:
                        listing.append(f"{f}:{lineno}:{'[gen] ' if gen else ''}{m}")

if list_family:
    print("\n".join(listing))
    sys.exit(0)
if show_allowed:
    print("\n".join(allowed_listing))
    sys.exit(0)

print(f"{'family':<16} {'mode':<7} {'hits':>7} {'files':>6}  allowed / top areas")
strict_total = 0
for name, _, _, note in FAMILIES:
    mode = "strict" if name in STRICT else "report"
    if name in STRICT:
        strict_total += counts[name]
        extra = ", ".join(f"{c}={n}" for c, n in sorted(allowed[name].items())) or "-"
    else:
        extra = ", ".join(f"{a}={c}" for a, c in areas[name].most_common(4))
    print(f"{name:<16} {mode:<7} {counts[name]:>7} {len(filecounts[name]):>6}  {extra}")
print(f"{'strict total':<24} {strict_total:>7}")
if strict and strict_total:
    print("legacy-name guard: strict families have non-allowlisted hits; run --list <family>", file=sys.stderr)
    sys.exit(1)
PY
