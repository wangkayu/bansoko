package application

import (
	"bansoko/internal/batchplan"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"bansoko/internal/episodetitle/namingfacts"
	"bansoko/internal/events"
	"bansoko/internal/library/domain"
	"bansoko/internal/namingvars"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parser"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/postprocessing/naming"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/workitems"
)

const (
	defaultRetryAfter                 = 5 * time.Minute
	defaultMaxAttempts                = 5
	maxPathLength                     = 4096
	maxReasonLength                   = 2048
	maxReasonCodeLength               = 128
	maxFingerprintLength              = 256
	overwriteExecutionProtocolVersion = "overwrite-execution-v1"
)

var (
	ErrInvalidKind                 = errorString("file operation handler received an unsupported work item kind")
	ErrInvalidClaim                = errorString("file operation claim is invalid")
	ErrOperationNotFound           = errorString("file operation not found")
	ErrArtifactNotVerified         = errorString("artifact is not verified")
	ErrArtifactVersionMismatch     = errorString("artifact version does not match file operation")
	ErrArtifactFingerprintMismatch = errorString("artifact file fingerprint does not match file operation")
	ErrArtifactFileNotFound        = errorString("artifact file not found")
	ErrSizeAwareObserverRequired   = errorString("size-aware file observation is required for this operation")
	ErrInvalidTarget               = errorString("file operation target is invalid")
	ErrOverwriteUnsupported        = errorString("overwrite collision policy is not supported")
	ErrSubtitleBindingMissing      = errorString("subtitle has no video candidate in the same artifact")
	ErrSubtitleBindingAmbiguous    = errorString("subtitle has multiple video candidates and cannot be disambiguated")
	ErrBoundTargetMismatch         = errorString("bound video frozen target does not match the locked binding target")
	// ErrMediaFactsMissing marks a video whose Release has no media fact
	// snapshot, or whose snapshot has no row for that file. Every acquisition
	// admission freezes the snapshot and artifact inspection writes one row per
	// file, so its absence is an integrity failure: the video is blocked
	// visibly, never treated as an unknown file.
	ErrMediaFactsMissing = errorString("release media facts are missing for this file")
	// ErrBatchPlanDrift marks a video whose inspected identity disagrees with
	// the batch plan the Release was accepted with (the plan has no entry for
	// it, or another coverage key, or one side resolved and the other not).
	// Both come from the same frozen parser and file kind list, so this is an
	// integrity failure: the file is blocked visibly, never re-planned.
	ErrBatchPlanDrift = errorString("the downloaded file disagrees with the accepted batch plan")
)

type errorString string

func (e errorString) Error() string { return string(e) }

// FileExecutor is the filesystem adapter port. The typed publication chain
// additionally requires the invocation port (typedPublicationInvoker); an
// executor without it fails closed with typed_publication_unsupported.
type FileExecutor interface {
	domain.PublicationPreparer
}

// ParentIdentityAuthorizer freezes the source and target parent directory
// identities during overwrite planning. The filesystem adapter owns the
// no-follow implementation; application code only consumes the typed facts.
type ParentIdentityAuthorizer interface {
	ParentIdentities(root, source, target string) (domain.FileIdentity, domain.FileIdentity, error)
}

// FileObserver is the legacy targeted read-after-effect port. New filesystem
// adapters should also implement SizeAwareFileObserver so handlers can verify
// the observed size as well as the fingerprint.
type FileObserver interface {
	Observe(context.Context, string) (string, error)
}

// SizeAwareFileObserver is an optional extension of FileObserver. Keeping this
// as a separate interface lets old fakes and persisted hash-only operations
// remain compatible without weakening the production filesystem adapter.
type SizeAwareFileObserver interface {
	ObserveFile(context.Context, string) (domain.FileObservation, error)
}

// RetryableError is an adapter-neutral way for tests and adapters to classify
// an external filesystem failure as retryable.
type RetryableError struct{ Err error }

func (e RetryableError) Error() string {
	if e.Err == nil {
		return "retryable file operation error"
	}
	return e.Err.Error()
}

func (e RetryableError) Unwrap() error { return e.Err }
func (RetryableError) Temporary() bool { return true }

// PlacementSink records a verified placement after a completed file effect.
// It is best-effort: a sink failure never rolls back the completed owner.
type PlacementSink interface {
	AdmitFromFileOperation(ctx context.Context, fileOperationID string) error
}

// Service owns file_operations and file_execute WorkItems.
type Service struct {
	db                   *sql.DB
	workItems            *workitems.Store
	executor             FileExecutor
	observer             FileObserver
	parentAuthorizer     ParentIdentityAuthorizer
	now                  func() time.Time
	businessDeliverySink plugintargetport.BusinessDeliveryEnqueuer
	diagnostics          observabilityport.Recorder
	activityWriter       observabilityport.ActivityWriter
	logger               observabilityport.RuntimeLogger
	eventHub             *events.Hub
	placementSink        PlacementSink
	placementRetryAfter  time.Duration
	mediaFacts           facts.MediaRepository
	// episodeTitles resolves the provider episode titles (bgm.ep_title /
	// tmdb.ep_title) the rename render queries from the persistent cache.
	// Nil means not wired and every lookup surfaces a transient error; the
	// composition root always wires one.
	episodeTitles *namingfacts.Querier
	preparedBatch *artifactPreparedBatch // invocation-local clone only; never shared Service
}

// SetPlacementSink wires the optional FileMaintenance placement admission.
// The sink is invoked by the durable placement obligation, never by the file
// effect path: completing a file operation commits the obligation and its typed
// continuation item, and the continuation claim (or the completion's
// immediate-kick optimization) runs the sink. A sink failure is therefore
// always recorded on the obligation and never blocks or rolls back the
// completed file operation.
func (s *Service) SetPlacementSink(sink PlacementSink) {
	if s != nil {
		s.placementSink = sink
	}
}

// SetMediaFactsStore replaces the frozen-identity source for naming and
// subtitle binding (NewService wires the PostgreSQL store). Without a store no
// snapshot can be read, so every video is blocked as media_facts_missing.
func (s *Service) SetMediaFactsStore(store facts.MediaRepository) {
	if s != nil {
		s.mediaFacts = store
	}
}

// SetEpisodeTitleQueries wires the persistent provider episode titles Querier
// (migration 046). The rename render is on the network-query whitelist; the
// Querier decides per source whether a lookup runs at all. Without it the
// lookup surfaces a transient error, never a silently empty fact.
func (s *Service) SetEpisodeTitleQueries(queries *namingfacts.Querier) {
	if s != nil {
		s.episodeTitles = queries
	}
}

// SetActivityWriter wires the central transaction-local business history writer.
func (s *Service) SetActivityWriter(writer observabilityport.ActivityWriter) {
	if s != nil {
		s.activityWriter = writer
	}
}

// SetDiagnosticRecorder wires the optional diagnostic episode boundary.
// Diagnostic writes are best-effort after-commit work and never block the
// business path.
func (s *Service) SetDiagnosticRecorder(recorder observabilityport.Recorder, logger observabilityport.RuntimeLogger) {
	s.diagnostics = recorder
	s.logger = logger
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

type artifactContext struct {
	artifactID      string
	artifactVersion int64
	downloadTaskID  string
	taskState       string
	frozenInput     []byte
	frozenPolicy    []byte
	releaseID       string
	releaseVersion  int64
	stableItemKey   string
	rawFacts        []byte
	sourceFacts     []byte
	parsedFacts     []byte
	sourceID        string
	sourceKind      string
	groupName       string
	// sourceProvider/sourceGroupRef are the provider-namespaced external group
	// identity read from the same source row as groupName.
	sourceProvider string
	sourceGroupRef string
	managedAnimeID string
	animeTitle     string
	titleOverride  sql.NullString
	// providerTitle is the provider catalog title frozen in the accepted media
	// (media.provider_title); empty for a custom/oneshot subscription or an
	// unobserved entry.
	providerTitle string
	metadataBGM   sql.NullString
	tmdbWorkID    sql.NullString
	tmdbMediaKind sql.NullString
	// mediaKindOverride is the current owner pin (tv | movie); it only matters
	// when the media fact snapshot carries no frozen binding.
	mediaKindOverride sql.NullString
	tmdbSeason        sql.NullInt64
	// bindingTitles and torrent are the native work titles, provider years and
	// the torrent metadata frozen in the accepted input (schema 6 / 4); empty for
	// legacy plans and older inputs, which never froze them. A nil year is a work
	// whose provider year was unknown when the input was accepted.
	bindingTitles frozenBindingTitles
	torrent       naming.TorrentNamingFacts
	correlationID sql.NullString
	files         []artifactFileContext
	acceptedFacts bool
	// frozenReleaseResolution is the release-level resolution frozen on the
	// release media fact snapshot; accepted (typed) plans fall back to it when
	// file inspection carries no resolution.
	frozenReleaseResolution string
	// acceptedResolution is media.quality.resolution frozen in the accepted
	// input (same title and attribute rules as the pre-download TargetSet);
	// it is the first fallback for accepted plans.
	acceptedResolution string
}

// frozenBindingTitles are the bound works' native titles and provider years
// frozen at accept.
type frozenBindingTitles struct {
	bgmTitle, bgmTitleCN, tmdbTitleCN, tmdbOriginalTitle string
	bgmYear, tmdbYear                                    *int64
}

type artifactFileContext struct {
	id                string
	fileRef           string
	relativePath      string
	sourcePath        string
	mediaKind         string
	sizeBytes         int64
	fingerprint       json.RawMessage
	facts             json.RawMessage
	bindingState      string
	boundVideoFileRef sql.NullString
	bindingSnapshot   []byte
}

type effectiveConfig struct {
	naming            naming.NamingRules
	parser            fileOperationParserRules
	effect            fileOperationEffectRules
	fileFilter        fileOperationFileFilterRules
	unclassifiedRoute string
	vars              *namingvars.FrozenEvaluation
	versions          map[string]int64
}

// These values are persisted configuration facts, not HTTP DTOs. They keep
// file-operation planning independent from the generated transport package
// while retaining the JSON shape owned by the configuration sections.
type fileOperationParserRules struct {
	EpisodePatterns []string `json:"episode_patterns"`
}

type fileOperationEffectRules struct {
	CollisionPolicy fileOperationCollisionPolicy `json:"collision_policy"`
	Operation       fileOperationKind            `json:"operation"`
}

type fileOperationCollisionPolicy string

const (
	fileOperationCollisionFail      fileOperationCollisionPolicy = "fail"
	fileOperationCollisionVersioned fileOperationCollisionPolicy = "versioned"
	fileOperationCollisionOverwrite fileOperationCollisionPolicy = "overwrite"
)

type fileOperationKind string

const (
	fileOperationKindCopy     fileOperationKind = "copy"
	fileOperationKindMove     fileOperationKind = "move"
	fileOperationKindHardlink fileOperationKind = "hardlink"
)

type fileOperationFileFilterRules struct {
	MustContain string `json:"must_contain"`
	NotIn       string `json:"not_in"`
}

type prepareContext struct {
	config    effectiveConfig
	configErr error
	// replanTargets separates explicit new-input plans from admission targets.
	// Retries retain this flag with the plan snapshot, not the WorkItem attempt.
	replanTargets bool
	// parserProvenance is the parser evidence frozen on the Release media fact
	// snapshot this artifact descends from. It is written into every frozen
	// FileOperation config snapshot so the plan can be explained and replayed
	// without reading current instance parser policy. Nil only before the
	// snapshot is read; nothing is invented for it.
	parserProvenance *parser.Provenance
	// oneShot is the finite one-shot marker reference for this prepare run.
	// Non-nil routes every file_execute WorkItem insertion through the
	// marker-first gate (CreateOrMergeOneShot) inside the caller's transaction.
	selectedRefs map[string]bool // nil means frozen policy decides; nonnil is exact accepted/early selection
	oneShot      *workitems.OneShotRef
	// videoFacts lists the video file refs the Release media fact snapshot
	// has a row for; nil means the Release has no snapshot at all.
	videoFacts map[string]bool
	// plan is the batch plan frozen on the same snapshot; nil means the
	// snapshot carries none (incomplete facts). drift marks the videos whose
	// inspected identity disagrees with it.
	plan  *batchplan.Plan
	drift map[string]bool
	// targetVersions holds the version number the frozen plan gives a video
	// (file ref -> number, recordTargetVersion); a re-render applies it.
	targetVersions map[string]int
}

// mediaFactsErr is the prepare-time integrity check of one video: it must
// have a row in the Release media fact snapshot, the snapshot must carry the
// batch plan, and the row must agree with that plan.
func (prep prepareContext) mediaFactsErr(ref string) error {
	if prep.videoFacts == nil {
		return fmt.Errorf("%w: the release has no media fact snapshot", ErrMediaFactsMissing)
	}
	if !prep.videoFacts[ref] {
		return fmt.Errorf("%w: the media fact snapshot has no row for file %s", ErrMediaFactsMissing, ref)
	}
	if prep.plan == nil {
		return fmt.Errorf("%w: the media fact snapshot has no batch plan", ErrMediaFactsMissing)
	}
	if prep.drift[ref] {
		return fmt.Errorf("%w: file %s", ErrBatchPlanDrift, ref)
	}
	return nil
}

func (prep prepareContext) admitsFile(ref string) bool {
	return prep.selectedRefs == nil || prep.selectedRefs[ref]
}

type operationPlan struct {
	id                    string
	artifactID            string
	artifactVersion       int64
	artifactFileID        string
	operationVersion      int64
	operation             string
	sourcePath            string
	targetPath            string
	sourceFingerprint     json.RawMessage
	expectedSizeBytes     int64
	intent                string
	collisionPolicy       string
	renderSnapshot        json.RawMessage
	configSnapshot        json.RawMessage
	state                 string
	failureCode           string
	failureReason         string
	idempotencyKey        string
	boundVideoOperationID string
	correlationID         string
	overwriteSnapshot     json.RawMessage
	sourceParentIdentity  domain.FileIdentity
	targetParentIdentity  domain.FileIdentity
}

type operationRecord struct {
	id                    string
	artifactID            string
	artifactVersion       int64
	artifactFileID        string
	operation             string
	sourcePath            string
	targetPath            string
	sourceFingerprint     []byte
	targetFingerprint     []byte
	expectedSizeBytes     sql.NullInt64
	observedTargetSize    sql.NullInt64
	intent                string
	collisionPolicy       string
	renderSnapshot        []byte
	configSnapshot        []byte
	overwriteSnapshot     []byte
	publicationCheckpoint []byte
	state                 string
	observedAt            sql.NullTime
	failureCode           sql.NullString
	failureReason         sql.NullString
	idempotencyKey        string
	version               int64
	boundVideoOperationID sql.NullString
}
