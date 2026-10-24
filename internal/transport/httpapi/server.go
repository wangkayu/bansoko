// Package httpapi is the HTTP transport. It registers exactly the
// paths declared in api/openapi.yaml, maps typed application results onto
// the generated contract types, and owns the HTTP error boundary.
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	accessapplication "bansoko/internal/accesscontrol/application"
	"bansoko/internal/anime/animeconfig"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	automationapplication "bansoko/internal/automation/application"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/events"
	filemaintenance "bansoko/internal/filemaintenance"
	filemaintenanceplanner "bansoko/internal/filemaintenance/planner"
	"bansoko/internal/filemanager"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parserpolicy"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	"bansoko/internal/postprocessing/naming"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/release/torrentstructure"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/runtime/maintenance"
	"bansoko/internal/runtime/readiness"
	templatecatalogapplication "bansoko/internal/templatecatalog/application"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/transport/mcp"
	updateapplication "bansoko/internal/update/application"
)

// DraftService is the candidate-draft boundary: candidate CRUD, immutable
// revisions, and the atomic Subscribe/activation commands. It is implemented by
// internal/anime/draft/application.Service and injected separately from the
// kernel, ownership and read-model owners, so a draft command cannot silently
// return to the kernel service.
type DraftService interface {
	ApplyAnimeConfigCandidate(ctx context.Context, candidateID, key string, input draftapplication.CandidateApplyInput) (draftapplication.CandidateApplyResult, error)
	GetAnimeBusinessEdit(ctx context.Context, animeID string) (draftapplication.BusinessEditView, error)
	SubscribeAnimeApplication(ctx context.Context, input draftapplication.SubscribeAnimeInput, idempotencyKey string) (draftapplication.SubscribeResult, error)
	ListAnimeConfigCandidates(ctx context.Context, params draftapplication.ListAnimeConfigCandidatesInput) ([]draftapplication.AnimeConfigCandidate, error)
	GetAnimeConfigCandidate(ctx context.Context, candidateID string) (draftapplication.AnimeConfigCandidate, error)
	SaveAnimeConfigCandidate(ctx context.Context, input draftapplication.CandidateDraftInput) (draftapplication.AnimeConfigCandidate, error)
	PatchAnimeConfigCandidate(ctx context.Context, candidateID string, expectedVersion int64, input draftapplication.CandidateDraftInput) (draftapplication.AnimeConfigCandidate, error)
	DeleteAnimeConfigCandidate(ctx context.Context, candidateID string) error
	ActivateAnimeConfigCandidate(ctx context.Context, candidateID string, expectedVersion int64, idempotencyKey string, input draftapplication.CandidateSubscribeInput) (draftapplication.SubscribeResult, error)
	DuplicateAnimeSubscription(ctx context.Context, sourceAnimeID string, input draftapplication.DuplicateSubscriptionInput, idempotencyKey string) (draftapplication.SubscribeResult, error)
}

// OwnershipService is the anime owner-mutation boundary. It is implemented by
// internal/anime/ownership/application.Service and injected separately from
// the draft/candidate pipeline, so owner CAS writes, retirement, clean up,
// discovery and metadata mutation cannot silently return to
// the root anime service.
type OwnershipService interface {
	ReevaluateAnimeReleasesApplication(ctx context.Context, managedAnimeID string) (ownershipapplication.ReleaseReevaluationResult, error)
	PatchAnimeMetadataApplication(ctx context.Context, managedAnimeID string, patch ownershipapplication.MetadataPatchInput) (ownershipapplication.AnimeMetadata, error)
	// RefreshAnimeMetadataApplication re-pulls the bound works bypassing the
	// positive caches (the 刷新元数据 action); the answer is the unchanged
	// metadata owner view.
	RefreshAnimeMetadataApplication(ctx context.Context, managedAnimeID string) (ownershipapplication.AnimeMetadata, error)
	MatchAnimeMetadata(ctx context.Context, managedAnimeID string, input ownershipapplication.MetadataMatchInput, idempotencyKey string) (ownershipapplication.MetadataMatchResult, error)
	PatchAnimeSubgroupsApplication(ctx context.Context, managedAnimeID string, patch ownershipapplication.SubgroupsPatchInput) (ownershipapplication.SubgroupsOwnerResult, error)
	DiscoverAnimeSubgroups(ctx context.Context, managedAnimeID string, input ownershipapplication.DiscoverSubgroupsInput) (ownershipapplication.DiscoverSubgroupsResult, error)
	PatchAnimeRules(ctx context.Context, managedAnimeID string, patch ownershipapplication.RulesPatchInput) (ownershipapplication.RulesPatchResult, error)
	PatchAnimeSubscription(ctx context.Context, managedAnimeID string, patch ownershipapplication.SubscriptionPatchInput) (ownershipapplication.Subscription, error)
	SetNewReleasesOnly(ctx context.Context, managedAnimeID string, input ownershipapplication.NewReleasesOnlyInput) (ownershipapplication.Subscription, error)
	DeleteAnimeSubscription(ctx context.Context, managedAnimeID string, input ownershipapplication.SubscriptionDeleteInput) error
	CleanupAnimeLocalData(ctx context.Context, managedAnimeID string, input ownershipapplication.LocalDataCleanupInput, idempotencyKey string) error
	PatchRSSSourceOffset(ctx context.Context, managedAnimeID, rssSourceID string, patch ownershipapplication.RSSSourceOffsetPatchInput) (ownershipapplication.RSSSource, error)
	PatchRSSSourceAniBTMode(ctx context.Context, managedAnimeID, rssSourceID string, patch ownershipapplication.RSSSourceAniBTModePatchInput) (ownershipapplication.RSSSource, error)
	RefreshRSSSource(ctx context.Context, rssSourceID, idempotencyKey string) (ownershipapplication.RSSRefreshAccepted, error)
	EffectiveNamingRules(ctx context.Context, managedAnimeID string) (naming.NamingRules, error)
}

// ReadmodelService owns the side-effect-free anime read projections.
type ReadmodelService interface {
	ListAnimeApplication(ctx context.Context) ([]readmodelapplication.AnimeListItem, error)
	ReadAnimeRSSListView(ctx context.Context, input readmodelapplication.ReadRSSListViewInput) (readmodelapplication.RSSListView, error)
	PreviewSubgroupsSelection(ctx context.Context, input readmodelapplication.SubgroupsSelectionPreviewInput) (readmodelapplication.SubgroupsSelectionPreview, error)
	GetAnimeWorkspaceApplication(ctx context.Context, managedAnimeID string) (readmodelapplication.AnimeWorkspace, error)
	// Execution records: the read-only list, detail and file page of
	// accepted acquisitions.
	ListAcquisitionRecords(ctx context.Context, query readmodelapplication.AcquisitionRecordQuery) (readmodelapplication.AcquisitionRecordPage, error)
	GetAcquisitionRecord(ctx context.Context, acquisitionID string) (readmodelapplication.AcquisitionRecordDetail, error)
	ListAcquisitionRecordFiles(ctx context.Context, acquisitionID string, query readmodelapplication.AcquisitionFileQuery) (readmodelapplication.AcquisitionFilePage, error)
}

// ReleaseRetryService runs one Release through normal acceptance again. It is
// transport-neutral so HTTP and MCP adapters project generated DTOs at egress.
type ReleaseRetryService interface {
	RetryRelease(ctx context.Context, releaseID, idempotencyKey string) (rssapplication.ReleaseRetryResult, error)
	// RedownloadPlacement runs a missing or mismatched placement's Release
	// again as a placement redownload.
	RedownloadPlacement(ctx context.Context, placementID, idempotencyKey string) (rssapplication.ReleaseRetryResult, error)
}

// OneShotService is the finite one-shot acquisition owner. It is implemented by
// internal/anime/oneshot/application.Service and injected separately from the
// root anime pipeline, so one-shot ownership cannot silently return to the root
// service.
type OneShotService interface {
	// DownloadSnapshotSeed admits one finite download for one seed that already
	// exists in an exact persisted provider feed snapshot.
	DownloadSnapshotSeed(ctx context.Context, input oneshotapplication.SnapshotSeedDownloadInput, idempotencyKey string) (oneshotapplication.OneShotAdmissionResult, error)
	AcquireSeedOnce(ctx context.Context, input oneshotapplication.AcquireSeedInput, requestIdempotencyKey string) (oneshotapplication.OneShotAdmissionResult, error)
	ReadOneShotAcquisition(ctx context.Context, oneShotID string) (oneshotapplication.OneShotAcquisitionView, error)
	// ListOneShotExecutionAttempts answers one sequence-DESC keyset page.
	ListOneShotExecutionAttempts(ctx context.Context, oneShotID string, limit int, cursor string) (oneshotapplication.OneShotAttemptPage, error)
	// GetOneShotExecutionAttempt answers one redacted attempt detail scoped to
	// its acquisition.
	GetOneShotExecutionAttempt(ctx context.Context, oneShotID, attemptID string) (oneshotapplication.OneShotExecutionAttempt, error)
}

// MediaRecognitionService is the side-effect-free parser/identity capability
// shared by Release, FileManager, and Agent transports.
type MediaRecognitionService interface {
	Preview(ctx context.Context, input generated.MediaRecognitionPreviewInput) (generated.MediaRecognitionPreviewResult, error)
	ListParsers(ctx context.Context) ([]generated.ParserCatalogEntry, error)
}

// ConnectionConfigService is the typed connection/route owner boundary,
// projected to generated HTTP values only by the transport handlers. It is
// implemented by internal/configuration/application.HTTPService.
type ConnectionConfigService interface {
	ListConnections(ctx context.Context) ([]configurationdomain.Connection, error)
	PutConnection(ctx context.Context, connectionID string, expectedVersion int64, input configurationdomain.ConnectionInput) (configurationdomain.Connection, error)
	TestConnection(ctx context.Context, connectionID string) (configurationdomain.ConnectionTestResult, error)
	ListNetworkRoutes(ctx context.Context) ([]configurationdomain.NetworkRoute, error)
	PutNetworkRoute(ctx context.Context, routeID string, expectedVersion int64, input configurationdomain.NetworkRouteInput) (configurationdomain.NetworkRoute, error)
	TestNetworkRoute(ctx context.Context, routeID string) (configurationdomain.NetworkRouteTestResult, error)
}

// ManualRetryService creates a new owner-specific WorkItem from a terminal
// failed item. The caller never supplies an external-effect key.
type ManualRetryService interface {
	ManualRetry(ctx context.Context, ownerType, ownerID, failedWorkItemID, reason, requestIdempotencyKey string) error
}

// AutomationService owns the typed automation schedule rows and manual
// dispatch-run lifecycle. It is implemented by internal/automation/application.
type AutomationService interface {
	ListSchedules(ctx context.Context) ([]automationapplication.Schedule, error)
	PatchSchedule(ctx context.Context, kind string, patch automationapplication.SchedulePatch) (automationapplication.Schedule, error)
	RunNow(ctx context.Context, kind, idempotencyKey string) (automationapplication.RunAccepted, error)
	GetRun(ctx context.Context, runID string) (automationapplication.DispatchRun, error)
}

// GlobalSectionService owns the typed global config section rows. It is
// implemented by internal/configuration/application.
type GlobalSectionService interface {
	ReadBusinessPreset(ctx context.Context) (animeconfig.BusinessSnapshot, error)
	ReadGlobalTagFilters(ctx context.Context) (json.RawMessage, error)
	ListGlobalSections(ctx context.Context) ([]configurationdomain.GlobalSectionView, error)
	PutGlobalSection(ctx context.Context, sectionName string, expectedVersion int64, input configurationdomain.GlobalSectionInput) (configurationdomain.GlobalSectionView, error)
	GetNamingTemplateLibrary(ctx context.Context) (configurationdomain.NamingTemplateLibraryView, error)
	PutNamingTemplateLibrary(ctx context.Context, expectedVersion int64, input configurationdomain.NamingTemplateLibraryInput) (configurationdomain.NamingTemplateLibraryView, error)
}

// MetadataSearchService searches external metadata providers for title
// candidates. It is implemented by an adapter around the configured
// identity-metadata candidate searcher.
type MetadataSearchService interface {
	SearchMetadata(ctx context.Context, provider, query string, limit int) ([]identitymetadatadomain.SearchCandidate, error)
}

// ObservabilityService is the read-only observability projection boundary.
// It is implemented by internal/observability/application.
type ObservabilityService interface {
	ListRecords(ctx context.Context, params observabilityapplication.RecordQuery) (observabilityapplication.RecordPage, error)
	ListDiagnostics(ctx context.Context, params observabilityapplication.DiagnosticQuery) (observabilityapplication.DiagnosticPage, error)
}

// PluginTargetService owns the plugin target configuration rows and their test
// deliveries. It is implemented by internal/plugintarget/application.
type PluginTargetService interface {
	ListTargets(ctx context.Context) (plugintargetapplication.TargetPage, error)
	CreateTarget(ctx context.Context, input plugintargetapplication.TargetInput, idempotencyKey string) (plugintargetapplication.TargetConfig, error)
	UpdateTarget(ctx context.Context, targetID string, expectedVersion int64, input plugintargetapplication.TargetUpdateInput) (plugintargetapplication.TargetConfig, error)
	TestTarget(ctx context.Context, targetID string, input plugintargetapplication.TargetTestInput, idempotencyKey string) (plugintargetapplication.TestAccepted, error)
	DeleteTarget(ctx context.Context, targetID string) error
	GetDelivery(ctx context.Context, deliveryID string) (plugintargetapplication.DeliveryDetail, error)
	ListDeliveries(ctx context.Context, params plugintargetapplication.DeliveryQuery) (plugintargetapplication.DeliveryPage, error)
}

// EmbeddedUpdateService owns the container-embedded self-update channel
// (BANSOKO_UPDATE_MODE=embedded). It is implemented by
// internal/update/application and is nil when the deployment runs another
// update mode: the surface then degrades instead of blocking the core.
type EmbeddedUpdateService interface {
	AdmitUpdate(ctx context.Context, input updateapplication.AdmitInput, idempotencyKey string) (updateapplication.Status, bool, error)
	CheckUpdate(ctx context.Context) (updateapplication.Check, error)
	GetUpdateStatus(ctx context.Context) (updateapplication.Status, bool, error)
}

// TemplateCatalogService serves the closed template variable/function catalogs
// and the side-effect-free preview renderer. It is implemented by
// internal/templatecatalog/application.
type TemplateCatalogService interface {
	GetNotificationCatalog(ctx context.Context, triggerKind, catalogVersion string) (templatecatalogapplication.NotificationTemplateCatalog, error)
	GetNamingCatalog(ctx context.Context, language string) (templatecatalogapplication.NamingTemplateCatalog, error)
	PreviewNotification(ctx context.Context, input templatecatalogapplication.NotificationTemplatePreviewInput) (templatecatalogapplication.NotificationTemplatePreview, error)
	PreviewNaming(ctx context.Context, input templatecatalogapplication.NamingTemplatePreviewInput) (templatecatalogapplication.NamingTemplatePreview, error)
}

// FileMaintenanceService is the policy/replan boundary (P3.3).
type FileMaintenanceService interface {
	GetPolicyApplication(ctx context.Context, animeID string) (filemaintenance.PolicyView, error)
	SavePolicyApplication(ctx context.Context, animeID string, input filemaintenance.PolicyInput, actor, channel string) (filemaintenance.PolicyView, error)
	GetRetentionPolicyDefaultApplication(ctx context.Context) (filemaintenance.RetentionPolicyDefaultView, error)
	CompareVocabularyApplication(ctx context.Context, animeID string) (map[string][]string, error)
	SaveRetentionPolicyDefaultApplication(ctx context.Context, input filemaintenance.RetentionPolicyWriteInput, actor, channel string) (filemaintenance.RetentionPolicyDefaultView, error)
	GetInstanceRetentionPolicyApplication(ctx context.Context, animeID string) (filemaintenance.InstanceRetentionPolicyView, error)
	SaveInstanceRetentionPolicyApplication(ctx context.Context, animeID string, input filemaintenance.RetentionPolicyWriteInput, actor, channel string) (filemaintenance.InstanceRetentionPolicyView, error)
	GetInstanceParserPolicy(ctx context.Context, animeID string) (parserpolicy.Instance, error)
	SaveInstanceParserPolicy(ctx context.Context, animeID string, input filemaintenance.ParserPolicyInput) (parserpolicy.Instance, error)
	GetDefaultParserPolicy(ctx context.Context) (filemaintenance.ParserPolicyDefaultView, error)
	SaveDefaultParserPolicy(ctx context.Context, input filemaintenance.ParserPolicyDefaultInput) (filemaintenance.ParserPolicyDefaultView, error)
	GetFileEffectPolicyDefault(ctx context.Context) (filemaintenance.FileEffectPolicyDefaultView, error)
	SaveFileEffectPolicyDefault(ctx context.Context, input filemaintenance.FileEffectPolicyDefaultInput) (filemaintenance.FileEffectPolicyDefaultView, error)
	GetInstanceFileEffectPolicy(ctx context.Context, animeID string) (filemaintenance.InstanceFileEffectPolicyView, error)
	SaveInstanceFileEffectPolicy(ctx context.Context, animeID string, input filemaintenance.InstanceFileEffectPolicyInput) (filemaintenance.InstanceFileEffectPolicyView, error)
	ReplanApplication(ctx context.Context, input filemaintenance.ReplanInput) (filemaintenanceplanner.PlanResult, error)
	ListPlacementViews(ctx context.Context, animeID string, limit, offset int) ([]filemaintenance.PlacementView, error)
	GetPlacementCoverageViews(ctx context.Context, placementID string) ([]filemaintenance.PlacementCoverageView, error)
	ListOffsetMigrationViews(ctx context.Context, animeID string, policyVersion *int64, limit, offset int) ([]filemaintenance.OffsetMigrationPlanView, error)
	ListOffsetMigrationGenerationViews(ctx context.Context, animeID string, limit, offset int) ([]filemaintenance.OffsetMigrationGenerationView, error)
	ListReconciliationPathDiffs(ctx context.Context, animeID string, generation int64, limit, offset int) ([]filemaintenance.PathDiffView, error)
	ListDecisionViews(ctx context.Context, animeID string, limit, offset int) ([]filemaintenance.ReplacementDecisionView, error)
	EvaluateDecisionApplication(ctx context.Context, input filemaintenance.EvaluateRequest) ([]filemaintenance.ReplacementDecisionView, error)
	GetOverviewApplication(ctx context.Context, animeID string) (filemaintenance.OverviewView, error)
	ListPlacementIssuesApplication(ctx context.Context, animeID string) ([]filemaintenance.PlacementIssueView, error)
	ReleasePlacementApplication(ctx context.Context, placementID, idempotencyKey string) (filemaintenance.PlacementReleaseResult, error)
	ListAnimeFilesApplication(ctx context.Context, animeID string) (filemaintenance.AnimeFilesView, error)
	AlignAnimeFilesApplication(ctx context.Context, animeID string, generation int64, placementIDs []string) (filemaintenance.AnimeFilesAlignResult, error)
	// AlignAnimeFilesWithRefreshApplication is the 对齐（刷新元数据）variant: a
	// positive-cache re-pull before the alignment is admitted (the adapter
	// shares the ownership application's re-pull).
	AlignAnimeFilesWithRefreshApplication(ctx context.Context, animeID string, generation int64, placementIDs []string) (filemaintenance.AnimeFilesAlignResult, error)
}

// CandidateFactsService serves the versioned release candidate facts.
type CandidateFactsService interface {
	GetFacts(ctx context.Context, releaseID string, version *int) (facts.View, error)
}

// TorrentStructureService serves the structure of a release's retained
// torrent payload.
type TorrentStructureService interface {
	Get(ctx context.Context, releaseID string) (torrentstructure.View, error)
}

// ReconciliationService is the align-already-downloaded-files owner boundary.
// It is implemented by internal/reconciliation/application.
type ReconciliationService interface {
	SubmitScan(ctx context.Context, input reconciliationapplication.ScanSubmitInput) (reconciliationapplication.ScanView, error)
	GetScan(ctx context.Context, animeID string) (reconciliationapplication.ScanView, error)
	ListObservations(ctx context.Context, animeID string, generation int64, limit, offset int) (reconciliationapplication.ObservationPage, error)
	GenerationUsable(ctx context.Context, animeID string, generation int64) (bool, error)
}

// QBManagementService is the qBittorrent operational passthrough boundary.
// It proxies the qB WebUI API for the built-in management dialog and never
// mutates DownloadTask owner truth. It is implemented by
// internal/configuration/application.QBManagementService.
type QBManagementService interface {
	ListTorrents(ctx context.Context, connectionID string) ([]configurationdomain.QBittorrentTorrentView, error)
	PauseTorrent(ctx context.Context, connectionID, hash string) error
	ResumeTorrent(ctx context.Context, connectionID, hash string) error
	DeleteTorrent(ctx context.Context, connectionID, hash string, deleteFiles bool) error
	GetTorrentFiles(ctx context.Context, connectionID, hash string) ([]configurationdomain.QBittorrentTorrentFileView, error)
	GetServerStatus(ctx context.Context, connectionID string) (configurationdomain.QBittorrentServerStatusView, error)
}

// FileManagerService is the file manager: quick-entry mounts over the
// absolute paths the process can reach, bounded directory pages with derived
// ownership states and locks, entry detail, an anime's directory (P0,
// read-only), and the P1 commands — user file operations on detached files,
// release from management, and align. It is implemented by
// internal/filemanager.
type FileManagerService interface {
	Mounts(ctx context.Context) ([]filemanager.Mount, error)
	List(ctx context.Context, path, cursor string, limit int) (filemanager.Listing, error)
	Detail(ctx context.Context, path string) (filemanager.Detail, error)
	Locate(ctx context.Context, managedAnimeID string) (filemanager.Location, error)
	// File-manager writes; each re-derives the entry and checks its revision.
	SubmitOperation(ctx context.Context, request filemanager.OperationRequest) (filemanager.UserOperation, error)
	Operation(ctx context.Context, id string) (filemanager.UserOperation, error)
	Release(ctx context.Context, request filemanager.ReleaseRequest) (filemanager.ReleaseOutcome, error)
	Align(ctx context.Context, request filemanager.AlignRequest) (filemanager.AlignOutcome, error)
	// Match a detached video to a season and episode, and the TMDB
	// season lists the match chooses from.
	PreviewMatch(ctx context.Context, request filemanager.MatchRequest) (filemanager.MatchPreview, error)
	SubmitMatch(ctx context.Context, request filemanager.SubmitMatchRequest) (filemanager.UserOperation, error)
	TMDBSeasons(ctx context.Context, managedAnimeID string) ([]filemanager.TMDBSeason, error)
	TMDBSeasonEpisodes(ctx context.Context, managedAnimeID string, season int) ([]filemanager.TMDBEpisode, error)
}

// MaintenanceGate is the runtime write gate. Product write commands consult
// it before executing; entering maintenance makes every write fail closed
// while read-only readiness and recovery diagnostics keep working. It is
// implemented by internal/runtime/maintenance.Gate and injected by the
// composition root. Nil is tolerated (writes pass) so unit tests that build a
// Server without a gate keep working.
type MaintenanceGate interface {
	RejectWrites() bool
	Status() maintenance.Status
}

type Dependencies struct {
	DB           *sql.DB
	Access       *accessapplication.Service
	Events       *events.Hub
	Draft        DraftService
	Ownership    OwnershipService
	Readmodel    ReadmodelService
	ReleaseRetry ReleaseRetryService
	// ImportRetry is set after construction (SetImportRetry); the import
	// retry command answers 503 until it is wired.
	ImportRetry ImportRetryService
	// Backup is set after construction (SetBackup); the backup endpoints
	// answer 503 until it is wired.
	Backup             BackupService
	OneShot            OneShotService
	MediaRecognition   MediaRecognitionService
	Catalog            CatalogService
	Connections        ConnectionConfigService
	ManualRetry        ManualRetryService
	Automation         AutomationService
	GlobalSections     GlobalSectionService
	MetadataSearch     MetadataSearchService
	ImagePosters       ImagePosterService
	ImageCovers        ImageCoverService
	Observability      ObservabilityService
	DiagnosticRecorder observabilityport.Recorder
	PluginTargets      PluginTargetService
	EmbeddedUpdate     EmbeddedUpdateService
	TemplateCatalog    TemplateCatalogService
	NamingVars         NamingVarsService
	Reconciliation     ReconciliationService
	CandidateFacts     CandidateFactsService
	TorrentStructure   TorrentStructureService
	FileMaintenance    FileMaintenanceService
	TorrentParse       TorrentParseService
	QBManagement       QBManagementService
	FileManager        FileManagerService
	Environment        string
	SkipAuth           bool
	FrontendDir        string
	SessionTTL         time.Duration
	RequestTimeout     time.Duration
	RuntimeLogger      RuntimeLogger
	MaintenanceGate    MaintenanceGate
	Readiness          *readiness.Checker
}

type Server struct {
	deps Dependencies
	mux  *http.ServeMux
	// backupDownloads holds the prepared browser backup downloads.
	backupDownloads backupDownloadStore
	// loginLimiter is process-local brute-force protection for /auth/login.
	loginLimiter *loginLimiter
}

func NewServer(deps Dependencies) (*Server, error) {
	return newServer(deps)
}

// NewProductionServer constructs the server used by the production composition
// root. Required write/read capabilities are validated before any routes are
// exposed; tests may continue using NewServer with a focused dependency set.
func NewProductionServer(deps Dependencies) (*Server, error) {
	if deps.GlobalSections == nil {
		return nil, errors.New("httpapi global sections service is nil")
	}
	if deps.Draft == nil {
		return nil, errors.New("httpapi anime service is nil")
	}
	if deps.Ownership == nil {
		return nil, errors.New("httpapi ownership service is nil")
	}
	if deps.ReleaseRetry == nil {
		return nil, errors.New("httpapi release retry service is nil")
	}
	if deps.OneShot == nil {
		return nil, errors.New("httpapi one-shot service is nil")
	}
	if deps.MediaRecognition == nil {
		return nil, errors.New("httpapi media recognition service is nil")
	}
	if deps.Catalog == nil {
		return nil, errors.New("httpapi catalog service is nil")
	}
	if deps.Connections == nil {
		return nil, errors.New("httpapi connections service is nil")
	}
	if deps.ManualRetry == nil {
		return nil, errors.New("httpapi manual retry service is nil")
	}
	if deps.Automation == nil {
		return nil, errors.New("httpapi automation service is nil")
	}
	if deps.MetadataSearch == nil {
		return nil, errors.New("httpapi metadata search service is nil")
	}
	if deps.ImagePosters == nil {
		return nil, errors.New("httpapi image poster service is nil")
	}
	if deps.ImageCovers == nil {
		return nil, errors.New("httpapi image cover service is nil")
	}
	if deps.Observability == nil {
		return nil, errors.New("httpapi observability service is nil")
	}
	if deps.DiagnosticRecorder == nil {
		return nil, errors.New("httpapi diagnostic recorder is nil")
	}
	if deps.PluginTargets == nil {
		return nil, errors.New("httpapi plugin target service is nil")
	}
	if deps.TemplateCatalog == nil {
		return nil, errors.New("httpapi template catalog service is nil")
	}
	if deps.NamingVars == nil {
		return nil, errors.New("httpapi naming vars service is nil")
	}
	if deps.Reconciliation == nil {
		return nil, errors.New("httpapi reconciliation service is nil")
	}
	if deps.FileMaintenance == nil {
		return nil, errors.New("httpapi file maintenance service is nil")
	}
	if deps.TorrentParse == nil {
		return nil, errors.New("httpapi torrent parse service is nil")
	}
	if deps.QBManagement == nil {
		return nil, errors.New("httpapi qB management service is nil")
	}
	if deps.FileManager == nil {
		return nil, errors.New("httpapi file manager service is nil")
	}
	if deps.Events == nil {
		return nil, errors.New("httpapi event hub is nil")
	}
	if deps.Readiness == nil {
		return nil, errors.New("httpapi readiness checker is nil")
	}
	return newServer(deps)
}

func newServer(deps Dependencies) (*Server, error) {
	if deps.DB == nil {
		return nil, errors.New("httpapi database is nil")
	}
	if deps.Access == nil {
		return nil, errors.New("httpapi access control service is nil")
	}
	if deps.Environment == "production" && deps.SkipAuth {
		return nil, errors.New("production runtime cannot enable skip-auth")
	}
	if deps.SessionTTL <= 0 {
		deps.SessionTTL = 24 * time.Hour
	}
	if deps.RuntimeLogger == nil {
		deps.RuntimeLogger = defaultRuntimeLogger
	}
	server := &Server{deps: deps, mux: http.NewServeMux(), loginLimiter: newLoginLimiter(nil)}
	return server, nil
}

// Handler assembles request-ID and authentication middleware around the
// generated OpenAPI routes, mounts the in-process MCP server behind the same
// bearer middleware, and serves the built frontend outside /api/v1.
func (s *Server) Handler() http.Handler {
	api := generated.HandlerWithOptions(s, generated.StdHTTPServerOptions{
		BaseURL: "/api/v1",
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, r, badRequest("REQUEST_INVALID", "The request is missing a required parameter or is malformed."))
		},
	})
	root := http.NewServeMux()
	apiChain := securityHeaders(s.withCommonMiddleware(api))
	root.Handle("/api/v1/", apiChain)
	// The in-process MCP server is derived from the OpenAPI catalog (x-mcp) and
	// replays every tool call against the same chain /api/v1/ serves, with the
	// MCP caller's own bearer token. It owns no second execution path.
	root.Handle("/api/v1/mcp", securityHeaders(s.withCommonMiddleware(mcp.Handler(apiChain))))
	root.Handle("/", frontendHandler(s.deps.FrontendDir))
	return root
}

// ServeHTTP lets the server act as the composition root HTTP handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

var _ generated.ServerInterface = (*Server)(nil)
