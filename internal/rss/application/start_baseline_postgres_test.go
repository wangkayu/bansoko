package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// enableNewReleasesOnly gives the fixture subscription the new-releases-only option
// with a creation time createdAgo in the past, exactly as a subscribe that
// froze it would: the option is set while pending, then the instance starts.
func enableNewReleasesOnly(t *testing.T, createdAgo time.Duration) time.Time {
	t.Helper()
	owner := ownerTestDB(t)
	ctx := context.Background()
	if _, err := owner.ExecContext(ctx, `UPDATE subscriptions SET created_at = NOW() - make_interval(secs => $2) WHERE id = $1`, "sub-rss-test", createdAgo.Seconds()); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE subscriptions SET state = 'pending' WHERE id = $1`,
		`UPDATE subscriptions SET new_releases_only = true, start_baseline_at = created_at WHERE id = $1`,
		`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE id = $1`,
	} {
		if _, err := owner.ExecContext(ctx, statement, "sub-rss-test"); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	var baseline time.Time
	if err := owner.QueryRowContext(ctx, `SELECT start_baseline_at FROM subscriptions WHERE id = 'sub-rss-test'`).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	return baseline
}

func publishedFeedItem(t *testing.T, n int, published time.Time) acquisitiondomain.FeedItem {
	t.Helper()
	item := discoveryFeedItem(t, n)
	item.Published = published
	return item
}

func releaseByKey(t *testing.T, db *sql.DB, sourceID, key string) (id, verdict, reason string, hasTask bool) {
	t.Helper()
	var task sql.NullString
	if err := db.QueryRowContext(context.Background(), `
SELECT id, verdict, reason_code, download_task_id FROM releases WHERE rss_source_id = $1 AND stable_item_key = $2`, sourceID, key).Scan(&id, &verdict, &reason, &task); err != nil {
		t.Fatalf("read release %s: %v", key, err)
	}
	return id, verdict, reason, task.Valid
}

// With new-releases-only, a Release published before the
// instance was created, or without a publish time, takes the quiet terminal
// verdict start_baseline without being evaluated. A Release published after
// the creation, including while the instance was still pending, is new and is
// evaluated normally. Automatic re-evaluation never reaches a baseline row.
func TestStartBaselineVerdicts(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	baseline := enableNewReleasesOnly(t, 24*time.Hour)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{
		publishedFeedItem(t, 1, baseline.Add(-48*time.Hour)),        // before the creation
		publishedFeedItem(t, 2, time.Time{}),                        // no publish time
		publishedFeedItem(t, 3, baseline.Add(12*time.Hour)),         // while pending
		publishedFeedItem(t, 4, time.Now().UTC().Add(-time.Minute)), // after the start
	}}
	service, err := newRSSService(t, db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	notices := &discoveryNotices{}
	service.SetBusinessDeliveryEnqueuer(notices)

	runDiscoveryRefresh(t, db, store, service, sourceID, "first")
	if ran := evaluateDueReleases(t, store, service); ran != 4 {
		t.Fatalf("evaluations = %d, want 4", ran)
	}
	for _, key := range []string{"guid-discovery-1", "guid-discovery-2"} {
		if _, verdict, reason, task := releaseByKey(t, db, sourceID, key); verdict != ReleaseVerdictStartBaseline || reason != ReleaseVerdictStartBaseline || task {
			t.Fatalf("%s: verdict=%s reason=%s task=%v, want start_baseline without a task", key, verdict, reason, task)
		}
	}
	for _, key := range []string{"guid-discovery-3", "guid-discovery-4"} {
		if _, verdict, _, task := releaseByKey(t, db, sourceID, key); verdict != "accepted_pending_download" || !task {
			t.Fatalf("%s: verdict=%s task=%v, want a new release accepted", key, verdict, task)
		}
	}
	// A later refresh discovers an item published before the creation: it is
	// recorded quietly (no discovery notice, no failure), and a genuinely new
	// item is discovered and notified as usual.
	fetcher.items = append(fetcher.items,
		publishedFeedItem(t, 5, baseline.Add(-time.Hour)),
		publishedFeedItem(t, 6, time.Now().UTC()),
	)
	runDiscoveryRefresh(t, db, store, service, sourceID, "second")
	if ran := evaluateDueReleases(t, store, service); ran != 2 {
		t.Fatalf("evaluations of the second refresh = %d, want 2", ran)
	}
	oldID, verdict, _, _ := releaseByKey(t, db, sourceID, "guid-discovery-5")
	if verdict != ReleaseVerdictStartBaseline {
		t.Fatalf("a late old release verdict = %s, want start_baseline", verdict)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE id = $1 AND discovery_notice_due`, oldID); n != 0 {
		t.Fatal("a start_baseline release kept its discovery notice")
	}
	newID, _, _, _ := releaseByKey(t, db, sourceID, "guid-discovery-6")
	got := notices.list()
	if len(got) != 1 || got[0].SourceOwnerID != newID {
		t.Fatalf("notices = %+v, want exactly one for the new release %s", got, newID)
	}

	// A rule change re-evaluates nothing that is start_baseline: scheduling
	// refuses it, a forced worker claim leaves it, and a later refresh with
	// changed facts keeps it.
	filter, err := json.Marshal(map[string]any{"must_contain": "", "not_in": "Show - 03"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE anime_config_overrides SET schema_version = 'release-rules-v1', value = $1
WHERE managed_anime_id = 'anime-rss-test' AND section_name = 'release_rules'`, filter); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateOrMergeReleaseEvaluation(ctx, txscope.Wrap(tx), store, oldID, currentReleaseVersion(t, db, oldID), "rules_changed"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countQuery(t, db, `SELECT count(*) FROM work_items WHERE kind = 'release_evaluate' AND owner_id = $1 AND state IN ('available', 'leased')`, oldID); n != 0 {
		t.Fatal("a start_baseline release was scheduled for re-evaluation")
	}
	forced := createKeyedRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: oldID}, workitems.KindReleaseEvaluate, "forced-baseline", currentReleaseVersion(t, db, oldID))
	finishReleaseClaim(t, store, service, forced)
	changed := publishedFeedItem(t, 1, baseline.Add(-48*time.Hour))
	changed.Title = "[Group] Test Show - 01 v2 1080p"
	fetcher.items[0] = changed
	runDiscoveryRefresh(t, db, store, service, sourceID, "third")
	evaluateDueReleases(t, store, service)
	for _, key := range []string{"guid-discovery-1", "guid-discovery-2", "guid-discovery-5"} {
		if _, verdict, _, _ := releaseByKey(t, db, sourceID, key); verdict != ReleaseVerdictStartBaseline {
			t.Fatalf("%s after a rule change = %s, want start_baseline", key, verdict)
		}
	}
	if decision := ReleaseRetryability(ReleaseRetryFacts{Verdict: ReleaseVerdictStartBaseline, LifecycleState: "active", SubscriptionState: "active"}); !decision.Allowed {
		t.Fatalf("a start_baseline release must be retryable: %+v", decision)
	}
}

// The manual download of a start_baseline release is the ordinary retry: it
// skips only the baseline gate and evaluates through every rule, so a release
// the rules filter stays filtered and an admissible one is accepted.
func TestStartBaselineManualRetryEvaluatesThroughRules(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	baseline := enableNewReleasesOnly(t, 24*time.Hour)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{
		publishedFeedItem(t, 1, baseline.Add(-48*time.Hour)),
		publishedFeedItem(t, 2, time.Time{}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runDiscoveryRefresh(t, db, store, service, sourceID, "first")
	evaluateDueReleases(t, store, service)
	filteredID, verdict, _, _ := releaseByKey(t, db, sourceID, "guid-discovery-1")
	acceptedID, verdict2, _, _ := releaseByKey(t, db, sourceID, "guid-discovery-2")
	if verdict != ReleaseVerdictStartBaseline || verdict2 != ReleaseVerdictStartBaseline {
		t.Fatalf("verdicts before retry = %s, %s", verdict, verdict2)
	}

	filter, err := json.Marshal(map[string]any{"must_contain": "", "not_in": "Show - 01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE anime_config_overrides SET schema_version = 'release-rules-v1', value = $1
WHERE managed_anime_id = 'anime-rss-test' AND section_name = 'release_rules'`, filter); err != nil {
		t.Fatal(err)
	}
	result, err := service.RetryRelease(ctx, filteredID, "retry-key-baseline-filtered")
	if err != nil {
		t.Fatalf("retry of a filtered baseline release: %v", err)
	}
	if result.Outcome != RetryOutcomeFiltered || result.DownloadTaskID != "" {
		t.Fatalf("retry result = %+v, want filtered by the rules", result)
	}
	if _, verdict, _, task := releaseByKey(t, db, sourceID, "guid-discovery-1"); verdict != "filtered" || task {
		t.Fatalf("filtered retry stored verdict=%s task=%v", verdict, task)
	}

	result, err = service.RetryRelease(ctx, acceptedID, "retry-key-baseline-accepted")
	if err != nil {
		t.Fatalf("retry of an admissible baseline release: %v", err)
	}
	if result.Outcome != RetryOutcomeAccepted || result.DownloadTaskID == "" {
		t.Fatalf("retry result = %+v, want accepted with a download task", result)
	}
	if _, verdict, _, task := releaseByKey(t, db, sourceID, "guid-discovery-2"); verdict == ReleaseVerdictStartBaseline || !task {
		t.Fatalf("accepted retry stored verdict=%s task=%v", verdict, task)
	}
}

// Without the option nothing changes: an old release is evaluated as before.
func TestStartBaselineOffEvaluatesOldReleases(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{
		publishedFeedItem(t, 1, time.Now().UTC().Add(-30*24*time.Hour)),
		publishedFeedItem(t, 2, time.Time{}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runDiscoveryRefresh(t, db, store, service, sourceID, "first")
	evaluateDueReleases(t, store, service)
	if n := countQuery(t, db, `SELECT count(*) FROM releases WHERE rss_source_id = $1 AND verdict = 'start_baseline'`, sourceID); n != 0 {
		t.Fatalf("start_baseline releases without the option = %d", n)
	}
}
