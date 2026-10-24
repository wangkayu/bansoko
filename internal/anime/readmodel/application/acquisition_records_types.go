package application

import (
	"errors"
	"time"
)

// Execution records: the read-only list and detail of accepted acquisition
// roots (subscription automatic, subscription add and one-shot). Every value
// here is projected from the root and its own execution graph; nothing is
// derived from a Release, a subscription log or the newest task, and no read
// drives execution. There is no retry, replan or recover action: repair is a
// new acquisition elsewhere.

// acquisitionAccessScope is the single deployment-owner access domain every
// root is written in today. Reads filter on it in the first predicate, so a row
// of an unknown scope reads as not found.
const acquisitionAccessScope = "deployment_owner_v1"

// Fixed stage names, in execution order.
const (
	AcquisitionStageSubmit        = "submit"
	AcquisitionStageDownloadWait  = "download_wait"
	AcquisitionStageInspect       = "inspect"
	AcquisitionStagePlan          = "plan"
	AcquisitionStageFileExecution = "file_execution"
	AcquisitionStageSettlement    = "settlement"
	// AcquisitionStageCompletion is the download-completion action on the
	// qBittorrent task: not_applicable unless the record succeeded.
	AcquisitionStageCompletion = "completion"
)

var acquisitionStageOrder = []string{
	AcquisitionStageSubmit, AcquisitionStageDownloadWait, AcquisitionStageInspect,
	AcquisitionStagePlan, AcquisitionStageFileExecution, AcquisitionStageSettlement, AcquisitionStageCompletion,
}

// Stage availability.
const (
	AcquisitionStagePresent       = "present"
	AcquisitionStageNotReached    = "not_reached"
	AcquisitionStageNotApplicable = "not_applicable"
)

// Stage states of a present stage. cancelled marks a stage that never finished
// because the root reached its terminal outcome first.
const (
	AcquisitionStageStatePending   = "pending"
	AcquisitionStageStateRunning   = "running"
	AcquisitionStageStateSucceeded = "succeeded"
	AcquisitionStageStateFailed    = "failed"
	AcquisitionStageStateBlocked   = "blocked"
	AcquisitionStageStateCancelled = "cancelled"
)

// Display value states.
const (
	AcquisitionDisplayVisible     = "visible"
	AcquisitionDisplayRedacted    = "redacted"
	AcquisitionDisplayUnavailable = "unavailable"
)

// Errors of the record reads. NotFound uses apperrors.NotFound.
var (
	// ErrAcquisitionFilesNotReady: the root is full but its file requirements
	// are not sealed yet, so there is no file set to page.
	ErrAcquisitionFilesNotReady = errors.New("acquisition files not ready")
	// ErrAcquisitionDetailUnavailable: the root is compact; its per-file detail
	// was reclaimed and only the terminal summary remains.
	ErrAcquisitionDetailUnavailable = errors.New("acquisition detail unavailable")
	// ErrAcquisitionReadInconsistent: a stored root does not decode into the
	// approved shape. It is never rendered as an empty success.
	ErrAcquisitionReadInconsistent = errors.New("acquisition read inconsistent")
)

// AcquisitionRecordQuery is one page request of the record list.
type AcquisitionRecordQuery struct {
	Limit          int
	Cursor         string
	ManagedAnimeID string
	State          string
	EntryKind      string
	// Origin narrows to "subscription" (automatic, subscription_add) or
	// "one_shot"; empty keeps both.
	Origin       string
	AcceptedFrom *time.Time
	AcceptedTo   *time.Time
}

// AcquisitionFileQuery is one page request of a root's files.
type AcquisitionFileQuery struct {
	Limit  int
	Cursor string
}

// AcquisitionDisplayValue is a bounded, redacted display string. A redacted or
// unavailable value carries no text.
type AcquisitionDisplayValue struct {
	State string
	Value *string
}

// AcquisitionCounts is the settled tally of a sealed requirement set.
type AcquisitionCounts struct {
	Total, Required, Excluded, Succeeded, Failed int64
}

// AcquisitionFailure is the safe primary failure of a terminal receipt.
type AcquisitionFailure struct {
	Stage      string
	ReasonCode string
	FileRef    *string
}

// AcquisitionOutcome is the immutable terminal receipt of a settled root.
type AcquisitionOutcome struct {
	Outcome            string
	RequirementsStatus string
	Counts             *AcquisitionCounts
	PrimaryFailure     *AcquisitionFailure
	EffectsComplete    bool
	HasConfirmed       bool
	HasUnknown         bool
	SettledAt          time.Time
}

// AcquisitionStage is one fixed stage of a full root. Fields other than Stage
// and Availability are nil unless the stage is present.
type AcquisitionStage struct {
	Stage           string
	Availability    string
	State           *string
	ReasonCode      *string
	AttemptsStarted *int64
	StartedAt       *time.Time
	UpdatedAt       *time.Time
	// Done/Total: the file progress of file_execution (delivered of required).
	Done  *int64
	Total *int64
}

// AcquisitionRequirements is the file requirement status of a full root.
type AcquisitionRequirements struct {
	Status        string
	FileCount     *int64
	RequiredCount *int64
	ExcludedCount *int64
}

// AcquisitionAnimeLink is the current managed anime the root came from, when
// it still exists.
type AcquisitionAnimeLink struct {
	ManagedAnimeID string
	Title          string
}

// AcquisitionReleaseLink is the source release of the root, when it still
// exists.
type AcquisitionReleaseLink struct {
	ReleaseID string
	Title     AcquisitionDisplayValue
}

// AcquisitionRecordSummary is one row of the record list.
type AcquisitionRecordSummary struct {
	AcquisitionID string
	// OneShotID is the one-shot marker handle when EntryKind is one_shot, nil for
	// every other entry kind.
	OneShotID    *string
	EntryKind    string
	State        string
	RecordDetail string
	AcceptedAt   time.Time
	TerminalAt   *time.Time
	SourceTitle  AcquisitionDisplayValue
	Anime        *AcquisitionAnimeLink
	// Stage/StageState: the stage the root is at (running) or failed in
	// (terminal); nil for a success.
	Stage      *string
	StageState *string
	ReasonCode *string
	Counts     *AcquisitionCounts
	// Release is the source release, when it still exists.
	Release *AcquisitionReleaseLink
	// Episodes are the episode numbers the frozen batch plan of the source
	// release places, ascending and unique; nil when it covers none.
	Episodes []int
	// Transfer is the last persisted download observation; set only while the
	// root is running at download_wait.
	Transfer *AcquisitionTransfer
}

// AcquisitionTransfer is the download sync poll's last persisted observation.
type AcquisitionTransfer struct {
	CompletedBytes int64
	TotalBytes     int64
	DownloadSpeed  *int64
	ObservedAt     time.Time
}

// AcquisitionRecordPage is one keyset page of the record list.
type AcquisitionRecordPage struct {
	AsOf         time.Time
	Items        []AcquisitionRecordSummary
	NextCursor   *string
	OriginCounts AcquisitionRecordOriginCounts
}

// AcquisitionRecordOriginCounts totals the records per origin under the
// page's anime, state and time filters, in the page's snapshot.
type AcquisitionRecordOriginCounts struct {
	Subscription int64
	OneShot      int64
}

// AcquisitionRecordDetail is the stable detail of one root.
type AcquisitionRecordDetail struct {
	AcquisitionRecordSummary
	ExecutionBranch string
	CompactedAt     *time.Time
	CompactReason   *string
	AsOf            time.Time
	Outcome         *AcquisitionOutcome
	Stages          []AcquisitionStage
	Requirements    *AcquisitionRequirements
	FilesAvailable  bool
	// Version is the root version an import retry sends as If-Match.
	Version int64
	// ImportRetryAvailable is true when the import retry would be accepted as
	// of this read (acquisition_import_retry_refusal answers NULL).
	ImportRetryAvailable bool
}

// AcquisitionFileOperation is the newest file operation of one required file.
type AcquisitionFileOperation struct {
	State      string
	Operation  string
	ReasonCode *string
}

// AcquisitionFileDelivery is the one recorded delivery result of a file.
type AcquisitionFileDelivery struct {
	Outcome        string
	CompletionMode *string
	EffectClass    string
	SourceCleanup  string
	ReasonCode     *string
}

// AcquisitionFileHandoff is the management handoff of a delivered file.
type AcquisitionFileHandoff struct {
	State      string
	ReasonCode *string
}

// AcquisitionFile is one manifest file of a full, sealed root.
type AcquisitionFile struct {
	Ordinal             int64
	FileRef             string
	RelativePath        AcquisitionDisplayValue
	SizeBytes           int64
	MediaKind           string
	Requirement         string
	SelectionReasonCode *string
	Operation           *AcquisitionFileOperation
	Delivery            *AcquisitionFileDelivery
	Handoff             *AcquisitionFileHandoff
	PlacementFailure    *AcquisitionFilePlacementFailure
	PlacementEnd        *AcquisitionFilePlacementEnd
	// PlacedPath is where the final object a successful delivery recorded
	// lies, relative to the library (displaypath root of the anime's files);
	// nil until the delivery succeeded.
	PlacedPath *AcquisitionDisplayValue
	// CollisionOutcome is none | skipped | versioned | overwritten, derived
	// from the recorded plan and publication facts; nil when they do not say.
	CollisionOutcome *string
}

// AcquisitionFilePlacementEnd is set when the placement this file's
// operation produced ended: superseded by a quality replacement, or displaced
// by another file at its path (cause overwrite) or by a redownload (cause
// redownload). Successor is the displacing placement's instance; nil when it
// was deleted (or for superseded).
type AcquisitionFilePlacementEnd struct {
	State     string
	Cause     *string
	Successor *AcquisitionAnimeLink
}

// AcquisitionFilePlacementFailure is set when the completed operation's
// placement handoff failed terminally; the record's import retry reopens it.
type AcquisitionFilePlacementFailure struct {
	Attempts int64
	// ReasonCode is the failed obligation's stable reason code, when recorded.
	ReasonCode *string
}

// AcquisitionFilePage is one ordinal keyset page of a root's files.
type AcquisitionFilePage struct {
	AcquisitionID string
	AsOf          time.Time
	Items         []AcquisitionFile
	NextCursor    *string
}
