package migrations

import (
	"context"
	"database/sql"
	"testing"
)

// Migration 100 renames the owner media kind column and constraint, rewrites
// the stored candidate drafts and revisions (recomputing a draft digest only
// where it was the canonical hash), and translates the replayed metadata
// result snapshot.
func TestMediaKindRenameUpgradeRewritesStoredRecords(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "099_config_scan_requests.sql")

	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}

	exec(`INSERT INTO managed_anime(id, title) VALUES ('mk-1', 'Movie')`)
	exec(`INSERT INTO anime_metadata(managed_anime_id, media_kind_override) VALUES ('mk-1', 'movie')`)

	// The candidate carries the old key; the fixture switch lets a seed row
	// skip the typed draft contract, and the defaults trigger sets the digest.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL bansoko.candidate_fixture = 'on'`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_config_candidates(
    id, origin, origin_entry_ref, title, instance_label, draft, draft_digest,
    last_activity, status, retention_until
)
VALUES ('mk-cand', 'custom', NULL, 'Fixture candidate', 'Fixture candidate',
        '{"metadata":{"media_kind_override":"movie","year":2020}}'::jsonb, '',
        CURRENT_TIMESTAMP, 'working', CURRENT_TIMESTAMP + INTERVAL '30 days')`); err != nil {
		tx.Rollback()
		t.Fatalf("seed candidate: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Revisions are inserted with the fixture body and then given the old-key
	// draft with the append-only guard lifted, as the migration does.
	insertRevision(t, db, "mk-cand", 1, sha256Fixture("a"), sha256Fixture("c"))
	insertRevision(t, db, "mk-cand", 2, sha256Fixture("a"), sha256Fixture("d"))
	exec(`ALTER TABLE anime_config_candidate_revisions DISABLE TRIGGER anime_config_candidate_revisions_guard_trg`)
	exec(`UPDATE anime_config_candidate_revisions
SET draft = '{"metadata":{"media_kind_override":"movie","year":2020}}'::jsonb
WHERE candidate_id = 'mk-cand'`)
	exec(`UPDATE anime_config_candidate_revisions SET draft_digest = ` + sprintfDigest("draft") + `
WHERE candidate_id = 'mk-cand' AND revision = 1`)
	exec(`ALTER TABLE anime_config_candidate_revisions ENABLE TRIGGER anime_config_candidate_revisions_guard_trg`)
	// Revision 2 keeps its non-canonical digest (sha256Fixture("a")).

	exec(`INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, result_snapshot)
VALUES ('metadata_match', 'mk-key', $1, 'mk-1',
        '{"metadata":{"media_kind_override":"movie","version":2}}'::jsonb)`, sha256Fixture("e"))

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	// Column and constraint.
	var kind string
	if err := db.QueryRowContext(ctx, `SELECT media_kind FROM anime_metadata WHERE managed_anime_id = 'mk-1'`).Scan(&kind); err != nil || kind != "movie" {
		t.Fatalf("media_kind = %q, err %v, want movie", kind, err)
	}
	var oldColumns, newConstraints int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_name = 'anime_metadata' AND column_name = 'media_kind_override'`).Scan(&oldColumns); err != nil || oldColumns != 0 {
		t.Fatalf("media_kind_override columns = %d, err %v, want 0", oldColumns, err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM pg_constraint
WHERE conrelid = 'anime_metadata'::regclass AND conname = 'anime_metadata_media_kind_check'`).Scan(&newConstraints); err != nil || newConstraints != 1 {
		t.Fatalf("anime_metadata_media_kind_check count = %d, err %v, want 1", newConstraints, err)
	}

	// Candidate draft: key translated, digest is the canonical hash.
	requireTranslatedDraft(t, db, `SELECT draft, draft_digest = `+sprintfDigest("draft")+` FROM anime_config_candidates WHERE id = 'mk-cand'`, true)
	// Revision 1 had the canonical digest and follows the new draft.
	requireTranslatedDraft(t, db, `SELECT draft, draft_digest = `+sprintfDigest("draft")+` FROM anime_config_candidate_revisions WHERE candidate_id = 'mk-cand' AND revision = 1`, true)
	// Revision 2 keeps its non-canonical digest, its key is translated.
	requireTranslatedDraft(t, db, `SELECT draft, draft_digest = `+"'"+sha256Fixture("a")+"'"+` FROM anime_config_candidate_revisions WHERE candidate_id = 'mk-cand' AND revision = 2`, true)

	// The revision guard is enabled again.
	if _, err := db.ExecContext(ctx, `UPDATE anime_config_candidate_revisions SET draft = '{}'::jsonb WHERE candidate_id = 'mk-cand' AND revision = 1`); err == nil {
		t.Fatal("a direct revision draft update was accepted after the upgrade")
	}

	// The replayed metadata result is translated.
	var translated bool
	if err := db.QueryRowContext(ctx, `
SELECT result_snapshot->'metadata'->>'media_kind' = 'movie'
   AND NOT (result_snapshot->'metadata' ? 'media_kind_override')
FROM idempotency_records WHERE idempotency_key = 'mk-key'`).Scan(&translated); err != nil || !translated {
		t.Fatalf("idempotency snapshot translated = %v, err %v", translated, err)
	}
}

func sprintfDigest(column string) string {
	return `'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(` + column + `), 'UTF8'), 'sha256'), 'hex')`
}

// requireTranslatedDraft reads (draft, digestOK) and checks the draft holds
// media_kind "movie" without the old key.
func requireTranslatedDraft(t *testing.T, db *sql.DB, query string, wantDigest bool) {
	t.Helper()
	var translated, digestOK bool
	row := db.QueryRowContext(context.Background(),
		`SELECT d->'metadata'->>'media_kind' = 'movie' AND NOT (d->'metadata' ? 'media_kind_override'), ok FROM (`+
			query+`) AS q(d, ok)`)
	if err := row.Scan(&translated, &digestOK); err != nil {
		t.Fatalf("read draft: %v\n%s", err, query)
	}
	if !translated {
		t.Fatalf("draft was not translated: %s", query)
	}
	if digestOK != wantDigest {
		t.Fatalf("draft digest check = %v, want %v: %s", digestOK, wantDigest, query)
	}
}
