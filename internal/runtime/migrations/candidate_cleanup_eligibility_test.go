package migrations

import (
	"context"
	"database/sql"
	"testing"
)

func TestCandidateCleanupRequiresExplicitEligibility(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedFixtureCandidate(t, db, "cand-retained")
	insertRevision(t, db, "cand-retained", 1, sha256Fixture("a"), sha256Fixture("b"))
	if _, err := db.ExecContext(ctx, `
UPDATE anime_config_candidates
SET current_revision=1, current_revision_digest=$2, retention_until=CURRENT_TIMESTAMP-INTERVAL '1 day'
WHERE id=$1`, "cand-retained", sha256Fixture("b")); err != nil {
		t.Fatal(err)
	}

	var cleanupAt sql.NullTime
	var cleanupReason sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT cleanup_eligible_at, cleanup_reason FROM anime_config_candidates WHERE id='cand-retained'`).Scan(&cleanupAt, &cleanupReason); err != nil {
		t.Fatal(err)
	}
	if cleanupAt.Valid || cleanupReason.Valid {
		t.Fatalf("expired candidate was granted cleanup authority: %v %v", cleanupAt, cleanupReason)
	}
	_, err := db.ExecContext(ctx, `DELETE FROM anime_config_candidates WHERE id='cand-retained'`)
	expectPgErrorCode(t, err, "23514")

	if _, err := db.ExecContext(ctx, `
UPDATE anime_config_candidates
SET status='archived', cleanup_eligible_at=CURRENT_TIMESTAMP, cleanup_reason='user_deleted'
WHERE id='cand-retained'`); err != nil {
		t.Fatalf("authorize cleanup: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM anime_config_candidates WHERE id='cand-retained'`); err != nil {
		t.Fatalf("delete eligible candidate: %v", err)
	}
	var revisions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_config_candidate_revisions WHERE candidate_id='cand-retained'`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 {
		t.Fatalf("eligible parent delete left %d revisions", revisions)
	}
}

func TestCandidateCleanupRejectsInferredOrInvalidAuthority(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedFixtureCandidate(t, db, "cand-invalid")

	for _, statement := range []string{
		`UPDATE anime_config_candidates SET cleanup_eligible_at=CURRENT_TIMESTAMP WHERE id='cand-invalid'`,
		`UPDATE anime_config_candidates SET status='archived', cleanup_eligible_at=CURRENT_TIMESTAMP, cleanup_reason='idle_expired' WHERE id='cand-invalid'`,
		`UPDATE anime_config_candidates SET status='archived', cleanup_eligible_at=CURRENT_TIMESTAMP, cleanup_reason='provider_owner_retired' WHERE id='cand-invalid'`,
	} {
		_, err := db.ExecContext(ctx, statement)
		expectPgErrorCode(t, err, "23514")
	}

	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id,title) VALUES ('anime-linked','Linked');
UPDATE anime_config_candidates
SET activated_managed_anime_id='anime-linked', status='dormant'
WHERE id='cand-invalid'`); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(ctx, `
UPDATE anime_config_candidates
SET cleanup_eligible_at=CURRENT_TIMESTAMP, cleanup_reason='user_deleted'
WHERE id='cand-invalid'`)
	expectPgErrorCode(t, err, "23514")

	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(
 id, origin, origin_entry_ref, title, draft, status, retention_until,
 cleanup_eligible_at, cleanup_reason
) VALUES ('cand-provider','mikan','entry-cleanup','Provider cleanup',
 '{"origin":"mikan","origin_entry_ref":"entry-cleanup","title":"Provider cleanup","metadata":{},"subgroups":{"entries":[]},"rules":{"sections":{"parser":{"mode":"inherit"},"release_filter":{"mode":"inherit"},"file_filter":{"mode":"inherit"},"subtitle":{"mode":"inherit"},"naming":{"mode":"inherit"},"file_effect":{"mode":"inherit"}}},"rss_sources":[]}',
 'archived', CURRENT_TIMESTAMP + INTERVAL '30 days', CURRENT_TIMESTAMP, 'provider_owner_retired')`); err != nil {
		t.Fatalf("valid provider cleanup authority: %v", err)
	}
}
