package application

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"strings"
)

// ObserveRecovery only reads the exact frozen target. No executor, fallback
// execution, rename, delete, plan preparation or naming evaluation is allowed.
func (s *Service) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if ctx == nil || s == nil || s.observer == nil || c.Kind != workitems.KindFileExecute || c.Owner.Type != workitems.OwnerFileOperation || strings.TrimSpace(c.Owner.ID) == "" {
		return worker.Outcome{}, ErrInvalidClaim
	}
	// Typed operations own their recovery through the read-only observation
	// path: the observation itself writes nothing; the cancel-won finish
	// re-locks and re-validates everything under its own gate.
	if _, class := classifyPlacementContinuation(c); class != continuationNormal {
		return s.observePlacementRecovery(ctx, c, class)
	}
	return s.observeTypedRecovery(ctx, c)
}

func (s *Service) observePlacementRecovery(ctx context.Context, c workitems.Claim, class int) (worker.Outcome, error) {
	if class == continuationMalformed {
		o, err := s.handleMalformedPlacementContinuation(ctx, c)
		o.Disposition = workitems.DispositionNoExternalEffect
		return o, err
	}
	op, err := s.loadOperation(ctx, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if op.state != ownerStateCompleted {
		return worker.AwaitRecovery(""), nil
	}
	obligation, found, err := s.loadPlacementObligation(ctx, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if !found {
		return worker.AwaitRecovery(""), nil
	}
	if obligation.state == placementObligationAdmitted {
		o := worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: placementObligationAdmitted, ReasonCode: "placement_recovery_admitted"}, s.placementObligationWriter(c))
		o.Disposition = workitems.DispositionConfirmed
		return o, nil
	}
	// A pending obligation may have crashed after placement/trigger committed.
	// Adopt only that persisted handoff; never invoke the unfenced admission sink.
	proven, err := s.placementHandoffCommitted(ctx, s.db, c)
	if err != nil {
		return worker.Outcome{}, err
	}
	if !proven {
		return worker.AwaitRecovery(""), nil
	}
	o := worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: placementObligationAdmitted, ReasonCode: "placement_recovery_committed"}, func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		ok, err := s.placementHandoffCommitted(ctx, scope.SQLTx(), c)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, workitems.ErrStaleClaim
		}
		return s.placementObligationWriter(c)(ctx, scope, result)
	})
	o.Disposition = workitems.DispositionConfirmed
	return o, nil
}

func (h PostProcessingPlanHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindPostProcessPlan || c.Owner.Type != workitems.OwnerPostProcessingPlan {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	return h.service.observePlanRecovery(ctx, c)
}
