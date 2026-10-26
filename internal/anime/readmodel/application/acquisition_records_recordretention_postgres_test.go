package application

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"bansoko/internal/persistence/txscope"
	retentionpersistence "bansoko/internal/recordretention/adapter/persistence"
	"bansoko/internal/testutil/disposabledb"
)

// purgeDiscoveryReleases runs the production janitor cleaner over the
// disposable database in one caller-owned transaction.
func purgeDiscoveryReleases(t *testing.T, db *sql.DB, batchSize int) int64 {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin discovery retention transaction: %v", err)
	}
	defer tx.Rollback()
	deleted, err := retentionpersistence.NewRepository(db).PurgeExpiredDiscoveryReleases(context.Background(), txscope.Wrap(tx), batchSize)
	if err != nil {
		t.Fatalf("purge discovery releases: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit discovery retention transaction: %v", err)
	}
	return deleted
}

// TestAcquisitionRecordSurvivesDiscoveryExpiry proves the execution record is
// outside the discovery-history deletion set: the janitor deletes only the
// release that never reached a download task, and the record keeps its release
// link, stages and file page while the owner is inactive.
func TestAcquisitionRecordSurvivesDiscoveryExpiry(t *testing.T) {
	service, db, downloadID, _ := seedRecordRoots(t)
	ctx := context.Background()
	owner := disposabledb.Owner(t)

	// The owner is stopped for longer than the window; the record's release is
	// acquisition evidence, the extra release is discovery history.
	if _, err := owner.ExecContext(ctx, `
UPDATE subscriptions SET state = 'stopped', updated_at = CURRENT_TIMESTAMP - INTERVAL '31 days'
WHERE id = 'subscription-trace'`); err != nil {
		t.Fatalf("stop the subscription: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('release-discovery', 'source-record', 'guid:discovery', '{"title":"[Group] Record Show - 02"}', '{}', 'discovered', 'discovered')`); err != nil {
		t.Fatalf("seed discovery release: %v", err)
	}

	before, err := service.GetAcquisitionRecord(ctx, downloadID)
	if err != nil || before.Release == nil || before.Release.ReleaseID != "release-record" || len(before.Stages) != 7 {
		t.Fatalf("record before expiry: %+v %v", before, err)
	}

	if deleted := purgeDiscoveryReleases(t, db, 500); deleted != 1 {
		t.Fatalf("only the discovery release may be deleted, deleted %d", deleted)
	}

	after, err := service.GetAcquisitionRecord(ctx, downloadID)
	if err != nil {
		t.Fatalf("the record must stay readable: %v", err)
	}
	if after.Release == nil || after.Release.ReleaseID != "release-record" {
		t.Fatalf("the record's release is acquisition evidence: %+v", after.Release)
	}
	if after.RecordDetail != "full" || len(after.Stages) != len(before.Stages) || after.State != before.State {
		t.Fatalf("the record changed with discovery expiry: before %+v after %+v", before, after)
	}
	if page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{}); err != nil || len(page.Items) != 2 {
		t.Fatalf("records stay listed: %+v %v", page.Items, err)
	}
}

// TestAcquisitionRecordReadsWithoutReleaseAfterOwnerCleanup proves the reader's
// post-release shape: when the owner lifecycle deletes the release (the local
// cleanup compacts the retired owner's roots in the same transaction), the
// record still reads with its title and outcome and no release link.
func TestAcquisitionRecordReadsWithoutReleaseAfterOwnerCleanup(t *testing.T) {
	service, _, _, failedID := seedRecordRoots(t)
	ctx := context.Background()
	owner := disposabledb.Owner(t)

	if _, err := owner.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1, retired_at = CURRENT_TIMESTAMP,
    retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1
WHERE id = 'anime-failed'`); err != nil {
		t.Fatalf("retire the owner: %v", err)
	}
	var epoch int64
	if err := owner.QueryRowContext(ctx, `SELECT fence_epoch FROM managed_anime WHERE id = 'anime-failed'`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	var cleanup string
	if err := owner.QueryRowContext(ctx, `SELECT public.cleanup_managed_anime_local_data('anime-failed', 'record-retention-cleanup', $1, $2)::text`,
		"sha256:"+strings.Repeat("0", 64), epoch).Scan(&cleanup); err != nil || !strings.Contains(cleanup, `"acquisitions_compacted": 1`) {
		t.Fatalf("owner cleanup must compact the release-bound root: %s %v", cleanup, err)
	}
	var releases int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM releases WHERE id = 'rel-failed'`).Scan(&releases); err != nil || releases != 0 {
		t.Fatalf("the owner lifecycle must have deleted the release: %d %v", releases, err)
	}

	record, err := service.GetAcquisitionRecord(ctx, failedID)
	if err != nil {
		t.Fatalf("a record whose release is gone must stay readable: %v", err)
	}
	if record.Release != nil {
		t.Fatalf("a deleted release must not surface a link: %+v", record.Release)
	}
	if record.Outcome == nil || record.Outcome.Outcome != "failed" || record.SourceTitle.State != AcquisitionDisplayVisible {
		t.Fatalf("the record must keep its receipt and title: %+v", record)
	}
	// The owner's local cleanup removed the managed anime in the same
	// transaction, so the anime link is gone too and nothing errors.
	if record.Anime != nil {
		t.Fatalf("a deleted owner must not surface an anime link: %+v", record.Anime)
	}
	if page, err := service.ListAcquisitionRecords(ctx, AcquisitionRecordQuery{}); err != nil {
		t.Fatalf("list: %v", err)
	} else {
		found := false
		for _, item := range page.Items {
			if item.AcquisitionID == failedID {
				found = true
			}
		}
		if !found {
			t.Fatalf("the compact record must stay listed: %+v", page.Items)
		}
	}
}
