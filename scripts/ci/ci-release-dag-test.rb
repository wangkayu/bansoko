#!/usr/bin/env ruby
# Validates declared trust and invocation edges; platform behavior is verified separately.
require 'yaml'
workflow = YAML.load_file(ENV.fetch('CI_WORKFLOW', '.github/workflows/ci.yml'))
jobs = workflow.fetch('jobs')
required = %w[contract-boundary go-unit-static frontend-quality postgres-integration-shard-1 postgres-integration-shard-2 postgres-integration-shard-3 qb-real-chain fixture-browser]
abort 'missing required gate' unless (required - jobs.keys).empty?
abort 'missing freshness' unless jobs.key?('persistence-freshness')
abort 'contract lacks freshness dependency' unless Array(jobs['contract-boundary']['needs']).include?('persistence-freshness')
visiting = {}; visited = {}
visit = lambda do |id|
  abort 'unknown dependency' unless jobs.key?(id)
  abort 'DAG cycle' if visiting[id]
  return if visited[id]
  visiting[id] = true
  Array(jobs[id]['needs']).each { |d| visit.call(d) }
  visiting.delete(id); visited[id] = true
end
jobs.each_key { |id| visit.call(id) }
freshness = jobs['persistence-freshness']['steps'].find { |s| s['name'] == 'run every persistence freshness check' }
abort 'freshness command mismatch' unless freshness && freshness['run'] == 'bash scripts/ci/ci-persistence-freshness.sh'
required.each do |id|
  job = jobs[id]
  # The host-runner gates run for both publishing events: a tag publishes only
  # after every gate ran again on the tagged commit in the same run.
  expected_condition = %w[qb-real-chain fixture-browser].include?(id) ? "${{ github.event_name == 'push' && (github.ref == 'refs/heads/main' || startsWith(github.ref, 'refs/tags/v')) }}" : nil
  abort 'required job weakened' unless job['if'] == expected_condition && !job['continue-on-error']
  job['steps'].each { |s| abort 'required step weakened' if s['if'] || s['continue-on-error'] }
end

# The PostgreSQL acceptance set is split across shard jobs (see
# scripts/ci/postgres-shards.txt). A shard that is present in the release
# dependencies but not actually runs its subset, or a fourth shard nobody
# depends on, would shrink or duplicate the gate.
shard_jobs = jobs.keys.select { |id| id.start_with?('postgres-integration-shard-') }.sort
shard_required = required.grep(/\Apostgres-integration-shard-/).sort
abort 'required shard set drifted' unless shard_jobs == shard_required
shard_jobs.each do |id|
  shard = jobs[id]['steps'].select { |s| s.fetch('env', {})['BANSOKO_TEST_PG_SHARD'] }
  abort "#{id}: a shard job runs exactly one gated step" unless shard.length == 1
  abort "#{id}: shard job runs outside the shard selector" unless shard.first['run'].include?('bash scripts/ci/test-integration.sh')
  # The job name and the selector must agree: a job called "shard 2" that runs
  # 1/3 would leave one subset unrun and another run twice, and the release
  # result check would still see a green job.
  index = id.delete_prefix('postgres-integration-shard-')
  abort "#{id}: selector does not name this shard" unless shard.first['env']['BANSOKO_TEST_PG_SHARD'].start_with?(index + '/')
end

MAIN_ONLY = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}"
TAG_ONLY = "${{ github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v') }}"
publishers = %w[docker-hub release docker-hub-release]
abort 'publisher set drifted' unless (jobs.keys - required - %w[persistence-freshness vulnerability-scan-go vulnerability-scan-frontend]).sort == publishers.sort
abort 'retired main publication remains' if jobs.key?('test-release')

release = jobs.fetch('release')
abort 'release condition unsafe' unless release['if'] == TAG_ONLY
abort 'release dependencies incomplete' unless Array(release['needs']).sort == required.sort
abort 'release tolerates failure' if release['continue-on-error']
abort 'global secret' if workflow.fetch('env', {}).to_s.include?('secrets.')
jobs.each do |id, job|
  abort 'job-level secret' if job.fetch('env', {}).to_s.include?('secrets.')
  steps = job.fetch('steps')
  checkout = steps.index { |s| s['uses'].to_s.start_with?('actions/checkout@') }
  sha = steps.index { |s| s['name'] == 'verify event SHA' && s['run'] == 'test "$(git rev-parse HEAD)" = "$GITHUB_SHA"' }
  abort "#{id}: missing SHA verification" unless checkout && sha && sha > checkout
  steps.each do |s|
    if s['uses'].to_s.start_with?('actions/checkout@')
      abort 'checkout source drift' unless s.fetch('with', {})['ref'] == '${{ github.sha }}'
    end
    abort 'secret in required gate' if !publishers.include?(id) && s.to_s.include?('secrets.')
  end
end
# Each formal-release step holds only the credentials it uses: the registry
# push token, the signing key, the workflow token for the channel branch, and
# the workflow token again (as GH_TOKEN) for the GitHub Release. A widened step
# is drift, not convenience.
expected = {
  'release notes and version preflight' => ['preflight', %w[BANSOKO_RELEASE_GITHUB_TOKEN]],
  'build image' => ['image', %w[DOCKERHUB_TOKEN]],
  'publish immutable package' => ['publish', %w[BANSOKO_RELEASE_SIGNING_KEY BANSOKO_RELEASE_GITHUB_TOKEN DOCKERHUB_TOKEN]],
  'advance signed channel' => ['channel', %w[BANSOKO_RELEASE_SIGNING_KEY BANSOKO_RELEASE_GITHUB_TOKEN]],
  'create GitHub Release' => ['github-release', %w[GH_TOKEN]]
}
steps = release['steps']
trust = steps.index { |s| s['run'] == 'python3 scripts/ci/ci-release-trust.py' }
abort 'missing platform trust check' unless trust
indices = []
expected.each do |name, (mode, secret_keys)|
  index = steps.index { |s| s['name'] == name }
  abort 'effect step absent' unless index
  s = steps[index]
  abort 'effect invocation drift' unless s['run'] == "python3 scripts/ci/ci-release.py #{mode}"
  abort 'effect override' if s['if'] || s['continue-on-error']
  env = s.fetch('env')
  abort 'needs result binding missing' unless env['BANSOKO_REQUIRED_RESULTS'] == '${{ toJSON(needs) }}'
  actual = env.select { |_k, v| v.to_s.include?('secrets.') }.keys
  abort 'secret scope mismatch' unless actual.sort == secret_keys.sort
  indices << index
end
# Order is the safety argument: trust, then the version/notes preflight before
# any effect, the channel only after the package, the GitHub Release last.
abort 'effects not ordered after trust' unless indices == indices.sort && indices.first > trust
abort 'release job lacks contents write' unless release.fetch('permissions', {}) == { 'contents' => 'write', 'actions' => 'read' }
abort 'workflow-level permissions widened' unless workflow.fetch('permissions', {}) == { 'contents' => 'read', 'actions' => 'read' }
jobs.each do |id, job|
  next if id == 'release'
  abort "#{id}: unexpected job permissions" if job.key?('permissions')
end
abort 'unexpected release command' unless steps.map { |s| s['run'] }.compact.sort == [
  'test "$(git rev-parse HEAD)" = "$GITHUB_SHA"', 'python3 scripts/ci/ci-release-trust.py',
  'python3 scripts/ci/ci-release.py preflight', 'python3 scripts/ci/ci-release.py image', 'python3 scripts/ci/ci-release.py publish',
  'python3 scripts/ci/ci-release.py channel', 'python3 scripts/ci/ci-release.py github-release'
].sort

# Docker Hub: main publishes only the test image; a tag publishes `X.Y.Z` and
# `:latest` only after the signed release of the same run succeeded.
{ 'docker-hub' => [MAIN_ONLY, required, 'test'], 'docker-hub-release' => [TAG_ONLY, required + ['release'], 'release'] }.each do |id, (condition, needs, mode)|
  hub = jobs.fetch(id)
  abort "#{id}: condition unsafe" unless hub['if'] == condition
  abort "#{id}: dependencies incomplete" unless Array(hub['needs']).sort == needs.sort
  abort "#{id}: tolerates failure" if hub['continue-on-error']
  hub_steps = hub['steps']
  abort "#{id}: step override" if hub_steps.any? { |s| s['if'] || s['continue-on-error'] }
  abort "#{id}: unexpected command" unless hub_steps.map { |s| s['run'] }.compact == [
    'test "$(git rev-parse HEAD)" = "$GITHUB_SHA"', 'python3 scripts/ci/ci-release-trust.py', "bash scripts/ci/ci-dockerhub-publish.sh #{mode}"
  ]
  hub_push = hub_steps.find { |s| s['run'] == "bash scripts/ci/ci-dockerhub-publish.sh #{mode}" }
  abort "#{id}: secret scope mismatch" unless hub_push.fetch('env', {}).select { |_k, v| v.to_s.include?('secrets.') }.keys == ['DOCKERHUB_TOKEN']
  hub_trust = hub_steps.find { |s| s['run'] == 'python3 scripts/ci/ci-release-trust.py' }
  abort "#{id}: trust secret drift" unless hub_trust.fetch('env', {}) == { 'BANSOKO_RELEASE_READ_TOKEN' => '${{ secrets.GITHUB_TOKEN }}' }
end
# A main push supersedes the previous main run; a release tag is never
# cancelled and releases are serialized in their own group.
abort 'concurrency drifted' unless workflow['concurrency'] == {
  'group' => "${{ startsWith(github.ref, 'refs/tags/') && 'bansoko-release' || 'bansoko-ci' }}",
  'cancel-in-progress' => "${{ !startsWith(github.ref, 'refs/tags/') }}"
}
abort 'retired workflow remains' if File.exist?('.gitlab-ci.yml')
# The gates run on the maintainer's own machines, so the trigger set is part of
# the trust boundary: a pull_request run checks out and executes the submitted
# tree, so on a public repository any fork could reach the host runner. Only
# this repository's own pushes and an explicit dispatch may start a run.
triggers = workflow[true] || workflow['on'] || {}
abort 'pull request runs on the host runner' if triggers.key?('pull_request')
abort 'publishing triggers drifted' unless triggers['push'] == { 'branches' => ['main'], 'tags' => ['v*'] }
puts 'Trusted CI DAG declarations passed (not platform execution proof)'
