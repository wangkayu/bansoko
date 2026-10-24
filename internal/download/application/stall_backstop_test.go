package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// This file is the BC 4.1 / 4.2 migration of the former float-ratio stall
// backstop assertions (execution-design 13.16.7 requires the coverage to move
// rather than disappear). The judgment now lives in
// download_finish_sync under the root -> task -> work lock, so every scenario
// below drives a real sync and reads the task row the owner wrote.
//
// Progress facts are arranged on the task columns the migration added:
// verified_bytes_highwater (NULL means "no reliable byte observation yet"),
// payload_total_bytes, last_progress_at and progress_activated_at.

// stalledCorrelation builds a seeded correlation whose provider object is
// downloading under the given raw status. It no longer carries a stall clock:
// the clock lives on the task and is arranged by seedProgressClock.
func stalledCorrelation(t *testing.T, progress float64, providerState string) string {
	t.Helper()
	raw, err := json.Marshal(providerCorrelation{
		ExternalID:    "tag:qb:release-qb-test",
		StableTag:     "qb:release-qb-test",
		Progress:      progress,
		State:         string(domain.TaskDownloading),
		ProviderState: providerState,
	})
	if err != nil {
		t.Fatalf("encode seeded correlation: %v", err)
	}
	return string(raw)
}

// setProviderCorrelation writes the provider object after the progress clock has
// been arranged. The 147 trigger opens the monitoring window on the first
// correlation a task gains, so a scenario that needs a historical clock must seed
// the clock first and the object second.
func setProviderCorrelation(t *testing.T, db *sql.DB, taskID, correlation string) {
	t.Helper()
	if _, err := ownerTestDB(t).ExecContext(context.Background(),
		`UPDATE download_tasks SET provider_correlation=$2::jsonb WHERE id=$1`, taskID, correlation); err != nil {
		t.Fatalf("seed provider correlation: %v", err)
	}
}

// seedProgressClock arranges the integer progress facts a judged task carries.
// A nil high-water mark means no reliable byte observation has ever been made,
// which is what separates DOWNLOAD_PROGRESS_UNVERIFIABLE from a real stall.
func seedProgressClock(t *testing.T, db *sql.DB, taskID string, highwater *int64, total int64, lastProgressAgo, activatedAgo time.Duration) {
	t.Helper()
	ctx := context.Background()
	var mark any
	if highwater != nil {
		mark = *highwater
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `
UPDATE download_tasks
SET verified_bytes_highwater=$2, payload_total_bytes=$3,
    last_progress_at=CURRENT_TIMESTAMP-$4::interval,
    progress_activated_at=CURRENT_TIMESTAMP-$5::interval
WHERE id=$1`, taskID, mark, total, lastProgressAgo.String(), activatedAgo.String()); err != nil {
		t.Fatalf("seed progress clock: %v", err)
	}
}

// readTaskRow reads the terminal facts a committed sync wrote.
func readTaskRow(t *testing.T, db *sql.DB, taskID string) (state string, failureCode, failureReason sql.NullString, correlation providerCorrelation) {
	t.Helper()
	var raw []byte
	if err := db.QueryRowContext(context.Background(), `
SELECT state, failure_code, failure_reason, provider_correlation
FROM download_tasks WHERE id = $1`, taskID).Scan(&state, &failureCode, &failureReason, &raw); err != nil {
		t.Fatalf("read download task: %v", err)
	}
	decoded, err := decodeCorrelation(raw)
	if err != nil {
		t.Fatalf("decode stored correlation: %v", err)
	}
	return state, failureCode, failureReason, decoded
}

// readProgressRow adds the integer progress facts the S1b columns carry.
func readProgressRow(t *testing.T, db *sql.DB, taskID string) (state string, failureCode, failureReason sql.NullString, correlation providerCorrelation, highwater sql.NullInt64, lastProgress sql.NullTime) {
	t.Helper()
	state, failureCode, failureReason, correlation = readTaskRow(t, db, taskID)
	if err := db.QueryRowContext(context.Background(), `
SELECT verified_bytes_highwater, last_progress_at
FROM download_tasks WHERE id = $1`, taskID).Scan(&highwater, &lastProgress); err != nil {
		t.Fatalf("read download progress: %v", err)
	}
	return state, failureCode, failureReason, correlation, highwater, lastProgress
}

// TestQBSyncStallTimeoutFailsTaskWithStalledCodeAndLeavesTheTorrentAlone is the
// migrated backstop case: a reliable high-water mark that stopped growing past
// the frozen stall window fails as DOWNLOAD_STALLED, and nothing is deleted or
// re-submitted on the way out.
func TestQBSyncStallTimeoutFailsTaskWithStalledCodeAndLeavesTheTorrentAlone(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-stalled", "downloading", "null")
	total := int64(1000)
	mark := int64(420)
	seedProgressClock(t, db, "task-qb-stalled", &mark, total, 7*time.Hour, 7*time.Hour)
	setProviderCorrelation(t, db, "task-qb-stalled", stalledCorrelation(t, 0.42, "stalleddl"))
	complete := int64(0)
	provider := &fakeProvider{syncTask: domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskDownloading, ProviderState: "stalleddl", Progress: 0.42,
		ContentCompleteBytes: &complete, PayloadTotalBytes: &total,
	}}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-stalled", workitems.KindQBSync)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted ||
		outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != "DOWNLOAD_STALLED" {
		t.Fatalf("stall outcome=%+v", outcome)
	}

	state, failureCode, failureReason, correlation, highwater, _ := readProgressRow(t, db, "task-qb-stalled")
	if state != "failed" || failureCode.String != "DOWNLOAD_STALLED" {
		t.Fatalf("stalled task state=%q code=%q", state, failureCode.String)
	}
	if !strings.Contains(failureReason.String, "已完成字节") || !strings.Contains(failureReason.String, "未删除任何文件") {
		t.Fatalf("stall reason does not state the observation and the non-action: %q", failureReason.String)
	}
	if correlation.ExternalID != "tag:qb:release-qb-test" {
		t.Fatalf("stall verdict dropped the correlation: %+v", correlation)
	}
	// The high-water mark is preserved: a stall is a statement about what was
	// already observed, never an instruction to forget it.
	if !highwater.Valid || highwater.Int64 != 420 {
		t.Fatalf("stall verdict rewrote the high-water mark: %+v", highwater)
	}

	// R3: nothing was removed, paused or re-submitted. The Provider port has no
	// removal method at all, so the strongest available assertion is that the
	// backstop issued exactly one read and no write of any kind.
	if provider.syncCalls != 1 || provider.fileSubmitCalls != 0 || provider.lookupCalls != 0 {
		t.Fatalf("stall backstop called the provider beyond one sync: %+v", provider)
	}
	var releaseTask sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT download_task_id FROM releases WHERE id = 'release-qb-test'`).Scan(&releaseTask); err != nil {
		t.Fatal(err)
	}
	if releaseTask.String != "task-qb-stalled" {
		t.Fatalf("stall verdict detached the release from its task: %q", releaseTask.String)
	}
}

// TestQBSyncStrictIntegerGrowthRestartsTheProgressClock migrates the old
// "progress movement" case to the integer protocol: only content-complete bytes
// strictly above the known high-water mark advance the clock, and a float ratio
// that wobbles without new bytes does not.
func TestQBSyncStrictIntegerGrowthRestartsTheProgressClock(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		observed  int64
		previous  int64
		wantMoved bool
	}{
		{name: "strict growth restarts", observed: 500, previous: 420, wantMoved: true},
		{name: "a wobbling float ratio does not", observed: 420, previous: 420, wantMoved: false},
		{name: "a falling count never resets", observed: 300, previous: 420, wantMoved: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openDownloadDB(t)
			seedDownloadTask(t, db, "task-qb-moving", "downloading", "null")
			total := int64(1000)
			previous := testCase.previous
			seedProgressClock(t, db, "task-qb-moving", &previous, total, 6*time.Hour+30*time.Minute, 7*time.Hour)
			setProviderCorrelation(t, db, "task-qb-moving", stalledCorrelation(t, 0.42, "stalleddl"))
			observed := testCase.observed
			provider := &fakeProvider{syncTask: domain.ProviderTask{
				ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
				State: domain.TaskDownloading, ProviderState: "stalleddl", Progress: 0.55,
				ContentCompleteBytes: &observed, PayloadTotalBytes: &total,
			}}
			service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
			claim := createClaimedQBWorkItem(t, db, store, "task-qb-moving", workitems.KindQBSync)

			outcome, err := service.Handle(context.Background(), claim)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.wantMoved {
				if outcome.OwnerResult.State != "downloading" {
					t.Fatalf("strict growth restarts the clock and must not fail: %+v", outcome)
				}
			} else if outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != "DOWNLOAD_STALLED" {
				t.Fatalf("a growth-free round past the window must fail as stalled: %+v", outcome)
			}
			_, _, _, _, highwater, lastProgress := readProgressRow(t, db, "task-qb-moving")
			wantMark := testCase.previous
			if testCase.observed > wantMark {
				wantMark = testCase.observed
			}
			if !highwater.Valid || highwater.Int64 != wantMark {
				t.Fatalf("high-water mark=%+v want %d (never below %d)", highwater, wantMark, testCase.previous)
			}
			moved := time.Since(lastProgress.Time) < time.Minute
			if moved != testCase.wantMoved {
				t.Fatalf("clock moved=%v want %v (last_progress_at=%v)", moved, testCase.wantMoved, lastProgress.Time)
			}
		})
	}
}

// TestQBSyncBelowTheThresholdKeepsCountingFromTheOriginalObservation migrates
// the "surviving clock" case: a sync that finds no new bytes must not silently
// restart the window and make the threshold unreachable.
func TestQBSyncBelowTheThresholdKeepsCountingFromTheOriginalObservation(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-counting", "downloading", "null")
	total := int64(1000)
	mark := int64(420)
	seedProgressClock(t, db, "task-qb-counting", &mark, total, 2*time.Hour, 3*time.Hour)
	setProviderCorrelation(t, db, "task-qb-counting", stalledCorrelation(t, 0.42, "stalleddl"))
	observed := int64(420)
	provider := &fakeProvider{syncTask: domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskDownloading, ProviderState: "stalleddl", Progress: 0.42,
		ContentCompleteBytes: &observed, PayloadTotalBytes: &total,
	}}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-counting", workitems.KindQBSync)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "downloading" {
		t.Fatalf("a two-hour stall outcome=%+v", outcome)
	}
	state, failureCode, _, _, _, lastProgress := readProgressRow(t, db, "task-qb-counting")
	if state != "downloading" || failureCode.Valid {
		t.Fatalf("a two-hour stall was failed early: state=%q code=%q", state, failureCode.String)
	}
	if time.Since(lastProgress.Time) < time.Hour {
		t.Fatalf("a motionless sync restarted the progress clock: %v", lastProgress.Time)
	}
}

// TestQBSyncFirstObservationNeverStallsARowWrittenBeforeTheBackstop migrates
// the legacy-row case: a task whose clock was never started keeps its window
// from being backdated by row age.
func TestQBSyncFirstObservationNeverStallsARowWrittenBeforeTheBackstop(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-legacy", "downloading", "null")
	setProviderCorrelation(t, db, "task-qb-legacy",
		`{"external_id":"tag:qb:release-qb-test","stable_tag":"qb:release-qb-test","progress":0.42,"state":"downloading"}`)
	if _, err := db.ExecContext(context.Background(),
		`UPDATE download_tasks SET created_at = CURRENT_TIMESTAMP - INTERVAL '30 days' WHERE id = 'task-qb-legacy'`); err != nil {
		t.Fatal(err)
	}
	total := int64(1000)
	observed := int64(420)
	provider := &fakeProvider{syncTask: domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskDownloading, ProviderState: "stalleddl", Progress: 0.42,
		ContentCompleteBytes: &observed, PayloadTotalBytes: &total,
	}}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-legacy", workitems.KindQBSync)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "downloading" {
		t.Fatalf("legacy row outcome=%+v", outcome)
	}
	state, failureCode, _, _, _, lastProgress := readProgressRow(t, db, "task-qb-legacy")
	if state != "downloading" || failureCode.Valid {
		t.Fatalf("a 30-day-old row was stalled on its first observation: state=%q code=%q", state, failureCode.String)
	}
	if !lastProgress.Valid || time.Since(lastProgress.Time) > time.Minute {
		t.Fatalf("the first observation did not establish a progress clock: %+v", lastProgress)
	}
}

// TestQBSyncHeldStatusesExemptAndQueuedNoLongerDoes migrates the held-torrent
// case and records the BC 4.2 narrowing: a deliberate hold or verify still
// exempts the round, while queueing, allocation and moving no longer do, and an
// unknown or empty status was never exempt.
func TestQBSyncHeldStatusesExemptAndQueuedNoLongerDoes(t *testing.T) {
	for _, testCase := range []struct {
		providerState string
		exempt        bool
	}{
		{providerState: "pauseddl", exempt: true},
		{providerState: "stoppeddl", exempt: true},
		{providerState: "checkingdl", exempt: true},
		{providerState: "checkingup", exempt: true},
		{providerState: "checkingresumedata", exempt: true},
		{providerState: "queueddl", exempt: false},
		{providerState: "allocating", exempt: false},
		{providerState: "moving", exempt: false},
		{providerState: "", exempt: false},
		{providerState: "stalleddl", exempt: false},
	} {
		t.Run(testCase.providerState, func(t *testing.T) {
			db := openDownloadDB(t)
			stateName := testCase.providerState
			if stateName == "" {
				stateName = "empty"
			}
			taskID := "task-qb-held-" + stateName
			seedDownloadTask(t, db, taskID, "downloading", "null")
			total := int64(1000)
			mark := int64(420)
			seedProgressClock(t, db, taskID, &mark, total, 30*time.Hour, 30*time.Hour)
			setProviderCorrelation(t, db, taskID, stalledCorrelation(t, 0.42, testCase.providerState))
			observed := int64(420)
			provider := &fakeProvider{syncTask: domain.ProviderTask{
				ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
				State: domain.TaskDownloading, ProviderState: testCase.providerState, Progress: 0.42,
				ContentCompleteBytes: &observed, PayloadTotalBytes: &total,
			}}
			service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
			claim := createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync)

			if _, err := service.Handle(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			state, failureCode, _, _, _, lastProgress := readProgressRow(t, db, taskID)
			if testCase.exempt {
				if state != "downloading" || failureCode.Valid {
					t.Fatalf("%s held for 30h must be exempt: state=%q code=%q", testCase.providerState, state, failureCode.String)
				}
				// Exemption skips one round only: the clock is not credited, so the
				// pause still counts toward the total download window.
				if time.Since(lastProgress.Time) < 29*time.Hour {
					t.Fatalf("exemption credited the paused time: last_progress_at=%v", lastProgress.Time)
				}
				return
			}
			if state != "failed" || failureCode.String != "DOWNLOAD_STALLED" {
				t.Fatalf("%s is no longer exempt and must stall: state=%q code=%q", testCase.providerState, state, failureCode.String)
			}
		})
	}
}

// TestQBSyncProgressUnverifiableWhenNoReliableBytesYet covers the second stall
// outcome: a current, trustworthy, non-exempt observation that has never carried
// a byte count is not evidence of "no progress".
func TestQBSyncProgressUnverifiableWhenNoReliableBytesYet(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-unverifiable", "downloading", "null")
	seedProgressClock(t, db, "task-qb-unverifiable", nil, 0, 7*time.Hour, 7*time.Hour)
	setProviderCorrelation(t, db, "task-qb-unverifiable", stalledCorrelation(t, 0, "downloading"))
	provider := &fakeProvider{syncTask: domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskDownloading, ProviderState: "downloading", Progress: 0,
	}}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-unverifiable", workitems.KindQBSync)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != "DOWNLOAD_PROGRESS_UNVERIFIABLE" {
		t.Fatalf("unverifiable outcome=%+v", outcome)
	}
	state, failureCode, failureReason, _, highwater, _ := readProgressRow(t, db, "task-qb-unverifiable")
	if state != "failed" || failureCode.String != "DOWNLOAD_PROGRESS_UNVERIFIABLE" {
		t.Fatalf("state=%q code=%q", state, failureCode.String)
	}
	if !strings.Contains(failureReason.String, "尚未给出可信的完成字节数") {
		t.Fatalf("reason must not claim a proven lack of progress: %q", failureReason.String)
	}
	if highwater.Valid {
		t.Fatalf("no reliable bytes must not invent a high-water mark: %+v", highwater)
	}
}

// TestQBSyncObservationUnavailableWhenLookupsFailAndStallExpired covers the
// third outcome, and the priority the ruling fixed: a suppressed lookup with
// budget left is retried, and only once the stall window has expired does the
// task fail as DOWNLOAD_OBSERVATION_UNAVAILABLE -- never as a stall.
func TestQBSyncObservationUnavailableWhenLookupsFailAndStallExpired(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-unavailable", "downloading", "null")
	total := int64(1000)
	mark := int64(420)
	seedProgressClock(t, db, "task-qb-unavailable", &mark, total, 7*time.Hour, 7*time.Hour)
	setProviderCorrelation(t, db, "task-qb-unavailable", stalledCorrelation(t, 0.42, "stalleddl"))
	provider := &fakeProvider{syncErr: context.DeadlineExceeded}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-unavailable", workitems.KindQBSync)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != "DOWNLOAD_OBSERVATION_UNAVAILABLE" {
		t.Fatalf("unavailable outcome=%+v", outcome)
	}
	state, failureCode, failureReason, _, highwater, _ := readProgressRow(t, db, "task-qb-unavailable")
	if state != "failed" || failureCode.String != "DOWNLOAD_OBSERVATION_UNAVAILABLE" {
		t.Fatalf("state=%q code=%q", state, failureCode.String)
	}
	if !strings.Contains(failureReason.String, "本轮未能读取到可信的 qBittorrent 状态") || strings.Contains(failureReason.String, "连续多次") {
		t.Fatalf("reason must state that the observation, not the progress, is unknown: %q", failureReason.String)
	}
	// A retained high-water mark must not promote an unobservable round into a
	// claim of proven stall.
	if !highwater.Valid || highwater.Int64 != 420 {
		t.Fatalf("unavailable verdict rewrote the retained mark: %+v", highwater)
	}
	if provider.syncCalls != 1 {
		t.Fatalf("unavailable verdict called the provider %d times", provider.syncCalls)
	}
}

// TestQBSyncSuppressedLookupKeepsPollingWhileStallIsNotYetExpired records the
// other half: with the stall window still open and budget left, a failed lookup
// is one counted error and a bounded retry, never a terminal verdict.
func TestQBSyncSuppressedLookupKeepsPollingWhileStallIsNotYetExpired(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-retry", "downloading", "null")
	total := int64(1000)
	mark := int64(420)
	seedProgressClock(t, db, "task-qb-retry", &mark, total, time.Hour, time.Hour)
	setProviderCorrelation(t, db, "task-qb-retry", stalledCorrelation(t, 0.42, "stalleddl"))
	provider := &fakeProvider{syncErr: context.DeadlineExceeded}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-retry", workitems.KindQBSync)

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "downloading" {
		t.Fatalf("a bounded retry must stay alive: %+v", outcome)
	}
	state, failureCode, _, _, _, _ := readProgressRow(t, db, "task-qb-retry")
	if state != "downloading" || failureCode.Valid {
		t.Fatalf("a failed lookup before the stall window must not terminate: state=%q code=%q", state, failureCode.String)
	}
	if countAvailableQBSyncItems(t, db, "task-qb-retry") < 1 {
		t.Fatal("a bounded retry must leave a scheduled successor")
	}
}

// TestStalledTaskManualRetryDoesNotReviveTerminalOwner keeps the terminal-owner
// guard on the new code.
func TestStalledTaskManualRetryDoesNotReviveTerminalOwner(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-recover", "downloading", "null")
	total := int64(1000)
	mark := int64(420)
	seedProgressClock(t, db, "task-qb-recover", &mark, total, 8*time.Hour, 8*time.Hour)
	setProviderCorrelation(t, db, "task-qb-recover", stalledCorrelation(t, 0.42, "stalleddl"))
	observed := int64(420)
	provider := &fakeProvider{syncTask: domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskDownloading, ProviderState: "stalleddl", Progress: 0.42,
		ContentCompleteBytes: &observed, PayloadTotalBytes: &total,
	}}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})

	claim := createClaimedQBWorkItem(t, db, store, "task-qb-recover", workitems.KindQBSync)
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != "DOWNLOAD_STALLED" {
		t.Fatalf("first sync=%+v", outcome)
	}

	// A legacy manual retry may create work; it cannot reopen the failed owner.
	readOwner := func(ctx context.Context, scope txscope.Tx) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("read owner transaction is nil")
		}
		var version int64
		err := tx.QueryRowContext(ctx, `SELECT version FROM download_tasks WHERE id = $1 FOR UPDATE`, "task-qb-recover").Scan(&version)
		return version, err
	}
	_, err = store.ManualRetry(ctx, workitems.OwnerRef{Type: workitems.OwnerDownloadTask, ID: "task-qb-recover"},
		claim.ID, "用户确认来源仍可用", "manual-retry-stalled-1", readOwner)
	if err == nil || !strings.Contains(err.Error(), "SYNC_WORK_TICKET_BINDING") {
		t.Fatalf("terminal retry should reject copied issued identity: %v", err)
	}
	state, code, _, _, _, _ := readProgressRow(t, db, "task-qb-recover")
	if state != "failed" || code.String != "DOWNLOAD_STALLED" {
		t.Fatalf("terminal owner revived: %s %s", state, code.String)
	}
	if provider.syncCalls != 1 {
		t.Fatalf("terminal retry touched provider: %d", provider.syncCalls)
	}
	if countAvailableQBSyncItems(t, db, "task-qb-recover") != 0 {
		t.Fatal("terminal retry left successor")
	}
}
