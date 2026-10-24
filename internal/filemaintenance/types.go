package filemaintenance

import (
	"encoding/json"
	"time"

	"bansoko/internal/parserpolicy"
	"bansoko/internal/qualityprofile"
	reconciliationapplication "bansoko/internal/reconciliation/application"
)

type PolicyInput struct {
	ExpectedVersion     int64
	Enabled             bool
	ScanIntervalSeconds *int
	AlignmentEnabled    *bool
	Safety              json.RawMessage
}

type PolicyView struct {
	ScopeKey            string
	Enabled             bool
	ScanIntervalSeconds *int
	AlignmentEnabled    *bool
	Safety              json.RawMessage
	Version             int64
}

type ReplanInput struct {
	ManagedAnimeID string
	Generation     int64
}

type OverviewView struct {
	Alignments      *[]AlignmentOperationView
	Cleanups        *[]CleanupOperationView
	CompanionIssues *[]CompanionIssueView
	Consumers       *[]ReconciliationConsumerStateView
	Decisions       *[]ReplacementDecisionView
	GlobalDefault   *RetentionPolicyDefaultView
	InstancePolicy  *InstanceRetentionPolicyView
	ManagedAnimeID  string
	Placements      *[]PlacementView
	PlacementIssues *[]PlacementIssueView
	Scan            *reconciliationapplication.ScanView
}

type ParserPolicyInput struct {
	ExpectedVersion int64
	Policy          parserpolicy.Policy
}

// ParserPolicyDefaultInput is the singleton creation-template save: the parser
// kind future instances freeze at creation. Version 0 creates the row.
type ParserPolicyDefaultInput struct {
	ExpectedVersion int64
	ParserKind      string
}

// ParserPolicyDefaultView is the singleton creation template. Version 0 means
// "never saved", in which case ParserKind is the versioned code default; it is
// never the version an instance freezes under.
type ParserPolicyDefaultView struct {
	ParserKind string
	Version    int64
	UpdatedAt  *time.Time
}

type PathDiffView struct {
	Differs      bool
	Disposition  string
	ExpectedPath *string
	FilePath     string
	// Companions are the video's registered companion sidecars observed by
	// the generation: each expected path derives from the video's
	// expected path plus the frozen suffix and original extension.
	Companions []CompanionPathDiffView
	// PlacementID and SizeBytes are the Placement-owned observation facts of
	// the row (empty/0 for a legacy observation).
	PlacementID string
	SizeBytes   int64
	// TargetUnavailable is the typed reason the row has no rendered target
	// (coverage_unresolved, render_facts_unavailable, target_render_failed);
	// the row then keeps its actual path and never differs.
	TargetUnavailable string
}

// CompanionPathDiffView is one companion sidecar's path difference.
// State is present, missing or size_mismatch as observed by the scan.
type CompanionPathDiffView struct {
	// ID is the registered companion the scan observed.
	ID           string
	Differs      bool
	ExpectedPath *string
	FilePath     string
	MediaKind    string
	State        string
}

// CompanionIssueView is one companion that needs attention in the latest
// generation: companion_missing, companion_size_mismatch or
// companion_not_aligned.
type CompanionIssueView struct {
	ExpectedPath *string
	FilePath     string
	MediaKind    string
	ReasonCode   string
	VideoPath    string
}

type UnclassifiedRoutePolicyValue string

const (
	UnclassifiedRoutePolicyValueRoute UnclassifiedRoutePolicyValue = "route"
	UnclassifiedRoutePolicyValueSkip  UnclassifiedRoutePolicyValue = "skip"
)

type FileEffectPolicyDefaultInput struct {
	ExpectedVersion   int64
	UnclassifiedRoute UnclassifiedRoutePolicyValue
}

type FileEffectPolicyDefaultView struct {
	UnclassifiedRoute UnclassifiedRoutePolicyValue
	UpdatedAt         time.Time
	Version           int64
}

type InstanceFileEffectPolicyInput struct {
	ExpectedVersion   int64
	UnclassifiedRoute UnclassifiedRoutePolicyValue
}

type InstanceFileEffectPolicyView struct {
	ManagedAnimeID       string
	SourceDefaultVersion int64
	UnclassifiedRoute    UnclassifiedRoutePolicyValue
	UpdatedAt            time.Time
	Version              int64
}

type PlacementView struct {
	CreatedAt          time.Time
	FactsVersion       string
	ID                 string
	ManagedAnimeID     string
	PhysicalPath       string
	SourceEvidenceType string
	SourceOwnerRef     string
	State              string
	TargetFingerprint  string
	VerificationState  string
	Version            int
}

type PlacementCoverageView struct {
	CoverageOrdinal int
	EvidenceRef     string
	FactsVersion    string
	ObservationKey  string
}

type OffsetMigrationPlanView struct {
	CreatedAt      time.Time
	FailureReason  *string
	ID             string
	ManagedAnimeID *string
	NewEpisode     string
	NewTargetPath  string
	OldTargetPath  string
	PlacementID    string
	PolicyVersion  int
	RawEpisode     string
	State          string
	UpdatedAt      *time.Time
	Version        int
}

type OffsetMigrationGenerationView struct {
	AppliedCount    *int
	BlockedCount    *int
	CompletedAt     *time.Time
	CreatedAt       time.Time
	ID              string
	ManagedAnimeID  string
	NewOffset       int
	OldOffset       int
	PlannedCount    *int
	PolicyVersion   int
	Reason          *string
	ReasonCode      *string
	ScopeKind       string
	ScopeOwnerID    string
	State           string
	SupersededCount *int
	UpdatedAt       *time.Time
	Version         int
}

type RetentionPolicyWriteInput struct {
	ExpectedVersion int64
	Policy          RetentionPolicy
}

type RetentionPolicyDefaultView struct {
	Policy    RetentionPolicy
	UpdatedAt *time.Time
	Version   int64
}

type InstanceRetentionPolicyView struct {
	ManagedAnimeID       string
	Policy               RetentionPolicy
	SourceDefaultVersion int64
	UpdatedAt            *time.Time
	Version              int64
}

type AlignmentOperationView struct {
	CompletedAt       *time.Time
	CreatedAt         time.Time
	FailureCode       *string
	ID                string
	ManagedAnimeID    string
	ObservationRef    string
	PolicyVersion     *int
	Reason            *string
	ScanGeneration    int
	SourceFingerprint string
	SourcePath        string
	State             string
	TargetPath        string
	UpdatedAt         *time.Time
	Version           int
}

type ReconciliationConsumerStateView struct {
	Consumer       string
	Generation     int
	ManagedAnimeID string
	Reason         *string
	ReasonCode     *string
	State          string
	UpdatedAt      time.Time
	Version        int
}

type CleanupOperationView struct {
	CreatedAt         time.Time
	Effect            string
	FailureCode       *string
	GraceDeadline     *time.Time
	ID                string
	ManagedAnimeID    string
	PlacementID       string
	PolicyVersion     int
	QuarantinePath    *string
	Reason            *string
	ReplacementRef    string
	RetentionMode     string
	SourceFingerprint string
	SourcePath        string
	State             string
	UpdatedAt         *time.Time
	Version           int
}

type ReplacementPlacementRef struct {
	Fingerprint string
	PlacementID string
	Version     int
}

type ReplacementPoolEvidence struct {
	Losers  *[]ReplacementPlacementRef
	PoolKey string
	Reason  *string
	Verdict string
	Winner  *ReplacementPlacementRef
	// Steps is the frozen item-by-item comparison that decided the pool.
	Steps []qualityprofile.Step
}

const (
	ReplacementDecisionViewStateAdmitted  = "admitted"
	ReplacementDecisionViewVerdictReplace = "replace"
)

type ReplacementDecisionView struct {
	AdmissionMode        string
	CandidatePlacementID *string
	CandidateReleaseID   string
	// CoverageKey is the one coverage key the decision freezes ("unresolved"
	// for a release without resolvable coverage).
	CoverageKey         string
	CreatedAt           time.Time
	ID                  string
	InputDigest         *string
	ManagedAnimeID      string
	PolicyVersion       int
	Pools               *[]ReplacementPoolEvidence
	ProfileVersion      string
	SnapshotGeneration  *int
	StaleReason         *string
	State               string
	SubscriptionVersion *int
	Verdict             string
	Version             int
}
