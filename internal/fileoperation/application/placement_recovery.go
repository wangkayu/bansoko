package application

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"errors"
)

type placementRecoveryQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// PlacementRecoverySink only admits the missing DB handoff inside the caller's
// recovery transaction. Implementations must check the live recovery fence.
type PlacementRecoverySink interface {
	AdmitPlacementRecoveryTx(context.Context, txscope.Tx, workitems.Claim) error
}

// Completed file owner is the durable old-phase proof. Placement must carry
// that exact path/digest, not merely share a file-operation foreign identity.
// Inside settlement, marker/work-item fencing is already held by the store;
// lock all observed owners before consuming the obligation.
func (s *Service) placementHandoffCommitted(ctx context.Context, q placementRecoveryQuery, c workitems.Claim) (bool, error) {
	lock := ""
	if _, ok := q.(*sql.Tx); ok {
		lock = " FOR UPDATE"
	}
	var state, path string
	var version int64
	var fingerprint []byte
	err := q.QueryRowContext(ctx, `SELECT state,version,target_path,target_fingerprint FROM file_operations WHERE id=$1`+lock, c.Owner.ID).Scan(&state, &version, &path, &fingerprint)
	if err != nil {
		return false, err
	}
	if state != ownerStateCompleted || version != c.ExpectedOwnerVersion {
		return false, nil
	}
	fp, err := decodeFingerprint(fingerprint)
	if err != nil {
		return false, nil
	}
	var placementID, placementPath, digest, verification, placementState, anime string
	var release sql.NullString
	err = q.QueryRowContext(ctx, `SELECT id,physical_path,target_fingerprint,verification_state,state,managed_anime_id,quality_facts_ref FROM placements WHERE source_evidence_type='file_operation' AND source_owner_ref=$1`+lock, c.Owner.ID).Scan(&placementID, &placementPath, &digest, &verification, &placementState, &anime, &release)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if placementPath != path || (digest != fp.Value && digest != string(fingerprint)) || verification != "verified" || placementState != "active" {
		return false, nil
	}
	// Without a durable trigger proof the independent downstream obligation is
	// not done. An absent WorkItem cannot be interpreted as a successful no-op.
	if !release.Valid {
		return false, nil
	}
	var triggerID string
	if tx, ok := q.(*sql.Tx); ok {
		err = workitems.LockPlacementTriggerTx(ctx, tx, anime, release.String).Scan(&triggerID)
	} else {
		err = q.QueryRowContext(ctx, `SELECT id FROM work_item_history WHERE owner_type='file_maintenance' AND owner_id=$1 AND kind='replacement_evaluate' AND input_snapshot->>'candidate_release_id'=$2 AND input_snapshot->>'trigger'='placement_committed' AND state<>'failed' ORDER BY created_at DESC LIMIT 1`, anime, release.String).Scan(&triggerID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if tx, ok := q.(*sql.Tx); ok {
			if sink, ok := s.placementSink.(PlacementRecoverySink); ok {
				if err := sink.AdmitPlacementRecoveryTx(ctx, txscope.Wrap(tx), c); err != nil {
					return false, err
				}
				return true, nil
			}
		}
		_, wired := s.placementSink.(PlacementRecoverySink)
		return wired, nil // observation only; admission happens after fencing.
	}
	return err == nil, err
}
