package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	accessapplication "bansoko/internal/accesscontrol/application"
	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	acquisitionsettlement "bansoko/internal/acquisition/terminal/settlement"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	animeapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	artifactadapter "bansoko/internal/artifact/adapter"
	artifactapplication "bansoko/internal/artifact/application"
	automationapplication "bansoko/internal/automation/application"
	catalogadapter "bansoko/internal/catalog/adapter"
	catalogrecordretentionpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	configurationapplication "bansoko/internal/configuration/application"
	configurationdomain "bansoko/internal/configuration/domain"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/downloadcompletion"
	episodetitle "bansoko/internal/episodetitle"
	episodetitleadapter "bansoko/internal/episodetitle/adapter"
	namingfacts "bansoko/internal/episodetitle/namingfacts"
	"bansoko/internal/events"
	"bansoko/internal/filemaintenance"
	filemaintenanceplanner "bansoko/internal/filemaintenance/planner"
	filemaintenancepolicy "bansoko/internal/filemaintenance/policy"
	"bansoko/internal/filemanager"
	filemanagerpersistence "bansoko/internal/filemanager/persistence"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	metadataadapter "bansoko/internal/identitymetadata/adapter"
	posterpersistence "bansoko/internal/identitymetadata/adapter/persistence"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/metadata/matcher"
	namingvarseditor "bansoko/internal/namingvars/editor"
	observabilitypersistence "bansoko/internal/observability/adapter/persistence"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parser"
	"bansoko/internal/parserpolicy"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	provideradapter "bansoko/internal/provider/adapter"
	providerdomain "bansoko/internal/provider/domain"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	retentionpersistence "bansoko/internal/recordretention/adapter/persistence"
	retentionapplication "bansoko/internal/recordretention/application"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/release/torrentstructure"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/runtime/readiness"
	runtimeworker "bansoko/internal/runtime/worker"
	selectionapplication "bansoko/internal/selection/application"
	"bansoko/internal/subscription/groupsource"
	templatecatalogapplication "bansoko/internal/templatecatalog/application"
	"bansoko/internal/transport/httpapi"
	"bansoko/internal/workitems"
)

type httpServerBase struct {
	dependencies httpapi.Dependencies
}

type serverEnvironment string
type serverFrontendDir string
type serverSkipAuth bool

type pluginRuntime struct {
	targetService   *plugintargetapplication.Service
	deliverHandler  *plugintargetapplication.PluginDeliverHandler
	admissionRunner *plugintargetapplication.AdmissionResolver
	templateService *templatecatalogapplication.Service
}

func provideQBManagementService(owner *configurationapplication.Service) httpapi.QBManagementService {
	return configurationapplication.NewQBManagementService(owner, nil)
}

func provideMediaRecognitionService() httpapi.MediaRecognitionService {
	return httpapi.NewMediaRecognitionService(parser.DefaultRegistry())
}

func provideTorrentParseService() httpapi.TorrentParseService {
	return httpapi.NewTorrentParseService()
}

func provideCandidateFactsService(repo facts.Repository) httpapi.CandidateFactsService {
	return facts.NewService(repo)
}

func provideTorrentStructureService(db *sql.DB) httpapi.TorrentStructureService {
	return torrentstructure.NewService(torrentstructure.NewPGRepository(db))
}

func provideImagePosterService(
	db *sql.DB,
	connections metadataadapter.ConnectionSource,
	clientResolver metadataadapter.HTTPClientResolver,
) httpapi.ImagePosterService {
	return metadataadapter.NewPosterResolver(connections, clientResolver, posterpersistence.NewRepository(db))
}

func provideImageCoverService(
	db *sql.DB,
	connections metadataadapter.ConnectionSource,
	clientResolver metadataadapter.HTTPClientResolver,
) httpapi.ImageCoverService {
	return metadataadapter.NewCoverProxy(connections, clientResolver, posterpersistence.NewProviderImageRepository(db))
}

func providePluginRuntime(
	db *sql.DB,
	secretStore plugintargetapplication.SecretStore,
	secretResolver plugintargetapplication.SecretResolver,
	workStore *workitems.Store,
	logger observabilityport.RuntimeLogger,
	downloadService *downloadapplication.Service,
	fileOperationService *fileoperationapplication.Service,
	rssService *rssapplication.Service,
	observabilityService *observabilityapplication.Service,
) (*pluginRuntime, error) {
	targetService, err := plugintargetapplication.NewService(db, secretStore, workStore)
	if err != nil {
		return nil, err
	}
	templateService := templatecatalogapplication.NewService()
	deliverHandler := plugintargetapplication.NewPluginDeliverHandler(db, secretResolver, workStore, nil, templateService)
	deliverHandler.SetDiagnosticRecorder(observabilityService, logger)
	rssService.SetDiagnosticRecorder(observabilityService, logger)
	rssService.SetBusinessDeliveryEnqueuer(targetService)
	downloadService.SetBusinessDeliveryEnqueuer(targetService)
	downloadService.SetDiagnosticRecorder(observabilityService, logger)
	fileOperationService.SetBusinessDeliveryEnqueuer(targetService)
	fileOperationService.SetDiagnosticRecorder(observabilityService, logger)
	return &pluginRuntime{
		targetService:   targetService,
		deliverHandler:  deliverHandler,
		admissionRunner: &plugintargetapplication.AdmissionResolver{Service: targetService, Logger: logger, SweepInterval: time.Second},
		templateService: templateService,
	}, nil
}

func providePluginTargetService(runtime *pluginRuntime) httpapi.PluginTargetService {
	return runtime.targetService
}

func provideTemplateCatalogService(runtime *pluginRuntime) httpapi.TemplateCatalogService {
	return runtime.templateService
}

// provideOwnershipService constructs the anime owner-mutation service. Its
// owner-set ports are injected by the file-maintenance and automation graphs
// after construction, so the graph keeps one instance shared by the HTTP
// ownership slot, the readmodel owner readers, and the naming renderer.
func provideOwnershipService(db *sql.DB, workStore *workitems.Store, connections groupsource.ConnectionSource) (*ownershipapplication.Service, error) {
	return ownershipapplication.NewService(db, ownershipapplication.Dependencies{WorkItems: workStore, Connections: connections})
}

// provideDraftService constructs the candidate-draft service, the owner of
// candidate CRUD, immutable revisions and the atomic Subscribe/activation
// transaction. Its policy-snapshot ports are injected by the file-maintenance
// graph and its activity journal by the application graph, so the graph keeps
// one instance shared by the HTTP draft slot, the MCP draft pipeline, the
// readmodel revision reader and the one-shot revision reader.
func provideDraftService(db *sql.DB, workStore *workitems.Store, connections groupsource.ConnectionSource) (*draftapplication.Service, error) {
	return draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: workStore, Connections: connections})
}

// provideReadmodelService constructs the readmodel service with explicit
// collaborators: the kernel anime Service supplies the config and parser-policy
// reads, the draft service supplies the saved-revision read, the ownership
// service supplies the owner projections the read model renders, the catalog
// service supplies the provider feed snapshot seed previews, and mediaFacts is
// the same facts.Store built from mediapersistence.NewRepository(db) that the
// composition root also hands to the file-maintenance owners. Sharing that one
// store keeps the frozen media-facts badges identical between the readmodel and
// the file-maintenance projections. seedPreviews is the same dual-kind source
// the application graph hands the one-shot service, so an origin=one_shot list
// read over a keyword-search snapshot reads the exact snapshot identity the
// later expansion and submission prove.
func provideReadmodelService(db *sql.DB, ownership *ownershipapplication.Service, anime *animeapplication.Service, draft *draftapplication.Service, catalog *catalogapplication.Service, seedPreviews animeapplication.SeedPreviewSource, mediaFacts *facts.Store, placementTargets *filemaintenance.NamingTargetRenderer) (*readmodelapplication.Service, error) {
	return readmodelapplication.NewService(db, readmodelapplication.Dependencies{
		Config:       anime,
		Owners:       ownership,
		Revisions:    draft,
		SeedPreviews: seedPreviews,
		// The manifest source is the read-only half of lazy expansion: the list
		// renders the manifests the snapshot or the expansion cache already
		// proves, and fetches nothing itself. The search read joins it so a
		// one-shot browse over a keyword-search snapshot renders the same way.
		SeedTorrentManifests: animeapplication.SeedTorrentManifestsWithKeywordSearch(
			catalog.ReadSeedTorrentManifests,
			catalogapplication.SeedSearchApplication{Service: catalog}.ReadSearchSeedTorrentManifests,
		),
		MediaFacts: mediaFacts,
		// The RSS row of an imported file shows the new path with the same
		// renderer the per-anime files dialog uses.
		PlacementTargets: placementTargets,
	})
}

func provideHTTPServerBase(
	db *sql.DB,
	access *accessapplication.Service,
	eventsHub *events.Hub,
	draft httpapi.DraftService,
	ownership httpapi.OwnershipService,
	readmodel httpapi.ReadmodelService,
	releaseRetry httpapi.ReleaseRetryService,
	oneshoot *oneshotapplication.Service,
	catalog httpapi.CatalogService,
	seedSearch httpapi.SeedSearchService,
	connections httpapi.ConnectionConfigService,
	manualRetry httpapi.ManualRetryService,
	automation httpapi.AutomationService,
	globalSections httpapi.GlobalSectionService,
	metadataSearch httpapi.MetadataSearchService,
	observability httpapi.ObservabilityService,
	diagnosticRecorder observabilityport.Recorder,
	embeddedUpdate httpapi.EmbeddedUpdateService,
	reconciliation httpapi.ReconciliationService,
	fileMaintenance httpapi.FileMaintenanceService,
	environment serverEnvironment,
	skipAuth serverSkipAuth,
	frontendDir serverFrontendDir,
	maintenanceGate httpapi.MaintenanceGate,
	readinessChecker *readiness.Checker,
	runtimeLogger httpapi.RuntimeLogger,
) httpServerBase {
	return httpServerBase{dependencies: httpapi.Dependencies{
		DB:                 db,
		Access:             access,
		Events:             eventsHub,
		Draft:              draft,
		Ownership:          ownership,
		Readmodel:          readmodel,
		ReleaseRetry:       releaseRetry,
		OneShot:            oneshoot,
		Catalog:            catalog,
		SeedSearch:         seedSearch,
		Connections:        connections,
		ManualRetry:        manualRetry,
		Automation:         automation,
		GlobalSections:     globalSections,
		MetadataSearch:     metadataSearch,
		Observability:      observability,
		DiagnosticRecorder: diagnosticRecorder,
		EmbeddedUpdate:     embeddedUpdate,
		Reconciliation:     reconciliation,
		FileMaintenance:    fileMaintenance,
		Environment:        string(environment),
		SkipAuth:           bool(skipAuth),
		FrontendDir:        string(frontendDir),
		MaintenanceGate:    maintenanceGate,
		Readiness:          readinessChecker,
		RuntimeLogger:      runtimeLogger,
	}}
}

func provideHTTPDependencies(
	base httpServerBase,
	qbManagement httpapi.QBManagementService,
	mediaRecognition httpapi.MediaRecognitionService,
	torrentParse httpapi.TorrentParseService,
	candidateFacts httpapi.CandidateFactsService,
	torrentStructure httpapi.TorrentStructureService,
	imagePosters httpapi.ImagePosterService,
	imageCovers httpapi.ImageCoverService,
	pluginTarget httpapi.PluginTargetService,
	templateCatalog httpapi.TemplateCatalogService,
	fileManager httpapi.FileManagerService,
) httpapi.Dependencies {
	dependencies := base.dependencies
	dependencies.QBManagement = qbManagement
	dependencies.MediaRecognition = mediaRecognition
	dependencies.TorrentParse = torrentParse
	dependencies.CandidateFacts = candidateFacts
	dependencies.TorrentStructure = torrentStructure
	dependencies.ImagePosters = imagePosters
	dependencies.ImageCovers = imageCovers
	dependencies.PluginTargets = pluginTarget
	dependencies.TemplateCatalog = templateCatalog
	dependencies.NamingVars = namingvarseditor.NewService(dependencies.DB)
	if fileManager != nil {
		dependencies.FileManager = fileManager
	}
	return dependencies
}

// provideFileManager builds the file manager (B13) over the owner-fact store,
// and the file_manager_execute owner service whose guard reads the same paths
// and facts. The denial zone is the composition root's: it names the
// deployment's own state directories. The P1 write ports (commands, align,
// policy) are attached by the composition root once their dependencies exist.
func provideFileManager(db *sql.DB, workStore *workitems.Store, denialZone filemanager.DenialZone) (*filemanager.Service, *filemaintenance.UserOperationService, error) {
	store, err := filemanagerpersistence.NewStore(db)
	if err != nil {
		return nil, nil, err
	}
	service, err := filemanager.NewService(store, denialZone)
	if err != nil {
		return nil, nil, err
	}
	userOperations := filemaintenance.NewUserOperationService(db, workStore,
		filesystem.OSFileExecutor{}, filesystem.OSFileObserver{},
		func() filemaintenance.CleanupFileExecutor {
			// A delete captures inside the directory that holds the file.
			return filesystem.NewOSCleanupExecutor(filemaintenance.CleanupAnchor)
		},
		filemanagerpersistence.UserOperationGuard{Service: service})
	return service, userOperations, nil
}

type fileMaintenanceGraph struct {
	replacementPolicies      *filemaintenance.ReplacementPolicyService
	parserPolicies           *parserpolicy.Service
	fileEffectPolicies       *filemaintenance.FileEffectPolicyService
	policyService            *filemaintenancepolicy.PolicyService
	planner                  *filemaintenanceplanner.Planner
	mediaFactsStore          *facts.Store
	replacementTrigger       *filemaintenance.ReplacementTriggerService
	cleanupService           *filemaintenance.CleanupService
	fileMaintenanceSnapshot  *filemaintenance.ReconciliationSnapshotReader
	namingRenderer           *filemaintenance.NamingTargetRenderer
	offsetMigrationPlanner   *filemaintenance.OffsetMigrationService
	offsetMigrationExecute   *filemaintenance.OffsetMigrationExecuteService
	offsetMigrationAdmission *filemaintenance.OffsetMigrationAdmissionService
	placementService         *filemaintenance.PlacementService
	alignmentOperations      *filemaintenance.AlignmentService
	calibrationService       *filemaintenance.CalibrationService
	replacementDecisions     *filemaintenance.ReplacementDecisionService
	candidateFactsRepo       facts.Repository
	oneShotMarkers           *animepersistence.Repository
	fileManager              *filemanager.Service
	userOperations           *filemaintenance.UserOperationService
	metadataRefresher        filemaintenance.MetadataWorkRefresher
}

func provideFileMaintenanceGraph(
	db *sql.DB,
	workStore *workitems.Store,
	draftService *draftapplication.Service,
	ownershipService *ownershipapplication.Service,
	denialZone filemanager.DenialZone,
	logger observabilityport.RuntimeLogger,
) (*fileMaintenanceGraph, error) {
	replacementPolicies := filemaintenance.NewReplacementPolicyService(db)
	markerRepo := animepersistence.NewRepository(db)
	// The retirement command that protects an active one-shot marker from
	// deletion lives in the ownership service; the draft/one-shot provisioning
	// ports are wired on the service that runs the owning transaction.
	ownershipService.SetOneShotMarkerRepository(markerRepo)
	parserPolicies := parserpolicy.NewService(db)
	draftService.SetFrozenParserPolicy(parserPolicies)
	fileEffectPolicies := filemaintenance.NewFileEffectPolicyService(db)
	// This store is shared with the downstream file-maintenance owners; the
	// readmodel needs it for the same frozen media-facts projection.
	mediaFactsStore := facts.NewStoreWithRepository(mediapersistence.NewRepository(db))

	replacementTrigger := filemaintenance.NewReplacementTriggerService(db, workStore)
	ownershipService.SetReplacementTrigger(replacementTrigger)

	replacementDecisions := filemaintenance.NewReplacementDecisionService(db)
	replacementDecisions.SetMediaFactsStore(mediaFactsStore)
	cleanupService := filemaintenance.NewCleanupService(db)
	// A cleanup capture is bounded by the anchor its composition root injects;
	// the file manager has no root layer, so that anchor is the deployment's
	// filesystem root.
	cleanupService.SetFileExecutor(filesystem.NewOSCleanupExecutor(filemaintenance.CleanupAnchor))
	cleanupService.SetWorkItems(workStore)
	replacementDecisions.SetCleanupHandoff(cleanupService)

	fileMaintenanceSnapshot := filemaintenance.NewReconciliationSnapshotReader(db)
	namingRenderer := filemaintenance.NewNamingTargetRenderer(db, ownershipService)
	policyService := filemaintenancepolicy.NewPolicyService(db)
	planner := filemaintenanceplanner.NewPlanner(fileMaintenanceSnapshot, namingRenderer)
	offsetMigrationPlanner := filemaintenance.NewOffsetMigrationService(db, namingRenderer)
	offsetMigrationPlanner.SetWorkItems(workStore)
	offsetMigrationExecute := filemaintenance.NewOffsetMigrationExecuteService(db)
	offsetExecutor, err := newOffsetMigrationPauseExecutor(filesystem.OSFileExecutor{}, os.Getenv)
	if err != nil {
		return nil, err
	}
	offsetMigrationExecute.SetFilePorts(offsetExecutor, filesystem.OSFileObserver{})
	offsetMigrationExecute.SetWorkItems(workStore)
	offsetMigrationAdmission := filemaintenance.NewOffsetMigrationAdmissionService()
	ownershipService.SetOffsetMigrationAdmission(offsetMigrationAdmission)

	candidateFactsRepo := facts.NewPGRepository(db)
	placementService := filemaintenance.NewPlacementService(db, filemaintenance.NewFileOperationEvidenceReader(db), fileMaintenanceSnapshot)
	alignmentOperations := filemaintenance.NewAlignmentService(db)
	alignmentOperations.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	alignmentOperations.SetWorkItems(workStore)
	calibrationService := filemaintenance.NewCalibrationService(
		db,
		placementService,
		policyService,
		planner,
		workStore,
	)
	calibrationService.SetLogger(logger)
	calibrationService.SetAlignmentConsumer(alignmentOperations)
	calibrationService.SetReplacementConsumer(replacementTrigger)
	calibrationService.SetCompanionCollisionSource(ownershipService)
	fileManager, userOperations, err := provideFileManager(db, workStore, denialZone)
	if err != nil {
		return nil, err
	}
	fileManager.SetAlignmentPolicy(fileManagerAlignmentPolicy{policies: policyService})

	return &fileMaintenanceGraph{
		replacementPolicies: replacementPolicies, parserPolicies: parserPolicies,
		fileEffectPolicies: fileEffectPolicies, policyService: policyService, planner: planner,
		mediaFactsStore:    mediaFactsStore,
		replacementTrigger: replacementTrigger, cleanupService: cleanupService,
		fileMaintenanceSnapshot: fileMaintenanceSnapshot,
		namingRenderer:          namingRenderer,
		offsetMigrationPlanner:  offsetMigrationPlanner, offsetMigrationExecute: offsetMigrationExecute,
		offsetMigrationAdmission: offsetMigrationAdmission, placementService: placementService,
		alignmentOperations: alignmentOperations, calibrationService: calibrationService,
		replacementDecisions: replacementDecisions, candidateFactsRepo: candidateFactsRepo,
		oneShotMarkers: markerRepo,
		fileManager:    fileManager, userOperations: userOperations,
	}, nil
}

type applicationGraph struct {
	retryService         *animeapplication.ManualRetryService
	catalogDBSource      catalogadapter.DBConnectionSource
	metadataSearcher     *metadataadapter.SearchService
	observabilityService *observabilityapplication.Service
	catalogService       *catalogapplication.Service
	seedSearch           catalogapplication.SeedSearchApplication
	seedPreviewSource    animeapplication.SeedPreviewSource
	retentionJanitor     *retentionapplication.Service
	oneShotReclaim       *workitems.OneShotReclaimService
	acquisitionSettle    *acquisitionsettlement.SettlementService
	oneShotService       *oneshotapplication.Service
}

func provideApplicationGraph(
	db *sql.DB,
	workStore *workitems.Store,
	dbSecretStore *configurationapplication.DatabaseSecretStore,
	runtimeConfiguration *configurationapplication.Service,
	eventHub *events.Hub,
	logger observabilityport.RuntimeLogger,
	fixtureAllowlist []string,
	fixtureResolver func(context.Context, string) ([]net.IP, error),
	animeService *animeapplication.Service,
	ownershipService *ownershipapplication.Service,
	draftService *draftapplication.Service,
	rssService *rssapplication.Service,
	downloadService *downloadapplication.Service,
	artifactService *artifactapplication.Service,
	fileOperationService *fileoperationapplication.Service,
	replacementDecisions *filemaintenance.ReplacementDecisionService,
	cleanupService *filemaintenance.CleanupService,
	placementService *filemaintenance.PlacementService,
	replacementTrigger *filemaintenance.ReplacementTriggerService,
	fileMaintenanceGraph *fileMaintenanceGraph,
	targetStore acquisitiontarget.Store,
) (*applicationGraph, error) {
	retryService, err := animeapplication.NewManualRetryService(db, workStore)
	if err != nil {
		return nil, err
	}
	retryService.SetEventHub(eventHub)

	catalogDBSource := catalogadapter.DBConnectionSource{DB: db}
	metadataSearcher := metadataadapter.NewSearchService(catalogDBSource, dbSecretStore, runtimeConfiguration)
	metadataSearcher.SetTitleRecorder(posterpersistence.NewRepository(db))
	observabilityService, err := observabilityapplication.NewService(db)
	if err != nil {
		return nil, err
	}
	rssService.SetActivityWriter(observabilityService)
	draftService.SetActivityWriter(observabilityService)
	ownershipService.SetActivityWriter(observabilityService)
	fileOperationService.SetActivityWriter(observabilityService)
	replacementDecisions.SetActivityWriter(observabilityService)
	cleanupService.SetActivityWriter(observabilityService)
	placementService.SetActivityWriter(observabilityService)
	replacementTrigger.SetDiagnostics(observabilityService, logger)
	// The file-operation owner's after-commit diagnostics are the visible
	// channel for the independent placement/index and replacement-trigger
	// stages, including the stable failure reason for a bounded retry.
	fileOperationService.SetDiagnosticRecorder(observabilityService, logger)

	catalogReaders := map[string]catalogapplication.Reader{
		catalogapplication.ProviderAniBT: catalogadapter.ConfiguredReader{Provider: catalogapplication.ProviderAniBT, Connections: catalogDBSource, ClientResolver: runtimeConfiguration, AllowPrivateHosts: fixtureAllowlist, Resolver: fixtureResolver},
		catalogapplication.ProviderMikan: catalogadapter.ConfiguredReader{Provider: catalogapplication.ProviderMikan, Connections: catalogDBSource, Recorder: observabilityService, ClientResolver: runtimeConfiguration, AllowPrivateHosts: fixtureAllowlist, Resolver: fixtureResolver},
	}
	catalogService := catalogapplication.NewProviderService(db, catalogReaders, catalogapplication.NewSubscriptionItemReader(db))
	catalogService.SetEventHub(eventHub)
	catalogService.SetGroupReaders(map[string]catalogapplication.GroupReader{
		"anibt": provideradapter.ConfiguredGroupReader{Provider: providerdomain.ProviderAniBT, Connections: catalogDBSource, ClientResolver: runtimeConfiguration},
		"mikan": provideradapter.ConfiguredGroupReader{Provider: providerdomain.ProviderMikan, Connections: catalogDBSource, Recorder: observabilityService, ClientResolver: runtimeConfiguration},
	})
	// Preview never prefetches torrents: the list carries RSS-native facts only,
	// and a seed's bytes are fetched lazily when the user expands it.
	catalogService.SetProviderFeedFetcher(acquisitionapplication.ConfiguredProviderFeedFetcher{
		Connections:          catalogDBSource,
		ClientResolver:       runtimeConfiguration,
		ParseTorrentMetadata: false,
		AllowPrivateHosts:    fixtureAllowlist,
		Resolver:             fixtureResolver,
	})
	catalogService.SetProviderConnectionSource(catalogDBSource)
	// The subgroup dictionary is the collected snapshot published with the
	// release (embedded, read-only), the persisted user layer, and the networked
	// retrieval that grows the user layer one group at a time. The retrieval
	// takes the same connection source and network-route resolver as the other
	// provider adapters, so a saved provider connection applies to it as well.
	catalogService.SetGroupDirectory(groupDirectory{})
	catalogService.SetGroupResolver(configuredGroupResolver{
		lookup: provideradapter.ConfiguredGroupLookup{Connections: catalogDBSource, ClientResolver: runtimeConfiguration},
	})
	catalogService.SetGroupDictionaryStore(catalogrecordretentionpersistence.NewGroupDictionaryRepository(db))
	catalogService.SetFeedSnapshotStore(catalogrecordretentionpersistence.NewRepository(db))
	// Lazy expansion is the only catalog path allowed to fetch a torrent: it gets
	// the locator-keyed cache and the same SSRF-safe payload reader the one-shot
	// acquisition uses, while the preview fetcher above deliberately has neither.
	// The fetcher is the configured one: a seed's torrent leaves through the same
	// saved provider connection and network route as the feed or search request
	// that published it, so expanding a seed over a proxied source does not dial
	// the upstream directly.
	catalogService.SetSeedTorrentCache(catalogrecordretentionpersistence.NewRepository(db))
	catalogService.SetSeedTorrentFetcher(acquisitionapplication.ConfiguredSeedTorrentFetcher{
		Connections:       catalogDBSource,
		ClientResolver:    runtimeConfiguration,
		AllowPrivateHosts: fixtureAllowlist,
		Resolver:          fixtureResolver,
	})
	// Keyword search resolves its upstream endpoint from the same typed provider
	// connection the catalog readers use, and runs under the same SSRF-safe
	// transport; a request never names an endpoint. Both sites are reached over
	// the one saved connection per provider, and the fallback is the official
	// public base, exactly like a catalog refresh.
	seedSearch := catalogapplication.SeedSearchApplication{
		Service:           catalogService,
		Endpoints:         catalogSearchEndpoints{connections: catalogDBSource, clients: runtimeConfiguration},
		Resolver:          fixtureResolver,
		AllowPrivateHosts: fixtureAllowlist,
	}
	// Custom-source previews follow the same lazy rule as provider previews: a
	// snapshot stores the RSS facts, and expansion fetches the torrent on demand.
	catalogService.SetCustomSnapshotApplication(catalogapplication.CustomSnapshotApplication{Revisions: draftService, Owners: draftService, Fetcher: customSnapshotNetworkFetcher{fetcher: acquisitionapplication.ConfiguredCustomRSSFetcher{Connections: catalogDBSource, ClientResolver: runtimeConfiguration, ParseTorrentMetadata: false, AllowPrivateHosts: fixtureAllowlist, Resolver: fixtureResolver}}, Snapshots: catalogrecordretentionpersistence.NewRepository(db)})
	provisioner := animeapplication.NewProvisioner(fileMaintenanceGraph.parserPolicies)
	// One seed preview source serves both feed and keyword-search snapshots: the
	// exact snapshot read and the seed's verified bytes, for both kinds. A search
	// seed therefore proves its snapshot through the search read and receives the
	// bytes the search expansion verified — never the feed payload reader, which
	// resolves a provider entry/group scope a search row does not have.
	seedPreviews := animeapplication.SeedPreviewSourceWithKeywordSearch(
		catalogService.ReadProviderEntryRSSByIdentity,
		catalogService.ReadProviderEntrySeedPayload,
		seedSearch.FetchSearchSnapshotByID,
		seedSearch.ReadSearchSeedPayload,
	)
	oneShotService, err := oneshotapplication.NewService(db, oneshotapplication.Dependencies{
		SeedPreviews:       seedPreviews,
		CandidateRevisions: draftService,
		OneShotMarkers:     fileMaintenanceGraph.oneShotMarkers,
		Provisioner:        &provisioner,
		TorrentFetcher:     animeapplication.NewTorrentPayloadFetcher(fixtureAllowlist, fixtureResolver),
		TargetStore:        targetStore,
		TargetProbe:        acquisitiontarget.FilesystemProbe{},
		WorkItems:          workStore,
		EventHub:           eventHub,
	})
	if err != nil {
		return nil, err
	}
	posterRepository := posterpersistence.NewRepository(db)
	retentionRegistry, err := retentionapplication.NewRegistry(retentionapplication.Dependencies{
		Observability:   observabilitypersistence.NewRepository(db),
		Catalog:         catalogrecordretentionpersistence.NewRepository(db),
		AnimeCandidates: animepersistence.NewRepository(db),
		WorkItems:       workStore,
		MediaPosters:    posterRepository,
		Discovery:       retentionpersistence.NewRepository(db),
		Maintenance:     retentionpersistence.NewRepository(db),
	})
	if err != nil {
		return nil, err
	}
	retentionJanitor, err := retentionapplication.NewService(db, retentionRegistry, logger, observabilityService)
	if err != nil {
		return nil, err
	}
	oneShotReclaim, err := workitems.NewOneShotReclaimService(db, logger)
	if err != nil {
		return nil, err
	}
	acquisitionSettle, err := acquisitionsettlement.NewSettlementService(db)
	if err != nil {
		return nil, err
	}
	if err := requireProductionTargetAcquisition(rssService, oneShotService); err != nil {
		return nil, err
	}
	return &applicationGraph{
		retryService: retryService, catalogDBSource: catalogDBSource,
		metadataSearcher: metadataSearcher, observabilityService: observabilityService,
		catalogService: catalogService, seedSearch: seedSearch, seedPreviewSource: seedPreviews, retentionJanitor: retentionJanitor,
		oneShotReclaim: oneShotReclaim, oneShotService: oneShotService,
		acquisitionSettle: acquisitionSettle,
	}, nil
}

type coreApplicationGraph struct {
	downloadService       *downloadapplication.Service
	rssService            *rssapplication.Service
	targetStore           acquisitiontarget.Store
	artifactService       *artifactapplication.Service
	fileOperationService  *fileoperationapplication.Service
	reconciliationService *reconciliationapplication.Service
}

// provideEpisodeTitleQueries builds the one shared Querier of the provider
// episode titles (bgm.ep_title / tmdb.ep_title). The adapters reuse the
// provider-connection wiring of the metadata search: one enabled connection
// per source, the deployment secrets for the TMDB API key, and the egress
// route resolver. The persistent cache (migration 046) is the freeze point
// the whitelisted naming renders query — the library rename render and the
// anime-files alignment and match renders; every other render stays
// network-free by never receiving this Querier.
func provideEpisodeTitleQueries(db *sql.DB, runtimeConfiguration *configurationapplication.Service, secretStore *configurationapplication.DatabaseSecretStore) *namingfacts.Querier {
	connections := catalogadapter.DBConnectionSource{DB: db}
	return &namingfacts.Querier{
		Cache: &episodetitle.PersistentCache{
			DB: db,
			Lookup: episodetitle.New(episodetitle.Options{
				Clients: map[episodetitle.Source]episodetitle.Client{
					episodetitle.SourceTMDB: episodetitleadapter.TMDBClient{Connections: connections, Secrets: secretStore, ClientResolver: runtimeConfiguration},
					episodetitle.SourceBGM:  episodetitleadapter.BGMClient{Connections: connections, ClientResolver: runtimeConfiguration},
				},
			}),
		},
	}
}

func provideCoreApplicationGraph(
	db *sql.DB,
	workStore *workitems.Store,
	httpConfigService *configurationapplication.HTTPService,
	runtimeConfiguration *configurationapplication.Service,
	fixtureAllowlist []string,
	fixtureResolver func(context.Context, string) ([]net.IP, error),
	eventHub *events.Hub,
	logger observabilityport.RuntimeLogger,
	animeService *animeapplication.Service,
	fileMaintenanceGraph *fileMaintenanceGraph,
) (*coreApplicationGraph, error) {
	downloadService, err := downloadapplication.NewService(
		db,
		workStore,
		qbittorrent.ConfiguredClient{Source: httpConfigService},
		httpConfigService,
	)
	if err != nil {
		return nil, err
	}
	rssService, err := rssapplication.NewService(db, workStore, rssapplication.ConfiguredSourceFetcher{
		Connections:          runtimeConfiguration,
		ClientResolver:       runtimeConfiguration,
		ParseTorrentMetadata: true,
		AllowPrivateHosts:    fixtureAllowlist,
		Resolver:             fixtureResolver,
	})
	if err != nil {
		return nil, err
	}
	targetStore := acquisitiontarget.NewStore(targetpersistence.NewRepository(db))
	rssService.SetTargetStore(targetStore)
	rssService.SetTargetAcquisition(acquisitiontarget.FilesystemProbe{})
	rssService.SetDownloaderAdmission(downloadService)
	rssService.SetEventHub(eventHub)
	artifactService, err := artifactapplication.NewService(db, artifactadapter.OSArtifactInspector{})
	if err != nil {
		return nil, err
	}
	// The finite one-shot owner is constructed in provideApplicationGraph from
	// this shared target store and the same filesystem admission boundary.
	fileOperationService, err := fileoperationapplication.NewService(
		db,
		filesystem.OSFileExecutor{},
		filesystem.OSFileObserver{},
	)
	if err != nil {
		return nil, err
	}
	// The planning stage persists the typed Plan and first planning WorkItem atomically.
	artifactService.SetEventHub(eventHub)
	fileOperationService.SetEventHub(eventHub)
	artifactService.SetMediaFactsStore(fileMaintenanceGraph.mediaFactsStore)
	// Execution-time media facts (file.resolution, file.video_codec,
	// file.audio_codec, file.container) are read from the downloaded file by the
	// MediaInfo CLI that the runtime image installs. A missing or failing tool
	// leaves them empty with a bounded diagnostic; it never fails an inspection.
	artifactService.SetMediaProber(artifactadapter.OSMediaProber{}, logger)
	fileOperationService.SetMediaFactsStore(fileMaintenanceGraph.mediaFactsStore)

	reconciliationService, err := reconciliationapplication.NewService(db, workStore, filesystem.OSFileScanner{})
	if err != nil {
		return nil, err
	}
	reconciliationService.SetEventHub(eventHub)
	// Every reconciliation scan run also refreshes the instance's latest
	// TargetObservation rows (stat only), the RSS row status's file facts.
	reconciliationService.SetTargetObserver(acquisitiontarget.NewScanner(targetStore, acquisitiontarget.FilesystemProbe{}))
	// A completed scan re-releases the anime's blocked replacement deletions
	// whose receipts prove their state (deployment recovery's resolution).
	reconciliationService.SetBlockedDeletionReleaser(fileMaintenanceGraph.cleanupService, logger)
	// Scan-alignment recovery probe: the composition root injects the
	// filemaintenance renderer (dependency direction filemaintenance ->
	// reconciliation), the library's no-follow file observer and the SQL
	// occupancy read, so a scan may record recovery CANDIDATE facts for
	// missing placements whose COMPLETE rendered unit is free of other
	// owners. The probe only stats exact rendered paths; recovery itself
	// stays behind the user's explicit confirm command.
	reconciliationService.SetRecoveryProbe(
		fileMaintenanceGraph.namingRenderer,
		// METADATA observation, never ObserveFile: the probe freezes a
		// metadata fingerprint into the candidate, and the confirm compares
		// with domain.MetadataFingerprintsMatch. A content digest here would
		// be rejected by ParseMetadataFingerprint and turn every confirm into
		// target_changed.
		filesystem.OSFileObserver{},
		filemaintenance.NewRecoveryOccupancy(db),
	)
	rssService.SetCandidateFactsRepository(fileMaintenanceGraph.candidateFactsRepo)
	rssService.SetMediaFactsStore(fileMaintenanceGraph.mediaFactsStore)
	rssService.SetReplacementAdmission(fileMaintenanceGraph.replacementDecisions)
	rssService.SetSubscriptionCompetition(fileMaintenanceGraph.replacementDecisions)
	// A parser kind change invalidates the parse evidence of every unsubmitted
	// Release, so the parser policy owner schedules their reevaluation inside
	// its own CAS transaction instead of waiting for the next feed refresh.
	fileMaintenanceGraph.parserPolicies.SetReleaseReevaluationScheduler(rssService)
	placementSink, placementFaultDelay, err := newPlacementFaultSink(&placementSinkAdapter{
		evidence:   filemaintenance.NewFileOperationEvidenceReader(db),
		mediaFacts: fileMaintenanceGraph.mediaFactsStore,
		placements: fileMaintenanceGraph.placementService,
		triggers:   fileMaintenanceGraph.replacementTrigger,
	}, db, os.Getenv)
	if err != nil {
		return nil, err
	}
	fileOperationService.SetPlacementSink(placementSink)
	if placementFaultDelay > 0 {
		// Disposable fixture session only: the real retry and exhaustion
		// transitions stay intact, they just happen inside a browser test.
		fileOperationService.SetPlacementRetryDelay(placementFaultDelay)
	}
	return &coreApplicationGraph{
		downloadService: downloadService, rssService: rssService, targetStore: targetStore,
		artifactService: artifactService, fileOperationService: fileOperationService,
		reconciliationService: reconciliationService,
	}, nil
}

// targetAcquisitionReadiness is the mandatory production target-boundary shape
// every admission path must satisfy at composition time.
type targetAcquisitionReadiness interface {
	TargetAcquisitionReady() bool
}

// requireProductionTargetAcquisition rejects a production graph unless both
// ordinary RSS and finite one-shot admissions share the configured target path.
func requireProductionTargetAcquisition(rss, oneShot targetAcquisitionReadiness) error {
	if rss == nil || !rss.TargetAcquisitionReady() {
		return errors.New("production RSS target acquisition (filesystem probe, target store) is not wired")
	}
	if oneShot == nil || !oneShot.TargetAcquisitionReady() {
		return errors.New("production one-shot target acquisition (filesystem probe, target store) is not wired")
	}
	return nil
}

type automationGraph struct {
	service    *automationapplication.Service
	dispatcher *automationapplication.Dispatcher
}

func provideAutomationGraph(
	db *sql.DB,
	workStore *workitems.Store,
	eventHub *events.Hub,
	catalogDBSource catalogadapter.DBConnectionSource,
	configService *configurationapplication.Service,
	metadataMatcher *matcher.Matcher,
	observabilityService *observabilityapplication.Service,
	logger observabilityport.RuntimeLogger,
	animeService *animeapplication.Service,
	ownershipService *ownershipapplication.Service,
	draftService *draftapplication.Service,
) (*automationGraph, error) {
	selectionService := selectionapplication.NewService(db)
	automationService, err := automationapplication.NewService(db)
	if err != nil {
		return nil, err
	}
	automationService.SetEventHub(eventHub)
	ownershipService.SetMetadataMatcher(metadataMatcher)
	ownershipService.SetObservabilityRecorder(observabilityport.Recorder(observabilityService))
	dispatcher := &automationapplication.Dispatcher{
		DB:           db,
		WorkItems:    workStore,
		PolicyReader: selectionService,
		GroupReaders: map[string]automationapplication.GroupReader{
			"anibt": provideradapter.ConfiguredGroupReader{Provider: providerdomain.ProviderAniBT, Connections: catalogDBSource, ClientResolver: configService},
			"mikan": provideradapter.ConfiguredGroupReader{Provider: providerdomain.ProviderMikan, Connections: catalogDBSource, Recorder: observabilityService, ClientResolver: configService},
		},
		Connections:     catalogDBSource,
		MetadataMatcher: metadataMatcher,
		Logger:          logger,
		Recorder:        observabilityport.Recorder(observabilityService),
		ActivityWriter:  observabilityService,
	}
	dispatcher.SetEventHub(eventHub)
	draftService.SetGroupDiscoveryProvider(dispatcher)
	ownershipService.SetGroupDiscoveryProvider(dispatcher)
	draftService.SetLogger(logger)
	ownershipService.SetLogger(logger)
	return &automationGraph{service: automationService, dispatcher: dispatcher}, nil
}

type workerGraph struct {
	registry *runtimeworker.Registry
	handlers map[string]runtimeworker.Handler
	build    func() (RuntimeLike, error)
}

// workerRegistration is one kind-to-handler binding of the composition root.
// It is a named type so the exact-set validator and its test can hold the same
// shape the production wiring uses.
type workerRegistration struct {
	kind    string
	handler runtimeworker.Handler
}

// validateWorkerRegistrations proves the wiring is a bijection onto the server's
// declared kind inventory. The previous check compared lengths, which cannot see
// a same-sized difference: dropping one kind while duplicating another passes it,
// and the map build silently keeps only the last handler for the duplicate while
// the dropped kind has no handler at all. Both halves of that failure are
// invisible until an item of the missing kind is claimed at runtime.
func validateWorkerRegistrations(registrations []workerRegistration, declared []string) error {
	declaredSet := make(map[string]bool, len(declared))
	for _, kind := range declared {
		if declaredSet[kind] {
			return fmt.Errorf("server work item kind %q is declared twice", kind)
		}
		declaredSet[kind] = true
	}
	seen := make(map[string]bool, len(registrations))
	for _, registration := range registrations {
		if registration.handler == nil {
			return fmt.Errorf("work item kind %q is registered with a nil handler", registration.kind)
		}
		if seen[registration.kind] {
			return fmt.Errorf("work item kind %q is registered twice", registration.kind)
		}
		seen[registration.kind] = true
		if !declaredSet[registration.kind] {
			return fmt.Errorf("work item kind %q is registered but not declared by the server", registration.kind)
		}
	}
	for kind := range declaredSet {
		if !seen[kind] {
			return fmt.Errorf("declared work item kind %q has no handler registration", kind)
		}
	}
	return nil
}

func provideWorkerGraph(
	workStore *workitems.Store,
	installID string,
	logger observabilityport.RuntimeLogger,
	rssService *rssapplication.Service,
	downloadService *downloadapplication.Service,
	artifactService *artifactapplication.Service,
	fileOperationService *fileoperationapplication.Service,
	pluginRuntime *pluginRuntime,
	reconciliationService *reconciliationapplication.Service,
	calibrationService *filemaintenance.CalibrationService,
	alignmentOperations *filemaintenance.AlignmentService,
	offsetMigrationExecute *filemaintenance.OffsetMigrationExecuteService,
	replacementDecisions *filemaintenance.ReplacementDecisionService,
	cleanupService *filemaintenance.CleanupService,
	userOperations *filemaintenance.UserOperationService,
	downloadCompletion *downloadcompletion.Service,
) (*workerGraph, error) {
	registrations := []workerRegistration{
		{kind: workitems.KindRSSRefresh, handler: rssService.RSSRefreshHandler()},
		{kind: workitems.KindReleaseEvaluate, handler: rssService.ReleaseEvaluateHandler()},
		{kind: workitems.KindQBSubmit, handler: downloadService.SubmitHandler()},
		{kind: workitems.KindQBSync, handler: downloadService.SyncHandler()},
		{kind: workitems.KindArtifactInspect, handler: artifactService.InspectHandler()},
		{kind: workitems.KindFileExecute, handler: fileOperationService},
		{kind: workitems.KindPostProcessPlan, handler: fileOperationService.PostProcessingPlanHandler()},
		{kind: workitems.KindPluginDeliver, handler: pluginRuntime.deliverHandler},
		{kind: workitems.KindReconciliationScan, handler: reconciliationService.ScanHandler()},
		{kind: workitems.KindReconciliationCalibrate, handler: calibrationService.CalibrateHandler()},
		{kind: workitems.KindReconciliationConsumeAlignment, handler: calibrationService.ConsumeAlignmentHandler()},
		{kind: workitems.KindReconciliationConsumeReplacement, handler: calibrationService.ConsumeReplacementHandler()},
		{kind: workitems.KindAlignmentExecute, handler: alignmentOperations.ExecuteHandler()},
		{kind: workitems.KindOffsetMigrationExecute, handler: offsetMigrationExecute.ExecuteHandler()},
		{kind: workitems.KindReplacementEvaluate, handler: replacementDecisions.EvaluateHandler()},
		{kind: workitems.KindSubscriptionCompetition, handler: rssService.CompetitionHandler()},
		{kind: workitems.KindReplacementDelete, handler: cleanupService.DeleteHandler()},
		{kind: workitems.KindFileManagerExecute, handler: userOperations.ExecuteHandler()},
		{kind: workitems.KindCompletionExecute, handler: downloadCompletion.Handler()},
	}
	// Exact-set, not a count: one missing kind plus one duplicate keeps the
	// length equal and silently overwrites a handler, so the shipped binary
	// would answer 已接线 for a kind it never registered.
	if err := validateWorkerRegistrations(registrations, serverWorkItemKinds()); err != nil {
		return nil, err
	}
	handlers := make(map[string]runtimeworker.Handler, len(registrations))
	for _, registration := range registrations {
		handlers[registration.kind] = registration.handler
	}
	workerRegistry, err := runtimeworker.NewRegistry(runtimeworker.DefaultPolicies()...)
	if err != nil {
		return nil, err
	}
	return &workerGraph{
		registry: workerRegistry,
		handlers: handlers,
		build: func() (RuntimeLike, error) {
			return runtimeworker.NewRuntime(workStore, workerRegistry, handlers, "worker-"+installID, logger)
		},
	}, nil
}

func provideFileMaintenanceService(
	db *sql.DB,
	fileMaintenanceGraph *fileMaintenanceGraph,
	reconciliationService *reconciliationapplication.Service,
	observabilityService *observabilityapplication.Service,
	logger observabilityport.RuntimeLogger,
) httpapi.FileMaintenanceService {
	service := filemaintenance.NewService(
		fileMaintenanceGraph.policyService,
		fileMaintenanceGraph.replacementPolicies,
		fileMaintenanceGraph.planner,
		fileMaintenanceGraph.placementService,
		fileMaintenanceGraph.replacementDecisions,
	)
	service.SetOwnerTruthWires(
		fileMaintenanceGraph.calibrationService,
		fileMaintenanceGraph.cleanupService,
		fileMaintenanceGraph.alignmentOperations,
		currentScanAdapter{reconciliation: reconciliationService},
	)
	service.SetAnimeExistence(filemaintenance.NewSQLAnimeExistenceReader(db))
	service.SetOffsetMigration(fileMaintenanceGraph.offsetMigrationPlanner)
	service.SetParserPolicy(fileMaintenanceGraph.parserPolicies)
	service.SetFileEffectPolicy(fileMaintenanceGraph.fileEffectPolicies)
	// The 对齐（刷新元数据）variant re-pulls the bound work through the
	// ownership application before any alignment op is admitted; a nil port
	// (test graphs) keeps the variant typed-unavailable while plain alignment
	// stays cache-first unaffected.
	service.SetMetadataWorkRefresher(fileMaintenanceGraph.metadataRefresher)
	// The recovery confirm re-stats the candidate's frozen target and each
	// present companion through the library's no-follow METADATA observer and
	// records the confirm in the business journal; an unwired port fails
	// closed. The port is ObserveMetadataFile (stat + identity), not
	// ObserveFile (SHA-256 of the content): the confirm compares metadata
	// fingerprints, and a content digest would never match the frozen
	// candidate.
	service.SetRecoveryObserver(filesystem.OSFileObserver{})
	service.SetRecoveryActivityWriter(observabilityService)
	pathDiffService := filemaintenance.NewPathDiffService(
		db,
		fileMaintenanceGraph.namingRenderer,
		logger,
	)
	service.SetPathDiff(pathDiffService)
	return service
}

// catalogSearchEndpoints resolves the typed upstream endpoint and client for one
// provider keyword search. It mirrors ConfiguredReader: a saved enabled
// connection wins, no enabled connection falls back to the official public base,
// and an ambiguous configuration is a real error instead of a silent choice. A
// search request therefore never names or influences its endpoint.
type catalogSearchEndpoints struct {
	connections catalogadapter.ConnectionSource
	clients     catalogadapter.ConnectionHTTPClientResolver
}

func (r catalogSearchEndpoints) ResolveSearchEndpoint(ctx context.Context, provider string) (string, *http.Client, error) {
	if r.connections == nil {
		return "", nil, errors.New("catalog search connection source is not configured")
	}
	var kind configurationdomain.ConnectionKind
	switch provider {
	case catalogapplication.ProviderAniBT:
		kind = configurationdomain.ConnectionAniBT
	case catalogapplication.ProviderMikan:
		kind = configurationdomain.ConnectionMikan
	case catalogapplication.ProviderNyaa:
		kind = configurationdomain.ConnectionNyaa
	default:
		return "", nil, fmt.Errorf("unsupported catalog provider %q", provider)
	}
	connections, err := r.connections.ListConnections(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("list %s connections: %w", provider, err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, kind)
	if err != nil {
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return "", nil, err
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(kind)
		if !ok {
			return "", nil, err
		}
		connection = fallback
	}
	var client *http.Client
	if connection.NetworkRouteID != "" {
		if r.clients == nil {
			return "", nil, errors.New("catalog search network route client is not configured")
		}
		client, err = r.clients.ClientForConnection(ctx, connection)
		if err != nil {
			return "", nil, fmt.Errorf("resolve catalog search network route: %w", err)
		}
	}
	return connection.Endpoint, client, nil
}
