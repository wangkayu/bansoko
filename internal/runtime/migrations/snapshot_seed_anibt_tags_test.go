package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 015 adds the one persisted carrier of an AniBT item's structured
// tag set. The tags arrive as namespaced XML, so a seed rebuilt from a snapshot
// can never re-derive them; without this column every surface that reads a
// snapshot (the non-subscribed preview, the one-shot download) evaluated
// `anibt.*` against nothing and failed releases the subscribed path accepted.
//
// The column must stay OUTSIDE the provider-feed-snapshot-v1 content hash:
// every already-published snapshot carries no tags, and the hash is verified on
// read, so folding the column into either canonical encoder would make
// VerifyContentHash fail closed on every existing snapshot. The publish function
// is therefore replaced rather than edited (the baseline is immutable and its
// closed seed key set would reject the new field).
func TestSnapshotSeedAniBTTagsMigrationShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "015_snapshot_seed_anibt_tags.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	requireFragments(t, text, "snapshot seed AniBT tag migration",
		"ALTER TABLE provider_feed_snapshot_seeds",
		"ADD COLUMN anibt_tags JSONB",
		"provider_feed_snapshot_seeds_anibt_tags_check",
		"jsonb_typeof(anibt_tags) = 'object'",
		"AND anibt_tags ? 'tags'",
		"jsonb_array_length(anibt_tags -> 'tags') <= 64",
		"CREATE OR REPLACE FUNCTION publish_feed_snapshot(",
		"'payload_index','manifest','files','anibt_tags'",
		// The block is closed as a SET, not with business_exact_keys: schema and
		// diagnostic are omitempty in the Go encoder, so the required-key form
		// would reject every tag set that omits either one. The removal names the
		// text[] overload explicitly; a bare literal is ambiguous.
		"anibt_tags - ARRAY['tags','schema','diagnostic']::text[]",
		"((s->'anibt_tags') - ARRAY['tags','schema','diagnostic']::text[])",
		// The optional fields are types, not free-form: an object schema would be
		// stringified by ->> inside anibt_tag_block and slip past the column.
		"jsonb_typeof(s->'anibt_tags'->'schema') NOT IN ('string','null')",
		"jsonb_typeof(s->'anibt_tags'->'diagnostic') NOT IN ('string','null')",
		"public.anibt_tag_block(x.anibt_tags)",
		// The value bound is the extractor's (128 bytes of value), not the
		// length of the whole "dimension:value" string: a 128-byte customtag
		// is valid and must not be refused at publish.
		"octet_length(substr(tag, strpos(tag, ':') + 1)) > 128",
		// The tag list is a list of strings. A nested object also yields text
		// (with colons) from jsonb_array_elements_text but cannot be decoded
		// into the []string the read path expects, and a CHECK cannot hold a
		// subquery, so the element type is a jsonpath test.
		`jsonb_path_exists(anibt_tags -> 'tags', '$[*] ? (@.type() != "string")')`,
		"GRANT EXECUTE ON FUNCTION publish_feed_snapshot(text, text, text, text, bigint, timestamp with time zone, text, jsonb, bytea[]) TO bansoko_runtime",
		"REVOKE ALL ON FUNCTION publish_feed_snapshot(text, text, text, text, bigint, timestamp with time zone, text, jsonb, bytea[]) FROM PUBLIC",
		// A helper function alongside the owner entry inherits PUBLIC EXECUTE
		// from PostgreSQL unless it is revoked. It is only ever called from
		// inside the SECURITY DEFINER owner, so it gets no caller-facing grant.
		"REVOKE ALL ON FUNCTION anibt_tag_block(jsonb) FROM PUBLIC",
	)

	// The frozen hash encoders must not learn about the column: they are the
	// content identity of every snapshot already in the table, and both are
	// explicit field lists, so any mention is a change to the hash input.
	baseline, err := os.ReadFile(filepath.Join(migrationsDir(t), baselineMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	if fragment := between(string(baseline), "CREATE FUNCTION feed_seed_canonical", "$$;"); strings.Contains(fragment, "anibt_tags") {
		t.Fatal("the SQL canonical feed encoder folded anibt_tags into the frozen content hash")
	}
	goSource, err := os.ReadFile(filepath.Join(filepath.Dir(migrationsDir(t)), "..", "internal", "catalog", "domain", "feed_snapshot.go"))
	if err != nil {
		t.Fatal(err)
	}
	if fragment := between(string(goSource), "type canonicalFeedSeed struct", "func CanonicalFeedSnapshotContentHash"); strings.Contains(fragment, "AniBTTags") {
		t.Fatal("the Go canonical feed encoder folded the AniBT tag set into the frozen content hash")
	}
}

// between returns the slice of source starting at from and ending at the first
// to after it, or "" when either marker is absent.
func between(source, from, to string) string {
	start := strings.Index(source, from)
	if start < 0 {
		return ""
	}
	rest := source[start:]
	end := strings.Index(rest, to)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
