#!/bin/sh
# Small side-effect-free release preflight helpers, shared with local tests.
ci_release_read_digest() {
  digest_file=$1
  [ -f "$digest_file" ] && [ -s "$digest_file" ] || {
    echo "ERROR: image digest artifact is missing or empty" >&2
    return 1
  }
  byte_count=$(wc -c <"$digest_file" | tr -d '[:space:]')
  case "$byte_count" in 71|72) ;; *) echo "ERROR: image digest artifact has invalid byte length" >&2; return 1 ;; esac
  # The byte stream was bounded to 71/72 bytes; splitting these decimal bytes is intentional.
  # shellcheck disable=SC2046
  set -- $(od -An -v -tu1 "$digest_file")
  [ "$#" -eq "$byte_count" ] || { echo "ERROR: image digest artifact contains invalid bytes" >&2; return 1; }
  expected_prefix='115 104 97 50 53 54 58'
  for expected in $expected_prefix; do
    [ "$1" = "$expected" ] || { echo "ERROR: image digest artifact has invalid format" >&2; return 1; }
    shift
  done
  digest_value='sha256:'
  hex_count=64
  while [ "$hex_count" -gt 0 ]; do
    byte=$1
    case "$byte" in
      ''|*[!0-9]*) echo "ERROR: image digest artifact contains invalid bytes" >&2; return 1 ;;
    esac
    { [ "$byte" -ge 48 ] && [ "$byte" -le 57 ]; } || { [ "$byte" -ge 97 ] && [ "$byte" -le 102 ]; } || { echo "ERROR: image digest artifact is not lowercase hexadecimal" >&2; return 1; }
    octal=$(printf '%03o' "$byte")
    char=$(printf "\\${octal}")
    digest_value="${digest_value}${char}"
    shift
    hex_count=$((hex_count - 1))
  done
  if [ "$byte_count" -eq 72 ]; then
    [ "$1" = 10 ] || { echo "ERROR: image digest artifact has invalid trailing byte" >&2; return 1; }
  fi
  printf '%s\n' "$digest_value"
}

