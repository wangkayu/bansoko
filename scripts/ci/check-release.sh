#!/usr/bin/env bash
# Release bundle validator (P0.7). Fail-closed component consistency check.
#
# Verifies:
#   1. manifest signature (existing cmd/bansoko-release verify);
#   2. manifest schema_version is the current bundle contract (release-manifest-1);
#   3. production compose constraints: no build:, application images pinned
#      by @sha256:, no source mounts, healthcheck uses /livez (not
#      /system/health), no separate migrate service (the app bootstrap migrates);
#   4. manifest ↔ repo consistency: migration chain checksums/order match
#      db/migrations, schema target is the newest migration, images is
#      non-empty for production, frontend_ref matches frontend/dist when
#      present;
#   5. embedded update segment: identity binding, payload digest/signature and
#      the bootstrap version contract (UPDATE-029): `bootstrap_min_version` must
#      be an `embedded-bootstrap-v<N>` ID and must not exceed the bootstrap
#      version baked into the image the release was built with
#      (BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION, derived from the pushed image by the
#      CI image job). A bundle that carries an image without that declaration
#      fails closed; a binary-only rehearsal bundle has no image to bind to.
#
# Any failure exits non-zero with {"valid":false,"reason":"..."} on stderr.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"

manifest="${BANSOKO_RELEASE_MANIFEST:-}"
public_key="${BANSOKO_RELEASE_PUBLIC_KEY:-}"
artifact="${BANSOKO_RELEASE_ARTIFACT:-}"
compose="${BANSOKO_RELEASE_COMPOSE:-$root/deployment/maintainer/docker-compose.production.yml}"
if [[ -z "$manifest" || -z "$public_key" ]]; then
  echo '{"valid":false,"reason":"BANSOKO_RELEASE_MANIFEST and BANSOKO_RELEASE_PUBLIC_KEY are required"}' >&2
  exit 2
fi

fail() {
  echo "{\"valid\":false,\"reason\":\"$1\"}" >&2
  exit 1
}

if [[ "${BANSOKO_RELEASE_ALLOW_EMPTY_IMAGES:-0}" == "1" && "${BANSOKO_REHEARSAL:-0}" != "1" ]]; then
  fail "BANSOKO_RELEASE_ALLOW_EMPTY_IMAGES=1 is rehearsal-only and requires BANSOKO_REHEARSAL=1"
fi

# 1. Signature verification.
args=(run ./cmd/bansoko-release verify --manifest "$manifest" --public-key "$public_key")
if [[ -n "$artifact" ]]; then
  args+=(--artifact "$artifact")
fi
(cd "$root" && go "${args[@]}") >/dev/null 2>&1 || fail "manifest signature verification failed"

# 2. Schema validation. Only the embedded strict release-manifest-1 schema is
# accepted. Empty images is rehearsal-only.
schema="$(ruby -rjson -e 'puts JSON.parse(File.read(ARGV[0]))["schema_version"]' "$manifest")"
case "$schema" in
  release-manifest-1)
    if [[ -z "$artifact" ]]; then
      fail "BANSOKO_RELEASE_ARTIFACT is required for release-manifest-1 manifests"
    fi
    # Re-run the signed verification with the artifact bound to the manifest;
    # no manifest may pass without this digest check.
    artifact_args=(run ./cmd/bansoko-release verify --manifest "$manifest" --public-key "$public_key" --artifact "$artifact")
    (cd "$root" && go "${artifact_args[@]}") >/dev/null 2>&1 || fail "manifest/artifact verification failed"
    schema_args=(run ./cmd/bansoko-release validate-schema --manifest "$manifest")
    if [[ "${BANSOKO_RELEASE_ALLOW_EMPTY_IMAGES:-0}" == "1" && "${BANSOKO_REHEARSAL:-0}" == "1" ]]; then schema_args+=(--allow-empty-images=true); fi
    (cd "$root" && go "${schema_args[@]}") >/dev/null 2>&1 || fail "current manifest schema validation failed"
    ;;
  *) fail "unsupported manifest schema_version: ${schema:-<missing>}" ;;
esac

# 3. Production compose constraints.
ruby -ryaml -e '
  spec = YAML.load_file(ARGV.fetch(0))
  spec.fetch("services").each do |name, svc|
    abort "service #{name} has a build: section" if svc.key?("build")
    image = svc.fetch("image", "")
    if image.start_with?("bansoko")
      # Pinned by digest, or by the builder-injected ${BANSOKO_BUNDLE_IMAGE_DIGEST}
      # template that expands to sha256:... at render time. A bare mutable tag
      # (bansoko:latest) or a build: section is forbidden.
      unless image.include?("@sha256:") || image.include?("@${BANSOKO_BUNDLE_IMAGE_DIGEST")
        abort "service #{name} app image must be pinned by digest: #{image}"
      end
    end
    (svc.fetch("volumes", []) || []).each do |vol|
      raw = vol.is_a?(Hash) ? vol.fetch("source", "") : vol.to_s
      if raw.start_with?(".", "/", "~", "${PWD}", "${BANSOKO_RELEASE_PUBLIC_KEY_FILE")
        next if raw.include?("deployment/public.key") # deployment-owned read-only secret
        abort "service #{name} volume looks like a source mount: #{raw}"
      end
    end
    if name == "app" || name == "server"
      hc = svc.dig("healthcheck", "test").to_a.join(" ")
      abort "app healthcheck must use /livez: #{hc}" unless hc.include?("/api/v1/livez")
      abort "app healthcheck must not use /system/health" if hc.include?("/system/health")
    end
    # The app bootstrap migrates the selected tree before serving (base or
    # overlay); a separate migration job would race it.
    abort "the retired migrate service must not return: the app bootstrap migrates before serving" if name == "migrate"
    if name == "app"
      abort "app must run the bootstrap ENTRYPOINT without entrypoint/command overrides" if svc.key?("entrypoint") || svc.key?("command")
      env = svc.fetch("environment", {}) || {}
      abort "app must explicitly set BANSOKO_UPDATE_MODE=embedded" unless env["BANSOKO_UPDATE_MODE"] == "embedded"
      derived = %w[BANSOKO_MIGRATION_DATABASE_URL BANSOKO_RUNTIME_DATABASE_URL BANSOKO_DB_RUNTIME_USER BANSOKO_DB_RUNTIME_PASSWORD BANSOKO_SESSION_SECRET BANSOKO_SECRET_ENCRYPTION_KEY]
      abort "app must not configure derived connections or generated secrets" if derived.any? { |key| env.key?(key) }
    end
  end
  puts "production compose constraints ok"
' "$compose" >/dev/null || fail "production compose constraint violation"

# 4a. Manifest ↔ migration chain consistency.
ruby -rjson -rdigest -e '
  manifest = JSON.parse(File.read(ARGV.fetch(0)))
  root = ARGV.fetch(1)
  files = Dir[File.join(root, "db", "migrations", "*.sql")].sort
  abort "no migrations on disk" if files.empty?
  expected = files.map do |path|
    {"id" => File.basename(path), "checksum" => Digest::SHA256.file(path).hexdigest}
  end
  actual = manifest.fetch("migrations")
  abort "migration count mismatch: manifest=#{actual.length} disk=#{expected.length}" unless actual.length == expected.length
  expected.each_with_index do |want, i|
    got = actual[i] || {}
    abort "migration #{want["id"]} checksum mismatch" unless got["id"] == want["id"] && got["checksum"] == want["checksum"]
    abort "migration #{want["id"]} must be forward-only" unless got["reversible"] == false
  end
  target = manifest.fetch("target_migration_version")
  latest = File.basename(files.last, ".sql")
  abort "schema target #{target} != latest migration #{latest}" unless target == latest
  puts "migration chain consistent (target=#{target})"
' "$manifest" "$root" >/dev/null || fail "migration chain inconsistency"

# 4b. Production bundle must carry image digests.
if [[ "${BANSOKO_RELEASE_ALLOW_EMPTY_IMAGES:-0}" != "1" ]]; then
  images="$(ruby -rjson -e 'puts JSON.parse(File.read(ARGV[0]))["images"].to_a.length' "$manifest")"
  [[ "$images" -gt 0 ]] || fail "production manifest must carry image digests (images=[]); build with docker available or set BANSOKO_RELEASE_ALLOW_EMPTY_IMAGES=1 for rehearsal only"
else
  images="0"
fi

# 4c. frontend_ref consistency when frontend/dist exists.
if [[ -d "$root/frontend/dist" ]]; then
  # Do not hash this with a tar stream: tar headers carry file mtimes, so the digest would differ between rebuilds and break rerun idempotence on an immutable publish.
  frontend_digest="$(cd "$root/frontend/dist" && find . -type f -print | LC_ALL=C sort | xargs shasum -a 256 | shasum -a 256 | awk '{print $1}')"
  manifest_frontend="$(ruby -rjson -e 'puts JSON.parse(File.read(ARGV[0]))["frontend_ref"].to_s' "$manifest")"
  if [[ -n "$manifest_frontend" && "$manifest_frontend" != "sha256:$frontend_digest" ]]; then
    fail "frontend_ref $manifest_frontend != frontend/dist sha256:$frontend_digest"
  fi
fi

# 5. Embedded update segment contract (B1): presence, three-layer identity
#    binding, bare-hex payload digest, monotonic sequence, payload signature,
#    and — when the archive sits next to the manifest — a full unpack smoke
#    check against release-metadata.json.
embedded_ok="$(ruby -rjson -e '
  m = JSON.parse(File.read(ARGV.fetch(0)))
  eu = m["embedded_update"]
  abort "missing embedded_update segment" unless eu.is_a?(Hash)
  seq = m["release_sequence"]
  abort "release_sequence must be a positive integer" unless seq.is_a?(Integer) && seq >= 1
  abort "segment release_sequence != top-level" unless eu["release_sequence"] == seq
  abort "format_version must be embedded-update-v1" unless eu["format_version"] == "embedded-update-v1"
  abort "payload_digest must be bare 64-hex sha256" unless eu["payload_digest"] =~ /\A[a-f0-9]{64}\z/
  abort "payload_size must be positive" unless eu["payload_size"].is_a?(Integer) && eu["payload_size"] > 0
  abort "payload_signature must be non-empty" unless eu["payload_signature"].is_a?(String) && !eu["payload_signature"].strip.empty?
  # The bootstrap minimum is an integer-ordered ID (UPDATE-029). A malformed ID
  # can never be compared at admission, so it must not pass the bundle check;
  # the runtime still refuses such a signed floor with its own typed error.
  bootstrap_min_version = eu["bootstrap_min_version"]
  abort "bootstrap_min_version must be embedded-bootstrap-v<N> with N a decimal integer (got: #{bootstrap_min_version.inspect})" unless bootstrap_min_version.is_a?(String) && bootstrap_min_version =~ /\Aembedded-bootstrap-v[0-9]+\z/
  abort "embedded_update.base_image_min_version is not part of the manifest; use min_image_version" if eu.key?("base_image_min_version")
  min_image_version = eu["min_image_version"]
  abort "embedded update payload requires a non-empty min_image_version" unless min_image_version.is_a?(String) && !min_image_version.strip.empty?
  abort "target_release_id mismatch" unless eu["target_release_id"] == m.fetch("release_id", "")
  abort "target_version mismatch" unless eu["target_version"] == m.fetch("version", "")
  abort "target_schema_version mismatch" unless eu["target_schema_version"] == m.fetch("target_migration_version", "")
  artifacts = m["artifacts"].to_a
  if artifacts[0]
    abort "platform/architecture must match the server artifact" unless eu["platform"] == artifacts[0]["platform"] && eu["architecture"] == artifacts[0]["architecture"]
  end
  puts eu["payload_digest"]
' "$manifest")" || fail "embedded update segment invalid"
echo "embedded update segment ok"

go -C "$root" run ./cmd/bansoko-release artifact-verify --digest "$embedded_ok" \
  --signature "$(ruby -rjson -e 'puts JSON.parse(File.read(ARGV[0])).dig("embedded_update","payload_signature")' "$manifest")" \
  --public-key "$public_key" >/dev/null || fail "embedded payload signature verification failed"

# 5a. Bootstrap version binding (UPDATE-029). The manifest's floor must not
#     exceed the bootstrap version baked into the image the release was built
#     with. That version is established from the pushed image bytes by the CI
#     image job and passed in as BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION; a bundle
#     that carries an image without it fails closed instead of skipping the
#     comparison. A binary-only rehearsal bundle carries no image, so there is
#     nothing to bind to and the step reports that explicitly.
bootstrap_image_version="${BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION:-}"
if [[ "$images" -gt 0 ]]; then
  [[ -n "$bootstrap_image_version" ]] || fail "BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION is required: the release carries an image, so the bootstrap version baked into it must be established (the CI image job derives it from the pushed image)"
fi
if [[ -n "$bootstrap_image_version" ]]; then
  ruby -rjson -e '
    min = JSON.parse(File.read(ARGV.fetch(0))).dig("embedded_update", "bootstrap_min_version").to_s
    baked = ARGV.fetch(1)
    abort "image bootstrap version #{baked.inspect} must be embedded-bootstrap-v<N> with N a decimal integer" unless baked =~ /\Aembedded-bootstrap-v[0-9]+\z/
    min_number = min[/[0-9]+\z/].to_i
    baked_number = baked[/[0-9]+\z/].to_i
    abort "bootstrap_min_version #{min} exceeds the bootstrap version baked into the release image (#{baked})" if min_number > baked_number
  ' "$manifest" "$bootstrap_image_version" || fail "manifest bootstrap_min_version exceeds the release image bootstrap version"
  echo "bootstrap version binding ok (manifest <= image $bootstrap_image_version)"
else
  echo "bootstrap version binding not applicable (bundle carries no image)"
fi

# 5b. Release notes. A formal X.Y.Z release must carry bilingual notes whose
#     first entry is its own version; a test or rehearsal identity carries none.
#     Structure, bounds and order are re-proved by the signature verification
#     above (ValidateReleaseNotes runs on every verify).
ruby -rjson -e '
  m = JSON.parse(File.read(ARGV.fetch(0)))
  version = m.fetch("version")
  notes = m["release_notes"]
  if version =~ /\A(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\z/
    abort "formal release #{version} carries no release_notes" unless notes.is_a?(Array) && !notes.empty?
    first = notes.first
    abort "release_notes must start with #{version}" unless first["version"] == version
    %w[zh-CN en].each do |language|
      text = first.dig("notes", language)
      abort "release_notes of #{version} lack #{language}" unless text.is_a?(String) && !text.strip.empty?
    end
    puts "release notes ok (#{notes.length} version(s))"
  else
    abort "test identity #{version} must not carry release_notes" unless notes.nil?
    puts "release notes not applicable (#{version} is not a formal release)"
  end
' "$manifest" || fail "release notes do not match the release identity"

payload="${BANSOKO_RELEASE_EMBEDDED_PAYLOAD:-$(dirname "$manifest")/embedded-update-v1.tar.gz}"
if [[ -f "$payload" ]]; then
  tmp_check="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-payload-check.XXXXXX")"
  tar -xzf "$payload" -C "$tmp_check" || fail "payload is not a readable gzip tar"
  [[ -f "$tmp_check/release-metadata.json" ]] || fail "payload lacks release-metadata.json"
  ruby -rjson -rdigest -e '
    root = ARGV.fetch(0)
    meta = JSON.parse(File.read(File.join(root, "release-metadata.json")))
    files = meta.fetch("files")
    expected = files.map { |f| f.fetch("path") }.sort
    listing = Dir.chdir(root) do
      Dir.glob("**/*", File::FNM_DOTMATCH)
        .reject { |p| File.directory?(File.join(root, p)) }
        .reject { |p| p == "release-metadata.json" } # self-referential by design
    end.sort
    abort "extracted tree differs from metadata files list (extracted=#{listing.length} declared=#{expected.length})" unless listing == expected
    files.each do |f|
      abs = File.join(root, f.fetch("path"))
      abort "declared entry missing on disk: #{f["path"]}" unless File.file?(abs)
      abort "size drift for #{f["path"]}" unless File.size(abs) == f.fetch("size")
      got = Digest::SHA256.file(abs).hexdigest
      abort "sha256 drift for #{f["path"]}" unless got == f.fetch("sha256")
    end
    puts "payload content manifest ok (#{files.length} files)"
  ' "$tmp_check" >/dev/null || fail "payload content does not match its embedded manifest"
  rm -rf "$tmp_check"
  echo "payload unpack smoke check passed"
fi

echo "Release check passed ($schema, $images image(s))"
