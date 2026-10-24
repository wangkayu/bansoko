package migrations

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func sha256Fixture(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func seedFixtureCandidate(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// The candidate guard has a fixture opt-out so a seed row can stay
	// independent of the typed draft contract.
	if _, err := tx.ExecContext(ctx, `SET LOCAL bansoko.candidate_fixture = 'on'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_config_candidates(
    id, origin, origin_entry_ref, title, instance_label, draft, draft_digest,
    last_activity, status, retention_until
)
VALUES ($1, 'custom', NULL, 'Fixture candidate', 'Fixture candidate', '{}'::jsonb,
        'sha256:' || encode(
            digest(convert_to(anime_config_candidate_canonical_json('{}'::jsonb), 'UTF8'), 'sha256'),
            'hex'
        ),
        CURRENT_TIMESTAMP, 'working', CURRENT_TIMESTAMP + INTERVAL '30 days')`,
		id); err != nil {
		t.Fatalf("seed fixture candidate: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// revisionSectionKeys is the canonical candidate-revision section key set.
type revisionSectionKeys struct {
	effective, versions, schemas string
	// parserPolicy is the frozen envelope the body guard accepts.
	parserPolicy string
}

var revisionSectionsCurrent = revisionSectionKeys{
	effective:    `{"subgroups":{},"parser":{},"release_rules":{},"file_rules":{},"naming_rules":{},"file_effect":{}}`,
	versions:     `{"subgroups":0,"parser":0,"release_rules":0,"file_rules":0,"naming_rules":0,"file_effect":0}`,
	schemas:      `{"subgroups":"subgroups-v1","parser":"parser-v1","release_rules":"release-rules-v1","file_rules":"file-rules-v1","naming_rules":"naming-rules-v1","file_effect":"file-effect-v1"}`,
	parserPolicy: `{"schema_version":"parser-policy-envelope-v1","kind":"regex","policy_version":1,"profile_version":0,"rules_owner_version":0,"source_owner":"global_parser_policy_default","provenance":"frozen_at_candidate_save"}`,
}

// insertRevision inserts one revision with the current canonical six section
// keys and a valid frozen parser envelope, matching what the body guard
// requires. Callers that need a non-default body should extend it explicitly.
func insertRevision(t *testing.T, db *sql.DB, candidateID string, revision int64, draftDigest, effectiveDigest string) {
	t.Helper()
	keys := revisionSectionsCurrent
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidate_revisions(
    candidate_id, revision, draft, draft_digest, effective,
    section_versions, section_schemas, effective_digest, parser_policy, parser_policy_digest
)
VALUES ($1, $2, '{}'::jsonb, $3, $6::jsonb, $7::jsonb, $8::jsonb, $4, $9::jsonb, $5)`,
		candidateID, revision, draftDigest, effectiveDigest, sha256Fixture("f"), keys.effective, keys.versions, keys.schemas, keys.parserPolicy); err != nil {
		t.Fatalf("insert revision %s@%d: %v", candidateID, revision, err)
	}
}

func expectPgErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected PostgreSQL error %s, got nil", code)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected PostgreSQL error %s, got %v", code, err)
	}
	if pgErr.Code != code {
		t.Fatalf("expected PostgreSQL error %s, got %s (%v)", code, pgErr.Code, err)
	}
}

// A candidate without a saved revision stays revision-less, the current
// revision pointer must reference an existing matching revision, a revision
// body is immutable, and removal is only possible through the owning candidate.
func TestCandidateRevisionPointerAndImmutability(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedFixtureCandidate(t, db, "cand-rev")

	// 1. No fabricated revision: a candidate row stays revision-less.
	var current sql.NullInt64
	if err := db.QueryRowContext(ctx, `
SELECT current_revision FROM anime_config_candidates WHERE id = 'cand-rev'`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current.Valid {
		t.Fatalf("candidate received a fabricated revision %d", current.Int64)
	}
	var revisionCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM anime_config_candidate_revisions WHERE candidate_id = 'cand-rev'`).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 0 {
		t.Fatalf("candidate has %d revisions, want 0", revisionCount)
	}

	// 2. A pointer with no revision behind it is rejected.
	_, err := db.ExecContext(ctx, `
UPDATE anime_config_candidates
SET current_revision = 9, current_revision_digest = $1
WHERE id = 'cand-rev'`, sha256Fixture("b"))
	expectPgErrorCode(t, err, "23503")

	// 3. Insert the revision first, then move the pointer: accepted.
	draftDigest := sha256Fixture("a")
	effectiveDigest := sha256Fixture("c")
	insertRevision(t, db, "cand-rev", 1, draftDigest, effectiveDigest)
	if _, err := db.ExecContext(ctx, `
UPDATE anime_config_candidates
SET current_revision = 1, current_revision_digest = $1
WHERE id = 'cand-rev'`, effectiveDigest); err != nil {
		t.Fatalf("move pointer: %v", err)
	}

	// 4. A mirrored digest that disagrees with the revision body is rejected.
	_, err = db.ExecContext(ctx, `
UPDATE anime_config_candidates
SET current_revision = 1, current_revision_digest = $1
WHERE id = 'cand-rev'`, sha256Fixture("d"))
	expectPgErrorCode(t, err, "23514")

	// 5. A revision body is immutable.
	_, err = db.ExecContext(ctx, `
UPDATE anime_config_candidate_revisions
SET effective_digest = $1
WHERE candidate_id = 'cand-rev' AND revision = 1`, sha256Fixture("e"))
	expectPgErrorCode(t, err, "23514")

	// 6. A revision cannot be deleted directly while its candidate exists.
	_, err = db.ExecContext(ctx, `
DELETE FROM anime_config_candidate_revisions
WHERE candidate_id = 'cand-rev' AND revision = 1`)
	expectPgErrorCode(t, err, "23514")

	// 7. Candidate deletion requires explicit parent cleanup authority; once granted,
	// deleting the candidate still cascades its revisions.
	if _, err := db.ExecContext(ctx, `UPDATE anime_config_candidates SET status='archived', cleanup_eligible_at=CURRENT_TIMESTAMP, cleanup_reason='user_deleted' WHERE id='cand-rev'`); err != nil {
		t.Fatalf("authorize candidate cleanup: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM anime_config_candidates WHERE id = 'cand-rev'`); err != nil {
		t.Fatalf("delete candidate: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM anime_config_candidate_revisions WHERE candidate_id = 'cand-rev'`).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 0 {
		t.Fatalf("candidate cascade left %d revisions", revisionCount)
	}
}
