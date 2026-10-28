#!/usr/bin/env bash
# Runs a gate as an unprivileged user when the CI job container starts as root.
#
# The product never runs as root (it drops to PUID/PGID), and root ignores
# directory permissions, so the real permission tests (EACCES, an unwritable
# cleanup namespace, a refused target parent) only mean something non-root.
# Those tests skip or fail under root, and the no-skip gates turn that red.
# Non-root callers (the VM, the host runner) run the command unchanged.
set -euo pipefail

[[ $# -gt 0 ]] || { echo 'usage: run-unprivileged.sh <command> [args...]' >&2; exit 2; }
if [[ "$(id -u)" != 0 ]]; then
  exec "$@"
fi

uid=1000
gid=1000
getent group "$gid" >/dev/null || groupadd -g "$gid" ci
getent passwd "$uid" >/dev/null || useradd -m -u "$uid" -g "$gid" -s /bin/bash ci
home="$(getent passwd "$uid" | cut -d: -f6)"
mkdir -p "$home"

# The checkout, the job's scratch dirs and the Go tree (module cache warmed by
# root) must belong to the user the gate runs as.
owned=("${GITHUB_WORKSPACE:-$PWD}" "$home" "$(go env GOPATH)")
for dir in "${TMPDIR:-}" "${GOTMPDIR:-}"; do
  if [[ -n "$dir" ]]; then
    mkdir -p "$dir"
    owned+=("$dir")
  fi
done
chown -R "$uid:$gid" "${owned[@]}"

exec env HOME="$home" USER=ci LOGNAME=ci \
  setpriv --reuid="$uid" --regid="$gid" --init-groups -- "$@"
