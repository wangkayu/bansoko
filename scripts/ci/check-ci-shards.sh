#!/usr/bin/env bash
# Fail-closed consistency check for the PostgreSQL gate's shard table.
#
# `scripts/ci/postgres-packages.txt` is the canonical acceptance set;
# `scripts/ci/postgres-shards.txt` says which container job runs each package.
# Splitting the lane is only safe while the two describe exactly the same set:
# a package missing from the shard table would silently stop running, and a
# package assigned twice would run twice while the lane still looked green.
#
# The workflow side is checked too: the shard jobs must ask for exactly the
# shard numbers the table uses, so renaming a job cannot drop a shard.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

packages="${BANSOKO_POSTGRES_PACKAGES_FILE:-$root/scripts/ci/postgres-packages.txt}"
shards="${BANSOKO_POSTGRES_SHARDS_FILE:-$root/scripts/ci/postgres-shards.txt}"
workflow="${CI_WORKFLOW:-$root/.github/workflows/ci.yml}"

for file in "$packages" "$shards" "$workflow"; do
  [[ -f "$file" ]] || { echo "check-ci-shards: missing $file" >&2; exit 1; }
done

ruby - "$packages" "$shards" "$workflow" <<'RUBY'
require 'yaml'

packages_path, shards_path, workflow_path = ARGV

packages = File.readlines(packages_path).map(&:strip).reject { |l| l.empty? || l.start_with?('#') }
abort 'check-ci-shards: postgres-packages.txt is empty' if packages.empty?

shard_rows = []
File.readlines(shards_path).each_with_index do |line, i|
  text = line.strip
  next if text.empty? || text.start_with?('#')
  fields = line.split
  abort "check-ci-shards: postgres-shards.txt:#{i + 1} is not `<shard>\\t<package>`" unless fields.length == 2
  shard, package = fields
  abort "check-ci-shards: postgres-shards.txt:#{i + 1} shard #{shard.inspect} is not a positive integer" unless shard =~ /\A[1-9][0-9]*\z/
  shard_rows << [shard.to_i, package]
end
abort 'check-ci-shards: postgres-shards.txt assigns no package' if shard_rows.empty?

abort 'check-ci-shards: postgres-packages.txt has duplicate entries' unless packages.uniq.length == packages.length

assigned = shard_rows.map(&:last)
duplicated = assigned.group_by { |p| p }.select { |_, v| v.length > 1 }.keys.sort
abort "check-ci-shards: assigned more than once: #{duplicated.join(', ')}" unless duplicated.empty?

missing = packages - assigned
abort "check-ci-shards: in postgres-packages.txt but in no shard: #{missing.join(', ')}" unless missing.empty?

unknown = assigned - packages
abort "check-ci-shards: assigned but not in postgres-packages.txt: #{unknown.join(', ')}" unless unknown.empty?

numbers = shard_rows.map(&:first).uniq.sort
expected = (1..numbers.max).to_a
abort "check-ci-shards: shard numbers are #{numbers.inspect}, want #{expected.inspect} with no gaps" unless numbers == expected

workflow = YAML.load_file(workflow_path)
shard_jobs = workflow.fetch('jobs').select { |id, _| id.start_with?('postgres-integration-shard-') }
abort 'check-ci-shards: the workflow declares no postgres-integration-shard-* job' if shard_jobs.empty?

workflow_shards = shard_jobs.map do |id, job|
  number = id.delete_prefix('postgres-integration-shard-')
  abort "check-ci-shards: job #{id} is not postgres-integration-shard-<n>" unless number =~ /\A[1-9][0-9]*\z/
  step = job.fetch('steps').find { |s| s.fetch('env', {})['BANSOKO_TEST_PG_SHARD'] }
  abort "check-ci-shards: job #{id} does not set BANSOKO_TEST_PG_SHARD" unless step
  value = step.dig('env', 'BANSOKO_TEST_PG_SHARD')
  abort "check-ci-shards: job #{id} sets BANSOKO_TEST_PG_SHARD=#{value.inspect}, want #{number}/#{numbers.max}" unless value == "#{number}/#{numbers.max}"
  number.to_i
end.sort

abort "check-ci-shards: the workflow runs shards #{workflow_shards.inspect}, the table uses #{numbers.inspect}" unless workflow_shards == numbers

release = workflow.fetch('jobs').fetch('test-release')
needs = Array(release['needs'])
uncovered = shard_jobs.keys - needs
abort "check-ci-shards: release does not depend on: #{uncovered.join(', ')}" unless uncovered.empty?

puts "postgres shard table consistent: #{packages.length} packages in #{numbers.length} shards"
RUBY
