package application

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
)

// ObserveRecovery looks up the immutable operation key on the frozen provider.
// It never uploads, adds, pauses or deletes a torrent. A missing task cannot
// prove quiescence of a previous network request, so absence remains observable
// blocked and is retried rather than falsely terminally failed.
func (h QBSubmitHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	s := h.service
	if s == nil || s.provider == nil || s.runtimeConfig == nil || c.Kind != workitems.KindQBSubmit {
		return worker.Outcome{}, ErrInvalidClaim
	}
	return s.typedSubmitRecovery(ctx, c)
}

// QBSync recovery uses only the frozen provider identity and its read-only Sync.
// It closes an interrupted poll through the typed owner without new I/O.
func (h QBSyncHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	s := h.service
	if s == nil || s.provider == nil || s.runtimeConfig == nil || c.Kind != workitems.KindQBSync || c.Owner.Type != workitems.OwnerDownloadTask {
		return worker.Outcome{}, ErrInvalidClaim
	}
	var reply stageReply
	err := s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_recover_sync($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	return worker.Committed(c, workitems.OwnerResult{State: reply.State, ReasonCode: reply.Reason}), nil
}
