package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	animeapp "bansoko/internal/anime/application"
	catalogapp "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/events"
	"bansoko/internal/metadata/backfill"
	"bansoko/internal/metadata/matcher"
	"bansoko/internal/observability/businesslog"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/redact"
	selectiondomain "bansoko/internal/selection/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
)

// notAiringDiscoveryInterval is the rediscovery cadence of an entry that is
// not currently airing; airing entries follow the group_discovery schedule.
const notAiringDiscoveryInterval = 24 * time.Hour

// Default: the Global subgroups section ships max_rss=3 and every new
// subscription copies it, so parallel group fan-out stays bounded unless the
// user raises it.
const defaultMaxRSS = 3

// Dispatcher executes due automation schedules by creating typed WorkItems.
// It owns the scheduled-dispatch lifecycle; manual RunNow runs are created by
// Service and also picked up here.
type DiscoveryPolicyReader interface {
	ReadDiscoveryPolicy(context.Context, string) (selectiondomain.DiscoveryPolicy, error)
}

// DiscoveryPolicyTxReader reads the canonical policy through an existing
// transaction. Configuration mutations use this port when a newly saved global
// section must be applied before the transaction commits.
type DiscoveryPolicyTxReader interface {
	ReadDiscoveryPolicyInTx(context.Context, txscope.Tx, string) (selectiondomain.DiscoveryPolicy, error)
}

type Dispatcher struct {
	DB              *sql.DB
	WorkItems       *workitems.Store
	GroupReaders    map[string]GroupReader
	PolicyReader    DiscoveryPolicyReader
	Connections     ConnectionSource
	MetadataMatcher *matcher.Matcher
	Logger          observabilityport.RuntimeLogger
	Recorder        observabilityport.Recorder
	ActivityWriter  observabilityport.ActivityWriter
	// CatalogRefresher executes catalog_refresh targets; nil fails that kind.
	CatalogRefresher CatalogRefresher
	eventHub         *events.Hub
	now              func() time.Time
	// reuseUnchangedGroups is set on the per-run copy of a scheduled group
	// discovery; see ReuseUnchangedGroupObservationTx.
	reuseUnchangedGroups bool
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (d *Dispatcher) SetEventHub(hub *events.Hub) {
	if d != nil {
		d.eventHub = hub
	}
}

type dispatchCounts struct {
	targetCount int
	enqueued    int
	merged      int
	skipped     int
	errors      int
}

type discoveryPolicy = selectiondomain.DiscoveryPolicy

func (d *Dispatcher) readDiscoveryPolicy(ctx context.Context, managedAnimeID string) (discoveryPolicy, error) {
	if d.PolicyReader == nil {
		return discoveryPolicy{
			MaxRSS: defaultMaxRSS,
			ProviderDefaults: map[providerdomain.Kind]selectiondomain.ProviderDefaultPolicy{
				providerdomain.ProviderAniBT: {Mode: selectiondomain.Ranked, Score: 50, Version: 1},
				providerdomain.ProviderMikan: {Mode: selectiondomain.Ranked, Score: 50, Version: 1},
			},
		}, nil
	}
	return d.PolicyReader.ReadDiscoveryPolicy(ctx, managedAnimeID)
}

// Discovery decisions are derived evidence, never user-authored intent.
// Existing manual rows are still preserved by the upsert below.
func providerSelectionStorage(_ selectiondomain.GroupSelectionDecision) (string, *bool) {
	return "auto", nil
}

type selectionQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func upsertProviderGroupSelection(ctx context.Context, q selectionQueryRower, subscriptionID, providerName, groupRef, displayName string, decision selectiondomain.GroupSelectionDecision) (bool, error) {
	selectionMode, manualSelected := providerSelectionStorage(decision)
	effectiveSelected := decision.Outcome == selectiondomain.GroupSelected
	selectionID := uuid.NewString()
	if err := q.QueryRowContext(ctx, `
INSERT INTO provider_group_selections(
    id, subscription_id, provider, group_ref, display_name,
    selection_mode, manual_selected, effective_selected, effective_score
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (subscription_id, provider, group_ref) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    selection_mode = CASE
        WHEN provider_group_selections.selection_mode = 'manual' THEN provider_group_selections.selection_mode
        ELSE EXCLUDED.selection_mode
    END,
    manual_selected = CASE
        WHEN provider_group_selections.selection_mode = 'manual' THEN provider_group_selections.manual_selected
        ELSE EXCLUDED.manual_selected
    END,
    effective_selected = CASE
        WHEN provider_group_selections.selection_mode = 'manual' THEN provider_group_selections.effective_selected
        ELSE EXCLUDED.effective_selected
    END,
    effective_score = EXCLUDED.effective_score,
    version = provider_group_selections.version + 1,
    updated_at = NOW()
RETURNING effective_selected`,
		selectionID, subscriptionID, providerName, groupRef, displayName,
		selectionMode, manualSelected, effectiveSelected, decision.Group.Score).Scan(&effectiveSelected); err != nil {
		return false, err
	}
	return effectiveSelected, nil
}

// DispatchDue scans for due automation schedules and executes them.
// Returns the number of schedule kinds dispatched.
func (d *Dispatcher) DispatchDue(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin due schedule selection: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
SELECT kind, interval_value, interval_unit
FROM automation_schedules
WHERE enabled AND next_run_at IS NOT NULL AND next_run_at <= $1
ORDER BY next_run_at`, now)
	if err != nil {
		return 0, fmt.Errorf("query due schedules: %w", err)
	}
	var due []struct {
		kind          string
		intervalValue int
		intervalUnit  string
	}
	for rows.Next() {
		var item struct {
			kind          string
			intervalValue int
			intervalUnit  string
		}
		if err := rows.Scan(&item.kind, &item.intervalValue, &item.intervalUnit); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan due schedule: %w", err)
		}
		due = append(due, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate due schedules: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit due schedule selection: %w", err)
	}

	for _, item := range due {
		if err := d.dispatchOne(ctx, item.kind, item.intervalValue, item.intervalUnit, now); err != nil {
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message:    "automation schedule dispatch failed",
				Operation:  "dispatch",
				ReasonCode: "dispatch_failed",
				OwnerType:  "automation_schedule",
				OwnerID:    item.kind,
				ErrorClass: "dispatch",
			})
		}
	}
	return len(due), nil
}

// DispatchManual picks up a queued manual run and executes it.
func (d *Dispatcher) DispatchManual(ctx context.Context) (int, error) {
	rows, err := d.DB.QueryContext(ctx, `
SELECT id, kind FROM automation_dispatch_runs
WHERE trigger = 'manual' AND state = 'queued'
ORDER BY created_at`)
	if err != nil {
		return 0, fmt.Errorf("query queued manual runs: %w", err)
	}
	var queued []struct {
		id   string
		kind string
	}
	for rows.Next() {
		var item struct {
			id   string
			kind string
		}
		if err := rows.Scan(&item.id, &item.kind); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan queued manual run: %w", err)
		}
		queued = append(queued, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate queued manual runs: %w", err)
	}

	for _, item := range queued {
		if err := d.dispatchManualRun(ctx, item.id, item.kind); err != nil {
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message:    "manual automation run dispatch failed",
				Operation:  "dispatch_manual",
				ReasonCode: "manual_dispatch_failed",
				OwnerType:  "automation_dispatch_run",
				OwnerID:    item.id,
				ErrorClass: "dispatch",
			})
		}
	}
	return len(queued), nil
}

func (d *Dispatcher) dispatchOne(ctx context.Context, kind string, intervalValue int, intervalUnit string, now time.Time) (errRet error) {
	_ = intervalValue
	_ = intervalUnit
	runID := uuid.NewString()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schedule dispatch tx: %w", err)
	}
	defer tx.Rollback()

	// Lock the schedule, then claim only while it is still due and no live run
	// exists. The schedule remains due until completion so a crash cannot erase
	// the next cycle before the stale-run reaper can recover it.
	var due bool
	if err := tx.QueryRowContext(ctx, `
SELECT enabled AND next_run_at IS NOT NULL AND next_run_at <= $2
FROM automation_schedules
WHERE kind = $1
FOR UPDATE`, kind, now).Scan(&due); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("lock schedule: %w", err)
	}
	if !due {
		return nil
	}
	var liveRunID string
	if err := tx.QueryRowContext(ctx, `
SELECT id FROM automation_dispatch_runs
WHERE kind = $1 AND state IN ('queued', 'running')
LIMIT 1`, kind).Scan(&liveRunID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check live scheduled run: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, started_at, retention_until, created_at, updated_at)
VALUES ($1, $2, 'scheduled', 'running', $3, $3::timestamptz + INTERVAL '30 days', $3, $3)`, runID, kind, now); err != nil {
		return fmt.Errorf("create dispatch run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schedule dispatch: %w", err)
	}

	// Ensure the dispatch run always reaches a terminal state, even if
	// runDispatch panics. Without this, a stuck 'running' row permanently
	// blocks all future dispatches of the same kind (unique constraint).
	defer func() {
		if r := recover(); r != nil {
			d.finishRun(ctx, runID, kind, dispatchCounts{errors: 1}, fmt.Errorf("panic: %v", r), now)
			errRet = fmt.Errorf("dispatch %s panic: %v", kind, r)
		}
	}()

	counts, dispatchErr := d.runDispatch(ctx, kind, true)
	d.finishRun(ctx, runID, kind, counts, dispatchErr, now)
	return dispatchErr
}

func (d *Dispatcher) dispatchManualRun(ctx context.Context, runID, kind string) (errRet error) {
	now := time.Now().UTC()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin manual dispatch tx: %w", err)
	}
	defer tx.Rollback()

	// Do not re-anchor retention_until here: the queued row carries
	// created_at + 30 days, and check3 bounds retention to
	// COALESCE(finished_at, created_at) + 30 days, so recomputing from a
	// later claim time would violate it. finishRun re-anchors retention to
	// finished_at instead.
	result, err := tx.ExecContext(ctx, `
UPDATE automation_dispatch_runs
SET state = 'running', started_at = $2, updated_at = $2
WHERE id = $1 AND state = 'queued'`, runID, now)
	if err != nil {
		return fmt.Errorf("claim manual run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read manual run claim result: %w", err)
	}
	if rows == 0 {
		return nil // Already claimed by another worker.
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit manual dispatch claim: %w", err)
	}

	// Ensure the dispatch run always reaches a terminal state, even if
	// runDispatch panics. Without this, a stuck 'running' row permanently
	// blocks all future dispatches of the same kind (unique constraint).
	defer func() {
		if r := recover(); r != nil {
			d.finishRun(ctx, runID, kind, dispatchCounts{errors: 1}, fmt.Errorf("panic: %v", r), now)
			errRet = fmt.Errorf("dispatch %s panic: %v", kind, r)
		}
	}()

	counts, dispatchErr := d.runDispatch(ctx, kind, false)
	d.finishRun(ctx, runID, kind, counts, dispatchErr, now)
	return dispatchErr
}

// runDispatch executes one kind. scheduled distinguishes the timer from a
// manual RunNow: only scheduled group discovery applies the airing cadence and
// the unchanged-observation shortcut; a manual run rediscovers everything.
func (d *Dispatcher) runDispatch(ctx context.Context, kind string, scheduled bool) (dispatchCounts, error) {
	switch kind {
	case "group_discovery":
		return d.dispatchGroupDiscoveryRun(ctx, scheduled)
	case "rss_refresh":
		return d.dispatchRSSRefresh(ctx)
	case "metadata_match":
		return d.dispatchMetadataMatch(ctx)
	case "catalog_refresh":
		return d.dispatchCatalogRefresh(ctx)
	default:
		return dispatchCounts{}, fmt.Errorf("unknown automation kind %q", kind)
	}
}

// finishRun records the terminal state at the actual completion time:
// startedAt is when the run was claimed, and a scheduled run's next_run_at is
// computed from completion so a slow run does not shorten the next interval.
func (d *Dispatcher) finishRun(ctx context.Context, runID, kind string, counts dispatchCounts, dispatchErr error, startedAt time.Time) {
	now := time.Now().UTC()
	if now.Before(startedAt) {
		now = startedAt
	}
	// Determine terminal state. A dispatch that returned an error is failed.
	// A dispatch that completed with per-target errors but some successes is
	// partial. Only error-free runs with no dispatch error are completed.
	// This must satisfy the CHECK constraints on automation_dispatch_runs:
	//   state <> 'completed'  OR dispatch_error_count = 0
	//   state <> 'partial'    OR (dispatch_error_count > 0 AND (enqueued_count + merged_count) > 0)
	//   state <> 'failed'     OR (dispatch_error_count > 0 AND (enqueued_count + merged_count) = 0)
	state := "completed"
	if dispatchErr != nil {
		state = "failed"
	} else if counts.errors > 0 {
		if counts.enqueued+counts.merged > 0 {
			state = "partial"
		} else {
			state = "failed"
		}
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		d.logError(ctx, observabilityport.RuntimeEvent{
			Message:    "automation dispatch run finish failed",
			Operation:  "finish_run",
			ReasonCode: "finish_run_begin_failed",
			OwnerType:  "automation_dispatch_run",
			OwnerID:    runID,
			ErrorClass: "finish_run",
		})
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE automation_dispatch_runs
SET state = $2, target_count = $3, enqueued_count = $4, merged_count = $5,
    skipped_count = $6, dispatch_error_count = $7,
    finished_at = $8, retention_until = $8::timestamptz + INTERVAL '30 days', updated_at = $8
WHERE id = $1`,
		runID, state, counts.targetCount, counts.enqueued, counts.merged, counts.skipped, counts.errors, now); err != nil {
		d.logError(ctx, observabilityport.RuntimeEvent{
			Message:    "automation dispatch run finish update failed",
			Operation:  "finish_run",
			ReasonCode: "finish_run_update_failed",
			OwnerType:  "automation_dispatch_run",
			OwnerID:    runID,
			ErrorClass: "finish_run",
		})
		return
	}
	// Scheduled runs advance from completion time. Manual runs do not alter the
	// schedule because they are explicit user actions.
	if kind != "" {
		if _, err := tx.ExecContext(ctx, `
UPDATE automation_schedules s
SET next_run_at = CASE WHEN s.enabled THEN $2::timestamptz + CASE s.interval_unit
        WHEN 'minute' THEN s.interval_value * INTERVAL '1 minute'
        WHEN 'hour' THEN s.interval_value * INTERVAL '1 hour'
        ELSE s.interval_value * INTERVAL '1 day'
    END ELSE NULL END,
    last_dispatch_at = $2, version = version + 1, updated_at = $2
WHERE s.kind = $1
  AND EXISTS (SELECT 1 FROM automation_dispatch_runs r WHERE r.id = $3 AND r.trigger = 'scheduled')`, kind, now, runID); err != nil {
			d.logError(ctx, observabilityport.RuntimeEvent{
				Message:    "automation dispatch run schedule update failed",
				Operation:  "finish_run",
				ReasonCode: "finish_run_schedule_failed",
				OwnerType:  "automation_dispatch_run",
				OwnerID:    runID,
				ErrorClass: "finish_run",
			})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		d.logError(ctx, observabilityport.RuntimeEvent{
			Message:    "automation dispatch run finish commit failed",
			Operation:  "finish_run",
			ReasonCode: "finish_run_commit_failed",
			OwnerType:  "automation_dispatch_run",
			OwnerID:    runID,
			ErrorClass: "finish_run",
		})
		return
	}
	d.recordObservability(ctx, kind, state, counts, dispatchErr)
	if d.eventHub != nil {
		d.eventHub.Publish(events.Event{Target: events.TargetRecords})
	}
}

func (d *Dispatcher) recordObservability(ctx context.Context, kind, state string, counts dispatchCounts, dispatchErr error) {
	if d.Recorder == nil {
		return
	}
	outcome, resultCode := dispatchRoutineOutcome(state, counts, dispatchErr)
	scopeID := kind
	summary := fmt.Sprintf("targets=%d enqueued=%d merged=%d skipped=%d errors=%d",
		counts.targetCount, counts.enqueued, counts.merged, counts.skipped, counts.errors)
	// Structured counts let the settings surface render a localized summary;
	// the English key=value summary stays as the machine-stable fallback.
	details, err := json.Marshal(map[string]int{
		"target_count":   counts.targetCount,
		"enqueued_count": counts.enqueued,
		"merged_count":   counts.merged,
		"skipped_count":  counts.skipped,
		"error_count":    counts.errors,
	})
	if err != nil {
		d.logError(ctx, observabilityport.RuntimeEvent{
			Message:    "automation routine details encoding failed",
			Operation:  "observability",
			ReasonCode: "routine_details_encode_failed",
			ErrorClass: "observability",
		})
	}
	routineIdentity := observabilityport.AutomationDispatchRoutine(observabilityport.RoutineOutcome(outcome), observabilityport.RoutineResultCode(resultCode))
	if err := d.Recorder.UpsertRoutine(ctx, observabilityport.RoutineRequest{
		Identity: routineIdentity, ScopeID: scopeID, Summary: summary, Details: details,
	}); err != nil {
		d.logError(ctx, observabilityport.RuntimeEvent{Message: "automation routine write failed", Operation: "observability", ReasonCode: "routine_write_failed", ErrorClass: "observability"})
	}
	if dispatchErr != nil || counts.errors > 0 {
		failure := observabilityport.InternalFailure(observabilityport.InternalAutomationDispatch, "")
		if dispatchErr == nil {
			failure.Severity = observabilityport.SeverityWarning
		}
		if err := d.Recorder.RecordDiagnosticFailure(ctx, failure); err != nil {
			d.logError(ctx, observabilityport.RuntimeEvent{Message: "automation diagnostic write failed", Operation: "observability", ReasonCode: "diagnostic_write_failed", ErrorClass: "observability"})
		}
		return
	}
	if err := d.Recorder.RecordDiagnosticSuccess(ctx, observabilityport.InternalSuccess(observabilityport.InternalAutomationDispatch)); err != nil {
		d.logError(ctx, observabilityport.RuntimeEvent{Message: "automation recovery write failed", Operation: "observability", ReasonCode: "recovery_write_failed", ErrorClass: "observability"})
	}
}

func dispatchRoutineOutcome(state string, counts dispatchCounts, dispatchErr error) (string, string) {
	if dispatchErr != nil {
		return "failure", "dispatch_failed"
	}
	if counts.errors > 0 {
		if counts.enqueued+counts.merged > 0 {
			return "warning", "dispatch_partial"
		}
		return "failure", "dispatch_failed"
	}
	return "success", "dispatch_completed"
}

func safeDispatchError(err error) string {
	if err == nil {
		return ""
	}
	message, matched := redact.Apply(err.Error(), 4096, redact.Mask, "dispatch failed with a redacted error")
	if matched {
		return message
	}
	return message
}

// ReapStaleRuns marks dispatch runs stuck in 'running' past a timeout as
// failed. This prevents a crashed/panicked dispatch (from before the
// defer+recover fix) from permanently blocking future runs of the same kind.
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
SELECT id, kind, trigger
FROM automation_dispatch_runs
WHERE state = 'running' AND started_at < CURRENT_TIMESTAMP - make_interval(secs => $1)
FOR UPDATE`, maxRunning.Seconds())
	if err != nil {
		return fmt.Errorf("list stale dispatch runs: %w", err)
	}
	type staleRun struct{ id, kind, trigger string }
	var stale []staleRun
	for rows.Next() {
		var run staleRun
		if err := rows.Scan(&run.id, &run.kind, &run.trigger); err != nil {
			rows.Close()
			return fmt.Errorf("scan stale dispatch run: %w", err)
		}
		stale = append(stale, run)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate stale dispatch runs: %w", err)
	}
	rows.Close()
	for _, run := range stale {
		if _, err := tx.ExecContext(ctx, `
UPDATE automation_dispatch_runs
SET state = 'failed', finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP,
    dispatch_error_count = GREATEST(dispatch_error_count, 1)
WHERE id = $1 AND state = 'running'`, run.id); err != nil {
			return fmt.Errorf("fail stale dispatch run %s: %w", run.id, err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE automation_schedules s
SET next_run_at = CASE WHEN s.enabled THEN CURRENT_TIMESTAMP ELSE NULL END,
    last_dispatch_at = CURRENT_TIMESTAMP, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE s.kind = $1 AND $2 = 'scheduled'`, run.kind, run.trigger); err != nil {
			return fmt.Errorf("recover stale schedule %s: %w", run.kind, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit stale dispatch reaper: %w", err)
	}
	if len(stale) > 0 && d.eventHub != nil {
		d.eventHub.Publish(events.Event{Target: events.TargetRecords})
	}
	if len(stale) > 0 {
		d.logInfo(ctx, observabilityport.RuntimeEvent{
			Message:    fmt.Sprintf("reaped %d stale automation run(s)", len(stale)),
			Operation:  "reap",
			Outcome:    "success",
			ReasonCode: "stale_runs_reaped",
		})
	}
	return nil
}

// dispatchGroupDiscovery finds active provider subscriptions, discovers their
// current groups, merges observed groups into the anime subgroups section, then
// reapplies selection policy and creates missing sources and refresh work items
// for selected groups.
func (d *Dispatcher) dispatchGroupDiscovery(ctx context.Context) (dispatchCounts, error) {
	return d.dispatchGroupDiscoveryRun(ctx, false)
}

func (d *Dispatcher) dispatchGroupDiscoveryRun(ctx context.Context, scheduled bool) (dispatchCounts, error) {
	var counts dispatchCounts

	// Cadence: the schedule ticks at the airing cadence (default 6 hours).
	// An entry that is not airing (absent from the latest successful catalog
	// snapshot of the current and previous season) is rediscovered only when
	// its last successful discovery is older than notAiringDiscoveryInterval.
	now := time.Now()
	if d.now != nil {
		now = d.now()
	}
	rows, err := d.DB.QueryContext(ctx, `
SELECT s.id, s.managed_anime_id, s.origin, s.origin_entry_ref, m.title,
       EXISTS (
           SELECT 1
           FROM provider_catalog_snapshots cs
           JOIN provider_catalog_entries e ON e.snapshot_id = cs.id
           WHERE cs.provider = s.origin AND cs.season = ANY($1::text[])
             AND cs.status IN ('fresh', 'withdrawn', 'zero_result')
             AND cs.version = (
                 SELECT MAX(latest.version) FROM provider_catalog_snapshots latest
                 WHERE latest.provider = cs.provider AND latest.season = cs.season
                   AND latest.status IN ('fresh', 'withdrawn', 'zero_result'))
             AND e.provider_entry_ref = s.origin_entry_ref AND e.state = 'present'),
       COALESCE(h.last_success_at > CURRENT_TIMESTAMP - make_interval(secs => $2), FALSE)
FROM subscriptions s
JOIN managed_anime m ON m.id = s.managed_anime_id
LEFT JOIN provider_group_refresh_health h
  ON h.provider = s.origin AND h.catalog_entry_ref = s.origin_entry_ref
WHERE s.state = 'active'
  AND s.origin IN ('anibt','mikan')
  AND s.origin_entry_ref IS NOT NULL`,
		[]string{catalogdomain.SeasonAt(now), catalogdomain.SeasonBefore(now)}, notAiringDiscoveryInterval.Seconds())
	if err != nil {
		return counts, fmt.Errorf("query subscriptions needing discovery: %w", err)
	}
	defer rows.Close()

	type subTarget struct {
		subscriptionID  string
		managedAnimeID  string
		provider        string
		catalogEntryRef string
		animeTitle      string
		airing          bool
		recentlyFound   bool
	}
	var targets []subTarget
	for rows.Next() {
		var t subTarget
		if err := rows.Scan(&t.subscriptionID, &t.managedAnimeID, &t.provider, &t.catalogEntryRef, &t.animeTitle, &t.airing, &t.recentlyFound); err != nil {
			return counts, fmt.Errorf("scan subscription: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return counts, fmt.Errorf("iterate subscriptions: %w", err)
	}

	counts.targetCount = len(targets)

	// Several subscriptions may share one provider entry: read its groups once
	// per dispatch run.
	run := *d
	run.GroupReaders = memoizeGroupReaders(d.GroupReaders)
	run.reuseUnchangedGroups = scheduled
	for _, target := range targets {
		if scheduled && !target.airing && target.recentlyFound {
			counts.skipped++
			continue
		}
		policy, err := d.readDiscoveryPolicy(ctx, target.managedAnimeID)
		if err != nil {
			counts.errors++
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message:    "group discovery policy read failed",
				Operation:  "group_discovery",
				ReasonCode: "policy_read_failed",
				OwnerType:  "subscription",
				OwnerID:    target.subscriptionID,
				ErrorClass: "group_discovery",
			})
			continue
		}
		c, err := run.discoverOne(ctx, target.managedAnimeID, target.subscriptionID, target.provider, target.catalogEntryRef, target.animeTitle, policy)
		if err != nil {
			counts.errors++
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message:    "group discovery failed",
				Operation:  "group_discovery",
				ReasonCode: "discovery_failed",
				OwnerType:  "subscription",
				OwnerID:    target.subscriptionID,
				ErrorClass: "group_discovery",
			})
			continue
		}
		counts.enqueued += c.enqueued
		counts.merged += c.merged
		counts.skipped += c.skipped
	}
	return counts, nil
}

// DiscoverGroups runs per-anime group discovery synchronously for the
// AnimeService one-click action. It reads subscription metadata, resolves the
// discovery policy, and delegates to the same discoverOne used by scheduled
// dispatch.
func (d *Dispatcher) DiscoverGroups(ctx context.Context, managedAnimeID string) (animeapp.DiscoverResult, error) {
	var subscriptionID, provider, catalogEntryRef, animeTitle string
	err := d.DB.QueryRowContext(ctx, `
SELECT s.id, s.origin, s.origin_entry_ref, m.title
FROM subscriptions s
JOIN managed_anime m ON m.id = s.managed_anime_id
WHERE s.managed_anime_id = $1`, managedAnimeID).Scan(
		&subscriptionID, &provider, &catalogEntryRef, &animeTitle)
	if err != nil {
		return animeapp.DiscoverResult{}, fmt.Errorf("read subscription for discovery: %w", err)
	}

	policy, err := d.readDiscoveryPolicy(ctx, managedAnimeID)
	if err != nil {
		return animeapp.DiscoverResult{Outcome: "config_error", Reason: "Failed to read discovery policy."}, nil
	}

	counts, err := d.discoverOneMode(ctx, managedAnimeID, subscriptionID, provider, catalogEntryRef, animeTitle, policy, true)
	if err != nil {
		return animeapp.DiscoverResult{Outcome: "provider_error", Reason: truncateReason(err.Error())}, nil
	}

	discoveredCount := counts.merged + counts.enqueued + counts.skipped
	if counts.errors > 0 {
		return animeapp.DiscoverResult{
			Outcome:         "provider_error",
			Reason:          "Group sources could not be created.",
			DiscoveredCount: discoveredCount,
		}, nil
	}
	if discoveredCount == 0 {
		return animeapp.DiscoverResult{Outcome: "no_groups", Reason: "No groups were found."}, nil
	}
	return animeapp.DiscoverResult{
		Outcome:         "discovered",
		Reason:          fmt.Sprintf("Discovered %d group(s).", discoveredCount),
		DiscoveredCount: discoveredCount,
	}, nil
}

func truncateReason(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > 512 {
		trimmed = trimmed[:512]
	}
	return trimmed
}

func (d *Dispatcher) discoverOne(ctx context.Context, managedAnimeID, subscriptionID, providerName, catalogEntryRef, animeTitle string, policy discoveryPolicy) (dispatchCounts, error) {
	return d.discoverOneMode(ctx, managedAnimeID, subscriptionID, providerName, catalogEntryRef, animeTitle, policy, false)
}

func (d *Dispatcher) discoverOneMode(ctx context.Context, managedAnimeID, subscriptionID, providerName, catalogEntryRef, animeTitle string, policy discoveryPolicy, authoritative bool) (dispatchCounts, error) {
	var counts dispatchCounts

	reader, ok := d.GroupReaders[providerName]
	if !ok {
		return counts, fmt.Errorf("no group reader for provider %q", providerName)
	}

	fence, err := readDiscoveryFence(ctx, d.DB, subscriptionID, providerName)
	if err != nil {
		return counts, err
	}
	result, err := reader.ReadGroups(ctx, catalogEntryRef)
	if err != nil {
		if healthErr := d.recordGroupRefreshFailure(ctx, providerName, catalogEntryRef, err); healthErr != nil {
			return counts, fmt.Errorf("read groups: %w (record health: %v)", err, healthErr)
		}
		return counts, fmt.Errorf("read groups: %w", err)
	}
	// Reject mismatched or duplicate provider identities before any selection
	// writes; silently skipping invalid refs would publish a partial scope.
	if err := validateDiscoveryIdentity(result, providerName, catalogEntryRef); err != nil {
		return counts, err
	}
	if authoritative && (!result.Complete || result.CompletenessEvidence == "") {
		return counts, errors.New("forced group refresh response is incomplete")
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return counts, err
	}
	defer tx.Rollback()
	if err := verifyDiscoveryFence(ctx, tx, subscriptionID, providerName, fence); err != nil {
		return counts, err
	}
	if reader, ok := d.PolicyReader.(DiscoveryPolicyTxReader); ok {
		policy, err = reader.ReadDiscoveryPolicyInTx(ctx, txscope.Wrap(tx), managedAnimeID)
		if err != nil {
			return counts, err
		}
	}
	// Scheduled discovery does not seal a new observation when nothing but the
	// observation time changed; a forced (user) refresh always publishes.
	var snapshot providerdomain.GroupSnapshot
	unchanged := false
	if !authoritative && d.reuseUnchangedGroups {
		snapshot, unchanged, err = catalogapp.ReuseUnchangedGroupObservationTx(ctx, tx, result)
		if err != nil {
			return counts, fmt.Errorf("compare group snapshot: %w", err)
		}
	}
	if !unchanged {
		snapshot, err = d.persistGroupSnapshotTx(ctx, tx, result)
		if err != nil {
			return counts, fmt.Errorf("persist group snapshot: %w", err)
		}
	}

	providerKind := providerdomain.Kind(providerName)

	decisions, err := rankDiscoveryScopeTx(ctx, tx, subscriptionID, providerName, result, policy, authoritative)
	if err != nil {
		return counts, err
	}

	if err := d.applyDiscoveryScopeTx(ctx, tx, subscriptionID, providerName, result, decisions, authoritative); err != nil {
		return counts, err
	}
	snapshotVersion := fmt.Sprintf("v%d", snapshot.Version)
	if result.SourceVersion != "" {
		snapshotVersion += ":" + result.SourceVersion
	}

	for _, decision := range decisions {
		observed := false
		for _, g := range result.Groups {
			if g.Ref == decision.Group.Ref {
				observed = true
				break
			}
		}
		if !observed {
			continue
		} // retained source/history must not be relabeled or recreated
		// Provider is stored separately; group_ref remains the provider-local
		// slug/ID so source provenance and UI policy targets use one shape.
		groupRefKey := decision.Group.Ref.ExternalID
		displayName := decision.Group.Ref.ExternalID
		// Try to find the display name from the observed groups.
		for _, g := range result.Groups {
			if g.Ref.ExternalID == groupRefKey {
				displayName = g.DisplayName
				break
			}
		}

		// Build a readable source fact for every observed group. Only an
		// effectively selected source receives a refresh WorkItem.
		rssURL, err := providerdomain.BuildProviderRSSURL(providerKind, readerEndpoint(ctx, d, providerKind), catalogEntryRef, decision.Group.Ref.ExternalID)
		if err != nil {
			return dispatchCounts{}, fmt.Errorf("prepare group source URL: %w", err)
		}

		workCreated, err := d.createProviderGroupSourceTx(ctx, tx, subscriptionID, providerName, catalogEntryRef, groupRefKey, decision.Group.Ref.ExternalID, displayName, animeTitle, rssURL, snapshotVersion)
		if err != nil {
			return dispatchCounts{}, fmt.Errorf("provision group source: %w", err)
		}
		if workCreated {
			counts.enqueued++
		} else {
			counts.skipped++
		}
	}

	if err := tx.Commit(); err != nil {
		return dispatchCounts{}, err
	}
	if d.eventHub != nil {
		d.eventHub.Publish(events.Event{Target: events.TargetRSS})
	}
	return counts, nil
}

// createProviderGroupSource upserts one readable provider source and its runtime.
// A refresh WorkItem is created only when the current selection is effective.
func (d *Dispatcher) createProviderGroupSource(ctx context.Context, subscriptionID, providerName, catalogEntryRef, groupRefKey, groupExternalID, displayName, animeTitle, rssURL, snapshotVersion string) (bool, error) {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	created, err := d.createProviderGroupSourceTx(ctx, tx, subscriptionID, providerName, catalogEntryRef, groupRefKey, groupExternalID, displayName, animeTitle, rssURL, snapshotVersion)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if d.eventHub != nil {
		d.eventHub.Publish(events.Event{Target: events.TargetRSS})
	}
	return created, nil
}

func (d *Dispatcher) createProviderGroupSourceTx(ctx context.Context, tx *sql.Tx, subscriptionID, providerName, catalogEntryRef, groupRefKey, groupExternalID, displayName, animeTitle, rssURL, snapshotVersion string) (bool, error) {
	providerKind := providerdomain.Kind(providerName)
	groupRef, err := providerdomain.NewGroupRef(providerKind, groupExternalID)
	if err != nil {
		return false, fmt.Errorf("validate provider group ref: %w", err)
	}
	if groupRefKey != groupRef.ExternalID {
		return false, errors.New("provider group ref key does not match normalized group ref")
	}
	if err := (providerdomain.FeedLocator{
		Provider:        providerKind,
		CatalogEntryRef: catalogEntryRef,
		Group:           groupRef,
	}).Validate(); err != nil {
		return false, fmt.Errorf("validate provider feed locator: %w", err)
	}

	sourceID := uuid.NewString()
	upsertErr := tx.QueryRowContext(ctx, `
INSERT INTO rss_sources(
    id, subscription_id, source_kind, locator, locator_display, group_name, title,
    provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version
)
VALUES ($1, $2, 'provider_group', $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (subscription_id, provider, provider_entry_ref, provider_group_ref)
    WHERE source_kind = 'provider_group' DO UPDATE SET
    locator = EXCLUDED.locator,
    locator_display = EXCLUDED.locator_display,
    group_name = EXCLUDED.group_name,
    title = EXCLUDED.title,
    provider = EXCLUDED.provider,
    provider_entry_ref = EXCLUDED.provider_entry_ref,
    provider_group_ref = EXCLUDED.provider_group_ref,
    discovery_snapshot_version = EXCLUDED.discovery_snapshot_version,
    provenance = EXCLUDED.provenance,
    version = rss_sources.version + 1,
    updated_at = NOW()
WHERE
    (
        substring(rss_sources.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') IS NULL
        AND substring(EXCLUDED.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') IS NOT NULL
    )
    OR (
        substring(rss_sources.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') IS NOT NULL
        AND substring(EXCLUDED.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') IS NOT NULL
        AND CAST(substring(EXCLUDED.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') AS NUMERIC)
            >= CAST(substring(rss_sources.discovery_snapshot_version FROM '^v([0-9]+)(:.*)?$') AS NUMERIC)
    )
RETURNING id`,
		sourceID, subscriptionID, rssURL, rssURL, displayName, animeTitle,
		providerName, catalogEntryRef, groupRef.ExternalID, snapshotVersion).Scan(&sourceID)
	if upsertErr != nil && !errors.Is(upsertErr, sql.ErrNoRows) {
		return false, fmt.Errorf("upsert rss source: %w", upsertErr)
	}
	if errors.Is(upsertErr, sql.ErrNoRows) {
		if err := tx.QueryRowContext(ctx, `
SELECT id
FROM rss_sources
WHERE subscription_id = $1 AND provider = $2 AND provider_entry_ref = $3
  AND provider_group_ref = $4 AND source_kind = 'provider_group'`,
			subscriptionID, providerName, catalogEntryRef, groupRef.ExternalID).Scan(&sourceID); err != nil {
			return false, fmt.Errorf("read existing rss source after stale snapshot: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)
ON CONFLICT (rss_source_id) DO NOTHING`, sourceID); err != nil {
		return false, fmt.Errorf("ensure rss source runtime: %w", err)
	}

	runtimeVersion, ineligibility, err := sourceRefreshEligibilityTx(ctx, tx, sourceID)
	if err != nil {
		return false, err
	}
	workCreated := false
	if ineligibility == subscriptiondomain.SourceRefreshEligible {
		_, err := d.WorkItems.Create(ctx, tx, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID},
			Kind:                 workitems.KindRSSRefresh,
			IdempotencyKey:       "rss_refresh:" + sourceID,
			MaxAttempts:          5,
			ExpectedOwnerVersion: runtimeVersion,
		})
		if err != nil && !errors.Is(err, workitems.ErrLiveItemConflict) {
			return false, err
		}
		workCreated = err == nil
	}

	return workCreated, nil
}

func sourceRefreshEligibilityTx(ctx context.Context, tx *sql.Tx, sourceID string) (int64, subscriptiondomain.SourceRefreshIneligibility, error) {
	var runtimeVersion int64
	var subscriptionID, subscriptionState, sourceKind string
	var provider, providerGroupRef sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT rt.version, src.subscription_id, sub.state, src.source_kind, src.provider, src.provider_group_ref
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN rss_source_runtime rt ON rt.rss_source_id = src.id
WHERE src.id = $1
FOR UPDATE OF src, sub, rt`, sourceID).Scan(
		&runtimeVersion, &subscriptionID, &subscriptionState, &sourceKind, &provider, &providerGroupRef); err != nil {
		return 0, subscriptiondomain.SourceRefreshEligible, fmt.Errorf("read source refresh eligibility: %w", err)
	}
	selectionExists := false
	providerSelected := false
	if sourceKind == "provider_group" {
		err := tx.QueryRowContext(ctx, `
SELECT effective_selected AND available
FROM provider_group_selections
WHERE subscription_id = $1 AND provider = $2 AND group_ref = $3
FOR UPDATE`, subscriptionID, provider.String, providerGroupRef.String).Scan(&providerSelected)
		selectionExists = err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, subscriptiondomain.SourceRefreshEligible, fmt.Errorf("lock provider selection for refresh eligibility: %w", err)
		}
	}
	return runtimeVersion, subscriptiondomain.EvaluateSourceRefreshEligibility(subscriptiondomain.SourceRefreshEligibilityFacts{
		SubscriptionState:       subscriptiondomain.SubscriptionState(subscriptionState),
		ProviderSource:          sourceKind == "provider_group",
		ProviderSelectionExists: selectionExists,
		ProviderSelected:        providerSelected,
	}), nil
}

// dispatchRSSRefresh enqueues a rss_refresh WorkItem for every active
// provider_group or custom_rss source. If a live item already exists for the
// owner, it is merged (skipped).
func (d *Dispatcher) dispatchRSSRefresh(ctx context.Context) (dispatchCounts, error) {
	var counts dispatchCounts

	// Automatic dispatch honours the per-source failure backoff and the
	// provider circuit; manual refreshes create their WorkItems elsewhere and
	// bypass both.
	rows, err := d.DB.QueryContext(ctx, `
SELECT rs.id, rt.version, rt.consecutive_failures,
       rt.consecutive_failures > 0 AND rt.last_refresh_at IS NOT NULL
           AND rt.last_refresh_at + make_interval(secs => LEAST($1::double precision * power(2, rt.consecutive_failures - 1), $2::double precision)) > CURRENT_TIMESTAMP,
       COALESCE(circuit.paused_until > CURRENT_TIMESTAMP, FALSE)
FROM rss_sources rs
JOIN subscriptions s ON s.id = rs.subscription_id
JOIN rss_source_runtime rt ON rt.rss_source_id = rs.id
LEFT JOIN rss_provider_circuits circuit
  ON rs.source_kind = 'provider_group' AND circuit.provider = rs.provider
WHERE s.state = 'active'
  -- Skip placeholder sources whose only releases are manually injected. The
  -- injected probe reads the small releases_injected_source_idx and is false
  -- for almost every source; the second probe stops at its first release.
  AND NOT (
    EXISTS (SELECT 1 FROM releases r WHERE r.rss_source_id = rs.id AND r.reason_code = 'injected')
    AND NOT EXISTS (SELECT 1 FROM releases r WHERE r.rss_source_id = rs.id AND r.reason_code <> 'injected')
  )
  AND (
    rs.source_kind = 'custom_rss'
    OR (
      rs.source_kind = 'provider_group'
      AND EXISTS (
        SELECT 1
        FROM provider_group_selections selection
        WHERE selection.subscription_id = rs.subscription_id
          AND selection.provider = rs.provider
          AND selection.group_ref = rs.provider_group_ref
          AND selection.effective_selected = TRUE
 AND selection.available = TRUE
      )
    )
  )`, subscriptiondomain.RefreshBackoffBase.Seconds(), subscriptiondomain.RefreshBackoffCap.Seconds())
	if err != nil {
		return counts, fmt.Errorf("query active rss sources: %w", err)
	}
	defer rows.Close()

	type sourceTarget struct {
		sourceID       string
		version        int64
		failures       int
		backingOff     bool
		providerPaused bool
	}
	var targets []sourceTarget
	for rows.Next() {
		var t sourceTarget
		if err := rows.Scan(&t.sourceID, &t.version, &t.failures, &t.backingOff, &t.providerPaused); err != nil {
			return counts, fmt.Errorf("scan rss source: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return counts, fmt.Errorf("iterate rss sources: %w", err)
	}

	counts.targetCount = len(targets)

	for _, target := range targets {
		if target.backingOff || target.providerPaused {
			counts.skipped++
			continue
		}
		tx, err := d.DB.BeginTx(ctx, nil)
		if err != nil {
			counts.errors++
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message:    "rss refresh dispatch transaction failed",
				Operation:  "rss_refresh",
				ReasonCode: "dispatch_tx_failed",
				OwnerType:  "rss_source",
				OwnerID:    target.sourceID,
				ErrorClass: "rss_refresh",
			})
			continue
		}
		runtimeVersion, ineligibility, eligibilityErr := sourceRefreshEligibilityTx(ctx, tx, target.sourceID)
		if eligibilityErr != nil {
			tx.Rollback()
			counts.errors++
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message:    "rss refresh source eligibility read failed",
				Operation:  "rss_refresh",
				ReasonCode: "eligibility_failed",
				OwnerType:  "rss_source",
				OwnerID:    target.sourceID,
				ErrorClass: "rss_refresh",
			})
			continue
		}
		if ineligibility != subscriptiondomain.SourceRefreshEligible {
			tx.Rollback()
			counts.skipped++
			continue
		}
		_, err = d.WorkItems.Create(ctx, tx, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: target.sourceID},
			Kind:                 workitems.KindRSSRefresh,
			IdempotencyKey:       "rss_refresh:" + target.sourceID,
			MaxAttempts:          5,
			ExpectedOwnerVersion: runtimeVersion,
		})
		if err != nil {
			tx.Rollback()
			if errors.Is(err, workitems.ErrLiveItemConflict) {
				counts.merged++
			} else {
				counts.errors++
				d.logWarn(ctx, observabilityport.RuntimeEvent{
					Message:    "rss refresh work item creation failed",
					Operation:  "rss_refresh",
					ReasonCode: "work_item_create_failed",
					OwnerType:  "rss_source",
					OwnerID:    target.sourceID,
					ErrorClass: "rss_refresh",
				})
			}
			continue
		}
		if err := tx.Commit(); err != nil {
			counts.errors++
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message:    "rss refresh dispatch commit failed",
				Operation:  "rss_refresh",
				ReasonCode: "dispatch_commit_failed",
				OwnerType:  "rss_source",
				OwnerID:    target.sourceID,
				ErrorClass: "rss_refresh",
			})
			continue
		}
		counts.enqueued++
	}
	return counts, nil
}

// dispatchCatalogRefresh refreshes the current and the next catalog season of
// every provider whose refresh switch is enabled. The
// refresh itself sends the stored conditional-request validators, so an
// unchanged season costs one 304 and rewrites no entries. Unlike the other
// kinds the target work is executed inline: there are at most four targets and
// the catalog refresh claim already deduplicates against manual refreshes.
// A refreshed season counts as enqueued, a disabled one as skipped, and an
// outage snapshot or refresh error as a dispatch error.
func (d *Dispatcher) dispatchCatalogRefresh(ctx context.Context) (dispatchCounts, error) {
	var counts dispatchCounts
	if d.CatalogRefresher == nil {
		return counts, errors.New("catalog refresher is not configured")
	}
	now := time.Now()
	if d.now != nil {
		now = d.now()
	}
	seasons := []string{catalogdomain.SeasonAt(now), catalogdomain.SeasonAfter(now)}
	for _, provider := range []string{catalogapp.ProviderAniBT, catalogapp.ProviderMikan} {
		counts.targetCount += len(seasons)
		// One switch per provider: the latest saved preference row of any
		// season, so an opt-out survives the season rollover.
		enabled, err := d.CatalogRefresher.ProviderRefreshEnabled(ctx, provider)
		if err != nil {
			counts.errors += len(seasons)
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message: "catalog refresh preference read failed", Operation: "catalog_refresh",
				ReasonCode: "preference_read_failed", OwnerType: "provider", OwnerID: provider, ErrorClass: "catalog_refresh",
			})
			continue
		}
		if !enabled {
			counts.skipped += len(seasons)
			continue
		}
		for _, season := range seasons {
			snapshot, err := d.CatalogRefresher.RefreshProviderSeason(ctx, provider, season)
			if err != nil || snapshot.Status == catalogdomain.StatusOutage {
				counts.errors++
				d.logWarn(ctx, observabilityport.RuntimeEvent{
					Message: "catalog season refresh failed", Operation: "catalog_refresh",
					ReasonCode: "catalog_refresh_failed", OwnerType: "provider", OwnerID: provider + ":" + season, ErrorClass: "catalog_refresh",
				})
				continue
			}
			counts.enqueued++
		}
	}
	return counts, nil
}

// dispatchMetadataMatch finds active subscriptions whose anime metadata is
// incomplete (BGM or TMDB tuple missing), runs the three-layer matcher, applies
// the shared fill-if-empty backfill (internal/metadata/backfill; partial results
// are persisted, stored values are never overwritten), and enqueues
// release_evaluate WorkItems for waiting_metadata releases once metadata is complete.
func (d *Dispatcher) dispatchMetadataMatch(ctx context.Context) (dispatchCounts, error) {
	var counts dispatchCounts
	if d.MetadataMatcher == nil {
		return counts, nil
	}

	rows, err := d.DB.QueryContext(ctx, `
SELECT ma.id, ma.title, s.origin, s.origin_entry_ref,
       m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season,
       m.total_episodes, m.year, m.season, m.media_kind_override, m.version
FROM managed_anime ma
JOIN subscriptions s ON s.managed_anime_id = ma.id AND s.state = 'active'
JOIN anime_metadata m ON m.managed_anime_id = ma.id
WHERE m.bgm_work_id IS NULL
	   OR m.tmdb_work_id IS NULL
	   OR m.tmdb_media_kind IS NULL
	   OR m.tmdb_season IS NULL`)
	if err != nil {
		return counts, fmt.Errorf("query subscriptions needing metadata: %w", err)
	}
	defer rows.Close()

	type metaTarget struct {
		managedAnimeID  string
		title           string
		origin          string
		entryRef        sql.NullString
		bgmWorkID       sql.NullString
		tmdbWorkID      sql.NullString
		tmdbKind        sql.NullString
		tmdbSeason      sql.NullInt64
		totalEpisodes   sql.NullInt64
		year            sql.NullInt64
		season          sql.NullInt64
		mediaKind       sql.NullString
		metadataVersion int64
	}
	var targets []metaTarget
	for rows.Next() {
		var t metaTarget
		if err := rows.Scan(&t.managedAnimeID, &t.title, &t.origin, &t.entryRef,
			&t.bgmWorkID, &t.tmdbWorkID, &t.tmdbKind, &t.tmdbSeason, &t.totalEpisodes, &t.year, &t.season, &t.mediaKind, &t.metadataVersion); err != nil {
			return counts, fmt.Errorf("scan metadata target: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return counts, fmt.Errorf("iterate metadata targets: %w", err)
	}

	counts.targetCount = len(targets)

	for _, target := range targets {
		entryRef := ""
		if target.entryRef.Valid {
			entryRef = target.entryRef.String
		}
		c, err := d.matchOne(ctx, target.managedAnimeID, target.title, target.origin, entryRef,
			target.bgmWorkID, target.tmdbWorkID, target.tmdbKind, target.mediaKind, target.tmdbSeason, target.totalEpisodes, target.year, target.season, target.metadataVersion)
		if err != nil {
			counts.errors++
			d.logWarn(ctx, observabilityport.RuntimeEvent{
				Message:    "metadata match dispatch failed",
				Operation:  "metadata_match",
				ReasonCode: "metadata_match_failed",
				OwnerType:  "managed_anime",
				OwnerID:    target.managedAnimeID,
				ErrorClass: "metadata_match",
			})
			continue
		}
		counts.enqueued += c.enqueued
		counts.merged += c.merged
		counts.skipped += c.skipped
	}
	return counts, nil
}

func (d *Dispatcher) matchOne(ctx context.Context, managedAnimeID, title, origin, entryRef string,
	bgmWorkID, tmdbWorkID, tmdbKind, ownerMediaKind sql.NullString, tmdbSeason, totalEpisodes, year, ownerSeason sql.NullInt64, metadataVersion int64) (dispatchCounts, error) {
	var counts dispatchCounts

	skipBgm := bgmWorkID.Valid
	skipTmdb := tmdbWorkID.Valid && tmdbKind.Valid && tmdbSeason.Valid

	result, err := d.MetadataMatcher.Match(matcher.WithAffectedAnime(ctx, managedAnimeID), origin, entryRef, title, skipBgm, skipTmdb)
	if err != nil {
		d.recordMetadataMatchOutcome(ctx, managedAnimeID, "failure", metadataMatchErrorCode(err), safeDispatchError(err))
		return counts, fmt.Errorf("match: %w", err)
	}
	if result == nil {
		d.recordMetadataMatchOutcome(ctx, managedAnimeID, "warning", "metadata_match_no_result", "No metadata match was found.")
		counts.skipped++
		return counts, nil
	}

	stored := backfill.Stored{}
	if bgmWorkID.Valid {
		stored.BGMWorkID = &bgmWorkID.String
	}
	if tmdbWorkID.Valid && tmdbKind.Valid && tmdbSeason.Valid {
		season := int(tmdbSeason.Int64)
		stored.TMDBWorkID, stored.TMDBKind, stored.TMDBSeason = &tmdbWorkID.String, &tmdbKind.String, &season
	}
	if totalEpisodes.Valid {
		n := int(totalEpisodes.Int64)
		stored.TotalEpisodes = &n
	}
	if year.Valid {
		y := int(year.Int64)
		stored.Year = &y
	}
	if ownerSeason.Valid {
		s := int(ownerSeason.Int64)
		stored.Season = &s
	}
	if ownerMediaKind.Valid {
		kind := ownerMediaKind.String
		stored.MediaKind = &kind
	}
	plan := backfill.Merge(stored, result)
	outcome := backfill.Classify(plan, result)
	if !plan.Changed() {
		if outcome == backfill.OutcomeNoResult {
			d.recordMetadataMatchOutcome(ctx, managedAnimeID, "warning", "metadata_match_no_result", "No metadata match was found.")
		} else {
			d.recordMetadataMatchOutcome(ctx, managedAnimeID, "warning", "metadata_match_partial", "Only part of the required metadata identity was found.")
		}
		counts.skipped++
		return counts, nil
	}

	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return counts, fmt.Errorf("begin metadata update tx: %w", err)
	}
	defer tx.Rollback()

	var lockedSubscriptionID, lockedTitle, subscriptionState string
	if err := tx.QueryRowContext(ctx, `
	SELECT s.id, ma.title, s.state
	FROM subscriptions s
	JOIN managed_anime ma ON ma.id = s.managed_anime_id
	WHERE s.managed_anime_id = $1 FOR UPDATE`, managedAnimeID).Scan(&lockedSubscriptionID, &lockedTitle, &subscriptionState); err != nil {
		return counts, fmt.Errorf("lock metadata subscription: %w", err)
	}
	if subscriptionState != "active" {
		// Paused or stopped after the target scan: no automatic action.
		counts.skipped++
		return counts, nil
	}

	// CAS write of the empty fields only: a version conflict writes nothing and
	// the next run retries.
	applied, err := backfill.ApplyTx(ctx, tx, managedAnimeID, metadataVersion, plan, result)
	if err != nil {
		return counts, err
	}
	if !applied {
		counts.skipped++
		return counts, nil
	}

	// If metadata is now complete, enqueue release_evaluate for waiting_metadata releases.
	if plan.Merged.Complete() {
		relRows, err := tx.QueryContext(ctx, `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources rs ON rs.id = r.rss_source_id
JOIN subscriptions s ON s.id = rs.subscription_id
WHERE s.managed_anime_id = $1
  AND r.verdict = 'waiting_metadata'
  AND `+animeapp.ReleaseReevaluationScopeSQL+`
ORDER BY r.id COLLATE "C"
FOR UPDATE OF r`,
			managedAnimeID)
		if err != nil {
			return counts, fmt.Errorf("query waiting releases: %w", err)
		}
		type relTarget struct {
			id      string
			version int64
		}
		var rels []relTarget
		for relRows.Next() {
			var rt relTarget
			if err := relRows.Scan(&rt.id, &rt.version); err != nil {
				relRows.Close()
				return counts, fmt.Errorf("scan waiting release: %w", err)
			}
			rels = append(rels, rt)
		}
		if err := relRows.Err(); err != nil {
			relRows.Close()
			return counts, fmt.Errorf("iterate waiting releases: %w", err)
		}
		relRows.Close()

		for _, rel := range rels {
			_, err := d.WorkItems.Create(ctx, tx, workitems.WorkItemSpec{
				Owner:                workitems.OwnerRef{Type: workitems.OwnerRelease, ID: rel.id},
				Kind:                 workitems.KindReleaseEvaluate,
				IdempotencyKey:       "release_evaluate:" + rel.id,
				MaxAttempts:          5,
				ExpectedOwnerVersion: rel.version,
			})
			if err != nil {
				if errors.Is(err, workitems.ErrLiveItemConflict) {
					counts.merged++
					continue
				}
				return counts, fmt.Errorf("create release evaluation for %s: %w", rel.id, err)
			}
			counts.enqueued++
		}
	}

	if err := businesslog.WriteMetadataAppliedTx(ctx, txscope.Wrap(tx), d.ActivityWriter, lockedSubscriptionID, managedAnimeID, metadataVersion+1); err != nil {
		return counts, fmt.Errorf("write metadata applied log: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return counts, fmt.Errorf("commit metadata update: %w", err)
	}
	if outcome == backfill.OutcomeComplete {
		d.recordMetadataMatchOutcome(ctx, managedAnimeID, "success", "metadata_match_completed", "Metadata matched successfully.")
	} else {
		d.recordMetadataMatchOutcome(ctx, managedAnimeID, "warning", "metadata_match_partial", "Only part of the required metadata identity was found.")
	}
	return counts, nil
}

func (d *Dispatcher) recordMetadataMatchOutcome(ctx context.Context, managedAnimeID, outcome, resultCode, summary string) {
	if d.Recorder == nil {
		return
	}
	identity := observabilityport.MetadataMatchRoutine(observabilityport.RoutineOutcome(outcome), observabilityport.RoutineResultCode(resultCode))
	if err := d.Recorder.UpsertRoutine(ctx, observabilityport.RoutineRequest{
		Identity: identity, ScopeID: managedAnimeID, ManagedAnimeID: managedAnimeID, Summary: summary,
	}); err != nil {
		d.logError(ctx, observabilityport.RuntimeEvent{
			Message: "automation routine write failed", Operation: "observability", ReasonCode: "routine_write_failed",
			OwnerType: "managed_anime", OwnerID: managedAnimeID, ErrorClass: "observability",
		})
	}
}

func metadataMatchErrorCode(err error) string {
	if matcher.IsMatchErrorKind(err, matcher.ErrorConfig) {
		return "metadata_match_config_error"
	}
	return "metadata_match_provider_error"
}

func (d *Dispatcher) persistGroupSnapshot(ctx context.Context, result providerdomain.GroupReadResult) (providerdomain.GroupSnapshot, error) {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return providerdomain.GroupSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := d.persistGroupSnapshotTx(ctx, tx, result)
	if err != nil {
		return snapshot, err
	}
	if err := tx.Commit(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (d *Dispatcher) persistGroupSnapshotTx(ctx context.Context, tx *sql.Tx, result providerdomain.GroupReadResult) (providerdomain.GroupSnapshot, error) {
	return catalogapp.PersistGroupObservationTx(ctx, tx, result)
}

func (d *Dispatcher) recordGroupRefreshFailure(ctx context.Context, providerName, catalogEntryRef string, cause error) error {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := d.DB.ExecContext(ctx, `
INSERT INTO provider_group_refresh_health(provider, catalog_entry_ref, state, last_attempt_at, error_code, error_message, version)
VALUES ($1, $2, 'error', CURRENT_TIMESTAMP, 'PROVIDER_GROUP_READ_FAILED', $3, 1)
ON CONFLICT (provider, catalog_entry_ref) DO UPDATE SET
    state = 'error', last_attempt_at = CURRENT_TIMESTAMP, error_code = 'PROVIDER_GROUP_READ_FAILED', error_message = EXCLUDED.error_message,
    version = provider_group_refresh_health.version + 1, updated_at = CURRENT_TIMESTAMP`, providerName, catalogEntryRef, message)
	return err
}

// readerEndpoint resolves the connection endpoint for a provider kind.
func readerEndpoint(ctx context.Context, d *Dispatcher, providerKind providerdomain.Kind) string {
	if d == nil || d.Connections == nil {
		return ""
	}
	connections, err := d.Connections.ListConnections(ctx)
	if err != nil {
		return ""
	}
	selected, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionKind(providerKind))
	if err != nil {
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return ""
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(configurationdomain.ConnectionKind(providerKind))
		if !ok {
			return ""
		}
		selected = fallback
	}
	return selected.Endpoint
}

// logError emits a structured error event. Raw error text never enters the
// message; the stable failure category lives in ReasonCode/ErrorClass.
func (d *Dispatcher) logError(ctx context.Context, event observabilityport.RuntimeEvent) {
	if d == nil || d.Logger == nil {
		return
	}
	event.Component = "automation"
	event.Outcome = "failure"
	d.Logger.Error(ctx, event)
}

// logWarn emits a structured warning event for recoverable per-target
// failures.
func (d *Dispatcher) logWarn(ctx context.Context, event observabilityport.RuntimeEvent) {
	if d == nil || d.Logger == nil {
		return
	}
	event.Component = "automation"
	event.Outcome = "failure"
	d.Logger.Warn(ctx, event)
}

// logInfo emits a structured informational event.
func (d *Dispatcher) logInfo(ctx context.Context, event observabilityport.RuntimeEvent) {
	if d == nil || d.Logger == nil {
		return
	}
	event.Component = "automation"
	d.Logger.Info(ctx, event)
}
