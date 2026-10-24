package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// These tests verify the integer progress high-water mark and the three stall
// outcomes inside the owner-held function, plus the priority of the sync error
// budget over the stall judgment.
//
// The stall threshold is frozen on the task from the acquisition limits, so the
// scenarios set stall_seconds to 1 and arrange real elapsed time rather than
// rewriting the clock. Each probe needs its own leased qb_sync claim holding an
// issued ticket, because a finished work item can only be read as an idempotent
// receipt afterwards.

// progressProbe arranges one accepted download task and returns the owner and
// runtime handles. limitsPatch overrides top-level limit keys; syncPatch
// overrides keys inside limits.sync.
func progressProbe(t *testing.T, tag string, limitsPatch, syncPatch map[string]any) (*sql.DB, *sql.DB, queuedParams) {
	t.Helper()
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, tag)
	p := validQueued(t, tag, tag+"-key", tag)
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	limits := input["limits"].(map[string]any)
	for key, value := range limitsPatch {
		limits[key] = value
	}
	if len(syncPatch) > 0 {
		sync := limits["sync"].(map[string]any)
		for key, value := range syncPatch {
			sync[key] = value
		}
	}
	p.InputCanonical = canonicalJSON(t, input)
	p.AdmissionDigest = acqDomainDigest("acquisition-input:v1", []byte(p.InputCanonical))
	if _, err := callQueuedAccept(t, owner, p); err != nil {
		t.Fatal(err)
	}
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtimeDB := openRuntimeConnection(t, password)
	t.Cleanup(func() { runtimeDB.Close() })
	return owner, runtimeDB, p
}

// armSyncClaim leases a fresh qb_sync work item whose frozen ordinal matches the
// task, and takes an issued ticket from the owner-held gate on the runtime role.
func armSyncClaim(t *testing.T, owner, runtimeDB *sql.DB, p queuedParams, name string) string {
	t.Helper()
	ctx := context.Background()
	var ordinal int
	if err := owner.QueryRowContext(ctx, `SELECT next_poll_ordinal FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(map[string]any{"poll_ordinal": ordinal})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx,
		`SELECT work_item_create($1,'download_task',$2,'qb_sync',$3,5,$4,1,0::double precision,7::smallint)`,
		name, p.TaskID, p.TaskIdempotency, string(snapshot)).Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx,
		`UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, name); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,$3,'progress-session')`, p.TaskID, name, ordinal).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var grant struct {
		Granted bool   `json:"granted"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Granted {
		t.Fatalf("sync grant refused: %s", grant.Reason)
	}
	return name
}

// arrangeProgress seeds the integer progress facts a judged task carries.
func arrangeProgress(t *testing.T, owner *sql.DB, task string, highwater *int64, total int64, lastProgressAgo string) {
	t.Helper()
	ctx := context.Background()
	var mark any
	if highwater != nil {
		mark = *highwater
	}
	if _, err := owner.ExecContext(ctx, `
UPDATE download_tasks SET verified_bytes_highwater=$2, payload_total_bytes=$3,
    last_progress_at=CURRENT_TIMESTAMP-$4::interval,
    progress_activated_at=CURRENT_TIMESTAMP-$4::interval
WHERE id=$1`, task, mark, total, lastProgressAgo); err != nil {
		t.Fatalf("arrange progress: %v", err)
	}
}

// readProgressFacts reads the committed progress facts of a probe task.
func readProgressFacts(t *testing.T, db *sql.DB, task string) (state, code string, highwater, total sql.NullInt64, activated, lastProgress sql.NullTime) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), `
SELECT state, COALESCE(failure_code,''), verified_bytes_highwater, payload_total_bytes,
       progress_activated_at, last_progress_at
FROM download_tasks WHERE id=$1`, task).Scan(&state, &code, &highwater, &total, &activated, &lastProgress); err != nil {
		t.Fatalf("read progress facts: %v", err)
	}
	return state, code, highwater, total, activated, lastProgress
}

func finishSyncProbe(t *testing.T, runtimeDB *sql.DB, p queuedParams, work, correlation string) (state, code, reasonText string) {
	t.Helper()
	// Legacy test vectors are decoded in the fixture only; SQL receives typed facts.
	var observation struct {
		Bytes *int64 `json:"content_complete_bytes"`
		Total *int64 `json:"payload_total_bytes"`
		State string `json:"provider_state"`
	}
	if err := json.Unmarshal([]byte(correlation), &observation); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	err := runtimeDB.QueryRowContext(context.Background(),
		`SELECT download_finish_sync($1,$2,1,'progress-session',1,'downloading',$3,'qb_sync_succeeded','',false,$4,$5,$6,$7,NULL,NULL,NULL)`,
		p.TaskID, work, correlation, observation.Bytes, observation.Total, observation.State, work+"-next").Scan(&raw)
	if err != nil {
		t.Fatalf("finish sync: %v", err)
	}
	var reply struct {
		State      string `json:"state"`
		ReasonCode string `json:"reason_code"`
		ReasonText string `json:"reason_text"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.State, reply.ReasonCode, reply.ReasonText
}

// TestDownloadProgressSubmitConfirmationOpensTheMonitoringWindow verifies that
// the transaction that first records the provider object opens the monitoring
// window, before a single byte is complete.
func TestDownloadProgressSubmitConfirmationOpensTheMonitoringWindow(t *testing.T) {
	owner, runtimeDB, p := progressProbe(t, "progress-activate", map[string]any{"stall_seconds": 3600}, nil)
	work := armSyncClaim(t, owner, runtimeDB, p, "activate-sync")
	state, _, _ := finishSyncProbe(t, runtimeDB, p, work, `{"external_id":"tag:progress"}`)
	if state != "downloading" {
		t.Fatalf("state=%s", state)
	}
	gotState, gotCode, highwater, total, activated, lastProgress := readProgressFacts(t, owner, p.TaskID)
	if gotState != "downloading" || gotCode != "" {
		t.Fatalf("state=%s code=%s", gotState, gotCode)
	}
	if !activated.Valid || !lastProgress.Valid {
		t.Fatalf("the monitoring window must open on first confirmation: activated=%+v last_progress=%+v", activated, lastProgress)
	}
	if !activated.Time.Equal(lastProgress.Time) {
		t.Fatalf("the progress clock must start at the activation instant: %v vs %v", activated.Time, lastProgress.Time)
	}
	// No byte evidence exists yet, so the mark stays NULL: that is what separates
	// DOWNLOAD_PROGRESS_UNVERIFIABLE from a proven stall.
	if highwater.Valid || total.Valid {
		t.Fatalf("no byte observation must not invent a mark: %+v %+v", highwater, total)
	}
}

// TestDownloadProgressHighWaterMarkOnlyRisesAndNeverResetsOnZero pins the mark against
// every shape a provider can report, each in its own committed transaction.
func TestDownloadProgressHighWaterMarkOnlyRisesAndNeverResetsOnZero(t *testing.T) {
	for index, probe := range []struct {
		name      string
		bytes     int64
		total     int64
		wantMark  int64
		wantMoved bool
	}{
		{name: "a falling count does not pull the mark back", bytes: 200, total: 1000, wantMark: 500, wantMoved: false},
		{name: "the same count is not new progress", bytes: 500, total: 1000, wantMark: 500, wantMoved: false},
		{name: "a reliable zero never restarts the clock", bytes: 0, total: 1000, wantMark: 500, wantMoved: false},
		{name: "strict growth advances the mark and the clock", bytes: 900, total: 1000, wantMark: 900, wantMoved: true},
		{name: "a second strict growth advances again", bytes: 950, total: 1000, wantMark: 950, wantMoved: true},
		{name: "a regression keeps the highest known mark", bytes: 400, total: 1000, wantMark: 500, wantMoved: false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			// A well-inside window that only strict growth may move. The mark is
			// monotonic, so this subtest gets its own accepted task rather than
			// asking a previous round's committed mark to move backwards.
			owner, runtimeDB, p := progressProbe(t, "progress-hw-"+string(rune('a'+index)),
				map[string]any{"stall_seconds": 3600}, nil)
			existing := int64(500)
			arrangeProgress(t, owner, p.TaskID, &existing, 1000, "1 minute")
			work := armSyncClaim(t, owner, runtimeDB, p, "highwater-sync")
			correlation := `{"external_id":"tag:progress","state":"downloading","provider_state":"downloading","content_complete_bytes":` +
				jsonInt(probe.bytes) + `,"payload_total_bytes":` + jsonInt(probe.total) + `}`
			state, _, _ := finishSyncProbe(t, runtimeDB, p, work, correlation)
			if state != "downloading" {
				t.Fatalf("a moving download must stay alive: state=%s", state)
			}
			gotState, gotCode, highwater, _, _, lastProgress := readProgressFacts(t, owner, p.TaskID)
			if gotState != "downloading" || gotCode != "" {
				t.Fatalf("a moving download must stay alive: state=%s code=%s", gotState, gotCode)
			}
			if !highwater.Valid || highwater.Int64 != probe.wantMark {
				t.Fatalf("high-water mark=%+v want %d", highwater, probe.wantMark)
			}
			moved := time.Since(lastProgress.Time) < 30*time.Second
			if moved != probe.wantMoved {
				t.Fatalf("clock moved=%v want %v (last_progress_at=%v)", moved, probe.wantMoved, lastProgress.Time)
			}
		})
	}
}

// TestDownloadProgressStalledProgressFailsWithStalledCode covers the proven-stall
// outcome: a reliable mark, no strict growth, window expired.
func TestDownloadProgressStalledProgressFailsWithStalledCode(t *testing.T) {
	owner, runtimeDB, p := progressProbe(t, "progress-stalled", map[string]any{"stall_seconds": 1}, nil)
	mark := int64(420)
	arrangeProgress(t, owner, p.TaskID, &mark, 1000, "1 hour")
	work := armSyncClaim(t, owner, runtimeDB, p, "stalled-sync")
	state, code, reasonText := finishSyncProbe(t, runtimeDB, p, work,
		`{"external_id":"tag:progress","state":"downloading","provider_state":"stalleddl","content_complete_bytes":420,"payload_total_bytes":1000}`)
	if state != "failed" || code != "DOWNLOAD_STALLED" {
		t.Fatalf("state=%s code=%s", state, code)
	}
	if reasonText == "" {
		t.Fatal("the committed reason text must travel with the code")
	}
	gotState, gotCode, highwater, _, _, _ := readProgressFacts(t, owner, p.TaskID)
	if gotState != "failed" || gotCode != "DOWNLOAD_STALLED" {
		t.Fatalf("state=%s code=%s", gotState, gotCode)
	}
	// The mark survives the verdict: a stall states what was already observed.
	if !highwater.Valid || highwater.Int64 != 420 {
		t.Fatalf("mark=%+v", highwater)
	}
}

// TestDownloadProgressMissingBytesFailsWithProgressUnverifiable covers the second
// outcome: a current, trustworthy, non-exempt observation that never carried a
// byte count is not evidence of "no progress".
func TestDownloadProgressMissingBytesFailsWithProgressUnverifiable(t *testing.T) {
	owner, runtimeDB, p := progressProbe(t, "progress-unverified", map[string]any{"stall_seconds": 1}, nil)
	arrangeProgress(t, owner, p.TaskID, nil, 0, "1 hour")
	work := armSyncClaim(t, owner, runtimeDB, p, "unverified-sync")
	state, code, reasonText := finishSyncProbe(t, runtimeDB, p, work,
		`{"external_id":"tag:progress","state":"downloading","provider_state":"downloading"}`)
	if state != "failed" || code != "DOWNLOAD_PROGRESS_UNVERIFIABLE" {
		t.Fatalf("state=%s code=%s", state, code)
	}
	if reasonText == "" {
		t.Fatal("the committed reason text must travel with the code")
	}
	gotState, gotCode, highwater, _, _, _ := readProgressFacts(t, owner, p.TaskID)
	if gotState != "failed" || gotCode != "DOWNLOAD_PROGRESS_UNVERIFIABLE" || highwater.Valid {
		t.Fatalf("state=%s code=%s mark=%+v", gotState, gotCode, highwater)
	}
}

// TestDownloadProgressFailedLookupFailsWithObservationUnavailable covers the third
// outcome: a suppressed lookup carries no observation, and a retained mark must
// not promote it into a claim of proven stall.
func TestDownloadProgressFailedLookupFailsWithObservationUnavailable(t *testing.T) {
	owner, runtimeDB, p := progressProbe(t, "progress-unavailable",
		map[string]any{"stall_seconds": 1}, map[string]any{"max_consecutive_errors": 5, "max_total_errors": 5})
	mark := int64(420)
	arrangeProgress(t, owner, p.TaskID, &mark, 1000, "1 hour")
	work := armSyncClaim(t, owner, runtimeDB, p, "unavailable-sync")
	var raw []byte
	if err := runtimeDB.QueryRowContext(context.Background(),
		`SELECT download_finish_sync($1,$2,1,'progress-session',1,'downloading',NULL,'provider_unavailable','temporary',true,NULL,NULL,NULL,'unavailable-next',NULL,NULL,NULL)`,
		p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		State      string `json:"state"`
		ReasonCode string `json:"reason_code"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.State != "failed" || reply.ReasonCode != "DOWNLOAD_OBSERVATION_UNAVAILABLE" {
		t.Fatalf("reply=%+v", reply)
	}
	gotState, gotCode, highwater, _, _, _ := readProgressFacts(t, owner, p.TaskID)
	if gotState != "failed" || gotCode != "DOWNLOAD_OBSERVATION_UNAVAILABLE" || !highwater.Valid || highwater.Int64 != 420 {
		t.Fatalf("state=%s code=%s mark=%+v", gotState, gotCode, highwater)
	}
}

// TestDownloadProgressSuppressedLookupKeepsPollingWhileStallIsNotExpired pins the
// other half: with the window still open and budget left, a failed lookup is one
// counted error and a bounded retry, never a terminal verdict.
func TestDownloadProgressSuppressedLookupKeepsPollingWhileStallIsNotExpired(t *testing.T) {
	owner, runtimeDB, p := progressProbe(t, "progress-retry",
		map[string]any{"stall_seconds": 3600}, map[string]any{"max_consecutive_errors": 5, "max_total_errors": 5})
	mark := int64(420)
	arrangeProgress(t, owner, p.TaskID, &mark, 1000, "1 minute")
	work := armSyncClaim(t, owner, runtimeDB, p, "retry-sync")
	var raw []byte
	if err := runtimeDB.QueryRowContext(context.Background(),
		`SELECT download_finish_sync($1,$2,1,'progress-session',1,'downloading',NULL,'provider_unavailable','temporary',true,NULL,NULL,NULL,'retry-next',NULL,NULL,NULL)`,
		p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		State      string `json:"state"`
		ReasonCode string `json:"reason_code"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	// No provider object was ever confirmed for this task, so the frozen branch
	// falls back to the task's own queued state instead of inventing one.
	if reply.State != "queued" {
		t.Fatalf("a failed lookup inside the window must retry: %+v", reply)
	}
	gotState, gotCode, _, _, _, _ := readProgressFacts(t, owner, p.TaskID)
	if gotState != "queued" || gotCode != "" {
		t.Fatalf("state=%s code=%s", gotState, gotCode)
	}
	// A task whose provider object was never confirmed stays queued, and the
	// frozen sync branch only schedules a sync successor for submitting,
	// downloading or waiting_retry: the retry of an unconfirmed submit belongs to
	// the submit path, so there is deliberately no sync successor here.
	var successor int
	if err := owner.QueryRowContext(context.Background(),
		`SELECT count(*) FROM work_item_history WHERE id='retry-next'`).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	if successor != 0 {
		t.Fatalf("an unconfirmed submit must not schedule a sync successor: %d", successor)
	}
}

// TestDownloadProgressErrorBudgetKeepsPriorityOverTheStallVerdict pins the precedence:
// when one transaction satisfies both the frozen sync error budget and the stall
// window, the budget branch settles the task and the stall judgment never runs.
func TestDownloadProgressErrorBudgetKeepsPriorityOverTheStallVerdict(t *testing.T) {
	owner, runtimeDB, p := progressProbe(t, "progress-priority",
		map[string]any{"stall_seconds": 1}, map[string]any{"max_consecutive_errors": 1, "max_total_errors": 1})
	mark := int64(420)
	arrangeProgress(t, owner, p.TaskID, &mark, 1000, "1 hour")
	work := armSyncClaim(t, owner, runtimeDB, p, "priority-sync")
	var raw []byte
	if err := runtimeDB.QueryRowContext(context.Background(),
		`SELECT download_finish_sync($1,$2,1,'progress-session',1,'downloading',NULL,'provider_unavailable','temporary',true,NULL,NULL,NULL,'priority-next',NULL,NULL,NULL)`,
		p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		State      string `json:"state"`
		ReasonCode string `json:"reason_code"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.State != "failed" || reply.ReasonCode != "SYNC_ERROR_BUDGET_EXHAUSTED" {
		t.Fatalf("the frozen budget branch must keep priority: %+v", reply)
	}
	gotState, gotCode, highwater, _, _, _ := readProgressFacts(t, owner, p.TaskID)
	if gotState != "failed" || gotCode != "SYNC_ERROR_BUDGET_EXHAUSTED" {
		t.Fatalf("state=%s code=%s", gotState, gotCode)
	}
	if !highwater.Valid || highwater.Int64 != 420 {
		t.Fatalf("mark=%+v", highwater)
	}
	var successor int
	if err := owner.QueryRowContext(context.Background(),
		`SELECT count(*) FROM work_item_history WHERE id='priority-next'`).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	if successor != 0 {
		t.Fatalf("a settled verdict must leave no normal successor: %d", successor)
	}
}

// TestDownloadProgressGuardRejectsForgedFacts pins the row guard: the mark
// never falls, the trusted total never changes, bytes never exceed the total, the
// activation instant is written once, and the clock never rewinds.
func TestDownloadProgressGuardRejectsForgedFacts(t *testing.T) {
	owner, _, p := progressProbe(t, "progress-guard", map[string]any{"stall_seconds": 3600}, nil)
	ctx := context.Background()
	mark := int64(500)
	arrangeProgress(t, owner, p.TaskID, &mark, 1000, "1 minute")
	for _, probe := range []struct{ name, patch, want string }{
		{"a falling mark", `verified_bytes_highwater=100`, "DOWNLOAD_HIGHWATER_REGRESSED"},
		{"a rewritten total", `payload_total_bytes=2000`, "DOWNLOAD_TOTAL_SIZE_CHANGED"},
		{"a rewritten activation instant", `progress_activated_at=CURRENT_TIMESTAMP`, "DOWNLOAD_PROGRESS_ACTIVATED_IMMUTABLE"},
		{"a rewound progress clock", `last_progress_at=CURRENT_TIMESTAMP-INTERVAL '1 hour'`, "DOWNLOAD_PROGRESS_CLOCK_REGRESSED"},
	} {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE download_tasks SET `+probe.patch+` WHERE id=$1`, p.TaskID)
		tx.Rollback()
		if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != probe.want {
			t.Fatalf("%s accepted: %v", probe.name, err)
		}
	}
	// Bytes above the trusted total needs a row whose total is still NULL: on a
	// row that already carries one, the total-change check fires first by design.
	fresh, _, freshParams := progressProbe(t, "progress-guard-total", map[string]any{"stall_seconds": 3600}, nil)
	totalTx, err := fresh.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = totalTx.ExecContext(ctx, `UPDATE download_tasks SET verified_bytes_highwater=600,payload_total_bytes=550 WHERE id=$1`, freshParams.TaskID)
	totalTx.Rollback()
	if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_BYTES_ABOVE_TOTAL" {
		t.Fatalf("bytes above the trusted total accepted: %v", err)
	}
}

func jsonInt(value int64) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "0"
	}
	return string(raw)
}
