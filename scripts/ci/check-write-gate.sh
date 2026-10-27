#!/usr/bin/env bash
# Write-entry inventory gate (P0.6).
#
# The maintenance middleware rejects every non-GET/HEAD product write while
# the gate is active; side-effect-free POSTs (previews, prechecks, tests,
# parses, scans) stay available via an explicit allowlist in
# internal/transport/httpapi/middleware.go.
#
# This gate proves:
#   1. every allowlist entry exists in OpenAPI (no dead entries);
#   2. every declared side-effect-free POST is allowlisted (maintenance would
#      otherwise wrongly reject a read-only op);
#   3. every other POST/PUT/PATCH/DELETE path is a product write covered by
#      the fail-closed default.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
openapi="$root/api/openapi.yaml"
middleware="$root/internal/transport/httpapi/middleware.go"

# Extract allowlisted POST paths: backtick-quoted regexes, strip ^ and $
# anchors, normalize [^/]+ parameter segments to {id}.
allowlisted="$(
  ruby -e 'src = File.read(ARGV[0]); src.scan(/`\^(\/api\/v1\/[^`]+)\$`/) { |m| puts m[0].gsub("[^/]+", "{id}") }' "$middleware" || true
)"

# Extract every POST path from OpenAPI (relative paths).
openapi_posts="$(
  ruby -ryaml -e 'spec = YAML.load_file(ARGV[0]); (spec["paths"] || {}).each_key { |p| puts p if (spec["paths"][p] || {}).key?("post") }' "$openapi"
)"

fail=0

# 1. Every allowlisted path must exist in OpenAPI. Convert the allowlist
#    template (strip /api/v1 prefix, {id} -> [^/]+) into a regex and match
#    against the OpenAPI relative paths.
for path in $allowlisted; do
  rel="${path#/api/v1}"
  regex="$(printf '%s' "$rel" | sed 's|{id}|[^/]+|g')"
  matched=0
  while IFS= read -r candidate; do
    if [[ "$candidate" =~ ^$regex$ ]]; then
      matched=1
      break
    fi
  done <<<"$openapi_posts"
  if [ "$matched" -eq 0 ]; then
    echo "allowlisted read-only POST not in OpenAPI: $path" >&2
    fail=1
  fi
done

# 2. Declared side-effect-free POSTs must be allowlisted.
side_effect_free="
/api/v1/anime/rss-list-view
/api/v1/anime/subgroups-selection-preview
/api/v1/parse/release-title
/api/v1/parse/torrent-files
/api/v1/media-recognition/previews
/api/v1/template-previews/notification
/api/v1/template-previews/naming
/api/v1/file-manager/entry/match/preview
/config/connections/{connection_id}/test
/config/network-routes/{route_id}/test
"
for path in $side_effect_free; do
  rel="${path#/api/v1}"
  regex="$(printf '%s' "$rel" | sed 's|{[^}]*}|[^/]+|g')"
  found=0
  while IFS= read -r entry; do
    entry_rel="${entry#/api/v1}"
    if [[ "$entry_rel" =~ ^${regex//\//\\/}$ ]]; then
      found=1
      break
    fi
  done <<<"$allowlisted"
  if [ "$found" -eq 0 ]; then
    echo "side-effect-free POST missing from middleware allowlist: $path" >&2
    fail=1
  fi
done

# 3. MCP write tools are gated by the same HTTP chain: a tool call is replayed
#    as an in-process request through the maintenance middleware. The OpenAPI
#    allowlist (`x-mcp.write`) must agree with the middleware classification:
#    a non-GET tool whose path is allowlisted as side-effect-free is
#    `write: false`; every other non-GET tool is `write: true`. A mismatch would
#    advertise a read-only hint on a gated write (or the reverse).
if ! ruby -ryaml -e '
spec = YAML.load_file(ARGV[0])
allow = STDIN.read.split("\n").reject(&:empty?)
bad = []
writes = 0
(spec["paths"] || {}).each do |path, item|
  item.each do |method, op|
    next unless op.is_a?(Hash) && op["x-mcp"].is_a?(Hash)
    writes += 1 if op["x-mcp"]["write"] == true
    next if method == "get"
    full = "/api/v1" + path.gsub(/\{[^}]*\}/, "{id}")
    read_only_post = method == "post" && allow.include?(full)
    bad << "#{op["x-mcp"]["tool"]} (#{method.upcase} #{path}): x-mcp.write=#{op["x-mcp"]["write"]} but the maintenance middleware treats it as #{read_only_post ? "side-effect free" : "a product write"}" if op["x-mcp"]["write"] == read_only_post
  end
end
bad << "too few MCP write tools in the OpenAPI allowlist (found #{writes}, want >=14)" if writes < 14
unless bad.empty?
  warn bad
  exit 1
end
' "$openapi" <<<"$allowlisted"; then
  fail=1
fi

# 4. CLI write commands are covered by the server gate (all CLI commands go
#    through the HTTP API). Assert the CLI command table still exists.
if ! grep -q 'case "candidate"' "$root/cmd/bansoko/main.go"; then
  echo "CLI candidate command table missing" >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "write-entry inventory gate FAILED" >&2
  exit 1
fi
echo "Write-entry inventory gate passed"
