#!/usr/bin/env bash
# Run `go test` and fail if any test skipped.
#
# Usage: go-test-no-skips.sh <label> [go test args...]
#
# A skipped test and a passing test are indistinguishable in `go test` exit
# status, so a test that quietly stops running inside an acceptance set is
# invisible. This wrapper is for gates that assert their preconditions are met:
# the PostgreSQL-backed ones already fail loudly on an unset or unreachable
# BANSOKO_TEST_DATABASE_URL, so what it catches is a test disabled for any other
# reason -- a missing secondary env var, a `t.Skip("flaky")` -- silently
# shrinking the gate.
#
# Explicit opt-in skips (public-network provider tests, live qB tests) are
# declared via BANSOKO_TEST_SKIP_ALLOW="TestA|TestB": they are not silent — the
# gate runner lists them in the lane script — and they are never part of the
# required acceptance set.
set -euo pipefail

label="${1:?usage: go-test-no-skips.sh <label> [go test args...]}"
shift

# Allowlist of test names whose skip is an explicit opt-in declaration.
allow_regex="${BANSOKO_TEST_SKIP_ALLOW:-}"

# This wrapper's own scratch lives in a dedicated subdirectory of TMPDIR so it
# stays apart from the test fixture trees.
events_dir="$(mktemp -d)"
events="$events_dir/events.jsonl"
trap 'rm -rf "$events_dir"' EXIT

status=0
# The stream is parsed as JSON rather than grepped: a package with no test files
# also reports Action "skip", and only an event carrying a "Test" field means a
# t.Skip fired inside a test. Tests excluded by -run emit no event at all.
go test -json "$@" | tee "$events" | ruby -rjson -e '
  STDOUT.sync = true
  STDIN.each_line do |line|
    event = (JSON.parse(line) rescue nil) or next
    print event["Output"] if event["Output"]
  end
' || status=$?

ruby -rjson -e '
  # The skip reason lands in an output event before the skip event, so keep the
  # last non-marker output line per test to report why it stopped.
  reasons = {}
  skipped = []
  allowed = (ENV["BANSOKO_TEST_SKIP_ALLOW"] || "").split("|").reject(&:empty?)
  File.readlines(ARGV[0]).each do |line|
    event = (JSON.parse(line) rescue nil) or next
    test = event["Test"] or next
    key = "#{event["Package"]} #{test}"
    case event["Action"]
    when "output"
      text = event["Output"].to_s.strip
      reasons[key] = text unless text.empty? || text.start_with?("=== ", "--- ")
    when "skip"
      skipped << key
    end
  end
  disallowed = skipped.reject { |key| allowed.any? { |re| key =~ /#{re}/ } }
  unless disallowed.empty?
    warn "#{ARGV[1]}: #{disallowed.length} test(s) skipped; every test in this gate must run"
    disallowed.each { |key| warn "  #{key}: #{reasons.fetch(key, "(no reason reported)")}" }
    exit 1
  end
' "$events" "$label"

exit "$status"
