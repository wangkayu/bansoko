package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/terminal/settlement"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/apperrors"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/workitems"
)

// Execution-attempt history reads, plus the fail-and-drain fixture
// shared by the one-shot PostgreSQL tests.

type oneShotMarkerState struct {
	state          string
	version        int64
	epoch          int64
	outcome        sql.NullString
	retentionUntil sql.NullTime
	currentAttempt sql.NullString
	terminalAt     sql.NullTime
	terminalReason sql.NullString
	terminalCode   sql.NullString
}

func readOneShotMarkerState(t *testing.T, db *sql.DB, oneShotID string) oneShotMarkerState {
	t.Helper()
	var state oneShotMarkerState
	if err := db.QueryRowContext(context.Background(), `
SELECT state, version, admission_epoch, terminal_outcome, retention_until, current_attempt_id,
       terminal_at, terminal_reason, terminal_reason_code
FROM one_shot_acquisitions WHERE id = $1`, oneShotID).Scan(
		&state.state, &state.version, &state.epoch, &state.outcome, &state.retentionUntil,
		&state.currentAttempt, &state.terminalAt, &state.terminalReason, &state.terminalCode); err != nil {
		t.Fatalf("read one-shot marker: %v", err)
	}
	return state
}

// failAndDrainOneShot forces the current one-shot chain into the terminal
// failed / reclaim_pending shape the real drain produces: the live download
// work and download task fail, formal settlement settles the acquisition root,
// and one retention pass settles the marker and its current attempt.
func failAndDrainOneShot(t *testing.T, db *sql.DB, oneShotID string) {
	t.Helper()
	ctx := context.Background()
	var downloadTaskID string
	var attemptID string
	var rootID string
	if err := db.QueryRowContext(ctx, `
SELECT m.download_task_id, m.current_attempt_id, d.acquisition_id
FROM one_shot_acquisitions m JOIN download_tasks d ON d.id = m.download_task_id
WHERE m.id = $1`, oneShotID).Scan(&downloadTaskID, &attemptID, &rootID); err != nil {
		t.Fatalf("read one-shot anchors: %v", err)
	}
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `
UPDATE work_items
SET last_error_code = 'qB_SUBMIT_FAILED', last_error_reason = 'forced download failure'
WHERE owner_type = 'download_task' AND owner_id = $1 AND state IN ('available', 'leased')`, downloadTaskID); err != nil {
		t.Fatalf("annotate one-shot download work item: %v", err)
	}
	if _, err := workItemFixtureDB(t).ExecContext(ctx, `
SELECT public.work_item_close(id,'failed') FROM work_items
WHERE owner_type = 'download_task' AND owner_id = $1 AND state IN ('available', 'leased')`, downloadTaskID); err != nil {
		t.Fatalf("fail one-shot download work item: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE download_tasks
SET state = 'failed', failure_code = 'qB_SUBMIT_FAILED', failure_reason = 'forced download failure',
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1`, downloadTaskID); err != nil {
		t.Fatalf("fail one-shot download task: %v", err)
	}
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatalf("construct root settlement service: %v", err)
	}
	if status, err := settler.SettleRoot(ctx, rootID); err != nil || status != "settled" {
		t.Fatalf("settle one-shot root: status=%q err=%v", status, err)
	}
	retention, err := workitems.NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatalf("construct retention service: %v", err)
	}
	result, err := retention.RunPass(ctx)
	if err != nil {
		t.Fatalf("drain one-shot chain: %v", err)
	}
	if result.Drained != 1 {
		t.Fatalf("drain result = %+v, want exactly one drained marker", result)
	}
}

// admitHumanOneShot admits one human current_input acquisition and
// returns its handle.
func admitHumanOneShot(t *testing.T, service *oneShotTestServices, seed SelectedSeed, snapshot animecatalogdomain.ProviderRSSPreviewResult, key string) string {
	t.Helper()
	_, input := currentInputForSeed(t, service, seed, nil)
	input.Seed = bindSeedIdentity(seed, snapshot)
	result, err := service.AcquireSeedOnce(context.Background(), input, key)
	if err != nil {
		t.Fatalf("admit one-shot acquisition: %v", err)
	}
	return result.OneShotID
}

func TestListOneShotExecutionAttemptsPaginatesNewestFirst(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	oneShotID := admitHumanOneShot(t, service, seed, snapshot, "one-shot-attempts-page-0001")

	failAndDrainOneShot(t, db, oneShotID)
	failed := readOneShotMarkerState(t, db, oneShotID)
	// Historical retry receipts (written before manual recovery was retired)
	// still paginate.
	previous := failed.currentAttempt.String
	for sequence := 2; sequence <= 3; sequence++ {
		id := fmt.Sprintf("historical-attempt-%d", sequence)
		if _, err := db.ExecContext(ctx, `INSERT INTO one_shot_execution_attempts(id,acquisition_id,sequence,previous_attempt_id,mode,resume_stage,input_snapshot,input_digest,state,failed_stage,outcome_code,outcome_reason,terminal_at) SELECT $1,acquisition_id,$2,$3,'retry','download',input_snapshot,input_digest,'failed','download','historical_failure','historical receipt',CURRENT_TIMESTAMP FROM one_shot_execution_attempts WHERE id=$3`, id, sequence, previous); err != nil {
			t.Fatal(err)
		}
		previous = id
	}

	page, err := service.ListOneShotExecutionAttempts(ctx, oneShotID, 2, "")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].Sequence != 3 || page.Items[1].Sequence != 2 {
		t.Fatalf("first page = %+v, want sequences 3,2", page.Items)
	}
	if page.NextCursor == nil {
		t.Fatal("first page has no next_cursor")
	}
	if page.Items[0].Mode != string(animeport.OneShotAttemptModeRetry) || page.Items[0].ResumeStage == nil || *page.Items[0].ResumeStage != "download" {
		t.Fatalf("first row = %+v, want a retry resuming download", page.Items[0])
	}

	second, err := service.ListOneShotExecutionAttempts(ctx, oneShotID, 2, *page.NextCursor)
	if err != nil {
		t.Fatalf("list attempts page 2: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Sequence != 1 || second.Items[0].Mode != string(animeport.OneShotAttemptModeInitial) {
		t.Fatalf("second page = %+v, want the initial attempt", second.Items)
	}
	if second.NextCursor != nil {
		t.Fatalf("second page next_cursor = %q, want nil at the end", *second.NextCursor)
	}

	if _, err := service.ListOneShotExecutionAttempts(ctx, oneShotID, 0, "not-a-cursor"); err == nil {
		t.Fatal("a malformed cursor must be refused")
	}
	if _, err := service.ListOneShotExecutionAttempts(ctx, oneShotID, 500, ""); err == nil {
		t.Fatal("an out-of-range limit must be refused")
	}
	if _, err := service.ListOneShotExecutionAttempts(ctx, "missing-acquisition", 20, ""); err == nil {
		t.Fatal("a missing acquisition must be a typed 404")
	}
}

func TestGetOneShotExecutionAttemptRejectsCrossAcquisition(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	first := admitHumanOneShot(t, service, seed, snapshot, "one-shot-attempt-detail-0001")
	second := admitHumanOneShot(t, service, seed, snapshot, "one-shot-attempt-detail-0002")

	firstState := readOneShotMarkerState(t, db, first)
	attempt, err := service.GetOneShotExecutionAttempt(ctx, first, firstState.currentAttempt.String)
	if err != nil {
		t.Fatalf("read own attempt: %v", err)
	}
	if attempt.AcquisitionID != first || attempt.Sequence != 1 || attempt.InputSnapshot == nil {
		t.Fatalf("attempt detail = %+v", attempt)
	}

	if _, err := service.GetOneShotExecutionAttempt(ctx, second, firstState.currentAttempt.String); err == nil {
		t.Fatal("a cross-acquisition attempt read must be refused")
	} else {
		var notFound *apperrors.NotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("cross-acquisition error = %v, want a typed 404", err)
		}
	}
}
