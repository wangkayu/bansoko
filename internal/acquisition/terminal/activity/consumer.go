// Package activity is the independent acquisition Activity consumer
// (ConsumeAcquisitionActivityTx).
//
// Typed acquisition stages write no Activity. After they commit, this consumer
// derives the run-log entries of an acquisition from its committed facts:
//
//   - the download task: qB submission confirmed, download completed/failed;
//   - the verified Artifact;
//   - each delivery result (file delivered, adopted, retained or failed);
//   - the root's terminal state (succeeded, failed, cancelled).
//
// It keeps no outbox, queue or cursor. Each fact row carries its own "logged"
// marker (download_tasks.activity_submit_logged_at / activity_terminal_logged_at,
// artifacts / acquisition_files / acquisitions.activity_logged_at),
// and every pass is a bounded scan, through a partial index, of the facts
// inside the retention window whose marker is still NULL. Each entry is written
// in its own transaction that locks the managed anime's lifecycle first
// (instance-first; a retired or missing owner is skipped, the retirement fence
// would refuse the row anyway), then the subscription, then locks the fact row
// and sets its marker through the owner-held acquisition_activity_mark_logged
// (SKIP LOCKED: a fact a stage transaction holds is left for the next pass;
// Go code never writes download_tasks directly), and appends with an
// immutable fact key. So a retry, a restart or two
// concurrent passes never produce a duplicate (ON CONFLICT on (subscription,
// dedupe_key)), and a fact leaves the scan exactly when its entry commits.
//
// A consumer failure rolls back only its own entry, marker included: the
// acquisition it describes is otherwise never touched. The next pass retries;
// while entries keep failing one global diagnostic episode stays open. A fact
// that has no scope (inactive owners, a Release that is gone) keeps a NULL
// marker and is never logged.
//
// One-shot acquisitions are logged like subscription ones: the one-shot's
// temporary anime and its subscription (origin oneshot) are the scope, and the
// records read derives the entry's source from that origin.
//
// An import retry re-runs the root: its file results and its settlement are
// facts again, so their keys carry the retry ordinal (":r<n>") and the second
// run gets its own entries.
//
// The settlement of a failed or cancelled root also writes, in its own
// transaction, the not_replaced line of each replace decision its terminal
// transition staled (stale_failed_candidate_decisions), so a planned
// replacement that never happens says so in the run.
package activity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/observability/businesslog"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

const (
	// DefaultWindow bounds the scan to facts younger than the business-log
	// retention, so a fact the log would already have purged is not re-logged.
	DefaultWindow = 14 * 24 * time.Hour
	// DefaultBatch bounds each source per pass.
	DefaultBatch = 200
)

// Consumer derives acquisition run-log entries from committed facts.
type Consumer struct {
	DB          *sql.DB
	Writer      observabilityport.ActivityWriter
	Diagnostics observabilityport.Recorder
	Window      time.Duration
	Batch       int
}

// Result counts one pass.
type Result struct {
	Written int
	Skipped int
	Failed  int
}

// entry is one derived Activity, the owner it must be locked under and the
// fact row whose marker the entry sets.
type entry struct {
	managedAnimeID string
	record         observabilityport.ActivityRecord
	fact           fact
	// notReplaced is set on the settlement of a failed or cancelled root:
	// the replace decisions its terminal transition staled.
	notReplaced *notReplacedScope
}

// fact names one fact row for acquisition_activity_mark_logged: its kind, its
// id and, for a file result, the file ref.
type fact struct {
	kind, id, fileRef string
}

// ConsumeOnce runs one bounded pass. It returns an error only when the scan
// itself fails or at least one entry could not be written; written entries
// stay committed either way.
func (c *Consumer) ConsumeOnce(ctx context.Context) (Result, error) {
	if c.DB == nil || c.Writer == nil {
		return Result{}, errors.New("acquisition activity consumer is not wired")
	}
	window, batch := c.Window, c.Batch
	if window <= 0 {
		window = DefaultWindow
	}
	if batch <= 0 {
		batch = DefaultBatch
	}
	var result Result
	var firstErr error
	for _, source := range sources {
		entries, err := source(ctx, c.DB, window, batch)
		if err != nil {
			firstErr = errors.Join(firstErr, err)
			result.Failed++
			continue
		}
		for _, e := range entries {
			written, err := c.write(ctx, e)
			switch {
			case err != nil:
				result.Failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("append %s for %s %s: %w", e.record.Kind, e.record.SubjectType, e.record.SubjectID, err)
				}
			case written:
				result.Written++
			default:
				result.Skipped++
			}
		}
	}
	c.recordDiagnostic(ctx, result, firstErr)
	if firstErr != nil {
		return result, firstErr
	}
	return result, nil
}

// write appends one entry: lifecycle (instance) first, then the subscription,
// then the fact row and its marker (acquisition_activity_mark_logged), then
// the idempotent insert, all in one transaction. A retired or missing owner,
// or a fact row another transaction holds, skips (the marker stays NULL, the
// next pass retries).
func (c *Consumer) write(ctx context.Context, e entry) (bool, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRowContext(ctx, `SELECT lifecycle_state FROM managed_anime WHERE id=$1 FOR KEY SHARE`, e.managedAnimeID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && state != "active") {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM subscriptions WHERE id=$1 AND managed_anime_id=$2 FOR KEY SHARE`, e.record.SubscriptionID, e.managedAnimeID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var marked bool
	if err := tx.QueryRowContext(ctx, `SELECT acquisition_activity_mark_logged($1, $2, NULLIF($3, ''))`, e.fact.kind, e.fact.id, e.fact.fileRef).Scan(&marked); err != nil {
		return false, fmt.Errorf("mark %s %s logged: %w", e.record.SubjectType, e.record.SubjectID, err)
	}
	if !marked {
		return false, nil
	}
	if err := c.Writer.AppendTx(ctx, txscope.Wrap(tx), e.record); err != nil {
		return false, err
	}
	if e.notReplaced != nil {
		if err := c.writeNotReplacedTx(ctx, tx, e); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// recordDiagnostic keeps one global episode open while entries fail and closes
// it after a clean pass. Diagnostics are best-effort.
func (c *Consumer) recordDiagnostic(ctx context.Context, result Result, err error) {
	if c.Diagnostics == nil {
		return
	}
	identity := observabilityport.AcquisitionActivityConsumeFailure()
	if err != nil {
		_ = c.Diagnostics.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{
			Identity: identity, Severity: observabilityport.SeverityWarning, ScopeID: "global",
			LastError: fmt.Sprintf("%d run-log entries could not be written: %s", result.Failed, err.Error()),
		})
		return
	}
	_ = c.Diagnostics.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{Identity: identity, ScopeID: "global"})
}

// The fixed scope of an acquisition: the subscription and managed anime its
// Release belongs to (a one-shot's Release belongs to its temporary anime and
// subscription). Roots whose Release is gone and owners that are not active
// have no scope (no row). retry_ordinal counts the root's import retries.
const scopeJoin = `
JOIN acquisitions root ON root.id = fact.acquisition_id
JOIN LATERAL (
  SELECT sub.id AS subscription_id, sub.managed_anime_id, release.id AS release_id,
         (SELECT COALESCE(max(x.ordinal), 0) FROM acquisition_import_retries x WHERE x.acquisition_id = root.id) AS retry_ordinal,
         COALESCE(btrim(source.group_name), '') AS group_name,
         COALESCE(btrim(release.raw_facts->>'title'), '') AS release_title,
         (SELECT (snap.release_facts->'batch_plan'->'coverage_keys')::text
          FROM media_fact_snapshots snap
          WHERE snap.release_id = release.id
          ORDER BY snap.created_at DESC, snap.id DESC
          LIMIT 1) AS coverage_keys
  FROM releases release
  JOIN rss_sources source ON source.id = release.rss_source_id
  JOIN subscriptions sub ON sub.id = source.subscription_id
  JOIN managed_anime anime ON anime.id = sub.managed_anime_id
  JOIN managed_anime life ON life.id = anime.id AND life.lifecycle_state = 'active'
  WHERE release.id = COALESCE(
    (SELECT t.release_id FROM download_tasks t WHERE t.acquisition_id = root.id ORDER BY t.id LIMIT 1),
    root.source_release_ref)
) scope ON TRUE`

type source func(context.Context, *sql.DB, time.Duration, int) ([]entry, error)

var sources = []source{submittedEntries, downloadTerminalEntries, artifactVerifiedEntries, fileResultEntries, settledEntries}

func windowStart(window time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(window/time.Second))
}

// submittedEntries: the qB submission was confirmed (the task carries its
// provider correlation) and not logged yet.
func submittedEntries(ctx context.Context, db *sql.DB, window time.Duration, batch int) ([]entry, error) {
	rows, err := db.QueryContext(ctx, `
SELECT fact.id, fact.version, COALESCE(fact.correlation_id, fact.acquisition_id),
       COALESCE(fact.provider_correlation->>'name', ''),
       fact.updated_at, `+manifestTotals+`,
       `+scopeColumns+`
FROM download_tasks fact`+scopeJoin+`
WHERE fact.provider_correlation IS NOT NULL AND fact.activity_submit_logged_at IS NULL
  AND fact.updated_at > CURRENT_TIMESTAMP - $1::interval
ORDER BY fact.updated_at, fact.id
LIMIT $2`, windowStart(window), batch)
	if err != nil {
		return nil, fmt.Errorf("scan submitted downloads: %w", err)
	}
	defer rows.Close()
	var out []entry
	for rows.Next() {
		var taskID, correlation, name string
		var version, files, size int64
		var at time.Time
		var scope runScope
		if err := rows.Scan(append([]any{&taskID, &version, &correlation, &name, &at, &files, &size}, scope.targets()...)...); err != nil {
			return nil, err
		}
		out = append(out, entry{managedAnimeID: scope.managedAnimeID, record: observabilityport.ActivityRecord{
			SubscriptionID: scope.subscriptionID, ManagedAnimeID: scope.managedAnimeID, Kind: "download_changed", Outcome: "info",
			ReasonCode: "submitted", SubjectType: "download_task", SubjectID: taskID, SubjectVersion: version,
			DedupeKey: "acquisition:submitted:" + taskID, ReleaseID: scope.releaseID,
			MessageCode:   observabilityport.MessageDownloadSubmitted,
			MessageParams: map[string]any{observabilityport.ParamName: name}, CorrelationID: correlation,
			CausationType: "release", CausationID: scope.releaseID, OccurredAt: at,
			Details: scope.details(map[string]any{"item_count": files, "size_bytes": size}),
		}, fact: fact{kind: "download_submitted", id: taskID}})
	}
	return out, rows.Err()
}

// downloadTerminalEntries: the download task reached completed or failed and
// was not logged yet.
func downloadTerminalEntries(ctx context.Context, db *sql.DB, window time.Duration, batch int) ([]entry, error) {
	rows, err := db.QueryContext(ctx, `
SELECT fact.id, fact.version, COALESCE(fact.correlation_id, fact.acquisition_id),
       fact.state, COALESCE(fact.failure_code, ''), fact.updated_at, `+manifestTotals+`,
       `+scopeColumns+`
FROM download_tasks fact`+scopeJoin+`
WHERE fact.state IN ('completed', 'failed') AND fact.activity_terminal_logged_at IS NULL
  AND fact.updated_at > CURRENT_TIMESTAMP - $1::interval
ORDER BY fact.updated_at, fact.id
LIMIT $2`, windowStart(window), batch)
	if err != nil {
		return nil, fmt.Errorf("scan terminal downloads: %w", err)
	}
	defer rows.Close()
	var out []entry
	for rows.Next() {
		var taskID, correlation, state, code string
		var version, files, size int64
		var at time.Time
		var scope runScope
		if err := rows.Scan(append([]any{&taskID, &version, &correlation, &state, &code, &at, &files, &size}, scope.targets()...)...); err != nil {
			return nil, err
		}
		outcome, reason, message := "success", "qb_sync_succeeded", observabilityport.MessageDownloadCompleted
		switch {
		case state == "failed" && code == downloaderTaskRemoved:
			// Deleted in the downloader by the user: the record is cancelled, which
			// is not a failure.
			outcome, reason, message = "info", reasonCode(code, "qb_sync_failed"), observabilityport.MessageDownloadRemoved
		case state == "failed":
			outcome, reason, message = "failure", reasonCode(code, "qb_sync_failed"), observabilityport.MessageDownloadFailed
		}
		out = append(out, entry{managedAnimeID: scope.managedAnimeID, record: observabilityport.ActivityRecord{
			SubscriptionID: scope.subscriptionID, ManagedAnimeID: scope.managedAnimeID, Kind: "download_changed", Outcome: outcome,
			ReasonCode: reason, SubjectType: "download_task", SubjectID: taskID, SubjectVersion: version,
			DedupeKey: "acquisition:download:" + taskID, ReleaseID: scope.releaseID, MessageCode: message,
			CorrelationID: correlation, CausationType: "release", CausationID: scope.releaseID, OccurredAt: at,
			Details: scope.details(map[string]any{"item_count": files, "size_bytes": size}),
		}, fact: fact{kind: "download_terminal", id: taskID}})
	}
	return out, rows.Err()
}

// artifactVerifiedEntries: the downloaded files were inspected and verified,
// and that was not logged yet.
func artifactVerifiedEntries(ctx context.Context, db *sql.DB, window time.Duration, batch int) ([]entry, error) {
	rows, err := db.QueryContext(ctx, `
SELECT fact.id, fact.version, fact.download_task_id, COALESCE(fact.correlation_id, fact.acquisition_id),
       fact.verified_at, `+manifestTotals+`,
       `+scopeColumns+`
FROM artifacts fact`+scopeJoin+`
WHERE fact.state = 'verified' AND fact.activity_logged_at IS NULL
  AND fact.verified_at > CURRENT_TIMESTAMP - $1::interval
ORDER BY fact.verified_at, fact.id
LIMIT $2`, windowStart(window), batch)
	if err != nil {
		return nil, fmt.Errorf("scan verified artifacts: %w", err)
	}
	defer rows.Close()
	var out []entry
	for rows.Next() {
		var artifactID, taskID, correlation string
		var version, files, size int64
		var at time.Time
		var scope runScope
		if err := rows.Scan(append([]any{&artifactID, &version, &taskID, &correlation, &at, &files, &size}, scope.targets()...)...); err != nil {
			return nil, err
		}
		out = append(out, entry{managedAnimeID: scope.managedAnimeID, record: observabilityport.ActivityRecord{
			SubscriptionID: scope.subscriptionID, ManagedAnimeID: scope.managedAnimeID, Kind: "artifact_verified", Outcome: "success",
			ReasonCode: "verified", SubjectType: "artifact", SubjectID: artifactID, SubjectVersion: version,
			DedupeKey: "acquisition:verified:" + artifactID, ReleaseID: scope.releaseID, MessageCode: observabilityport.MessageArtifactVerified,
			CorrelationID: correlation, CausationType: "download_task", CausationID: taskID, OccurredAt: at,
			Details: scope.details(map[string]any{"item_count": files, "size_bytes": size}),
		}, fact: fact{kind: "artifact_verified", id: artifactID}})
	}
	return out, rows.Err()
}

// fileResultEntries: one entry per required file's committed delivery result
// (immutable), whether the file was executed, adopted, retained or failed —
// including files a planner refused and dependants suppressed with their video —
// that was not logged yet.
func fileResultEntries(ctx context.Context, db *sql.DB, window time.Duration, batch int) ([]entry, error) {
	rows, err := db.QueryContext(ctx, `
SELECT fact.acquisition_id, fact.file_ref, fact.outcome, COALESCE(fact.completion_mode, ''),
       COALESCE(fact.delivery_reason_code, ''), fact.delivered_at,
       COALESCE(op.id, ''), COALESCE(op.version, 1), COALESCE(op.target_path, ''), COALESCE(op.artifact_id, ''),
       COALESCE(fact.relative_path, ''), fact.exact_size,
       `+scopeColumns+`
FROM acquisition_files fact`+scopeJoin+`
LEFT JOIN file_operations op ON op.id = fact.operation_id
WHERE fact.activity_logged_at IS NULL
  AND fact.delivered_at > CURRENT_TIMESTAMP - $1::interval
ORDER BY fact.delivered_at, fact.acquisition_id, fact.file_ref
LIMIT $2`, windowStart(window), batch)
	if err != nil {
		return nil, fmt.Errorf("scan file results: %w", err)
	}
	defer rows.Close()
	var out []entry
	for rows.Next() {
		var acquisitionID, fileRef, outcome, mode, code, operationID, targetPath, artifactID, sourcePath string
		var version, size int64
		var at time.Time
		var scope runScope
		if err := rows.Scan(append([]any{&acquisitionID, &fileRef, &outcome, &mode, &code, &at, &operationID, &version, &targetPath, &artifactID, &sourcePath, &size}, scope.targets()...)...); err != nil {
			return nil, err
		}
		subjectType, subjectID := "file_operation", operationID
		if operationID == "" {
			subjectType, subjectID = "acquisition", acquisitionID
		}
		name := baseName(targetPath, sourcePath)
		kind, result, reason, message := "file_alignment_completed", "success", "file_alignment_completed", observabilityport.MessageFileImported
		switch {
		case outcome == "failed":
			name = baseName(sourcePath, targetPath)
			kind, result, reason, message = "file_alignment_failed", "failure", reasonCode(code, "file_operation_failed"), observabilityport.MessageFileImportFailed
		case mode == "adopted":
			reason, message = "file_adopted", observabilityport.MessageFileAdopted
		case mode == "retain":
			reason, message = "file_retained", observabilityport.MessageFileRetained
		}
		step := map[string]any{"file_name": businesslog.BoundText(name, 1024), "size_bytes": size}
		if outcome != "failed" {
			step["library_folder"] = businesslog.LibraryFolder(targetPath)
		}
		causationType, causationID := "acquisition", acquisitionID
		if artifactID != "" {
			causationType, causationID = "artifact", artifactID
		}
		out = append(out, entry{managedAnimeID: scope.managedAnimeID, record: observabilityport.ActivityRecord{
			SubscriptionID: scope.subscriptionID, ManagedAnimeID: scope.managedAnimeID, Kind: kind, Outcome: result,
			ReasonCode: reason, SubjectType: subjectType, SubjectID: subjectID, SubjectVersion: version,
			DedupeKey: scope.attemptKey("acquisition:file:" + acquisitionID + ":" + fileRef), ReleaseID: scope.releaseID,
			MessageCode: message, MessageParams: map[string]any{observabilityport.ParamFileName: name},
			CorrelationID: acquisitionID, CausationType: causationType, CausationID: causationID, OccurredAt: at,
			Details: scope.details(step),
		}, fact: fact{kind: "file_result", id: acquisitionID, fileRef: fileRef}})
	}
	return out, rows.Err()
}

// downloaderTaskRemoved is the DownloadTask failure code of a task deleted in
// the downloader; its root settles cancelled.
const downloaderTaskRemoved = "DOWNLOADER_TASK_REMOVED"

// settledEntries: the root reached its one terminal state. A cancelled root is
// either the retirement disposition (OWNER_RETIRED; its owner is retired, so in
// practice it has no scope and is skipped like every retired owner) or a
// download deleted in the downloader (DOWNLOADER_TASK_REMOVED), logged as info.
// A failed or cancelled root carries the not_replaced follow-up; whether its
// download completed tells a failed download from a failed import.
func settledEntries(ctx context.Context, db *sql.DB, window time.Duration, batch int) ([]entry, error) {
	rows, err := db.QueryContext(ctx, `
SELECT fact.id, fact.version, fact.state, fact.terminal_at,
       COALESCE(fact.terminal_receipt->'primary_failure'->>'reason_code', ''),
       (SELECT count(*) FROM acquisition_files df WHERE df.acquisition_id = fact.id AND df.outcome = 'succeeded'),
       (SELECT count(*) FROM acquisition_files df WHERE df.acquisition_id = fact.id AND df.outcome = 'failed'),
       EXISTS (SELECT 1 FROM download_tasks t WHERE t.acquisition_id = fact.id AND t.state = 'completed'),
       `+scopeColumns+`
FROM (SELECT r.*, r.id AS acquisition_id FROM acquisitions r
      WHERE r.terminal_at IS NOT NULL AND r.activity_logged_at IS NULL
        AND r.state IN ('succeeded', 'failed', 'cancelled')
        AND r.terminal_at > CURRENT_TIMESTAMP - $1::interval) fact`+scopeJoin+`
ORDER BY fact.terminal_at, fact.id
LIMIT $2`, windowStart(window), batch)
	if err != nil {
		return nil, fmt.Errorf("scan settled acquisitions: %w", err)
	}
	defer rows.Close()
	var out []entry
	for rows.Next() {
		var id, state, code string
		var version, delivered, failed int64
		var at time.Time
		var downloaded bool
		var scope runScope
		if err := rows.Scan(append([]any{&id, &version, &state, &at, &code, &delivered, &failed,
			&downloaded}, scope.targets()...)...); err != nil {
			return nil, err
		}
		outcome, reason, message := "success", "acquisition_succeeded", observabilityport.MessageAcquisitionDone
		var notReplaced *notReplacedScope
		switch state {
		case "failed":
			outcome, reason, message = "failure", reasonCode(code, "acquisition_failed"), observabilityport.MessageAcquisitionFail
			cause := businesslog.NotReplacedDownloadFailed
			if downloaded {
				cause = businesslog.NotReplacedImportFailed
			}
			notReplaced = &notReplacedScope{reason: cause, runContext: scope.runContext()}
		case "cancelled":
			outcome, reason, message = "info", reasonCode(code, "owner_retired"), observabilityport.MessageAcquisitionStop
			notReplaced = &notReplacedScope{reason: businesslog.NotReplacedCancelled, runContext: scope.runContext()}
		}
		step := map[string]any{"item_count": delivered}
		if failed > 0 {
			step["error_count"] = failed
		}
		out = append(out, entry{managedAnimeID: scope.managedAnimeID, record: observabilityport.ActivityRecord{
			SubscriptionID: scope.subscriptionID, ManagedAnimeID: scope.managedAnimeID, Kind: "acquisition_settled", Outcome: outcome,
			ReasonCode: reason, SubjectType: "acquisition", SubjectID: id, SubjectVersion: version,
			DedupeKey: scope.attemptKey("acquisition:settled:" + id), ReleaseID: scope.releaseID, MessageCode: message,
			CorrelationID: id, CausationType: "acquisition", CausationID: id, OccurredAt: at,
			Details: scope.details(step),
		}, fact: fact{kind: "acquisition_settled", id: id}, notReplaced: notReplaced})
	}
	return out, rows.Err()
}

// reasonCode lowercases a committed reason code into the run-log reason form
// (the frontend catalog is keyed by lowercase codes) and bounds it.
func reasonCode(code, fallback string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > 128 {
		return fallback
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return fallback
		}
	}
	return code
}

// baseName returns the last path element of the first non-empty path; only
// the name is shown, never the private directory.
func baseName(paths ...string) string {
	for _, p := range paths {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if i := strings.LastIndex(p, "/"); i >= 0 {
			p = p[i+1:]
		}
		if p != "" {
			return p
		}
	}
	return ""
}
