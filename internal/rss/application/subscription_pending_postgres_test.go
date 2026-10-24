package application

import (
	"context"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/workitems"
)

// A pending (never started) subscription does no work: a refresh of its
// source, manual included, stores nothing, and a forced evaluation writes no
// verdict. Its first start is a normal first scan, never a resume baseline:
// the feed is stored, evaluated and admitted like a new subscription's.
func TestPendingSubscriptionDoesNoWorkAndFirstStartIsNotABaseline(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = 'pending' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{discoveryFeedItem(t, 1), discoveryFeedItem(t, 2)}}
	service, err := newRSSService(t, db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	notices := &discoveryNotices{}
	service.SetBusinessDeliveryEnqueuer(notices)

	runDiscoveryRefresh(t, db, store, service, sourceID, "pending")
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1`, sourceID); n != 0 {
		t.Fatalf("a pending refresh stored %d releases", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM rss_source_runtime WHERE rss_source_id = $1 AND last_refresh_at IS NOT NULL`, sourceID); n != 0 {
		t.Fatal("a pending refresh recorded a refresh on the source runtime")
	}
	if n := countQuery(t, db, `SELECT count(*) FROM work_items WHERE kind = 'release_evaluate'`); n != 0 {
		t.Fatalf("a pending refresh scheduled %d evaluations", n)
	}

	// The first start, exactly as the subscription PATCH writes it: the state
	// moves to active and resumed_at stays unset.
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = 'active', version = version + 1, updated_at = NOW() WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	runDiscoveryRefresh(t, db, store, service, sourceID, "first-start")
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1`, sourceID); n != 2 {
		t.Fatalf("the first refresh after start stored %d releases, want 2", n)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1 AND verdict = 'resume_baseline'`, sourceID); n != 0 {
		t.Fatalf("the first start took a resume baseline for %d releases", n)
	}
	if ran := evaluateDueReleases(t, store, service); ran != 2 {
		t.Fatalf("evaluations after the first start = %d, want 2", ran)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1 AND verdict IN ('discovered', 'subscription_stopped', 'resume_baseline')`, sourceID); n != 0 {
		t.Fatalf("%d releases were not evaluated as a started subscription", n)
	}
}

// A release_evaluate item for a pending subscription ends without a verdict.
func TestPendingSubscriptionEvaluationWritesNoVerdict(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{discoveryFeedItem(t, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	runDiscoveryRefresh(t, db, store, service, sourceID, "seed")
	var releaseID, before string
	if err := db.QueryRowContext(ctx, `SELECT id, verdict FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID, &before); err != nil {
		t.Fatal(err)
	}
	// Fence the owner back to pending before the scheduled evaluation runs: no
	// path returns there, so this only isolates the worker gate.
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = 'pending' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	if ran := evaluateDueReleases(t, store, service); ran != 1 {
		t.Fatalf("evaluations run = %d, want 1", ran)
	}
	var after string
	if err := db.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE id = $1`, releaseID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("a pending evaluation changed the verdict %s -> %s", before, after)
	}
	if decision := ReleaseRetryability(ReleaseRetryFacts{Verdict: before, LifecycleState: "active", SubscriptionState: "pending"}); decision.Allowed || decision.ReasonCode != RetryReasonNotStarted {
		t.Fatalf("retry decision for a pending subscription = %+v", decision)
	}
}
