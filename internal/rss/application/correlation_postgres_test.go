package application

import (
	"context"
	"testing"

	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

func TestReleaseEvaluateEstablishesCorrelationLineage(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	insertReleaseForEvaluation(t, db, sourceID, "release-corr", "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")

	// A program-owned torrent payload lets the accepted release create a
	// DownloadTask at the task boundary.
	seedNativeTorrent(t, db, "torrent-corr", "release-corr")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-corr"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-corr"))
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-corr", "accepted_pending_download", "accepted", true)

	ctx := context.Background()
	var correlationID string
	if err := db.QueryRowContext(ctx, `
SELECT correlation_id FROM download_tasks WHERE release_id = 'release-corr'`).Scan(&correlationID); err != nil {
		t.Fatal(err)
	}
	if correlationID == "" || len(correlationID) < 8 {
		t.Fatalf("download task correlation=%q, want a generated lineage id", correlationID)
	}

	// The release_processed Activity carries the same lineage id, so the
	// Business Logs chain is joinable from the very first owner.
	var activityCorrelation string
	var kind string
	if err := db.QueryRowContext(ctx, `
SELECT correlation_id, kind FROM subscription_log_entries
WHERE subject_type = 'release' AND subject_id = 'release-corr'
ORDER BY occurred_at DESC LIMIT 1`).Scan(&activityCorrelation, &kind); err != nil {
		t.Fatal(err)
	}
	if kind != "release_processed" {
		t.Fatalf("activity kind=%s, want release_processed", kind)
	}
	if activityCorrelation != correlationID {
		t.Fatalf("activity correlation=%q, want %q (same lineage)", activityCorrelation, correlationID)
	}
}

var _ = worker.Outcome{}
var _ = parser.ParserRegex
