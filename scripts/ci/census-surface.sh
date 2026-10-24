#!/usr/bin/env bash
# Surface census: every OpenAPI operation with its callers (frontend client,
# MCP tool, bansoko command, spec mention when the dev environment is present). Evidence on stdout, not a
# checked-in ledger. With --check it exits non-zero when an operation has no
# frontend, MCP or CLI caller and no `x-surface-keep: "<reason>"`, when an MCP
# allowlist entry is invalid, or when a keep reason is stale or empty.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

mode=report
case "${1:-}" in
  --check) mode=check ;;
  "") ;;
  *) echo "usage: census-surface.sh [--check]" >&2; exit 2 ;;
esac

if ! command -v ruby >/dev/null 2>&1; then
  echo "census-surface: ruby is required to parse api/openapi.yaml" >&2
  exit 1
fi

CENSUS_MODE="$mode" ruby -ryaml -e '
mode = ENV.fetch("CENSUS_MODE")
doc = YAML.load_file("api/openapi.yaml")

ops = []
doc.fetch("paths").each do |path, item|
  item.each do |method, op|
    next unless op.is_a?(Hash) && op["operationId"]
    ops << { id: op["operationId"], method: method.upcase, path: path, keep: op["x-surface-keep"], fe: [], mcp: [], cli: [], spec: [] }
  end
end

def path_regex(path)
  parts = path.split("/", -1).map { |seg| seg =~ /\A\{.+\}\z/ ? "[^/]+" : Regexp.escape(seg) }
  Regexp.new("\\A" + parts.join("/") + "(\\{\\})?(\\?.*)?\\z")
end
ops.each { |o| o[:re] = path_regex(o[:path]); o[:params] = o[:path].scan(/\{[^}]+\}/).size }

# Read one string literal starting at src[i] (a quote), then any `+ operand`
# continuations (Go/TS concatenation). Expressions become "{}". Returns shape.
def read_shape(src, i)
  shape = +""
  loop do
    q = src[i]
    i += 1
    while i < src.size && src[i] != q
      if q == "`" && src[i, 2] == "${"
        depth = 1
        i += 2
        while i < src.size && depth > 0
          depth += 1 if src[i] == "{"
          depth -= 1 if src[i] == "}"
          i += 1
        end
        shape << "{}"
        next
      end
      if src[i] == "\\" then i += 2; next end
      shape << src[i]
      i += 1
    end
    i += 1
    j = i
    j += 1 while src[j] =~ /\s/
    break unless src[j] == "+"
    j += 1
    j += 1 while src[j] =~ /\s/
    if src[j] =~ /["`]/
      i = j
      next
    end
    depth = 0
    while j < src.size
      c = src[j]
      depth += 1 if c == "(" || c == "[" || c == "{"
      if c == ")" || c == "]" || c == "}"
        break if depth == 0
        depth -= 1
      end
      break if depth == 0 && (c == "," || c == "+" || c == "\n")
      j += 1
    end
    shape << "{}"
    k = j
    k += 1 while src[k] =~ /\s/
    break unless src[k] == "+"
    k += 1
    k += 1 while src[k] =~ /\s/
    break unless src[k] =~ /["`]/
    i = k
  end
  shape
end

def best_match(ops, method, shape)
  hits = ops.select { |o| o[:method] == method && o[:re].match?(shape) }
  hits.min_by { |o| o[:params] }
end

# --- frontend: literals that look like API paths, method from the call window.
fe_files = Dir["frontend/src/**/*.{ts,tsx}"].reject { |f| f =~ /\.test\.|generated\.ts\z|__tests__|\.spec\./ }
fe_sources = fe_files.to_h { |f| [f, File.read(f)] }
client_path = "frontend/src/lib/api/client.ts"
fe_files.each do |f|
  src = fe_sources[f]
  src.scan(/["`\x27]\/(?:api\/v2\/)?[A-Za-z]/) do
    start = $~.begin(0)
    shape = read_shape(src, start).sub(/\A\/api\/v1/, "")
    window = src[start, 500].to_s
    m = window[/method:\s*"(\w+)"/, 1]
    method = (m || "GET").upcase
    hit = best_match(ops, method, shape)
    next unless hit
    if f == client_path
      # A typed client function only counts when some other frontend source
      # (not a test) references it; an unused client export is not a caller.
      fn = src[0, start].scan(/export\s+(?:async\s+)?function\s+(\w+)/).flatten.last
      users = fn ? fe_sources.select { |g, text| g != client_path && text.match?(/\b#{Regexp.escape(fn)}\b/) }.keys : []
      users.each { |g| hit[:fe] << g unless hit[:fe].include?(g) }
    else
      hit[:fe] << f unless hit[:fe].include?(f)
    end
  end
end

# --- CLI: literals in cmd/bansoko, methods from nearby .do(...) calls.
Dir["cmd/bansoko/*.go"].reject { |f| f.end_with?("_test.go") }.each do |f|
  src = File.read(f)
  src.scan(/"\/[A-Za-z]/) do
    start = $~.begin(0)
    shape = read_shape(src, start)
    window = src[start, 4000].to_s
    methods = window.scan(/\.do\([^,]+,\s*"(\w+)"/).flatten.uniq
    methods.each do |m|
      hit = best_match(ops, m, shape)
      hit[:cli] << f if hit && !hit[:cli].include?(f)
    end
  end
end

# --- MCP: the allowlist is the OpenAPI file itself. An operation carrying
# `x-mcp: {tool: ...}` is a tool; a composite under `x-mcp-composites` lists the
# operationIds it composes. There is no source scanning to keep in sync.
by_id = ops.to_h { |o| [o[:id], o] }
tools = {}
doc.fetch("paths").each_value do |item|
  item.each_value do |op|
    next unless op.is_a?(Hash) && op["operationId"] && op["x-mcp"].is_a?(Hash)
    tool = op["x-mcp"]["tool"].to_s
    tools[tool] = op["operationId"]
    by_id.fetch(op["operationId"])[:mcp] << tool
  end
end
unmapped_tools = []
(doc["x-mcp-composites"] || []).each do |composite|
  tool = composite["tool"].to_s
  tools[tool] = Array(composite["operations"]).join("+")
  Array(composite["operations"]).each do |id|
    if (target = by_id[id])
      target[:mcp] << tool
    else
      unmapped_tools << "#{tool} (composes unknown operationId #{id})"
    end
  end
end
dupes = tools.keys.group_by { |t| t }.select { |_, v| v.size > 1 }.keys
unmapped_tools.concat(dupes.map { |t| "#{t} (declared twice)" })

# --- spec: operationId mentioned in .trellis/spec or the agent skill (informational, not a caller).
spec = (Dir[".trellis/spec/**/*.md"] + [".agents/skills/bansoko-agent/SKILL.md"]).select { |f| File.file?(f) }.map { |f| [f, File.read(f)] }
ops.each do |o|
  spec.each { |f, text| o[:spec] << f if text =~ /\b#{Regexp.escape(o[:id])}\b/ }
end

cli_cmds = File.read("cmd/bansoko/main.go").scan(/^\tcase "([a-z-]+)":/).flatten - %w[help]

callerless = ops.select { |o| o[:fe].empty? && o[:mcp].empty? && o[:cli].empty? }
orphans = callerless.select { |o| o[:keep].to_s.strip.empty? }
kept = callerless - orphans
stale_keep = ops.select { |o| !o[:keep].nil? && !callerless.include?(o) }
empty_keep = ops.select { |o| o.key?(:keep) && !o[:keep].nil? && o[:keep].to_s.strip.empty? }

puts "Surface census", "=============="
puts "operations=#{ops.size}"
puts "frontend_callers=#{ops.count { |o| !o[:fe].empty? }}"
puts "mcp_tools=#{tools.size} (#{(doc["x-mcp-composites"] || []).size} composite) mapped_operations=#{ops.count { |o| !o[:mcp].empty? }}"
puts "cli_commands=#{cli_cmds.size} (#{cli_cmds.join(" ")}) operations=#{ops.count { |o| !o[:cli].empty? }}"
puts "no_frontend_mcp_or_cli_caller=#{callerless.size}"
puts "  kept_with_x-surface-keep=#{kept.size}"
puts "  orphans_without_keep=#{orphans.size}"
puts "stale_x-surface-keep=#{stale_keep.size}"
puts
puts "operation_id\tmethod\tpath\tfrontend\tmcp\tcli\tspec\tkeep"
ops.sort_by { |o| o[:id] }.each do |o|
  puts [o[:id], o[:method], o[:path],
        o[:fe].empty? ? "-" : "fe", o[:mcp].empty? ? "-" : o[:mcp].join(","),
        o[:cli].empty? ? "-" : "cli", o[:spec].empty? ? "-" : "spec",
        o[:keep] || "-"].join("\t")
end
unless unmapped_tools.empty?
  puts
  puts "mcp_tools_without_operation:"
  unmapped_tools.each { |t| puts "  #{t}" }
end

if mode == "check"
  problems = []
  orphans.each { |o| problems << "no frontend/MCP/CLI caller and no x-surface-keep: #{o[:id]} (#{o[:method]} #{o[:path]})" }
  unmapped_tools.each { |t| problems << "MCP allowlist entry is invalid: #{t}" }
  stale_keep.each { |o| problems << "stale x-surface-keep on an operation that has a caller: #{o[:id]}" }
  empty_keep.each { |o| problems << "empty x-surface-keep reason: #{o[:id]}" }
  unless problems.empty?
    warn problems
    exit 1
  end
end
'
