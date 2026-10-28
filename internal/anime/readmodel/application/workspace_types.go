package application

import (
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/batchplan"
)

// AnimeWorkspace is the application-owned owner-chain read model.
type AnimeWorkspace struct {
	AcquisitionTargets *[]AcquisitionTarget
	Artifacts          []Artifact
	// ConfigWaitingFiles counts the downloaded files whose import waits for
	// the anime's configuration (waiting plans of running acquisitions).
	ConfigWaitingFiles int64
	DownloadTasks      []WorkspaceDownloadTask
	EpisodeProgress    *EpisodeProgress
	FileOperations     []FileOperation
	PlacementHandoffs  []PlacementHandoff
	Logs               []SubscriptionLogEntry
	ManagedAnime       ManagedAnime
	Metadata           AnimeMetadata
	MetadataHold       MetadataHold
	Releases           []Release
	RSSSourceRuntimes  []RSSSourceRuntime
	RSSSources         []RSSSource
	Rules              RulesOwner
	Subgroups          SubgroupsOwnerResult
	Subscription       Subscription
	TargetObservations *[]TargetObservation
}

type WorkspaceDownloadTask struct {
	FailedWorkItemID    *string
	FailureCode         *string
	FailureReason       *string
	FrozenInput         FrozenDownloadInput
	FrozenPolicy        FrozenPolicySnapshot
	ID                  string
	IdempotencyKey      string
	ProviderCorrelation *WorkspaceProviderCorrelation
	ReleaseID           string
	State               string
	Version             int64
}

type WorkspaceProviderCorrelation struct {
	ExternalID *string
	InfoHash   *string
	StableTag  *string
}

type Release struct {
	// AcquisitionID is the newest execution record accepted from this release
	// in the deployment access scope; nil when none was accepted.
	AcquisitionID         *string
	AcquisitionTargets    *[]AcquisitionTarget
	DownloadTaskID        *string
	EvaluationVersion     *string
	EvaluatedRulesVersion int64
	InternalFacts         []byte
	ID                    string
	ParsedFacts           *ReleaseParsedFacts
	RawFacts              RawReleaseFacts
	ReasonCode            string
	ReasonDetail          *string
	ReleaseFiles          *[]ReleaseFile
	RSSSourceID           string
	SourceFacts           SourceFacts
	StableItemKey         string
	TargetObservations    *[]TargetObservation
	Verdict               string
	Version               int64
	// currentTaskState and currentAcquisitionState are the states of the
	// Release's current download task and of that task's acquisition ("" when
	// absent): the retry decision's facts.
	currentTaskState        string
	currentAcquisitionState string
	// placementMissing is the retry decision's PlacementMissing fact: the
	// latest library scan found a placed file of this Release missing or
	// holding other content (read only while the current task is completed).
	placementMissing bool
	// frozenPlan is the batch plan of the latest media fact snapshot; nil
	// without one.
	frozenPlan *batchplan.Plan
	// rawSourceFacts, inputHashV1 and inputHashV2 are the stored source facts and
	// the torrent input's info hashes, read only for the RSS list view's Vars
	// evidence (releaseReadOptions.vars).
	rawSourceFacts           []byte
	inputHashV1, inputHashV2 string
	// payloadDigest is the digest of the exact torrent bytes this release was
	// parsed from (release_torrent_inputs.payload_digest). It is the payload
	// binding every file_ref is derived from, so a reader that rebuilds a
	// release's manifest needs it even when it is not reading Vars evidence.
	// Empty when the release has no stored torrent input.
	payloadDigest string
}

type RawReleaseFacts struct {
	EnclosureLength *int64     `json:"enclosure_length"`
	EnclosureType   *string    `json:"enclosure_type"`
	PublishedAt     *time.Time `json:"published_at"`
	Title           string     `json:"title"`
}

type ReleaseParsedFacts struct {
	Episode   *float32  `json:"episode"`
	GroupName *string   `json:"group_name"`
	Languages *[]string `json:"languages"`
	Quality   *string   `json:"quality"`
	Special   *bool     `json:"special"`
	// Coverage is a batch release's planned episodes.
	Coverage *ReleaseCoverage `json:"coverage"`
}

// ReleaseCoverage is the sorted episodes a batch places.
type ReleaseCoverage struct {
	Episodes []int `json:"episodes"`
}

type SourceFacts struct {
	GroupName     *string `json:"group_name"`
	IdentityKind  string  `json:"identity_kind"`
	IdentityValue string  `json:"identity_value"`
	// AniBT is the frozen AniBT tag set (AniBT-source releases only).
	AniBT *SourceAniBTTags `json:"anibt,omitempty"`
	// TitleTags is the frozen title-parse tag set (releases observed after the
	// title row existed).
	TitleTags *SourceTitleTags `json:"title_tags,omitempty"`
}

// SourceTitleTags mirrors the persisted title tag block of Release.source_facts.
type SourceTitleTags struct {
	Schema         string   `json:"schema"`
	Implementation string   `json:"implementation"`
	ConfigDigest   string   `json:"config_digest,omitempty"`
	Tags           []string `json:"tags"`
	Diagnostic     string   `json:"diagnostic,omitempty"`
}

// SourceAniBTTags is the persisted AniBT tag block of Release.source_facts;
// decoding reclassifies customTag values frozen before the sub-dimensions.
type SourceAniBTTags = acquisitiondomain.StoredAniBTTags

type ReleaseFile struct {
	Episode          *string
	Filename         string
	Language         *string
	MediaKind        string
	Path             string
	Resolution       *string
	Revision         *string
	SizeBytes        *int64
	TargetPath       *string
	TargetDiagnostic *TargetDiagnostic
	// Outcome and OutcomeReason are the file's conclusion in the batch plan
	// the release was accepted with; nil without an accepted plan.
	Outcome       *string
	OutcomeReason *string
}

type TargetDiagnostic struct {
	Code       string
	Message    string
	SourcePath *string
}

type AcquisitionTarget struct {
	CollisionPolicy   string
	ExpectedSizeBytes int64
	ID                string
	Intent            string
	MediaKind         string
	ReleaseID         string
	SourcePath        string
	SpecDigest        string
	TargetPath        string
	TargetRef         string
	Version           int64
}

type TargetObservation struct {
	ObservedAt        time.Time
	ObservedSizeBytes *int64
	ReasonCode        *string
	State             string
	TargetID          string
	Version           int64
}

type Artifact struct {
	DownloadTaskID   string
	FailedWorkItemID *string
	FailureCode      *string
	FailureReason    *string
	Files            []ArtifactFile
	Fingerprints     []Fingerprint
	ID               string
	State            string
	Version          int64
}

type ArtifactFile struct {
	Fingerprint     Fingerprint
	MediaKind       string
	Path            string
	Ref             string
	SubtitleBinding *ArtifactFileSubtitleBinding
}

type ArtifactFileSubtitleBinding struct {
	BoundVideoFileRef *string
	Confidence        *float32
	Diagnostics       []NamingDiagnostic
	Method            *string
	State             string
}

type NamingDiagnostic struct {
	Code    string
	Message string
	Path    *string
}

type Fingerprint struct {
	Algorithm string `json:"algorithm"`
	SizeBytes int64  `json:"size_bytes"`
	Value     string `json:"value"`
}

type FileOperation struct {
	ArtifactFileRef         string
	ArtifactID              string
	CollisionPolicy         string
	ExpectedSizeBytes       *int64
	FailedWorkItemID        *string
	FailureCode             *string
	FailureReason           *string
	FrozenConfigSnapshot    FrozenFileConfigSnapshot
	FrozenRenderSnapshot    FrozenRenderSnapshot
	ID                      string
	IdempotencyKey          string
	Intent                  *string
	ObservedAt              *time.Time
	ObservedTargetSizeBytes *int64
	Operation               string
	SourceFingerprint       Fingerprint
	SourcePath              string
	State                   string
	TargetFingerprint       *Fingerprint
	TargetPath              string
	Version                 int64
}

// PlacementHandoff is the workspace projection of one durable post-completion
// placement obligation. It carries only user-understandable stage facts — the
// public stage state, the stable reason, the attempt count and the next due
// time of the live continuation — and never a WorkItem or internal identity.
type PlacementHandoff struct {
	FileOperationID string
	State           string
	ReasonCode      *string
	Attempts        int64
	AttemptBudget   int64
	NextRetryAt     *time.Time
	UpdatedAt       time.Time
}

type FrozenFileConfigSnapshot struct {
	RulesVersion  int64  `json:"rules_version"`
	SafetyVersion string `json:"safety_version"`
}

type FrozenRenderSnapshot struct {
	Directory       string `json:"directory"`
	Filename        string `json:"filename"`
	RendererVersion string `json:"renderer_version"`
}

type SubscriptionLogEntry struct {
	// AnimeTitle is the anime's current display title, read with the entry.
	AnimeTitle string
	// Source is subscription, or one_shot for the work of a one-shot download.
	Source         string
	CausationID    *string
	CausationType  *string
	CorrelationID  *string
	DedupeKey      string
	Details        *SubscriptionLogDetails
	GroupRef       *string
	ID             string
	Kind           string
	ManagedAnimeID string
	MessageCode    string
	MessageParams  SubscriptionLogMessageParams
	OccurredAt     time.Time
	Outcome        string
	ReasonCode     *string
	ReleaseID      *string
	RetentionUntil time.Time
	RSSSourceID    *string
	SubjectID      *string
	SubjectType    *string
	SubjectVersion *int64
	SubscriptionID string
}

// SubscriptionLogMessageParams are the scalar values an entry's message
// interpolates.
type SubscriptionLogMessageParams struct {
	Count     *int64  `json:"count"`
	FileName  *string `json:"file_name"`
	GroupName *string `json:"group_name"`
	Name      *string `json:"name"`
}

type SubscriptionLogDetails struct {
	ErrorCount   *int    `json:"error_count"`
	ItemCount    *int    `json:"item_count"`
	ReasonCode   *string `json:"reason_code"`
	ReasonDetail *string `json:"reason_detail"`
}
