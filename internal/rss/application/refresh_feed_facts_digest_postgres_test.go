package application

import (
	"context"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// TestRefreshDoesNotReenqueueOnEvaluationRewrittenFacts is the regression this
// change exists for: the evaluation stage rewrites parsed_facts and
// internal_facts with its decision-time spelling, so a poll that compares the
// stored columns against what the feed produced saw a permanent difference and
// re-ran the whole chain — evaluation and competition included — every cycle.
// A poll that observed nothing new now leaves the decided Release, its version
// and its evaluation alone.
func TestRefreshDoesNotReenqueueOnEvaluationRewrittenFacts(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{decidedRefreshItem(t)}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(version int64) {
		t.Helper()
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, version)
		outcome, err := service.Handle(context.Background(), claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish {
			t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	}
	refresh(1)

	ctx := context.Background()
	var releaseID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	var observedDigest string
	if err := db.QueryRowContext(ctx, `SELECT feed_facts_digest FROM releases WHERE id = $1`, releaseID).Scan(&observedDigest); err != nil {
		t.Fatal(err)
	}
	if len(observedDigest) != 64 {
		t.Fatalf("feed facts digest = %q, want a sha256 hex value", observedDigest)
	}

	// The evaluation stage decides the release and rewrites parsed_facts into
	// its own decision-time spelling: the batch plan places nothing, so the
	// parsed episode is cleared.
	var decidedVersion int64
	if err := db.QueryRowContext(ctx, `
UPDATE releases
SET verdict = 'filtered', reason_code = 'no_placeable_files',
    parsed_facts = '{"special": false, "group_name": "Test Group"}'::jsonb,
    version = version + 1
WHERE id = $1
RETURNING version`, releaseID).Scan(&decidedVersion); err != nil {
		t.Fatal(err)
	}

	// The same feed item again: nothing the refresh observed has changed, even
	// though the row's facts were rewritten under it.
	refresh(2)

	var version int64
	var verdict, reasonCode, digest string
	if err := db.QueryRowContext(ctx, `
SELECT version, verdict, COALESCE(reason_code, ''), feed_facts_digest
FROM releases WHERE id = $1`, releaseID).Scan(&version, &verdict, &reasonCode, &digest); err != nil {
		t.Fatal(err)
	}
	if version != decidedVersion {
		t.Fatalf("an unchanged poll advanced the release: version=%d want %d", version, decidedVersion)
	}
	if verdict != "filtered" || reasonCode != "no_placeable_files" {
		t.Fatalf("the refresh took back the decision: verdict=%q reason=%q", verdict, reasonCode)
	}
	if digest != observedDigest {
		t.Fatalf("an unchanged poll rewrote the feed facts digest: %q want %q", digest, observedDigest)
	}
	var liveItems int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM work_items
WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate' AND state IN ('available', 'leased')`, releaseID).Scan(&liveItems); err != nil {
		t.Fatal(err)
	}
	if liveItems != 1 {
		t.Fatalf("an unchanged poll left %d live evaluations, want the original one", liveItems)
	}

	// A real feed change still re-enqueues.
	changed := decidedRefreshItem(t)
	changed.Title = "[Group] Test Show - 01v2 1080p"
	fake.items = []acquisitiondomain.FeedItem{changed}
	refresh(3)
	if err := db.QueryRowContext(ctx, `SELECT version FROM releases WHERE id = $1`, releaseID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version <= decidedVersion {
		t.Fatalf("a changed feed item did not advance the release: version=%d want > %d", version, decidedVersion)
	}
	var evaluationVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT expected_owner_version FROM work_items
WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate' AND state IN ('available', 'leased')`, releaseID).Scan(&evaluationVersion); err != nil {
		t.Fatal(err)
	}
	if evaluationVersion != version {
		t.Fatalf("the re-enqueued evaluation targets version %d, want %d", evaluationVersion, version)
	}
}

// TestRefreshConvergesWhenEvaluationRewritesReusedTorrentFacts covers the
// metadata-outage variant of the churn regression. Evaluation owns a different
// parsed spelling and adds target facts to the stored file list. Reusing those
// facts may require one convergence write, but the next identical outage poll
// must leave the Release version and its evaluation untouched.
func TestRefreshConvergesWhenEvaluationRewritesReusedTorrentFacts(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	complete := decidedRefreshItem(t)
	fake := &rssFakeSourceFetcher{items: []acquisitiondomain.FeedItem{complete}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(version int64) {
		t.Helper()
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, version)
		outcome, err := service.Handle(context.Background(), claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish {
			t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	}
	refresh(1)

	ctx := context.Background()
	var releaseID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}

	decision := complete
	decision.ReleaseFiles = append([]acquisitiondomain.ReleaseFile(nil), complete.ReleaseFiles...)
	targetPath := "Test Show/Season 01/show-01.mkv"
	decision.ReleaseFiles[0].TargetPath = &targetPath
	decisionInternal, err := encodeInternalFeedFacts(decision)
	if err != nil {
		t.Fatal(err)
	}
	var decidedVersion int64
	if err := db.QueryRowContext(ctx, `
UPDATE releases
SET verdict = 'filtered', reason_code = 'no_placeable_files',
    parsed_facts = '{"special": false, "group_name": "Test Group"}'::jsonb,
    internal_facts = $2::jsonb, version = version + 1
WHERE id = $1
RETURNING version`, releaseID, decisionInternal).Scan(&decidedVersion); err != nil {
		t.Fatal(err)
	}

	unavailable := complete
	unavailable.TorrentPayload = nil
	unavailable.TorrentInfoHash = ""
	unavailable.TorrentInfoHashV1 = ""
	unavailable.TorrentInfoHashV2 = ""
	unavailable.TorrentVersion = ""
	unavailable.TorrentFilename = ""
	unavailable.TorrentSizeBytes = 0
	unavailable.ReleaseFiles = nil
	unavailable.TorrentErrorCode = "torrent_metadata_unavailable"
	unavailable.TorrentErrorReason = "temporary metadata outage"
	fake.items = []acquisitiondomain.FeedItem{unavailable}
	refresh(2)

	var reusedVersion int64
	var reusedTarget, reusedDigest string
	if err := db.QueryRowContext(ctx, `
SELECT version, COALESCE(internal_facts #>> '{release_files,0,target_path}', ''), feed_facts_digest
FROM releases WHERE id = $1`, releaseID).Scan(&reusedVersion, &reusedTarget, &reusedDigest); err != nil {
		t.Fatal(err)
	}
	if reusedVersion <= decidedVersion || reusedTarget != targetPath {
		t.Fatalf("metadata reuse did not persist the evaluated facts: version=%d target=%q", reusedVersion, reusedTarget)
	}

	refresh(3)
	var version int64
	var targetAfter, digestAfter string
	if err := db.QueryRowContext(ctx, `
SELECT version, COALESCE(internal_facts #>> '{release_files,0,target_path}', ''), feed_facts_digest
FROM releases WHERE id = $1`, releaseID).Scan(&version, &targetAfter, &digestAfter); err != nil {
		t.Fatal(err)
	}
	if version != reusedVersion {
		t.Fatalf("an unchanged metadata-reuse poll advanced the release: version=%d want %d", version, reusedVersion)
	}
	if targetAfter != targetPath || digestAfter != reusedDigest {
		t.Fatalf("an unchanged metadata-reuse poll rewrote the converged facts: target=%q digest=%q", targetAfter, digestAfter)
	}
	var evaluationVersion int64
	if err := db.QueryRowContext(ctx, `
SELECT expected_owner_version FROM work_items
WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate' AND state IN ('available', 'leased')`, releaseID).Scan(&evaluationVersion); err != nil {
		t.Fatal(err)
	}
	if evaluationVersion != reusedVersion {
		t.Fatalf("an unchanged metadata-reuse poll touched the evaluation: version=%d want %d", evaluationVersion, reusedVersion)
	}
}
