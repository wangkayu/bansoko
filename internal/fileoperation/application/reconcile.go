package application

import (
	"context"
	"fmt"

	"bansoko/internal/deployment"
)

// ReconcileUnknownEffect classifies FileOperation external effects after a
// deployment rollback. Every operation runs the typed publication lane, whose
// interrupted attempts are settled only by the ticketed recovery lane from
// persistent publication evidence. Deployment reconcile is therefore
// read-only: any operation still pending or running blocks the gate with an
// unknown effect; it never observes the target or writes the owner.
func (s *Service) ReconcileUnknownEffect(ctx context.Context, _ deployment.ReconcileRequest) (deployment.ReconcileResult, error) {
	var open int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_operations WHERE state IN ('pending', 'running')`).Scan(&open); err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
			Reason: safeReason(fmt.Errorf("scan file operations for recovery: %w", err))}, nil
	}
	if open > 0 {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, Reason: errTypedExecutionPending.Error()}, nil
	}
	return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: deployment.EffectNone,
		Reason: "settled 0 file operations"}, nil
}
