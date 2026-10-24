package application

import (
	"bansoko/internal/testutil/storedsections"
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Regression coverage for the runtime-health gap (R1/B0): a work item whose
// owner chain can no longer be resolved (JOIN to anime_metadata fails with
// ErrNoRows, e.g. a broken fixture with no metadata row, or an anime deleted
// out from under a pending item) must be terminated instead of retried
// forever. Observed in production-like dev as "RSS source not found" every two
// minutes for ~12 hours on a single orphaned rss_refresh item.

func TestRefreshOrphanSourceWithoutMetadataTerminates(t *testing.T) {
	db := openRSSDatabase(t)
	// seedRSSOwner with metadataComplete=false still writes a metadata row
	// (with NULL bindings), so the JOIN succeeds and the source is not orphaned.
	// Reproduce the broken-fixture shape exactly: no anime_metadata row at
	// all, matching the src-purge residue that caused the observed zombie.
	seedAnimeWithSourcesNoMetadata(t, db, "anime-orphan-src", "sub-orphan-src", "source-orphan-src")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}

	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: "source-orphan-src"}, workitems.KindRSSRefresh, 1)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatalf("orphan refresh must return a terminal outcome, not an error: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("orphan refresh must finish as failed, got kind=%s terminal=%s", outcome.Kind, outcome.TerminalState)
	}
	if outcome.OwnerResult.ReasonCode != "source_unavailable" {
		t.Fatalf("orphan refresh reason code = %q, want source_unavailable", outcome.OwnerResult.ReasonCode)
	}
	if outcome.WriteOwner != nil {
		t.Fatal("orphan refresh must not attempt an owner write: the owner row is gone")
	}
	// The runtime row still exists but has no version bump and stays 'ready':
	// the item is simply retired because it can never succeed.
	assertRuntimeStillReady(t, db, "source-orphan-src")
}

func TestReleaseEvaluateOrphanReleaseWithoutMetadataTerminates(t *testing.T) {
	db := openRSSDatabase(t)
	seedAnimeWithSourcesNoMetadata(t, db, "anime-orphan-rel", "sub-orphan-rel", "source-orphan-rel")
	insertReleaseForEvaluation(t, db, "source-orphan-rel", "release-orphan-rel", "[Group] Orphan - 01", "https://downloads.example.test/orphan-01.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}

	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-orphan-rel"}, workitems.KindReleaseEvaluate, 1)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatalf("orphan release evaluate must return a terminal outcome, not an error: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("orphan release evaluate must finish as failed, got kind=%s terminal=%s", outcome.Kind, outcome.TerminalState)
	}
	if outcome.OwnerResult.ReasonCode != "release_unavailable" {
		t.Fatalf("orphan release evaluate reason code = %q, want release_unavailable", outcome.OwnerResult.ReasonCode)
	}
	if outcome.WriteOwner != nil {
		t.Fatal("orphan release evaluate must not attempt an owner write: the owner row is gone")
	}
}

// seedAnimeWithSourcesNoMetadata creates a subscription + source + runtime but
// deliberately NO anime_metadata row, reproducing the exact broken-fixture
// shape (src-purge) where the snapshot JOIN returns zero rows.
func seedAnimeWithSourcesNoMetadata(t *testing.T, db *sql.DB, animeID, subscriptionID, sourceID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Orphan Show')`, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ($1, $2, 'custom', 'Test', 'active')`, subscriptionID, animeID); err != nil {
		t.Fatal(err)
	}
	for range []string{"subgroups", "rules"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id) VALUES ($1) ON CONFLICT (managed_anime_id) DO NOTHING`, animeID); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, animeID)
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', 'https://feeds.example.test/rss.xml', 'https://feeds.example.test/rss.xml', 'Group', 'Orphan Show')`, sourceID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)`, sourceID); err != nil {
		t.Fatal(err)
	}
}

func assertRuntimeStillReady(t *testing.T, db *sql.DB, sourceID string) {
	t.Helper()
	var version int64
	var health string
	if err := db.QueryRowContext(context.Background(), `
SELECT version, health FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&version, &health); err != nil {
		t.Fatalf("read runtime after orphan terminate: %v", err)
	}
	// The freshly seeded runtime row is 'unknown' (column default) and version 1.
	// Terminating the orphan item must not bump it or change its health.
	if version != 1 || health != "unknown" {
		t.Fatalf("runtime changed on orphan terminate: version=%d health=%s, want 1/unknown", version, health)
	}
}
