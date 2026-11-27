package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/events"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

const dispatchFinalizeTimeout = 5 * time.Second

func (d *Dispatcher) runClaimedDispatch(ctx context.Context, runID, kind string, scheduled bool) {
	if ctx.Err() != nil {
		_ = d.finishRun(ctx, runID, unconfirmedDispatch())
		return
	}
	result := unconfirmedDispatch()
	defer func() {
		// A panic leaves the result unconfirmed. finishRun logs persistence failures.
		_ = recover()
		_ = d.finishRun(ctx, runID, result)
	}()
	counts, err := d.runDispatch(ctx, kind, scheduled)
	result = dispatchOutcome(ctx, counts, err)
}

type finalizedDispatch struct{ id, kind, trigger string }

func (d *Dispatcher) finishRun(parent context.Context, runID string, result dispatchResult) error {
	// Finalize the claimed record even when shutdown canceled the work, but do
	// not keep the worker waiting indefinitely for a database or transaction lock.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), dispatchFinalizeTimeout)
	defer cancel()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err == nil {
		defer tx.Rollback()
		var run finalizedDispatch
		run, err = d.finalizeDispatchTx(ctx, tx, runID, result, false)
		if err == nil && run.id == "" {
			return nil // A recovery path already settled this run.
		}
		if err == nil {
			err = tx.Commit()
			if err == nil {
				d.publishDispatchResult(ctx, run, result)
				return nil
			}
		}
	}
	d.logError(ctx, observabilityport.RuntimeEvent{
		Message: "automation dispatch run finish failed", Operation: "finish_run",
		ReasonCode: "finish_run_failed", OwnerType: "automation_dispatch_run", OwnerID: runID,
		ErrorClass: "finish_run",
	})
	return fmt.Errorf("finish dispatch run: %w", err)
}

// Both normal completion and recovery settle the same running row and health
// summary atomically. Recovered schedules are due immediately, as before.
func (d *Dispatcher) finalizeDispatchTx(ctx context.Context, tx *sql.Tx, id string, result dispatchResult, recovered bool) (finalizedDispatch, error) {
	counts := result.counts
	var completed *int
	var upstream json.RawMessage
	if result.state != "unconfirmed" {
		completed = &counts.completed
		if counts.upstream != nil {
			var err error
			upstream, err = json.Marshal(counts.upstream)
			if err != nil {
				return finalizedDispatch{}, fmt.Errorf("encode dispatch upstream outcomes: %w", err)
			}
		}
	}
	run := finalizedDispatch{id: id}
	var finishedAt time.Time
	err := tx.QueryRowContext(ctx, `
UPDATE automation_dispatch_runs
SET state = $2, target_count = $3, enqueued_count = $4, merged_count = $5,
    skipped_count = $6, dispatch_error_count = $7, completed_count = $8, upstream_outcomes = $9,
    finished_at = GREATEST(CURRENT_TIMESTAMP, started_at),
    retention_until = GREATEST(CURRENT_TIMESTAMP, started_at) + INTERVAL '30 days',
    updated_at = GREATEST(CURRENT_TIMESTAMP, started_at)
WHERE id = $1 AND state = 'running'
RETURNING kind, trigger, finished_at`, id, result.state, counts.targetCount, counts.enqueued,
		counts.merged, counts.skipped, counts.errors, completed, upstream).Scan(&run.kind, &run.trigger, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return finalizedDispatch{}, nil
	}
	if err != nil {
		return finalizedDispatch{}, fmt.Errorf("write dispatch outcome: %w", err)
	}
	if run.trigger == "scheduled" {
		if _, err := tx.ExecContext(ctx, `
UPDATE automation_schedules s
SET next_run_at = CASE WHEN NOT s.enabled THEN NULL WHEN $3 THEN $2::timestamptz
    ELSE $2::timestamptz + CASE s.interval_unit
        WHEN 'minute' THEN s.interval_value * INTERVAL '1 minute'
        WHEN 'hour' THEN s.interval_value * INTERVAL '1 hour'
        ELSE s.interval_value * INTERVAL '1 day'
    END END,
    last_dispatch_at = $2, version = version + 1, updated_at = $2
WHERE s.kind = $1`, run.kind, finishedAt, recovered); err != nil {
			return finalizedDispatch{}, fmt.Errorf("advance dispatch schedule: %w", err)
		}
	}
	if err := d.recordObservability(ctx, txscope.Wrap(tx), run.kind, result); err != nil {
		return finalizedDispatch{}, err
	}
	return run, nil
}

func (d *Dispatcher) recordObservability(ctx context.Context, tx txscope.Tx, kind string, result dispatchResult) error {
	if d.Recorder == nil {
		return nil
	}
	outcome, resultCode := result.routineOutcome()
	counts := result.counts
	summary := "Automation run outcome could not be confirmed."
	var details json.RawMessage
	if result.state != "unconfirmed" {
		summary = fmt.Sprintf("targets=%d completed=%d enqueued=%d merged=%d skipped=%d errors=%d",
			counts.targetCount, counts.completed, counts.enqueued, counts.merged, counts.skipped, counts.errors)
		var err error
		fields := map[string]any{
			"target_count": counts.targetCount, "completed_count": counts.completed,
			"enqueued_count": counts.enqueued, "merged_count": counts.merged,
			"skipped_count": counts.skipped, "error_count": counts.errors,
		}
		if counts.upstream != nil {
			fields["upstream_outcomes"] = counts.upstream
		}
		details, err = json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("encode dispatch routine details: %w", err)
		}
	}
	if err := d.Recorder.UpsertRoutineTx(ctx, tx, observabilityport.RoutineRequest{
		Identity: observabilityport.AutomationDispatchRoutine(outcome, resultCode),
		ScopeID:  kind, Summary: summary, Details: details,
	}); err != nil {
		return fmt.Errorf("write dispatch routine: %w", err)
	}
	if outcome == observabilityport.RoutineOutcomeSuccess {
		return d.Recorder.RecordDiagnosticSuccessTx(ctx, tx, observabilityport.InternalSuccess(observabilityport.InternalAutomationDispatch))
	}
	failure := observabilityport.InternalFailure(observabilityport.InternalAutomationDispatch, "")
	if outcome == observabilityport.RoutineOutcomeWarning {
		failure.Severity = observabilityport.SeverityWarning
	}
	return d.Recorder.RecordDiagnosticFailureTx(ctx, tx, failure)
}

func (d *Dispatcher) publishDispatchResult(ctx context.Context, run finalizedDispatch, result dispatchResult) {
	d.logDispatchResult(ctx, run.id, run.kind, run.trigger, result)
	if d.eventHub != nil {
		d.eventHub.Publish(events.Event{Target: events.TargetRecords})
	}
}

// ReapStaleRuns releases live run slots whose completion can no longer be confirmed.
func (d *Dispatcher) ReapStaleRuns(ctx context.Context, maxRunning time.Duration) error {
	if maxRunning <= 0 {
		return fmt.Errorf("stale run timeout must be positive")
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin stale dispatch reaper: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
SELECT id FROM automation_dispatch_runs
WHERE state = 'running' AND started_at < CURRENT_TIMESTAMP - make_interval(secs => $1)
ORDER BY kind FOR UPDATE`, maxRunning.Seconds())
	if err != nil {
		return fmt.Errorf("list stale dispatch runs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan stale dispatch run: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close stale dispatch runs: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate stale dispatch runs: %w", err)
	}
	result := unconfirmedDispatch()
	settled := make([]finalizedDispatch, 0, len(ids))
	for _, id := range ids {
		run, err := d.finalizeDispatchTx(ctx, tx, id, result, true)
		if err != nil {
			return fmt.Errorf("reap stale dispatch: %w", err)
		}
		if run.id != "" {
			settled = append(settled, run)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit stale dispatch reaper: %w", err)
	}
	for _, run := range settled {
		d.publishDispatchResult(ctx, run, result)
	}
	return nil
}
