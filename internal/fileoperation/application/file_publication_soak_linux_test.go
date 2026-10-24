//go:build linux && soak

package application

import (
	"fmt"
	"testing"

	"bansoko/internal/library/domain"
)

// B5: per-write-point unknown-commit/rollback expansions and the overwrite
// competition matrix run in the soak lane (scripts/ci/test-soak.sh);
// the canonical lane keeps the single-chain proofs. Moved to keep the
// fileoperation/application package inside its 900s budget.

// The cleanup probe fires right before the unlink. Only a move still has a
// present object to clean after publication (the staged temp was renamed onto
// the target), so the interruption matrix runs over move.
func TestCleanupEveryWriteReceiptPersistsUnknown(t *testing.T) {
	for _, point := range []string{"before_object_purge"} {
		t.Run("move/"+point, func(t *testing.T) { runPublicationProofChain(t, domain.OperationMove, "cleanup_fault:"+point) })
	}
}

func TestAllCopyMoveStepUnknownCommits(t *testing.T) {
	for _, operation := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
		for _, collision := range []domain.CollisionPolicy{domain.CollisionBlock, domain.CollisionVersion} {
			if operation == domain.OperationHardlink && collision == domain.CollisionBlock {
				continue
			}
			count := 3
			if collision == domain.CollisionVersion {
				count++
			}
			if operation == domain.OperationMove {
				count++
			}
			for _, needle := range []string{"file_start_step(", "file_record_receipt("} {
				max := count
				if needle == "file_record_receipt(" {
					max++
				}
				for n := 1; n <= max; n++ {
					for _, rollback := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/%s/%d/rollback=%v", operation, collision, needle, n, rollback), func(t *testing.T) { runPublicationProofChain(t, operation, needle, rollback, int32(n), collision) })
					}
				}
			}
		}
	}
}

func TestVersionCleanupEveryWriteReceiptPersistsUnknown(t *testing.T) {
	for _, point := range []string{"before_object_purge"} {
		t.Run("move/"+point, func(t *testing.T) {
			runPublicationProofChain(t, domain.OperationMove, "cleanup_fault:"+point, domain.CollisionVersion)
		})
	}
}

func TestFinishWritePointRollbackMatrix(t *testing.T) {
	// Per-write-point fault: the entire atomic write set rolls
	// back atomically (no half-write / no partial commit) with the real proof
	// preserved. Classification: apply-phase & clean commit-rollback
	// => ErrFinishNotCommitted (retry-safe, same fixed entry, no new grant);
	// committed-but-ack-lost => idempotent success; unreadable ledger => ambiguous,
	// a plain error that never wraps ErrFinishNotCommitted, zero-write.
	runPublicationProofChain(t, domain.OperationCopy, "finish_rollback")
}

func TestFinishWritePointRollbackMatrixCancelled(t *testing.T) {
	// The same per-write-point matrix over the cancel-won recovery lane
	// (file_finish_cancelled_success; suppression successor shape).
	runPublicationProofChain(t, domain.OperationCopy, "finish_rollback_cancel")
}

// TestOverwriteExchangeCompetitionMatrix is the overwrite exchange matrix
// (overwrite/displaced, path-level):
// both sequences, 50 real rounds each, no automatic reruns. Each round runs
// the REAL chain (attempt → prepare → admission → single invocation →
// receipts → proof attempt) against a REAL occupied target, and a real
// competing rename occupies the window right before the publishing rename. The hook is the barrier:
// it fires inside the adapter's own syscall path, so every round is a genuine
// interleaving, not a sleeps-based race. Any semantic FAIL stops the run; a
// failure is never retried inside the loop.
func TestOverwriteExchangeCompetitionMatrix(t *testing.T) {
	for sequence, fault := range map[string]any{
		"ow_frozen_changed":   "ow_frozen_changed",
		"ow_exchange_foreign": "ow_exchange_foreign",
	} {
		t.Run(sequence, func(t *testing.T) {
			for round := 1; round <= overwriteCompetitionRounds; round++ {
				t.Run(fmt.Sprintf("round-%02d", round), func(t *testing.T) {
					runPublicationProofChain(t, domain.OperationCopy, fault)
				})
			}
		})
	}
}
