package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"bansoko/internal/apperrors"
	"bansoko/internal/library/displaypath"
)

// The SQL half of the execution-record read. Each call runs in one short
// REPEATABLE READ, READ ONLY transaction, so every query of one response sees
// the same snapshot: a root compacted mid-request is read either whole or as
// compact, never as a full header over an emptied graph. No call locks, writes,
// settles or enqueues anything.

const (
	acquisitionPageDefault = 50
	acquisitionPageMax     = 100
)

// acquisitionRootSelect projects one root with its current graph owners. The
// download task and the plan are unique per root; the artifact is unique per
// task; file operations and delivery results are aggregated per root.
const acquisitionRootSelect = `
SELECT r.id, r.entry_kind, r.execution_branch, r.state, r.record_state,
       r.accepted_at, r.retention_anchor_at, r.terminal_at, r.compacted_at, r.compact_trigger,
       r.display_title::text, r.terminal_schema, r.terminal_receipt::text,
       r.settlement_blocked_code,
       ma.id, COALESCE(NULLIF(btrim(ma.title_override), ''), ma.title),
       rel.id, rel.raw_facts->>'title',
       t.state, t.failure_code, t.submit_attempts_started, t.created_at, t.updated_at, t.wait_started_at,
       a.state, a.failure_code, a.inspect_attempts_started, a.created_at, a.updated_at,
       p.state, p.outcome_code, p.plan_attempts_started, p.created_at, p.updated_at,
       COALESCE(ops.total, 0), COALESCE(ops.live, 0),
       r.manifest_digest IS NOT NULL, r.requirements_item_count, r.requirements_required_count,
       COALESCE(dr.ok, 0), COALESCE(dr.failed, 0), dr.first_failed, dr.last_at,
       dc.state, dc.result_code, dc.created_at, dc.updated_at,
       osa.id AS one_shot_id,
       t.verified_bytes_highwater, t.payload_total_bytes, t.provider_correlation->>'download_speed',
       snap.coverage_keys::text,
       r.version,
       CASE WHEN r.record_state = 'full' AND r.state IN ('succeeded', 'failed')
            THEN acquisition_import_retry_refusal(r.id) IS NULL ELSE false END,
       COALESCE(ir.retries, 0), ir.last_at,
       COALESCE(pf.failed, 0), pf.first_reason
FROM acquisitions r
LEFT JOIN managed_anime ma ON ma.id = r.origin_managed_anime_ref
LEFT JOIN releases rel ON rel.id = r.source_release_ref
LEFT JOIN download_tasks t ON r.record_state = 'full' AND t.acquisition_id = r.id
LEFT JOIN artifacts a ON a.download_task_id = t.id AND a.acquisition_id = r.id
LEFT JOIN postprocessing_plans p ON r.record_state = 'full' AND p.acquisition_id = r.id
LEFT JOIN download_completions dc ON dc.acquisition_id = r.id
LEFT JOIN LATERAL (
    SELECT count(*) AS total,
           count(*) FILTER (WHERE o.state IN ('pending', 'running', 'waiting_retry')) AS live
    FROM file_operations o
    WHERE r.record_state = 'full' AND o.acquisition_id = r.id
) ops ON TRUE
LEFT JOIN LATERAL (
    SELECT count(*) FILTER (WHERE d.outcome = 'succeeded') AS ok,
           count(*) FILTER (WHERE d.outcome = 'failed') AS failed,
           (SELECT f.delivery_reason_code FROM acquisition_files f
             WHERE f.acquisition_id = r.id AND f.outcome = 'failed'
             ORDER BY f.file_ref COLLATE "C" LIMIT 1) AS first_failed,
           max(d.delivered_at) AS last_at
    FROM acquisition_files d
    WHERE d.acquisition_id = r.id
) dr ON TRUE
-- A one-shot root's marker is matched on the request idempotency key both
-- rows carry (the marker owns HTTP replay); every other entry kind keeps NULL.
LEFT JOIN one_shot_acquisitions osa ON r.entry_kind = 'one_shot' AND osa.request_idempotency_key = r.request_key
-- The batch plan frozen with the source release's media fact snapshot: the
-- newest snapshot, the same one prepare places the files by.
LEFT JOIN LATERAL (
    SELECT s.release_facts->'batch_plan'->'coverage_keys' AS coverage_keys
    FROM media_fact_snapshots s
    WHERE s.release_id = r.source_release_ref
    ORDER BY s.created_at DESC, s.id DESC
    LIMIT 1
) snap ON TRUE
-- Accepted import retries: the record shows the run count and the latest start.
LEFT JOIN LATERAL (
    SELECT count(*) AS retries, max(x.accepted_at) AS last_at
    FROM acquisition_import_retries x
    WHERE x.acquisition_id = r.id
) ir ON TRUE
-- Failed placement handoffs of completed files fail the import stage too; an
-- import retry reopens them.
LEFT JOIN LATERAL (
    SELECT count(*) AS failed,
           (SELECT po2.reason_code FROM placement_obligations po2 JOIN file_operations o2 ON o2.id = po2.file_operation_id
             WHERE o2.acquisition_id = r.id AND po2.state = 'failed' AND po2.reason_code IS NOT NULL
             ORDER BY o2.file_ref COLLATE "C" LIMIT 1) AS first_reason
    FROM placement_obligations po JOIN file_operations o ON o.id = po.file_operation_id
    WHERE r.record_state = 'full' AND o.acquisition_id = r.id AND po.state = 'failed'
) pf ON TRUE`

type acquisitionRootRow struct {
	id, entryKind, branch, state, recordState string
	acceptedAt, anchorAt                      time.Time
	terminalAt, compactedAt                   sql.NullTime
	compactTrigger                            sql.NullString
	displayTitle                              []byte
	terminalSchema                            sql.NullString
	terminalReceipt                           []byte
	blockedCode                               sql.NullString
	animeID, animeTitle                       sql.NullString
	releaseID, releaseTitle                   sql.NullString
	task, artifact, plan                      nullableStageOwner
	waitStartedAt                             sql.NullTime
	opsTotal, opsLive                         int64
	manifestSealed                            bool
	reqItems, reqRequired                     sql.NullInt64
	deliveredOK, deliveredFailed              int64
	firstFailed                               sql.NullString
	lastDeliveryAt                            sql.NullTime
	completionState, completionResult         sql.NullString
	completionCreated, completionUpdated      sql.NullTime
	oneShotID                                 sql.NullString
	completedBytes, totalBytes                sql.NullInt64
	downloadSpeed                             sql.NullString
	coverageKeys                              sql.NullString
	version                                   int64
	importRetryAvailable                      bool
	importRetries                             int64
	importRetriedAt                           sql.NullTime
	placementsFailed                          int64
	placementFailedReason                     sql.NullString
}

type nullableStageOwner struct {
	state, failureCode   sql.NullString
	attempts             sql.NullInt64
	createdAt, updatedAt sql.NullTime
}

func (o nullableStageOwner) value(wait sql.NullTime) *acquisitionStageOwner {
	if !o.state.Valid {
		return nil
	}
	owner := &acquisitionStageOwner{State: o.state.String, FailureCode: nullableStringPointer(o.failureCode),
		AttemptsStarted: o.attempts.Int64, CreatedAt: o.createdAt.Time, UpdatedAt: o.updatedAt.Time}
	if wait.Valid {
		owner.WaitStartedAt = ptr(wait.Time)
	}
	return owner
}

func scanAcquisitionRoot(scanner interface{ Scan(...any) error }) (acquisitionRootRow, error) {
	var row acquisitionRootRow
	err := scanner.Scan(&row.id, &row.entryKind, &row.branch, &row.state, &row.recordState,
		&row.acceptedAt, &row.anchorAt, &row.terminalAt, &row.compactedAt, &row.compactTrigger,
		&row.displayTitle, &row.terminalSchema, &row.terminalReceipt,
		&row.blockedCode,
		&row.animeID, &row.animeTitle,
		&row.releaseID, &row.releaseTitle,
		&row.task.state, &row.task.failureCode, &row.task.attempts, &row.task.createdAt, &row.task.updatedAt, &row.waitStartedAt,
		&row.artifact.state, &row.artifact.failureCode, &row.artifact.attempts, &row.artifact.createdAt, &row.artifact.updatedAt,
		&row.plan.state, &row.plan.failureCode, &row.plan.attempts, &row.plan.createdAt, &row.plan.updatedAt,
		&row.opsTotal, &row.opsLive,
		&row.manifestSealed, &row.reqItems, &row.reqRequired,
		&row.deliveredOK, &row.deliveredFailed, &row.firstFailed, &row.lastDeliveryAt,
		&row.completionState, &row.completionResult, &row.completionCreated, &row.completionUpdated,
		&row.oneShotID,
		&row.completedBytes, &row.totalBytes, &row.downloadSpeed,
		&row.coverageKeys,
		&row.version, &row.importRetryAvailable, &row.importRetries, &row.importRetriedAt,
		&row.placementsFailed, &row.placementFailedReason)
	return row, err
}

func nullTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return ptr(value.Time)
}

// project turns one root row into its detail; the summary is embedded.
func (row acquisitionRootRow) project(asOf time.Time) (AcquisitionRecordDetail, error) {
	title, err := storedDisplayTitle(row.displayTitle)
	if err != nil {
		return AcquisitionRecordDetail{}, err
	}
	detail := AcquisitionRecordDetail{
		AcquisitionRecordSummary: AcquisitionRecordSummary{
			AcquisitionID: row.id, EntryKind: row.entryKind, State: row.state, RecordDetail: row.recordState,
			AcceptedAt: row.acceptedAt, TerminalAt: nullTimePointer(row.terminalAt), SourceTitle: title,
			OneShotID: nullableStringPointer(row.oneShotID),
		},
		ExecutionBranch: row.branch, CompactedAt: nullTimePointer(row.compactedAt),
		CompactReason: nullableStringPointer(row.compactTrigger), AsOf: asOf, Stages: []AcquisitionStage{},
		Version: row.version, ImportRetryAvailable: row.importRetryAvailable,
	}
	if row.animeID.Valid {
		detail.Anime = &AcquisitionAnimeLink{ManagedAnimeID: row.animeID.String, Title: row.animeTitle.String}
	}
	if row.releaseID.Valid {
		detail.Release = &AcquisitionReleaseLink{ReleaseID: row.releaseID.String, Title: displayValue(nullableStringPointer(row.releaseTitle))}
	}
	detail.Episodes = frozenPlanEpisodes(row.coverageKeys)
	if row.state != "running" {
		outcome, err := storedTerminalReceipt(row.terminalSchema.String, row.terminalReceipt)
		if err != nil {
			return AcquisitionRecordDetail{}, err
		}
		if outcome.Outcome != row.state {
			return AcquisitionRecordDetail{}, fmt.Errorf("%w: receipt outcome %q on %s root", ErrAcquisitionReadInconsistent, outcome.Outcome, row.state)
		}
		detail.Outcome = outcome
		detail.Counts = outcome.Counts
	}
	if row.recordState == "full" {
		facts := acquisitionGraphFacts{
			Branch: row.branch, RootState: row.state, TerminalAt: detail.TerminalAt,
			BlockedCode: nullableStringPointer(row.blockedCode),
			Task:        row.task.value(row.waitStartedAt), Artifact: row.artifact.value(sql.NullTime{}), Plan: row.plan.value(sql.NullTime{}),
			LiveOperations: row.opsLive, Operations: row.opsTotal, ManifestSealed: row.manifestSealed,
			DeliveredOK: row.deliveredOK, DeliveredFailed: row.deliveredFailed,
			FirstFailedReason: nullableStringPointer(row.firstFailed), LastDeliveryAt: nullTimePointer(row.lastDeliveryAt),
			ImportRetries: row.importRetries, ImportRetriedAt: nullTimePointer(row.importRetriedAt),
			PlacementsFailed: row.placementsFailed, PlacementFailedReason: nullableStringPointer(row.placementFailedReason),
		}
		if row.completionState.Valid {
			facts.Completion = &acquisitionCompletionFacts{State: row.completionState.String, ResultCode: nullableStringPointer(row.completionResult),
				CreatedAt: row.completionCreated.Time, UpdatedAt: row.completionUpdated.Time}
		}
		if row.reqItems.Valid {
			facts.Requirements = &acquisitionRequirementFacts{Items: row.reqItems.Int64, Required: row.reqRequired.Int64}
		}
		detail.Stages = deriveAcquisitionStages(facts)
		detail.Requirements = requirementsView(facts)
		detail.FilesAvailable = facts.Requirements != nil
	}
	detail.Stage, detail.StageState, detail.ReasonCode = summaryPosition(row.state, detail.Stages, detail.Outcome)
	if detail.Stage != nil && *detail.Stage == AcquisitionStageDownloadWait && detail.StageState != nil && *detail.StageState == AcquisitionStageStateRunning {
		detail.Transfer = downloadTransfer(row.completedBytes, row.totalBytes, row.downloadSpeed, row.task.updatedAt)
	}
	return detail, nil
}

func (s *Service) readOnlySnapshot(ctx context.Context) (*sql.Tx, time.Time, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("begin acquisition record read: %w", err)
	}
	var asOf time.Time
	if err := tx.QueryRowContext(ctx, `SELECT statement_timestamp()`).Scan(&asOf); err != nil {
		_ = tx.Rollback()
		return nil, time.Time{}, fmt.Errorf("read acquisition record snapshot time: %w", err)
	}
	return tx, asOf, nil
}

func pageLimit(limit int) (int, error) {
	if limit == 0 {
		return acquisitionPageDefault, nil
	}
	if limit < 1 || limit > acquisitionPageMax {
		return 0, &apperrors.BadRequest{Code: "LIMIT_INVALID", Message: "limit must be between 1 and 100."}
	}
	return limit, nil
}

// acquisitionOriginEntryKinds is who started the work, by entry kind: the
// automatic RSS chain and the add-subscription download are subscription work,
// a one-shot is one-shot work. The acquisitions check constraint admits only
// these three entry kinds, so every record has exactly one origin; a row
// outside the map fails the read instead of being counted anywhere.
var acquisitionOriginEntryKinds = map[string][]string{
	"subscription": {"automatic", "subscription_add"},
	"one_shot":     {"one_shot"},
}

// recordBaseFilter is the anime, state and accepted-time part of the list
// filter, appended after the given leading arguments ($1 is the access scope).
func recordBaseFilter(query AcquisitionRecordQuery, args []any) (string, []any) {
	where := `r.access_scope = $1`
	add := func(clause string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(clause, len(args))
	}
	if query.ManagedAnimeID != "" {
		add(` AND r.origin_managed_anime_ref = $%d`, query.ManagedAnimeID)
	}
	if query.State != "" {
		add(` AND r.state = $%d`, query.State)
	}
	if query.AcceptedFrom != nil {
		add(` AND r.retention_anchor_at >= $%d`, *query.AcceptedFrom)
	}
	if query.AcceptedTo != nil {
		add(` AND r.retention_anchor_at < $%d`, *query.AcceptedTo)
	}
	return where, args
}

// readRecordOriginCounts totals the records under the base filter per origin
// in the page's snapshot. A record whose entry kind has no origin fails the
// read (fail closed) rather than being dropped from both totals.
func readRecordOriginCounts(ctx context.Context, tx *sql.Tx, where string, args []any) (AcquisitionRecordOriginCounts, error) {
	n := len(args)
	args = append(args, pq.Array(acquisitionOriginEntryKinds["subscription"]), pq.Array(acquisitionOriginEntryKinds["one_shot"]))
	var counts AcquisitionRecordOriginCounts
	var unmapped int64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`
SELECT count(*) FILTER (WHERE r.entry_kind = ANY($%[1]d)),
       count(*) FILTER (WHERE r.entry_kind = ANY($%[2]d)),
       count(*) FILTER (WHERE NOT (r.entry_kind = ANY($%[1]d) OR r.entry_kind = ANY($%[2]d)))
FROM acquisitions r
WHERE `, n+1, n+2)+where, args...).Scan(&counts.Subscription, &counts.OneShot, &unmapped)
	if err != nil {
		return AcquisitionRecordOriginCounts{}, fmt.Errorf("count acquisition records by origin: %w", err)
	}
	if unmapped != 0 {
		return AcquisitionRecordOriginCounts{}, fmt.Errorf("%w: %d records have an entry kind without an origin", ErrAcquisitionReadInconsistent, unmapped)
	}
	return counts, nil
}

// ListAcquisitionRecords answers one keyset page of the record list, newest
// accepted first.
func (s *Service) ListAcquisitionRecords(ctx context.Context, query AcquisitionRecordQuery) (AcquisitionRecordPage, error) {
	limit, err := pageLimit(query.Limit)
	if err != nil {
		return AcquisitionRecordPage{}, err
	}
	switch query.State {
	case "", "running", "succeeded", "failed", "cancelled":
	default:
		return AcquisitionRecordPage{}, &apperrors.BadRequest{Code: "QUERY_INVALID", Message: "state is not a record state."}
	}
	switch query.EntryKind {
	case "", "automatic", "subscription_add", "one_shot":
	default:
		return AcquisitionRecordPage{}, &apperrors.BadRequest{Code: "QUERY_INVALID", Message: "entry_kind is not a record entry kind."}
	}
	if _, ok := acquisitionOriginEntryKinds[query.Origin]; query.Origin != "" && !ok {
		return AcquisitionRecordPage{}, &apperrors.BadRequest{Code: "QUERY_INVALID", Message: "origin is not a record origin."}
	}
	if query.AcceptedFrom != nil && query.AcceptedTo != nil && query.AcceptedTo.Before(*query.AcceptedFrom) {
		return AcquisitionRecordPage{}, &apperrors.BadRequest{Code: "QUERY_INVALID", Message: "to must not be before from."}
	}
	var after *recordCursor
	if query.Cursor != "" {
		cursor, ok := decodeRecordCursor(query.Cursor)
		if !ok {
			return AcquisitionRecordPage{}, &apperrors.BadRequest{Code: "INVALID_CURSOR", Message: "cursor is not a record list position."}
		}
		if cursor.Filter != recordFilterDigest(query) {
			return AcquisitionRecordPage{}, &apperrors.BadRequest{Code: "CURSOR_FILTER_MISMATCH", Message: "cursor was issued for different filters."}
		}
		after = &cursor
	}
	// The origin counts share the anime, state and time filters with the page
	// but never origin, entry kind or the cursor.
	countWhere, countArgs := recordBaseFilter(query, []any{acquisitionAccessScope})
	where, args := recordBaseFilter(query, []any{acquisitionAccessScope, limit + 1})
	add := func(clause string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(clause, len(args))
	}
	if query.EntryKind != "" {
		add(` AND r.entry_kind = $%d`, query.EntryKind)
	}
	if query.Origin != "" {
		add(` AND r.entry_kind = ANY($%d)`, pq.Array(acquisitionOriginEntryKinds[query.Origin]))
	}
	if after != nil {
		args = append(args, after.At, after.ID)
		where += fmt.Sprintf(` AND (r.retention_anchor_at, r.id) < ($%d, $%d)`, len(args)-1, len(args))
	}

	tx, asOf, err := s.readOnlySnapshot(ctx)
	if err != nil {
		return AcquisitionRecordPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Count before opening the page rows: one transaction holds one connection,
	// and a second query while rows are still open fails as a busy connection.
	counts, err := readRecordOriginCounts(ctx, tx, countWhere, countArgs)
	if err != nil {
		return AcquisitionRecordPage{}, err
	}
	rows, err := tx.QueryContext(ctx, acquisitionRootSelect+`
WHERE `+where+`
ORDER BY r.retention_anchor_at DESC, r.id DESC
LIMIT $2`, args...)
	if err != nil {
		return AcquisitionRecordPage{}, fmt.Errorf("list acquisition records: %w", err)
	}
	defer rows.Close()
	page := AcquisitionRecordPage{AsOf: asOf, Items: []AcquisitionRecordSummary{}, OriginCounts: counts}
	var last acquisitionRootRow
	for rows.Next() {
		row, err := scanAcquisitionRoot(rows)
		if err != nil {
			return AcquisitionRecordPage{}, fmt.Errorf("scan acquisition record: %w", err)
		}
		if len(page.Items) == limit {
			page.NextCursor = ptr(encodeCursor(recordCursor{Version: 1, At: last.anchorAt, ID: last.id, Filter: recordFilterDigest(query)}))
			break
		}
		detail, err := row.project(asOf)
		if err != nil {
			return AcquisitionRecordPage{}, err
		}
		page.Items = append(page.Items, detail.AcquisitionRecordSummary)
		last = row
	}
	if err := rows.Err(); err != nil {
		return AcquisitionRecordPage{}, fmt.Errorf("iterate acquisition records: %w", err)
	}
	return page, nil
}

// GetAcquisitionRecord answers the stable detail of one root; a missing or
// foreign-scope root is not found.
func (s *Service) GetAcquisitionRecord(ctx context.Context, acquisitionID string) (AcquisitionRecordDetail, error) {
	tx, asOf, err := s.readOnlySnapshot(ctx)
	if err != nil {
		return AcquisitionRecordDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := scanAcquisitionRoot(tx.QueryRowContext(ctx, acquisitionRootSelect+`
WHERE r.id = $1 AND r.access_scope = $2`, acquisitionID, acquisitionAccessScope))
	if errors.Is(err, sql.ErrNoRows) {
		return AcquisitionRecordDetail{}, &apperrors.NotFound{Resource: "acquisition", ID: acquisitionID}
	}
	if err != nil {
		return AcquisitionRecordDetail{}, fmt.Errorf("read acquisition record: %w", err)
	}
	return row.project(asOf)
}

// ListAcquisitionRecordFiles answers one ordinal page of a full root's sealed
// file set. A compact root has no file detail left; a root whose requirements
// are not sealed has no file set yet.
func (s *Service) ListAcquisitionRecordFiles(ctx context.Context, acquisitionID string, query AcquisitionFileQuery) (AcquisitionFilePage, error) {
	tx, asOf, err := s.readOnlySnapshot(ctx)
	if err != nil {
		return AcquisitionFilePage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var recordState string
	var sealed bool
	var animeRef sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT r.record_state, r.requirements_digest IS NOT NULL, r.origin_managed_anime_ref
FROM acquisitions r
WHERE r.id = $1 AND r.access_scope = $2`, acquisitionID, acquisitionAccessScope).Scan(&recordState, &sealed, &animeRef)
	if errors.Is(err, sql.ErrNoRows) {
		return AcquisitionFilePage{}, &apperrors.NotFound{Resource: "acquisition", ID: acquisitionID}
	}
	if err != nil {
		return AcquisitionFilePage{}, fmt.Errorf("read acquisition file root: %w", err)
	}
	if recordState != "full" {
		return AcquisitionFilePage{}, ErrAcquisitionDetailUnavailable
	}
	if !sealed {
		return AcquisitionFilePage{}, ErrAcquisitionFilesNotReady
	}
	limit, err := pageLimit(query.Limit)
	if err != nil {
		return AcquisitionFilePage{}, err
	}
	after := int64(-1)
	if query.Cursor != "" {
		cursor, ok := decodeFileCursor(query.Cursor, acquisitionID)
		if !ok {
			return AcquisitionFilePage{}, &apperrors.BadRequest{Code: "INVALID_CURSOR", Message: "cursor is not a file position of this record."}
		}
		after = cursor.After
	}
	rows, err := tx.QueryContext(ctx, `
SELECT mf.ordinal, mf.file_ref, mf.relative_path, mf.exact_size, mf.media_kind,
       mf.requirement, mf.requirement_reason_code,
       op.id, op.state, op.operation, op.failure_code,
       mf.outcome, mf.completion_mode, mf.effect_class, mf.delivery_source_cleanup, mf.delivery_reason_code,
       NULL::text, NULL::text, pf.attempts, pf.reason_code,
       pl.state, pl.displaced_cause, pl.displaced_by_anime_id,
       CASE WHEN succ.id IS NOT NULL THEN COALESCE(NULLIF(btrim(succ.title_override), ''), succ.title) END,
       CASE WHEN mf.outcome = 'succeeded' THEN mf.final_object->'location'->>'canonical_path' END,
       mf.canonical_path, mf.collision, mf.plan_kind, mf.plan_reason_code,
       op.collision_policy, op.displaced_state
FROM acquisition_files mf
LEFT JOIN LATERAL (
    SELECT o.id, o.state, o.operation, o.failure_code, o.collision_policy,
           o.publication_step_checkpoint->'publish_target'->'proof'->'displaced'->>'state' AS displaced_state
    FROM file_operations o
    WHERE o.acquisition_id = mf.acquisition_id AND o.file_ref = mf.file_ref
    ORDER BY o.created_at DESC, o.id DESC
    LIMIT 1
) op ON TRUE
-- A terminally failed placement handoff of the completed operation; the
-- record's import retry reopens it.
LEFT JOIN placement_obligations pf ON pf.file_operation_id = op.id AND op.state = 'completed' AND pf.state = 'failed'
-- The placement this file's operation produced, once it ended by a quality
-- replacement (superseded) or another file took its path (displaced).
LEFT JOIN placements pl ON pl.source_evidence_type = 'file_operation' AND pl.source_owner_ref = op.id
    AND pl.state IN ('superseded', 'displaced')
LEFT JOIN managed_anime succ ON succ.id = pl.displaced_by_anime_id
WHERE mf.acquisition_id = $1 AND mf.ordinal > $2
ORDER BY mf.ordinal
LIMIT $3`, acquisitionID, after, limit+1)
	if err != nil {
		return AcquisitionFilePage{}, fmt.Errorf("list acquisition files: %w", err)
	}
	defer rows.Close()
	page := AcquisitionFilePage{AcquisitionID: acquisitionID, AsOf: asOf, Items: []AcquisitionFile{}}
	var placedAbsolute []string
	var placedIndex []int
	for rows.Next() {
		var file AcquisitionFile
		var relativePath string
		var selection, opState, opKind, opFailure sql.NullString
		var outcome, completion, effect, cleanup, deliveryReason sql.NullString
		var handoffState, handoffReason sql.NullString
		var opID sql.NullString
		var failedAttempts sql.NullInt64
		var failedReason sql.NullString
		var endState, endCause, successorAnime, successorTitle sql.NullString
		var placedPath sql.NullString
		var frozenTarget, frozenCollision, planKind, planReason, opCollision, displaced sql.NullString
		if err := rows.Scan(&file.Ordinal, &file.FileRef, &relativePath, &file.SizeBytes, &file.MediaKind,
			&file.Requirement, &selection, &opID, &opState, &opKind, &opFailure,
			&outcome, &completion, &effect, &cleanup, &deliveryReason, &handoffState, &handoffReason, &failedAttempts, &failedReason,
			&endState, &endCause, &successorAnime, &successorTitle, &placedPath,
			&frozenTarget, &frozenCollision, &planKind, &planReason, &opCollision, &displaced); err != nil {
			return AcquisitionFilePage{}, fmt.Errorf("scan acquisition file: %w", err)
		}
		if len(page.Items) == limit {
			last := page.Items[len(page.Items)-1]
			page.NextCursor = ptr(encodeCursor(fileCursor{Version: 1, Root: acquisitionID, After: last.Ordinal}))
			break
		}
		file.RelativePath = displayValue(&relativePath)
		file.SelectionReasonCode = nullableStringPointer(selection)
		if placedPath.Valid && placedPath.String != "" {
			placedAbsolute = append(placedAbsolute, placedPath.String)
			placedIndex = append(placedIndex, len(page.Items))
		}
		file.CollisionOutcome = acquisitionCollisionOutcome(acquisitionCollisionFacts{
			FrozenCollision: frozenCollision.String, OperationCollision: opCollision.String,
			PlanKind: planKind.String, PlanReasonCode: planReason.String, OperationFailure: opFailure.String,
			DeliveryOutcome: outcome.String, CompletionMode: completion.String, DeliveryReasonCode: deliveryReason.String,
			FrozenTarget: frozenTarget.String, FinalPath: placedPath.String, DisplacedState: displaced.String,
		})
		if opState.Valid {
			file.Operation = &AcquisitionFileOperation{State: opState.String, Operation: opKind.String, ReasonCode: nullableStringPointer(opFailure)}
		}
		if outcome.Valid {
			file.Delivery = &AcquisitionFileDelivery{Outcome: outcome.String, CompletionMode: nullableStringPointer(completion),
				EffectClass: effect.String, SourceCleanup: cleanup.String, ReasonCode: nullableStringPointer(deliveryReason)}
		}
		if handoffState.Valid {
			file.Handoff = &AcquisitionFileHandoff{State: handoffState.String, ReasonCode: nullableStringPointer(handoffReason)}
		}
		if failedAttempts.Valid {
			file.PlacementFailure = &AcquisitionFilePlacementFailure{Attempts: failedAttempts.Int64, ReasonCode: nullableStringPointer(failedReason)}
		}
		if endState.Valid {
			file.PlacementEnd = &AcquisitionFilePlacementEnd{State: endState.String, Cause: nullableStringPointer(endCause)}
			if successorAnime.Valid {
				file.PlacementEnd.Successor = &AcquisitionAnimeLink{ManagedAnimeID: successorAnime.String, Title: successorTitle.String}
			}
		}
		page.Items = append(page.Items, file)
	}
	if err := rows.Err(); err != nil {
		return AcquisitionFilePage{}, fmt.Errorf("iterate acquisition files: %w", err)
	}
	rows.Close()
	if len(placedAbsolute) > 0 {
		// The placed path is shown relative to the library: the display root
		// of the anime's active files (the per-anime files list's notion of
		// root) together with this page's placed paths.
		libraryPaths, err := animeLibraryPaths(ctx, tx, animeRef.String)
		if err != nil {
			return AcquisitionFilePage{}, err
		}
		root := displaypath.Root(append(libraryPaths, placedAbsolute...))
		for i, at := range placedIndex {
			if at >= len(page.Items) {
				continue
			}
			placed := libraryRelativeDisplay(root, placedAbsolute[i])
			page.Items[at].PlacedPath = &placed
		}
	}
	return page, nil
}

// animeLibraryPaths are the current paths of the anime's active verified
// placements and their active companions ("" anime = none).
func animeLibraryPaths(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, animeID string) ([]string, error) {
	if strings.TrimSpace(animeID) == "" {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `
SELECT p.physical_path FROM placements p
WHERE p.managed_anime_id = $1 AND p.state = 'active' AND p.verification_state = 'verified'
UNION ALL
SELECT c.current_path FROM placement_companions c
JOIN placements p ON p.id = c.placement_id AND p.state = 'active' AND p.verification_state = 'verified'
WHERE c.managed_anime_id = $1 AND c.state = 'active'
LIMIT 2000`, animeID)
	if err != nil {
		return nil, fmt.Errorf("read anime library paths: %w", err)
	}
	defer rows.Close()
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan anime library path: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}
