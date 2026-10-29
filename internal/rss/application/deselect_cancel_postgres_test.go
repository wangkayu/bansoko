package application

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"bansoko/internal/acquisition/terminal/settlement"
	animeapplication "bansoko/internal/anime/application"
	downloadapplication "bansoko/internal/download/application"
	downloaddomain "bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"
)

// Migration 103 end to end: a provider group that leaves a subscription's
// selection cancels the subscription's pre-import downloads of that group
// (the download worker deletes the qB task with its data), the cancelled
// releases return to filtered/provider_group_not_selected and the competition
// admits the fallbacks; a group that enters the selection again evaluates
// those releases from their stored facts with no feed refresh.

// cancelE2EProvider is a qB that is never reachable for polls; a cancel
// never polls or looks anything up.
type cancelE2EProvider struct {
	syncCalls, lookupCalls int
}

func (p *cancelE2EProvider) LookupByOperationKey(context.Context, string) (downloaddomain.ProviderTask, bool, error) {
	p.lookupCalls++
	return downloaddomain.ProviderTask{}, false, nil
}

func (p *cancelE2EProvider) Sync(context.Context, string) (downloaddomain.ProviderTask, error) {
	p.syncCalls++
	return downloaddomain.ProviderTask{}, context.DeadlineExceeded
}

func (p *cancelE2EProvider) ClassifyProviderError(error) downloaddomain.ProviderErrorClass {
	return downloaddomain.ProviderErrorRetryable
}

type cancelE2EConfig struct{ calls int }

func (c *cancelE2EConfig) GetQBittorrentRuntimeConfig(context.Context) (qbittorrent.RuntimeConfig, error) {
	c.calls++
	return qbittorrent.RuntimeConfig{}, errors.New("qB is not configured in this fixture")
}

// cancelE2ERemover records the deletes; each one removes the task and its data.
type cancelE2ERemover struct{ calls []string }

func (r *cancelE2ERemover) RemoveCancelledDownload(_ context.Context, acquisitionID string, _, _ []byte) downloadapplication.CancelRemoval {
	r.calls = append(r.calls, acquisitionID)
	return downloadapplication.CancelRemoval{Effect: downloadapplication.CancelEffectDeletedWithData, Code: "removed_data"}
}

type cancelE2E struct {
	*competitionFixture
	downloads *downloadapplication.Service
	provider  *cancelE2EProvider
	config    *cancelE2EConfig
	remover   *cancelE2ERemover
}

func newCancelE2E(t *testing.T) *cancelE2E {
	t.Helper()
	f := newCompetitionFixture(t, "single_copy")
	f.markRefreshed("source-rss-test", "source-beta")
	provider := &cancelE2EProvider{}
	config := &cancelE2EConfig{}
	downloads, err := downloadapplication.NewService(f.db, f.store, provider, config)
	if err != nil {
		t.Fatal(err)
	}
	remover := &cancelE2ERemover{}
	downloads.SetCancelRemover(remover)
	return &cancelE2E{competitionFixture: f, downloads: downloads, provider: provider, config: config, remover: remover}
}

// currentDownload is the release's current task and its acquisition.
func (e *cancelE2E) currentDownload(releaseID string) (taskID, root string) {
	e.t.Helper()
	if err := e.db.QueryRow(`SELECT task.id, task.acquisition_id FROM releases r JOIN download_tasks task ON task.id = r.download_task_id WHERE r.id = $1`, releaseID).Scan(&taskID, &root); err != nil {
		e.t.Fatalf("current download of %s: %v", releaseID, err)
	}
	return taskID, root
}

// closeSubmit ends the task's submission the way an accepted add does.
func (e *cancelE2E) closeSubmit(taskID string) {
	e.t.Helper()
	if _, err := ownerTestDB(e.t).Exec(`SELECT work_item_close(id, 'succeeded') FROM work_items WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_submit'`, taskID); err != nil {
		e.t.Fatal(err)
	}
}

// arrangeDownloading puts the release's admitted download into qB: the task
// is downloading and its next poll waits for the poll interval.
func (e *cancelE2E) arrangeDownloading(releaseID string) string {
	e.t.Helper()
	taskID, _ := e.currentDownload(releaseID)
	e.closeSubmit(taskID)
	owner := ownerTestDB(e.t)
	if _, err := owner.Exec(`UPDATE download_tasks SET state = 'downloading', provider_correlation = jsonb_build_object('external_id', 'tag:' || idempotency_key, 'stable_tag', idempotency_key), version = version + 1 WHERE id = $1`, taskID); err != nil {
		e.t.Fatal(err)
	}
	if _, err := owner.Exec(`SELECT work_item_create(gen_random_uuid()::text, 'download_task', id, 'qb_sync', idempotency_key, 5,
    jsonb_build_object('trigger', 'qb_sync_progress', 'poll_ordinal', next_poll_ordinal), version, 3600::double precision, 7::smallint)
FROM download_tasks WHERE id = $1`, taskID); err != nil {
		e.t.Fatal(err)
	}
	return taskID
}

// arrangeCompleted finishes the release's download: it waits for import.
func (e *cancelE2E) arrangeCompleted(releaseID string) string {
	e.t.Helper()
	taskID, _ := e.currentDownload(releaseID)
	e.closeSubmit(taskID)
	if _, err := ownerTestDB(e.t).Exec(`UPDATE download_tasks SET state = 'completed', version = version + 1 WHERE id = $1`, taskID); err != nil {
		e.t.Fatal(err)
	}
	return taskID
}

// runDownloads runs every due submit and sync item the way the runtime does
// and returns the handled tasks.
func (e *cancelE2E) runDownloads() []string {
	e.t.Helper()
	ctx := context.Background()
	var handled []string
	for {
		claim, err := e.store.ClaimDue(ctx, "deselect-download-worker", []string{workitems.KindQBSubmit, workitems.KindQBSync}, time.Minute)
		if errors.Is(err, workitems.ErrNoDueWork) {
			return handled
		}
		if err != nil {
			e.t.Fatal(err)
		}
		outcome, err := e.downloads.Handle(ctx, claim)
		if err != nil || outcome.Kind != worker.OutcomeCommitted {
			e.t.Fatalf("%s of %s: %+v %v", claim.Kind, claim.Owner.ID, outcome, err)
		}
		if outcome.AfterCommit != nil {
			outcome.AfterCommit(ctx)
		}
		handled = append(handled, claim.Owner.ID)
	}
}

func (e *cancelE2E) settle(root string) {
	e.t.Helper()
	settler, err := settlement.NewSettlementService(e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	if status, err := settler.SettleRoot(context.Background(), root); err != nil || status != "settled" {
		e.t.Fatalf("settle %s: %q %v", root, status, err)
	}
}

type cancelledTask struct {
	state, code, effect, cause string
}

func (e *cancelE2E) task(taskID string) cancelledTask {
	e.t.Helper()
	var task cancelledTask
	var code, effect, cause sql.NullString
	if err := e.db.QueryRow(`SELECT state, failure_code, cancel_effect, cancel_cause FROM download_tasks WHERE id = $1`, taskID).Scan(&task.state, &code, &effect, &cause); err != nil {
		e.t.Fatal(err)
	}
	task.code, task.effect, task.cause = code.String, effect.String, cause.String
	return task
}

func (e *cancelE2E) requireRoot(root, state, code string) {
	e.t.Helper()
	var gotState string
	var gotCode sql.NullString
	if err := e.db.QueryRow(`SELECT state, terminal_receipt->'primary_failure'->>'reason_code' FROM acquisitions WHERE id = $1`, root).Scan(&gotState, &gotCode); err != nil {
		e.t.Fatal(err)
	}
	if gotState != state || gotCode.String != code {
		e.t.Fatalf("root %s = %s/%s, want %s/%s", root, gotState, gotCode.String, state, code)
	}
}

// requireReset requires the release out of the selection again: filtered,
// provider_group_not_selected, no current task, its cancelled task kept.
func (e *cancelE2E) requireReset(ids ...string) {
	e.t.Helper()
	for _, id := range ids {
		var verdict, reason string
		var current sql.NullString
		var tasks int
		if err := e.db.QueryRow(`SELECT verdict, reason_code, download_task_id, (SELECT count(*) FROM download_tasks t WHERE t.release_id = r.id) FROM releases r WHERE id = $1`, id).
			Scan(&verdict, &reason, &current, &tasks); err != nil {
			e.t.Fatal(err)
		}
		if verdict != "filtered" || reason != "provider_group_not_selected" || current.Valid || tasks == 0 {
			e.t.Fatalf("release %s = %s/%s current=%v tasks=%d, want reset to provider_group_not_selected", id, verdict, reason, current, tasks)
		}
	}
}

func (e *cancelE2E) downloadFailedAdmissions() int {
	e.t.Helper()
	var count int
	if err := ownerTestDB(e.t).QueryRow(`SELECT count(*) FROM notification_admissions WHERE trigger_kind = 'download_failed'`).Scan(&count); err != nil {
		e.t.Fatal(err)
	}
	return count
}

// reopenEvaluations lists the releases with a release_evaluate item and the
// item's trigger.
func (e *cancelE2E) reopenEvaluations() map[string]string {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT owner_id, input_snapshot->>'trigger' FROM work_items WHERE owner_type = 'release' AND kind = 'release_evaluate'`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	items := map[string]string{}
	for rows.Next() {
		var id, trigger string
		if err := rows.Scan(&id, &trigger); err != nil {
			e.t.Fatal(err)
		}
		items[id] = trigger
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return items
}

// runEvaluations runs the due release evaluations the way the runtime does.
func (e *cancelE2E) runEvaluations() []string {
	e.t.Helper()
	var ran []string
	for {
		claim, err := e.store.ClaimDue(context.Background(), "deselect-evaluator", []string{workitems.KindReleaseEvaluate}, time.Minute)
		if errors.Is(err, workitems.ErrNoDueWork) {
			sort.Strings(ran)
			return ran
		}
		if err != nil {
			e.t.Fatal(err)
		}
		e.finish(claim, e.handle(claim))
		ran = append(ran, claim.Owner.ID)
	}
}

func (e *cancelE2E) setAlphaSelected(selected bool) {
	e.t.Helper()
	if _, err := e.db.Exec(`UPDATE provider_group_selections SET effective_selected = $1, version = version + 1 WHERE id = 'sel-alpha'`, selected); err != nil {
		e.t.Fatal(err)
	}
}

// deselectAlphaBySitePolicy is the site-policy writer's transaction: the
// anime row first, the recomputed selection, then the competition enqueue.
func (e *cancelE2E) deselectAlphaBySitePolicy() {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM managed_anime WHERE id = 'anime-rss-test' FOR UPDATE`); err != nil {
		e.t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_group_selections SET effective_selected = FALSE, version = version + 1 WHERE id = 'sel-alpha'`); err != nil {
		e.t.Fatal(err)
	}
	if _, err := competition.EnqueueTx(ctx, tx, "anime-rss-test", competition.TriggerSitePolicy, competition.WhenAwaiting); err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatal(err)
	}
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// single_copy with two selected groups: alpha downloads episodes 1-3 (two in
// qB, one not yet submitted) and has episode 4 completed; beta's releases of
// 1-3 are covered in flight. Deselecting alpha (by hand or by the site
// policy) cancels alpha's three pre-import downloads, deletes the two in qB
// with their data, raises no notification, resets the releases and admits
// beta's fallbacks; the completed episode 4 is left alone.
func TestDeselectCancelsTheGroupDownloadsAndAdmitsFallbacksPostgres(t *testing.T) {
	for _, writer := range []string{"manual", "site_policy"} {
		t.Run(writer, func(t *testing.T) {
			e := newCancelE2E(t)
			for episode, id := range []string{"a-01", "a-02", "a-03", "a-04"} {
				e.addRelease(id, "source-rss-test", "[Alpha] Test Show - 0"+string(rune('1'+episode))+" [1080p]", episode+1)
			}
			for episode, id := range []string{"b-01", "b-02", "b-03"} {
				e.addRelease(id, "source-beta", "[Beta] Test Show - 0"+string(rune('1'+episode))+" [720p]", episode+1)
			}
			e.evaluate("a-01", "a-02", "a-03", "a-04", "b-01", "b-02", "b-03")
			e.mustRunCompetition("competition_decided")
			e.requireAdmitted("a-01", "a-02", "a-03", "a-04")
			e.requireCoveredInFlight("b-01", "b-02", "b-03")
			first := e.arrangeDownloading("a-01")
			second := e.arrangeDownloading("a-02")
			unsent, unsentRoot := e.currentDownload("a-03")
			completed := e.arrangeCompleted("a-04")
			_, firstRoot := e.currentDownload("a-01")
			_, secondRoot := e.currentDownload("a-02")

			if writer == "manual" {
				e.setAlphaSelected(false)
			} else {
				e.deselectAlphaBySitePolicy()
			}
			for _, id := range []string{first, second, unsent} {
				if task := e.task(id); task.cause != "provider_group_not_selected" {
					t.Fatalf("task %s intent = %+v", id, task)
				}
			}
			if task := e.task(completed); task.state != "completed" || task.cause != "" {
				t.Fatalf("a completed download got an intent: %+v", task)
			}

			// The deselect woke the waiting polls: the worker acts now.
			if handled := sortedCopy(e.runDownloads()); !equalStrings(handled, sortedCopy([]string{first, second, unsent})) {
				t.Fatalf("handled downloads = %v", handled)
			}
			for _, id := range []string{first, second} {
				if task := e.task(id); task.state != "failed" || task.code != downloadapplication.FailureProviderGroupNotAllowed || task.effect != downloadapplication.CancelEffectDeletedWithData {
					t.Fatalf("task %s = %+v", id, task)
				}
			}
			if task := e.task(unsent); task.state != "failed" || task.code != downloadapplication.FailureProviderGroupNotAllowed || task.effect != "not_submitted" {
				t.Fatalf("unsent task = %+v", task)
			}
			if got := sortedCopy(e.remover.calls); !equalStrings(got, sortedCopy([]string{firstRoot, secondRoot})) {
				t.Fatalf("qB deletes = %v, want the two submitted downloads", got)
			}
			if e.provider.syncCalls != 0 || e.provider.lookupCalls != 0 || e.config.calls != 0 {
				t.Fatalf("a cancel reached qB otherwise: polls=%d lookups=%d config=%d", e.provider.syncCalls, e.provider.lookupCalls, e.config.calls)
			}
			if n := e.downloadFailedAdmissions(); n != 0 {
				t.Fatalf("the cancels raised %d download_failed admissions", n)
			}

			for _, root := range []string{firstRoot, secondRoot, unsentRoot} {
				e.settle(root)
				e.requireRoot(root, "cancelled", downloadapplication.FailureProviderGroupNotAllowed)
			}
			e.requireReset("a-01", "a-02", "a-03")
			if items := e.reopenEvaluations(); len(items) != 0 {
				t.Fatalf("a deselected group's releases must not be evaluated: %v", items)
			}
			if task := e.task(completed); task.state != "completed" || task.cause != "" || task.effect != "" {
				t.Fatalf("the completed download was touched: %+v", task)
			}
			assertReleaseState(t, e.db, "a-04", "accepted_pending_download", "accepted", true)

			e.mustRunCompetition("competition_decided")
			e.requireAdmitted("b-01", "b-02", "b-03")
			if n := e.downloadFailedAdmissions(); n != 0 {
				t.Fatalf("download_failed admissions = %d", n)
			}
		})
	}
}

// A group selected again before the worker acts withdraws the intent: the
// download goes on and nothing is deleted.
func TestReselectBeforeTheWorkerWithdrawsTheCancelPostgres(t *testing.T) {
	e := newCancelE2E(t)
	e.addRelease("a-01", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	e.evaluate("a-01")
	e.mustRunCompetition("competition_decided")
	taskID := e.arrangeDownloading("a-01")

	e.setAlphaSelected(false)
	if task := e.task(taskID); task.cause != "provider_group_not_selected" {
		t.Fatalf("intent = %+v", task)
	}
	e.setAlphaSelected(true)
	if handled := e.runDownloads(); len(handled) != 1 || handled[0] != taskID {
		t.Fatalf("handled = %v", handled)
	}
	task := e.task(taskID)
	if task.cause != "" || task.effect != "" || (task.state != "downloading" && task.state != "waiting_retry") {
		t.Fatalf("a withdrawn cancel changed the download: %+v", task)
	}
	if len(e.remover.calls) != 0 || e.provider.syncCalls != 1 {
		t.Fatalf("the withdrawn cancel deleted %v or skipped the poll (%d polls)", e.remover.calls, e.provider.syncCalls)
	}
	assertReleaseState(t, e.db, "a-01", "accepted_pending_download", "accepted", true)
}

// The selection's entry mirrors its exit: a reselected group's releases that
// left with the selection (a cancelled download reset by the settle, a
// waiting release 081 dropped) are evaluated again from their stored facts
// when the group is selected again. No refresh runs and no feed item is new;
// a deselect and reselect inside one transaction schedules nothing.
func TestReselectReopensTheGroupReleasesWithoutARefreshPostgres(t *testing.T) {
	e := newCancelE2E(t)
	e.addRelease("a-01", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	e.evaluate("a-01")
	e.mustRunCompetition("competition_decided")
	e.arrangeDownloading("a-01")
	_, root := e.currentDownload("a-01")
	e.addRelease("a-02", "source-rss-test", "[Alpha] Test Show - 02 [1080p]", 2)
	e.evaluate("a-02")
	e.requireAwaiting("a-02")

	e.setAlphaSelected(false)
	assertReleaseState(t, e.db, "a-02", "filtered", "provider_group_not_selected", false)
	if handled := e.runDownloads(); len(handled) != 1 {
		t.Fatalf("handled = %v", handled)
	}
	e.settle(root)
	e.requireReset("a-01")
	e.drainCompetition()
	if items := e.reopenEvaluations(); len(items) != 0 {
		t.Fatalf("nothing is evaluated while the group is out: %v", items)
	}

	// Selected and deselected again inside one transaction: the committed
	// selection is unchanged, so nothing is scheduled.
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []bool{true, false} {
		if _, err := tx.Exec(`UPDATE provider_group_selections SET effective_selected = $1, version = version + 1 WHERE id = 'sel-alpha'`, selected); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if items := e.reopenEvaluations(); len(items) != 0 {
		t.Fatalf("an intermediate selection scheduled %v", items)
	}

	e.setAlphaSelected(true)
	items := e.reopenEvaluations()
	if len(items) != 2 || items["a-01"] != "group_selected" || items["a-02"] != "group_selected" {
		t.Fatalf("reselect evaluations = %v, want a-01 and a-02 by group_selected", items)
	}
	var refreshes int
	if err := e.db.QueryRow(`SELECT count(*) FROM work_items WHERE kind = 'rss_refresh'`).Scan(&refreshes); err != nil || refreshes != 0 {
		t.Fatalf("refresh items = %d %v", refreshes, err)
	}
	if ran := e.runEvaluations(); !equalStrings(ran, []string{"a-01", "a-02"}) {
		t.Fatalf("evaluated = %v", ran)
	}
	if e.fetcher.calls != 0 {
		t.Fatalf("the reopen fetched the feed %d times", e.fetcher.calls)
	}
	// a-01 keeps its cancelled task as history; it has no current task.
	for _, id := range []string{"a-01", "a-02"} {
		var verdict string
		var current sql.NullString
		if err := e.db.QueryRow(`SELECT verdict, download_task_id FROM releases WHERE id = $1`, id).Scan(&verdict, &current); err != nil {
			t.Fatal(err)
		}
		if verdict != competition.VerdictAwaiting || current.Valid {
			t.Fatalf("release %s = %s current=%v, want awaiting the competition", id, verdict, current)
		}
	}
	e.mustRunCompetition("competition_decided")
	e.requireAdmitted("a-01", "a-02")
	var tasks int
	if err := e.db.QueryRow(`SELECT count(*) FROM download_tasks WHERE release_id = 'a-01'`).Scan(&tasks); err != nil || tasks != 2 {
		t.Fatalf("a-01 tasks = %d %v, want the cancelled one kept and a new one", tasks, err)
	}
}

// A group selected again while its subscription is stopped is an entry the
// resume completes: deselect (the download cancelled and reset, the waiting
// release dropped), stop, reselect (nothing scheduled while stopped), resume.
// The resume evaluates both releases again from their stored facts, with no
// refresh and no fetch, and the competition admits them.
func TestResumeReopensAGroupSelectedWhileStoppedPostgres(t *testing.T) {
	e := newCancelE2E(t)
	e.addRelease("a-01", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	e.evaluate("a-01")
	e.mustRunCompetition("competition_decided")
	e.arrangeDownloading("a-01")
	_, root := e.currentDownload("a-01")
	e.addRelease("a-02", "source-rss-test", "[Alpha] Test Show - 02 [1080p]", 2)
	e.evaluate("a-02")
	e.requireAwaiting("a-02")

	e.setAlphaSelected(false)
	if handled := e.runDownloads(); len(handled) != 1 {
		t.Fatalf("handled = %v", handled)
	}
	e.settle(root)
	e.requireReset("a-01")
	assertReleaseState(t, e.db, "a-02", "filtered", "provider_group_not_selected", false)
	e.drainCompetition()

	setState := func(state string) {
		t.Helper()
		if _, err := e.db.Exec(`UPDATE subscriptions SET state = $1, version = version + 1,
    resumed_at = CASE WHEN $1 = 'active' THEN NOW() ELSE resumed_at END WHERE id = 'sub-rss-test'`, state); err != nil {
			t.Fatal(err)
		}
	}
	setState("stopped")
	e.setAlphaSelected(true)
	if items := e.reopenEvaluations(); len(items) != 0 {
		t.Fatalf("a stopped subscription scheduled %v", items)
	}

	setState("active")
	items := e.reopenEvaluations()
	if len(items) != 2 || items["a-01"] != "group_selected" || items["a-02"] != "group_selected" {
		t.Fatalf("resume evaluations = %v, want a-01 and a-02 by group_selected", items)
	}
	var refreshes int
	if err := e.db.QueryRow(`SELECT count(*) FROM work_items WHERE kind = 'rss_refresh'`).Scan(&refreshes); err != nil || refreshes != 0 {
		t.Fatalf("refresh items = %d %v", refreshes, err)
	}
	if ran := e.runEvaluations(); !equalStrings(ran, []string{"a-01", "a-02"}) {
		t.Fatalf("evaluated = %v", ran)
	}
	if e.fetcher.calls != 0 {
		t.Fatalf("the resume fetched the feed %d times", e.fetcher.calls)
	}
	for _, id := range []string{"a-01", "a-02"} {
		var verdict string
		var current sql.NullString
		if err := e.db.QueryRow(`SELECT verdict, download_task_id FROM releases WHERE id = $1`, id).Scan(&verdict, &current); err != nil {
			t.Fatal(err)
		}
		if verdict != competition.VerdictAwaiting || current.Valid {
			t.Fatalf("release %s = %s current=%v, want awaiting the competition", id, verdict, current)
		}
	}
	e.mustRunCompetition("competition_decided")
	e.requireAdmitted("a-01", "a-02")
}

// A user cancel of a subscription download: the record ends cancelled, the
// release keeps its cancelled task (no reopen), the competition admits the
// fallback, and a retry puts the release back into the competition.
func TestUserCancelAdmitsTheFallbackAndRetryBringsItBackPostgres(t *testing.T) {
	ctx := context.Background()
	e := newCancelE2E(t)
	e.addRelease("a-01", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	e.addRelease("b-01", "source-beta", "[Beta] Test Show - 01 [720p]", 1)
	e.evaluate("a-01", "b-01")
	e.mustRunCompetition("competition_decided")
	e.requireAdmitted("a-01")
	e.requireCoveredInFlight("b-01")
	taskID := e.arrangeDownloading("a-01")
	_, root := e.currentDownload("a-01")

	cancel, err := animeapplication.NewDownloadCancelService(e.db)
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := e.db.QueryRow(`SELECT version FROM acquisitions WHERE id = $1`, root).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := cancel.CancelDownload(ctx, root, version, "user-cancel-key-0001"); err != nil {
		t.Fatal(err)
	}
	if handled := e.runDownloads(); len(handled) != 1 || handled[0] != taskID {
		t.Fatalf("handled = %v", handled)
	}
	if task := e.task(taskID); task.state != "failed" || task.code != downloadapplication.FailureUserCancelled || task.effect != downloadapplication.CancelEffectDeletedWithData {
		t.Fatalf("task = %+v", task)
	}
	if len(e.remover.calls) != 1 || e.remover.calls[0] != root {
		t.Fatalf("qB deletes = %v", e.remover.calls)
	}
	e.settle(root)
	e.requireRoot(root, "cancelled", downloadapplication.FailureUserCancelled)
	if current, _ := e.currentDownload("a-01"); current != taskID {
		t.Fatalf("a user cancel keeps the release on its task: %s", current)
	}
	if n := e.downloadFailedAdmissions(); n != 0 {
		t.Fatalf("a user cancel raised %d download_failed admissions", n)
	}

	e.mustRunCompetition("competition_decided")
	e.requireAdmitted("b-01")
	if items := e.reopenEvaluations(); len(items) != 0 {
		t.Fatalf("a user cancel must not reopen: %v", items)
	}
	result, err := e.service.RetryRelease(ctx, "a-01", "retry-key-user-cancel-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAwaiting || result.Verdict != competition.VerdictAwaiting {
		t.Fatalf("retry = %+v, want awaiting", result)
	}
}
