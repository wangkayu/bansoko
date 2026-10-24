package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// TriggerPlacementRecoveryTx is the narrow placement-commit trigger port. It
// preserves the normal subscription gate, epoch and CreateOrMerge admission,
// but leaves commit/rollback to the fenced recovery settlement caller.
func (s *ReplacementTriggerService) TriggerPlacementRecoveryTx(ctx context.Context, scope txscope.Tx, animeID, releaseID string) error {
	if s == nil || s.workItems == nil || scope.SQLTx() == nil || animeID == "" || releaseID == "" {
		return errors.New("placement recovery trigger is not configured")
	}
	tx := scope.SQLTx()
	var state string
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT state,version FROM subscriptions WHERE managed_anime_id=$1 FOR UPDATE`, animeID).Scan(&state, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "active" {
		return nil
	}
	// Repeated recovery never merges or restarts an already committed trigger.
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_item_history WHERE owner_type='file_maintenance' AND owner_id=$1 AND kind='replacement_evaluate' AND input_snapshot->>'candidate_release_id'=$2 AND input_snapshot->>'trigger'=$3 AND (input_snapshot->>'subscription_version')::bigint=$4)`, animeID, releaseID, TriggerPlacementCommitted, version).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	snapshot, err := json.Marshal(map[string]any{"candidate_release_id": releaseID, "trigger": TriggerPlacementCommitted, "subscription_version": version})
	if err != nil {
		return err
	}
	_, _, err = s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: animeID}, Kind: workitems.KindReplacementEvaluate, IdempotencyKey: "replacement_evaluate:" + animeID, InputSnapshot: snapshot, MaxAttempts: triggerMaxAttempts, ExpectedOwnerVersion: version})
	return err
}
