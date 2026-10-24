#!/usr/bin/env sh
# Lists the components the PostgreSQL stage contributes to the runtime image.
#
# The runtime layer is Alpine 3.20 and this stage is Alpine 3.24.1. The files
# copied here keep their original paths, so they replace same-named 3.20
# packages at runtime — and the 3.20 package database in the runtime layer
# cannot describe them at the version actually shipped. Both the files and the
# apk records that own them are present here, so this is where the list is
# written.
#
# Usage: generate-pg17-components.sh <staged-paths.txt> <components.tsv>
#   staged-paths.txt  absolute paths placed into the runtime layer, one per line
#   components.tsv    output: name<TAB>version<TAB>licence<TAB>source URL
set -eu
paths="${1:?usage: generate-pg17-components.sh <staged-paths.txt> <components.tsv>}"
out="${2:?usage: generate-pg17-components.sh <staged-paths.txt> <components.tsv>}"
db=/lib/apk/db/installed
[ -f "$db" ] || { echo "generate-pg17-components: $db is missing" >&2; exit 1; }
[ -s "$paths" ] || { echo "generate-pg17-components: $paths is empty" >&2; exit 1; }

# PostgreSQL ships these itself, outside Alpine's package manager: the three
# client tools and the libpq they link. One component under the PostgreSQL
# licence, named by the version this image contains. Any *other* unowned path
# is a file nobody accounts for and aborts the build.
unowned_expected=' /usr/local/bin/pg_dump /usr/local/bin/pg_restore /usr/local/bin/psql /usr/local/lib/libpq.so.5 '

: >"$out"
bad=0
while IFS= read -r path; do
  [ -n "$path" ] || continue
  owner="$(apk info -W "$path" 2>/dev/null | sed -n 's/.* is owned by \(.*\)$/\1/p')" || owner=""
  if [ -z "$owner" ]; then
    case "$unowned_expected" in
      *" $path "*) ;;
      *) echo "generate-pg17-components: $path has no owning package and is not a PostgreSQL artefact" >&2; bad=1 ;;
    esac
    continue
  fi
  # apk info -W prints "<name>-<version>", the same join the record's P and V
  # fields make. A package whose record declares no licence is a component we
  # cannot name, so it aborts instead of entering the list blank.
  row="$(awk -F: -v want="$owner" '
    function flush() {
      if (p != "" && p "-" v == want) printf "%s\t%s\t%s\t%s\n", p, v, l, u
      p = ""; v = ""; l = ""; u = ""
    }
    /^P:/ { flush(); p = substr($0, 3) }
    /^V:/ { v = substr($0, 3) }
    /^L:/ { l = substr($0, 3) }
    /^U:/ { u = substr($0, 3) }
    END { flush() }' "$db")"
  if [ -z "$row" ]; then
    echo "generate-pg17-components: $path is owned by $owner, which $db does not describe" >&2
    bad=1
    continue
  fi
  printf '%s\n' "$row" >>"$out"
done <"$paths"
[ "$bad" = 0 ] || exit 1
[ -s "$out" ] || { echo "generate-pg17-components: no staged path belongs to a package" >&2; exit 1; }

version="$(pg_config --version | awk '{print $2}')"
case "$version" in
  ''|*[!0-9.]*) echo "generate-pg17-components: pg_config reports no version" >&2; exit 1 ;;
esac
printf 'postgresql\t%s\tPostgreSQL\thttps://www.postgresql.org/about/licence/\n' "$version" >>"$out"
sort -u "$out" -o "$out"

awk -F'\t' 'NF != 4 || $1 == "" || $2 == "" || $3 == "" {
              print "generate-pg17-components: component with no version or licence: " $0 > "/dev/stderr"; bad = 1
            }
            END { exit bad ? 1 : 0 }' "$out"
