package domain

import (
	"errors"
	"time"
)

type TaskState string

const (
	TaskQueued      TaskState = "queued"
	TaskSubmitted   TaskState = "submitted"
	TaskDownloading TaskState = "downloading"
	TaskCompleted   TaskState = "completed"
	TaskFailed      TaskState = "failed"
	TaskCanceled    TaskState = "canceled"
)

type CancellationState string

const (
	CancellationRequested CancellationState = "requested"
	CancellationSucceeded CancellationState = "succeeded"
	CancellationNoOp      CancellationState = "no_op"
	CancellationFailed    CancellationState = "failed"
)

type CancellationOutcome string

const (
	CancellationOutcomeCanceled         CancellationOutcome = "canceled"
	CancellationOutcomeAlreadyCanceled  CancellationOutcome = "already_canceled"
	CancellationOutcomeAlreadyCompleted CancellationOutcome = "already_completed"
	CancellationOutcomeTaskChanged      CancellationOutcome = "task_changed"
	CancellationOutcomeProviderFailed   CancellationOutcome = "provider_failed"
)

type RetryState string

const (
	RetryRequested  RetryState = "requested"
	RetrySucceeded  RetryState = "succeeded"
	RetryFailed     RetryState = "failed"
	RetrySuperseded RetryState = "superseded"
)

type RetryIntent struct {
	ID                 string
	DownloadTaskID     string
	IdempotencyKey     string
	RequestVersion     int64
	RequestHash        string
	Generation         int64
	ProviderTaskID     string
	State              RetryState
	TraceID            string
	Version            int64
	ErrorMessage       string
	ProviderErrorClass ProviderErrorClass
	AttemptCount       int
	NextAttemptAt      *time.Time
}

// PathSnapshot freezes the path facts used by one qBittorrent effect. It is
// deliberately limited to the roots; provider credentials and connection URLs
// are never part of this snapshot. Under the same-path convention the qB
// reported root is the application root, so no path translation is frozen.
type PathSnapshot struct {
	RemoteRoot      string `json:"remote_root"`
	ApplicationRoot string `json:"application_root"`
}

type ProviderTask struct {
	ID       string
	Hash     string
	Name     string
	SavePath string
	State    TaskState
	// ProviderState is the raw provider status string behind State. State
	// deliberately collapses several provider statuses into one owner state, so
	// this field exists to explain and to exempt -- a stall backstop must not
	// count a user-paused or queue-held torrent as stalled. It never
	// participates in owner state classification.
	ProviderState string
	Path          string
	Paths         []string
	Files         []ProviderFile
	Progress      float64
	Error         string
	PathSnapshot  *PathSnapshot
	// ContentCompleteBytes is the byte count of content the provider reports as
	// complete. qB calls this field `completed`: it counts bytes of the torrent that
	// are done, not the session's cumulative network traffic (`downloaded`, which
	// also counts re-downloads and discarded pieces) and not a float ratio. It is
	// presence-aware, because "the protocol carried no such field" is a different
	// fact from "the provider reported zero".
	ContentCompleteBytes *int64
	// PayloadTotalBytes is the trusted total size of the frozen torrent content.
	// An observation whose content-complete bytes exceed it, or that reports a
	// different total for the same object, is provider evidence corruption rather
	// than progress.
	PayloadTotalBytes *int64
	// ContentPath is the raw qB-reported torrents/info content_path. It is
	// provider evidence for admission checks and is never derived from the
	// client's own frozen PathSnapshot.
	ContentPath string
	// DownloadSpeed is the provider-reported download rate in bytes per second
	// at this observation, nil when the protocol carried none. Display only: it
	// never participates in owner state, progress or stall judgment.
	DownloadSpeed *int64
}

// ErrProviderTaskAbsent is the one definitive provider answer that the task is
// gone: the provider's lookup succeeded and its reply does not contain the
// task. A transport, HTTP, authentication or decode failure is never absence.
// Two consecutive absent observations end the task as deleted in the downloader (DOWNLOADER_TASK_REMOVED).
var ErrProviderTaskAbsent = errors.New("provider task is absent")

// ProviderErrorClass distinguishes errors that are safe to terminally classify
// from errors that may have crossed an external side-effect boundary.
type ProviderErrorClass string

const (
	ProviderErrorUnknown   ProviderErrorClass = "unknown"
	ProviderErrorRetryable ProviderErrorClass = "retryable"
	ProviderErrorAmbiguous ProviderErrorClass = "ambiguous"
	ProviderErrorTerminal  ProviderErrorClass = "terminal"
)

type OperationKind string

const (
	OperationSubmit OperationKind = "submit"
	OperationRetry  OperationKind = "retry"
	OperationCancel OperationKind = "cancel"
)

type OperationPhase string

const (
	OperationReserved  OperationPhase = "reserved"
	OperationExecuting OperationPhase = "executing"
	OperationRecovery  OperationPhase = "recovery"
)

type ProviderFile struct {
	Path      string
	SizeBytes int64
	MediaKind string
	Language  string
	Progress  float64
}

// ArtifactFacts is the path-level observation of one downloaded file: its
// size, device/inode and modification time from one fstat. The content is
// never read.
type ArtifactFacts struct {
	Path       string
	SizeBytes  int64
	Device     uint64
	Inode      uint64
	ModifiedAt time.Time
	ObservedAt time.Time
	MediaKind  string
}

type VerificationResult string

const (
	VerificationVerified      VerificationResult = "verified"
	VerificationWithConflicts VerificationResult = "verified_with_conflicts"
	VerificationFailed        VerificationResult = "failed"
)

type DownloadTask struct {
	ID                        string
	DecisionID                string
	Origin                    string
	Provider                  string
	ProviderTaskID            string
	LocalRoot                 string
	State                     TaskState
	TraceID                   string
	IdempotencyKey            string
	ProviderOperationKey      string
	ActiveOperationKind       OperationKind
	ActiveOperationID         string
	ActiveOperationGeneration int64
	ActiveOperationPhase      OperationPhase
	ActiveOperationClaimID    string
	ActiveOperationClaimUntil *time.Time
	ProviderErrorClass        ProviderErrorClass
	ProviderErrorMessage      string
	NextRecoveryAt            *time.Time
	Version                   int64
	// Provider-reported progress facts persisted by the sync path. Read models
	// expose them as a snapshot; ProgressSnapshotAt == nil means no provider
	// snapshot has been persisted yet.
	Name                    string
	Progress                float64
	SizeBytes               int64
	DownloadedBytes         int64
	ProgressSnapshotAt      *time.Time
	ArtifactID              string
	ArtifactPath            string
	ArtifactFingerprint     string
	ArtifactItems           []ArtifactItem
	VerificationResult      VerificationResult
	ConfirmedEpisodeBinding *ConfirmedEpisodeBinding
	AllowedActions          []string
}

type CancelInput struct {
	TaskID          string
	ExpectedVersion int64
	IdempotencyKey  string
}

type CancellationResult struct {
	ID                        string
	DownloadTaskID            string
	RequestVersion            int64
	RequestHash               string
	State                     CancellationState
	Outcome                   CancellationOutcome
	ProviderTaskID            string
	TraceID                   string
	Version                   int64
	ErrorMessage              string
	ProviderErrorClass        ProviderErrorClass
	AttemptCount              int
	NextAttemptAt             *time.Time
	FencedOperationID         string
	FencedOperationGeneration int64
	ProviderEffectState       string
	ProviderLookupState       string
	FencedOperationClaimID    string
	FencedOperationClaimUntil *time.Time
	ProviderResult            map[string]any
	Task                      DownloadTask
}

type ManualArtifactImport struct {
	ID                      string
	ManagedAnimeID          string
	LibraryProfileID        string
	DownloadTaskID          string
	ArtifactID              string
	SourceRoot              string
	SourcePath              string
	ParserPattern           string
	EpisodeOffset           int
	State                   string
	IdempotencyKey          string
	TraceID                 string
	ActorID                 string
	VerificationID          string
	Fingerprint             string
	FailureReason           string
	VerificationResult      VerificationResult
	ConfirmedEpisodeBinding *ConfirmedEpisodeBinding
	Version                 int64
	CreatedAt               time.Time
	UpdatedAt               time.Time
	RequestHash             string
	AllowedActions          []string
}

type ManualImportInput struct {
	ManagedAnimeID string
	// LibraryConfigID is the typed singleton library connection used by the
	// manual-import path. LibraryProfileID remains a persistence
	// compatibility field for historical import rows and tests.
	LibraryConfigID  string
	LibraryProfileID string
	RelativePath     string
	ParserPattern    string
	EpisodeOffset    int
	ActorID          string
	IdempotencyKey   string
}

type ExternalArtifactSource struct {
	Root string
	Path string
}

type ArtifactItem struct {
	ID             string
	DownloadTaskID string
	ArtifactID     string
	Path           string
	SizeBytes      int64
	SHA256         string
	MediaKind      string
	Language       string
	IsPrimary      bool
	ObservedAt     time.Time
	Version        int64
}

// PlacementFacts is the Download-owned snapshot handed to Library when an
// already verified artifact is explicitly sent through placement planning.
// It intentionally contains no Library root, operation, or collision policy.
type PlacementFacts struct {
	ManagedAnimeID string
	ArtifactID     string
	Path           string
	Fingerprint    string
	MediaKind      string
	BGMID          string
	TMDBID         string
	TMDBMediaKind  string
	TMDBSeason     *int
	Resolution     string
	Quality        string
	Language       string
	TraceID        string
	UserTitle      string
	GroupName      string
	CustomPath     string
	FactsVersion   string
	EpisodeRef     string
	EpisodeNumber  *int
	Subtitles      []PlacementSubtitle
}

type PlacementSubtitle struct {
	Path        string
	Fingerprint string
	Language    string
}

type Artifact struct {
	ID             string
	DownloadTaskID string
	Facts          ArtifactFacts
	Version        int64
}

type Verification struct {
	ID         string
	ArtifactID string
	Result     VerificationResult
	Details    map[string]any
	VerifiedAt time.Time
}

type ConfirmedEpisode struct {
	ParserVersion string
	Pattern       string
	Offset        int
	RawCapture    string
	EpisodeNumber int
	Confidence    string
	Evidence      map[string]any
}

// EpisodeMappingSnapshot is copied only when an independent owner feature has
// already consumed a mapping. Download never resolves or creates mappings.
type EpisodeMappingSnapshot struct {
	Ref             string
	Version         int64
	Provider        string
	SourceEpisode   int
	ProviderEpisode int
	State           string
	Evidence        map[string]any
}

type ConfirmedEpisodeBinding struct {
	ID              string
	DownloadTaskID  string
	ArtifactID      string
	ArtifactItemRef string
	AnimeID         string
	VerificationID  string
	Episode         ConfirmedEpisode
	Mapping         *EpisodeMappingSnapshot
	Actor           string
	Version         int64
}
