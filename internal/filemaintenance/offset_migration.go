package filemaintenance

import (
	"context"
	"database/sql"
	"time"

	"bansoko/internal/workitems"
)

// OffsetMigrationService plans the affected-Placement scan produced by an RSS
// link offset edit. Committing a link offset CAS edit creates one idempotent
// scan generation: every current, eligible placement whose release came from
// that link is recalculated from its frozen raw
// episode and provenance (never from the current filename or final path),
// the new EpisodeIdentity is resolved exactly once with the new offset, and
// the new target is rendered with the frozen identity. Operation rows are
// keyed by (policy_version, placement_id, source_fingerprint) so a replayed
// scan is idempotent. Execution belongs only to OffsetMigrationExecuteService
// through its offset_migration_execute WorkItem; the old placement stays
// authoritative until the new one commits.
type OffsetMigrationService struct {
	db       *sql.DB
	renderer *NamingTargetRenderer
	work     *workitems.Store
	now      func() time.Time
}

// NewOffsetMigrationService wires the planner.
func NewOffsetMigrationService(db *sql.DB, renderer *NamingTargetRenderer) *OffsetMigrationService {
	return &OffsetMigrationService{db: db, renderer: renderer, now: time.Now}
}

// SetWorkItems wires the durable work-item store used by PlanGeneration to
// admit one offset_migration_execute item per ready operation inside the
// planning transaction. Planners without a wired store (unit tests) skip
// admission; the store is always wired by the composition root.
func (s *OffsetMigrationService) SetWorkItems(store *workitems.Store) {
	if s != nil {
		s.work = store
	}
}

// ListMigrations returns the frozen migration plans of one anime (optional
// policy version filter), newest first, side-effect free. Only RSS-link plans
// are listed; anime-level plans from before migration 311 are history only.
func (s *OffsetMigrationService) ListMigrationsApplication(ctx context.Context, animeID string, policyVersion *int64, limit, offset int) ([]OffsetMigrationPlanView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	query := `
SELECT id, placement_id, managed_anime_id, policy_version,
       COALESCE(raw_episode, ''), COALESCE(new_episode, ''),
       old_target_path, COALESCE(new_target_path, ''), state,
       COALESCE(failure_reason, ''), version, created_at, updated_at
FROM offset_migration_plans
WHERE scope_kind = 'rss_source'
  AND ($1 = '' OR managed_anime_id = $1)
  AND ($2 = 0 OR policy_version = $2)
ORDER BY created_at DESC, id DESC
LIMIT $3 OFFSET $4`
	rows, err := s.db.QueryContext(ctx, query, animeID, orZero(policyVersion), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := make([]OffsetMigrationPlanView, 0, limit)
	for rows.Next() {
		var view OffsetMigrationPlanView
		if err := rows.Scan(&view.ID, &view.PlacementID, &view.ManagedAnimeID, &view.PolicyVersion,
			&view.RawEpisode, &view.NewEpisode, &view.OldTargetPath, &view.NewTargetPath,
			&view.State, &view.FailureReason, &view.Version, &view.CreatedAt, &view.UpdatedAt); err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

// ListGenerations returns the durable RSS-link offset migration generations of
// one anime (optional filter), newest first, side-effect free. Every generation
// state and aggregate count is exposed as owner truth; the caller may pair it
// with ListMigrations for the per-placement operation states.
func (s *OffsetMigrationService) ListGenerationsApplication(ctx context.Context, animeID string, limit, offset int) ([]OffsetMigrationGenerationView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	query := `
SELECT id, managed_anime_id, scope_kind, scope_owner_id, policy_version,
       old_offset, new_offset, state, COALESCE(reason_code, ''),
       COALESCE(reason, ''), planned_count, blocked_count, applied_count,
       superseded_count, version, created_at, updated_at, completed_at
FROM offset_migration_generations
WHERE scope_kind = 'rss_source'
  AND ($1 = '' OR managed_anime_id = $1)
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3`
	rows, err := s.db.QueryContext(ctx, query, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := make([]OffsetMigrationGenerationView, 0, limit)
	for rows.Next() {
		var (
			view        OffsetMigrationGenerationView
			completedAt sql.NullTime
		)
		if err := rows.Scan(&view.ID, &view.ManagedAnimeID, &view.ScopeKind, &view.ScopeOwnerID,
			&view.PolicyVersion, &view.OldOffset, &view.NewOffset, &view.State,
			&view.ReasonCode, &view.Reason, &view.PlannedCount, &view.BlockedCount,
			&view.AppliedCount, &view.SupersededCount, &view.Version,
			&view.CreatedAt, &view.UpdatedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			value := completedAt.Time
			view.CompletedAt = &value
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

func orZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
