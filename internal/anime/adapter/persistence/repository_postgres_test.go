package persistence

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func TestRepositoryPurgesOnlyExplicitlyEligibleCandidatesAndRespectsBatch(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	insertCandidate := func(id, status string, retentionUntil time.Time, eligibleAt *time.Time) {
		t.Helper()
		var cleanupAt, reason any
		if eligibleAt != nil {
			cleanupAt, reason = *eligibleAt, "user_deleted"
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(
    id, origin, title, instance_label, draft, status, retention_until,
    cleanup_eligible_at, cleanup_reason
) VALUES ($1::text, 'custom', $1::text, $1::text,
    jsonb_build_object('origin','custom','origin_entry_ref',NULL,'title',$1::text,'instance_label',$1::text),
    $2, $3, $4, $5)`, id, status, retentionUntil, cleanupAt, reason); err != nil {
			t.Fatalf("seed candidate %s: %v", id, err)
		}
	}

	past1, past2, future := now.Add(-2*time.Hour), now.Add(-time.Hour), now.Add(time.Hour)
	insertCandidate("expired-working-custom", "working", now.Add(-24*time.Hour), nil)
	insertCandidate("expired-archived-custom", "archived", now.Add(-24*time.Hour), nil)
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(id, origin, title, instance_label, draft, status, retention_until)
VALUES ('expired-working-agent', 'oneshot', 'Agent', 'Agent',
        '{"origin":"oneshot","origin_entry_ref":null,"title":"Agent","instance_label":"Agent"}',
        'working', CURRENT_TIMESTAMP - INTERVAL '1 day')`); err != nil {
		t.Fatalf("seed agent candidate: %v", err)
	}
	insertCandidate("eligible-1", "archived", now.Add(30*24*time.Hour), &past1)
	insertCandidate("eligible-2", "archived", now.Add(30*24*time.Hour), &past2)
	insertCandidate("eligible-future", "archived", now.Add(-24*time.Hour), &future)

	repository := NewRepository(db)
	purge := func(batchSize int) int64 {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		count, err := repository.PurgeEligibleAnimeConfigCandidates(ctx, txscope.Wrap(tx), batchSize)
		if err != nil {
			t.Fatalf("purge eligible candidates: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return count
	}

	if got := purge(1); got != 1 {
		t.Fatalf("first purge = %d, want 1", got)
	}
	assertCandidateCount(t, ctx, db, "id = 'eligible-1'", 0)
	if got := purge(1); got != 1 {
		t.Fatalf("second purge = %d, want 1", got)
	}
	assertCandidateCount(t, ctx, db, "id IN ('expired-working-custom','expired-archived-custom','expired-working-agent','eligible-future')", 4)
	if got := purge(10); got != 0 {
		t.Fatalf("idempotent purge = %d, want 0", got)
	}
}

func TestRepositoryPurgeSkipsLockedEligibleCandidate(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(
 id, origin, title, instance_label, draft, status, retention_until, cleanup_eligible_at, cleanup_reason
) VALUES ('eligible-locked','custom','Locked','Locked','{"origin":"custom","origin_entry_ref":null,"title":"Locked","instance_label":"Locked"}',
 'archived', CURRENT_TIMESTAMP + INTERVAL '30 days', CURRENT_TIMESTAMP - INTERVAL '1 hour', 'user_deleted')`); err != nil {
		t.Fatal(err)
	}
	holder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.ExecContext(ctx, `SELECT id FROM anime_config_candidates WHERE id='eligible-locked' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct {
		count int64
		err   error
	}, 1)
	go func() {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			done <- struct {
				count int64
				err   error
			}{err: err}
			return
		}
		defer tx.Rollback()
		count, err := NewRepository(db).PurgeEligibleAnimeConfigCandidates(ctx, txscope.Wrap(tx), 1)
		if err == nil {
			err = tx.Commit()
		}
		done <- struct {
			count int64
			err   error
		}{count, err}
	}()
	select {
	case result := <-done:
		if result.err != nil || result.count != 0 {
			t.Fatalf("locked purge = %d, %v; want 0,nil", result.count, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("purge waited on locked candidate")
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	assertCandidateCount(t, ctx, db, "id = 'eligible-locked'", 1)
}

// A retired instance keeps its business config as read-only history, and that
// row pins its candidate revision through a foreign key without a delete
// action. The purge must leave such a candidate alone instead of failing the
// whole sweep, and still delete unreferenced eligible candidates.
func TestRepositoryPurgeSkipsCandidateReferencedByRetainedBusinessConfig(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()

	for _, id := range []string{"referenced", "unreferenced"} {
		if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(
    id, origin, title, instance_label, draft, status, retention_until,
    cleanup_eligible_at, cleanup_reason
) VALUES ($1::text, 'custom', $1::text, $1::text,
    jsonb_build_object('origin','custom','origin_entry_ref',NULL,'title',$1::text,'instance_label',$1::text),
    'archived', CURRENT_TIMESTAMP + INTERVAL '30 days',
    CURRENT_TIMESTAMP - INTERVAL '1 hour', 'user_deleted')`, id); err != nil {
			t.Fatalf("seed candidate %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidate_revisions(
    candidate_id, revision, draft, draft_digest, effective,
    section_versions, section_schemas, effective_digest, parser_policy, parser_policy_digest
) VALUES ('referenced', 1, '{}'::jsonb, 'sha256:' || repeat('a', 64),
    '{"subgroups":{},"parser":{},"release_rules":{},"file_rules":{},"naming_rules":{},"file_effect":{}}'::jsonb,
    '{"subgroups":{},"parser":{},"release_rules":{},"file_rules":{},"naming_rules":{},"file_effect":{}}'::jsonb,
    '{"subgroups":{},"parser":{},"release_rules":{},"file_rules":{},"naming_rules":{},"file_effect":{}}'::jsonb,
    'sha256:' || repeat('b', 64),
    '{"schema_version":"parser-policy-envelope-v1","kind":"regex","policy_version":1,"profile_version":0,"rules_owner_version":0,"source_owner":"global_parser_policy_default","provenance":"frozen_at_candidate_save"}'::jsonb,
    'sha256:' || repeat('c', 64))`); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title, lifecycle_state, retired_at, retired_subscription_version)
VALUES ('retired-anime', 'Retired', 'retired', CURRENT_TIMESTAMP, 1)`); err != nil {
		t.Fatalf("seed retired anime: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_business_configs(managed_anime_id, candidate_id, revision, business_snapshot, business_digest, business_version)
SELECT 'retired-anime', 'referenced', 1, snapshot,
    'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(snapshot), 'UTF8'), 'sha256'), 'hex'), 1
FROM (SELECT '{}'::jsonb AS snapshot) AS s`); err != nil {
		t.Fatalf("seed retained business config: %v", err)
	}

	purge := func() int64 {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		count, err := NewRepository(db).PurgeEligibleAnimeConfigCandidates(ctx, txscope.Wrap(tx), 10)
		if err != nil {
			t.Fatalf("purge must not fail on a candidate pinned by a retained business config: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := purge(); got != 1 {
		t.Fatalf("purge = %d, want 1 (only the unreferenced candidate)", got)
	}
	assertCandidateCount(t, ctx, db, "id = 'unreferenced'", 0)
	assertCandidateCount(t, ctx, db, "id = 'referenced'", 1)
	if got := purge(); got != 0 {
		t.Fatalf("repeat purge = %d, want 0", got)
	}

	// Once the retained instance row is gone the candidate becomes purgeable.
	if _, err := db.ExecContext(ctx, `DELETE FROM anime_business_configs WHERE managed_anime_id = 'retired-anime'`); err != nil {
		t.Fatal(err)
	}
	if got := purge(); got != 1 {
		t.Fatalf("purge after release = %d, want 1", got)
	}
	assertCandidateCount(t, ctx, db, "id = 'referenced'", 0)
}

func TestRepositoryRejectsUnboundedCandidateCleanupBatch(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	for _, batchSize := range []int{0, maxRetentionBatchSize + 1} {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewRepository(db).PurgeEligibleAnimeConfigCandidates(context.Background(), txscope.Wrap(tx), batchSize)
		_ = tx.Rollback()
		if err == nil {
			t.Fatalf("batch size %d unexpectedly succeeded", batchSize)
		}
	}
}

func assertCandidateCount(t *testing.T, ctx context.Context, db *sql.DB, predicate string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM anime_config_candidates WHERE "+predicate).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("candidate count (%s) = %d, want %d", predicate, got, want)
	}
}
