package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sqlc-dev/pqtype"

	generated "bansoko/internal/anime/adapter/persistence/generated"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/persistence/txscope"
)

// Repository is the PostgreSQL implementation of the anime one-shot marker
// port. It deliberately owns all marker SQL and generated sqlc types.
const maxRetentionBatchSize = 500

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) PurgeEligibleAnimeConfigCandidates(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("anime candidate cleanup repository is nil")
	}
	if batchSize < 1 || batchSize > maxRetentionBatchSize {
		return 0, fmt.Errorf("anime candidate cleanup batch size must be between 1 and %d", maxRetentionBatchSize)
	}
	tx := scope.SQLTx()
	if tx == nil {
		return 0, fmt.Errorf("purge eligible anime candidates requires a transaction")
	}
	result, err := generated.New(tx).PurgeEligibleAnimeConfigCandidates(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge eligible anime candidates: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read purged anime candidate count: %w", err)
	}
	return count, nil
}

func (r *Repository) Insert(ctx context.Context, scope txscope.Tx, record animeport.OneShotMarkerRecord) error {
	tx := scope.SQLTx()
	if tx == nil {
		return fmt.Errorf("one-shot marker insert requires a transaction")
	}
	leases := record.ActiveEffectLeases
	if len(leases) == 0 {
		leases = json.RawMessage("[]")
	}
	queries := generated.New(tx)
	if record.State == "running" {
		if err := queries.InsertOneShotMarker(ctx, generated.InsertOneShotMarkerParams{
			ID: record.ID, ManagedAnimeID: record.ManagedAnimeID, SubscriptionID: record.SubscriptionID,
			RssSourceID: record.RSSSourceID, ReleaseID: record.ReleaseID, DownloadTaskID: nullableStringValue(record.DownloadTaskID),
			CorrelationID: record.CorrelationID, RequestIdempotencyKey: record.Request_Idempotency_Key,
			RequestHash: record.RequestHash, ResultSnapshot: record.ResultSnapshot, InputSnapshot: nullableRawMessage(record.InputSnapshot), State: record.State,
			AdmissionEpoch: record.AdmissionEpoch, ActiveEffectCount: int32(record.ActiveEffectCount),
			ActiveEffectLeases: leases, Version: record.Version, CurrentAttemptID: nullableStringValue2(record.CurrentAttemptID),
		}); err != nil {
			return fmt.Errorf("insert one-shot marker: %w", err)
		}
		return nil
	}
	// An already-terminal marker: the finite chain completed at admission (the
	// exact target set existed) and no download chain was created. The
	// retention deadline is computed from database time in the query.
	if record.RetentionDays < 1 {
		return fmt.Errorf("terminal one-shot marker insert requires a positive retention window")
	}
	if err := queries.InsertTerminalOneShotMarker(ctx, generated.InsertTerminalOneShotMarkerParams{
		ID: record.ID, ManagedAnimeID: record.ManagedAnimeID, SubscriptionID: record.SubscriptionID,
		RssSourceID: record.RSSSourceID, ReleaseID: record.ReleaseID, DownloadTaskID: nullableStringValue(record.DownloadTaskID),
		CorrelationID: record.CorrelationID, RequestIdempotencyKey: record.Request_Idempotency_Key,
		RequestHash: record.RequestHash, ResultSnapshot: record.ResultSnapshot, InputSnapshot: nullableRawMessage(record.InputSnapshot), State: record.State,
		TerminalOutcome: nullableStringValue(record.TerminalOutcome), TerminalReasonCode: nullableStringValue(record.TerminalReasonCode),
		TerminalReason: nullableStringValue(record.TerminalReason), RetentionDays: int32(record.RetentionDays),
		AdmissionEpoch: record.AdmissionEpoch, ActiveEffectCount: int32(record.ActiveEffectCount),
		ActiveEffectLeases: leases, Version: record.Version, CurrentAttemptID: nullableStringValue2(record.CurrentAttemptID),
	}); err != nil {
		return fmt.Errorf("insert terminal one-shot marker: %w", err)
	}
	return nil
}

func nullableStringValue2(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullableStringValue(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func (r *Repository) GetByRequestIdempotencyKey(ctx context.Context, key string) (*animeport.OneShotMarkerRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("one-shot marker database is nil")
	}
	row, err := generated.New(r.db).GetOneShotMarkerByRequestIdempotencyKey(ctx, key)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return markerOrError(oneShotMarkerRow{
		ID: row.ID, ManagedAnimeID: row.ManagedAnimeID, SubscriptionID: row.SubscriptionID,
		RSSSourceID: row.RssSourceID, ReleaseID: row.ReleaseID, DownloadTaskID: row.DownloadTaskID,
		CorrelationID: row.CorrelationID, RequestIdempotencyKey: row.RequestIdempotencyKey,
		RequestHash: row.RequestHash, ResultSnapshot: row.ResultSnapshot, InputSnapshot: row.InputSnapshot,
		State: row.State, TerminalOutcome: row.TerminalOutcome, TerminalReasonCode: row.TerminalReasonCode,
		TerminalReason: row.TerminalReason, TerminalAt: row.TerminalAt, RetentionUntil: row.RetentionUntil,
		AdmissionEpoch: row.AdmissionEpoch, ActiveEffectCount: row.ActiveEffectCount,
		ActiveEffectLeases: row.ActiveEffectLeases, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, err, "get one-shot marker by request idempotency key")
}

func (r *Repository) LockByRequestIdempotencyKey(ctx context.Context, scope txscope.Tx, key string) (*animeport.OneShotMarkerRecord, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return nil, fmt.Errorf("one-shot marker lock requires a transaction")
	}
	row, err := generated.New(tx).LockOneShotMarkerByRequestIdempotencyKey(ctx, key)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return markerOrError(oneShotMarkerRow{
		ID: row.ID, ManagedAnimeID: row.ManagedAnimeID, SubscriptionID: row.SubscriptionID,
		RSSSourceID: row.RssSourceID, ReleaseID: row.ReleaseID, DownloadTaskID: row.DownloadTaskID,
		CorrelationID: row.CorrelationID, RequestIdempotencyKey: row.RequestIdempotencyKey,
		RequestHash: row.RequestHash, ResultSnapshot: row.ResultSnapshot, InputSnapshot: row.InputSnapshot,
		State: row.State, TerminalOutcome: row.TerminalOutcome, TerminalReasonCode: row.TerminalReasonCode,
		TerminalReason: row.TerminalReason, TerminalAt: row.TerminalAt, RetentionUntil: row.RetentionUntil,
		AdmissionEpoch: row.AdmissionEpoch, ActiveEffectCount: row.ActiveEffectCount,
		ActiveEffectLeases: row.ActiveEffectLeases, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		CurrentAttemptID: row.CurrentAttemptID,
	}, err, "lock one-shot marker by request idempotency key")
}

func (r *Repository) GetByID(ctx context.Context, id string) (*animeport.OneShotMarkerRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("one-shot marker database is nil")
	}
	row, err := generated.New(r.db).GetOneShotMarkerByID(ctx, id)
	return markerOrError(oneShotMarkerRow{
		ID: row.ID, ManagedAnimeID: row.ManagedAnimeID, SubscriptionID: row.SubscriptionID,
		RSSSourceID: row.RssSourceID, ReleaseID: row.ReleaseID, DownloadTaskID: row.DownloadTaskID,
		CorrelationID: row.CorrelationID, RequestIdempotencyKey: row.RequestIdempotencyKey,
		RequestHash: row.RequestHash, ResultSnapshot: row.ResultSnapshot, InputSnapshot: row.InputSnapshot,
		State: row.State, TerminalOutcome: row.TerminalOutcome, TerminalReasonCode: row.TerminalReasonCode,
		TerminalReason: row.TerminalReason, TerminalAt: row.TerminalAt, RetentionUntil: row.RetentionUntil,
		AdmissionEpoch: row.AdmissionEpoch, ActiveEffectCount: row.ActiveEffectCount,
		ActiveEffectLeases: row.ActiveEffectLeases, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		CurrentAttemptID: row.CurrentAttemptID,
	}, err, "get one-shot marker by id")
}

func (r *Repository) LockStateByManagedAnimeID(ctx context.Context, scope txscope.Tx, managedAnimeID string) (string, bool, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return "", false, fmt.Errorf("one-shot marker delete lock requires a transaction")
	}
	state, err := generated.New(tx).LockOneShotMarkerStateByManagedAnimeID(ctx, managedAnimeID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("lock one-shot acquisition marker for delete: %w", err)
	}
	return state, true, nil
}

type oneShotMarkerRow struct {
	ID                    string
	ManagedAnimeID        string
	SubscriptionID        string
	RSSSourceID           string
	ReleaseID             string
	DownloadTaskID        sql.NullString
	CorrelationID         string
	RequestIdempotencyKey string
	RequestHash           string
	ResultSnapshot        json.RawMessage
	InputSnapshot         pqtype.NullRawMessage
	State                 string
	TerminalOutcome       sql.NullString
	TerminalReasonCode    sql.NullString
	TerminalReason        sql.NullString
	TerminalAt            sql.NullTime
	RetentionUntil        sql.NullTime
	AdmissionEpoch        int64
	ActiveEffectCount     int32
	ActiveEffectLeases    json.RawMessage
	Version               int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	CurrentAttemptID      sql.NullString
}

// markerOrError converts the generated full-row shape to the port row and keeps
// sql.ErrNoRows intact for callers that distinguish a missing marker.
func markerOrError(row oneShotMarkerRow, err error, operation string) (*animeport.OneShotMarkerRecord, error) {
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return oneShotMarkerRecordFromRow(row), nil
}

func oneShotMarkerRecordFromRow(row oneShotMarkerRow) *animeport.OneShotMarkerRecord {
	record := &animeport.OneShotMarkerRecord{
		ID:                      row.ID,
		ManagedAnimeID:          row.ManagedAnimeID,
		SubscriptionID:          row.SubscriptionID,
		RSSSourceID:             row.RSSSourceID,
		ReleaseID:               row.ReleaseID,
		CorrelationID:           row.CorrelationID,
		Request_Idempotency_Key: row.RequestIdempotencyKey,
		RequestHash:             row.RequestHash,
		ResultSnapshot:          row.ResultSnapshot,
		InputSnapshot:           rawMessage(row.InputSnapshot),
		State:                   row.State,
		AdmissionEpoch:          row.AdmissionEpoch,
		ActiveEffectCount:       int(row.ActiveEffectCount),
		ActiveEffectLeases:      row.ActiveEffectLeases,
		Version:                 row.Version,
		CurrentAttemptID:        row.CurrentAttemptID.String,
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
	}
	record.DownloadTaskID = nullableString(row.DownloadTaskID)
	record.TerminalOutcome = nullableString(row.TerminalOutcome)
	record.TerminalReasonCode = nullableString(row.TerminalReasonCode)
	record.TerminalReason = nullableString(row.TerminalReason)
	if row.TerminalAt.Valid {
		value := row.TerminalAt.Time
		record.TerminalAt = &value
	}
	if row.RetentionUntil.Valid {
		value := row.RetentionUntil.Time
		record.RetentionUntil = &value
	}
	return record
}

func nullableRawMessage(value json.RawMessage) pqtype.NullRawMessage {
	if len(value) == 0 {
		return pqtype.NullRawMessage{}
	}
	return pqtype.NullRawMessage{RawMessage: value, Valid: true}
}

func rawMessage(value pqtype.NullRawMessage) json.RawMessage {
	if !value.Valid {
		return nil
	}
	return append(json.RawMessage(nil), value.RawMessage...)
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	cloned := value.String
	return &cloned
}

// --- one-shot execution attempts -------------------------------------------

func (r *Repository) InsertAttemptTx(ctx context.Context, scope txscope.Tx, record animeport.OneShotAttemptRecord) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("one-shot attempt database is nil")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return fmt.Errorf("one-shot attempt insert requires a transaction")
	}
	var previous sql.NullString
	if record.PreviousAttempt != nil {
		previous = sql.NullString{String: *record.PreviousAttempt, Valid: true}
	}
	var resumeStage sql.NullString
	if record.ResumeStage != nil {
		resumeStage = sql.NullString{String: string(*record.ResumeStage), Valid: true}
	}
	snapshot := record.InputSnapshot
	if len(snapshot) == 0 {
		snapshot = json.RawMessage("{}")
	}
	var failedStage sql.NullString
	if record.FailedStage != nil {
		failedStage = sql.NullString{String: string(*record.FailedStage), Valid: true}
	}
	if err := generated.New(tx).InsertOneShotAttempt(ctx, generated.InsertOneShotAttemptParams{
		ID: record.ID, AcquisitionID: record.AcquisitionID, Sequence: record.Sequence,
		PreviousAttemptID: previous, Mode: string(record.Mode), ResumeStage: resumeStage,
		InputSnapshot: snapshot, InputDigest: record.InputDigest, State: record.State,
		FailedStage:           failedStage,
		OutcomeCode:           nullableStringValue(record.OutcomeCode),
		OutcomeReason:         nullableStringValue(record.OutcomeReason),
		TerminalAt:            nullableTimeValue(record.TerminalAt),
		RequestIdempotencyKey: nullableStringValue(record.RequestIdempotencyKey),
		RequestHash:           nullableStringValue(record.RequestHash),
	}); err != nil {
		return fmt.Errorf("insert one-shot execution attempt: %w", err)
	}
	return nil
}

func nullableTimeValue(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}

func oneShotAttemptRecordFromGenerated(row generated.OneShotExecutionAttempts) *animeport.OneShotAttemptRecord {
	record := &animeport.OneShotAttemptRecord{
		ID: row.ID, AcquisitionID: row.AcquisitionID, Sequence: row.Sequence,
		Mode: animeport.OneShotAttemptMode(row.Mode), InputSnapshot: row.InputSnapshot,
		InputDigest: row.InputDigest, State: row.State, Version: row.Version,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.PreviousAttemptID.Valid {
		previous := row.PreviousAttemptID.String
		record.PreviousAttempt = &previous
	}
	if row.ResumeStage.Valid {
		stage := animeport.OneShotAttemptStage(row.ResumeStage.String)
		record.ResumeStage = &stage
	}
	if row.FailedStage.Valid {
		stage := animeport.OneShotAttemptStage(row.FailedStage.String)
		record.FailedStage = &stage
	}
	if row.OutcomeCode.Valid {
		code := row.OutcomeCode.String
		record.OutcomeCode = &code
	}
	if row.OutcomeReason.Valid {
		reason := row.OutcomeReason.String
		record.OutcomeReason = &reason
	}
	if row.TerminalAt.Valid {
		at := row.TerminalAt.Time
		record.TerminalAt = &at
	}
	if row.RequestIdempotencyKey.Valid {
		key := row.RequestIdempotencyKey.String
		record.RequestIdempotencyKey = &key
	}
	if row.RequestHash.Valid {
		hash := row.RequestHash.String
		record.RequestHash = &hash
	}
	return record
}

func (r *Repository) GetAttemptByID(ctx context.Context, attemptID string) (*animeport.OneShotAttemptRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("one-shot attempt database is nil")
	}
	row, err := generated.New(r.db).GetOneShotAttemptByID(ctx, attemptID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("get one-shot execution attempt: %w", err)
	}
	return oneShotAttemptRecordFromGenerated(row), nil
}

func (r *Repository) GetAttemptForAcquisition(ctx context.Context, acquisitionID, attemptID string) (*animeport.OneShotAttemptRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("one-shot attempt database is nil")
	}
	row, err := generated.New(r.db).GetOneShotAttemptForAcquisition(ctx, generated.GetOneShotAttemptForAcquisitionParams{
		ID: attemptID, AcquisitionID: acquisitionID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("get one-shot execution attempt for acquisition: %w", err)
	}
	return oneShotAttemptRecordFromGenerated(row), nil
}

func (r *Repository) ListAttemptsByAcquisition(ctx context.Context, acquisitionID string, beforeSequence *int64, limit int) ([]animeport.OneShotAttemptRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("one-shot attempt database is nil")
	}
	if limit < 1 {
		return nil, fmt.Errorf("one-shot attempt page size must be positive")
	}
	var before int64
	if beforeSequence != nil {
		before = *beforeSequence
	}
	rows, err := generated.New(r.db).ListOneShotAttemptsByAcquisition(ctx, generated.ListOneShotAttemptsByAcquisitionParams{
		AcquisitionID: acquisitionID, BeforeSequence: before, PageSize: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list one-shot execution attempts: %w", err)
	}
	records := make([]animeport.OneShotAttemptRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, *oneShotAttemptRecordFromGenerated(row))
	}
	return records, nil
}

// TerminalizeAttemptTx moves the running attempt to its one-way terminal state
// under a CAS version check. The failed stage is advisory provenance derived
// from the anchored chain by the caller.
func (r *Repository) TerminalizeAttemptTx(ctx context.Context, scope txscope.Tx, attemptID string, expectedVersion int64, failedStage *animeport.OneShotAttemptStage, outcomeCode, outcomeReason string, terminalAt time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("one-shot attempt database is nil")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return 0, fmt.Errorf("one-shot attempt terminalization requires a transaction")
	}
	var stage sql.NullString
	if failedStage != nil {
		stage = sql.NullString{String: string(*failedStage), Valid: true}
	}
	version, err := generated.New(tx).TerminalizeOneShotAttempt(ctx, generated.TerminalizeOneShotAttemptParams{
		ID:              attemptID,
		State:           "failed",
		FailedStage:     stage,
		OutcomeCode:     sql.NullString{String: outcomeCode, Valid: true},
		OutcomeReason:   sql.NullString{String: outcomeReason, Valid: true},
		TerminalAt:      sql.NullTime{Time: terminalAt, Valid: true},
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return 0, fmt.Errorf("terminalize one-shot execution attempt: %w", err)
	}
	return version, nil
}
