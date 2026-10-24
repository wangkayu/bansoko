package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"errors"
	"time"
)

// finishPreparedFileSuccess is a fixed internal caller wired into the
// runtime worker entry below: the claim comes only from the WorkItem store,
// the prepared result is captured read-only from persistent proof, and no
// caller-provided writer or arbitrary action can enter this transaction.
func (s *Service) finishPreparedFileSuccess(ctx context.Context, c workitems.Claim, p preparedFileResult) (worker.Outcome, error) {
	// The fixed entry is the ONLY runtime-callable finish surface. Both the
	// owner lane (tests/ops) and the runtime lane go through the same wrapper —
	// the wrapper performs no write of its own and delegates to the exact
	// private command, so the atomic body's write set and the single-writer
	// property are unchanged for either lane.
	command := "SELECT public.file_finish_runtime_entry($1,$2,$3,$4,$5,$6,$7)"
	cancelled := p.recoveryClaim && c.CancelRequested
	var reply struct {
		State   string `json:"state"`
		Version int64  `json:"owner_version"`
	}
	err := s.workItems.WithFileFinalizationTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		tx := scope.SQLTx()
		if err := verifyPreparedFileResultTx(ctx, tx, c, p); err != nil {
			return "", err
		}
		var raw []byte
		if err := tx.QueryRowContext(ctx, command, c.Owner.ID, c.ID, c.FencingToken, p.permit, p.fileFactCanonical, p.notificationPayload, cancelled).Scan(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		if reply.State != "completed" || reply.Version != c.ExpectedOwnerVersion+1 {
			return "", fmt.Errorf("FILE_FINISH_REPLY_INVALID")
		}
		// The placement handoff (obligation + typed continuation
		// WorkItem) is part of the finish success write set and commits in THIS
		// transaction, after the fixed command terminalized the claimed work
		// (so the one-live-item invariant admits the continuation) and before
		// Commit. Any failure — including ErrOneShotReclaiming from the
		// marker re-check inside createPlacementObligationTx — rolls back the
		// whole finish with zero writes. Same for the cancelled_success lane.
		// No sink wired means the stage has no owner (unchanged legacy rule).
		if s.placementSink != nil {
			var effectKey string
			if err := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM file_operations WHERE id=$1`, c.Owner.ID).Scan(&effectKey); err != nil {
				return "", fmt.Errorf("read completed file operation for placement obligation: %w", err)
			}
			if _, err := s.createPlacementObligationTx(ctx, tx, c.Owner.ID, reply.Version, effectKey, c.OneShot); err != nil {
				return "", err
			}
		}
		// Activity belongs to the independent consumer, not the finish write set.
		return workitems.DispositionConfirmed, nil
	})
	if err != nil {
		// A driver-boundary error leaving this transaction is
		// outcome-ambiguous (the server may have committed or rolled back). The
		// durable business ledger is re-read on an independent query path: the
		// terminal operation state, its notification admission and the succeeded
		// work are the last writes of the atomic finish body, so their joint
		// presence proves the commit landed (single-transaction atomicity
		// excludes partial ledgers). Full ledger ⇒ the outcome is replayed
		// idempotently with no repeated write; empty/partial ledger ⇒ PG
		// atomicity guarantees a clean rollback and the error is wrapped
		// ErrFinishNotCommitted (safe to retry through the same fixed entry,
		// no new grant is involved); an unreadable ledger is surfaced as an
		// ambiguous failure — never blindly replayed, never retried in-process.
		committed, confirmErr := s.confirmFinishCommitted(ctx, c)
		if confirmErr != nil {
			return worker.Outcome{}, fmt.Errorf("file finish commit outcome unverifiable: %w (original error: %w)", confirmErr, err)
		}
		if committed {
			out := worker.Committed(c, workitems.OwnerResult{State: "completed"})
			out.Disposition = workitems.DispositionConfirmed
			return out, nil
		}
		return worker.Outcome{}, fmt.Errorf("%w: %w", ErrFinishNotCommitted, err)
	}
	out := worker.Committed(c, workitems.OwnerResult{State: reply.State})
	out.Disposition = workitems.DispositionConfirmed
	return out, nil
}

// ErrFinishNotCommitted marks a finish attempt whose transaction rolled back
// cleanly (verified against the durable ledger): retrying the same fixed
// entry is safe and involves no new grant.
var ErrFinishNotCommitted = errors.New("file finish transaction not committed")

// confirmFinishCommitted re-reads the durable full ledger for this finish:
// terminal operation state, its notification admission and the succeeded
// work. Their joint presence is sufficient proof of the commit; anything else
// (including an unreadable ledger, reported as an error) leaves the outcome
// undecided for the caller.
func (s *Service) confirmFinishCommitted(ctx context.Context, c workitems.Claim) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT
	 EXISTS(SELECT 1 FROM public.file_operations WHERE id=$1 AND state='completed')
	 AND EXISTS(SELECT 1 FROM public.notification_admissions WHERE source_owner_type='file_operation' AND source_owner_id=$1)
	 AND EXISTS(SELECT 1 FROM public.work_item_outcomes WHERE id=$2 AND owner_id=$1 AND kind='file_execute' AND state='succeeded')`,
		c.Owner.ID, c.ID).Scan(&ok)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// handleTypedExecution is the runtime worker entry for typed
// file operations. It is the ONLY handler lane allowed to reach the fixed
// finish: the claim arrives from the WorkItem store (never an HTTP input), the
// prepared result is an in-process read-only capture of the persistent
// publication proof (never a caller-supplied success flag), and the finish
// transaction re-verifies the complete proof under its root-first lock set
// before writing any business result. The finish command
// stays runtime-REVOKEed: only this trusted fixed caller reaches it.
//
// This lane adds NO writer to the typed
// three-fact graph: every business write (projection, handoff/suppression,
// operations terminal, admission, work terminal, settlement) remains inside
// the single file_finish_success/cancelled_success command transaction;
// recovery observation stays read-only (the finish re-locks and re-validates).
//
// Recovery claims (BlockRecovery/ClaimRecovery after cancel) carry the cancel
// marker and route to file_finish_cancelled_success through the same
// fixed entry — the suppression semantics are owned by the SQL body.
func (s *Service) handleTypedExecution(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	operation, err := s.loadOperation(ctx, claim.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if err := validateClaimInput(claim, operation); err != nil {
		return s.finishTypedFailure(ctx, claim, "file_operation_input_invalid", err)
	}
	if operation.state == ownerStateCompleted || operation.state == ownerStateFailed {
		// A terminal operation never starts a new attempt; replays are zero-write.
		return typedExecutionBlocked(), nil
	}
	return s.convergeTypedExecution(ctx, claim, operation, true)
}

// convergeTypedExecution decides the typed lane from persistent evidence only
//
// operation, so the evidence row is the operation's whole execution history:
//   - closed/confirmed proof: finish through the fixed entry, whichever
//     token of the same work arrives (the original, a later re-claim, or the
//     recovery claim) — a proven effect is completed, never failed or redone;
//   - evidence that is not closed: a previous attempt was interrupted after
//     its permit; the effect is never redone and the operation ends visibly
//     (FILE_EXECUTION_INTERRUPTED, effect class from the evidence, kept);
//   - no evidence: a normal claim runs the typed chain; a cancelled or
//     recovery claim may not start an effect and ends visibly instead.
func (s *Service) convergeTypedExecution(ctx context.Context, claim workitems.Claim, operation operationRecord, mayStart bool) (worker.Outcome, error) {
	var permit string
	var closed bool
	err := s.db.QueryRowContext(ctx, `SELECT publication_permit_id, publication_dispatch_state='closed' AND publication_effect_class='confirmed' AND publication_conflict_evidence IS NULL AND publication_resolution_canonical IS NOT NULL FROM public.file_operations WHERE id=$1 AND publication_work_item_id=$2 AND publication_permit_id IS NOT NULL ORDER BY publication_fencing_token DESC LIMIT 1`, claim.Owner.ID, claim.ID).Scan(&permit, &closed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return worker.Outcome{}, err
	}
	exists := err == nil
	if closed {
		prepared, err := s.prepareTypedFileResult(ctx, claim, permit)
		if err != nil {
			return s.typedFinishBlockedOutcome(ctx, claim, operation, err)
		}
		out, err := s.finishPreparedFileSuccess(ctx, claim, prepared)
		if err != nil {
			return s.typedFinishBlockedOutcome(ctx, claim, operation, err)
		}
		return out, nil
	}
	if exists {
		return s.finishTypedFailure(ctx, claim, "FILE_EXECUTION_INTERRUPTED", errors.New("a previous attempt left publication evidence that is not closed; the effect is not redone"))
	}
	if claim.CancelRequested {
		return s.finishTypedFailure(ctx, claim, "FILE_EXECUTION_CANCELLED", errors.New("cancelled before any publication"))
	}
	if !mayStart {
		return s.finishTypedFailure(ctx, claim, "FILE_EXECUTION_INTERRUPTED", errors.New("claim budget exhausted before any publication"))
	}
	return s.runTypedPublication(ctx, claim, operation)
}

// prepareTypedFileResult captures the read-only prepared result: ordinary
// claims capture the original identity; cancel-won recovery claims capture
// through the recovery lane (which re-derives the original work/token/version
// and marks recoveryClaim so finishPreparedFileSuccess routes to the
// cancelled command). The capture authorizes no I/O and no finish.
func (s *Service) prepareTypedFileResult(ctx context.Context, claim workitems.Claim, permit string) (preparedFileResult, error) {
	var token int64
	if err := s.db.QueryRowContext(ctx, `SELECT publication_fencing_token FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&token); err != nil {
		return preparedFileResult{}, err
	}
	if claim.CancelRequested || claim.FencingToken > token {
		return s.prepareRecoveredSuccessfulFileResult(ctx, claim, permit)
	}
	return s.prepareSuccessfulFileResult(ctx, claim, permit)
}

// publicationPermitForClaim resolves the operation's publication permit (the
// evidence row id bound to this original work). A missing permit fails the
// finish gates fail-closed; it never fabricates one.
func (s *Service) publicationPermitForClaim(ctx context.Context, claim workitems.Claim) (string, error) {
	var permit string
	if err := s.db.QueryRowContext(ctx, `SELECT publication_permit_id FROM public.file_operations WHERE publication_work_item_id=$1 AND id=$2`, claim.ID, claim.Owner.ID).Scan(&permit); err != nil {
		return "", err
	}
	return permit, nil
}

// typedResultIfFinished captures the read-only prepared result when the
// operation's publication proof is already closed/confirmed — the claim goes
// straight to the fixed finish (idempotent full-ledger confirm). An
// absent or not-yet-closed proof returns an empty result: the typed chain
// owns the execution phases instead. This is a state probe, never a skip of
// the finish gates — the finish re-locks and re-validates everything.
func (s *Service) typedResultIfFinished(ctx context.Context, claim workitems.Claim) (preparedFileResult, error) {
	permit, permitErr := s.publicationPermitForClaim(ctx, claim)
	if permitErr != nil {
		return preparedFileResult{}, permitErr
	}
	var closed bool
	if err := s.db.QueryRowContext(ctx, `SELECT publication_dispatch_state='closed' AND publication_effect_class='confirmed' AND publication_conflict_evidence IS NULL AND publication_resolution_canonical IS NOT NULL FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&closed); err != nil {
		return preparedFileResult{}, err
	}
	if !closed {
		return preparedFileResult{}, errPublicationNotClosed
	}
	return s.prepareTypedFileResult(ctx, claim, permit)
}

// errPublicationNotClosed marks a permit whose evidence has not reached the
// closed/confirmed terminal proof shape.
var errPublicationNotClosed = errors.New("publication proof not closed")

// typedFinishBlockedOutcome maps a failed read-only result capture to the
// bounded product outcomes: an unavailable proof is not a failure of the
// effect — it keeps the risk visible instead of inventing a terminal state.
func (s *Service) typedFinishBlockedOutcome(ctx context.Context, claim workitems.Claim, operation operationRecord, cause error) (worker.Outcome, error) {
	// A finish that cleanly rolled back (ErrFinishNotCommitted) or a transient
	// capture error is retried: a closed proof must complete, never fail.
	if errors.Is(cause, errPublicationNotClosed) || errors.Is(cause, ErrFinishNotCommitted) || isRetryable(cause) {
		return worker.Retry("file_proof_capture_transient", safeReason(cause), defaultRetryAfter, nil), nil
	}
	return s.finishTypedFailure(ctx, claim, "FILE_SUCCESS_PROOF_UNAVAILABLE", cause)
}

// observeTypedRecovery is the runtime recovery observation for typed
// operations. Recovery observes the exact frozen target read-only — the
// cancel-won cancelled-finish owns every business write under its own
// gate, so this observation never classifies the external effect and never
// terminates the item. The worker keeps the claim leased; the recovery
// protocol (BlockRecovery/ClaimRecovery) re-drives the fixed entry.
func (s *Service) observeTypedRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	operation, err := s.loadOperation(ctx, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if operation.state == ownerStateCompleted || operation.state == ownerStateFailed {
		return worker.AwaitRecovery(""), nil
	}
	// The recovery claim converges exactly like a re-claim but never starts
	// an effect: a closed proof finishes, anything else ends visibly.
	return s.convergeTypedExecution(ctx, c, operation, false)
}

// typedPublicationOperation reports whether the frozen operation kind is
func typedPublicationOperation(kind domain.OperationKind) bool {
	return kind == domain.OperationCopy || kind == domain.OperationMove || kind == domain.OperationHardlink
}
