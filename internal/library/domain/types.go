package domain

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

type PlacementState string

const (
	PlacementPlanned   PlacementState = "planned"
	PlacementExecuting PlacementState = "executing"
	PlacementPlaced    PlacementState = "placed"
	PlacementObserved  PlacementState = "observed"
	PlacementBlocked   PlacementState = "blocked"
	PlacementFailed    PlacementState = "failed"
)

type OperationKind string

const (
	OperationCopy     OperationKind = "copy"
	OperationMove     OperationKind = "move"
	OperationHardlink OperationKind = "hardlink"
	OperationNoop     OperationKind = "noop"
)

type OperationIntent string

const (
	IntentEnsurePresent OperationIntent = "ensure_present"
	IntentReplace       OperationIntent = "replace"
	IntentExplicit      OperationIntent = "explicit"
)

const FileErrorCodeTargetOccupied = "TARGET_OCCUPIED"

type CollisionPolicy string

const (
	CollisionVersion   CollisionPolicy = "version"
	CollisionBlock     CollisionPolicy = "block"
	CollisionOverwrite CollisionPolicy = "overwrite"
)

type LibraryProfile struct {
	ID              string
	Name            string
	CollisionPolicy CollisionPolicy
	Operation       OperationKind
	State           string
	Version         int64
}

// LibraryConfig is the typed singleton library target used by the target
// automatic placement path. LibraryProfile remains a compatibility shape for
// historical manual/import APIs; automatic work must not select among named
// profiles.
type LibraryConfig struct {
	ID              string
	CollisionPolicy CollisionPolicy
	Operation       OperationKind
	Version         int64
}

type OperationState string

const (
	OperationPlanned   OperationState = "planned"
	OperationExecuting OperationState = "executing"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
	OperationSkipped   OperationState = "skipped"
)

type PlacementPlan struct {
	ID                    string
	ArtifactID            string
	NamingRenderID        string
	LibraryProfileID      string
	LibraryProfileVersion int64
	TargetPath            string
	CollisionPolicy       CollisionPolicy
	EpisodeRef            string
	EpisodeNumber         *int
	State                 PlacementState
	TraceID               string
	Version               int64
	Gates                 []PlacementGate
	FailureCode           string
	FailureReason         string
}

type PlacementGate struct {
	Code           string `json:"code"`
	State          string `json:"state"`
	Message        string `json:"message"`
	Impact         string `json:"impact,omitempty"`
	RecoveryAction string `json:"recovery_action,omitempty"`
}

type FileOperation struct {
	ID                string
	PlacementPlanID   string
	Operation         OperationKind
	Intent            OperationIntent
	SourcePath        string
	TargetPath        string
	State             OperationState
	SourceFingerprint string
	// SourceSizeKnown distinguishes an explicit zero-byte fact from legacy
	// hash-only operations whose SourceSizeBytes remains at its zero value.
	SourceSizeBytes   int64
	SourceSizeKnown   bool
	ExpectedSizeBytes int64
	ExpectedSizeKnown bool
	TargetFingerprint string
	TargetSizeBytes   int64
	CollisionPolicy   CollisionPolicy
	OverwriteRecovery *OverwriteRecovery
	IdempotencyKey    string
	FencingToken      int64
	LeaseExpiresAt    *time.Time
	Version           int64
	FailureCode       string
	FailureReason     string
	// Parent identities are frozen by authorization and supplied to every *at
	// filesystem mutation. They are durable recovery facts when persisted in an
	// overwrite checkpoint, and execution-local facts for non-overwrite work.
	SourceParentIdentity FileIdentity
	TargetParentIdentity FileIdentity
}

type FileIdentity struct {
	Device          uint64 `json:"device"`
	Inode           uint64 `json:"inode"`
	GenerationNanos int64  `json:"generation_nanos,omitempty"`
}

type FileObservation struct {
	Fingerprint string
	SizeBytes   int64
	Identity    FileIdentity
	// HandleEvidence is an opaque kernel object handle when the filesystem
	// exposes one. It is optional and never part of FileIdentity.
	HandleEvidence *HandleEvidence `json:"handle_evidence,omitempty"`
	// ChangeNanos is an observation-time mutation marker (ctime on Linux).
	// Zero is unavailable and remains compatible with legacy observations.
	ChangeNanos int64 `json:"change_nanos,omitempty"`
}

type HandleEvidence struct {
	MountID int64  `json:"mount_id"`
	Type    int32  `json:"type"`
	Opaque  string `json:"opaque"`
}

// RecoveryUncertainError marks an error where an external file effect may have
// happened but durable recovery evidence is not known to be complete. Owners
// must retry and reconcile this class instead of terminalizing it immediately.
type RecoveryUncertainError struct {
	Err error
}

func (e RecoveryUncertainError) Error() string {
	if e.Err == nil {
		return "file operation recovery is uncertain"
	}
	return e.Err.Error()
}

func (e RecoveryUncertainError) Unwrap() error         { return e.Err }
func (RecoveryUncertainError) Temporary() bool         { return true }
func (RecoveryUncertainError) RecoveryUncertain() bool { return true }

func MarkRecoveryUncertain(err error) error {
	if err == nil {
		return nil
	}
	return RecoveryUncertainError{Err: err}
}

type OverwriteStage string

const (
	OverwriteStagePrepared  OverwriteStage = "prepared"
	OverwriteStageStaged    OverwriteStage = "staged"
	OverwriteStageExchanged OverwriteStage = "exchanged"
	OverwriteStageRestored  OverwriteStage = "restored"
	OverwriteStageBlocked   OverwriteStage = "blocked"
	OverwriteStageAborted   OverwriteStage = "aborted"
	OverwriteStageCommitted OverwriteStage = "committed"
)

// OverwriteRecovery freezes the paths and observations needed to resume or
// reconcile one operation-specific atomic overwrite without guessing.
type OverwriteRecovery struct {
	Stage                OverwriteStage
	CanonicalRoot        string
	CanonicalSource      string
	CanonicalTarget      string
	StagingPath          string
	SourceParentIdentity FileIdentity `json:"source_parent_identity"`
	TargetParentIdentity FileIdentity `json:"target_parent_identity"`
	SourceObservation    FileObservation
	TargetObservation    FileObservation
	StagedObservation    FileObservation
	DisplacedObservation FileObservation
	DisplacedNonRegular  bool
	DisplacedType        string       `json:"displaced_type,omitempty"`
	DisplacedIdentity    FileIdentity `json:"displaced_identity,omitempty"`
	BlockedReason        string       `json:"blocked_reason,omitempty"`
	// ForeignEvidence records a regular external object observed during final
	// verification. It is retained in an operation-private quarantine so a
	// later recovery can return a deterministic collision instead of retrying
	// the same unresolved staging path forever.
	ForeignEvidencePath string          `json:"foreign_evidence_path,omitempty"`
	ForeignEvidence     FileObservation `json:"foreign_evidence,omitempty"`
	// ForeignNonRegularEvidence is Lstat-only evidence. Unknown non-regular
	// objects are never hashed, followed, or deleted by recovery.
	ForeignNonRegularEvidence bool         `json:"foreign_non_regular_evidence,omitempty"`
	ForeignEvidenceType       string       `json:"foreign_evidence_type,omitempty"`
	ForeignEvidenceIdentity   FileIdentity `json:"foreign_evidence_identity,omitempty"`
}

type FileOperationCheckpoint func(context.Context, FileOperation, OverwriteRecovery) (FileOperation, error)

type PlacementProblem struct {
	Code          string   `json:"code"`
	Summary       string   `json:"summary"`
	Detail        string   `json:"detail"`
	RepairActions []string `json:"repair_actions"`
}

type LocalMediaFact struct {
	State             string   `json:"state"`
	Value             any      `json:"value,omitempty"`
	Source            string   `json:"source"`
	FactRef           string   `json:"fact_ref"`
	DefinitionVersion string   `json:"definition_version"`
	Diagnostics       []string `json:"diagnostics,omitempty"`
}

type LocalMediaFactsSnapshot struct {
	ID               string
	ArtifactID       string
	Path             string
	Fingerprint      string
	SizeBytes        int64
	Version          string
	InspectorVersion string
	Values           map[string]LocalMediaFact
	InspectedAt      time.Time
}

type ActiveEpisodePlacement struct {
	ID              string
	PlacementPlanID string
	ArtifactID      string
	EpisodeRef      string
	EpisodeNumber   *int
	TargetPath      string
	State           string
	ObservationID   string
	Version         int64
}

type SubtitleInput struct {
	SourcePath        string
	SourceFingerprint string
	Language          string
}

type SubtitlePlacement struct {
	ID                string
	PlacementPlanID   string
	OperationID       string
	Language          string
	SourcePath        string
	TargetPath        string
	SourceFingerprint string
	TargetFingerprint string
	State             string
	ObservedAt        *time.Time
	Version           int64
}

type Observation struct {
	ID              string
	PlacementPlanID string
	ObservedPath    string
	Fingerprint     string
	SizeBytes       int64
	ObservedAt      time.Time
	Source          string
}

// ScanScope is deliberately relative to a LibraryProfile root for full and
// targeted scans. placement_owned bypasses enumeration entirely: the caller
// supplies explicit absolute locator paths (owned Placement current paths and
// bounded source provenance) and the scanner only observes those files.
type ScanScopeKind string

const (
	ScanScopeFull           ScanScopeKind = "full"
	ScanScopeTargeted       ScanScopeKind = "targeted"
	ScanScopePlacementOwned ScanScopeKind = "placement_owned"
)

type ScanScope struct {
	Kind         ScanScopeKind `json:"kind"`
	RelativePath string        `json:"relative_path,omitempty"`
}

func (s ScanScope) Valid() bool {
	switch s.Kind {
	case ScanScopeFull:
		return s.RelativePath == ""
	case ScanScopeTargeted:
		if strings.TrimSpace(s.RelativePath) == "" || filepath.IsAbs(s.RelativePath) {
			return false
		}
		clean := filepath.Clean(s.RelativePath)
		return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
	case ScanScopePlacementOwned:
		// Paths are validated per-entry by the adapter; the scope itself only
		// declares the mode.
		return s.RelativePath == ""
	default:
		return false
	}
}

type ScanCoverageState string

const (
	ScanCoverageTargetedComplete ScanCoverageState = "targeted_complete"
	ScanCoverageComplete         ScanCoverageState = "complete"
	ScanCoveragePartial          ScanCoverageState = "partial"
	ScanCoverageFailed           ScanCoverageState = "failed"
	ScanCoverageStale            ScanCoverageState = "stale"
)

type ScanCoverage struct {
	ID             string
	ManagedAnimeID string
	IdempotencyKey string
	Scope          ScanScope
	ScannerVersion string
	State          ScanCoverageState
	StartedAt      time.Time
	CompletedAt    *time.Time
	ErrorMessage   string
	Version        int64
}

type ScanRequest struct {
	// TreeTop is the root the scan is scoped to: the directory the work's own
	// files live in. A placement-owned scan (ScanScopePlacementOwned) observes
	// Paths instead and uses this only as the scope identity.
	TreeTop string
	Scope   ScanScope
	// Paths is used only by ScanScopePlacementOwned: explicit absolute locator
	// paths (owned Placement current paths and bounded provenance fallbacks).
	// Absent files are skipped, never errors.
	Paths []string
}

type ScanResult struct {
	// TreeTop is the scanned tree's top: the directory the work's own files
	// live in.
	TreeTop        string
	ScannerVersion string
	Files          []ScanFile
}

type ScanFile struct {
	Path        string
	Fingerprint string
	SizeBytes   int64
	EpisodeKey  string
	SpecialKind string
	ObservedAt  time.Time
}

type FileObservationFact struct {
	ID             string
	CoverageID     string
	Path           string
	Fingerprint    string
	SizeBytes      int64
	EpisodeKey     string
	SpecialKind    string
	ScannerVersion string
	ObservedAt     time.Time
}

// ReconciliationFacts is an immutable snapshot prepared by Library for the
// Reconciliation owner. It contains only persisted placement and scan facts;
// callers cannot supply paths, fingerprints, or filesystem operations.
type ReconciliationFacts struct {
	ManagedAnimeID    string
	PlacementPlanID   string
	ArtifactID        string
	EpisodeKey        string
	TargetPath        string
	TargetRelative    string
	SourcePath        string
	SourceFingerprint string
	Observed          []ReconciliationObservedFile
	Coverage          ReconciliationCoverage
	Generation        int64
}

type ReconciliationObservedFile struct {
	Path        string
	Fingerprint string
	EpisodeKey  string
	ObservedAt  time.Time
}

type ReconciliationCoverage struct {
	ID        string
	State     ScanCoverageState
	Scope     ScanScope
	Completed bool
	AsOf      time.Time
	Version   int64
}

type ManagedArtifact struct {
	ID                string
	Path              string
	Fingerprint       string
	VerificationState string
	DownloadState     string
	ObservedAt        time.Time
	AllowedActions    []string
}

type ManagedPlacement struct {
	ID                  string
	ArtifactID          string
	TargetPath          string
	State               string
	OperationID         string
	Operation           OperationKind
	OperationState      OperationState
	ObservedPath        string
	ObservedFingerprint string
	IsActive            bool
	AllowedActions      []string
}

type ManagedScan struct {
	ID               string
	Scope            ScanScope
	ScannerVersion   string
	State            ScanCoverageState
	ObservationCount int
	StartedAt        time.Time
	CompletedAt      *time.Time
	ErrorMessage     string
}

type ManagedReconciliation struct {
	ID                string
	PlacementPlanID   string
	ArtifactID        string
	Kind              string
	State             string
	SourcePath        string
	TargetPath        string
	SourceFingerprint string
	TargetFingerprint string
	Reason            string
	Version           int64
	AllowedActions    []string
}

type ManagedExclusion struct {
	ID             string
	ReleaseID      string
	Provider       string
	Scope          string
	Reason         string
	ActorID        string
	PolicyVersion  string
	State          string
	ExpiresAt      *time.Time
	Version        int64
	AllowedActions []string
}

type ManagedReplacement struct {
	ID                   string
	EpisodeRef           string
	OldPlacementID       string
	CandidatePlacementID string
	Verdict              string
	State                string
	PolicyVersion        int64
	Reason               string
	Version              int64
	AllowedActions       []string
}

type ManagedCleanup struct {
	ID             string
	SettlementID   string
	PlacementID    string
	TargetPath     string
	Fingerprint    string
	State          string
	Reason         string
	Version        int64
	AllowedActions []string
}

type FileManagementProblem struct {
	Code          string
	SubjectID     string
	State         string
	Detail        string
	RepairActions []string
}

type FileManagement struct {
	ManagedAnimeID    string
	SubscriptionState string
	Artifacts         []ManagedArtifact
	Placements        []ManagedPlacement
	Scans             []ManagedScan
	Observations      []FileObservationFact
	Reconciliations   []ManagedReconciliation
	Exclusions        []ManagedExclusion
	Replacements      []ManagedReplacement
	Cleanups          []ManagedCleanup
	Problems          []FileManagementProblem
	AllowedActions    []string
	AsOf              time.Time
}
