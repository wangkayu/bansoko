// Package application implements the automation scheduling and
// dispatch-run lifecycle. Worker dispatch is a separate concern.
package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
)

var allowedAutomationKinds = map[string]bool{
	"group_discovery": true,
	"rss_refresh":     true,
	"metadata_match":  true,
	"catalog_refresh": true,
}

// Service owns the automation schedule rows and dispatch-run lifecycle.
type Service struct {
	db       *sql.DB
	eventHub *events.Hub
}

// NewService returns an automation service bound to the shared pool.
func NewService(db *sql.DB) (*Service, error) {
	if db == nil {
		return nil, errors.New("automation service database is nil")
	}
	return &Service{db: db}, nil
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

// ListSchedules returns every automation schedule row ordered by kind.
func (s *Service) ListSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT kind, enabled, interval_value, interval_unit, next_run_at, last_dispatch_at, version
FROM automation_schedules
ORDER BY kind`)
	if err != nil {
		return nil, fmt.Errorf("read automation schedules: %w", err)
	}
	defer rows.Close()
	result := []Schedule{}
	for rows.Next() {
		var schedule Schedule
		var kind, intervalUnit string
		var nextRunAt, lastDispatchAt sql.NullTime
		if err := rows.Scan(&kind, &schedule.Enabled, &schedule.IntervalValue, &intervalUnit, &nextRunAt, &lastDispatchAt, &schedule.Version); err != nil {
			return nil, fmt.Errorf("scan automation schedule: %w", err)
		}
		schedule.Kind = AutomationKindValue(kind)
		schedule.IntervalUnit = AutomationIntervalUnitValue(intervalUnit)
		if nextRunAt.Valid {
			t := nextRunAt.Time
			schedule.NextRunAt = &t
		}
		if lastDispatchAt.Valid {
			t := lastDispatchAt.Time
			schedule.LastDispatchAt = &t
		}
		result = append(result, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read automation schedules: %w", err)
	}
	outcomes, err := s.latestRunOutcomes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range result {
		if outcome, ok := outcomes[string(result[i].Kind)]; ok {
			outcome := outcome
			result[i].LastRunOutcome = &outcome
		}
	}
	return result, nil
}

// latestRunOutcomes returns the most recent dispatch run per kind, keyed by kind.
func (s *Service) latestRunOutcomes(ctx context.Context) (map[string]RunOutcome, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT ON (kind) kind, state, started_at, finished_at
FROM automation_dispatch_runs
ORDER BY kind, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("read latest automation run outcomes: %w", err)
	}
	defer rows.Close()
	outcomes := map[string]RunOutcome{}
	for rows.Next() {
		var kind, state string
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&kind, &state, &startedAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("scan latest automation run outcome: %w", err)
		}
		outcome := RunOutcome{State: AutomationStateValue(state)}
		if startedAt.Valid {
			t := startedAt.Time
			outcome.StartedAt = &t
		}
		if finishedAt.Valid {
			t := finishedAt.Time
			outcome.FinishedAt = &t
		}
		outcomes[kind] = outcome
	}
	return outcomes, rows.Err()
}

// PatchSchedule replaces one schedule's enabled/interval fields with
// expected-version CAS. next_run_at is recomputed from the new interval when
// enabled, or cleared when disabled.
func (s *Service) PatchSchedule(ctx context.Context, kind string, patch SchedulePatch) (Schedule, error) {
	if !allowedAutomationKinds[kind] {
		return Schedule{}, &apperrors.BadRequest{Code: "AUTOMATION_KIND_INVALID", Message: "Unknown automation kind."}
	}
	if patch.IntervalValue < 1 {
		return Schedule{}, &apperrors.BadRequest{Code: "INTERVAL_INVALID", Message: "interval_value must be at least 1."}
	}
	if err := validateIntervalBounds(kind, patch.IntervalValue, string(patch.IntervalUnit)); err != nil {
		return Schedule{}, err
	}
	if string(patch.IntervalUnit) != "minute" && string(patch.IntervalUnit) != "hour" && string(patch.IntervalUnit) != "day" {
		return Schedule{}, &apperrors.BadRequest{Code: "INTERVAL_UNIT_INVALID", Message: "interval_unit must be minute, hour, or day."}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Schedule{}, fmt.Errorf("begin automation schedule patch: %w", err)
	}
	defer tx.Rollback()

	var version int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM automation_schedules WHERE kind = $1 FOR UPDATE`, kind).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, &apperrors.NotFound{Resource: "automation_schedule", ID: kind}
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("lock automation schedule: %w", err)
	}
	if version != patch.ExpectedVersion {
		return Schedule{}, &apperrors.VersionConflict{Owner: "automation_schedule", CurrentVersion: version}
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE automation_schedules
SET enabled = $2, interval_value = $3::integer, interval_unit = $4::text,
    next_run_at = CASE WHEN $2 THEN CURRENT_TIMESTAMP + CASE $4::text
        WHEN 'minute' THEN $3::integer * INTERVAL '1 minute'
        WHEN 'hour' THEN $3::integer * INTERVAL '1 hour'
        ELSE $3::integer * INTERVAL '1 day'
    END ELSE NULL END,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE kind = $1`,
		kind, patch.Enabled, patch.IntervalValue, string(patch.IntervalUnit)); err != nil {
		return Schedule{}, fmt.Errorf("save automation schedule: %w", err)
	}

	var schedule Schedule
	var intervalUnit string
	var nrAt, ldAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
SELECT kind, enabled, interval_value, interval_unit, next_run_at, last_dispatch_at, version
FROM automation_schedules
WHERE kind = $1`, kind).Scan(&schedule.Kind, &schedule.Enabled, &schedule.IntervalValue, &intervalUnit, &nrAt, &ldAt, &schedule.Version)
	if err != nil {
		return Schedule{}, fmt.Errorf("read automation schedule after patch: %w", err)
	}
	schedule.IntervalUnit = AutomationIntervalUnitValue(intervalUnit)
	if nrAt.Valid {
		t := nrAt.Time
		schedule.NextRunAt = &t
	}
	if ldAt.Valid {
		t := ldAt.Time
		schedule.LastDispatchAt = &t
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, fmt.Errorf("commit automation schedule patch: %w", err)
	}
	outcomes, err := s.latestRunOutcomes(ctx)
	if err != nil {
		return Schedule{}, err
	}
	if outcome, ok := outcomes[kind]; ok {
		outcome := outcome
		schedule.LastRunOutcome = &outcome
	}
	return schedule, nil
}

// RunNow inserts one manual dispatch-run row. The request key makes retries
// return the same run, while a different request still respects the one-live-run
// invariant for each automation kind.
func (s *Service) RunNow(ctx context.Context, kind, idempotencyKey string) (RunAccepted, error) {
	if !allowedAutomationKinds[kind] {
		return RunAccepted{}, &apperrors.BadRequest{Code: "AUTOMATION_KIND_INVALID", Message: "Unknown automation kind."}
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return RunAccepted{}, &apperrors.BadRequest{Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key must be between 8 and 128 characters."}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunAccepted{}, fmt.Errorf("begin automation run: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT kind FROM automation_schedules WHERE kind = $1 FOR UPDATE`, kind); err != nil {
		return RunAccepted{}, fmt.Errorf("lock automation schedule for run: %w", err)
	}

	var existingID, existingState string
	err = tx.QueryRowContext(ctx, `
SELECT id, state
FROM automation_dispatch_runs
WHERE kind = $1 AND request_idempotency_key = $2`, kind, idempotencyKey).Scan(&existingID, &existingState)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return RunAccepted{}, fmt.Errorf("commit automation run replay: %w", err)
		}
		return RunAccepted{Accepted: true, RunID: existingID, State: AutomationStateValue(existingState)}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RunAccepted{}, fmt.Errorf("read automation run idempotency: %w", err)
	}

	runID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, request_idempotency_key)
VALUES ($1, $2, 'manual', 'queued', $3)`, runID, kind, idempotencyKey); err != nil {
		if isUniqueViolation(err) {
			return RunAccepted{}, &apperrors.Business{Status: 409, Code: "LIVE_RUN_EXISTS", Message: "A run is already queued or running for this automation kind."}
		}
		return RunAccepted{}, fmt.Errorf("create automation dispatch run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE automation_schedules SET last_dispatch_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE kind = $1`, kind); err != nil {
		return RunAccepted{}, fmt.Errorf("update last_dispatch_at: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RunAccepted{}, fmt.Errorf("commit automation run: %w", err)
	}
	if s.eventHub != nil {
		s.eventHub.Publish(events.Event{Target: events.TargetRecords})
	}
	return RunAccepted{Accepted: true, RunID: runID, State: AutomationStateValue("queued")}, nil
}

// GetRun returns one dispatch-run row by ID.
func (s *Service) GetRun(ctx context.Context, runID string) (DispatchRun, error) {
	return s.scanDispatchRun(ctx, s.db.QueryRowContext(ctx, dispatchRunSelect+` WHERE id = $1`, runID))
}

const dispatchRunSelect = `SELECT id, kind, trigger, state, target_count, enqueued_count, merged_count, skipped_count, dispatch_error_count, started_at, finished_at, retention_until FROM automation_dispatch_runs`

func (s *Service) scanDispatchRun(ctx context.Context, row *sql.Row) (DispatchRun, error) {
	var run DispatchRun
	var kind, trigger, state string
	var startedAt, finishedAt sql.NullTime
	if err := row.Scan(&run.ID, &kind, &trigger, &state, &run.TargetCount, &run.EnqueuedCount, &run.MergedCount, &run.SkippedCount, &run.DispatchErrorCount, &startedAt, &finishedAt, &run.RetentionUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DispatchRun{}, &apperrors.NotFound{Resource: "automation_dispatch_run", ID: ""}
		}
		return DispatchRun{}, fmt.Errorf("read dispatch run: %w", err)
	}
	run.Kind = AutomationKindValue(kind)
	run.Trigger = AutomationTriggerValue(trigger)
	run.State = AutomationStateValue(state)
	if startedAt.Valid {
		t := startedAt.Time
		run.StartedAt = &t
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		run.FinishedAt = &t
	}
	return run, nil
}

func intervalDuration(value int, unit string) time.Duration {
	switch unit {
	case "minute":
		return time.Duration(value) * time.Minute
	case "hour":
		return time.Duration(value) * time.Hour
	case "day":
		return time.Duration(value) * 24 * time.Hour
	default:
		return 0
	}
}

// validateIntervalBounds mirrors the DB CHECK constraint so invalid patches are
// rejected before the write.
func validateIntervalBounds(kind string, value int, unit string) error {
	d := intervalDuration(value, unit)
	if kind == "rss_refresh" {
		if d < 5*time.Minute || d > 7*24*time.Hour {
			return &apperrors.BadRequest{Code: "INTERVAL_INVALID", Message: "rss_refresh interval must be between 5 minutes and 7 days."}
		}
		return nil
	}
	if d < time.Hour || d > 7*24*time.Hour {
		return &apperrors.BadRequest{Code: "INTERVAL_INVALID", Message: "automation interval must be between 1 hour and 7 days."}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
