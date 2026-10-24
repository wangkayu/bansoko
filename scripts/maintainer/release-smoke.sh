#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/bansoko-release-smoke.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
latest_schema_version="$(ruby -e '
  root = ARGV.fetch(0)
  versions = Dir[File.join(root, "db", "migrations", "*.sql")].map { |path| File.basename(path, ".sql") }
  abort "no migrations found" if versions.empty?
  puts versions.max_by { |version| version.split("_", 2).first.to_i }
' "$root")"

ruby -rjson -e '
  document = JSON.parse(File.read(ARGV.fetch(0)))
  required = %w[schema_version release_id version channel published_at source_commit api_contract_version target_migration_version rollback_class artifacts sbom_ref provenance_ref artifact_digest signature immutable]
  missing = required - Array(document["required"])
  abort "release schema missing required fields: #{missing.join(",")}" unless missing.empty?
  abort "release schema must reject mutable latest" unless document.fetch("properties").fetch("version").fetch("minLength") >= 1
  abort "release schema must pin schema_version to release-manifest-1" unless document.fetch("properties").fetch("schema_version").fetch("const") == "release-manifest-1"
  abort "current schema must require embedded update" unless document.fetch("required").include?("embedded_update")
  abort "current schema must require release_sequence" unless document.fetch("required").include?("release_sequence")
  abort "current schema must require non-empty frontend_ref" unless document.fetch("properties").fetch("frontend_ref").fetch("pattern") == "^sha256:[0-9a-fA-F]{64}$"
' "$root/internal/releasecontract/manifest.schema.json"

go_args=(run ./cmd/bansoko-release)
go "${go_args[@]}" keygen --private-key "$tmp/private.key" --public-key "$tmp/public.key" >/dev/null

printf '%s\n' '{"schema_version":"release-manifest-0","release_id":"release-smoke","version":"2.0.0-smoke.1","channel":"stable","published_at":"2026-07-13T00:00:00Z","source_commit":"0123456789abcdef0123456789abcdef01234567","api_contract_version":"v2","target_migration_version":"'"$latest_schema_version"'","required_downtime":true,"backup_required":true,"rollback_class":"code_only","artifacts":[{"platform":"linux","architecture":"amd64","digest":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","size":0,"signature":"artifact-signature"}],"sbom_ref":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","provenance_ref":"https://example.invalid/provenance/release-smoke","artifact_digest":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","signature":"","immutable":true}' > "$tmp/unsigned.json"
BANSOKO_EXPECTED_SCHEMA_VERSION="$latest_schema_version" ruby -rjson -e '
  manifest = JSON.parse(File.read(ARGV.fetch(0)))
  abort "release smoke manifest is not immutable" unless manifest["immutable"] == true
  abort "release smoke manifest uses mutable version" if manifest["version"] == "latest"
  abort "release smoke manifest targets a stale migration" unless manifest["target_migration_version"] == ENV.fetch("BANSOKO_EXPECTED_SCHEMA_VERSION")
' "$tmp/unsigned.json"

go "${go_args[@]}" sign --input "$tmp/unsigned.json" --output "$tmp/signed.json" --private-key "$tmp/private.key" >/dev/null
go "${go_args[@]}" verify --manifest "$tmp/signed.json" --public-key "$tmp/public.key" >/dev/null

# A correctly signed manifest with an unknown schema_version must be rejected
# by check-release.sh before any current-only validation runs.
reject_output="$tmp/unknown-schema-check.log"
if BANSOKO_RELEASE_MANIFEST="$tmp/signed.json" BANSOKO_RELEASE_PUBLIC_KEY="$tmp/public.key" \
  "$root/scripts/ci/check-release.sh" >"$reject_output" 2>&1; then
  echo "check-release.sh accepted an unknown manifest schema_version" >&2
  exit 1
fi
grep -q "unsupported manifest schema_version: release-manifest-0" "$reject_output" || {
  echo "check-release.sh did not reject the unknown schema_version for the expected reason" >&2
  exit 1
}

echo "Release manifest/schema smoke passed"
