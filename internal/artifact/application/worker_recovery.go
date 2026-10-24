package application

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
)

// Recovery never runs the inspector. Only the inspection-specific typed
// transition may release a blocked claim for a subsequent normal execution.
func (h ArtifactInspectHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindArtifactInspect || c.Owner.Type != workitems.OwnerArtifact {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	snapshot, err := h.service.loadArtifactSnapshot(ctx, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if snapshot.state == "verified" {
		return worker.AwaitRecovery("ARTIFACT_VERIFIED_WORK_LIVE"), nil
	}
	var result workitems.OwnerResult
	store := &workitems.Store{DB: h.service.db}
	err = store.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		if c.OneShot != nil {
			var exists bool
			if err := scope.SQLTx().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM one_shot_acquisitions m CROSS JOIN LATERAL jsonb_array_elements(m.active_effect_leases) e WHERE m.id=$1 AND e->>'work_item_id'=$2 AND (e->>'claimed_epoch')::bigint=$3)`, c.OneShot.MarkerID, c.ID, c.OneShot.AdmissionEpoch).Scan(&exists); err != nil {
				return "", err
			}
			if exists {
				return "", workitems.ErrInvalidOneShotLedger
			}
		}
		var raw []byte
		// The existing artifact KindPolicy uses exponential backoff. Keep its
		// frozen WorkItem attempt as the input, never increment it in recovery.
		registry, err := worker.NewRegistry(worker.DefaultPolicies()...)
		if err != nil {
			return "", err
		}
		delay := registry.MustGet(c.Kind).Backoff(c.Attempt + 1)
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.artifact_inspect_requeue_interrupted($1,$2,$3,$4)`, c.Owner.ID, c.ID, c.FencingToken, delay.Seconds()).Scan(&raw); err != nil {
			return "", err
		}
		var reply struct {
			State string `json:"state"`
			Code  string `json:"reason_code"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		result = workitems.OwnerResult{State: reply.State, ReasonCode: reply.Code}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	return worker.Committed(c, result), nil
}
