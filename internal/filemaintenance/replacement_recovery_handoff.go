package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// handoffRecoveryLosersTx records the deletion obligations of a recovered
// decision in the caller's transaction. No filesystem call, fresh evaluate or
// after-commit handoff occurs. seen carries the losers the other recovered
// decisions of the same release already handed off: one loser has one
// deletion obligation.
func (s *ReplacementDecisionService) handoffRecoveryLosersTx(ctx context.Context, scope txscope.Tx, d *decisionRow, seen map[string]bool) error {
	if len(d.Losers) == 0 {
		return nil
	}
	if s.cleanup == nil || s.cleanup.workItems == nil {
		return workitems.ErrInvalidSpec
	}
	return s.admitLoserDeletionsTx(ctx, scope, d, seen)
}

// admitLoserDeletionsTx records, in the settle transaction that superseded
// them, one deletion obligation per superseded loser of the decision and one
// per cleanable companion of it. Rules:
//   - a released or displaced loser (or one this settle did not take over) is
//     never deleted, and its companions stay with it;
//   - a loser at the winner's path is only superseded: the path now holds the
//     winner;
//   - the identity frozen for the deletion is the loser's recorded path-level
//     identity (size, plus device/inode when recorded); without a recorded
//     size the obligation is admitted as skipped.
func (s *ReplacementDecisionService) admitLoserDeletionsTx(ctx context.Context, scope txscope.Tx, d *decisionRow, handed map[string]bool) error {
	if s.cleanup == nil {
		return errors.New("cleanup handoff is not wired")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("loser deletion transaction is nil")
	}
	winnerID, winnerPath := "", ""
	if d.Winner != nil && d.Winner.PlacementID != "" {
		winnerID = d.Winner.PlacementID
		err := tx.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = $1`, winnerID).Scan(&winnerPath)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	for _, loser := range d.Losers {
		if handed[loser.PlacementID] {
			continue
		}
		handed[loser.PlacementID] = true
		var path, state string
		var coverageRaw []byte
		err := tx.QueryRowContext(ctx, `
SELECT p.physical_path, p.state,
       COALESCE((SELECT jsonb_agg(pc.observation_key ORDER BY pc.coverage_ordinal) FROM placement_coverage pc WHERE pc.placement_id = p.id), '[]'::jsonb)
FROM placements p
WHERE p.id = $1 AND p.managed_anime_id = $2
FOR UPDATE`, loser.PlacementID, d.AnimeID).Scan(&path, &state, &coverageRaw)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load loser placement %s: %w", loser.PlacementID, err)
		}
		if err != nil {
			return err
		}
		if state != "superseded" {
			continue
		}
		var coverage []string
		if err := json.Unmarshal(coverageRaw, &coverage); err != nil {
			return fmt.Errorf("decode loser coverage: %w", err)
		}
		if err := s.admitCompanionDeletionsTx(ctx, scope, d, loser.PlacementID, winnerID, coverage); err != nil {
			return err
		}
		if strings.TrimSpace(winnerPath) != "" && path == winnerPath {
			continue
		}
		id := "cln-" + loser.PlacementID
		var admitted bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM replacement_deletions WHERE id = $1)`, id).Scan(&admitted); err != nil {
			return err
		}
		if admitted {
			// Another key decision of the same multi-episode loser owns it.
			continue
		}
		identity, err := loserDeletionIdentityTx(ctx, tx, loser.PlacementID)
		if err != nil {
			return err
		}
		if err := s.cleanup.admitTx(ctx, scope, CleanupInput{
			ID:                id,
			IdempotencyKey:    "cln:" + loser.PlacementID + ":" + d.ID,
			PlacementID:       loser.PlacementID,
			ManagedAnimeID:    d.AnimeID,
			Coverage:          coverage,
			ReplacementRef:    d.ID,
			AdmissionMode:     string(d.AdmissionMode),
			SourcePath:        path,
			SourceFingerprint: identity,
			CorrelationID:     d.CorrelationID,
			CausationType:     "replacement_decision",
			CausationID:       d.ID,
		}); err != nil {
			return fmt.Errorf("admit loser deletion: %w", err)
		}
	}
	return nil
}

// loserDeletionIdentityTx freezes the loser's recorded path-level identity as
// a metadata fingerprint; "" when no size was recorded.
func loserDeletionIdentityTx(ctx context.Context, tx *sql.Tx, placementID string) (string, error) {
	record, err := readPlacementIdentityTx(ctx, tx, placementID)
	if err != nil {
		return "", fmt.Errorf("read loser identity: %w", err)
	}
	facts, known := record.metadataFacts()
	if !known {
		return "", nil
	}
	return domain.MetadataFingerprint(facts.SizeBytes, domain.FileIdentity{Device: facts.Device, Inode: facts.Inode}), nil
}

// dedupePlacementIDs returns the unique non-empty ids in first-seen order.
func dedupePlacementIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}
