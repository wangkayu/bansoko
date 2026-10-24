#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
oapi_version="${OAPI_CODEGEN_VERSION:-2.4.1}"
ts_version="${OPENAPI_TYPESCRIPT_VERSION:-7.8.0}"

oapi_codegen_bin="$(command -v oapi-codegen || true)"
if [[ -z "$oapi_codegen_bin" ]]; then
  gobin="$(go env GOBIN 2>/dev/null || true)"
  gopath="$(go env GOPATH 2>/dev/null || true)"
  for candidate in "${gobin:+$gobin/oapi-codegen}" "${gopath%%:*}/bin/oapi-codegen"; do
    if [[ -n "$candidate" && -x "$candidate" ]]; then
      oapi_codegen_bin="$candidate"
      break
    fi
  done
fi
if [[ -z "$oapi_codegen_bin" ]]; then
  # CI containers never have this binary (`golang:1.26-bookworm` does not ship it and
  # the contract job installs only nodejs/ruby/curl), so this branch is the *normal*
  # path on CI: a module fetch plus a build, which is why it takes tens of seconds and
  # why a hiccup fails the lane. It failed on runs 37 and 50 with the same ~69-second
  # fingerprint — pnpm finishing, then a long silent gap, then "regeneration failed".
  # The fetch is retried here, and every attempt is announced, so a failure names the
  # network instead of the freshness check that merely noticed it.
  echo "oapi-codegen not found; installing v${oapi_version} via go install (this fetches modules)..." >&2
  installed=0
  for attempt in 1 2 3; do
    if go install "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v${oapi_version}"; then
      installed=1
      break
    fi
    echo "go install attempt ${attempt} failed; retrying" >&2
    sleep $((attempt * 5))
  done
  if [[ "$installed" != "1" ]]; then
    echo "failed to install oapi-codegen v${oapi_version} after 3 attempts (network or module proxy problem)" >&2
    exit 2
  fi
  oapi_codegen_bin="$(go env GOPATH)/bin/oapi-codegen"
fi
if ! "$oapi_codegen_bin" --version 2>&1 | rg -q "v${oapi_version}([[:space:]]|$)"; then
  echo "oapi-codegen version mismatch (expected v${oapi_version})" >&2
  exit 2
fi
if ! command -v pnpm >/dev/null 2>&1; then
  echo "pnpm is required for TypeScript contract generation" >&2
  exit 2
fi

ts_codegen_version="$(cd "$root/frontend" && pnpm exec openapi-typescript --version 2>/dev/null || true)"
ts_codegen_version="${ts_codegen_version#v}"
if [[ "$ts_codegen_version" != "$ts_version" ]]; then
  echo "openapi-typescript version mismatch (expected v${ts_version}; run pnpm install --frozen-lockfile in frontend)" >&2
  exit 2
fi

cd "$root"
# BANSOKO_CONTRACT_OUT_ROOT redirects both generated files under a scratch root so
# check-contract.sh can prove source/output sync without touching the tree.
out_root="${BANSOKO_CONTRACT_OUT_ROOT:-$root}"
go_out="$out_root/internal/transport/httpapi/generated/oapi.gen.go"
ts_out="$out_root/frontend/src/lib/api/generated.ts"
mkdir -p "$(dirname "$go_out")" "$(dirname "$ts_out")"
if [[ "$out_root" == "$root" ]]; then
  "$oapi_codegen_bin" -config api/codegen.yaml api/openapi.yaml
else
  # oapi-codegen ignores -o when -config carries output:, so override via a
  # synthesized config with the same settings and a redirected output path.
  override_config="$out_root/codegen.override.yaml"
  sed "s|^output:.*|output: $go_out|" api/codegen.yaml > "$override_config"
  "$oapi_codegen_bin" -config "$override_config" api/openapi.yaml
fi
(cd frontend && pnpm exec openapi-typescript ../api/openapi.yaml -o "$ts_out")

# The agent skill is generated from the one embedded guide
# (internal/agentguide/guide.md), the same text the server, the MCP resource
# and `bansoko guide` serve. It is tracked and checked for drift by
# check-contract.sh.
for entry in ".agents/skills/bansoko-agent:bansoko-agent"; do
  skill_dir="$out_root/${entry%%:*}"
  skill_name="${entry##*:}"
  mkdir -p "$skill_dir"
  go run ./cmd/bansoko guide --skill --name "$skill_name" > "$skill_dir/SKILL.md"
done
