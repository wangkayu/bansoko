#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
helper="$root/scripts/verify/real-effect-frontend-build.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fake="$tmp/bin"
mkdir -p "$fake" "$tmp/frontend/node_modules" "$tmp/frontend/dist"
printf stale > "$tmp/frontend/dist/index.html"
cat > "$fake/pnpm" <<'EOF'
#!/usr/bin/env bash
set -eu
case "${FAKE_PNPM_MODE:-success}:$1" in
  install:install) exit 19 ;;
  build19:build) exit 19 ;;
  missing:build) exit 0 ;;
  success:build)
    out=""
    for arg in "$@"; do
      # Real pnpm 11 forwards "--" literally and vite then drops --outDir.
      [[ "$arg" != -- ]] || { echo "fake pnpm: literal -- would hide --outDir from vite" >&2; exit 91; }
    done
    while (($#)); do
      if [[ "$1" == --outDir ]]; then shift; out="$1"; fi
      shift || true
    done
    mkdir -p "$out"
    printf '<html></html>\n' > "$out/index.html"
    ;;
  *) echo "unexpected fake pnpm invocation: $*" >&2; exit 90 ;;
esac
EOF
chmod +x "$fake/pnpm"
export PATH="$fake:$PATH"
run_fail() {
  local mode="$1" expected="$2" name="$3"
  local out="$tmp/out-$name" status=0
  FAKE_PNPM_MODE="$mode" "$helper" "$tmp/frontend" "$out" >/dev/null 2>&1 || status=$?
  if [[ "$status" -ne "$expected" ]]; then
    echo "FAIL: $name returned $status, expected $expected" >&2; exit 1
  fi
  [[ ! -e "$out" ]] || { echo "FAIL: $name left output behind" >&2; exit 1; }
}
# Remove node_modules to exercise explicit install failure.
rm -rf "$tmp/frontend/node_modules"
run_fail install 19 install-failure
mkdir -p "$tmp/frontend/node_modules"
# An old dist is deliberately present; build exit 19 must still fail, and it is not served.
FAKE_PNPM_MODE=build19 run_fail build19 19 build-19-with-old-dist
run_fail missing 20 missing-artifact
out="$tmp/out-success"
FAKE_PNPM_MODE=success "$helper" "$tmp/frontend" "$out"
[[ -s "$out/index.html" ]]
[[ "$(cat "$tmp/frontend/dist/index.html")" == stale ]]
# Output paths cannot be reused, even when a stale successful tree is present.
if FAKE_PNPM_MODE=success "$helper" "$tmp/frontend" "$out" >/dev/null 2>&1; then
  echo 'FAIL: reused output directory was accepted' >&2; exit 1
fi
echo 'real-effect frontend build helper tests passed'
