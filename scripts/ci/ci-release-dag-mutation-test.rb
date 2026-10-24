#!/usr/bin/env ruby
require 'yaml'
require 'tmpdir'
require 'open3'
base = YAML.load_file('.github/workflows/ci.yml')
positive, status = Open3.capture2e('ruby', 'scripts/ci/ci-release-dag-test.rb')
abort "positive control failed: #{positive}" unless status.success?
mutants = {
  'always release' => ->(w) { w['jobs']['test-release']['if'] = '${{ always() }}' },
  'missing gate' => ->(w) { w['jobs']['test-release']['needs'].pop },
  'skipped required' => ->(w) { w['jobs']['qb-real-chain']['if'] = '${{ false }}' },
  'global secret' => ->(w) { w['env'] = {'KEY' => '${{ secrets.KEY }}'} },
  'job secret' => ->(w) { w['jobs']['test-release']['env'] = {'KEY' => '${{ secrets.KEY }}'} },
  'secret in gate' => ->(w) { w['jobs']['go-unit-static']['steps'][0]['env'] = {'KEY' => '${{ secrets.KEY }}'} },
  'continue on error' => ->(w) { w['jobs']['test-release']['continue-on-error'] = true },
  'unknown edge' => ->(w) { w['jobs']['test-release']['needs'] << 'missing' },
  'cycle' => ->(w) { w['jobs']['persistence-freshness']['needs'] = ['contract-boundary'] },
  'lost freshness' => ->(w) { w['jobs']['contract-boundary']['needs'] = [] },
  'swallowed freshness' => ->(w) { w['jobs']['persistence-freshness']['steps'].last['run'] += ' || true' },
  'wrong checkout' => ->(w) { w['jobs']['test-release']['steps'][0]['with']['ref'] = 'main' },
  'echo SHA' => ->(w) { w['jobs']['test-release']['steps'][1]['run'] = 'echo test HEAD' },
  'echo effect' => ->(w) { w['jobs']['test-release']['steps'][3]['run'] = 'echo ci-test-release.py image' },
  'missing results' => ->(w) { w['jobs']['test-release']['steps'][3]['env'].delete('BANSOKO_REQUIRED_RESULTS') },
  'early signing key' => ->(w) { w['jobs']['test-release']['steps'][3]['env']['BANSOKO_RELEASE_SIGNING_KEY'] = '${{ secrets.KEY }}' },
  'effect reversed' => ->(w) { s=w['jobs']['test-release']['steps']; s[4],s[5]=s[5],s[4] },
  # The release dependencies are what make the split PostgreSQL gate binding;
  # each of these removes a real guarantee the DAG test asserts.
  'stale runs kept' => ->(w) { w['concurrency']['cancel-in-progress'] = false },
  'shard dropped from release' => ->(w) { w['jobs']['test-release']['needs'].delete('postgres-integration-shard-2') },
  'shard bypasses selector' => ->(w) { w['jobs']['postgres-integration-shard-1']['steps'].last['env'].delete('BANSOKO_TEST_PG_SHARD') },
  'extra shard job' => ->(w) { w['jobs']['postgres-integration-shard-4'] = Marshal.load(Marshal.dump(w['jobs']['postgres-integration-shard-3'])) },
  # A shard whose declared index and env disagree would run the wrong subset
  # while still publishing: the DAG test reads both.
  'docker hub always' => ->(w) { w['jobs']['docker-hub']['if'] = '${{ always() }}' },
  'docker hub missing gate' => ->(w) { w['jobs']['docker-hub']['needs'].pop },
  'docker hub skips trust' => ->(w) { w['jobs']['docker-hub']['steps'].delete_at(2) },
  'docker hub token in trust' => ->(w) { w['jobs']['docker-hub']['steps'][2]['env']['DOCKERHUB_TOKEN'] = '${{ secrets.DOCKERHUB_TOKEN }}' },
  'docker hub extra secret' => ->(w) { w['jobs']['docker-hub']['steps'][3]['env']['KEY'] = '${{ secrets.KEY }}' },
  'docker hub push first' => ->(w) { s=w['jobs']['docker-hub']['steps']; s[2],s[3]=s[3],s[2] },
  'shard index drift' => ->(w) { w['jobs']['postgres-integration-shard-2']['steps'].last['env']['BANSOKO_TEST_PG_SHARD'] = '1/3' },
  # Dual publication and its write scope: each of these is a real regression the
  # DAG test has to catch, not a cosmetic edit.
  'github destination dropped' => ->(w) { w['jobs']['test-release']['steps'][4]['env']['BANSOKO_RELEASE_DESTINATIONS'] = 'gitea' },
  'github token without destination' => ->(w) { w['jobs']['test-release']['steps'][5]['env'].delete('BANSOKO_RELEASE_DESTINATIONS') },
  'write scope removed' => ->(w) { w['jobs']['test-release'].delete('permissions') },
  'write scope widened to the workflow' => ->(w) { w['jobs']['test-release'].delete('permissions'); w['permissions'] = {'contents' => 'write'} },
  'write scope handed to a gate' => ->(w) { w['jobs']['contract-boundary']['permissions'] = {'contents' => 'write'} },
  'write token reaches the image step' => ->(w) { w['jobs']['test-release']['steps'][3]['env']['BANSOKO_RELEASE_GITHUB_TOKEN'] = '${{ secrets.GITHUB_TOKEN }}' }
}
Dir.mktmpdir('release-dag-mutation') do |dir|
  mutants.each_with_index do |(name, mutate), i|
    copy = Marshal.load(Marshal.dump(base)); mutate.call(copy)
    path = File.join(dir, "#{i}.yml"); File.write(path, YAML.dump(copy))
    output, status = Open3.capture2e({'CI_WORKFLOW'=>path}, 'ruby', 'scripts/ci/ci-release-dag-test.rb')
    abort "survived: #{name}" if status.success?
    puts "killed: #{name}"
  end
end
puts "DAG mutations passed: #{mutants.length}"
