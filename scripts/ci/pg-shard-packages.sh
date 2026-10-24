#!/usr/bin/env bash
# Print the packages assigned to one PostgreSQL gate shard.
#
# Usage: pg-shard-packages.sh <n>/<total>
#
# `scripts/ci/test-integration.sh` is the only caller: the container lane runs it
# once per shard job with `BANSOKO_TEST_PG_SHARD` set, while host-runner
# and local runs leave the variable unset and get the whole set. The selection
# lives here so it can be exercised without a database; the table itself is
# checked against `scripts/ci/postgres-packages.txt` by scripts/ci/check-ci-shards.sh.
set -euo pipefail

spec="${1:?usage: pg-shard-packages.sh <n>/<total>}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
table="${BANSOKO_POSTGRES_SHARDS_FILE:-$root/scripts/ci/postgres-shards.txt}"

[[ "$spec" =~ ^([1-9][0-9]*)/([1-9][0-9]*)$ ]] \
  || { echo "shard selector ${spec} is not <n>/<total>" >&2; exit 1; }
index="${BASH_REMATCH[1]}"
total="${BASH_REMATCH[2]}"

[[ -f "$table" ]] || { echo "missing shard table $table" >&2; exit 1; }

last="$(awk -F'\t' '$1 ~ /^[0-9]+$/ { if ($1+0 > m) m = $1+0 } END { print m+0 }' "$table")"
[[ "$last" == "$total" ]] \
  || { echo "shard selector ${spec}: the table defines ${last} shard(s)" >&2; exit 1; }

selected="$(awk -F'\t' -v want="$index" '$1 == want { print $2 }' "$table")"
[[ -n "$selected" ]] || { echo "shard ${spec} selects no package" >&2; exit 1; }
printf '%s\n' "$selected"
