package migrations

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestProviderFeedSnapshotHashSchemaAndAppendOnly proves the hash-schema label
// check, the append-only guards, and the cascade from the owning snapshot. The
// snapshot is published through the repository because the commit-time seal
// recomputes the seed count and the canonical content hash.
func TestProviderFeedSnapshotHashSchemaAndAppendOnly(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	snapshot := publishFeedFixture(t, db)

	// 1. A snapshot carries the current hash schema and a projection schema;
	// other labels are rejected immediately by the column checks.
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT hash_schema FROM provider_feed_snapshots WHERE id = $1`, snapshot.ID).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if schema != "provider-feed-snapshot-v1" {
		t.Fatalf("snapshot hash schema = %q, want provider-feed-snapshot-v1", schema)
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO provider_feed_snapshots(
    id, provider, provider_entry_ref, provider_group_ref, version, content_hash, hash_schema, seed_count, projection_schema
)
VALUES ('snap-foreign-schema', 'anibt', 'entry-1', 'group-1', 3, $1, 'provider-feed-snapshot-v2', 1, 'torrent-manifest-v1')`, sha256Fixture("e"))
	expectPgErrorCode(t, err, "23514")
	_, err = db.ExecContext(ctx, `
INSERT INTO provider_feed_snapshots(
    id, provider, provider_entry_ref, provider_group_ref, version, content_hash, seed_count)
VALUES ('snap-no-projection', 'anibt', 'entry-1', 'group-1', 4, $1, 1)`, sha256Fixture("f"))
	expectPgErrorCode(t, err, "23514")

	// 2. The snapshot body is append-only.
	_, err = db.ExecContext(ctx, `
UPDATE provider_feed_snapshots SET content_hash = $1 WHERE id = $2`, sha256Fixture("b"), snapshot.ID)
	expectPgErrorCode(t, err, "23514")

	// 3. A seed row is append-only, payload bytes and manifest leaves included.
	var seedID string
	if err := db.QueryRowContext(ctx, `SELECT s.id FROM provider_feed_snapshot_seeds s WHERE s.snapshot_id = $1`, snapshot.ID).Scan(&seedID); err != nil {
		t.Fatalf("read the published seed: %v", err)
	}
	_, err = db.ExecContext(ctx, `UPDATE provider_feed_snapshot_seeds SET title = 'changed' WHERE id = $1`, seedID)
	expectPgErrorCode(t, err, "23514")
	_, err = db.ExecContext(ctx, `UPDATE provider_feed_snapshot_seeds SET payload = '\x03'::bytea WHERE id = $1`, seedID)
	expectPgErrorCode(t, err, "23514")
	_, err = db.ExecContext(ctx, `UPDATE provider_feed_snapshot_seeds SET files = '[]'::jsonb WHERE id = $1`, seedID)
	expectPgErrorCode(t, err, "23514")

	// 4. A leaf row cannot be deleted while its parent still exists.
	_, err = db.ExecContext(ctx, `DELETE FROM provider_feed_snapshot_seeds WHERE id = $1`, seedID)
	expectPgErrorCode(t, err, "23514")

	// 5. Deleting the owning snapshot cascades its seeds.
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE id = $1`, snapshot.ID); err != nil {
		t.Fatalf("delete owning snapshot: %v", err)
	}
	for table, id := range map[string]string{
		"provider_feed_snapshot_seeds": seedID,
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = $1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s was not cascaded from its owning snapshot", table)
		}
	}
}

// TestProviderFeedSnapshotSeedFilename proves the nullable seed filename fact:
// an unknown filename stays NULL, a real torrent filename is preserved
// verbatim, and the 1..512 trimmed-length bound rejects empty and oversized
// values. Extra seed rows are inserted directly into a published snapshot; the
// seal only runs when the snapshot header is inserted.
func TestProviderFeedSnapshotSeedFilename(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	snapshot := publishFeedFixture(t, db)
	insert := func(id string, filename any) error {
		_, err := db.ExecContext(ctx, `
INSERT INTO provider_feed_snapshot_seeds(id, snapshot_id, stable_seed_key, locator, locator_display, title, torrent_filename)
VALUES ($1, $2, 'key-' || $1::text, 'https://example.test/' || $1::text || '.torrent', 'https://example.test/' || $1::text || '.torrent', 'Filename', $3)`, id, snapshot.ID, filename)
		return err
	}

	// 1. An unknown seed filename stays NULL instead of a fabricated name.
	if err := insert("seed-null", nil); err != nil {
		t.Fatalf("seed NULL filename: %v", err)
	}
	var stored sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT torrent_filename FROM provider_feed_snapshot_seeds WHERE id = 'seed-null'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Valid {
		t.Fatalf("unknown seed filename = %q, want NULL", stored.String)
	}

	// 2. A real torrent filename is preserved verbatim.
	if err := insert("seed-ok", "[Test Group] Show - 01.torrent"); err != nil {
		t.Fatalf("seed valid filename: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT torrent_filename FROM provider_feed_snapshot_seeds WHERE id = 'seed-ok'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Valid || stored.String != "[Test Group] Show - 01.torrent" {
		t.Fatalf("stored filename = %+v", stored)
	}

	// 3. An empty or whitespace-only value is rejected by the trimmed bound.
	expectPgErrorCode(t, insert("seed-empty", "   "), "23514")

	// 4. The 512-character upper bound is enforced.
	expectPgErrorCode(t, insert("seed-long", strings.Repeat("a", 513)), "23514")
}
