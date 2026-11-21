#!/usr/bin/env sh
# Writes the components the base image contributes to the notice list.
#
# The image is a distribution: busybox, musl and the rest of the Alpine layer
# travel inside it as object code, and their licences (busybox is GPL-2.0-only)
# apply to that layer exactly as the Go modules' do to the binaries. The Go
# stage cannot list them — it is a different image — so the runtime stage is
# where the installed-package database exists, and this script reads it there.
#
# The runtime APK database includes the OS libraries used by PostgreSQL.
# Only the PostgreSQL client binaries and libpq come from the pinned PostgreSQL
# image, which supplies their version and licence in a separate TSV.
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
trap 'rm -f "$tmp.runtime"' EXIT INT TERM

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

{
  cat <<'EOF'

## Base image packages

The runtime layer is `alpine`. Its packages, including the OS libraries used by the PostgreSQL clients, ship as installed by APK. Alpine publishes their licences and source packaging in its `aports` tree.

| Package | Version | License | Source |
|---|---|---|---|
EOF
  sort "$tmp.runtime" | awk -F'\t' '{ printf "| %s | %s | %s | %s |\n", $1, $2, $3, ($4 == "" ? "-" : $4) }'

  cat <<'EOF'

## PostgreSQL client components

`pg_dump`, `pg_restore`, `psql` and `libpq` come from the pinned `postgres:17-alpine` image. They are installed under `/usr/local` without replacing any runtime APK files.

| Component | Version | License | Source |
|---|---|---|---|
EOF
  awk -F'\t' '{ printf "| %s | %s | %s | %s |\n", $1, $2, $3, $4 }' "$pg_components"
} >>"$notices"

# Every SPDX identifier in both tables needs its text beside the list. The
# expressions here are the plain "A AND B" / "A OR B" forms, so splitting on
# whitespace and dropping the operators yields the identifiers.
ids="$( { cut -f3 "$tmp.runtime"; cut -f3 "$pg_components"; } \
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
