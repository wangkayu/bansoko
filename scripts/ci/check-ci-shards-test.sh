#!/usr/bin/env bash
# The shard table is only load-bearing if the checker refuses every way it can
# stop matching the canonical package set. Positive control first, then one
# mutant per rule: each must be rejected, and for the reason it targets.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT

bash "$root/scripts/ci/check-ci-shards.sh" >"$temp/positive.log" 2>&1

packages="$root/scripts/ci/postgres-packages.txt"
shards="$root/scripts/ci/postgres-shards.txt"

# mutant <name> <expected message fragment> <file to rewrite> -- the rewrite is
# a python program reading the source and writing the mutant.
expect_refusal() {
  local name="$1" fragment="$2" packages_file="$3" shards_file="$4"
  if BANSOKO_POSTGRES_PACKAGES_FILE="$packages_file" \
     BANSOKO_POSTGRES_SHARDS_FILE="$shards_file" \
     bash "$root/scripts/ci/check-ci-shards.sh" >"$temp/$name.log" 2>&1; then
    echo "shard checker accepted mutant: $name" >&2
    exit 1
  fi
  grep -qF -- "$fragment" "$temp/$name.log" || {
    echo "mutant $name was refused for the wrong reason:" >&2
    cat "$temp/$name.log" >&2
    exit 1
  }
}

# A package listed but never assigned: the gate would silently stop running it.
python3 - "$shards" "$temp/missing.txt" <<'PY'
import pathlib, sys
source, out = sys.argv[1:]
lines = [l for l in pathlib.Path(source).read_text().splitlines() if not l.endswith('./internal/parserpolicy')]
pathlib.Path(out).write_text('\n'.join(lines) + '\n')
PY
expect_refusal missing 'in no shard: ./internal/parserpolicy' "$packages" "$temp/missing.txt"

# A package assigned twice: it would run twice while the lane still looked green.
python3 - "$shards" "$temp/duplicate.txt" <<'PY'
import pathlib, sys
source, out = sys.argv[1:]
text = pathlib.Path(source).read_text()
pathlib.Path(out).write_text(text + '1\t./internal/parserpolicy\n')
PY
expect_refusal duplicate 'assigned more than once: ./internal/parserpolicy' "$packages" "$temp/duplicate.txt"

# A package nobody has anymore: a rename left the old path behind as a stale
# extra row (the canonical list still has every real package).
python3 - "$shards" "$temp/unknown.txt" <<'PY'
import pathlib, sys
source, out = sys.argv[1:]
pathlib.Path(out).write_text(pathlib.Path(source).read_text() + '3\t./internal/parserpolicy_old\n')
PY
expect_refusal unknown 'assigned but not in postgres-packages.txt: ./internal/parserpolicy_old' "$packages" "$temp/unknown.txt"

# A gap in the shard numbers: the workflow would ask for a shard that is not there.
python3 - "$shards" "$temp/gap.txt" <<'PY'
import pathlib, sys
source, out = sys.argv[1:]
text = pathlib.Path(source).read_text().replace('\n3\t', '\n4\t')
pathlib.Path(out).write_text(text)
PY
expect_refusal gap 'shard numbers are [1, 2, 4], want [1, 2, 3, 4] with no gaps' "$packages" "$temp/gap.txt"

echo 'postgres shard checker positive and four refusal cases passed'
