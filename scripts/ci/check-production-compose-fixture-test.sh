#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
helper="${CHECK_PRODUCTION_IMAGE_REFS_HELPER:-$root/scripts/ci/check-production-image-refs.sh}"
source "$helper"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/check-compose-fixture.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
compose="$tmp/compose.yml"
cp "$root/deployment/maintainer/docker-compose.production.yml" "$compose"
if ! check_application_image_refs "$compose"; then
  echo 'valid production compose fixture unexpectedly failed' >&2; exit 1
fi
make_variant() {
  local service="$1" image="$2" service_name="${3:-$1}" output="$4"
  ruby -ryaml -e '
    doc = YAML.load_file(ARGV[0])
    service, image, replacement, output = ARGV[1..]
    value = doc.fetch("services").fetch(service)
    value["image"] = image unless image == "__keep__"
    if replacement != "__keep__" && replacement != service
      doc["services"][replacement] = doc["services"].delete(service)
    end
    File.write(output, YAML.dump(doc))
  ' "$compose" "$service" "$image" "$service_name" "$output"
}
expect_reject() {
  local label="$1" file="$2"
  local label="$1" file="$2" expected_reason="${3:-image must exactly equal}" output
  if output="$(check_application_image_refs "$file" 2>&1)"; then
    echo "checker accepted negative fixture: $label" >&2; exit 1
  fi
  [[ "$output" == *"$expected_reason"* ]] || { echo "negative fixture failed for wrong reason ($label): $output" >&2; exit 1; }
}
digest='@${BANSOKO_BUNDLE_IMAGE_DIGEST:?BANSOKO_BUNDLE_IMAGE_DIGEST (sha256:...) must be injected by the builder}'
expected='${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko}'"$digest"
for service in app; do
  # The repository is a variable with a public default, so every mutation has
  # to stay inside that form: a literal repository is as wrong as a bad one.
  make_variant "$service" '${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wrong-org/bansoko}'"$digest" __keep__ "$tmp/wrong-org-$service.yml"
  expect_reject "wrong org only on $service" "$tmp/wrong-org-$service.yml"
  make_variant "$service" '${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko-other}'"$digest" __keep__ "$tmp/similar-$service.yml"
  expect_reject "similar repo only on $service" "$tmp/similar-$service.yml"
  make_variant "$service" '${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/Bansoko}'"$digest" __keep__ "$tmp/uppercase-$service.yml"
  expect_reject "non-lowercase repository only on $service" "$tmp/uppercase-$service.yml"
  make_variant "$service" 'registry.example.test/acme/app@sha256:'"$(printf 'a%.0s' {1..64})" __keep__ "$tmp/old-prefix-$service.yml"
  expect_reject "old prefix only on $service" "$tmp/old-prefix-$service.yml"
  make_variant "$service" '${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko}:latest' __keep__ "$tmp/latest-$service.yml"
  expect_reject "legal latest tag only on $service" "$tmp/latest-$service.yml"
  make_variant "$service" '${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko}:v1.2.3' __keep__ "$tmp/tag-$service.yml"
  expect_reject "plain tag instead of injected digest only on $service" "$tmp/tag-$service.yml"
  make_variant "$service" 'wangkayu/bansoko'"$digest" __keep__ "$tmp/fixed-repo-$service.yml"
  expect_reject "fixed repository instead of the variable only on $service" "$tmp/fixed-repo-$service.yml"
  make_variant "$service" "$expected" "${service}-typo" "$tmp/selector-$service.yml"
expect_reject "missing required selector $service" "$tmp/selector-$service.yml" "missing required service"
done
# Prove the fixture rejects a matcher mutant that accepts any non-empty image fields.
if [[ -z "${CHECK_PRODUCTION_IMAGE_REFS_HELPER:-}" ]]; then
# Prove the fixture rejects a matcher mutant that accepts any non-empty image fields.
mutant="$tmp/accept-any-image.sh"
cat >"$mutant" <<'MUTANT'
#!/usr/bin/env bash
set -euo pipefail
check_application_image_refs() {
  ruby -ryaml -e 'd=YAML.load_file(ARGV[0]); %w[app].each { |s| abort "missing service" unless d.fetch("services").key?(s); abort "missing image" if d.fetch("services").fetch(s)["image"].to_s.empty? }' "$1"
}
MUTANT
if CHECK_PRODUCTION_IMAGE_REFS_HELPER="$mutant" bash "$root/scripts/ci/check-production-compose-fixture-test.sh" >"$tmp/mutant.out" 2>&1; then
  echo 'fixture accepted matcher mutant that allows any non-empty image' >&2; exit 1
fi
grep -q 'checker accepted negative fixture: wrong org only on app' "$tmp/mutant.out" || { cat "$tmp/mutant.out" >&2; echo 'mutant failed for an unexpected reason' >&2; exit 1; }
printf '%s\n' 'image matcher mutant was rejected by the wrong-org image assertion'
fi
printf '%s\n' 'production compose image fixture tests passed: exact app selector/repo-variable/digest required; single-sided wrong org, old prefix, similar repo, uppercase, latest, plain tag, fixed repository, and selector mutations rejected'
