package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/apperrors"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// LocalOwnerCleanup is a maintenance/test-only helper for deleting one local
// FileMaintenance owner tree. It is not wired to HTTP, workers, or ManagedAnime
// retirement and never touches qBittorrent or the filesystem.
type LocalOwnerCleanup struct {
	db *sql.DB
}

func NewLocalOwnerCleanup(db *sql.DB) *LocalOwnerCleanup {
	return &LocalOwnerCleanup{db: db}
}

func (s *LocalOwnerCleanup) CleanupLocalOwnersTx(ctx context.Context, scope txscope.Tx, animeID string) error {
	if s == nil || s.db == nil {
		return errors.New("local owner cleanup is not configured")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("local owner cleanup transaction is nil")
	}
	if err := s.checkLiveDestructiveEffects(ctx, tx, animeID); err != nil {
		return err
	}
	return s.deleteLeafFirst(ctx, tx, animeID)
}

// checkLiveDestructiveEffects fails closed with a typed conflict when any
// checkpointed destructive effect has not converged: live alignment/cleanup
// claims, checkpointed alignment (ready/running) or cleanup
// (admitted, or blocked with a checkpointed attempt) operations, an offset migration
// generation/operation/work item that is still live, or an admitted frozen
// replace decision. All row locks serialize against the effect handlers that
// revalidate the same owners (settle locks the Subscription/placements; the
// cleanup/alignment handlers lock their own owner rows), so the check and the
// delete commit atomically.
func (s *LocalOwnerCleanup) checkLiveDestructiveEffects(ctx context.Context, tx *sql.Tx, animeID string) error {
	var conflict bool
	err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
    -- Live replacement/settle claim: the evaluate worker may settle a frozen
    -- replace decision and hand losers to cleanup on the next claim.
    SELECT 1 FROM work_items
    WHERE owner_type = 'file_maintenance' AND owner_id = $1
      AND kind = 'replacement_evaluate' AND state IN ('available', 'leased')
) OR EXISTS (
    -- Live alignment execution claim with a checkpointed/ready operation.
    SELECT 1 FROM work_items wi
    JOIN alignment_operations a ON a.id = wi.owner_id
    WHERE wi.owner_type = 'alignment_operation' AND a.managed_anime_id = $1 AND wi.state IN ('available', 'leased')
) OR EXISTS (
    -- Live cleanup execution claim.
    SELECT 1 FROM work_items wi
    JOIN cleanup_operations c ON c.id = wi.owner_id
    WHERE wi.owner_type = 'cleanup_operation' AND c.managed_anime_id = $1 AND wi.state IN ('available', 'leased')
) OR EXISTS (
    -- An offset execution claim is live while it is available or leased. The
    -- plan is the typed owner and is scoped by its managed anime id.
    SELECT 1 FROM work_items wi
    JOIN offset_migration_plans p ON p.id = wi.owner_id
    WHERE wi.owner_type = 'offset_migration_operation'
      AND wi.kind = 'offset_migration_execute'
      AND p.managed_anime_id = $1
      AND wi.state IN ('available', 'leased')
) OR EXISTS (
    -- A generation is not converged until planning/execution reaches a
    -- terminal generation state.
    SELECT 1 FROM offset_migration_generations
    WHERE managed_anime_id = $1
      AND state IN ('pending', 'planning', 'ready', 'running')
) OR EXISTS (
    -- Per-placement offset operations retain frozen effect intent until they
    -- reach applied/failed/blocked/superseded.
    SELECT 1 FROM offset_migration_plans
    WHERE managed_anime_id = $1
      AND state IN ('planned', 'ready', 'running', 'waiting_retry', 'effect_unknown')
) OR EXISTS (
    -- Checkpointed alignment operation with a frozen attempt (or about to
    -- write one): the effect may have landed or is about to.
    SELECT 1 FROM alignment_operations
    WHERE managed_anime_id = $1 AND state IN ('ready', 'running')
) OR EXISTS (
    -- Pending deletions: an admitted row has a pending effect; a blocked row
    -- with an attempt may hold a captured file awaiting its purge.
    SELECT 1 FROM cleanup_operations
    WHERE managed_anime_id = $1 AND (state = 'admitted' OR (state = 'blocked' AND attempt IS NOT NULL))
) OR EXISTS (
    -- A frozen admitted replace decision is a checkpointed destructive
    -- intent: settle would supersede losers and admit cleanup.
    SELECT 1 FROM replacement_decisions
    WHERE managed_anime_id = $1 AND verdict = 'replace' AND state = 'admitted'
)`, animeID).Scan(&conflict)
	if err != nil {
		return fmt.Errorf("check live FileMaintenance effects: %w", err)
	}
	if conflict {
		return &apperrors.Business{
			Status:  409,
			Code:    "LOCAL_OWNER_CLEANUP_ACTIVE_EFFECT",
			Message: "the instance has checkpointed FileMaintenance effects in flight (alignment/cleanup/replacement/offset migration); local owner cleanup is refused until they converge",
		}
	}
	return nil
}

// deleteLeafFirst removes the FileMaintenance owner tree of one anime. The
// order mirrors the FK-free rows (no cascade) and the PRD leaf-first order.
func (s *LocalOwnerCleanup) deleteLeafFirst(ctx context.Context, tx *sql.Tx, animeID string) error {
	// Work items of every FileMaintenance kind: file_maintenance owners plus
	// alignment_operation / cleanup_operation / offset_migration_operation owners
	// of this anime.
	if err := workitems.DeleteLocalMaintenanceTreeTx(ctx, tx, animeID); err != nil {
		return fmt.Errorf("delete FileMaintenance work items: %w", err)
	}
	// Offset migration work items point at plans, and plans point at
	// generations. Remove this owner chain before any downstream Release-level
	// media-fact files are deleted by the anime subscription cascade.
	if _, err := tx.ExecContext(ctx, `DELETE FROM offset_migration_plans WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete offset migration plans: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM offset_migration_generations WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete offset migration generations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cleanup_operations WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete cleanup operations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM alignment_operations WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete alignment operations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM replacement_decisions WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete replacement decisions: %w", err)
	}
	// Placement coverage cascades with the placement rows (ON DELETE CASCADE),
	// so only the placement rows need an explicit delete.
	if _, err := tx.ExecContext(ctx, `DELETE FROM placements WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete placements: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reconciliation_files WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete reconciliation files: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reconciliation_scans WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete reconciliation scans: %w", err)
	}
	// The merged configuration row (owner versions, the three policy axes and
	// the frozen business) is instance-owned and must be removed before the
	// ManagedAnime row.
	if _, err := tx.ExecContext(ctx, `DELETE FROM anime_business_configs WHERE managed_anime_id = $1`, animeID); err != nil {
		return fmt.Errorf("delete anime business config: %w", err)
	}
	// The instance FileMaintenance policy override must die with the instance:
	// a surviving 'anime:<id>' row would let the periodic scheduler keep
	// submitting scans for a deleted ID (the scan tables have no FK and
	// SubmitScan would recreate an owner row).
	if _, err := tx.ExecContext(ctx, `DELETE FROM filemaintenance_policies WHERE scope_key = 'anime:' || $1`, animeID); err != nil {
		return fmt.Errorf("delete instance file maintenance policy: %w", err)
	}
	return nil
}
