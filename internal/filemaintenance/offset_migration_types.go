package filemaintenance

import (
	"encoding/json"
	"time"
)

// OffsetMigrationScopeKind identifies the owner whose offset changed. The RSS
// link is the only offset owner.
type OffsetMigrationScopeKind string

const OffsetMigrationScopeRSSSource OffsetMigrationScopeKind = "rss_source"

// OffsetMigrationGenerationState is the durable scan-generation lifecycle.
type OffsetMigrationGenerationState string

const (
	OffsetMigrationGenerationPending             OffsetMigrationGenerationState = "pending"
	OffsetMigrationGenerationPlanning            OffsetMigrationGenerationState = "planning"
	OffsetMigrationGenerationReady               OffsetMigrationGenerationState = "ready"
	OffsetMigrationGenerationRunning             OffsetMigrationGenerationState = "running"
	OffsetMigrationGenerationCompleted           OffsetMigrationGenerationState = "completed"
	OffsetMigrationGenerationCompletedWithBlocks OffsetMigrationGenerationState = "completed_with_blocks"
	OffsetMigrationGenerationFailed              OffsetMigrationGenerationState = "failed"
	OffsetMigrationGenerationSuperseded          OffsetMigrationGenerationState = "superseded"
)

// IsTerminal reports whether the generation can no longer make progress.
func (state OffsetMigrationGenerationState) IsTerminal() bool {
	switch state {
	case OffsetMigrationGenerationCompleted,
		OffsetMigrationGenerationCompletedWithBlocks,
		OffsetMigrationGenerationFailed,
		OffsetMigrationGenerationSuperseded:
		return true
	default:
		return false
	}
}

// OffsetMigrationOperationState is the durable per-placement lifecycle.
type OffsetMigrationOperationState string

const (
	OffsetMigrationOperationPlanned       OffsetMigrationOperationState = "planned"
	OffsetMigrationOperationReady         OffsetMigrationOperationState = "ready"
	OffsetMigrationOperationRunning       OffsetMigrationOperationState = "running"
	OffsetMigrationOperationWaitingRetry  OffsetMigrationOperationState = "waiting_retry"
	OffsetMigrationOperationEffectUnknown OffsetMigrationOperationState = "effect_unknown"
	OffsetMigrationOperationApplied       OffsetMigrationOperationState = "applied"
	OffsetMigrationOperationBlocked       OffsetMigrationOperationState = "blocked"
	OffsetMigrationOperationFailed        OffsetMigrationOperationState = "failed"
	OffsetMigrationOperationSuperseded    OffsetMigrationOperationState = "superseded"
)

// IsTerminal reports whether the operation has reached a final owner state.
func (state OffsetMigrationOperationState) IsTerminal() bool {
	switch state {
	case OffsetMigrationOperationApplied,
		OffsetMigrationOperationBlocked,
		OffsetMigrationOperationFailed,
		OffsetMigrationOperationSuperseded:
		return true
	default:
		return false
	}
}

// IsExecutable reports whether the normal executor may claim this operation.
// Recovery owns running and effect_unknown; retry admission owns waiting_retry.
func (state OffsetMigrationOperationState) IsExecutable() bool {
	return state == OffsetMigrationOperationReady
}

// OffsetMigrationGeneration is the durable owner for one offset policy version.
type OffsetMigrationGeneration struct {
	ID              string
	ManagedAnimeID  string
	ScopeKind       OffsetMigrationScopeKind
	ScopeOwnerID    string
	PolicyVersion   int64
	OldOffset       int
	NewOffset       int
	State           OffsetMigrationGenerationState
	PlannedCount    int
	BlockedCount    int
	AppliedCount    int
	SupersededCount int
	ReasonCode      string
	Reason          string
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

// OffsetMigrationOperation is one frozen placement evolution operation.
type OffsetMigrationOperation struct {
	ID                  string
	GenerationID        string
	ManagedAnimeID      string
	ScopeKind           OffsetMigrationScopeKind
	ScopeOwnerID        string
	PlacementID         string
	PlacementVersion    int64
	PolicyVersion       int64
	SourcePath          string
	SourceFingerprint   string
	Fingerprint         json.RawMessage
	MediaFactSnapshotID string
	MediaFactFileRef    string
	RSSSourceID         string
	SourceEpisodeOffset *int
	MediaKind           string
	BoundSeason         *int
	OldIdentity         json.RawMessage
	NewIdentity         json.RawMessage
	OldCoverageKey      string
	NewCoverageKey      string
	OldTargetPath       string
	NewTargetPath       string
	TargetContract      json.RawMessage
	State               OffsetMigrationOperationState
	Attempt             json.RawMessage
	ReasonCode          string
	FailureReason       string
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CompletedAt         *time.Time
}
