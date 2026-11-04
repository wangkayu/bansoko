package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// An offset batch can revive a completed candidate whose placement lost an
// earlier contest. Its historical task stays frozen, the automatic evaluation
// detaches only the current-task pointer, and the normal competition creates
// exactly one new automatic task after choosing the candidate again.
func TestPGOffsetRetryRevivesSupersededCandidateThroughCompetition(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("rel-offset-revive", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.evaluate("rel-offset-revive")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("rel-offset-revive")
	oldTaskID := completeCurrentDownload(t, "rel-offset-revive", "succeeded")

	var oldNamespace string
	if err := f.db.QueryRow(`SELECT request_namespace FROM acquisitions WHERE id = (SELECT acquisition_id FROM download_tasks WHERE id = $1)`, oldTaskID).Scan(&oldNamespace); err != nil {
		t.Fatal(err)
	}
	seedOffsetRetryPlacement(t, f.db, "placement-offset-revive", "rel-offset-revive", "/library/revived.mkv", "superseded", "tv:e1")
	// The change key belongs to the placement that moved. The revivable loser is
	// a different placement on the old key, so this also covers the case where
	// the original winner leaves and another group becomes the winner.
	seedOffsetRetryPlacement(t, f.db, "placement-offset-moved", "", "/library/moved.mkv", "superseded", "tv:e2")
	if _, err := f.db.Exec(`
INSERT INTO offset_alignment_changes(id, rss_source_id, source_version, old_offset, new_offset, rekeyed_count, state)
VALUES ('change-offset-revive', 'source-rss-test', 99, 0, 1, 1, 'recorded');
INSERT INTO offset_alignment_change_keys(change_id, placement_id, old_observation_key, new_observation_key)
VALUES ('change-offset-revive', 'placement-offset-moved', 'tv:e1', 'tv:e2')`); err != nil {
		t.Fatal(err)
	}

	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT 1 FROM subscriptions WHERE id = 'sub-rss-test' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ScheduleOffsetReconciliationTx(context.Background(), txscope.Wrap(tx), "anime-rss-test", "sub-rss-test", []string{"change-offset-revive"}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertOffsetRetryKey(t, f.db, "rel-offset-revive", "offset:change-offset-revive")

	claim, err := f.store.ClaimDue(context.Background(), "offset-revive-test", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Owner.ID != "rel-offset-revive" {
		t.Fatalf("claimed owner = %s", claim.Owner.ID)
	}
	f.finish(claim, f.handle(claim))
	assertOffsetRetryVerdict(t, f.db, "rel-offset-revive", "awaiting_competition")
	assertOffsetRetryKey(t, f.db, "rel-offset-revive", "offset:change-offset-revive")
	var currentTask sql.NullString
	if err := f.db.QueryRow(`SELECT download_task_id FROM releases WHERE id = 'rel-offset-revive'`).Scan(&currentTask); err != nil {
		t.Fatal(err)
	}
	if currentTask.Valid {
		t.Fatalf("the settled task was not detached before competition: %s", currentTask.String)
	}
	if got := releaseTaskCount(t, f.db, "rel-offset-revive"); got != 1 {
		t.Fatalf("tasks before the decision = %d, want only the historical task", got)
	}

	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("rel-offset-revive")
	assertOffsetRetryKey(t, f.db, "rel-offset-revive", "")
	if got := releaseTaskCount(t, f.db, "rel-offset-revive"); got != 2 {
		t.Fatalf("tasks after revival = %d, want one historical and one new task", got)
	}
	var newTaskID, newState, newNamespace string
	if err := f.db.QueryRow(`
SELECT task.id, task.state, acq.request_namespace
FROM releases r
JOIN download_tasks task ON task.id = r.download_task_id
JOIN acquisitions acq ON acq.id = task.acquisition_id
WHERE r.id = 'rel-offset-revive'`).Scan(&newTaskID, &newState, &newNamespace); err != nil {
		t.Fatal(err)
	}
	if newTaskID == oldTaskID {
		t.Fatal("competition reused the settled historical task")
	}
	if newState != "queued" {
		t.Fatalf("new task state = %s, want queued", newState)
	}
	if newNamespace != oldNamespace {
		t.Fatalf("new task namespace = %s, want automatic namespace %s", newNamespace, oldNamespace)
	}
	var historicalState string
	if err := f.db.QueryRow(`SELECT state FROM download_tasks WHERE id = $1`, oldTaskID).Scan(&historicalState); err != nil {
		t.Fatal(err)
	}
	if historicalState != "completed" {
		t.Fatalf("historical task state = %s, want completed", historicalState)
	}
}

// A persisted qualification survives a stop. Resume recovery re-enqueues the
// Release without changing its historical task, and replaying recovery while
// the evaluation is leased cannot create a duplicate task.
func TestPGOffsetRetryRecoveryAfterResume(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("rel-offset-resume", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.evaluate("rel-offset-resume")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("rel-offset-resume")
	completeCurrentDownload(t, "rel-offset-resume", "succeeded")
	seedOffsetRetryPlacement(t, f.db, "placement-offset-resume", "rel-offset-resume", "/library/resume.mkv", "superseded", "tv:e1")
	if _, err := f.db.Exec(`UPDATE releases SET competition_retry_key = 'offset:resume', verdict = 'subscription_stopped', download_task_id = NULL WHERE id = 'rel-offset-resume'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'stopped' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'active' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	scheduleOffsetRetryRecovery(t, f, "sub-rss-test")
	assertOffsetRetryKey(t, f.db, "rel-offset-resume", "offset:resume")

	claim, err := f.store.ClaimDue(context.Background(), "offset-resume-test", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Periodic recovery fills only a missing work item. It must not fence an
	// evaluation that is already leased and making progress.
	scheduleOffsetRetryRecovery(t, f, "sub-rss-test")
	f.finish(claim, f.handle(claim))
	assertOffsetRetryVerdict(t, f.db, "rel-offset-resume", "awaiting_competition")
	f.mustRunCompetition("competition_decided")
	if got := releaseTaskCount(t, f.db, "rel-offset-resume"); got != 2 {
		t.Fatalf("tasks after resumed revival = %d, want 2", got)
	}
	assertOffsetRetryKey(t, f.db, "rel-offset-resume", "")
}

// If a stop wins before the first automatic evaluation, the early stopped path
// leaves both the settled pointer and qualification untouched. Resume detaches
// and evaluates them once; it does not mistake the old task for a running one.
func TestPGOffsetRetryStoppedBeforeFirstEvaluationKeepsSettledPointer(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("rel-offset-stopped-first", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.evaluate("rel-offset-stopped-first")
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted("rel-offset-stopped-first")
	oldTaskID := completeCurrentDownload(t, "rel-offset-stopped-first", "succeeded")
	seedOffsetRetryPlacement(t, f.db, "placement-stopped-first", "rel-offset-stopped-first", "/library/stopped-first.mkv", "superseded", "tv:e1")
	if _, err := f.db.Exec(`UPDATE releases SET competition_retry_key = 'offset:stopped-first' WHERE id = 'rel-offset-stopped-first'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'stopped' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	f.evaluate("rel-offset-stopped-first")
	assertOffsetRetryKey(t, f.db, "rel-offset-stopped-first", "offset:stopped-first")
	var currentTask string
	if err := f.db.QueryRow(`SELECT download_task_id FROM releases WHERE id = 'rel-offset-stopped-first'`).Scan(&currentTask); err != nil {
		t.Fatal(err)
	}
	if currentTask != oldTaskID {
		t.Fatalf("stopped evaluation changed current task to %s", currentTask)
	}
	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'active' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	scheduleOffsetRetryRecovery(t, f, "sub-rss-test")
	claim, err := f.store.ClaimDue(context.Background(), "offset-stopped-first", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.finish(claim, f.handle(claim))
	assertOffsetRetryVerdict(t, f.db, "rel-offset-stopped-first", "awaiting_competition")
	f.mustRunCompetition("competition_decided")
	if got := releaseTaskCount(t, f.db, "rel-offset-stopped-first"); got != 2 {
		t.Fatalf("tasks after stopped-first recovery = %d, want 2", got)
	}
}

// An evaluation that was loaded while active must not withdraw its automatic
// qualification merely because the subscription stopped before an early
// parser-policy abandonment. Resume evaluates the persisted qualification.
func TestPGOffsetRetryAbandonmentFreezesWhenSubscriptionStops(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	releaseID := "rel-offset-abandon-stopped"
	f.addRelease(releaseID, "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.evaluate(releaseID)
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted(releaseID)
	oldTaskID := completeCurrentDownload(t, releaseID, "succeeded")
	seedOffsetRetryPlacement(t, f.db, "placement-abandon-stopped", releaseID, "/library/abandon-stopped.mkv", "superseded", "tv:e1")
	if _, err := f.db.Exec(`UPDATE releases SET competition_retry_key = 'offset:abandon-stopped' WHERE id = $1`, releaseID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.loadReleaseSnapshot(context.Background(), releaseID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.parserPolicyError = errors.New("fixture parser policy failure")
	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'stopped' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	outcome, err := f.service.evaluateRelease(context.Background(), workitems.Claim{ExpectedOwnerVersion: snapshot.releaseVersion}, snapshot)
	if err != nil || outcome.WriteOwner == nil {
		t.Fatalf("abandonment outcome = %+v, err = %v", outcome, err)
	}
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outcome.WriteOwner(context.Background(), txscope.Wrap(tx), outcome.OwnerResult); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertOffsetRetryKey(t, f.db, releaseID, "offset:abandon-stopped")
	var currentTask string
	if err := f.db.QueryRow(`SELECT download_task_id FROM releases WHERE id = $1`, releaseID).Scan(&currentTask); err != nil {
		t.Fatal(err)
	}
	if currentTask != oldTaskID {
		t.Fatalf("stopped abandonment changed current task to %s", currentTask)
	}

	if _, err := f.db.Exec(`UPDATE subscriptions SET state = 'active' WHERE id = 'sub-rss-test'`); err != nil {
		t.Fatal(err)
	}
	scheduleOffsetRetryRecovery(t, f, "sub-rss-test")
	claim, err := f.store.ClaimDue(context.Background(), "offset-abandon-stopped", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.finish(claim, f.handle(claim))
	assertOffsetRetryVerdict(t, f.db, releaseID, "awaiting_competition")
	f.mustRunCompetition("competition_decided")
	if got := releaseTaskCount(t, f.db, releaseID); got != 2 {
		t.Fatalf("tasks after resumed abandonment = %d, want 2", got)
	}
}

// Process recovery cannot turn an uncommitted automatic offset evaluation into
// a terminal Release failure. Settling the interrupted item leaves the owner
// untouched, and ordinary recovery performs the evaluation once.
func TestPGOffsetRetrySurvivesWorkerRecovery(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	releaseID := "rel-offset-worker-recovery"
	f.addRelease(releaseID, "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.evaluate(releaseID)
	f.mustRunCompetition("competition_decided")
	f.requireAdmitted(releaseID)
	oldTaskID := completeCurrentDownload(t, releaseID, "succeeded")
	seedOffsetRetryPlacement(t, f.db, "placement-worker-recovery", releaseID, "/library/worker-recovery.mkv", "superseded", "tv:e1")
	if _, err := f.db.Exec(`UPDATE releases SET competition_retry_key = 'offset:worker-recovery' WHERE id = $1`, releaseID); err != nil {
		t.Fatal(err)
	}
	claim := f.claimEvaluation(releaseID)
	if err := f.store.BlockRecovery(context.Background(), claim, "attempt_exhausted"); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	recovery, err := f.store.ClaimRecovery(context.Background(), []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observer, ok := f.service.ReleaseEvaluateHandler().(worker.RecoveryObserver)
	if !ok {
		t.Fatal("release handler lacks recovery observation")
	}
	outcome, err := observer.ObserveRecovery(context.Background(), recovery)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SettleRecovery(context.Background(), recovery, outcome.TerminalState, outcome.OwnerResult, outcome.Disposition, outcome.WriteOwner, outcome.PostTransition); err != nil {
		t.Fatal(err)
	}
	assertOffsetRetryKey(t, f.db, releaseID, "offset:worker-recovery")
	assertOffsetRetryVerdict(t, f.db, releaseID, "accepted_pending_download")
	var currentTask string
	if err := f.db.QueryRow(`SELECT download_task_id FROM releases WHERE id = $1`, releaseID).Scan(&currentTask); err != nil {
		t.Fatal(err)
	}
	if currentTask != oldTaskID {
		t.Fatalf("worker recovery changed current task to %s", currentTask)
	}

	scheduleOffsetRetryRecovery(t, f, "sub-rss-test")
	claim, err = f.store.ClaimDue(context.Background(), "offset-worker-recovered", []string{workitems.KindReleaseEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.finish(claim, f.handle(claim))
	assertOffsetRetryVerdict(t, f.db, releaseID, "awaiting_competition")
	f.mustRunCompetition("competition_decided")
	if got := releaseTaskCount(t, f.db, releaseID); got != 2 {
		t.Fatalf("tasks after worker recovery = %d, want 2", got)
	}
	assertOffsetRetryKey(t, f.db, releaseID, "")
}

// A transient admission retry detaches the settled task but keeps the automatic
// qualification. The next worker attempt resumes through competition. Exhausting
// that retry is terminal and clears the qualification instead.
func TestPGOffsetRetrySurvivesTransientWorkerRetryAndClearsOnExhaustion(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name := "transient"
		if terminal {
			name = "exhausted"
		}
		t.Run(name, func(t *testing.T) {
			f := newCompetitionFixture(t, "single_copy")
			f.markRefreshed("source-rss-test", "source-beta")
			releaseID := "rel-offset-worker-" + name
			f.addRelease(releaseID, "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
			f.evaluate(releaseID)
			f.mustRunCompetition("competition_decided")
			f.requireAdmitted(releaseID)
			completeCurrentDownload(t, releaseID, "succeeded")
			seedOffsetRetryPlacement(t, f.db, "placement-worker-"+name, releaseID, "/library/worker-"+name+".mkv", "superseded", "tv:e1")
			if _, err := f.db.Exec(`UPDATE releases SET competition_retry_key = 'offset:worker' WHERE id = $1`, releaseID); err != nil {
				t.Fatal(err)
			}
			claim := f.claimEvaluation(releaseID)
			snapshot, err := f.service.loadReleaseSnapshot(context.Background(), releaseID)
			if err != nil {
				t.Fatal(err)
			}
			if terminal {
				claim.Attempt = claim.MaxAttempts
			}
			if _, err := f.store.Retry(context.Background(), claim, "test_admission_retry", "transient test admission", 0, f.service.releaseAdmissionRetryWriter(claim, snapshot)); err != nil {
				t.Fatal(err)
			}
			if got := releaseTaskCount(t, f.db, releaseID); got != 1 {
				t.Fatalf("tasks after admission retry = %d, want 1", got)
			}
			if terminal {
				assertOffsetRetryKey(t, f.db, releaseID, "")
				assertOffsetRetryVerdict(t, f.db, releaseID, "acquisition_failed")
				return
			}
			assertOffsetRetryKey(t, f.db, releaseID, "offset:worker")

			claim, err = f.store.ClaimDue(context.Background(), "offset-worker-retry", []string{workitems.KindReleaseEvaluate}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			f.finish(claim, f.handle(claim))
			assertOffsetRetryVerdict(t, f.db, releaseID, "awaiting_competition")
			f.mustRunCompetition("competition_decided")
			if got := releaseTaskCount(t, f.db, releaseID); got != 2 {
				t.Fatalf("tasks after transient retry recovery = %d, want 2", got)
			}
			assertOffsetRetryKey(t, f.db, releaseID, "")
		})
	}
}

// The scheduler grants no retry to a live or terminal placement, nor to a
// running acquisition. Even if a qualification races with one of those facts,
// the locked owner write withdraws it without duplicating the current task.
func TestPGOffsetRetryDoesNotDuplicateRunningOrPlacedCandidates(t *testing.T) {
	for _, guard := range []string{"active", "released", "displaced", "running"} {
		t.Run(guard, func(t *testing.T) {
			f := newCompetitionFixture(t, "single_copy")
			f.markRefreshed("source-rss-test", "source-beta")
			releaseID := "rel-offset-guard-" + guard
			f.addRelease(releaseID, "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
			f.evaluate(releaseID)
			f.mustRunCompetition("competition_decided")
			f.requireAdmitted(releaseID)
			var currentTask string
			if err := f.db.QueryRow(`SELECT download_task_id FROM releases WHERE id = $1`, releaseID).Scan(&currentTask); err != nil {
				t.Fatal(err)
			}
			if guard == "running" {
				if _, err := f.db.Exec(`UPDATE download_tasks SET state = 'downloading' WHERE id = $1`, currentTask); err != nil {
					t.Fatal(err)
				}
				if _, err := ownerTestDB(t).Exec(`UPDATE acquisitions SET state = 'running' WHERE id = (SELECT acquisition_id FROM download_tasks WHERE id = $1)`, currentTask); err != nil {
					t.Fatal(err)
				}
			} else {
				completeCurrentDownload(t, releaseID, "succeeded")
			}
			seedOffsetRetryPlacement(t, f.db, "placement-old-"+guard, releaseID, "/library/old-"+guard+".mkv", "superseded", "tv:e1")
			if guard != "running" {
				guardPlacement := "placement-guard-" + guard
				seedOffsetRetryPlacement(t, f.db, guardPlacement, releaseID, "/library/guard-"+guard+".mkv", "active", "tv:e1")
				switch guard {
				case "released":
					if _, err := f.db.Exec(`UPDATE placements SET state = 'released', released_at = CURRENT_TIMESTAMP, released_by = 'test', release_request_key = $2 WHERE id = $1`, guardPlacement, "release-"+guard); err != nil {
						t.Fatal(err)
					}
				case "displaced":
					seedOffsetRetryPlacement(t, f.db, "placement-successor-"+guard, "", "/library/successor-"+guard+".mkv", "active", "tv:e2")
					if _, err := f.db.Exec(`
UPDATE placements SET state = 'displaced', displaced_at = CURRENT_TIMESTAMP, displaced_cause = 'overwrite',
       displaced_by_placement_id = $2, displaced_by_anime_id = 'anime-rss-test', displaced_by_ref = 'test'
WHERE id = $1`, guardPlacement, "placement-successor-"+guard); err != nil {
						t.Fatal(err)
					}
				}
			}
			seedOffsetRetryPlacement(t, f.db, "placement-change-"+guard, "", "/library/change-"+guard+".mkv", "superseded", "tv:e2")
			changeID := "change-guard-" + guard
			if _, err := f.db.Exec(`
INSERT INTO offset_alignment_changes(id, rss_source_id, source_version, old_offset, new_offset, rekeyed_count, state)
VALUES ($1, 'source-rss-test', 99, 0, 1, 1, 'recorded')`, changeID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`
INSERT INTO offset_alignment_change_keys(change_id, placement_id, old_observation_key, new_observation_key)
VALUES ($1, $2, 'tv:e1', 'tv:e2')`, changeID, "placement-change-"+guard); err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`SELECT 1 FROM subscriptions WHERE id = 'sub-rss-test' FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			if err := f.service.ScheduleOffsetReconciliationTx(context.Background(), txscope.Wrap(tx), "anime-rss-test", "sub-rss-test", []string{changeID}); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			assertOffsetRetryKey(t, f.db, releaseID, "")

			// Simulate a qualification persisted just before the guard became
			// visible. The owner writer must still fail closed.
			if _, err := f.db.Exec(`UPDATE releases SET competition_retry_key = 'offset:guard' WHERE id = $1`, releaseID); err != nil {
				t.Fatal(err)
			}
			f.evaluate(releaseID)
			assertOffsetRetryKey(t, f.db, releaseID, "")
			if got := releaseTaskCount(t, f.db, releaseID); got != 1 {
				t.Fatalf("task count with %s guard = %d, want 1", guard, got)
			}
			var taskAfter string
			if err := f.db.QueryRow(`SELECT download_task_id FROM releases WHERE id = $1`, releaseID).Scan(&taskAfter); err != nil {
				t.Fatal(err)
			}
			if taskAfter != currentTask {
				t.Fatalf("current task changed with %s guard: %s -> %s", guard, currentTask, taskAfter)
			}
		})
	}
}

func scheduleOffsetRetryRecovery(t *testing.T, f *competitionFixture, subscriptionID string) {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT 1 FROM subscriptions WHERE id = $1 FOR UPDATE`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ScheduleOffsetRetryRecoveryTx(context.Background(), txscope.Wrap(tx), subscriptionID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertOffsetRetryVerdict(t *testing.T, db *sql.DB, releaseID, want string) {
	t.Helper()
	var verdict string
	if err := db.QueryRow(`SELECT verdict FROM releases WHERE id = $1`, releaseID).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	if verdict != want {
		t.Fatalf("verdict of %s = %q, want %q", releaseID, verdict, want)
	}
}

func assertOffsetRetryKey(t *testing.T, db *sql.DB, releaseID, want string) {
	t.Helper()
	var key string
	if err := db.QueryRow(`SELECT COALESCE(competition_retry_key, '') FROM releases WHERE id = $1`, releaseID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != want {
		t.Fatalf("offset retry key of %s = %q, want %q", releaseID, key, want)
	}
}

func seedOffsetRetryPlacement(t *testing.T, db *sql.DB, placementID, releaseID, path, state, coverageKey string) {
	t.Helper()
	var qualityRef any
	if releaseID != "" {
		qualityRef = releaseID
	}
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
                       source_fingerprint, target_fingerprint, quality_facts_ref, facts_version,
                       verification_state, state, replacement_eligible)
VALUES ($1, $2, 'anime-rss-test', 'file_operation', $3, $4, $5, $6, 'candidate-facts-v1',
        'verified', $7, true)`, placementID, path, "operation-"+placementID, "source-"+placementID, "target-"+placementID, qualityRef, state); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, 0, 'candidate-facts-v1', $3)`, placementID, coverageKey, "evidence-"+placementID); err != nil {
		t.Fatal(err)
	}
}
