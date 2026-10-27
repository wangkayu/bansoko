package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	facts "bansoko/internal/release/facts"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
	downloadadmission "bansoko/internal/download/admission"
	downloaddomain "bansoko/internal/download/domain"
	"bansoko/internal/events"
	"bansoko/internal/namingvars"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parser"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"
)

const (
	defaultMaxAttempts = 5
	defaultRetryAfter  = 5 * time.Minute
	evaluationVersion  = animedomain.EvaluatorVersion
)

const (
	rssCursorStrategyContentHash = "content_hash"
	rssCursorStrategyEnclosure   = "enclosure"
	rssCursorStrategyGuid        = "guid"
	rssCursorStrategyLink        = "link"

	rssLogKindReleaseProcessed = "release_processed"
	rssLogKindNewContent       = "rss_new_content"
	rssLogKindRefreshFailed    = "rss_refresh_failed"
	rssLogOutcomeFailure       = "failure"
	rssLogOutcomeInfo          = "info"
)

type rssCursor struct {
	Strategy string `json:"strategy"`
	Value    string `json:"value"`
}

type rssLastGoodFacts struct {
	FeedTitle           *string `json:"feed_title,omitempty"`
	ItemCount           int     `json:"item_count"`
	NewestStableItemKey string  `json:"newest_stable_item_key"`
}

type frozenPolicySnapshot struct {
	ConfigIdentity    animedomain.ConfigRevisionIdentity `json:"config_identity"`
	EffectiveDigest   string                             `json:"effective_digest"`
	EffectiveSections json.RawMessage                    `json:"effective_sections"`
	EvaluatorVersion  *string                            `json:"evaluator_version,omitempty"`
	MetadataVersion   int64                              `json:"metadata_version"`
	Parser            *parser.Provenance                 `json:"parser"`
	RulesVersion      int64                              `json:"rules_version"`
	Vars              *namingvars.FrozenEvaluation       `json:"vars,omitempty"`
	SectionSchemas    map[string]string                  `json:"section_schemas"`
	SectionVersions   map[string]int64                   `json:"section_versions"`
}

// SourceLocator is the typed execution snapshot for one RSS source. Provider
// references are facts persisted by discovery; transport references are
// explicit custom-RSS configuration and are never inferred from the URL.
type SourceLocator struct {
	Kind               animedomain.SourceKind
	URL                string
	HTTPFeedConnection string
	NetworkRoute       string
	Provider           string
	ProviderEntryRef   string
	ProviderGroupRef   string
}

// SourceFetcher is the production RSS transport port. It selects a provider
// adapter or custom RSS transport from the persisted source kind and facts.
type SourceFetcher interface {
	FetchSource(context.Context, SourceLocator) ([]acquisitiondomain.FeedItem, error)
}

// Service owns the RSS refresh and Release evaluation handlers. The
// WorkItem store supplies the fenced transaction that invokes owner writers.
type Service struct {
	db             *sql.DB
	workItems      *workitems.Store
	sourceFetcher  SourceFetcher
	retryAfter     time.Duration
	diagnostics    observabilityport.Recorder
	activityWriter observabilityport.ActivityWriter
	logger         observabilityport.RuntimeLogger
	eventHub       *events.Hub
	candidateFacts facts.Repository
	mediaFacts     facts.MediaRepository
	// targetProbe and targetStore form the deployment-owned pre-download
	// admission port. Targets are absolute by construction (the naming template
	// carries its own deployment path), so no library-root resolver participates.
	// Services constructed without SetTargetAcquisition retain the fixture-only
	// legacy path; once explicitly wired, missing capabilities fail closed.
	targetProbe                 target.Probe
	targetAcquisitionConfigured bool
	targetStore                 target.Store
	replacementAdmission        ReplacementAdmission
	downloaderAdmission         downloadadmission.Matcher
	// notifications admits release_discovered notices; nil admits none.
	notifications plugintargetport.BusinessDeliveryEnqueuer
	// competition decides the admissions of a subscription instance. Unset
	// keeps the fixture-only direct path where release evaluation admits
	// itself; production always wires it.
	competition CompetitionDecider
}

// ReplacementAdmission supplies a previously frozen retention decision to
// acquisition planning. It is intentionally a narrow port: RSS evaluation
// consumes only the per-key candidate intents and the read-only coverage
// verdicts that deduplicate a batch plan, and never owns replacement
// settlement. The plan coverage and file units travel with the request so the
// decisions are never blocked by a candidate-facts projection that only lands
// later.
type ReplacementAdmission interface {
	CoverageVerdicts(ctx context.Context, managedAnimeID, releaseID string, candidate target.CandidateEvidence) (map[string]batchplan.Verdict, error)
	CandidateIntents(ctx context.Context, managedAnimeID, releaseID string, evidence *target.CandidateEvidence) (map[string]target.Intent, error)
}

// SetCandidateFactsRepository wires the P3.2 versioned facts contract.
// Unset (nil) keeps evaluation behavior identical.
func (s *Service) SetCandidateFactsRepository(repo facts.Repository) {
	if s != nil {
		s.candidateFacts = repo
	}
}

// SetMediaFactsStore wires the P4 MediaFactSnapshot owner. Unset (nil) keeps
// evaluation behavior identical.
func (s *Service) SetMediaFactsStore(store facts.MediaRepository) {
	if s != nil {
		s.mediaFacts = store
	}
}

// SetTargetStore wires the persistence-backed frozen target owner. Target
// planning/probing remains outside the owner transaction; this setter only
// supplies the adapter-backed writer used by release evaluation.
func (s *Service) SetTargetStore(store target.Store) {
	if s != nil {
		s.targetStore = store
	}
}

// SetTargetAcquisition wires the target-based pre-download admission boundary.
// Targets render as absolute paths, so this boundary needs the filesystem probe
// and nothing else. Once enabled, an unavailable probe or a relative target is
// an admission failure rather than a legacy-path fallback.
func (s *Service) SetTargetAcquisition(probe target.Probe) {
	if s == nil {
		return
	}
	s.targetProbe = probe
	s.targetAcquisitionConfigured = true
}

// TargetAcquisitionReady reports whether the mandatory production pre-download
// target boundary is fully wired: a filesystem probe and the
// persistence-backed frozen target owner. The composition root asserts this at
// startup so a production boot can never silently fall back to the legacy
// fail-open admission path.
func (s *Service) TargetAcquisitionReady() bool {
	return s != nil && s.targetAcquisitionConfigured && s.targetProbe != nil && s.targetStore.Configured()
}

// SetReplacementAdmission wires the optional pre-acquisition replacement
// decision reader. When absent, ordinary releases keep ensure_present intent.
func (s *Service) SetReplacementAdmission(admission ReplacementAdmission) {
	if s != nil {
		s.replacementAdmission = admission
	}
}

func (s *Service) SetDownloaderAdmission(matcher downloadadmission.Matcher) {
	if s != nil {
		s.downloaderAdmission = matcher
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

// NewService constructs the M1 RSS application service. The SourceFetcher is
// the single production transport port; there is no URL-only fallback path.
func NewService(db *sql.DB, workItems *workitems.Store, sourceFetcher SourceFetcher) (*Service, error) {
	if db == nil {
		return nil, errors.New("rss service database is nil")
	}
	if workItems == nil {
		return nil, errors.New("rss service work item store is nil")
	}
	if sourceFetcher == nil {
		return nil, errors.New("rss service source fetcher is nil")
	}
	return &Service{
		db:            db,
		workItems:     workItems,
		sourceFetcher: sourceFetcher,
		retryAfter:    defaultRetryAfter,
		// Every evaluation freezes the Release media fact snapshot;
		// SetMediaFactsStore may replace the store.
		mediaFacts: facts.NewStore(db),
	}, nil
}

// Handle routes only the two M1 RSS kinds. It intentionally has no periodic
// successor behavior; the fixed automation dispatcher owns future refreshes.
func (s *Service) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if s == nil || s.db == nil || s.workItems == nil || s.sourceFetcher == nil {
		return worker.Outcome{}, errors.New("rss service is not configured")
	}
	if ctx == nil {
		return worker.Outcome{}, errors.New("rss handler context is nil")
	}
	var outcome worker.Outcome
	var err error
	switch claim.Kind {
	case workitems.KindRSSRefresh:
		outcome, err = s.handleRefresh(ctx, claim)
		if err == nil {
			outcome = s.withRefreshCompetition(outcome, claim.Owner.ID)
		}
	case workitems.KindReleaseEvaluate:
		outcome, err = s.handleReleaseEvaluate(ctx, claim)
		if err == nil {
			outcome = s.withCompetitionFollowUp(outcome, s.releaseManagedAnimeID(ctx, claim))
		}
	default:
		return worker.Outcome{}, fmt.Errorf("unsupported RSS work item kind %q", claim.Kind)
	}
	if err != nil {
		return outcome, err
	}
	return s.withEvent(outcome, events.TargetRSS, claim.ManagedAnimeID), nil
}

func (s *Service) withEvent(outcome worker.Outcome, target, managedAnimeID string) worker.Outcome {
	if s == nil || s.eventHub == nil {
		return outcome
	}
	outcome.AfterCommit = s.eventHub.AfterCommitFor(target, managedAnimeID, outcome.AfterCommit)
	return outcome
}

var _ worker.Handler = (*Service)(nil)

// RSSRefreshHandler is the typed handler registered for rss_refresh claims.
type RSSRefreshHandler struct{ service *Service }

func (h RSSRefreshHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if h.service == nil {
		return worker.Outcome{}, errors.New("RSS refresh service is nil")
	}
	if claim.Kind != workitems.KindRSSRefresh {
		return worker.Outcome{}, fmt.Errorf("RSS refresh handler received %q", claim.Kind)
	}
	return h.service.Handle(ctx, claim)
}

// ReleaseEvaluateHandler is the typed handler registered for release_evaluate claims.
type ReleaseEvaluateHandler struct{ service *Service }

func (h ReleaseEvaluateHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if h.service == nil {
		return worker.Outcome{}, errors.New("release evaluation service is nil")
	}
	if claim.Kind != workitems.KindReleaseEvaluate {
		return worker.Outcome{}, fmt.Errorf("release evaluation handler received %q", claim.Kind)
	}
	return h.service.Handle(ctx, claim)
}

// RSSRefreshHandler returns the source-owner handler for rss_refresh.
func (s *Service) RSSRefreshHandler() worker.Handler {
	return RSSRefreshHandler{service: s}
}

// ReleaseEvaluateHandler returns the release-owner handler for release_evaluate.
func (s *Service) ReleaseEvaluateHandler() worker.Handler {
	return ReleaseEvaluateHandler{service: s}
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type sourceSnapshot struct {
	sourceID           string
	subscriptionID     string
	managedAnimeID     string
	sourceKind         animedomain.SourceKind
	locator            string
	locatorDisplay     string
	httpFeedConnection string
	networkRoute       string
	provider           string
	providerEntryRef   string
	providerGroupRef   string
	groupName          string
	title              string
	episodeOffset      int
	subscriptionState  string
	selectionExists    bool
	providerSelected   bool
	runtimeVersion     int64
	cursorJSON         []byte
	lastGoodJSON       []byte
	// etag/lastModified are the validators of the last complete successful
	// fetch; they make the next refresh a conditional request.
	etag         string
	lastModified string
	// consecutiveFailures is the source's failure streak; the next retry
	// delay is subscriptiondomain.RefreshBackoff(consecutiveFailures+1).
	consecutiveFailures int
	// resumeBaselinePending reports that the subscription was resumed after
	// this source's runtime existed and no successful refresh has recorded the
	// baseline since. The owner writer re-reads it under the subscription lock.
	resumeBaselinePending bool
	// newReleasesOnly/startBaselineAt are the subscription's frozen new-releases-only
	// gate. A refresh does not fetch the .torrent of an item the gate covers:
	// its Release takes start_baseline without any rule, and an explicit retry
	// fetches it then.
	newReleasesOnly bool
	startBaselineAt sql.NullTime
	metadataVersion int64
	rulesVersion    int64
	// parserKind comes from the instance parser policy owner; the one offset is
	// episodeOffset, the RSS link's own offset. profileVersion is the resolved
	// effective parser-section version supplying the patterns.
	parserKind     string
	parserVersion  int64
	profileVersion int64
	// sourceVersion is the RSS source CAS version episodeOffset was read from;
	// it is frozen provenance evidence.
	sourceVersion int64
	metadataHold  animedomain.MetadataHold
	// mediaKind/mediaSeason/mediaSeasonBound are the frozen metadata binding
	// (TMDB media kind + bound season) one release identity resolves with;
	// mediaKind "movie" short-circuits the resolver to the canonical movie:0
	// identity. They are read at snapshot load and re-frozen into the media
	// fact snapshot, never re-read from anime_metadata downstream.
	mediaKind        string
	mediaSeason      int
	mediaSeasonBound bool
	effective        animedomain.EffectiveConfig
}

type releaseSnapshot struct {
	releaseID         string
	sourceID          string
	subscriptionID    string
	managedAnimeID    string
	animeTitle        string
	stableItemKey     string
	rawFacts          []byte
	sourceFacts       []byte
	parsedFacts       []byte
	internalFacts     []byte
	verdict           string
	reasonCode        string
	reasonDetail      sql.NullString
	evaluationVersion sql.NullString
	downloadTaskID    sql.NullString
	releaseVersion    int64
	locator           string
	locatorDisplay    string
	groupName         string
	episodeOffset     int
	subscriptionState string
	anibtMode         string
	sourceKind        string
	// sourceProvider/sourceGroupRef are the provider group identity of a
	// provider_group source (null for custom RSS).
	sourceProvider  sql.NullString
	sourceGroupRef  sql.NullString
	metadataVersion int64
	rulesVersion    int64
	parserKind      string
	parserVersion   int64
	// parserPolicyError records that the release's ManagedAnime has no usable
	// instance_parser_policies row. The evaluation path checks it after the
	// already-submitted/terminal early returns and fails the item closed with
	// the typed parser-policy-required reason instead of degrading to
	// regex. Already-submitted work is therefore still settled.
	parserPolicyError error
	// profileVersion is the resolved effective parser-section version that
	// supplied the episode patterns.
	profileVersion int64
	// sourceVersion is the RSS source CAS version episodeOffset was read from;
	// it is frozen provenance evidence.
	sourceVersion int64
	metadataHold  animedomain.MetadataHold
	// mediaKind/mediaSeason/mediaSeasonBound are the frozen metadata binding
	// (TMDB media kind + bound season) one release identity resolves with;
	// mediaKind "movie" short-circuits the resolver to the canonical movie:0
	// identity. Work IDs are frozen alongside them so downstream naming never
	// re-reads anime_metadata after submission.
	mediaKind        string
	mediaSeason      int
	mediaSeasonBound bool
	bgmWorkID        string
	tmdbWorkID       string
	effective        animedomain.EffectiveConfig
	resolved         animedomain.ResolvedSections
	// Retry facts: the owner lifecycle, the state of the current download
	// task and of its acquisition, and how many download tasks the Release
	// has owned (the next acceptance is download attempt taskCount+1).
	lifecycleState   string
	taskState        string
	acquisitionState string
	taskCount        int
	// manualRetry marks an evaluation run for the explicit retry command
	// instead of the release_evaluate worker.
	manualRetry bool
	// newReleasesOnly/startBaselineAt are the instance's frozen new-releases-only
	// option and its baseline (the instance creation time).
	newReleasesOnly bool
	startBaselineAt sql.NullTime
	// redownload marks a manual retry run for a placement redownload: a
	// Release that already placed its files may run again.
	redownload bool
	// placementMissing is the PlacementMissing retry fact
	// (ReleasePlacementIssueSQL).
	placementMissing bool
	// competitionDecision is set only when the subscription competition runs
	// the decided stage of this evaluation: its per-key verdicts replace the
	// read-only coverage verdicts of the batch plan deduplication.
	competitionDecision *competition.Decision
}

// downloadAttempt is the ordinal of the download the next acceptance creates.
func (s releaseSnapshot) downloadAttempt() int { return s.taskCount + 1 }

// admitsWork says whether this evaluation may accept the Release: the
// automatic path only for an active subscription, an explicit retry also for
// a stopped one.
func (s releaseSnapshot) admitsWork(subscriptionState string) bool {
	// A manual retry is admitted for a stopped subscription only; a pending
	// (never started) one does no work.
	return subscriptionState == "active" || (s.manualRetry && subscriptionState == "stopped")
}

func (s releaseSnapshot) retryFacts() ReleaseRetryFacts {
	return ReleaseRetryFacts{Verdict: s.verdict, DownloadTaskState: s.taskState, AcquisitionState: s.acquisitionState, LifecycleState: s.lifecycleState, SubscriptionState: s.subscriptionState, PlacementMissing: s.placementMissing}
}

type feedEvaluation struct {
	item                  acquisitiondomain.FeedItem
	result                animedomain.PreviewReleaseResult
	stableKind            string
	stableKey             string
	rawFacts              []byte
	sourceFacts           []byte
	parsedFacts           []byte
	internalFacts         []byte
	torrentMetadataReused bool
}

type storedFeedFacts struct {
	GUID               string              `json:"guid,omitempty"`
	Title              string              `json:"title"`
	Link               string              `json:"link,omitempty"`
	Description        string              `json:"description,omitempty"`
	Enclosure          string              `json:"enclosure,omitempty"`
	EnclosureType      string              `json:"enclosure_type,omitempty"`
	EnclosureLength    int64               `json:"enclosure_length,omitempty"`
	TorrentURL         string              `json:"torrent_url,omitempty"`
	TorrentInfoHash    string              `json:"torrent_info_hash,omitempty"`
	TorrentInfoHashV1  string              `json:"torrent_info_hash_v1,omitempty"`
	TorrentInfoHashV2  string              `json:"torrent_info_hash_v2,omitempty"`
	TorrentVersion     string              `json:"torrent_version,omitempty"`
	TorrentFilename    string              `json:"torrent_filename,omitempty"`
	TorrentSizeBytes   int64               `json:"torrent_size_bytes,omitempty"`
	TorrentErrorCode   string              `json:"torrent_error_code,omitempty"`
	TorrentErrorReason string              `json:"torrent_error_reason,omitempty"`
	DownloadURL        string              `json:"download_url,omitempty"`
	Published          *time.Time          `json:"published_at,omitempty"`
	Provider           string              `json:"provider,omitempty"`
	ReleaseFiles       []storedReleaseFile `json:"release_files,omitempty"`
}

type storedReleaseFile struct {
	SourcePath       string                              `json:"source_path"`
	Filename         string                              `json:"filename"`
	SizeBytes        *int64                              `json:"size_bytes,omitempty"`
	MediaKind        string                              `json:"media_kind,omitempty"`
	SourceHash       string                              `json:"source_hash,omitempty"`
	Facts            map[string]any                      `json:"facts,omitempty"`
	TargetPath       *string                             `json:"target_path,omitempty"`
	TargetDiagnostic *acquisitiondomain.TargetDiagnostic `json:"target_diagnostic,omitempty"`
}

type storedSourceFacts struct {
	GroupName     string `json:"group_name,omitempty"`
	IdentityKind  string `json:"identity_kind"`
	IdentityValue string `json:"identity_value"`
	// AniBT is the frozen AniBT structured tag set; present only for releases
	// observed through an AniBT source.
	AniBT *StoredAniBTTags `json:"anibt,omitempty"`
	// Title is the frozen title-parse tag set (every release observed after
	// the title row existed), with the parser version and dictionary digest
	// it was read with.
	Title *StoredTitleTags `json:"title_tags,omitempty"`
}

// StoredTitleTags and StoredAniBTTags are the persisted (public) tag block
// shapes inside Release.source_facts, owned by the acquisition domain so every
// reader decodes one shape.
type (
	StoredTitleTags = acquisitiondomain.StoredTitleTags
	StoredAniBTTags = acquisitiondomain.StoredAniBTTags
)

type selectedReleaseIdentity struct {
	ReleaseID      string          `json:"release_id"`
	RSSSourceID    string          `json:"rss_source_id"`
	StableItemKey  string          `json:"stable_item_key"`
	RawFacts       json.RawMessage `json:"raw_facts"`
	SourceFacts    json.RawMessage `json:"source_facts"`
	InternalFacts  json.RawMessage `json:"internal_facts"`
	ReleaseVersion int64           `json:"release_version"`
	SubscriptionID string          `json:"subscription_id"`
	ManagedAnimeID string          `json:"managed_anime_id"`
}

type frozenTargetFacts struct {
	Admission       string               `json:"admission"`
	Reason          string               `json:"reason,omitempty"`
	TargetSetDigest string               `json:"target_set_digest,omitempty"`
	Targets         []target.Spec        `json:"targets"`
	Observations    []target.Observation `json:"observations"`
}

type internalFrozenDownloadInput struct {
	ReleaseStableItemKey string                        `json:"release_stable_item_key"`
	SelectedRelease      selectedReleaseIdentity       `json:"selected_release"`
	TargetFacts          *frozenTargetFacts            `json:"target_facts,omitempty"`
	TargetSetDigest      string                        `json:"target_set_digest,omitempty"`
	SourceLocatorDisplay string                        `json:"source_locator_display"`
	TorrentInputID       string                        `json:"torrent_input_id"`
	TorrentPayloadDigest string                        `json:"torrent_payload_digest"`
	TorrentFilename      string                        `json:"torrent_filename"`
	TorrentParserVersion string                        `json:"torrent_parser_version"`
	TorrentVersion       string                        `json:"torrent_version"`
	InfoHash             string                        `json:"info_hash,omitempty"`
	InfoHashV1           string                        `json:"info_hash_v1,omitempty"`
	InfoHashV2           string                        `json:"info_hash_v2,omitempty"`
	ReleaseFiles         []storedReleaseFile           `json:"release_files,omitempty"`
	PathSnapshot         *downloaddomain.PathSnapshot  `json:"path_snapshot,omitempty"`
	AdoptionProvenance   *downloadadmission.Provenance `json:"adoption_provenance,omitempty"`
}
