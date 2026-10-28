package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const seasonlessKeyRewriteMigration = "096_seasonless_key_rewrite.sql"

// 096 rewrites instance rows to the seasonless key once and keeps sealed rows
// (acquisition inputs, file facts, replacement deletions) as they were.
func TestSeasonlessKeyRewriteMigrationFragments(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), seasonlessKeyRewriteMigration))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	requireFragments(t, text, "seasonless key rewrite migration",
		"SET replacement_eligible = false, version = p.version + 1",
		"'placements_excluded', jsonb_build_object('count', a.placements)",
		"ON CONFLICT (subscription_id, dedupe_key) DO NOTHING;",
		"UPDATE placement_coverage c",
		"UPDATE offset_migration_plans op",
		"UPDATE reconciliation_files r",
		"UPDATE replacement_decisions",
		"UPDATE release_candidate_facts",
		"facts = facts - 'season'",
		"UPDATE media_fact_snapshots",
		"UPDATE media_fact_files",
	)
	for _, sealed := range []string{
		"UPDATE acquisitions",
		"UPDATE replacement_deletions",
		"UPDATE artifact_files",
		"UPDATE file_facts",
		"DELETE FROM placements",
		"physical_path =",
	} {
		if strings.Contains(text, sealed) {
			t.Errorf("seasonless key rewrite migration must not contain %q", sealed)
		}
	}
}

// A4: an instance bound to TMDB season 2 holds season 1 episodes 1-3 and
// season 2 episodes 1-4 under v1 keys. Season 2 becomes tv:e1-4 and stays
// replacement-eligible; season 1 stays active with its v1 keys, becomes
// ineligible (its version moves) and one activity entry records the count.
// Every other instance row is rewritten; rows of an excluded placement keep
// their v1 keys. A second run changes nothing.
func TestSeasonlessKeyRewriteKeepsTheBoundSeasonAndExcludesTheRest(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "095_seasonless_episode_identity.sql")

	seedRewriteInstance(t, db, "a4", `tmdb_work_id, tmdb_media_kind, tmdb_season`, `'100', 'tv', 2`)
	for episode := 1; episode <= 3; episode++ {
		seedRewritePlacement(t, db, "a4", fmt.Sprintf("a4-s1-%d", episode), "active", fmt.Sprintf("tv:s1:e%d", episode))
	}
	for episode := 1; episode <= 4; episode++ {
		seedRewritePlacement(t, db, "a4", fmt.Sprintf("a4-s2-%d", episode), "active", fmt.Sprintf("tv:s2:e%d", episode))
	}
	seedRewritePlacement(t, db, "a4", "a4-old", "superseded", "tv:s1:e1")
	// A single-season instance has nothing to exclude and gets no entry.
	seedRewriteInstance(t, db, "single", `tmdb_work_id, tmdb_media_kind, tmdb_season`, `'200', 'tv', 1`)
	seedRewritePlacement(t, db, "single", "single-1", "active", "tv:s1:e1")

	rewriteExec(t, db,
		`INSERT INTO offset_migration_plans(id, managed_anime_id, placement_id, policy_version, source_fingerprint, old_target_path, state, placement_version, old_coverage_key, new_coverage_key, target_contract)
		 VALUES ('plan-s1', 'a4', 'a4-s1-1', 1, 'fp-plan-s1', '/library/a4-s1-1.mkv', 'planned', 1, 'tv:s1:e1', 'tv:s1:e2', '{"coverage_key":"tv:s1:e2"}'),
		        ('plan-s2', 'a4', 'a4-s2-1', 1, 'fp-plan-s2', '/library/a4-s2-1.mkv', 'planned', 1, 'tv:s2:e1', 'tv:s2:e2', '{"coverage_key":"tv:s2:e2"}')`,
		`INSERT INTO release_candidate_facts(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys)
		 VALUES ('rel-a4', 1, 'parser-v1', 'input-a4', '{}', '{"season":2,"episode":3}', '["tv:s2:e3"]')`,
		`INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
		 VALUES ('snap-a4', 'rel-a4', 'v1', 'episode-identity-v1', 'reltitle', 'digest-a4', '{"coverage_keys":["tv:s2:e3"]}', '{"batch_plan":[{"coverage_key":"tv:s2:e3"}]}')`,
		`INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, file_evidence, effective_facts)
		 VALUES ('snap-a4', 'file-1', 'video', 1, '{}', '{"coverage_keys":["tv:s2:e3"]}')`,
		`INSERT INTO reconciliation_files(managed_anime_id, generation, file_path, fingerprint, size_bytes, facts, observed_at, coverage)
		 VALUES ('a4', 1, '/library/a4-s1-1.mkv', 'fp', 1, '{"placement_id":"a4-s1-1"}', CURRENT_TIMESTAMP, '["tv:s1:e1"]'),
		        ('a4', 1, '/library/a4-s2-1.mkv', 'fp', 1, '{"placement_id":"a4-s2-1"}', CURRENT_TIMESTAMP, '["tv:s2:e1"]')`,
		`INSERT INTO replacement_decisions(id, idempotency_key, managed_anime_id, candidate_release_id, policy_version, profile_version, candidate_facts_ref, coverage_key, coverage_keys, admission_mode, pools, verdict, state)
		 VALUES ('decision-a4', 'decision-a4', 'a4', 'rel-a4', 1, 'profile-v1', 'facts:rel-a4', 'tv:s2:e1', '["tv:s2:e1"]', 'per_group',
		         '[{"pool_key":"tv:s2:e1","winner":{"placement_id":"a4-s2-1"}}]', 'keep', 'settled')`,
	)

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	assertRewriteState := func(pass string) {
		t.Helper()
		for episode := 1; episode <= 3; episode++ {
			id := fmt.Sprintf("a4-s1-%d", episode)
			assertRewritePlacement(t, db, pass, id, "active", false, 2, fmt.Sprintf("tv:s1:e%d", episode))
		}
		for episode := 1; episode <= 4; episode++ {
			id := fmt.Sprintf("a4-s2-%d", episode)
			assertRewritePlacement(t, db, pass, id, "active", true, 1, fmt.Sprintf("tv:e%d", episode))
		}
		assertRewritePlacement(t, db, pass, "a4-old", "superseded", true, 1, "tv:e1")
		assertRewritePlacement(t, db, pass, "single-1", "active", true, 1, "tv:e1")

		rewriteExpect(t, db, pass, "the excluded placement's live offset plan follows its version and keeps v1 keys",
			`SELECT placement_version = 2 AND old_coverage_key = 'tv:s1:e1' AND new_coverage_key = 'tv:s1:e2'
			        AND target_contract->>'coverage_key' = 'tv:s1:e2'
			 FROM offset_migration_plans WHERE id = 'plan-s1'`)
		rewriteExpect(t, db, pass, "the reference season's offset plan is rewritten",
			`SELECT placement_version = 1 AND version = 1 AND old_coverage_key = 'tv:e1' AND new_coverage_key = 'tv:e2'
			        AND target_contract->>'coverage_key' = 'tv:e2'
			 FROM offset_migration_plans WHERE id = 'plan-s2'`)
		rewriteExpect(t, db, pass, "candidate facts carry tv:e3 and no season",
			`SELECT coverage_keys = '["tv:e3"]'::jsonb AND NOT facts ? 'season' AND facts->>'episode' = '3'
			 FROM release_candidate_facts WHERE release_id = 'rel-a4'`)
		rewriteExpect(t, db, pass, "the media fact snapshot and its file carry tv:e3",
			`SELECT s.release_identity = '{"coverage_keys":["tv:e3"]}'::jsonb
			        AND s.release_facts = '{"batch_plan":[{"coverage_key":"tv:e3"}]}'::jsonb
			        AND f.effective_facts = '{"coverage_keys":["tv:e3"]}'::jsonb
			 FROM media_fact_snapshots s JOIN media_fact_files f ON f.snapshot_id = s.id WHERE s.id = 'snap-a4'`)
		rewriteExpect(t, db, pass, "the scan row of an excluded placement keeps its v1 key, the other is rewritten",
			`SELECT bool_and(CASE facts->>'placement_id'
			                     WHEN 'a4-s1-1' THEN coverage = '["tv:s1:e1"]'::jsonb
			                     ELSE coverage = '["tv:e1"]'::jsonb END)
			 FROM reconciliation_files WHERE managed_anime_id = 'a4'`)
		rewriteExpect(t, db, pass, "the replacement decision carries tv:e1",
			`SELECT coverage_key = 'tv:e1' AND coverage_keys = '["tv:e1"]'::jsonb AND pools->0->>'pool_key' = 'tv:e1'
			 FROM replacement_decisions WHERE id = 'decision-a4'`)
		rewriteExpect(t, db, pass, "one activity entry records three excluded files",
			`SELECT count(*) = 1
			        AND bool_and(kind = 'placement_changed' AND outcome = 'info' AND message_code = 'placements_excluded'
			                     AND message_params = '{"count":3}'::jsonb AND details->>'reference_season' = '2')
			 FROM subscription_log_entries WHERE subscription_id = 'sub-a4'`)
		rewriteExpect(t, db, pass, "a single-season instance gets no entry",
			`SELECT count(*) = 0 FROM subscription_log_entries WHERE subscription_id = 'sub-single'`)
	}
	assertRewriteState("upgrade")

	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), seasonlessKeyRewriteMigration))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	assertRewriteState("second run")
}

// An unbound instance keeps the season with the most active files; a tie goes
// to the larger season.
func TestSeasonlessKeyRewriteKeepsTheLargestSeasonOfAnUnboundInstance(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "095_seasonless_episode_identity.sql")

	seedRewriteInstance(t, db, "tie", "", "")
	seedRewritePlacement(t, db, "tie", "tie-s1-1", "active", "tv:s1:e1")
	seedRewritePlacement(t, db, "tie", "tie-s1-2", "active", "tv:s1:e2")
	seedRewritePlacement(t, db, "tie", "tie-s3-1", "active", "tv:s3:e1")
	seedRewritePlacement(t, db, "tie", "tie-s3-2", "active", "tv:s3:e2")
	seedRewriteInstance(t, db, "most", "", "")
	seedRewritePlacement(t, db, "most", "most-s1-1", "active", "tv:s1:e1")
	seedRewritePlacement(t, db, "most", "most-s1-2", "active", "tv:s1:e2")
	seedRewritePlacement(t, db, "most", "most-s2-1", "active", "tv:s2:e1")

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	assertRewritePlacement(t, db, "upgrade", "tie-s3-1", "active", true, 1, "tv:e1")
	assertRewritePlacement(t, db, "upgrade", "tie-s3-2", "active", true, 1, "tv:e2")
	assertRewritePlacement(t, db, "upgrade", "tie-s1-1", "active", false, 2, "tv:s1:e1")
	assertRewritePlacement(t, db, "upgrade", "tie-s1-2", "active", false, 2, "tv:s1:e2")
	assertRewritePlacement(t, db, "upgrade", "most-s1-1", "active", true, 1, "tv:e1")
	assertRewritePlacement(t, db, "upgrade", "most-s1-2", "active", true, 1, "tv:e2")
	assertRewritePlacement(t, db, "upgrade", "most-s2-1", "active", false, 2, "tv:s2:e1")
	rewriteExpect(t, db, "upgrade", "the tie keeps season 3 and records two files",
		`SELECT count(*) = 1 AND bool_and(message_params = '{"count":2}'::jsonb AND details->>'reference_season' = '3')
		 FROM subscription_log_entries WHERE subscription_id = 'sub-tie'`)
	rewriteExpect(t, db, "upgrade", "the majority keeps season 1 and records one file",
		`SELECT count(*) = 1 AND bool_and(message_params = '{"count":1}'::jsonb AND details->>'reference_season' = '1')
		 FROM subscription_log_entries WHERE subscription_id = 'sub-most'`)
}

// A fresh database, and one whose rows already carry the seasonless key,
// upgrade through 096 unchanged: nothing is excluded, rewritten or recorded.
func TestSeasonlessKeyRewriteIsANoOpWithoutV1Keys(t *testing.T) {
	ctx := context.Background()

	fresh := openDisposableDB(t)
	installMigrationsThrough(t, fresh, "095_seasonless_episode_identity.sql")
	if err := (Runner{DB: fresh, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade a fresh database: %v", err)
	}
	rewriteExpect(t, fresh, "fresh", "a fresh database holds no placement and no entry",
		`SELECT (SELECT count(*) FROM placements) = 0 AND (SELECT count(*) FROM subscription_log_entries) = 0`)

	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "095_seasonless_episode_identity.sql")
	seedRewriteInstance(t, db, "v2", `tmdb_work_id, tmdb_media_kind, tmdb_season`, `'300', 'tv', 2`)
	seedRewritePlacement(t, db, "v2", "seasonless-1", "active", "tv:e1")
	seedRewritePlacement(t, db, "v2", "seasonless-2", "active", "tv:e2")
	seedRewritePlacement(t, db, "v2", "seasonless-movie", "active", "movie:0")
	rewriteExec(t, db,
		`INSERT INTO release_candidate_facts(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys)
		 VALUES ('rel-v2', 1, 'parser-v1', 'input-v2', '{}', '{"episode":2}', '["tv:e2"]')`)
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade v2 rows: %v", err)
	}
	assertRewritePlacement(t, db, "v2", "seasonless-1", "active", true, 1, "tv:e1")
	assertRewritePlacement(t, db, "v2", "seasonless-2", "active", true, 1, "tv:e2")
	assertRewritePlacement(t, db, "v2", "seasonless-movie", "active", true, 1, "movie:0")
	rewriteExpect(t, db, "v2", "v2 candidate facts stay as written",
		`SELECT coverage_keys = '["tv:e2"]'::jsonb AND facts = '{"episode":2}'::jsonb FROM release_candidate_facts WHERE release_id = 'rel-v2'`)
	rewriteExpect(t, db, "v2", "no entry is written",
		`SELECT count(*) = 0 FROM subscription_log_entries`)
}

// seedRewriteInstance inserts an instance with an active subscription, its
// metadata row (the given columns and values, none when empty) and one RSS
// release rel-<id>.
func seedRewriteInstance(t *testing.T, db *sql.DB, id, metadataColumns, metadataValues string) {
	t.Helper()
	metadata := `INSERT INTO anime_metadata(managed_anime_id) VALUES ('` + id + `')`
	if metadataColumns != "" {
		metadata = `INSERT INTO anime_metadata(managed_anime_id, ` + metadataColumns + `) VALUES ('` + id + `', ` + metadataValues + `)`
	}
	rewriteExec(t, db,
		`INSERT INTO managed_anime(id, title) VALUES ('`+id+`', 'Show `+id+`')`,
		metadata,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-`+id+`', '`+id+`', 'custom', 'rewrite', 'active')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
		 VALUES ('src-`+id+`', 'sub-`+id+`', 'custom_rss', 'https://feeds.example/`+id+`', 'https://feeds.example/`+id+`', 'G', 'Show')`,
		`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('src-`+id+`')`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
		 VALUES ('rel-`+id+`', 'src-`+id+`', 'key', '{}', '{}', 'accepted_pending_download', 'new')`,
	)
}

// seedRewritePlacement inserts one verified, replacement-eligible placement
// with one coverage key.
func seedRewritePlacement(t *testing.T, db *sql.DB, animeID, placementID, state, key string) {
	t.Helper()
	rewriteExec(t, db,
		`INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version, state)
		 VALUES ('`+placementID+`', '/library/`+placementID+`.mkv', '`+animeID+`', 'reconciliation', 'scan:`+placementID+`', 'source', 'target', 'facts-v1', '`+state+`')`,
		`INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
		 VALUES ('`+placementID+`', '`+key+`', 0, 'facts-v1', '`+placementID+`')`,
	)
}

func rewriteExec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
}

func rewriteExpect(t *testing.T, db *sql.DB, pass, what, query string) {
	t.Helper()
	var ok sql.NullBool
	if err := db.QueryRowContext(context.Background(), query).Scan(&ok); err != nil {
		t.Fatalf("%s: %s: %v", pass, what, err)
	}
	if !ok.Valid || !ok.Bool {
		t.Fatalf("%s: %s does not hold", pass, what)
	}
}

func assertRewritePlacement(t *testing.T, db *sql.DB, pass, placementID, state string, eligible bool, version int64, key string) {
	t.Helper()
	var gotState, gotKey string
	var gotEligible bool
	var gotVersion int64
	if err := db.QueryRowContext(context.Background(), `
SELECT p.state, p.replacement_eligible, p.version, c.observation_key
FROM placements p JOIN placement_coverage c ON c.placement_id = p.id
WHERE p.id = $1`, placementID).Scan(&gotState, &gotEligible, &gotVersion, &gotKey); err != nil {
		t.Fatalf("%s: read %s: %v", pass, placementID, err)
	}
	if gotState != state || gotEligible != eligible || gotVersion != version || gotKey != key {
		t.Fatalf("%s: %s = state %s eligible %v version %d key %s, want %s %v %d %s",
			pass, placementID, gotState, gotEligible, gotVersion, gotKey, state, eligible, version, key)
	}
}
