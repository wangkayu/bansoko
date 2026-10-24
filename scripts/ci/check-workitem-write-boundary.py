#!/usr/bin/env python3
"""Check complete SQL literals for WorkItem DML and row-lock targets."""
import pathlib
import re
import sys

root = pathlib.Path(__file__).resolve().parents[2]
dml = re.compile(r"\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|TRUNCATE(?:\s+TABLE)?)\s+(?:public\.)?work_items\b", re.I)
locks = re.compile(r"\bFOR\s+(?:NO\s+KEY\s+UPDATE|KEY\s+SHARE|UPDATE|SHARE)(?:\s+OF\s+([\w., ]+?))?(?=\s+(?:SKIP|NOWAIT)|\s*;?\s*$)", re.I | re.M)


def locks_workitem(sql):
    aliases = {"work_items"}
    for match in re.finditer(r"(?:FROM|JOIN)\s+(?:public\.)?work_items(?:\s+(?:AS\s+)?(\w+))?", sql, re.I):
        if match[1] and match[1].upper() not in {"WHERE", "JOIN", "ORDER", "FOR"}:
            aliases.add(match[1])
    if not re.search(r"\b(?:FROM|JOIN)\s+(?:public\.)?work_items\b", sql, re.I):
        return False
    return any(not m[1] or set(re.split(r"\s*,\s*", m[1].strip())) & aliases for m in locks.finditer(sql))


errors = []
for path in (root / "internal").rglob("*"):
    if path.suffix not in (".go", ".sql") or path.name.endswith("_test.go"):
        continue
    relative = path.relative_to(root).as_posix()
    if relative.startswith("internal/testutil/") or "generated" in path.parts:
        continue
    text = path.read_text()
    blocks = [(m.start(1), m[1]) for m in re.finditer(r"`([^`]+)`", text, re.S)] if path.suffix == ".go" else [(m.start(), m[0]) for m in re.finditer(r"(?ms)^-- name: .*?(?=^-- name: |\Z)", text)]
    for pos, sql in blocks:
        dynamic_suffix = path.suffix == ".go" and "work_items" in sql and re.match(r"\s*\+", text[pos + len(sql) + 1:])
        if dml.search(sql) or locks_workitem(sql) or dynamic_suffix:
            line = text.count("\n", 0, pos) + 1
            errors.append(f"{relative}:{line}: WorkItem DML/row locks must call fixed owner functions")
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print("WorkItem DML and row-lock boundary passed")
