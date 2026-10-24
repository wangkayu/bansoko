package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"bansoko/internal/acquisition/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// TestRefreshSameItemTwiceIsIdempotent asserts the refresh merge semantics for
// a repeated feed item: the second refresh creates no duplicate release and,
// when the facts are unchanged, leaves the existing row untouched (same
// version and verdict). The source runtime reports ready after both passes.
func TestRefreshSameItemTwiceIsIdempotent(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{items: []domain.FeedItem{{GUID: "guid-idempotent", Title: "[Group] Test Show - 01 1080p"}}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}

	refresh := func(expectedVersion int64, idempotencyKey string) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		id, err := store.Create(ctx, tx, workitems.WorkItemSpec{
			Owner: workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID},
			Kind:  workitems.KindRSSRefresh, IdempotencyKey: idempotencyKey,
			InputSnapshot: json.RawMessage(`{"trigger":"test"}`), MaxAttempts: 3, ExpectedOwnerVersion: expectedVersion,
		})
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		claim, err := store.ClaimDue(ctx, "rss-test-worker", []string{workitems.KindRSSRefresh}, time.Minute)
		if err != nil || claim.ID != id {
			t.Fatalf("claim refresh: id=%s claim=%+v err=%v", id, claim, err)
		}
		outcome, err := service.Handle(ctx, claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
			t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	}

	refresh(1, "rss_refresh:test-owner-first")
	var releaseID, verdict string
	var version int64
	if err := db.QueryRowContext(ctx, `
SELECT id, verdict, version FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&releaseID, &verdict, &version); err != nil {
		t.Fatal(err)
	}
	if verdict != "accepted_pending_download" || version != 1 {
		t.Fatalf("first refresh release verdict=%q version=%d, want accepted_pending_download v1", verdict, version)
	}

	refresh(2, "rss_refresh:test-owner-second")
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("second refresh produced %d releases, want 1 (no duplicate)", count)
	}
	var replayVerdict string
	var replayVersion int64
	if err := db.QueryRowContext(ctx, `SELECT verdict, version FROM releases WHERE id = $1`, releaseID).Scan(&replayVerdict, &replayVersion); err != nil {
		t.Fatal(err)
	}
	if replayVerdict != verdict || replayVersion != version {
		t.Fatalf("second refresh overwrote facts: verdict=%q/%q version=%d/%d", replayVerdict, verdict, replayVersion, version)
	}

	var health string
	var reasonCode sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT health, reason_code FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&health, &reasonCode); err != nil {
		t.Fatal(err)
	}
	if health != "ready" || reasonCode.Valid {
		t.Fatalf("runtime health=%q reason=%v after two refreshes, want ready with no reason", health, reasonCode.Valid)
	}
}

// TestReleaseEvaluateReplayKeepsTerminalOwnerState asserts that evaluating a
// release that already has a committed download task is a no-op replay: the
// verdict, version, and download task are unchanged, and the work item still
// reaches a terminal state.
func TestReleaseEvaluateReplayKeepsTerminalOwnerState(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	insertReleaseForEvaluation(t, db, sourceID, "release-replay", "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")
	seedNativeTorrent(t, db, "torrent-replay", "release-replay")
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}

	firstClaim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-replay"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-replay"))
	firstOutcome, err := service.Handle(context.Background(), firstClaim)
	if err != nil || firstOutcome.Kind != worker.OutcomeFinish {
		t.Fatalf("first evaluate outcome=%+v err=%v", firstOutcome, err)
	}
	if err := store.FinishWithResult(context.Background(), firstClaim, firstOutcome.TerminalState, firstOutcome.OwnerResult, firstOutcome.WriteOwner); err != nil {
		t.Fatal(err)
	}

	var verdict string
	var version int64
	var taskID sql.NullString
	if err := db.QueryRowContext(context.Background(), `
SELECT verdict, version, download_task_id FROM releases WHERE id = 'release-replay'`).Scan(&verdict, &version, &taskID); err != nil {
		t.Fatal(err)
	}
	if verdict != "accepted_pending_download" || !taskID.Valid {
		t.Fatalf("first evaluate verdict=%q task=%v, want accepted_pending_download with task", verdict, taskID.Valid)
	}

	// Replaying the evaluation must not rebuild the task or rewrite the verdict.
	replayClaim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-replay"}, workitems.KindReleaseEvaluate, version)
	replayOutcome, err := service.Handle(context.Background(), replayClaim)
	if err != nil || replayOutcome.Kind != worker.OutcomeFinish {
		t.Fatalf("replay evaluate outcome=%+v err=%v", replayOutcome, err)
	}
	if err := store.FinishWithResult(context.Background(), replayClaim, replayOutcome.TerminalState, replayOutcome.OwnerResult, replayOutcome.WriteOwner); err != nil {
		t.Fatal(err)
	}

	var replayVerdict string
	var replayVersion int64
	var replayTaskID sql.NullString
	if err := db.QueryRowContext(context.Background(), `
SELECT verdict, version, download_task_id FROM releases WHERE id = 'release-replay'`).Scan(&replayVerdict, &replayVersion, &replayTaskID); err != nil {
		t.Fatal(err)
	}
	if replayVerdict != verdict || replayVersion != version || replayTaskID.String != taskID.String {
		t.Fatalf("replay changed owner: verdict=%q/%q version=%d/%d task=%q/%q",
			replayVerdict, verdict, replayVersion, version, replayTaskID.String, taskID.String)
	}
	var taskCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM download_tasks WHERE release_id = 'release-replay'`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("replay created %d download tasks, want 1", taskCount)
	}
	var replayItemState string
	if err := db.QueryRowContext(context.Background(), `
SELECT state FROM work_item_history WHERE owner_type = 'release' AND owner_id = 'release-replay'
ORDER BY created_at DESC LIMIT 1`).Scan(&replayItemState); err != nil {
		t.Fatal(err)
	}
	if replayItemState != workitems.StateSucceeded {
		t.Fatalf("replay work item state=%q, want succeeded", replayItemState)
	}
}

// TestRSSSourceRuntimeHealthTransitionMatrix asserts the health state machine
// of RSSSourceRuntime in one chain: a transient fetch failure degrades the
// source (retryable) and the next claim recovers it to ready; an unsupported
// locator then fails it terminally; a later successful refresh restores ready
// and clears the reason. Each transition advances the runtime version, and
// each step reuses or creates the live work item as the owner state machine
// requires.
func TestRSSSourceRuntimeHealthTransitionMatrix(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	ctx := context.Background()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &rssFakeSourceFetcher{items: []domain.FeedItem{{GUID: "guid-health", Title: "[Group] Test Show - 01 1080p"}}}
	service, err := newRSSService(t, db, store, fake)
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeHealth := func(t *testing.T, wantHealth string, wantReason *string) {
		t.Helper()
		var health string
		var reasonCode sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT health, reason_code FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&health, &reasonCode); err != nil {
			t.Fatal(err)
		}
		if health != wantHealth || reasonCode.Valid != (wantReason != nil) {
			t.Fatalf("runtime health=%q reason=%v, want %q reason=%v", health, reasonCode.Valid, wantHealth, wantReason != nil)
		}
		if wantReason != nil && (!reasonCode.Valid || reasonCode.String != *wantReason) {
			t.Fatalf("runtime reason=%v, want %q", reasonCode.String, *wantReason)
		}
	}
	// currentVersion is the committed runtime version after the previous step;
	// the next refresh work item must claim against it.
	currentVersion := int64(1)

	t.Run("transient fetch failure degrades then recovers", func(t *testing.T) {
		fake.err = errors.New("transient fetch failure")
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, currentVersion)
		outcome, err := service.Handle(ctx, claim)
		if err != nil || outcome.Kind != worker.OutcomeRetry || outcome.ReasonCode != "feed_fetch_failed" {
			t.Fatalf("degraded refresh outcome=%+v err=%v", outcome, err)
		}
		if _, err := store.Retry(ctx, claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		reason := "source_degraded"
		assertRuntimeHealth(t, "degraded", &reason)

		// The retried work item is live again; the next claim on the same row
		// with the recovered fetcher restores ready and terminates it.
		fake.err = nil
		makeRSSWorkItemDue(t, db, claim.ID)
		recoveryClaim, err := store.ClaimDue(ctx, "rss-test-worker", []string{workitems.KindRSSRefresh}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		recoveryOutcome, err := service.Handle(ctx, recoveryClaim)
		if err != nil || recoveryOutcome.Kind != worker.OutcomeFinish || recoveryOutcome.TerminalState != workitems.StateSucceeded {
			t.Fatalf("recovery refresh outcome=%+v err=%v", recoveryOutcome, err)
		}
		if err := store.FinishWithResult(ctx, recoveryClaim, recoveryOutcome.TerminalState, recoveryOutcome.OwnerResult, recoveryOutcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		assertRuntimeHealth(t, "ready", nil)
		if err := db.QueryRowContext(ctx, `SELECT version FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&currentVersion); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unsupported locator fails terminally", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET locator = 'opaque://feed/DO_NOT_DISCLOSE' WHERE id = $1`, sourceID); err != nil {
			t.Fatal(err)
		}
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, currentVersion)
		outcome, err := service.Handle(ctx, claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
			t.Fatalf("terminal refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		reason := "RSS_LOCATOR_SCHEME_UNSUPPORTED"
		assertRuntimeHealth(t, "failed", &reason)
		if err := db.QueryRowContext(ctx, `SELECT version FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&currentVersion); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("successful refresh recovers to ready", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET locator = 'https://feeds.example.test/rss.xml' WHERE id = $1`, sourceID); err != nil {
			t.Fatal(err)
		}
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, currentVersion)
		outcome, err := service.Handle(ctx, claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
			t.Fatalf("recovery refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		assertRuntimeHealth(t, "ready", nil)
	})
}

// makeRSSWorkItemDue moves a retried work item into the claimable window
// (store.Retry schedules it at now+retryAfter).
func makeRSSWorkItemDue(t *testing.T, db *sql.DB, workItemID string) {
	t.Helper()
	fixtureDB := ownerTestDB(t)
	if _, err := fixtureDB.ExecContext(context.Background(), `
UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, workItemID); err != nil {
		t.Fatalf("make work item due: %v", err)
	}
}
