package application

import (
	"context"
	"database/sql"
	"fmt"

	"bansoko/internal/deployment"
)

// ReconcileUnknownEffect only classifies durable submission evidence. Deployment
// recovery never observes qB or settles an owner: unknown permits are handed to
// the ticketed worker.
func (s *Service) ReconcileUnknownEffect(ctx context.Context, _ deployment.ReconcileRequest) (deployment.ReconcileResult, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT t.id, t.state, t.submit_permit_id, t.submit_dispatch_started_at, t.submit_effect_class, t.submit_dispatch_state
FROM download_tasks t
WHERE t.state IN ('queued', 'submitting')
ORDER BY t.id COLLATE "C"`)
	if err != nil {
		return deployment.ReconcileResult{}, fmt.Errorf("read submission recovery evidence: %w", err)
	}
	defer rows.Close()
	result := deployment.ReconcileResult{State: deployment.StateCompleted, Effect: deployment.EffectNone,
		Reason: "submission evidence classified; worker owns ticketed reconciliation"}
	for rows.Next() {
		var id, state string
		var permit, effect, dispatch sql.NullString
		var started sql.NullTime
		if err := rows.Scan(&id, &state, &permit, &started, &effect, &dispatch); err != nil {
			return deployment.ReconcileResult{}, err
		}
		if !permit.Valid {
			if state == "submitting" {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown,
					Reason: "submitting task has no submission evidence; rebuild or operator intervention required", EvidenceRef: id}, nil
			}
			continue // queued, with no dispatch authority issued
		}
		if !started.Valid {
			continue
		}
		if effect.String == "confirmed" || dispatch.String == "closed" {
			if result.Effect != deployment.EffectUnknown {
				result.Effect = deployment.EffectPresent
			}
			continue
		}
		if effect.String == "unknown" {
			result.Effect = deployment.EffectUnknown
			if result.EvidenceRef == "" {
				result.EvidenceRef = permit.String // stable representative; worker scans all retained permits
			}
			continue
		}
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown,
			Reason: "submission evidence cannot be explained; rebuild or operator intervention required", EvidenceRef: permit.String}, nil
	}
	return result, rows.Err()
}
