package application

import (
	"context"
	"fmt"

	"bansoko/internal/deployment"
)

// ReconcileUnknownEffect classifies durable inspection obligations only. Disk
// observation and every owner transition require a real inspection claim.
func (s *Service) ReconcileUnknownEffect(ctx context.Context, _ deployment.ReconcileRequest) (deployment.ReconcileResult, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.state,
 COALESCE((SELECT w.id FROM work_items w WHERE w.owner_type='artifact' AND w.owner_id=a.id AND w.kind='artifact_inspect' AND w.state IN ('available','leased') ORDER BY w.id COLLATE "C" LIMIT 1),''),
 EXISTS(SELECT 1 FROM acquisitions m WHERE m.id=a.acquisition_id AND m.manifest_digest IS NOT NULL),
 (SELECT count(*) FROM postprocessing_plans p WHERE p.artifact_id=a.id AND p.acquisition_id=a.acquisition_id),
 EXISTS(SELECT 1 FROM postprocessing_plans p WHERE p.artifact_id=a.id) OR EXISTS(SELECT 1 FROM file_operations f WHERE f.artifact_id=a.id) OR EXISTS(SELECT 1 FROM artifact_files f WHERE f.artifact_id=a.id AND f.acquisition_id IS DISTINCT FROM a.acquisition_id)
 FROM artifacts a ORDER BY a.id COLLATE "C"`)
	if err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: safeFailureReason(err)}, nil
	}
	defer rows.Close()
	ref := ""
	for rows.Next() {
		var id, state, work string
		var manifest bool
		var plans int
		var downstream bool
		if err := rows.Scan(&id, &state, &work, &manifest, &plans, &downstream); err != nil {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: safeFailureReason(err)}, nil
		}
		switch state {
		case "pending", "waiting_retry":
			if downstream {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectNone, Reason: fmt.Sprintf("artifact %s has downstream commitment or invalid file roots", id)}, nil
			}
			if work == "" {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectNone, Reason: fmt.Sprintf("artifact %s has no live inspection work", id)}, nil
			}
			if ref == "" {
				ref = work
			}
		case "verified":
			if !manifest || plans != 1 || work != "" {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectNone, Reason: fmt.Sprintf("artifact %s has incomplete inspection graph", id)}, nil
			}
		case "failed":
		default:
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectNone, Reason: "artifact state is unexplained"}, nil
		}
	}
	if err := rows.Err(); err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: safeFailureReason(err)}, nil
	}
	return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: deployment.EffectNone, EvidenceRef: ref, Reason: "durable inspection obligations classified"}, nil
}
