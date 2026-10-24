#!/usr/bin/env bash
# Structured production Compose image selector and digest predicate.
set -euo pipefail
check_application_image_refs() {
  local compose_file="$1"
ruby -ryaml -e '
    doc = YAML.load_file(ARGV.fetch(0))
    expected = %q{${BANSOKO_BUNDLE_IMAGE_REPOSITORY:-wangkayu/bansoko}@${BANSOKO_BUNDLE_IMAGE_DIGEST:?BANSOKO_BUNDLE_IMAGE_DIGEST (sha256:...) must be injected by the builder}}
    services = doc.fetch("services")
    %w[app].each do |name|
      service = services.fetch(name) { abort "missing required service #{name}" }
      image = service["image"]
      abort "#{name}.image must exactly equal the current repository and injected digest expression" unless image == expected
    end
  ' "$compose_file"
}
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  [[ $# -eq 1 ]] || { echo "usage: $0 COMPOSE_FILE" >&2; exit 2; }
  check_application_image_refs "$1"
fi
