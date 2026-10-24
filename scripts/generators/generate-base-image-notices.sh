#!/usr/bin/env sh
# Writes the components the base image contributes to the notice list.
#
# The image is a distribution: busybox, musl and the rest of the Alpine layer
# travel inside it as object code, and their licences (busybox is GPL-2.0-only)
# apply to that layer exactly as the Go modules' do to the binaries. The Go
# stage cannot list them — it is a different image — so the runtime stage is
# where the installed-package database exists, and this script reads it there.
#
# Two layers ship object code from Alpine trees:
#   - the runtime layer itself (alpine:3.20 + what `apk add` installed), read
#     from this stage's package database;
#   - the PostgreSQL client tools (postgres:17-alpine, so Alpine 3.24.1),
#     listed by generate-pg17-components.sh in the stage that holds both those
#     files and the package records that own them, and handed to us as a TSV.
# A package present in both at different versions is replaced at runtime by the
# PostgreSQL layer's copy, so its row carries the version actually in the image
# and names the version it displaced.
#
# The licence texts follow the rule the Go stage applies: MIT/BSD/GPL all
# require the notice to travel with the binary, and these components come from
# upstream trees rather than the Go module graph, so their texts are not in
# what that stage collected. The texts live in deployment/licenses, one file
# per SPDX identifier; an identifier with no file aborts the build.
#
# Usage: generate-base-image-notices.sh <notices.md> <pg17-components.tsv> <licence-dir> <licences.txt>
set -eu
notices="${1:?usage: generate-base-image-notices.sh <notices.md> <components.tsv> <licence-dir> <licences.txt>}"
pg_components="${2:?usage: generate-base-image-notices.sh <notices.md> <components.tsv> <licence-dir> <licences.txt>}"
licence_dir="${3:?usage: generate-base-image-notices.sh <notices.md> <components.tsv> <licence-dir> <licences.txt>}"
licences="${4:?usage: generate-base-image-notices.sh <notices.md> <components.tsv> <licence-dir> <licences.txt>}"
db=/lib/apk/db/installed
[ -f "$db" ] || { echo "generate-base-image-notices: $db is missing; refusing to ship an unlisted base layer" >&2; exit 1; }
[ -s "$pg_components" ] || { echo "generate-base-image-notices: $pg_components is empty; the PostgreSQL layer would go unlisted" >&2; exit 1; }

tmp="${TMPDIR:-/tmp}/generate-base-image-notices.$$"
trap 'rm -f "$tmp.runtime" "$tmp.runtime.sorted" "$tmp.runtime.listed" "$tmp.pg.sorted"' EXIT INT TERM

# The installed database is a sequence of records separated by blank lines with
# one-letter fields: P name, V version, L licence (an SPDX expression), U source
# URL. A record without a licence is a component we cannot name, so it aborts
# the build rather than passing silently.
awk -F: '
  /^P:/ {p=substr($0,3)}
  /^V:/ {v=substr($0,3)}
  /^L:/ {l=substr($0,3)}
  /^U:/ {u=substr($0,3)}
  /^$/ {
    if (p != "") {
      if (l == "") { print "UNLICENSED " p > "/dev/stderr"; bad=1 }
      printf "%s\t%s\t%s\t%s\n", p, v, l, u
      p=""; v=""; l=""; u=""
    }
  }
  END {
    if (p != "") {
      if (l == "") { print "UNLICENSED " p > "/dev/stderr"; bad=1 }
      printf "%s\t%s\t%s\t%s\n", p, v, l, u
    }
    if (bad) exit 1
  }' "$db" >"$tmp.runtime" || {
  echo "generate-base-image-notices: the base layer has a package with no declared licence" >&2
  exit 1
}
[ -s "$tmp.runtime" ] || { echo "generate-base-image-notices: $db listed no packages" >&2; exit 1; }
sort "$tmp.runtime" >"$tmp.runtime.sorted"
sort "$pg_components" >"$tmp.pg.sorted"

# A package the PostgreSQL layer carries at another version has had its files
# replaced in the image, so its runtime row would name a version that is no
# longer there. Those rows are dropped here and appear once, below, at the
# version actually shipped; the Replaces column keeps the trace.
awk -F'\t' 'NR == FNR { pg[$1] = $2; next }
            !($1 in pg) || pg[$1] == $2' "$tmp.pg.sorted" "$tmp.runtime.sorted" >"$tmp.runtime.listed"

{
  cat <<'EOF'

## Base image packages

The runtime layer is `alpine`, whose packages ship with the image as installed binaries. They keep their own licences; each package's licence text and complete source are published by Alpine in its `aports` tree at the source URL below, which is where the corresponding source for this layer can be obtained. A package the PostgreSQL layer replaces at another version is listed under that layer instead, so no component appears twice.

| Package | Version | License | Source |
|---|---|---|---|
EOF
  awk -F'\t' '{ printf "| %s | %s | %s | %s |\n", $1, $2, $3, ($4 == "" ? "-" : $4) }' "$tmp.runtime.listed"

  cat <<'EOF'

## PostgreSQL client components

`pg_dump`, `pg_restore`, `psql` and the `libpq` they link come from the pinned `postgres:17-alpine` image (Alpine 3.24.1). They are installed at their original paths together with every non-musl shared library they need. The runtime layer's own package database belongs to Alpine 3.20 and cannot describe them, so they are listed here instead. Where a component also exists in the runtime layer, its row names the version this image actually carries and the older one it replaced.

| Component | Version | License | Source | Replaces |
|---|---|---|---|---|
EOF
  awk -F'\t' '
    # A runtime package whose PostgreSQL-layer namesake differs in version: the
    # copied file is the one in the image, so the row reports the newer version.
    NR == FNR { rt[$1] = $2; next }
    {
      replaces = ""
      if (($1 in rt) && rt[$1] != $2) replaces = rt[$1] " (runtime layer)"
      printf "| %s | %s | %s | %s | %s |\n", $1, $2, $3, ($4 == "" ? "-" : $4), (replaces == "" ? "-" : replaces)
    }' "$tmp.runtime" "$tmp.pg.sorted"
} >>"$notices"

# Every SPDX identifier in both tables needs its text beside the list. The
# expressions here are the plain "A AND B" / "A OR B" forms, so splitting on
# whitespace and dropping the operators yields the identifiers.
ids="$( { cut -f3 "$tmp.runtime"; cut -f3 "$tmp.pg.sorted"; } \
  | tr ' ' '\n' | grep -v -x -e '' -e 'AND' -e 'OR' -e '(' -e ')' | sort -u)"

{
  printf '\n================================================================================\n'
  printf 'Base image licence texts\n'
  printf '================================================================================\n'
  printf '\nThe texts below belong to the components listed in the base image sections above, one per SPDX identifier the components declare.\n'
} >>"$licences"

for id in $ids; do
  case "$id" in
    Public-Domain)
      # Not a licence: the component dedicates its works to the public domain
      # and says so in its own source, so there is no text to reproduce.
      printf '\n--------------------------------------------------------------------------------\n%s\n--------------------------------------------------------------------------------\nNo licence text: the component dedicates its works to the public domain.\n' "$id" >>"$licences"
      continue
      ;;
    GPL-2.0-only) file=GPL-2.0.txt ;;
    GPL-2.0-or-later) file=GPL-2.0.txt ;;
    GPL-3.0-only) file=GPL-3.0.txt ;;
    GPL-3.0-or-later) file=GPL-3.0.txt ;;
    LGPL-2.0-only) file=LGPL-2.0.txt ;;
    LGPL-2.0-or-later) file=LGPL-2.0.txt ;;
    LGPL-2.1-only) file=LGPL-2.1.txt ;;
    LGPL-2.1-or-later) file=LGPL-2.1.txt ;;
    LGPL-3.0-only) file=LGPL-3.0.txt ;;
    LGPL-3.0-or-later) file=LGPL-3.0.txt ;;
    *) file="$id.txt" ;;
  esac
  if [ ! -f "$licence_dir/$file" ]; then
    echo "generate-base-image-notices: no licence text for $id (expected deployment/licenses/$file)" >&2
    exit 1
  fi
  {
    printf '\n--------------------------------------------------------------------------------\n%s\n--------------------------------------------------------------------------------\n' "$id"
    cat "$licence_dir/$file"
    printf '\n'
  } >>"$licences"
done
