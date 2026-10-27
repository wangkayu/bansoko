#!/usr/bin/env ruby
require 'yaml'
require 'tmpdir'
require 'open3'
base = YAML.load_file('.github/workflows/ci.yml')
positive, status = Open3.capture2e('ruby', 'scripts/ci/ci-release-dag-test.rb')
abort "positive control failed: #{positive}" unless status.success?
# Release job steps: 0 checkout, 1 SHA, 2 trust, 3 preflight, 4 image,
# 5 publish, 6 channel, 7 GitHub Release. The test-release job has the same
# steps 0-6 in its own test-* modes and no GitHub Release.
mutants = {
  'always release' => ->(w) { w['jobs']['release']['if'] = '${{ always() }}' },
  'release on main' => ->(w) { w['jobs']['release']['if'] = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}" },
  'main runs the formal lane' => ->(w) { w['jobs']['test-release'] = Marshal.load(Marshal.dump(w['jobs']['release'])); w['jobs']['test-release']['if'] = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}" },
  'test lane on tags' => ->(w) { w['jobs']['test-release']['if'] = "${{ github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v') }}" },
  'test lane always' => ->(w) { w['jobs']['test-release']['if'] = '${{ always() }}' },
  'test lane missing gate' => ->(w) { w['jobs']['test-release']['needs'].delete('fixture-browser') },
  'test lane dropped' => ->(w) { w['jobs'].delete('test-release') },
  'test lane tolerates failure' => ->(w) { w['jobs']['test-release']['continue-on-error'] = true },
  'test lane skips trust' => ->(w) { w['jobs']['test-release']['steps'].delete_at(2) },
  'test lane writes the formal channel' => ->(w) { w['jobs']['test-release']['steps'][6]['run'] = 'python3 scripts/ci/ci-release.py channel' },
  'test lane publishes a GitHub Release' => ->(w) { w['jobs']['test-release']['steps'] << {'name' => 'create GitHub Release', 'env' => {'BANSOKO_RELEASE_REF_PROTECTED' => 'true', 'BANSOKO_REQUIRED_RESULTS' => '${{ toJSON(needs) }}', 'GH_TOKEN' => '${{ secrets.GITHUB_TOKEN }}'}, 'run' => 'python3 scripts/ci/ci-release.py github-release'} },
  'test channel before its package' => ->(w) { s=w['jobs']['test-release']['steps']; s[5],s[6]=s[6],s[5] },
  'test preflight after the image' => ->(w) { s=w['jobs']['test-release']['steps']; s[3],s[4]=s[4],s[3] },
  'test preflight dropped' => ->(w) { w['jobs']['test-release']['steps'].delete_at(3) },
  'signing key reaches the test preflight' => ->(w) { w['jobs']['test-release']['steps'][3]['env']['BANSOKO_RELEASE_SIGNING_KEY'] = '${{ secrets.RELEASE_SIGNING_KEY }}' },
  'signing key reaches the test image' => ->(w) { w['jobs']['test-release']['steps'][4]['env']['BANSOKO_RELEASE_SIGNING_KEY'] = '${{ secrets.RELEASE_SIGNING_KEY }}' },
  'test lane loses the protected ref proof' => ->(w) { w['jobs']['test-release']['steps'][5]['env'].delete('BANSOKO_RELEASE_REF_PROTECTED') },
  'test lane loses its results binding' => ->(w) { w['jobs']['test-release']['steps'][6]['env'].delete('BANSOKO_REQUIRED_RESULTS') },
  'test lane write scope removed' => ->(w) { w['jobs']['test-release'].delete('permissions') },
  'test lane trust holds the hub token' => ->(w) { w['jobs']['test-release']['steps'][2]['env']['DOCKERHUB_TOKEN'] = '${{ secrets.DOCKERHUB_TOKEN }}' },
  'missing gate' => ->(w) { w['jobs']['release']['needs'].pop },
  'skipped required' => ->(w) { w['jobs']['qb-real-chain']['if'] = '${{ false }}' },
  'host gate skipped on tags' => ->(w) { w['jobs']['fixture-browser']['if'] = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}" },
  'global secret' => ->(w) { w['env'] = {'KEY' => '${{ secrets.KEY }}'} },
  'job secret' => ->(w) { w['jobs']['release']['env'] = {'KEY' => '${{ secrets.KEY }}'} },
  'secret in gate' => ->(w) { w['jobs']['go-unit-static']['steps'][0]['env'] = {'KEY' => '${{ secrets.KEY }}'} },
  'continue on error' => ->(w) { w['jobs']['release']['continue-on-error'] = true },
  'unknown edge' => ->(w) { w['jobs']['release']['needs'] << 'missing' },
  'cycle' => ->(w) { w['jobs']['persistence-freshness']['needs'] = ['contract-boundary'] },
  'lost freshness' => ->(w) { w['jobs']['contract-boundary']['needs'] = [] },
  'swallowed freshness' => ->(w) { w['jobs']['persistence-freshness']['steps'].last['run'] += ' || true' },
  'wrong checkout' => ->(w) { w['jobs']['release']['steps'][0]['with']['ref'] = 'main' },
  'echo SHA' => ->(w) { w['jobs']['release']['steps'][1]['run'] = 'echo test HEAD' },
  'echo effect' => ->(w) { w['jobs']['release']['steps'][4]['run'] = 'echo ci-release.py image' },
  'missing results' => ->(w) { w['jobs']['release']['steps'][4]['env'].delete('BANSOKO_REQUIRED_RESULTS') },
  'early signing key' => ->(w) { w['jobs']['release']['steps'][4]['env']['BANSOKO_RELEASE_SIGNING_KEY'] = '${{ secrets.KEY }}' },
  'effect reversed' => ->(w) { s=w['jobs']['release']['steps']; s[5],s[6]=s[6],s[5] },
  'preflight after the image' => ->(w) { s=w['jobs']['release']['steps']; s[3],s[4]=s[4],s[3] },
  'preflight dropped' => ->(w) { w['jobs']['release']['steps'].delete_at(3) },
  'github release before the channel' => ->(w) { s=w['jobs']['release']['steps']; s[6],s[7]=s[7],s[6] },
  'github release dropped' => ->(w) { w['jobs']['release']['steps'].delete_at(7) },
  'signing key reaches the github release' => ->(w) { w['jobs']['release']['steps'][7]['env']['BANSOKO_RELEASE_SIGNING_KEY'] = '${{ secrets.RELEASE_SIGNING_KEY }}' },
  'signing key reaches the preflight' => ->(w) { w['jobs']['release']['steps'][3]['env']['BANSOKO_RELEASE_SIGNING_KEY'] = '${{ secrets.RELEASE_SIGNING_KEY }}' },
  'release cancelled by main' => ->(w) { w['concurrency']['cancel-in-progress'] = true },
  'one shared group' => ->(w) { w['concurrency']['group'] = 'bansoko-ci' },
  'stale main runs kept' => ->(w) { w['concurrency']['cancel-in-progress'] = false },
  'tag trigger dropped' => ->(w) { (w[true] || w['on'])['push'].delete('tags') },
  'shard dropped from release' => ->(w) { w['jobs']['release']['needs'].delete('postgres-integration-shard-2') },
  'shard bypasses selector' => ->(w) { w['jobs']['postgres-integration-shard-1']['steps'].last['env'].delete('BANSOKO_TEST_PG_SHARD') },
  'extra shard job' => ->(w) { w['jobs']['postgres-integration-shard-4'] = Marshal.load(Marshal.dump(w['jobs']['postgres-integration-shard-3'])) },
  'docker hub always' => ->(w) { w['jobs']['docker-hub']['if'] = '${{ always() }}' },
  'docker hub test image on tags' => ->(w) { w['jobs']['docker-hub']['if'] = "${{ github.event_name == 'push' }}" },
  'docker hub missing gate' => ->(w) { w['jobs']['docker-hub']['needs'].pop },
  'docker hub skips trust' => ->(w) { w['jobs']['docker-hub']['steps'].delete_at(2) },
  'docker hub token in trust' => ->(w) { w['jobs']['docker-hub']['steps'][2]['env']['DOCKERHUB_TOKEN'] = '${{ secrets.DOCKERHUB_TOKEN }}' },
  'docker hub extra secret' => ->(w) { w['jobs']['docker-hub']['steps'][3]['env']['KEY'] = '${{ secrets.KEY }}' },
  'docker hub push first' => ->(w) { s=w['jobs']['docker-hub']['steps']; s[2],s[3]=s[3],s[2] },
  'docker hub main publishes latest' => ->(w) { w['jobs']['docker-hub']['steps'][3]['run'] = 'bash scripts/ci/ci-dockerhub-publish.sh release' },
  'latest before the signed release' => ->(w) { w['jobs']['docker-hub-release']['needs'].delete('release') },
  'latest on main' => ->(w) { w['jobs']['docker-hub-release']['if'] = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}" },
  'latest skips trust' => ->(w) { w['jobs']['docker-hub-release']['steps'].delete_at(2) },
  'latest pushes the test tag' => ->(w) { w['jobs']['docker-hub-release']['steps'][3]['run'] = 'bash scripts/ci/ci-dockerhub-publish.sh test' },
  'shard index drift' => ->(w) { w['jobs']['postgres-integration-shard-2']['steps'].last['env']['BANSOKO_TEST_PG_SHARD'] = '1/3' },
  'publish step loses the github token' => ->(w) { w['jobs']['release']['steps'][5]['env'].delete('BANSOKO_RELEASE_GITHUB_TOKEN') },
  'channel step loses the github token' => ->(w) { w['jobs']['release']['steps'][6]['env'].delete('BANSOKO_RELEASE_GITHUB_TOKEN') },
  'registry token reaches the channel step' => ->(w) { w['jobs']['release']['steps'][6]['env']['DOCKERHUB_TOKEN'] = '${{ secrets.DOCKERHUB_TOKEN }}' },
  'write scope removed' => ->(w) { w['jobs']['release'].delete('permissions') },
  'write scope widened to the workflow' => ->(w) { w['jobs']['release'].delete('permissions'); w['permissions'] = {'contents' => 'write'} },
  'write scope handed to a gate' => ->(w) { w['jobs']['contract-boundary']['permissions'] = {'contents' => 'write'} },
  'write scope handed to the latest image' => ->(w) { w['jobs']['docker-hub-release']['permissions'] = {'contents' => 'write'} },
  'write token reaches the image step' => ->(w) { w['jobs']['release']['steps'][4]['env']['BANSOKO_RELEASE_GITHUB_TOKEN'] = '${{ secrets.GITHUB_TOKEN }}' }
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
