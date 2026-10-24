package editor

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"bansoko/internal/persistence/txscope"
	retentionpersistence "bansoko/internal/retention/adapter/persistence"
	"bansoko/internal/testutil/disposabledb"
)

// seedDiscoveryReleaseForEditor writes one stopped-owner release chain and
// returns the release id. The release carries a complete file inventory, the
// evidence a dry run reads.
func seedDiscoveryReleaseForEditor(t *testing.T, owner *sql.DB, release string) {
	t.Helper()
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-editor', 'Editor Show')`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('subscription-editor', 'anime-editor', 'custom', 'Editor', 'active')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-editor', 'subscription-editor', 'custom_rss', 'https://feed.example.test/editor',
        'https://feed.example.test/editor', 'Group', 'Editor Show')`,
	} {
		if _, err := owner.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed editor owner chain: %v", err)
		}
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, internal_facts, source_facts, verdict, reason_code)
VALUES ($1, 'source-editor', $1, '{"title":"[Group] Editor Show - 01 [1080p]"}',
        '{"title":"[Group] Editor Show - 01 [1080p]","release_files":[{"source_path":"Editor Show/Editor Show - 01.mkv","filename":"Editor Show - 01.mkv","size_bytes":4096}]}',
        '{}', 'discovered', 'discovered')`, release); err != nil {
		t.Fatalf("seed editor release: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `
UPDATE subscriptions SET state = 'stopped', updated_at = CURRENT_TIMESTAMP - INTERVAL '31 days'
WHERE id = 'subscription-editor'`); err != nil {
		t.Fatalf("stop the editor subscription: %v", err)
	}
}

func purgeEditorDiscoveryReleases(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin discovery retention transaction: %v", err)
	}
	defer tx.Rollback()
	deleted, err := retentionpersistence.NewRepository(db).PurgeExpiredDiscoveryReleases(context.Background(), txscope.Wrap(tx), 500)
	if err != nil {
		t.Fatalf("purge discovery releases: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit discovery retention transaction: %v", err)
	}
	return deleted
}

// TestReleaseEvidenceAfterDiscoveryExpiry proves the release trace's surviving
// surface: the dry-run release picker no longer offers a purged discovery
// release, and loading its evidence answers the typed release_not_found the
// read surface already carries instead of an internal error.
func TestReleaseEvidenceAfterDiscoveryExpiry(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	ctx := context.Background()
	loader := newReleaseLoader(db)
	seedDiscoveryReleaseForEditor(t, owner, "release-editor")

	options, err := loader.ListReleases(ctx, 20)
	if err != nil || len(options) != 1 || options[0].ID != "release-editor" || options[0].AnimeTitle != "Editor Show" {
		t.Fatalf("the release must be offered before expiry: %+v %v", options, err)
	}

	if deleted := purgeEditorDiscoveryReleases(t, db); deleted != 1 {
		t.Fatalf("one discovery release must be deleted, deleted %d", deleted)
	}

	options, err = loader.ListReleases(ctx, 20)
	if err != nil {
		t.Fatalf("list releases after expiry: %v", err)
	}
	if len(options) != 0 {
		t.Fatalf("a purged release must not be offered: %+v", options)
	}
	_, _, err = loader.LoadReleaseEvidence(ctx, "release-editor")
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != ReasonReleaseNotFound {
		t.Fatalf("a purged release must be release_not_found, got %v", err)
	}
}
