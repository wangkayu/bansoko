package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"bansoko/internal/batchplan"
	downloadadmission "bansoko/internal/download/admission"
	downloaddomain "bansoko/internal/download/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"
)

// selectiveAdmission is a downloader lookup that fails for the releases named
// in fail and finds no existing task for every other release.
type selectiveAdmission struct {
	mu   sync.Mutex
	fail string
}

func (a *selectiveAdmission) setFail(releaseID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail = releaseID
}

func (a *selectiveAdmission) MatchAdmission(_ context.Context, request downloadadmission.Request) (downloadadmission.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != "" && strings.HasPrefix(request.OperationKey, "qb:"+a.fail) {
		return downloadadmission.Result{}, errors.New("qB task lookup failed with HTTP 503")
	}
	return downloadadmission.Result{PathSnapshot: downloaddomain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}}, nil
}

func releaseRetryKey(t *testing.T, db *sql.DB, releaseID string) string {
	t.Helper()
	var key sql.NullString
	if err := db.QueryRow(`SELECT competition_retry_key FROM releases WHERE id = $1`, releaseID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key.String
}

// competitionItemState reads the instance's live competition item.
type competitionItemState struct {
	state, trigger string
	attempt, retry int
	due            bool
	fencing        int64
	leaseUntil     sql.NullTime
}

func (f *competitionFixture) competitionItem() competitionItemState {
	f.t.Helper()
	var item competitionItemState
	var retry sql.NullInt64
	if err := f.db.QueryRow(`
SELECT state, COALESCE(input_snapshot->>'trigger', ''), (input_snapshot->>'retry')::int, attempt,
       available_at <= CURRENT_TIMESTAMP, fencing_token, lease_until
FROM work_items WHERE owner_type = 'file_maintenance' AND owner_id = 'anime-rss-test' AND kind = 'subscription_competition'`).Scan(
		&item.state, &item.trigger, &retry, &item.attempt, &item.due, &item.fencing, &item.leaseUntil); err != nil {
		f.t.Fatalf("read the competition item: %v", err)
	}
	item.retry = int(retry.Int64)
	return item
}

// spentCompetitionBudget counts the instance's ended competition items that
// failed or needed more than their first claim.
func (f *competitionFixture) spentCompetitionBudget() int {
	f.t.Helper()
	var count int
	if err := f.db.QueryRow(`
SELECT COUNT(*) FROM work_item_outcomes
WHERE kind = 'subscription_competition' AND owner_id = 'anime-rss-test' AND (state <> 'succeeded' OR attempt > 1)`).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

// drainCompetition runs due rounds until none is left.
func (f *competitionFixture) drainCompetition() {
	f.t.Helper()
	for range 6 {
		if _, ran := f.runCompetition(); !ran {
			return
		}
	}
	f.t.Fatal("the competition did not settle")
}

// holdSubscription locks the instance's subscription row in its own
// transaction until release is called, so concurrent commits meet at it.
func (f *competitionFixture) holdSubscription() (release func()) {
	f.t.Helper()
	tx, err := f.db.Begin()
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT 1 FROM subscriptions WHERE managed_anime_id = 'anime-rss-test' FOR UPDATE`); err != nil {
		_ = tx.Rollback()
		f.t.Fatal(err)
	}
	return func() { _ = tx.Commit() }
}

// waitForLockWaiters reports whether n other sessions wait on a lock at once
// within a few seconds: the racing commits are proven to meet at the held
// row instead of running one after the other.
func (f *competitionFixture) waitForLockWaiters(n int) bool {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := f.db.QueryRow(`
SELECT COUNT(*) FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			return false
		}
		if waiting >= n {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// makeCompetitionDue makes the instance's delayed competition item due now.
func (f *competitionFixture) makeCompetitionDue() {
	f.t.Helper()
	if _, err := ownerTestDB(f.t).Exec(`UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE kind = 'subscription_competition'`); err != nil {
		f.t.Fatal(err)
	}
}

// B1: an explicit retry that leaves a release waiting stays that retry through
// the competition. A release kept out by new-releases-only (start_baseline) is
// retried: it waits (awaiting); an automatic re-evaluation while it waits does
// not put it back to start_baseline; the decided stage admits it through the
// manual retry entry; and no release_discovered notice is sent.
func TestCompetitionRetryRunsAsTheRetryThroughTheDecisionPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	enableNewReleasesOnly(t, 24*time.Hour)
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-old", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	if _, err := f.db.Exec(`UPDATE releases SET discovery_notice_due = TRUE WHERE id = 'a-old'`); err != nil {
		t.Fatal(err)
	}
	f.evaluate("a-old")
	assertReleaseState(t, f.db, "a-old", ReleaseVerdictStartBaseline, ReleaseVerdictStartBaseline, false)

	result, err := f.service.RetryRelease(context.Background(), "a-old", "retry-key-baseline-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAwaiting {
		t.Fatalf("retry = %+v, want awaiting", result)
	}
	if key := releaseRetryKey(t, f.db, "a-old"); key != "retry-key-baseline-1" {
		t.Fatalf("the waiting release must keep its retry: %q", key)
	}
	// A rules change re-evaluates the waiting release automatically.
	f.evaluate("a-old")
	f.requireAwaiting("a-old")
	if key := releaseRetryKey(t, f.db, "a-old"); key != "retry-key-baseline-1" {
		t.Fatalf("an automatic re-evaluation must keep the retry: %q", key)
	}

	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-old")
	var namespace string
	if err := f.db.QueryRow(`
SELECT acq.request_namespace FROM releases r
JOIN download_tasks task ON task.id = r.download_task_id
JOIN acquisitions acq ON acq.id = task.acquisition_id
WHERE r.id = 'a-old'`).Scan(&namespace); err != nil {
		t.Fatal(err)
	}
	if namespace != "manual_acquire_v1" {
		t.Fatalf("the decided retry must use the manual retry entry: %q", namespace)
	}
	if key := releaseRetryKey(t, f.db, "a-old"); key != "" {
		t.Fatalf("a decided release drops the retry key: %q", key)
	}
	if notices := f.notices.list(); len(notices) != 0 {
		t.Fatalf("a retry is not a discovery: %+v", notices)
	}
}

// S2: a stopped subscription's explicit retry whose subscription is resumed
// between its evaluation and its write waits for the competition instead of
// downloading directly; an automatic evaluation in the same race never
// downloads either.
func TestCompetitionResumeDuringADirectEvaluationAwaitsPostgres(t *testing.T) {
	ctx := context.Background()
	t.Run("explicit retry", func(t *testing.T) {
		f := newCompetitionFixture(t, "single_copy")
		f.markRefreshed("source-rss-test", "source-beta")
		if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE id = 'sub-rss-test'`); err != nil {
			t.Fatal(err)
		}
		f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
		snapshot, err := f.service.loadReleaseSnapshot(ctx, "a-720")
		if err != nil {
			t.Fatal(err)
		}
		snapshot.manualRetry, snapshot.retryKey = true, "retry-key-resumed-1"
		if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE id = 'sub-rss-test'`); err != nil {
			t.Fatal(err)
		}
		result, err := f.service.evaluateReleaseRetry(ctx, "a-720", "retry-key-resumed-1", releaseRetryRequestHash("a-720"), snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != RetryOutcomeAwaiting || f.taskCount() != 0 {
			t.Fatalf("retry = %+v with %d tasks, want awaiting and no download", result, f.taskCount())
		}
		f.mustRunCompetition("competition_decided")
		f.requireAdmitted("a-720")
	})
	t.Run("automatic evaluation", func(t *testing.T) {
		f := newCompetitionFixture(t, "single_copy")
		f.markRefreshed("source-rss-test", "source-beta")
		if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE id = 'sub-rss-test'`); err != nil {
			t.Fatal(err)
		}
		f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
		claim := f.claimEvaluation("a-720")
		outcome := f.handle(claim)
		if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE id = 'sub-rss-test'`); err != nil {
			t.Fatal(err)
		}
		err := f.store.FinishWithResultPostTransition(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition)
		if !errors.Is(err, errCompetitionActivated) {
			t.Fatalf("finish error = %v, want the activation refusal", err)
		}
		if f.taskCount() != 0 {
			t.Fatal("an evaluation that raced a resume must not download directly")
		}
		assertReleaseState(t, f.db, "a-720", "discovered", "discovered", false)
		// The runtime reclaims the item when its lease ends; the evaluation
		// then runs on the active subscription and waits for the competition.
		if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, claim.ID); err != nil {
			t.Fatal(err)
		}
		again, err := f.store.ClaimDue(ctx, "competition-test-evaluator", []string{workitems.KindReleaseEvaluate}, time.Minute)
		if err != nil || again.ID != claim.ID {
			t.Fatalf("reclaim = %+v %v", again, err)
		}
		f.finish(again, f.handle(again))
		f.requireAwaiting("a-720")
		f.mustRunCompetition("competition_decided")
		f.requireAdmitted("a-720")
	})
}

// L1: a provider group that leaves the selection ends its waiting releases as
// filtered (provider_group_not_selected) and drops their discovery notice; the
// instance enqueues no round for releases out of its scope.
func TestCompetitionDeselectedGroupDropsItsWaitingReleasesPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-01", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.addRelease("b-01", "source-beta", "[Beta] Test Show - 01 [1080p]", 1)
	f.evaluate("a-01", "b-01")
	f.requireAwaiting("a-01", "b-01")
	if _, err := f.db.Exec(`UPDATE releases SET discovery_notice_due = TRUE WHERE id = 'b-01'`); err != nil {
		t.Fatal(err)
	}

	if _, err := f.db.Exec(`UPDATE provider_group_selections SET effective_selected = FALSE, version = version + 1 WHERE id = 'sel-beta'`); err != nil {
		t.Fatal(err)
	}
	assertReleaseState(t, f.db, "b-01", "filtered", "provider_group_not_selected", false)
	var due bool
	if err := f.db.QueryRow(`SELECT discovery_notice_due FROM releases WHERE id = 'b-01'`).Scan(&due); err != nil || due {
		t.Fatalf("a dropped release drops its discovery notice: %v %v", due, err)
	}
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-01")

	// Only out-of-scope releases could still wait: nothing enqueues.
	f.addRelease("b-02", "source-beta", "[Beta] Test Show - 02 [1080p]", 2)
	if _, err := f.db.Exec(`UPDATE releases SET verdict = 'awaiting_competition', reason_code = 'awaiting_competition' WHERE id = 'b-02'`); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if enqueued, err := competition.EnqueueTx(context.Background(), tx, "anime-rss-test", competition.TriggerSourceRefreshed, competition.WhenAwaiting); err != nil || enqueued {
		t.Fatalf("an out-of-scope waiting release must not enqueue a round: %v %v", enqueued, err)
	}
}

// L4: a retry that asked a redownload of a missing placed file and then lost
// the competition withdraws its redownload request: the placement holds its
// episode again.
func TestCompetitionLosingRetryWithdrawsItsRedownloadRequestPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-720")
	f.mustRunCompetition("competition_decided")
	placeCurrentDownload(t, f.db, "a-720")
	f.drainCompetition()
	if _, err := ownerTestDB(t).Exec(`
INSERT INTO reconciliation_files(managed_anime_id, generation, file_path, fingerprint, size_bytes, facts, observed_at, disposition)
VALUES ('anime-rss-test', 1, '/library/competition/a-720.mkv', 'fp-a-720', 1, jsonb_build_object('placement_id', 'plc-a-720'), now(), 'missing')`); err != nil {
		t.Fatal(err)
	}
	// A better release of the episode waits for the round.
	f.addRelease("b-1080", "source-beta", "[Beta] Test Show - 01 [1080p]", 1)
	f.evaluate("b-1080")

	result, err := f.service.RetryRelease(context.Background(), "a-720", "retry-key-redownload-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAwaiting {
		t.Fatalf("retry = %+v, want awaiting", result)
	}
	if requested, key := redownloadMarker(t, f.db, "plc-a-720"); !requested || key != "retry-key-redownload-1" {
		t.Fatalf("the waiting retry keeps its redownload request: %v %q", requested, key)
	}
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("b-1080")
	var verdict, reason string
	var task sql.NullString
	if err := f.db.QueryRow(`SELECT verdict, reason_code, download_task_id FROM releases WHERE id = 'a-720'`).Scan(&verdict, &reason, &task); err != nil {
		t.Fatal(err)
	}
	if verdict != "filtered" || reason != batchplan.ReasonNoPlaceableFiles || task.Valid {
		t.Fatalf("the losing retry = %s/%s task=%v, want filtered without a download", verdict, reason, task.Valid)
	}
	if requested, key := redownloadMarker(t, f.db, "plc-a-720"); requested || key != "" {
		t.Fatalf("a retry that downloads nothing withdraws its request: %v %q", requested, key)
	}
	if key := releaseRetryKey(t, f.db, "a-720"); key != "" {
		t.Fatalf("the decided release drops the retry key: %q", key)
	}
}

// S5: only the instance's first round waits for first refreshes, and that
// wait always ends: a never-refreshed source without a runtime row or without
// a queued refresh holds nothing, and after the first round a newly selected
// group's pending first refresh holds no incremental round.
func TestCompetitionFirstRefreshWaitEndsPostgres(t *testing.T) {
	t.Run("no runtime row or queued refresh", func(t *testing.T) {
		f := newCompetitionFixture(t, "single_copy")
		f.markRefreshed("source-rss-test")
		if _, err := ownerTestDB(t).Exec(`DELETE FROM rss_source_runtime WHERE rss_source_id = 'source-beta'`); err != nil {
			t.Fatal(err)
		}
		f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
		f.evaluate("a-720")
		f.mustRunCompetition("competition_decided")
		f.requireAdmitted("a-720")
		if !f.firstRoundDone() {
			t.Fatal("the decided round records the first round")
		}
	})
	t.Run("new group after the first round", func(t *testing.T) {
		f := newCompetitionFixture(t, "single_copy")
		f.markRefreshed("source-rss-test", "source-beta")
		f.addRelease("a-01", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
		f.evaluate("a-01")
		f.mustRunCompetition("competition_decided")

		if _, err := f.db.Exec(`
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title,
                        provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version, provenance)
VALUES ('source-gamma', 'sub-rss-test', 'provider_group', 'https://feeds.example.test/gamma.xml', 'https://feeds.example.test/gamma.xml', 'Gamma', 'Test Show',
        'anibt', '517057', 'gamma', 'v1',
        '{"provider":"anibt","provider_entry_ref":"517057","provider_group_ref":"gamma","discovery_snapshot_version":"v1"}'::jsonb)`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-gamma')`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(`
INSERT INTO provider_group_selections(id, subscription_id, provider, group_ref, display_name, selection_mode, effective_selected, effective_score, available, availability_generation)
VALUES ('sel-gamma', 'sub-rss-test', 'anibt', 'gamma', 'Gamma', 'auto', TRUE, 50, TRUE, 1)`); err != nil {
			t.Fatal(err)
		}
		f.queueRefresh("source-gamma")
		f.addRelease("a-02", "source-rss-test", "[Alpha] Test Show - 02 [720p]", 2)
		f.evaluate("a-02")
		f.mustRunCompetition("competition_decided")
		f.requireAdmitted("a-02")
	})
}

// S3: a release whose decided stage fails (a downloader lookup outage) is
// left out of its round: the other releases commit, it keeps waiting with its
// episode closed, and the round runs again after the retry delay.
func TestCompetitionDecidedStageFailureLeavesOnlyThatReleaseWaitingPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "per_group")
	f.markRefreshed("source-rss-test", "source-beta")
	lookup := &selectiveAdmission{fail: "b-01"}
	f.service.SetDownloaderAdmission(lookup)
	f.addRelease("a-01", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.addRelease("b-01", "source-beta", "[Beta] Test Show - 01 [1080p]", 1)
	f.evaluate("a-01", "b-01")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-01")
	f.requireAwaiting("b-01")
	item := f.competitionItem()
	if item.trigger != competition.TriggerRoundRetry || item.retry != 1 || item.due || item.attempt != 0 {
		t.Fatalf("the round must run again later on a fresh budget: %+v", item)
	}
	if _, ran := f.runCompetition(); ran {
		t.Fatal("the retry round waits for its delay")
	}
	lookup.setFail("")
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE kind = 'subscription_competition'`); err != nil {
		t.Fatal(err)
	}
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("b-01")
}

// S4: a merge into a running round leaves its lease alone, and a round whose
// inputs moved before its commit ends without writing and leaves a fresh item
// that is due at once on a fresh attempt budget.
func TestCompetitionMovedRoundSpendsNoBudgetPostgres(t *testing.T) {
	ctx := context.Background()
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-720")
	claim, ok := f.claimCompetition()
	if !ok || claim.Attempt != 1 {
		t.Fatalf("claim = %+v %v", claim, ok)
	}
	leased := f.competitionItem()

	run := competitionRun{}
	var err error
	if run.round, err = f.service.decideCompetition(ctx, "anime-rss-test", nil); err != nil {
		t.Fatal(err)
	}
	f.service.runDecidedStages(ctx, "anime-rss-test", &run)
	// A better release lands before the commit; its evaluation merges into
	// the running round.
	f.addRelease("b-1080", "source-beta", "[Beta] Test Show - 01 [1080p]", 1)
	f.evaluate("b-1080")
	if merged := f.competitionItem(); merged.state != "leased" || merged.fencing != leased.fencing || !merged.leaseUntil.Time.Equal(leased.leaseUntil.Time) {
		t.Fatalf("a merge must leave the running round's lease alone: %+v, was %+v", merged, leased)
	}
	outcome, err := f.service.commitCompetition(ctx, claim, run)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != competition.TriggerRoundMoved {
		t.Fatalf("moved round = %+v", outcome)
	}
	f.requireAwaiting("a-720", "b-1080")
	if fresh := f.competitionItem(); fresh.state != "available" || fresh.attempt != 0 || !fresh.due {
		t.Fatalf("a moved round leaves a fresh item due at once: %+v", fresh)
	}
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("b-1080")
	f.requireCoveredInFlight("a-720")
	if spent := f.spentCompetitionBudget(); spent != 0 {
		t.Fatalf("contention must not spend the budget: %d", spent)
	}
}

// Two rounds of one instance run at once (the second reclaimed the first's
// expired lease): only the current claim commits, and the episode gets one
// download.
func TestCompetitionConcurrentRoundsAdmitOncePostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-1080", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.addRelease("b-480", "source-beta", "[Beta] Test Show - 01 [480p]", 1)
	f.evaluate("a-1080", "a-720", "b-480")
	first, _ := f.claimCompetition()
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok := f.claimCompetition()
	if !ok || second.ID != first.ID || second.FencingToken == first.FencingToken {
		t.Fatalf("reclaim = %+v %v", second, ok)
	}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for index, claim := range []workitems.Claim{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[index] = f.handleCompetition(claim)
		}()
	}
	close(start)
	wg.Wait()
	// The fenced claim is refused by the claim lock (stale, or its item
	// already closed by the current claim).
	if !(errors.Is(errs[0], workitems.ErrStaleClaim) || errors.Is(errs[0], workitems.ErrOwnerRetired)) || errs[1] != nil {
		t.Fatalf("round errors = %v, want the fenced claim refused and the current one committed", errs)
	}
	f.requireAdmitted("a-1080")
	f.requireCoveredInFlight("a-720", "b-480")
	if f.taskCount() != 1 {
		t.Fatalf("download tasks = %d, want one", f.taskCount())
	}
}

// The competition commit and a refresh commit of the same instance meet at
// the subscription row (both take it before the competition item): neither
// deadlocks, the round decides, and no attempt is spent. Repeated per episode
// so both orders occur.
func TestCompetitionCommitAndRefreshFinishConcurrentlyPostgres(t *testing.T) {
	ctx := context.Background()
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	for episode := 1; episode <= 3; episode++ {
		id := fmt.Sprintf("a-%02d", episode)
		f.addRelease(id, "source-rss-test", fmt.Sprintf("[Alpha] Test Show - %02d [720p]", episode), episode)
		f.evaluate(id)
		f.queueRefresh("source-beta")
		competitionClaim, ok := f.claimCompetition()
		if !ok {
			t.Fatal("the evaluation must enqueue the round")
		}
		refreshClaim, err := f.store.ClaimDue(ctx, "competition-test-refresher", []string{workitems.KindRSSRefresh}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		refreshOutcome, err := f.service.Handle(ctx, refreshClaim)
		if err != nil || refreshOutcome.Kind != worker.OutcomeFinish {
			t.Fatalf("refresh = %+v %v", refreshOutcome, err)
		}
		release := f.holdSubscription()
		errs := make([]error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errs[0] = f.handleCompetition(competitionClaim)
		}()
		go func() {
			defer wg.Done()
			if refreshOutcome.PostTransition != nil {
				errs[1] = f.store.FinishWithResultPostTransition(ctx, refreshClaim, refreshOutcome.TerminalState, refreshOutcome.OwnerResult, refreshOutcome.WriteOwner, refreshOutcome.PostTransition)
			} else {
				errs[1] = f.store.FinishWithResult(ctx, refreshClaim, refreshOutcome.TerminalState, refreshOutcome.OwnerResult, refreshOutcome.WriteOwner)
			}
		}()
		met := f.waitForLockWaiters(2)
		release()
		wg.Wait()
		if !met {
			t.Fatalf("episode %d: the competition commit and the refresh commit never waited at the subscription together", episode)
		}
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("episode %d: competition %v, refresh %v", episode, errs[0], errs[1])
		}
		f.drainCompetition()
		f.requireAdmitted(id)
	}
	if spent := f.spentCompetitionBudget(); spent != 0 {
		t.Fatalf("contention must not spend the budget: %d", spent)
	}
}

// The competition commit and an evaluation commit that brings a new waiting
// release meet at the subscription row: whichever commits first, the new
// release is decided by a later round (a moved round or the evaluation's own
// enqueue), nothing deadlocks and no attempt is spent.
func TestCompetitionCommitAndEvaluationFinishConcurrentlyPostgres(t *testing.T) {
	ctx := context.Background()
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-720")
	f.addRelease("b-1080", "source-beta", "[Beta] Test Show - 01 [1080p]", 1)
	evaluation := f.claimEvaluation("b-1080")
	evaluated := f.handle(evaluation)
	competitionClaim, ok := f.claimCompetition()
	if !ok {
		t.Fatal("the first evaluation must enqueue the round")
	}

	release := f.holdSubscription()
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = f.handleCompetition(competitionClaim)
	}()
	go func() {
		defer wg.Done()
		errs[1] = f.store.FinishWithResultPostTransition(ctx, evaluation, evaluated.TerminalState, evaluated.OwnerResult, evaluated.WriteOwner, evaluated.PostTransition)
	}()
	met := f.waitForLockWaiters(2)
	release()
	wg.Wait()
	if !met {
		t.Fatal("the competition commit and the evaluation commit never waited at the subscription together")
	}
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("competition %v, evaluation %v", errs[0], errs[1])
	}
	f.drainCompetition()
	f.requireAdmitted("b-1080")
	f.requireCoveredInFlight("a-720")
	if f.taskCount() != 1 {
		t.Fatalf("download tasks = %d, want one", f.taskCount())
	}
	if spent := f.spentCompetitionBudget(); spent != 0 {
		t.Fatalf("contention must not spend the budget: %d", spent)
	}
}

// B1: a waiting retry re-evaluated while its subscription is stopped keeps its
// retry key (and so its redownload requests); after the resume the evaluation
// is that retry again and the decided stage admits it through the manual
// retry entry.
func TestCompetitionStoppedReEvaluationKeepsTheRetryPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	enableNewReleasesOnly(t, 24*time.Hour)
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-old", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-old")
	assertReleaseState(t, f.db, "a-old", ReleaseVerdictStartBaseline, ReleaseVerdictStartBaseline, false)
	result, err := f.service.RetryRelease(context.Background(), "a-old", "retry-key-stopped-1")
	if err != nil || result.Outcome != RetryOutcomeAwaiting {
		t.Fatalf("retry = %+v %v, want awaiting", result, err)
	}

	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	f.evaluate("a-old")
	assertReleaseState(t, f.db, "a-old", "subscription_stopped", "subscription_stopped", false)
	if key := releaseRetryKey(t, f.db, "a-old"); key != "retry-key-stopped-1" {
		t.Fatalf("a stopped re-evaluation must keep the retry: %q", key)
	}

	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	f.evaluate("a-old")
	f.requireAwaiting("a-old")
	if key := releaseRetryKey(t, f.db, "a-old"); key != "retry-key-stopped-1" {
		t.Fatalf("the resumed evaluation is the retry again: %q", key)
	}
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-old")
	var namespace string
	if err := f.db.QueryRow(`
SELECT acq.request_namespace FROM releases r
JOIN download_tasks task ON task.id = r.download_task_id
JOIN acquisitions acq ON acq.id = task.acquisition_id
WHERE r.id = 'a-old'`).Scan(&namespace); err != nil {
		t.Fatal(err)
	}
	if namespace != "manual_acquire_v1" {
		t.Fatalf("the decided retry must use the manual retry entry: %q", namespace)
	}
}

// M1: a round that finds its gate closed (a live evaluation) leaves a delayed
// item. A settle that arrives while an evaluation runs is not lost when that
// evaluation ends without a waiting release: the fallback is decided later.
func TestCompetitionClosedGateKeepsTheSettledRoundPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-1080", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-1080", "a-720")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-1080")
	f.requireCoveredInFlight("a-720")

	// An evaluation that will not wait (its group is not selected) is live.
	if _, err := f.db.Exec(`UPDATE provider_group_selections SET effective_selected = FALSE, version = version + 1 WHERE id = 'sel-beta'`); err != nil {
		t.Fatal(err)
	}
	f.addRelease("b-02", "source-beta", "[Beta] Test Show - 02 [1080p]", 2)
	evaluation := f.claimEvaluation("b-02")

	failCurrentDownload(t, "a-1080")
	f.mustRunCompetition(competitionWaitingEvaluations)
	if item := f.competitionItem(); item.state != "available" || item.trigger != competition.TriggerGateWait || item.due || item.attempt != 0 {
		t.Fatalf("a closed gate leaves a delayed item: %+v", item)
	}
	f.finish(evaluation, f.handle(evaluation))
	if f.requireNoAwaiting(); f.liveCompetitions() != 1 {
		t.Fatal("the delayed round must survive the evaluation's end")
	}
	f.makeCompetitionDue()
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-720")
}

// requireNoAwaiting requires that no release of the instance waits.
func (f *competitionFixture) requireNoAwaiting() {
	f.t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM releases WHERE verdict = 'awaiting_competition'`).Scan(&count); err != nil || count != 0 {
		f.t.Fatalf("waiting releases = %d %v, want none", count, err)
	}
}

// M1: an interrupted round (recovery observation) and a round that failed on
// its last attempt carry over to a fresh delayed item on a fresh budget; an
// earlier attempt keeps the ordinary retry.
func TestCompetitionInterruptedOrExhaustedRoundCarriesOverPostgres(t *testing.T) {
	ctx := context.Background()
	requireCarried := func(f *competitionFixture) {
		f.t.Helper()
		if item := f.competitionItem(); item.state != "available" || item.trigger != competition.TriggerRoundCarried || item.due || item.attempt != 0 {
			f.t.Fatalf("the round must carry over to a fresh delayed item: %+v", item)
		}
		f.makeCompetitionDue()
		f.mustRunCompetition("competition_decided")
		f.requireAdmitted("a-720")
	}
	t.Run("recovery", func(t *testing.T) {
		f := newCompetitionFixture(t, "single_copy")
		f.markRefreshed("source-rss-test", "source-beta")
		f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
		f.evaluate("a-720")
		claim, ok := f.claimCompetition()
		if !ok {
			t.Fatal("no round")
		}
		if err := f.store.BlockRecovery(ctx, claim, "attempt_exhausted"); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, claim.ID); err != nil {
			t.Fatal(err)
		}
		recovery, err := f.store.ClaimRecovery(ctx, []string{workitems.KindSubscriptionCompetition}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		observer, _ := f.service.CompetitionHandler().(worker.RecoveryObserver)
		o, err := observer.ObserveRecovery(ctx, recovery)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.SettleRecovery(ctx, recovery, o.TerminalState, o.OwnerResult, o.Disposition, o.WriteOwner, o.PostTransition); err != nil {
			t.Fatal(err)
		}
		requireCarried(f)
	})
	t.Run("last attempt", func(t *testing.T) {
		f := newCompetitionFixture(t, "single_copy")
		f.markRefreshed("source-rss-test", "source-beta")
		f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
		f.evaluate("a-720")
		claim, ok := f.claimCompetition()
		if !ok {
			t.Fatal("no round")
		}
		fallback, _ := f.service.CompetitionHandler().(worker.RecoveryFallbackHandler)
		if _, handled := fallback.RecoveryFallback(ctx, claim, errors.New("decide failed")); handled {
			t.Fatal("an attempt with budget left keeps the ordinary retry")
		}
		last := claim
		last.Attempt = last.MaxAttempts
		o, handled := fallback.RecoveryFallback(ctx, last, errors.New("decide failed"))
		if !handled {
			t.Fatal("the last attempt must carry the round over")
		}
		f.finish(claim, o)
		requireCarried(f)
	})
}

// L1: a round winner whose decided stage fails is left out and the round is
// decided again without it, so the next candidate takes the episode instead
// of being written as covered by the failed winner. A release still left out
// once the delayed retries ran out keeps waiting with the visible reason
// competition_stalled.
func TestCompetitionLeftOutWinnerFreesItsEpisodePostgres(t *testing.T) {
	t.Run("next candidate takes the episode", func(t *testing.T) {
		f := newCompetitionFixture(t, "single_copy")
		f.markRefreshed("source-rss-test", "source-beta")
		f.service.SetDownloaderAdmission(&selectiveAdmission{fail: "a-1080"})
		f.addRelease("a-1080", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
		f.addRelease("b-720", "source-beta", "[Beta] Test Show - 01 [720p]", 1)
		f.evaluate("a-1080", "b-720")
		f.mustRunCompetition("competition_decided")
		f.requireAdmitted("b-720")
		f.requireAwaiting("a-1080")
		if item := f.competitionItem(); item.trigger != competition.TriggerRoundRetry || item.retry != 1 {
			t.Fatalf("the left-out winner is retried later: %+v", item)
		}
	})
	t.Run("stalled after the retries", func(t *testing.T) {
		f := newCompetitionFixture(t, "single_copy")
		f.markRefreshed("source-rss-test", "source-beta")
		f.service.SetDownloaderAdmission(&selectiveAdmission{fail: "a-1080"})
		f.addRelease("a-1080", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
		f.evaluate("a-1080")
		f.mustRunCompetition("competition_decided")
		for retry := 1; retry <= competition.MaxAttempts; retry++ {
			if item := f.competitionItem(); item.trigger != competition.TriggerRoundRetry || item.retry != retry {
				t.Fatalf("retry %d: %+v", retry, item)
			}
			f.requireAwaiting("a-1080")
			f.makeCompetitionDue()
			f.mustRunCompetition("competition_decided")
		}
		if f.liveCompetitions() != 0 {
			t.Fatal("the retries are bounded")
		}
		assertReleaseState(t, f.db, "a-1080", competition.VerdictAwaiting, competitionStalled, false)
	})
}

// The settle trigger merges into the competition item without the
// subscription lock. A settle that commits while a round waits at the
// subscription row to commit leaves that round moved; the fresh round lets
// the fallback in. Nothing deadlocks and no attempt is spent.
func TestCompetitionCommitAndSettleTriggerConcurrentlyPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-1080", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.addRelease("a-720", "source-rss-test", "[Alpha] Test Show - 01 [720p]", 1)
	f.evaluate("a-1080", "a-720")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("a-1080")
	f.requireCoveredInFlight("a-720")
	f.addRelease("a-02", "source-rss-test", "[Alpha] Test Show - 02 [720p]", 2)
	f.evaluate("a-02")
	claim, ok := f.claimCompetition()
	if !ok {
		t.Fatal("the evaluation must enqueue the round")
	}
	var taskID, acquisitionID string
	if err := f.db.QueryRow(`SELECT task.id, task.acquisition_id FROM releases r JOIN download_tasks task ON task.id = r.download_task_id WHERE r.id = 'a-1080'`).Scan(&taskID, &acquisitionID); err != nil {
		t.Fatal(err)
	}

	release := f.holdSubscription()
	var (
		outcome    worker.Outcome
		roundErr   error
		roundDone  = make(chan struct{})
		settled    = make(chan error, 1)
		ownerDB    = ownerTestDB(t)
		settleDone bool
	)
	go func() {
		defer close(roundDone)
		outcome, roundErr = f.handleCompetition(claim)
	}()
	met := f.waitForLockWaiters(1)
	if met {
		go func() {
			if _, err := ownerDB.Exec(`UPDATE download_tasks SET state='failed',failure_code='download_stalled',failure_reason='fixture',version=version+1 WHERE id=$1`, taskID); err != nil {
				settled <- err
				return
			}
			_, err := ownerDB.Exec(`UPDATE acquisitions SET state='failed',terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, acquisitionID)
			settled <- err
		}()
		select {
		case err := <-settled:
			settleDone = true
			if err != nil {
				release()
				<-roundDone
				t.Fatalf("settle: %v", err)
			}
		case <-time.After(10 * time.Second):
		}
	}
	release()
	<-roundDone
	if !met || !settleDone {
		t.Fatalf("the settle must commit while the round waits to commit (met=%v settled=%v)", met, settleDone)
	}
	if roundErr != nil || outcome.OwnerResult.ReasonCode != competition.TriggerRoundMoved {
		t.Fatalf("the round that met the settle = %+v %v, want moved", outcome.OwnerResult, roundErr)
	}
	f.drainCompetition()
	f.requireAdmitted("a-02", "a-720")
	if spent := f.spentCompetitionBudget(); spent != 0 {
		t.Fatalf("contention must not spend the budget: %d", spent)
	}
}
