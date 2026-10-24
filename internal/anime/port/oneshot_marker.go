package port

import (
	"context"
	"encoding/json"
	"time"

	"bansoko/internal/persistence/txscope"
)

// OneShotMarkerRecord is the application-neutral row representation for the
// anime-owned one_shot_acquisitions marker. The persistence adapter maps its
// generated sqlc row into this value before it crosses the adapter boundary.
type OneShotMarkerRecord struct {
	ID             string
	ManagedAnimeID string
	SubscriptionID string
	RSSSourceID    string
	ReleaseID      string
	// DownloadTaskID is nil when the finite command adopted an already
	// satisfied exact target set and created no download chain.
	DownloadTaskID          *string
	CorrelationID           string
	Request_Idempotency_Key string
	RequestHash             string
	ResultSnapshot          json.RawMessage
	InputSnapshot           json.RawMessage
	State                   string
	TerminalOutcome         *string
	TerminalReasonCode      *string
	TerminalReason          *string
	TerminalAt              *time.Time
	RetentionUntil          *time.Time
	// RetentionDays is required only for an already-terminal insert (a marker
	// whose chain completed at admission); the retention deadline is computed
	// from database time, never from a caller clock.
	RetentionDays      int
	AdmissionEpoch     int64
	ActiveEffectCount  int
	ActiveEffectLeases json.RawMessage
	Version            int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// CurrentAttemptID names the execution attempt whose terminal evidence the
	// marker's terminal projection mirrors (the attempts model). Empty
	// only for legacy rows before their backfilled attempt pointer resolves.
	CurrentAttemptID string
}

// OneShotAttemptStage is the typed pipeline stage an attempt resumed or failed
// at. Empty means the initial attempt (no stage resume).
type OneShotAttemptStage string

const (
	OneShotStageDownload      OneShotAttemptStage = "download"
	OneShotStageArtifact      OneShotAttemptStage = "artifact"
	OneShotStagePlanning      OneShotAttemptStage = "planning"
	OneShotStageFileExecution OneShotAttemptStage = "file_execution"
	OneShotStagePlacement     OneShotAttemptStage = "placement"
)

// OneShotAttemptMode distinguishes the first attempt from the retired manual
// recoveries. The retry and replan values remain readable on historical rows;
// nothing creates them any more.
type OneShotAttemptMode string

const (
	OneShotAttemptModeInitial OneShotAttemptMode = "initial"
	OneShotAttemptModeRetry   OneShotAttemptMode = "retry"
	OneShotAttemptModeReplan  OneShotAttemptMode = "replan"
)

// OneShotAttemptRecord is one immutable bounded execution attempt of a
// one-shot acquisition. Terminal rows never return to running; the marker's
// terminal projection mirrors exactly the current attempt.
type OneShotAttemptRecord struct {
	ID              string
	AcquisitionID   string
	Sequence        int64
	PreviousAttempt *string
	Mode            OneShotAttemptMode
	ResumeStage     *OneShotAttemptStage
	InputSnapshot   json.RawMessage
	InputDigest     string
	State           string
	FailedStage     *OneShotAttemptStage
	OutcomeCode     *string
	OutcomeReason   *string
	TerminalAt      *time.Time
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// RequestIdempotencyKey and RequestHash were the identity of the retired
	// manual recovery commands. Both are nil for admission-created attempts.
	RequestIdempotencyKey *string
	RequestHash           *string
}

// OneShotAttemptRepository is the typed persistence surface for execution
// attempts. Transactional methods receive the caller-owned scope and never
// begin, commit, or roll back.
type OneShotAttemptRepository interface {
	// InsertAttemptTx persists one new attempt row. The per-acquisition
	// running-attempt unique index rejects a second running attempt.
	InsertAttemptTx(ctx context.Context, scope txscope.Tx, record OneShotAttemptRecord) error
	// GetAttemptByID reads one attempt row.
	GetAttemptByID(ctx context.Context, attemptID string) (*OneShotAttemptRecord, error)
	// TerminalizeAttemptTx moves the running attempt to its one-way terminal
	// state under a CAS version check. The attempt must be running; a lost
	// race is a typed conflict.
	TerminalizeAttemptTx(ctx context.Context, scope txscope.Tx, attemptID string, expectedVersion int64, outcomeCode, outcomeReason string, terminalAt time.Time) (int64, error)
}

// OneShotMarkerRepository is the complete typed persistence surface needed by
// anime one-shot admission, replay, reads, and unsubscribe protection.
// Transactional methods receive the caller-owned scope; implementations unwrap
// it only at the persistence boundary and never begin, commit, or roll back.
type OneShotMarkerRepository interface {
	Insert(ctx context.Context, scope txscope.Tx, record OneShotMarkerRecord) error
	GetByRequestIdempotencyKey(ctx context.Context, key string) (*OneShotMarkerRecord, error)
	LockByRequestIdempotencyKey(ctx context.Context, scope txscope.Tx, key string) (*OneShotMarkerRecord, error)
	GetByID(ctx context.Context, id string) (*OneShotMarkerRecord, error)
	LockStateByManagedAnimeID(ctx context.Context, scope txscope.Tx, managedAnimeID string) (state string, found bool, err error)
	// InsertAttemptTx persists one execution attempt row of this acquisition.
	// The per-acquisition running-attempt unique index rejects a second
	// running attempt.
	InsertAttemptTx(ctx context.Context, scope txscope.Tx, record OneShotAttemptRecord) error
	// GetAttemptByID reads one attempt row; sql.ErrNoRows when missing.
	GetAttemptByID(ctx context.Context, attemptID string) (*OneShotAttemptRecord, error)
	// GetAttemptForAcquisition reads one attempt scoped to its acquisition so a
	// cross-acquisition read cannot resolve. sql.ErrNoRows when it does not
	// belong to the acquisition.
	GetAttemptForAcquisition(ctx context.Context, acquisitionID, attemptID string) (*OneShotAttemptRecord, error)
	// ListAttemptsByAcquisition reads one sequence-DESC keyset page. A nil
	// beforeSequence starts from the newest attempt.
	ListAttemptsByAcquisition(ctx context.Context, acquisitionID string, beforeSequence *int64, limit int) ([]OneShotAttemptRecord, error)
}
