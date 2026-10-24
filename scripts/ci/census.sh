#!/usr/bin/env bash
# Generate the repository census from the current checkout.
# The report is evidence, not a checked-in baseline: every number is computed
# from source files and the current test run.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

coverage_mode="${CENSUS_COVERAGE_MODE:-unit}"
if [[ "${1:-}" == "--coverage=all" ]]; then
  coverage_mode=all
elif [[ "${1:-}" == "--coverage=unit" ]]; then
  coverage_mode=unit
elif [[ "${1:-}" == "--no-coverage" ]]; then
  coverage_mode=none
fi

if ! command -v ruby >/dev/null 2>&1; then
  echo "census: ruby is required to parse api/openapi.yaml" >&2
  exit 1
fi

printf '%s\n' 'Census' '======'
printf 'checkout=%s\n' "$(git rev-parse --short HEAD 2>/dev/null || printf unknown)"
printf 'generated_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

endpoint_output="$(ruby -ryaml -e '
  doc = YAML.load_file("api/openapi.yaml")
  seen = {}
  doc.fetch("paths").each do |path, operations|
    operations.each do |method, operation|
      next unless operation.is_a?(Hash) && operation["operationId"]
      operation_id = operation["operationId"]
      abort("duplicate operationId: #{operation_id}") if seen.key?(operation_id)
      seen[operation_id] = true
      puts [operation_id, method.upcase, path].join("\t")
    end
  end
' | LC_ALL=C sort)"
endpoints=()
while IFS= read -r endpoint; do
  [[ -n "$endpoint" ]] && endpoints+=("$endpoint")
done <<<"$endpoint_output"

if [[ "${#endpoints[@]}" -eq 0 ]]; then
  echo 'census: no OpenAPI operationId values found' >&2
  exit 1
fi

printf '\n[openapi]\noperation_ids=%s\n' "${#endpoints[@]}"
printf '%s\n' 'operation_id method path'
printf '%s\n' "${endpoints[@]}"

client="frontend/src/lib/api/client.ts"
covered=0
uncovered=()
for endpoint in "${endpoints[@]}"; do
  operation_id="${endpoint%%$'\t'*}"
  if rg -q --glob '*.ts' --glob '*.tsx' "\\b${operation_id}\\b" "$client"; then
    covered=$((covered + 1))
  else
    uncovered+=("$endpoint")
  fi
done
printf '\n[frontend_client]\ncomparison=exact_operation_id_symbol_reference_in_client_ts\nclient_operation_id_references=%s\n' "$covered"
printf 'operation_ids_without_exact_client_reference=%s\n' "${#uncovered[@]}"
if [[ "${#uncovered[@]}" -gt 0 ]]; then
  printf '%s\n' "${uncovered[@]}"
fi

go_files=$(find internal -type f -name '*.go' | wc -l | tr -d ' ')
go_lines=$(find internal -type f -name '*.go' -print0 | xargs -0 wc -l | awk 'END {print ($1+0)}')
go_test_files=$(find internal cmd -type f -name '*_test.go' | wc -l | tr -d ' ')
go_packages=$(go list ./internal/... 2>/dev/null | wc -l | tr -d ' ')
frontend_files=$(find frontend/src -type f \( -name '*.ts' -o -name '*.tsx' \) | wc -l | tr -d ' ')
frontend_lines=$(find frontend/src -type f \( -name '*.ts' -o -name '*.tsx' \) -print0 | xargs -0 wc -l | awk 'END {print ($1+0)}')
migration_files=$(find db/migrations -type f -name '*.sql' | wc -l | tr -d ' ')
migration_lines=$(find db/migrations -type f -name '*.sql' -print0 | xargs -0 wc -l | awk 'END {print ($1+0)}')
table_defs=$(rg -i -o --glob '*.sql' '^\s*CREATE\s+(?:TEMPORARY\s+)?TABLE\b' db/migrations | wc -l | tr -d ' ')
inline_sql=$(rg -i -o --glob '*.go' '^\s*(SELECT|INSERT|UPDATE|DELETE|WITH|CREATE[[:space:]]+TABLE|ALTER[[:space:]]+TABLE|DROP[[:space:]]+TABLE)\b' internal | wc -l | tr -d ' ')

printf '\n[scale]\ngo_packages=%s\ngo_files=%s\ngo_lines=%s\ngo_test_files=%s\nfrontend_v2_ts_tsx_files=%s\nfrontend_v2_ts_tsx_lines=%s\nv2_migration_files=%s\nv2_migration_lines=%s\nv2_create_table_statements=%s\ninline_sql_statement_starts=%s\n' \
  "$go_packages" "$go_files" "$go_lines" "$go_test_files" "$frontend_files" "$frontend_lines" \
  "$migration_files" "$migration_lines" "$table_defs" "$inline_sql"

if [[ "$coverage_mode" == none ]]; then
  printf '\n[coverage]\nstatus=not_requested\n'
  exit 0
fi

coverage_file="$(mktemp "${TMPDIR:-/tmp}/bansoko-census-cover.XXXXXX")"
coverage_summary="$(mktemp "${TMPDIR:-/tmp}/bansoko-census-summary.XXXXXX")"
cleanup() { rm -f "$coverage_file" "$coverage_summary"; }
trap cleanup EXIT

coverage_packages=(./internal/... ./cmd/...)
if [[ "$coverage_mode" == unit ]]; then
  all_packages="$(go list ./internal/... ./cmd/... | sort)"
  pg_packages="$(sed 's|^\./|bansoko/|' scripts/ci/postgres-packages.txt | sort)"
  coverage_packages=()
  while IFS= read -r package; do
    if ! grep -qxF "$package" <<<"$pg_packages"; then
      coverage_packages+=("$package")
    fi
  done <<<"$all_packages"
fi
if [[ "${#coverage_packages[@]}" -eq 0 ]]; then
  echo 'census: no packages available for coverage' >&2
  exit 1
fi

printf '\n[coverage]\nmode=%s\npackages=%s\n' "$coverage_mode" "${#coverage_packages[@]}"
if go test "${coverage_packages[@]}" -p 1 -count=1 -coverprofile="$coverage_file"; then
  go tool cover -func="$coverage_file" | tee "$coverage_summary"
  total="$(awk '$1 == "total:" {print $3}' "$coverage_summary")"
  printf 'status=passed\ntotal=%s\n' "${total:-unavailable}"
else
  printf 'status=failed\n' >&2
  exit 1
fi
