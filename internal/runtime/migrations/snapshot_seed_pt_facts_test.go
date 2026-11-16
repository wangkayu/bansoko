package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 141 persists the tracker-reported display facts of a Jackett
// torznab search item: swarm counts (seeders, derived leechers), billing
// factors and caps-resolved categories. None of them can be re-derived from a
// snapshot: the swarm numbers move by the minute, and the category names come
// from the indexer's caps document, so without these columns every surface
// that reads a persisted search snapshot would render a bare second line.
//
// Like anibt_tags and source_site before them, all five columns must stay
// OUTSIDE the provider-feed-snapshot-v1 content hash: snapshots published
// before the columns existed still verify on read. The publish function is
// therefore replaced once more (its closed seed key set would reject the new
// fields) with the same 11-parameter signature migration 106 granted.
func TestSnapshotSeedPTFactsMigrationShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "141_pt_seed_facts.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	requireFragments(t, text, "snapshot seed PT facts migration",
		"ADD COLUMN seeders BIGINT",
		"ADD COLUMN leechers BIGINT",
		"ADD COLUMN download_factor DOUBLE PRECISION",
		"ADD COLUMN upload_factor DOUBLE PRECISION",
		"ADD COLUMN categories JSONB",
		// One CHECK guards all five columns; the count bound matches the Go
		// parser bound (1<<40) and the factor bound the parser's 1e6.
		"provider_feed_snapshot_seeds_pt_facts_check",
		"leechers <= 1099511627776",
		"upload_factor <= 1000000",
		"jsonb_typeof(categories) = 'array' AND jsonb_array_length(categories) <= 32",
		// The publish function keeps the 11-parameter signature migration 106
		// granted, so no new GRANT is needed and none may appear.
		"CREATE OR REPLACE FUNCTION publish_feed_snapshot(",
		"'seeders','leechers','download_factor','upload_factor','categories'",
		// Counts are integers in the same closed loop as size_bytes.
		"ARRAY['size_bytes','payload_index','seeders','leechers']",
		// Factors are numbers, never negative.
		"ARRAY['download_factor','upload_factor']",
		// Categories are a closed {id,name} object list. Both set-returning
		// walks and the length check fold a JSON null to SQL NULL first:
		// jsonb_array_length(jsonb 'null') raises a driver error instead of
		// yielding zero rows, and PostgreSQL does not short-circuit the OR.
		"jsonb_array_length(NULLIF(s->'categories','null'::jsonb)) > 32",
		"jsonb_array_elements(NULLIF(s->'categories','null'::jsonb))",
		// A non-array categories value is persisted as NULL, never as-is.
		"CASE WHEN jsonb_typeof(x.categories) = 'array' THEN x.categories ELSE NULL END",
		"seeders BIGINT,leechers BIGINT,download_factor DOUBLE PRECISION,upload_factor DOUBLE PRECISION,categories JSONB",
	)
	if strings.Contains(text, "GRANT EXECUTE") {
		t.Fatal("migration 141 re-grants publish_feed_snapshot; the 11-parameter grant from migration 106 already covers it")
	}

	// The frozen hash encoders must not learn about the columns: they are the
	// content identity of every snapshot already in the table, and both are
	// explicit field lists, so any mention is a change to the hash input.
	baseline, err := os.ReadFile(filepath.Join(migrationsDir(t), baselineMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	if fragment := between(string(baseline), "CREATE FUNCTION feed_seed_canonical", "$$;"); strings.Contains(fragment, "seeders") {
		t.Fatal("the SQL canonical feed encoder folded PT facts into the frozen content hash")
	}
	goSource, err := os.ReadFile(filepath.Join(filepath.Dir(migrationsDir(t)), "..", "internal", "catalog", "domain", "feed_snapshot.go"))
	if err != nil {
		t.Fatal(err)
	}
	if fragment := between(string(goSource), "type canonicalFeedSeed struct", "func CanonicalFeedSnapshotContentHash"); strings.Contains(fragment, "Seeders") {
		t.Fatal("the Go canonical feed encoder folded PT facts into the frozen content hash")
	}
}
