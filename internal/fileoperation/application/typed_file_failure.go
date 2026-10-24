package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"github.com/jackc/pgx/v5/pgconn"
)

// finishTypedFailure is the fixed typed explicit-failure terminal (B5, 184).
// A typed failure goes through the fixed command, which fails the operation, records the delivery result with the
// effect class derived from persisted evidence, fails not-yet-started
// descendants (DEPENDENCY_FAILED) and terminalizes the work in one transaction.
// No placement obligation is created and no file is touched.
func (s *Service) finishTypedFailure(ctx context.Context, c workitems.Claim, code string, cause error) (worker.Outcome, error) {
	code = boundedCode(code, "file_operation_failed")
	reason := boundedReason(safeReason(cause), 1024)
	var reply struct {
		State  string `json:"state"`
		Effect string `json:"effect_class"`
	}
	disposition := workitems.DispositionAmbiguous
	err := s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.file_finish_failed($1,$2,$3,$4,$5)`, c.Owner.ID, c.ID, c.FencingToken, code, reason).Scan(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		if reply.State != ownerStateFailed {
			return "", fmt.Errorf("FILE_FAILURE_REPLY_INVALID")
		}
		// The one-shot admission settles only on a known effect class; an
		// unknown effect keeps it open so the risk stays visible.
		switch reply.Effect {
		case "no_external_effect":
			disposition = workitems.DispositionNoExternalEffect
		case "confirmed":
			disposition = workitems.DispositionConfirmed
		default:
			disposition = workitems.DispositionAmbiguous
		}
		return disposition, nil
	})
	if err != nil {
		// Commit outcome unknown: the operation and its work failed together
		// or not at all; re-read independently before reporting.
		if committed, confirmErr := s.confirmTypedFailureCommitted(ctx, c); confirmErr == nil && committed {
			out := worker.Committed(c, workitems.OwnerResult{State: ownerStateFailed, ReasonCode: code, Reason: reason})
			out.Disposition = workitems.DispositionAmbiguous
			return out, nil
		}
		return worker.Outcome{}, fmt.Errorf("typed file failure %s not committed: %w", code, err)
	}
	out := worker.Committed(c, workitems.OwnerResult{State: ownerStateFailed, ReasonCode: code, Reason: reason})
	out.Disposition = disposition
	return worker.WithAfterCommit(out, observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger, observabilityport.DiagnosticFailureRequest{
		Identity: observabilityport.FileOperationFailure(), Severity: observabilityport.SeverityError,
		ScopeID: c.Owner.ID, LastError: reason,
	})), nil
}

func (s *Service) confirmTypedFailureCommitted(ctx context.Context, c workitems.Claim) (bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.file_operations WHERE id=$1 AND state='failed')
	 AND EXISTS(SELECT 1 FROM public.work_item_outcomes WHERE id=$2 AND owner_id=$1 AND state='failed')`, c.Owner.ID, c.ID).Scan(&ok)
	return ok, err
}

// typedAttemptFailureCode maps a fixed-primitive refusal of a new file attempt
// to its terminal code, or "" when the refusal is not terminal (stale claim,
// dependency not ready — the claim is left to its normal lease semantics).
func typedAttemptFailureCode(err error) string {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" {
		return ""
	}
	switch pg.Message {
	case "FILE_BUDGET_EXHAUSTED":
		return "FILE_ATTEMPT_BUDGET_EXHAUSTED"
	case "FILE_UNRESOLVED_EFFECT":
		// A previous attempt of this operation left evidence that is not
		// closed: never redo the effect; end visibly with the evidence kept.
		return "FILE_EXECUTION_INTERRUPTED"
	}
	return ""
}
