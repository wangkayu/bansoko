#!/usr/bin/env bash
# Serial no-skip acceptance of owned recovery/publication protocols.
# Does not provision, migrate, deploy or destroy a database.
set -euo pipefail
cd "$(dirname "$0")/../.."
bash scripts/ci/require-disposable-db.sh
mode="${1:-all}"
case "$mode" in
  publication|all)
    bash scripts/ci/go-test-no-skips.sh publication-protocol -p 1 ./internal/workitems \
      -run '^(TestPGPublicationCancelBeforeBeginRejectsNewEffect|TestPGPublicationReceiptSurvivesCancelAndExpiredLease|TestPGPublicationCrashPermitBlocksGenerationAndUnknownSettlement|TestPGPublicationBeginBarrierSerializesCancelAndReplan|TestPGPublicationLostReceiptReconcilesOriginalPermitAndLedger|TestPGPublicationConflictingArtifactReplanRejectsWithoutDeadlock)$' \
      -count=1 -timeout=900s
    ;;
  recovery) ;;
  *) echo 'usage: verify-worker-recovery-publication.sh [all|publication|recovery]' >&2; exit 2 ;;
esac
if [[ "$mode" != publication ]]; then
  bash scripts/ci/go-test-no-skips.sh worker-owner-recovery -p 1 \
    ./internal/workitems ./internal/runtime/worker \
    ./internal/rss/application ./internal/artifact/application \
    ./internal/download/application ./internal/fileoperation/application \
    ./internal/reconciliation/application ./internal/filemaintenance \
    -run '^(TestPGAutomatic.*|TestDispatcherAutomaticallyObservesBlockedWorkWithoutBusinessHandle|TestPGReleaseRecoveryDoesNotReadmitOrFetch|TestPGVerifiedArtifactRecoveryRepairsHandoffWithoutInspection|TestPGQBSyncRecoveryMissingFrozenIdentityRemainsBlocked|TestPGFileRecoveryObservesFrozenTargetWithoutExecutor|TestPGScanRecoveryRejectsChangedGenerationAtomically|TestPGCalibrationRecoveryMissingCommitProofRemainsBlocked|TestPGPendingPlacementRecoveryWithoutPhaseProofNeverCallsSink|TestPGPendingPlacementRecoveryRechecksCommittedIdentity|TestPGRecoveryStaleFenceRollsBackOneShotLedgerConsumption|TestPGPlacementRecoveryMissingTriggerAdmitsOnceUnderConcurrentSettlement|TestPGPlacementRecoveryTriggerAndObligationRollbackTogether|TestPGPlacementRecoverySinkRejectsStaleClaim)$' \
    -count=1 -timeout=900s
  bash scripts/ci/go-test-no-skips.sh worker-lifecycle-full -p 1 \
    ./internal/runtime/worker ./internal/workitems -count=1 -timeout=900s
fi
