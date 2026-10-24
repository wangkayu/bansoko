#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
fixture="$root/scripts/ci/embedded-update-docker-fixture.sh"
matrix="$root/scripts/verify/embedded-crash-matrix.sh"
bash -n "$fixture" "$matrix"
"$matrix" --self-test >/dev/null
[[ "$("$matrix" --list | wc -l | tr -d '[:space:]')" == 9 ]]
expected="$( {
  for scenario in staging marker-before-backup after-backup migrate; do printf 'disposable|crash-%s|resume|none|container\n' "$scenario"; done
  printf 'disposable|daemon-restart-bootstrap|resume|none|daemon\n'
  for scenario in normal migrate-failure readiness-failure rollback-blocked; do printf 'regression|%s|resume|none|container\n' "$scenario"; done
} | sort)"
[[ "$("$matrix" --list | sort)" == "$expected" ]]
for contract in 'pending.json' 'operation_id' 'release_id' 'manifest_digest' 'release_sequence' 'previous_release_id' 'resources_clean' 'BANSOKO_EMBEDDED_FIXTURE_STOP_BEFORE_MARKER' 'BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER' 'BANSOKO_EMBEDDED_FIXTURE_FAIL_READINESS'; do
  grep -q "$contract" "$fixture" || { echo "fixture contract missing: $contract" >&2; exit 1; }
done
! grep -q 'recover status' "$fixture"
! grep -q 'resume_or_abort' "$fixture"
! grep -q -- '--decision-probe' "$fixture"
if BANSOKO_EMBEDDED_E2E=1 "$fixture" --self-test >/dev/null 2>&1; then echo "fixture --self-test must refuse" >&2; exit 1; fi
if BANSOKO_CRASH_MATRIX=1 BANSOKO_EMBEDDED_E2E=1 BANSOKO_ENVIRONMENT=production "$matrix" >/dev/null 2>&1; then echo "matrix must refuse production" >&2; exit 1; fi
python3 - "$matrix" "$fixture" <<'PY'
import pathlib,re,sys
matrix,fixture=(pathlib.Path(x).read_text() for x in sys.argv[1:])
required=['"evidence_kind":"real_docker"','"self_test":False','image_cleanup','source_identity','source_commit','source_tree_hash','image_id','resources_clean','pending.json','release_sequence','previous_release_id']
missing=[item for item in required if item not in matrix+fixture]
if missing: raise SystemExit(f"static fixture contract missing: {missing}")
if re.search(r'cp\s+.*(?:journal|\.log).*evidence',matrix,re.I): raise SystemExit("raw evidence copy detected")
print('{"kind":"embedded-update-fixture-static-self-test","passed":true,"real_evidence":false}')
PY
