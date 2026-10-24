package filemaintenance

import (
	persistence "bansoko/internal/filemaintenance/adapter/persistence"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Persist the entire frozen loser obligation with the recovered decision. No
// filesystem calls, fresh evaluate, or transaction-after-commit handoff occurs.
// seen carries the losers the other recovered decisions of the same release
// already handed off: one loser has one cleanup owner.
func (s *ReplacementDecisionService) handoffRecoveryLosersTx(ctx context.Context, scope txscope.Tx, d *decisionRow, seen map[string]bool) error {
	if len(d.Losers) == 0 {
		return nil
	}
	if s.cleanup == nil || s.cleanup.workItems == nil {
		return workitems.ErrInvalidSpec
	}
	tx := scope.SQLTx()
	var effect string
	var deadline time.Time
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(cleanup_policy->>'effect',''),'quarantine'),CURRENT_TIMESTAMP+make_interval(secs=>COALESCE(NULLIF(cleanup_policy->>'grace_seconds','')::bigint,0)) FROM anime_business_configs WHERE managed_anime_id=$1 AND retention_version=$2 AND cleanup_policy IS NOT NULL FOR SHARE`, d.AnimeID, d.PolicyVersion).Scan(&effect, &deadline); err != nil {
		return err
	}
	if effect != "quarantine" {
		return workitems.ErrInvalidSpec
	}
	var winnerPath string
	if err := tx.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id=$1 AND managed_anime_id=$2 FOR SHARE`, d.Winner.PlacementID, d.AnimeID).Scan(&winnerPath); err != nil {
		return err
	}
	for _, loser := range d.Losers {
		if seen[loser.PlacementID] {
			continue
		}
		seen[loser.PlacementID] = true
		var path, fp, state string
		var coverage []byte
		if err := tx.QueryRowContext(ctx, `SELECT physical_path,source_fingerprint,COALESCE((SELECT jsonb_agg(observation_key ORDER BY observation_key) FROM placement_coverage WHERE placement_id=p.id),'[]'::jsonb),state FROM placements p WHERE id=$1 AND managed_anime_id=$2 FOR UPDATE NOWAIT`, loser.PlacementID, d.AnimeID).Scan(&path, &fp, &coverage, &state); err != nil {
			return err
		}
		if state == "displaced" {
			// Its path belongs to the successor: never cleaned up, and its
			// companion relations ended with it.
			continue
		}
		if normalized := fingerprintHex(fp); validFingerprintHex(normalized) {
			fp = normalized
		}
		// The loser's companions follow it into cleanup unless the winner
		// now owns their path, also for a same-path loser.
		if err := s.handoffRecoveryCompanionsTx(ctx, scope, d, loser.PlacementID, coverage, effect, deadline); err != nil {
			return err
		}
		if path == winnerPath {
			continue
		}
		id := "cln-" + loser.PlacementID
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT replacement_ref FROM cleanup_operations WHERE id=$1`, id).Scan(&owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && owner != d.ID {
			// Another key decision of the same multi-episode loser owns its
			// cleanup already.
			continue
		}
		if err := s.cleanup.repository.InsertTx(ctx, scope, persistence.OperationInput{ID: id, IdempotencyKey: "cln:" + loser.PlacementID + ":" + d.ID, PlacementID: loser.PlacementID, ManagedAnimeID: d.AnimeID, Coverage: coverage, ReplacementRef: d.ID, PolicyVersion: d.PolicyVersion, RetentionMode: string(d.AdmissionMode), SourcePath: path, SourceFingerprint: fp, Effect: effect, GraceDeadline: &deadline, CorrelationID: d.CorrelationID, CausationType: "replacement_decision", CausationID: d.ID}); err != nil {
			return err
		}
		var version int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM cleanup_operations WHERE id=$1 AND replacement_ref=$2 FOR UPDATE`, id, d.ID).Scan(&version); err != nil {
			return err
		}
		snapshot, _ := json.Marshal(map[string]any{"cleanup_id": id, "stage": workitems.KindCleanupQuarantine})
		if _, _, err := s.cleanup.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerCleanup, ID: id}, Kind: workitems.KindCleanupQuarantine, IdempotencyKey: "cln:" + id + ":" + workitems.KindCleanupQuarantine, InputSnapshot: snapshot, MaxAttempts: 3, ExpectedOwnerVersion: version}); err != nil {
			return err
		}
	}
	return nil
}
