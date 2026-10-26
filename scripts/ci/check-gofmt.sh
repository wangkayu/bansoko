#!/usr/bin/env bash
# Fail the build on gofmt drift over the tracked Go tree.
#
# Formatting is not enforced by `go build`/`go vet`, so an unformatted file is
# invisible until the next unrelated reformat pass mixes style churn into a
# behavioural diff. This gate makes drift red at review time instead.
#
# Generated files are excluded on purpose:
#   */generated/*.go   sqlc output — regeneration, not editing, owns the bytes
#                      (scripts/generators/generate-*-persistence.sh --check);
#   *.gen.go           oapi-codegen output (api/codegen.yaml);
#   wire_gen.go        google/wire output.
# A formatter pass over them would be reverted by the next `--check` /
# `go generate`, so the rule is: fix the generator, not the artefact.
#
# The formatter is taken from the toolchain the go command selects for this
# go.mod (GOTOOLCHAIN), not from PATH: a gofmt built from a different Go version
# can disagree on layout, and a gate that flags formatting that its own
# toolchain produced is unactionable. GOFMT_BIN overrides this.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

go_bin="$(command -v go || true)"
if [[ -z "$go_bin" ]]; then
  echo "go not found on PATH (install the toolchain that go.mod requires)" >&2
  exit 2
fi

gofmt_bin="${GOFMT_BIN:-}"
if [[ -z "$gofmt_bin" ]]; then
  goroot="$("$go_bin" env GOROOT)"
  gofmt_bin="$goroot/bin/gofmt"
  if [[ ! -x "$gofmt_bin" ]]; then
    gofmt_bin="$(command -v gofmt || true)"
  fi
fi
if [[ -z "$gofmt_bin" || ! -x "$gofmt_bin" ]]; then
  echo "no gofmt binary found (looked in GOROOT/bin and PATH; set GOFMT_BIN)" >&2
  exit 2
fi

echo "== gofmt ($("$go_bin" version), $gofmt_bin) =="

# bash 3.2 (macOS) has no mapfile; keep the collection loop portable.
files=()
while IFS= read -r file; do
  files+=("$file")
done < <(git ls-files -- '*.go' | grep -vE '(/generated/|\.gen\.go$|(^|/)wire_gen\.go$)' || true)
if [[ "${#files[@]}" -eq 0 ]]; then
  echo "no tracked Go files to format" >&2
  exit 1
fi
status=0
while IFS= read -r file; do
  [[ -z "$file" ]] && continue
  echo "needs gofmt: $file" >&2
  status=1
done < <("$gofmt_bin" -l "${files[@]}")

if [[ "$status" -ne 0 ]]; then
  echo "gofmt drift detected; run '$gofmt_bin -w' on the files above" >&2
  exit 1
fi

echo "gofmt clean: ${#files[@]} tracked Go files"
