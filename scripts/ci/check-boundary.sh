#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"

python3 "$root/scripts/ci/check-workitem-write-boundary.py"

active_runtime=(
  "$root/internal" "$root/cmd/bansoko-server" "$root/cmd/bansoko-migrate"
  "$root/cmd/bansoko-reset" "$root/cmd/bansoko-bootstrap"
  "$root/cmd/bansoko-release" "$root/frontend/src" "$root/api" "$root/db/migrations"
)
# The optional recovery source remains a separately deployable/manual
# artifact and is not part of the default application runtime inventory.
implementation_runtime=(
  "$root/internal" "$root/cmd/bansoko-server" "$root/cmd/bansoko-migrate"
  "$root/cmd/bansoko-reset" "$root/cmd/bansoko-bootstrap" "$root/cmd/bansoko-release"
)

anime_root="$root/internal/anime"
anime_paths=(domain application draft/application ownership/application oneshot/application readmodel/application)
# Within anime application buckets, oneshot may import kernel (`application`)
# and draft; imports from ownership/readmodel remain forbidden by the matrix below.
# `application/list_transport.go` is deleted with the readmodel move: it had no
# production caller and projected generated HTTP DTOs inside the application
# layer. The ownership move relocated four of the remaining six legacy facades
# (`metadata_patch_transport.go`, `reevaluate_transport.go`,
# `subgroups_generated_mapper.go`, `subgroups_transport.go`) into the ownership
# bucket and the draft move relocated the last two (`domain_transport.go`,
# `subscribe_transport.go`) into the draft bucket, so the exception is keyed by
# package-relative path rather than by basename, and it still shrinks only by
# deleting a facade.
anime_generated_baseline=(
  draft/application/domain_transport.go
  draft/application/subscribe_transport.go
  ownership/application/metadata_patch_transport.go
  ownership/application/reevaluate_transport.go
  ownership/application/subgroups_generated_mapper.go
  ownership/application/subgroups_transport.go
)

anime_inventory() {
  local base="$1" rel="$2" abs="$1/$2" result status
  [[ -d "$abs" ]] || { echo "Missing configured anime source directory: $rel" >&2; return 1; }
  set +e
  result=$(cd "$base" && rg --files "$rel" --glob '*.go' --glob '!**/*_test.go' 2>&1)
  status=$?
  set -e
  (( status != 2 )) || { echo "rg search error inventorying $rel: $result" >&2; return 1; }
  (( status != 1 )) && [[ -n "$result" ]] || { echo "No production Go-file inventory for configured anime source directory: $rel" >&2; return 1; }
  printf '%s\n' "$result"
}

anime_check() {
  local base="$1" expected="${2:-}" source rel import_path imports status output file baseline entry module_root
  local -a configured=() files=()
  module_root="$root"
  [[ "$base" == "$anime_root" ]] || module_root="${base%/internal/anime}"
  if [[ -n "$expected" ]]; then
    read -r -a configured <<< "$expected"
  else
    for source in "${anime_paths[@]}"; do
      [[ "$source" == domain || "$source" == application || -d "$base/$source" ]] && configured+=("$source")
    done
  fi
  for source in "${configured[@]}"; do
    output=$(anime_inventory "$base" "$source") || return 1
    # `go list` resolves the compiler's package import set rather than scanning
    # source text. Keep inventory independent so missing files/rg failures fail.
    rel="internal/anime/$source"
    import_path="bansoko/$rel"
    set +e
    imports=$(cd "$module_root" && go list -e -f '{{join .Imports "\n"}}' "./$rel" 2>&1)
    status=$?
    set -e
    if (( status != 0 )); then
      echo "go list failed for configured anime package $rel: $imports" >&2
      return 1
    fi
    while IFS= read -r entry; do
      [[ -z "$entry" ]] && continue
      case "$source:$entry" in
        domain:bansoko/internal/anime/application*|domain:bansoko/internal/anime/draft/application*|domain:bansoko/internal/anime/ownership/application*|domain:bansoko/internal/anime/oneshot/application*|domain:bansoko/internal/anime/readmodel/application*)
          echo "Forbidden anime import from $rel: $entry" >&2; return 1 ;;
        application:bansoko/internal/anime/draft/application*|application:bansoko/internal/anime/ownership/application*|application:bansoko/internal/anime/oneshot/application*|application:bansoko/internal/anime/readmodel/application*)
          echo "Forbidden anime import from $rel: $entry" >&2; return 1 ;;
        draft/application:bansoko/internal/anime/ownership/application*|draft/application:bansoko/internal/anime/oneshot/application*|draft/application:bansoko/internal/anime/readmodel/application*)
          echo "Forbidden anime import from $rel: $entry" >&2; return 1 ;;
        ownership/application:bansoko/internal/anime/draft/application*|ownership/application:bansoko/internal/anime/oneshot/application*|ownership/application:bansoko/internal/anime/readmodel/application*)
          echo "Forbidden anime import from $rel: $entry" >&2; return 1 ;;
        oneshot/application:bansoko/internal/anime/ownership/application*|oneshot/application:bansoko/internal/anime/readmodel/application*|readmodel/application:bansoko/internal/anime/oneshot/application*)
          echo "Forbidden anime import from $rel: $entry" >&2; return 1 ;;
      esac
      case "$source:$entry" in
        application:bansoko/internal/transport/httpapi/generated*|draft/application:bansoko/internal/transport/httpapi/generated*|ownership/application:bansoko/internal/transport/httpapi/generated*)
          if [[ "$base" == "$anime_root" ]]; then
            baseline=0
            for file in "${anime_generated_baseline[@]}"; do [[ "$file" == "$source"/* ]] && baseline=1; done
            (( baseline )) || { echo "Forbidden generated HTTP DTO import in $rel: $entry" >&2; return 1; }
          else
            echo "Forbidden generated HTTP DTO import in $rel: $entry" >&2; return 1
          fi
          ;;
        application:bansoko/internal/anime/transport*|draft/application:bansoko/internal/anime/transport*)
          if [[ "$base" == "$anime_root" ]]; then
            output=$(rg -l --glob '*.go' --glob '!**/*_test.go' '"bansoko/internal/anime/transport"' "$base/$source" 2>&1) || status=$?
            if [[ "$output" != *"$source/domain_transport.go"* ]]; then
              echo "Forbidden application-to-anime/transport import from $rel: $entry" >&2; return 1
            fi
          else
            echo "Forbidden application-to-anime/transport import from $rel: $entry" >&2; return 1
          fi
          ;;
        application:bansoko/internal/transport/httpapi*)
          echo "Forbidden application-to-transport import from $rel: $entry" >&2; return 1 ;;
        */application:bansoko/internal/anime/transport*|*/application:bansoko/internal/transport/httpapi*)
          echo "Forbidden application-to-transport import from $rel: $entry" >&2; return 1 ;;
      esac
    done <<< "$imports"
  done
  # `go list` aggregates imports at package scope, so attribute generated DTO
  # imports to their source paths for the ratchet. Every configured anime
  # application bucket is attributed, because the legacy facade basenames no
  # longer all live in the root package.
  local generated_sources
  set +e
  generated_sources=$(cd "$root" && rg -l --glob '*.go' --glob '!**/*_test.go' \
    '"bansoko/internal/transport/httpapi/generated"' internal/anime/application internal/anime/draft/application internal/anime/ownership/application 2>&1)
  status=$?
  set -e
  if (( status == 2 )); then echo "rg search error checking anime DTO source paths: $generated_sources" >&2; return 1; fi
  if (( status == 0 )); then
    while IFS= read -r file; do
      file="${file#internal/anime/}"
      baseline=0
      for entry in "${anime_generated_baseline[@]}"; do [[ "$file" == "$entry" ]] && baseline=1; done
      (( baseline )) || { echo "Forbidden generated HTTP DTO import in unbaselined path $file" >&2; return 1; }
    done <<< "$generated_sources"
  fi
  for entry in "${anime_generated_baseline[@]}"; do
    [[ -f "$root/internal/anime/$entry" ]] || { echo "Stale anime generated-import baseline path: $entry" >&2; return 1; }
  done
  set +e
  output=$(cd "$root" && go list -e -f '{{join .Imports "\n"}}' ./internal/anime/... 2>&1)
  status=$?
  set -e
  if (( status != 0 )); then echo "go list failed for anime production inventory: $output" >&2; return 1; fi
  if printf '%s\n' "$output" | rg -n '^bansoko/internal/anime/application/animetest$'; then
    echo "Production anime package imports forbidden anime/application/animetest" >&2; return 1
  fi
}

run_probe() {
  local probe="$1" tmp="$2" fakebin="$2/bin" fixture="$2/internal/anime"
  printf 'module bansoko\n\ngo 1.26.10\n' > "$tmp/go.mod"
  mkdir -p "$fixture/domain" "$fixture/application" "$fakebin"
  printf 'package domain\n' > "$fixture/domain/domain.go"
  printf 'package application\n' > "$fixture/application/app.go"
  case "$probe" in
    sibling-import)
      mkdir -p "$fixture/draft/application"
      printf 'package application\n' > "$fixture/draft/application/draft.go"
      printf 'package application\nimport _ "bansoko/internal/anime/draft/application"\n' > "$fixture/application/app.go"
      anime_check "$fixture" 'domain application draft/application' ;;
    generated-import)
      mkdir -p "$fixture/draft/application"
      printf 'package application\nimport _ "bansoko/internal/transport/httpapi/generated"\n' > "$fixture/draft/application/moved.go"
      anime_check "$fixture" 'domain draft/application' ;;
    missing-path) anime_check "$fixture" 'domain application ownership/application' ;;
    rg-error)
      printf '#!/usr/bin/env bash\necho simulated-rg-search-error >&2\nexit 2\n' > "$fakebin/rg"
      chmod +x "$fakebin/rg"
      PATH="$fakebin:$PATH" anime_check "$fixture" 'domain application' ;;
    ratchet-import)
      printf 'package application\nimport _ "bansoko/internal/transport/httpapi/generated"\n' > "$fixture/application/new_file.go"
      anime_check "$fixture" 'domain application' ;;
    *) echo "Unknown boundary probe: $probe" >&2; return 2 ;;
  esac
}

if [[ "${1:-}" == --probe ]]; then
  [[ $# == 2 ]] || { echo "Usage: $0 --probe sibling-import|generated-import|missing-path|rg-error|ratchet-import" >&2; exit 2; }
  probe_tmp=$(mktemp -d)
  trap 'rm -rf "$probe_tmp"' EXIT
  run_probe "$2" "$probe_tmp"
  exit $?
fi

anime_check "$anime_root"
run_boundary_probes() {
  local probe result status
  local tmp
  tmp=$(mktemp -d)
  for probe in sibling-import generated-import missing-path rg-error ratchet-import; do
    set +e
    result=$(run_probe "$probe" "$tmp/$probe" 2>&1)
    status=$?
    set -e
    if (( status == 0 )); then echo "Boundary negative probe unexpectedly passed: $probe" >&2; rm -rf "$tmp"; return 1; fi
    [[ -n "$result" ]] || { echo "Boundary negative probe produced no diagnostic: $probe" >&2; rm -rf "$tmp"; return 1; }
  done
  rm -rf "$tmp"
}
run_boundary_probes



if compgen -G "$root/cmd/v2ctl/*.go" >/dev/null; then
  echo "Retired cmd/v2ctl Go source must not return" >&2
  exit 1
fi

retired_go_sources="$(
  cd "$root"
  rg --files internal | rg '^internal/(releaseevaluation|platformoperations|provider/application|identitymetadata/application)/.*\.go$' || true
)"
if [[ -n "$retired_go_sources" ]]; then
  echo "Retired wide-V2 package paths contain active Go source:" >&2
  echo "$retired_go_sources" >&2
  echo "Remove these files." >&2
  exit 1
fi

# Retired top-level package names must not reappear under internal/;
# internal/persistence is the current persistence package and is allowed.
if rg -n '"bansoko/internal/(auth|api|core|services|models|domain|ports)["/]' "${implementation_runtime[@]}"; then
  echo "runtime imports a retired package" >&2
  exit 1
fi

if rg -n --glob '!**/*_test.go' '"bansoko/internal/transport/httpapi/generated"' \
  "$root/internal/filemaintenance/path_diff.go" \
  "$root/internal/filemaintenance/types.go"; then
  echo "Transport-neutral filemaintenance path-diff code must not import generated HTTP DTOs" >&2
  exit 1
fi
global_generated_sources=$(rg -l --glob '*.go' --glob '!**/*_test.go' \
  '"bansoko/internal/transport/httpapi/generated"' "$root/internal" 2>/dev/null || true)
while IFS= read -r source; do
  [[ -z "$source" ]] && continue
  case "$source" in
    */anime/*) continue ;;
    */domain/*|*/application/*)
      case "$source" in
        *_transport.go|*_generated_mapper.go) ;;
        *) echo "Transport-neutral domain/application source imports generated HTTP DTOs: $source" >&2; exit 1 ;;
      esac ;;
  esac
done <<< "$global_generated_sources"


if rg -n --glob '**/application/*.go' --glob '**/application/**/*.go' '"bansoko/internal/transport/httpapi/generated"' \
  "$root/internal/plugintarget/application" \
  "$root/internal/reconciliation/application" \
  "$root/internal/fileoperation/application" \
  "$root/internal/artifact/application" \
  "$root/internal/download/application" \
  "$root/internal/configuration/application" \
  "$root/internal/templatecatalog/application" \
  "$root/internal/update/application" \
  "$root/internal/observability/application" \
  --glob '!**/*_test.go'; then
  echo "Transport-neutral application packages must not import generated HTTP DTOs" >&2
  exit 1
fi


if rg -n --glob '!**/*_test.go' --glob '!**/runtime/migrations/**' 'internal/(releaseevaluation|platformoperations|provider/application|identitymetadata/application)(/|"|$)|\b(SubscriptionPlan|LifecycleTrace|operational_state|CommandIntent|process_checkpoint)\b|\bv2_((subscription|cleanup|placement|reconciliation|replacement|update)_(batch_)?plans?|subscription_plan_(previews|metadata)|lifecycle_(traces|milestones)|process_(instances|checkpoints)|attention(_history)?|outbox|product_milestone_outbox|inbox|leases|command_intents|activity|group_selection_decisions|setup_finalizations|external_control_clients|policy_profiles|automation_controls|subscription_policies|subgroup_policies|group_policy_sets|group_policy_rules|provider_group_defaults)\b' "${active_runtime[@]}"; then
  echo "Active source contains retired packages or vocabulary" >&2
  exit 1
fi

# Active contract authority and product runtime (OpenAPI, generated Go/TS,
# Go runtime, CLI, frontend) must not carry the retired Task Center route/
# schemas or replacement_scan. Forward-only migration history and migration
# retirement tests legitimately reference the retired kind and are excluded.
retired_vocab_runtime=(
  "$root/api" "$root/internal" "$root/cmd/bansoko-server" "$root/cmd/bansoko-migrate"
  "$root/cmd/bansoko-reset" "$root/cmd/bansoko-bootstrap"
  "$root/cmd/bansoko-release"
  "$root/frontend/src"
)
if rg -n -i 'observability/tasks|workitemsummary|taskstatecounts|observabilitytaskpage|replacement_scan|task[-_]center|任务中心' \
  "${retired_vocab_runtime[@]}" \
  --glob '*.yaml' --glob '*.go' --glob '*.ts' --glob '*.tsx' \
  --glob '!**/*_test.go' --glob '!**/*.test.ts' --glob '!**/*.test.tsx' \
  --glob '!**/runtime/migrations/**'; then
  echo "Active contract/runtime contains retired Task Center or replacement_scan vocabulary" >&2
  exit 1
fi

# /api/v1 is the current HTTP base path; the archived implementation's ui.
# package and GORM AutoMigrate must not come back.
if rg -n 'ui\.|AutoMigrate' "${implementation_runtime[@]}"; then
  echo "composition/runtime contains a forbidden archived boundary" >&2
  exit 1
fi

if rg -n 'components/anime/legacy|api/legacy|hooks/legacy' "$root/frontend/src"; then
  echo "Frontend imports a legacy module" >&2
  exit 1
fi

qb_effect_runtime=(
  "$root/internal/download/domain"
  "$root/internal/download/application"
  "$root/internal/download/qbittorrent"
)
if rg -n --glob '!**/*_test.go' '\bSourceURL\b|json:"source_url|SubmitWithSnapshot|form\.Set\("urls"' "${qb_effect_runtime[@]}"; then
  echo "Production qB effect path contains a forbidden URL/magnet submission fallback" >&2
  exit 1
fi

# Retired product and transitional names must not return; the allowlisted
# exceptions live in the guard script.
if ! legacy_names="$("$root/scripts/ci/check-legacy-names.sh" --strict 2>&1)"; then
  echo "$legacy_names" >&2
  echo "Retired product names returned (scripts/ci/check-legacy-names.sh --list <family>)" >&2
  exit 1
fi

echo "Boundary check passed"

# P0.6: maintenance write-entry inventory gate (OpenAPI allowlist ↔ middleware).
"$root/scripts/ci/check-write-gate.sh"

# P0.7: production compose must stay immutable — no build:, no mutable app
# tags, no source mounts, no DB-ping healthcheck as admission signal.
prod_compose="$root/deployment/maintainer/docker-compose.production.yml"
if rg -n '^\s+build:|image: bansoko:[a-z0-9._-]+$|\./|/Users/|system/health' "$prod_compose" | grep -v 'deployment/public.key' | grep -v 'system/health\]' | grep -v 'not be gated on DB-ping /system/health'; then
  echo "Production compose violates immutable bundle constraints" >&2
  exit 1
fi
if ! rg -q 'api/v1/livez' "$prod_compose"; then
  echo "Production compose must use /api/v1/livez healthcheck" >&2
  exit 1
fi

# P1.1: the old single-lane RunOnce loop must not return to composition roots.
# The lane-based Runtime (heartbeat/policy/drain) is the only execution path.
if rg -n '\.RunOnce\(' "$root/cmd" --glob '*.go' --glob '!**/*_test.go'; then
  echo "Composition root must not use the retired single-lane Worker.RunOnce" >&2
  exit 1
fi

# P3.1: the synchronous reconciliation Scan/Apply paths are deleted; the
# async snapshot provider is the only shape.
if rg -n 'ReconciliationApply|scanReconciliation|applyReconciliation|reconciliation/apply|reconciliation/scan"' "$root/cmd" "$root/internal" --glob '*.go' --glob '!**/generated/**' --glob '!**/migrations/**'; then
  echo "Synchronous reconciliation Scan/Apply paths must not return" >&2
  exit 1
fi

# OFFSET-SCOPE: the episode offset belongs only to an RSS link row or a
# one-shot request. The anime-level default offset and the dual
# instance/source offset inputs are gone; the single-offset input flattening
# (PRUNE-046 S4) removed the last frozen readers, so the names survive only in
# tests that assert the retired key is never read or accepted.
if (cd "$root" && rg -n -e 'default_offset' -e 'ParserDefaultOffset' -e 'InstanceOffset' \
  internal cmd api frontend/src frontend/e2e scripts deployment \
  --glob '!**/generated/**' \
  --glob '!internal/runtime/migrations/*_test.go' \
  --glob '!internal/acquisition/terminal/domain/registry_input.go' \
  --glob '!internal/acquisition/terminal/domain/testdata/**' \
  --glob '!internal/artifact/application/frozen_parser_selection_test.go' \
  --glob '!internal/acquisition/terminal/application/qualified_accept_postgres_test.go' \
  --glob '!scripts/ci/check-boundary.sh'); then
  echo "The anime-level default offset and the instance/source offset pair must not return outside migration history" >&2
  exit 1
fi

# P2.2: production Compose must be digest-only (no build:, no mutable tags,
# migration entrypoint present) — the supervisor only switches immutable
# signed bundles. The contract runner invokes this gate explicitly after the
# boundary checks so it is not run twice in that lane.
if [[ "${BANSOKO_CONTRACT_BOUNDARY_SKIP_COMPOSE:-0}" != "1" ]] && ! "$root/scripts/ci/check-production-compose.sh" >/dev/null 2>&1; then
  echo "Production Compose is not digest-only; update capability stays unavailable" >&2
  exit 1
fi
