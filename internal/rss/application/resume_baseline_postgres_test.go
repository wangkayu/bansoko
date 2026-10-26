package application

import (
	"context"
	"database/sql"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/persistence/txscope"
	retentionpersistence "bansoko/internal/recordretention/adapter/persistence"
	"bansoko/internal/workitems"
)

func countQuery(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// Stop -> the discovery janitor purges the stopped subscription's history ->
// resume -> the first refresh re-reads the same feed window. That refresh only
// records the baseline, so the purged releases
// come back as rows but nothing is discovered, evaluated or notified. A
// genuinely new item on the next refresh is discovered as usual.
func TestResumeRefreshRecordsBaselineAfterPurge(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	window := []acquisitiondomain.FeedItem{discoveryFeedItem(t, 1), discoveryFeedItem(t, 2)}
	fetcher := &rssFakeSourceFetcher{items: window}
	service, err := newRSSService(t, db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	notices := &discoveryNotices{}
	service.SetBusinessDeliveryEnqueuer(notices)

	// The subscription's first scan, then a stop long enough for the janitor.
	runDiscoveryRefresh(t, db, store, service, sourceID, "first")
	if _, err := db.ExecContext(ctx, `
UPDATE subscriptions SET state = 'stopped', version = version + 1, updated_at = CURRENT_TIMESTAMP - INTERVAL '31 days'
WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	evaluateDueReleases(t, store, service)
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1 AND download_task_id IS NULL`, sourceID); n != 2 {
		t.Fatalf("discovery-only releases before the purge = %d, want 2", n)
	}
	// Run-log rows naming a release hold it until their own 30-day bound; the
	// stop is 31 days old, so they have expired as well.
	if _, err := ownerTestDB(t).ExecContext(ctx, `DELETE FROM subscription_log_entries WHERE release_id IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	purged, err := retentionpersistence.NewRepository(db).PurgeExpiredDiscoveryReleases(ctx, txscope.Wrap(tx), 500)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if purged != 2 {
		t.Fatalf("purged %d releases, want 2", purged)
	}

	// Resume, exactly as the subscription PATCH writes it.
	if _, err := db.ExecContext(ctx, `
UPDATE subscriptions SET state = 'active', version = version + 1, updated_at = NOW(), resumed_at = NOW()
WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	runDiscoveryRefresh(t, db, store, service, sourceID, "resume")
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1`, sourceID); n != 2 {
		t.Fatalf("the baseline refresh must record the feed window, releases = %d", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1 AND discovery_notice_due`, sourceID); n != 0 {
		t.Fatalf("the baseline refresh marked %d discoveries", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM work_items WHERE kind = 'release_evaluate'`); n != 0 {
		t.Fatalf("the baseline refresh scheduled %d evaluations", n)
	}
	if ran := evaluateDueReleases(t, store, service); ran != 0 {
		t.Fatalf("evaluations ran after the baseline refresh: %d", ran)
	}
	if got := notices.list(); len(got) != 0 {
		t.Fatalf("the baseline refresh admitted notices: %+v", got)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM subscription_log_entries WHERE subscription_id = 'sub-rss-test' AND reason_code = 'rss_new_content' AND occurred_at >= (SELECT resumed_at FROM subscriptions WHERE id = 'sub-rss-test')`); n != 0 {
		t.Fatalf("the baseline refresh logged new content %d times", n)
	}
	if n := countQuery(t, db, `
SELECT count(*) FROM rss_source_runtime rt JOIN subscriptions sub ON sub.id = 'sub-rss-test'
WHERE rt.rss_source_id = $1 AND rt.resume_baseline_at = sub.resumed_at`, sourceID); n != 1 {
		t.Fatal("the baseline refresh must record the resume it answered")
	}

	// The baseline items carry the durable baseline verdict.
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1 AND verdict = 'resume_baseline'`, sourceID); n != 2 {
		t.Fatalf("baseline releases = %d, want 2", n)
	}
	var baselineID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM releases WHERE rss_source_id = $1 AND stable_item_key = $2`, sourceID, "guid-discovery-1").Scan(&baselineID); err != nil {
		t.Fatal(err)
	}
	// Every evaluation entry refuses it: scheduling, the worker and retry.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateOrMergeReleaseEvaluation(ctx, txscope.Wrap(tx2), store, baselineID, currentReleaseVersion(t, db, baselineID), "test"); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM work_items WHERE kind = 'release_evaluate'`); n != 0 {
		t.Fatalf("a baseline release was scheduled for evaluation")
	}
	forced := createKeyedRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: baselineID}, workitems.KindReleaseEvaluate, "forced", currentReleaseVersion(t, db, baselineID))
	finishReleaseClaim(t, store, service, forced)
	if decision := ReleaseRetryability(ReleaseRetryFacts{Verdict: "resume_baseline", LifecycleState: "active"}); decision.Allowed || decision.ReasonCode != RetryReasonPresentAtResume {
		t.Fatalf("retry decision = %+v", decision)
	}

	// A later refresh is ordinary: a baseline item whose facts changed stays
	// unevaluated, and only the genuinely new item is discovered, evaluated
	// and notified.
	changed := discoveryFeedItem(t, 1)
	changed.Title = "[Group] Test Show - 01 v2 1080p"
	fetcher.items = []acquisitiondomain.FeedItem{changed, discoveryFeedItem(t, 2), discoveryFeedItem(t, 3)}
	runDiscoveryRefresh(t, db, store, service, sourceID, "after")
	var newReleaseID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM releases WHERE rss_source_id = $1 AND discovery_notice_due`, sourceID).Scan(&newReleaseID); err != nil {
		t.Fatalf("the refresh after the baseline must mark exactly the new release: %v", err)
	}
	if ran := evaluateDueReleases(t, store, service); ran != 1 {
		t.Fatalf("evaluations after the baseline = %d, want 1 (the new item)", ran)
	}
	got := notices.list()
	if len(got) != 1 || got[0].SourceOwnerID != newReleaseID {
		t.Fatalf("notices = %+v, want one for %s", got, newReleaseID)
	}
	var verdict string
	if err := db.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE id = $1`, baselineID).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	if verdict != "resume_baseline" {
		t.Fatalf("baseline release verdict after a facts change = %s", verdict)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1 AND verdict = 'resume_baseline'`, sourceID); n != 2 {
		t.Fatalf("baseline releases after the later refresh = %d, want 2", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM work_items WHERE kind = 'release_evaluate'`); n != 0 {
		t.Fatalf("evaluations left after the later refresh = %d", n)
	}
}

// A source linked after the resume is not answered by the resume: its first
// scan takes the ordinary baseline (no notice) but evaluates as a new source.
func TestResumeBaselineSkipsSourcesLinkedAfterResume(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	if _, err := ownerTestDB(t).ExecContext(ctx, `
UPDATE subscriptions SET resumed_at = CURRENT_TIMESTAMP - INTERVAL '1 hour' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{discoveryFeedItem(t, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	runDiscoveryRefresh(t, db, store, service, sourceID, "first")
	if n := countQuery(t, db, `SELECT count(*) FROM work_items WHERE kind = 'release_evaluate'`); n != 1 {
		t.Fatalf("a source linked after the resume must evaluate its first scan, evaluations = %d", n)
	}
}
