#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
version="${BANSOKO_RELEASE_VERSION:-}"
release_id="${BANSOKO_RELEASE_ID:-}"
channel="${BANSOKO_RELEASE_CHANNEL:-stable}"
private_key="${BANSOKO_RELEASE_PRIVATE_KEY:-}"
public_key="${BANSOKO_RELEASE_PUBLIC_KEY:-}"
output_dir="${BANSOKO_RELEASE_OUTPUT_DIR:-$root/.local-artifacts/bansoko-release}"
goos="${BANSOKO_RELEASE_GOOS:-linux}"
goarch="${BANSOKO_RELEASE_GOARCH:-amd64}"

# Embedded-update contract inputs (B1). All of them enter signed bytes, so
# they are required and validated fail-closed instead of being defaulted.
release_sequence="${BANSOKO_RELEASE_SEQUENCE:-}"
bootstrap_min_version="${BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION:-}"
base_image_min_version="${BANSOKO_BASE_IMAGE_MIN_VERSION:-}"
api_version="${BANSOKO_API_VERSION:-v1}"
# Embedded payload packaging must be reproducible: no implicit "git commit
# time" fallback anymore.
source_epoch="${SOURCE_DATE_EPOCH:-}"

schema_version="$(ruby -e '
  root = ARGV.fetch(0)
  versions = Dir[File.join(root, "db", "migrations", "*.sql")].map { |path| File.basename(path, ".sql") }
  abort "no migrations found" if versions.empty?
  puts versions.max_by { |version| version.split("_", 2).first.to_i }
' "$root")"

if [[ -z "$version" || -z "$release_id" || -z "$private_key" || -z "$public_key" ]]; then
  echo "BANSOKO_RELEASE_VERSION, BANSOKO_RELEASE_ID, BANSOKO_RELEASE_PRIVATE_KEY and BANSOKO_RELEASE_PUBLIC_KEY are required" >&2
  exit 2
fi
if [[ -z "$release_sequence" ]]; then
  echo "BANSOKO_RELEASE_SEQUENCE is required: a publisher-owned monotonic integer for embedded-update replay protection" >&2
  exit 2
fi
if ! [[ "$release_sequence" =~ ^[0-9]+$ ]] || (( release_sequence < 1 )); then
  echo "BANSOKO_RELEASE_SEQUENCE must be a positive integer (got: $release_sequence)" >&2
  exit 2
fi
if [[ -z "$bootstrap_min_version" ]]; then
  echo "BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION is required (e.g. embedded-bootstrap-v1)" >&2
  exit 2
fi
# Bootstrap version IDs (UPDATE-029) are `embedded-bootstrap-v<N>` with integer
# ordering on N. The value enters signed bytes and is compared as an integer at
# admission, so a malformed ID must never be signed.
if [[ ! "$bootstrap_min_version" =~ ^embedded-bootstrap-v([0-9]+)$ ]]; then
  echo "ERROR: BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION must be embedded-bootstrap-v<N> with N a decimal integer (got: $bootstrap_min_version)" >&2
  exit 2
fi
bootstrap_min_version_number="${BASH_REMATCH[1]}"
# Publisher declaration of the bootstrap version baked into the image this
# release targets. The CI image job derives it from the pushed image bytes; the
# binding below is what makes a release whose minimum exceeds its image
# impossible to sign, so it is required whenever an image is part of the bundle.
declared_image_bootstrap_version="${BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION:-}"
if [[ -n "$declared_image_bootstrap_version" && ! "$declared_image_bootstrap_version" =~ ^embedded-bootstrap-v([0-9]+)$ ]]; then
  echo "ERROR: BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION must be embedded-bootstrap-v<N> with N a decimal integer (got: $declared_image_bootstrap_version)" >&2
  exit 2
fi
if [[ -z "$base_image_min_version" ]]; then
  echo "BANSOKO_BASE_IMAGE_MIN_VERSION is required (the base image compatibility label this payload supports)" >&2
  exit 2
fi
if [[ -z "$source_epoch" ]] || ! [[ "$source_epoch" =~ ^[0-9]+$ ]]; then
  echo "SOURCE_DATE_EPOCH is required and must be a unix timestamp integer for deterministic payload packing" >&2
  exit 2
fi
if [[ "$version" == "latest" || "$version" == *latest* ]]; then
  echo "mutable latest is not a release identity" >&2
  exit 2
fi
case "$channel" in
  stable|beta|nightly) ;;
  *) echo "unsupported release channel: $channel" >&2; exit 2 ;;
esac
if [[ "$(git -C "$root" status --porcelain)" != "" && "${BANSOKO_ALLOW_DIRTY_RELEASE:-0}" != "1" ]]; then
  echo "release build requires a clean git worktree; set BANSOKO_ALLOW_DIRTY_RELEASE=1 only for local rehearsal" >&2
  exit 2
fi
if [[ ! -f "$private_key" || ! -f "$public_key" ]]; then
  echo "release signing key files are required" >&2
  exit 2
fi

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# Reads the single `embedded-bootstrap-v<N>` literal out of /app/bootstrap of a
# locally built/loaded image (UPDATE-029). Empty or multi-line output means the
# image's baked bootstrap version could not be established; the caller fails
# closed. Docker is already required wherever this is called.
baked_bootstrap_version() {
  docker run --rm --entrypoint sh "$1" -c \
    "tr -c '[:print:]' '\\n' < /app/bootstrap | grep -o 'embedded-bootstrap-v[0-9][0-9]*' | sort -u" 2>/dev/null || true
}

source_commit="$(git -C "$root" rev-parse HEAD)"
published_at="$(ruby -rtime -e 'puts Time.at(Integer(ARGV.fetch(0))).utc.iso8601' "$source_epoch")"

# Embedded payload needs a real built frontend dist. Fail-closed: never fall
# back to a stale directory silently (this does not run pnpm build itself).
frontend_dist="${BANSOKO_RELEASE_FRONTEND_DIST:-$root/frontend/dist}"
if [[ ! -d "$frontend_dist" ]]; then
  echo "frontend dist directory not found: $frontend_dist (set BANSOKO_RELEASE_FRONTEND_DIST to the built output)" >&2
  exit 2
fi
if [[ ! -f "$frontend_dist/index.html" ]]; then
  echo "frontend dist at $frontend_dist is missing index.html; run pnpm build first" >&2
  exit 2
fi

artifact_name="bansoko-server-${goos}-${goarch}"
payload_name="embedded-update-v1.tar.gz"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-release.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT
mkdir -p "$output_dir"

artifact_path="$tmp_dir/$artifact_name"
CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -C "$root" -trimpath -buildvcs=false \
  -ldflags "-s -w -buildid= -X bansoko/internal/runtime/buildinfo.Version=$version -X bansoko/internal/runtime/buildinfo.Commit=$source_commit -X bansoko/internal/runtime/buildinfo.BuildID=$release_id -X bansoko/internal/runtime/buildinfo.BuildTime=$published_at -X bansoko/internal/runtime/buildinfo.APIVersion=$api_version -X bansoko/internal/runtime/buildinfo.SchemaVersion=$schema_version -X bansoko/internal/runtime/buildinfo.BaseCompatibilityVersion=$base_image_min_version -X bansoko/internal/runtime/buildinfo.BuildDirty=false" \
  -o "$artifact_path" ./cmd/bansoko-server

# The embedded payload ships its own migration job so the container bootstrap
# can migrate the database from overlay content before promoting the release.
migrate_path="$tmp_dir/bansoko-migrate"
CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -C "$root" -trimpath -buildvcs=false \
  -ldflags "-s -w -buildid=" \
  -o "$migrate_path" ./cmd/bansoko-migrate

artifact_digest="$(sha256_file "$artifact_path")"
artifact_size="$(wc -c < "$artifact_path" | tr -d '[:space:]')"
artifact_signature="$(go -C "$root" run ./cmd/bansoko-release artifact-sign --digest "$artifact_digest" --private-key "$private_key")"

# --- Embedded update payload -------------------------------------------------
# A deterministic gzip tar built by internal/update/packagemaker. The
# overall compressed byte digest is bare 64-hex sha256 and is signed through
# the same artifact-digest surface as the server binary.
payload_path="$tmp_dir/$payload_name"
pack_summary="$(go -C "$root" run ./cmd/bansoko-release pack-payload \
  --server-binary "$artifact_path" \
  --migrate-binary "$migrate_path" \
  --migrations-dir "$root/db/migrations" \
  --frontend-dist "$frontend_dist" \
  --defaults-dir "$root/deployment/defaults" \
  --output "$payload_path" \
  --source-date-epoch "$source_epoch" \
  --format-version "embedded-update-v1" \
  --release-id "$release_id" \
  --version "$version" \
  --source-commit "$source_commit" \
  --build-id "$release_id" \
  --target-schema-version "$schema_version" \
  --platform "$goos" \
  --architecture "$goarch")"
payload_digest="$(ruby -rjson -e 'puts JSON.parse(STDIN.read).fetch("digest")' <<<"$pack_summary")"
payload_size="$(ruby -rjson -e 'puts JSON.parse(STDIN.read).fetch("size").to_i' <<<"$pack_summary")"
if [[ ! -f "$payload_path" ]]; then
  echo "embedded payload was not produced at $payload_path" >&2
  exit 1
fi
if [[ "$(sha256_file "$payload_path")" != "$payload_digest" ]]; then
  echo "embedded payload digest mismatch between packager output and on-disk archive" >&2
  exit 1
fi
payload_signature="$(go -C "$root" run ./cmd/bansoko-release artifact-sign --digest "$payload_digest" --private-key "$private_key")"
go -C "$root" run ./cmd/bansoko-release artifact-verify --digest "$payload_digest" --signature "$payload_signature" --public-key "$public_key" >/dev/null
echo "Embedded update payload: $payload_name digest=$payload_digest size=$payload_size files=$(tar -tzf "$payload_path" | grep -cv '/$')"

go list -C "$root" -m -f '{{.Path}}@{{.Version}}' all | LC_ALL=C sort > "$tmp_dir/modules.txt"
ruby -rjson -e '
  modules = File.readlines(ARGV.fetch(0), chomp: true).reject(&:empty?)
  packages = modules.each_with_index.map do |name, index|
    {"SPDXID" => "SPDXRef-Package-#{index + 1}", "name" => name}
  end
  document = {"spdxVersion" => "SPDX-2.3", "dataLicense" => "CC0-1.0", "SPDXID" => "SPDXRef-DOCUMENT", "name" => "bansoko", "packages" => packages}
  File.write(ARGV.fetch(1), JSON.generate(document) + "\n")
' "$tmp_dir/modules.txt" "$tmp_dir/sbom.spdx.json"
sbom_digest="$(sha256_file "$tmp_dir/sbom.spdx.json")"

BANSOKO_RELEASE_VERSION="$version" BANSOKO_RELEASE_ID="$release_id" BANSOKO_SOURCE_COMMIT="$source_commit" SOURCE_DATE_EPOCH="$source_epoch" \
ruby -rjson -e '
  document = {
    "type" => "https://in-toto.io/Statement/v1",
    "subject" => [{"name" => ARGV.fetch(0), "digest" => {"sha256" => ARGV.fetch(1)}}],
    "predicateType" => "https://slsa.dev/provenance/v1",
    "predicate" => {
      "buildType" => "bansoko/reproducible-go-build",
      "invocation" => {"parameters" => {"version" => ENV.fetch("BANSOKO_RELEASE_VERSION"), "release_id" => ENV.fetch("BANSOKO_RELEASE_ID")}},
      "metadata" => {"source_commit" => ENV.fetch("BANSOKO_SOURCE_COMMIT"), "source_date_epoch" => ENV.fetch("SOURCE_DATE_EPOCH")}
    }
  }
  File.write(ARGV.fetch(2), JSON.generate(document) + "\n")
' "$artifact_name" "$artifact_digest" "$tmp_dir/provenance.json"
provenance_digest="$(sha256_file "$tmp_dir/provenance.json")"

# --- Container image (P0.7): digest-pinned production bundle -----------------
# The image embeds bootstrap+server+migrate+frontend+migrations; the embedded-update
# bootstrap wrapper is the default ENTRYPOINT and the production compose runs the
# migration job via `bootstrap migrate`. Host-owned supervisor/recovery binaries
# are kept outside this application image. Docker is optional: BANSOKO_RELEASE_SKIP_IMAGES=1
# produces a binary-only bundle (images=[]) for rehearsal; check-release rejects
# that for production. A publisher-provided registry digest is sufficient for
# Docker-less package jobs and is therefore handled before probing for docker.
image_digest=""
frontend_ref=""
if [[ "${BANSOKO_RELEASE_SKIP_IMAGES:-0}" != "1" ]]; then
  if [[ -n "${BANSOKO_RELEASE_IMAGE_DIGEST:-}" ]]; then
    # Explicit digest injection: the publisher pushed the image to the
    # registry first and reuses the registry manifest digest here. This is
    # the authoritative path — a local rebuild would change the image id and
    # drift the manifest away from what the registry actually serves.
    image_digest="$BANSOKO_RELEASE_IMAGE_DIGEST"
    if [[ ! "$image_digest" =~ ^sha256:[a-f0-9]{64}$ ]]; then
      echo "ERROR: BANSOKO_RELEASE_IMAGE_DIGEST must be a registry manifest digest matching sha256:<64 hex> (got: $image_digest)" >&2
      exit 1
    fi
  elif command -v docker >/dev/null 2>&1; then
    image_tag="bansoko:${release_id}"
    # No --pull: the production bundle builds against the local base images,
    # and the Dockerfile pins every stage's base image by digest, so a build
    # never resolves a moving tag. --pull would hit the network on every build
    # and stall offline/firewalled runners.
    # The image must match the release artifact architecture (GOARCH), not the
    # build host: on Apple Silicon a plain `docker build` produces an arm64
    # image that an amd64 deployment host cannot pull (P2.4 first rollout
    # finding). buildx --load keeps the image inspectable by the digest read
    # below and needs a builder that can emulate the target platform (Docker
    # Desktop ships QEMU; CI runners build natively).
    docker buildx build -q --load \
      --platform "linux/${goarch}" \
      --build-arg VERSION="$version" \
      --build-arg SOURCE_COMMIT="$source_commit" \
      --build-arg BUILD_ID="$release_id" \
      --build-arg BUILD_TIME="$published_at" \
      --build-arg BUILD_DIRTY=false \
      --build-arg API_VERSION="$api_version" \
      --build-arg SCHEMA_VERSION="$schema_version" \
      --build-arg BASE_COMPATIBILITY_VERSION="$base_image_min_version" \
      -t "$image_tag" "$root" >/dev/null
    image_digest="$(docker inspect --format '{{index .RepoDigests 0}}' "$image_tag" 2>/dev/null | sed 's/.*@//' || true)"
    if [[ -z "$image_digest" ]]; then
      # A local build without a registry push has no RepoDigest, so there is
      # no registry manifest digest to address the release with. Only the
      # isolated rehearsal may fall back to the local config digest (which
      # the registry cannot serve); production releases must push first and
      # re-run the builder with BANSOKO_RELEASE_IMAGE_DIGEST=<registry digest>.
      if [[ "${BANSOKO_RELEASE_ALLOW_CONFIG_DIGEST:-0}" == "1" && "${BANSOKO_REHEARSAL:-0}" == "1" ]]; then
        echo "WARN: RepoDigests empty; falling back to the local config digest (rehearsal-only exemption: BANSOKO_RELEASE_ALLOW_CONFIG_DIGEST=1 + BANSOKO_REHEARSAL=1)" >&2
        image_digest="sha256:$(docker inspect --format '{{index .Id}}' "$image_tag" | sed 's/^sha256://')"
      else
        echo "ERROR: no RepoDigests entry for $image_tag — the image was not pushed to a registry or no registry manifest digest was recorded. Push the image and rebuild with BANSOKO_RELEASE_IMAGE_DIGEST=sha256:<registry manifest digest>. Exemption (isolated rehearsal only): set BOTH BANSOKO_RELEASE_ALLOW_CONFIG_DIGEST=1 and BANSOKO_REHEARSAL=1." >&2
        exit 1
      fi
    fi
  else
    echo "ERROR: docker is required to build the release image, or provide BANSOKO_RELEASE_IMAGE_DIGEST=sha256:<registry manifest digest>" >&2
    exit 1
  fi
  echo "bundle image digest: $image_digest"
fi
# Do not hash this with a tar stream: tar headers carry file mtimes, so the digest would differ between rebuilds and break rerun idempotence on an immutable publish.
frontend_ref="sha256:$(cd "$frontend_dist" && find . -type f -print | LC_ALL=C sort | xargs shasum -a 256 | shasum -a 256 | awk '{print $1}')"

# --- Bootstrap version binding (UPDATE-029) ---------------------------------
# A release may only require a bootstrap that the image it was built for really
# carries. When this script builds the image itself the version is read back
# from those image bytes; otherwise the publisher declaration (the CI image job
# reads it back from the pushed image) is required. It is never inferred from a
# default: an image whose baked bootstrap version cannot be established fails
# closed, and signing a manifest whose minimum exceeds it is impossible here.
image_bootstrap_version="$declared_image_bootstrap_version"
if [[ -n "$image_digest" ]]; then
  if [[ -n "${image_tag:-}" ]]; then
    derived_image_bootstrap_version="$(baked_bootstrap_version "$image_tag")"
    if [[ "$derived_image_bootstrap_version" == *$'\n'* ]]; then
      echo "ERROR: the built image carries more than one embedded-bootstrap-v<N> literal; the baked bootstrap version is ambiguous:
$derived_image_bootstrap_version" >&2
      exit 1
    fi
    if [[ -z "$derived_image_bootstrap_version" ]]; then
      echo "ERROR: no embedded-bootstrap-v<N> literal found in /app/bootstrap of $image_tag; the built-image bootstrap version cannot be established" >&2
      exit 1
    fi
    if [[ ! "$derived_image_bootstrap_version" =~ ^embedded-bootstrap-v[0-9]+$ ]]; then
      echo "ERROR: /app/bootstrap of $image_tag carries an invalid bootstrap version ID: $derived_image_bootstrap_version" >&2
      exit 1
    fi
    if [[ -n "$image_bootstrap_version" && "$image_bootstrap_version" != "$derived_image_bootstrap_version" ]]; then
      echo "ERROR: BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION=$image_bootstrap_version does not match the bootstrap version baked into $image_tag ($derived_image_bootstrap_version)" >&2
      exit 1
    fi
    image_bootstrap_version="$derived_image_bootstrap_version"
  fi
  if [[ -z "$image_bootstrap_version" ]]; then
    echo "ERROR: BANSOKO_RELEASE_IMAGE_BOOTSTRAP_VERSION is required when the release carries an image and this script did not build it: provide the bootstrap version baked into the image the manifest is bound to (the CI image job derives it from the pushed image)" >&2
    exit 1
  fi
  bootstrap_min_version_number=$((10#$bootstrap_min_version_number))
  image_bootstrap_version_number=$((10#${image_bootstrap_version#embedded-bootstrap-v}))
  if (( bootstrap_min_version_number > image_bootstrap_version_number )); then
    echo "ERROR: BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION=$bootstrap_min_version exceeds the bootstrap version baked into the release image ($image_bootstrap_version)" >&2
    exit 1
  fi
  echo "Bootstrap version binding: manifest_min=$bootstrap_min_version image=$image_bootstrap_version"
fi

BANSOKO_RELEASE_VERSION="$version" BANSOKO_RELEASE_ID="$release_id" BANSOKO_RELEASE_CHANNEL="$channel" BANSOKO_RELEASE_PUBLISHED_AT="$published_at" BANSOKO_SOURCE_COMMIT="$source_commit" BANSOKO_RELEASE_API_VERSION="$api_version" BANSOKO_RELEASE_SCHEMA_VERSION="$schema_version" BANSOKO_RELEASE_GOOS="$goos" BANSOKO_RELEASE_GOARCH="$goarch" BANSOKO_RELEASE_ARTIFACT_DIGEST="$artifact_digest" BANSOKO_RELEASE_ARTIFACT_SIZE="$artifact_size" BANSOKO_RELEASE_ARTIFACT_SIGNATURE="$artifact_signature" BANSOKO_RELEASE_SBOM_DIGEST="$sbom_digest" BANSOKO_RELEASE_PROVENANCE_DIGEST="$provenance_digest" BANSOKO_RELEASE_IMAGE_DIGEST="$image_digest" BANSOKO_RELEASE_FRONTEND_REF="$frontend_ref" \
BANSOKO_RELEASE_SEQUENCE="$release_sequence" \
BANSOKO_RELEASE_EMBEDDED_FORMAT="embedded-update-v1" \
BANSOKO_RELEASE_PAYLOAD_DIGEST="$payload_digest" BANSOKO_RELEASE_PAYLOAD_SIZE="$payload_size" BANSOKO_RELEASE_PAYLOAD_SIGNATURE="$payload_signature" \
BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION="$bootstrap_min_version" BANSOKO_BASE_IMAGE_MIN_VERSION="$base_image_min_version" \
ruby -rjson -rdigest -e '
  root = ARGV.fetch(0)
  migrations = Dir[File.join(root, "db", "migrations", "*.sql")].sort.map do |path|
    {"id" => File.basename(path), "checksum" => Digest::SHA256.file(path).hexdigest, "reversible" => false, "compatibility" => "forward-only"}
  end
  images = []
  unless ENV.fetch("BANSOKO_RELEASE_IMAGE_DIGEST", "").empty?
    images = [{"name" => "bansoko", "digest" => ENV.fetch("BANSOKO_RELEASE_IMAGE_DIGEST"), "platform" => "#{ENV.fetch("BANSOKO_RELEASE_GOOS")}/#{ENV.fetch("BANSOKO_RELEASE_GOARCH")}"}]
  end
  # Digest representation note: images[].digest uses the conventional
  # registry "sha256:<64hex>" form; embedded_update.payload_digest is the
  # bare 64-hex form that feeds SignArtifactDigest /
  # VerifyArtifactDigestSignature directly. Both are sha256 of their bytes.
  manifest = {
    "schema_version" => "release-manifest-1",
    "release_sequence" => Integer(ENV.fetch("BANSOKO_RELEASE_SEQUENCE")),
    "release_id" => ENV.fetch("BANSOKO_RELEASE_ID"),
    "version" => ENV.fetch("BANSOKO_RELEASE_VERSION"), "channel" => ENV.fetch("BANSOKO_RELEASE_CHANNEL"),
    "published_at" => ENV.fetch("BANSOKO_RELEASE_PUBLISHED_AT"), "source_commit" => ENV.fetch("BANSOKO_SOURCE_COMMIT"),
    "api_contract_version" => ENV.fetch("BANSOKO_RELEASE_API_VERSION"), "target_migration_version" => ENV.fetch("BANSOKO_RELEASE_SCHEMA_VERSION"),
    "compatible_from_versions" => [], "required_downtime" => true, "backup_required" => true,
    "rollback_class" => "restore_required", "migrations" => migrations,
    "images" => images, "compose_ref" => "deployment/maintainer/docker-compose.production.yml",
    "frontend_ref" => ENV.fetch("BANSOKO_RELEASE_FRONTEND_REF", ""),
    "artifacts" => [{"platform" => ENV.fetch("BANSOKO_RELEASE_GOOS"), "architecture" => ENV.fetch("BANSOKO_RELEASE_GOARCH"), "digest" => ENV.fetch("BANSOKO_RELEASE_ARTIFACT_DIGEST"), "size" => Integer(ENV.fetch("BANSOKO_RELEASE_ARTIFACT_SIZE")), "signature" => ENV.fetch("BANSOKO_RELEASE_ARTIFACT_SIGNATURE")}],
    "sbom_ref" => "sha256:#{ENV.fetch("BANSOKO_RELEASE_SBOM_DIGEST")}", "provenance_ref" => "sha256:#{ENV.fetch("BANSOKO_RELEASE_PROVENANCE_DIGEST")}",
    "artifact_digest" => ENV.fetch("BANSOKO_RELEASE_ARTIFACT_DIGEST"), "signature" => "", "immutable" => true,
    "embedded_update" => {
      "format_version" => ENV.fetch("BANSOKO_RELEASE_EMBEDDED_FORMAT"),
      "platform" => ENV.fetch("BANSOKO_RELEASE_GOOS"),
      "architecture" => ENV.fetch("BANSOKO_RELEASE_GOARCH"),
      "payload_digest" => ENV.fetch("BANSOKO_RELEASE_PAYLOAD_DIGEST"),
      "payload_size" => Integer(ENV.fetch("BANSOKO_RELEASE_PAYLOAD_SIZE")),
      "payload_signature" => ENV.fetch("BANSOKO_RELEASE_PAYLOAD_SIGNATURE"),
      "bootstrap_min_version" => ENV.fetch("BANSOKO_RELEASE_BOOTSTRAP_MIN_VERSION"),
      "min_image_version" => ENV.fetch("BANSOKO_BASE_IMAGE_MIN_VERSION"),
      "target_release_id" => ENV.fetch("BANSOKO_RELEASE_ID"),
      "target_version" => ENV.fetch("BANSOKO_RELEASE_VERSION"),
      "target_schema_version" => ENV.fetch("BANSOKO_RELEASE_SCHEMA_VERSION"),
      "release_sequence" => Integer(ENV.fetch("BANSOKO_RELEASE_SEQUENCE"))
    }
  }
  File.write(ARGV.fetch(1), JSON.generate(manifest) + "\n")
' "$root" "$tmp_dir/manifest.json"

# Validate the unsigned current manifest before it enters the signed byte set.
go -C "$root" run ./cmd/bansoko-release validate-schema --manifest "$tmp_dir/manifest.json" --allow-empty-images="$([[ "${BANSOKO_REHEARSAL:-0}" == "1" ]] && echo true || echo false)" >/dev/null

signed_manifest="$output_dir/release-manifest.json"
go -C "$root" run ./cmd/bansoko-release sign --input "$tmp_dir/manifest.json" --output "$signed_manifest" --private-key "$private_key" >/dev/null
go -C "$root" run ./cmd/bansoko-release verify --manifest "$signed_manifest" --public-key "$public_key" --artifact "$artifact_path" >/dev/null
go -C "$root" run ./cmd/bansoko-release artifact-verify --digest "$artifact_digest" --signature "$artifact_signature" --public-key "$public_key" >/dev/null
go -C "$root" run ./cmd/bansoko-release verify-embedded --manifest "$signed_manifest" --public-key "$public_key" --payload "$payload_path" >/dev/null

cp "$artifact_path" "$output_dir/$artifact_name"
cp "$payload_path" "$output_dir/$payload_name"
cp "$tmp_dir/sbom.spdx.json" "$output_dir/sbom.spdx.json"
cp "$tmp_dir/provenance.json" "$output_dir/provenance.json"
chmod 0755 "$output_dir/$artifact_name"
chmod 0644 "$output_dir/$payload_name" "$output_dir/release-manifest.json" "$output_dir/sbom.spdx.json" "$output_dir/provenance.json"
echo "Reproducible release built: $output_dir"
