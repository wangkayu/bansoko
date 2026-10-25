//go:build wireinject

package main

import (
	"context"
	"database/sql"
	"net"

	accessapplication "bansoko/internal/accesscontrol/application"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animeapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	artifactapplication "bansoko/internal/artifact/application"
	catalogadapter "bansoko/internal/catalog/adapter"
	configurationapplication "bansoko/internal/configuration/application"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/downloadcompletion"
	"bansoko/internal/events"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/filemanager"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	metadataadapter "bansoko/internal/identitymetadata/adapter"
	"bansoko/internal/metadata/matcher"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	facts "bansoko/internal/release/facts"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/runtime/readiness"
	"bansoko/internal/transport/httpapi"
	"bansoko/internal/workitems"
	"github.com/google/wire"
)

func initializePluginRuntime(
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
	wire.Build(providePluginRuntime)
	return nil, nil
}

func initializeFileMaintenanceGraph(
	db *sql.DB,
	workStore *workitems.Store,
	draftService *draftapplication.Service,
	ownershipService *ownershipapplication.Service,
	denialZone filemanager.DenialZone,
	logger observabilityport.RuntimeLogger,
) (*fileMaintenanceGraph, error) {
	wire.Build(provideFileMaintenanceGraph)
	return nil, nil
}

func initializeCoreApplicationGraph(
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
	wire.Build(provideCoreApplicationGraph)
	return nil, nil
}

func initializeAutomationGraph(
	db *sql.DB,
	workStore *workitems.Store,
	eventHub *events.Hub,
	catalogDBSource catalogadapter.DBConnectionSource,
	metadataMatcher *matcher.Matcher,
	observabilityService *observabilityapplication.Service,
	logger observabilityport.RuntimeLogger,
	animeService *animeapplication.Service,
	ownershipService *ownershipapplication.Service,
	draftService *draftapplication.Service,
	configService *configurationapplication.Service,
) (*automationGraph, error) {
	wire.Build(provideAutomationGraph)
	return nil, nil
}

func initializeApplicationGraph(
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
	wire.Build(provideApplicationGraph)
	return nil, nil
}

func initializeWorkerGraph(
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
	wire.Build(provideWorkerGraph)
	return nil, nil
}

func initializeFileMaintenanceService(
	db *sql.DB,
	fileMaintenanceGraph *fileMaintenanceGraph,
	reconciliationService *reconciliationapplication.Service,
	logger observabilityport.RuntimeLogger,
) httpapi.FileMaintenanceService {
	wire.Build(provideFileMaintenanceService)
	return nil
}

func initializeHTTPServer(
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
	owner *configurationapplication.Service,
	plugin *pluginRuntime,
	candidateFactsRepo facts.Repository,
	posterConnections metadataadapter.ConnectionSource,
	posterClientResolver metadataadapter.HTTPClientResolver,
	fileManager httpapi.FileManagerService,
) (*httpapi.Server, error) {
	wire.Build(
		provideHTTPServerBase,
		provideQBManagementService,
		provideMediaRecognitionService,
		provideTorrentParseService,
		provideCandidateFactsService,
		provideTorrentStructureService,
		provideImagePosterService,
		provideImageCoverService,
		providePluginTargetService,
		provideTemplateCatalogService,
		provideHTTPDependencies,
		httpapi.NewProductionServer,
	)
	return nil, nil
}
