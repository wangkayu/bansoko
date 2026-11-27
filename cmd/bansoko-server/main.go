// bansoko-server is the composition root. It wires runtime configuration,
// the PostgreSQL pool, migration verification, and the application
// services into the HTTP transport. It contains no product policy.
package main

import (
	"bansoko/frontend"
	"bansoko/internal/episodetitle"
	episodetitleadapter "bansoko/internal/episodetitle/adapter"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	accessapplication "bansoko/internal/accesscontrol/application"
	acquisitionactivity "bansoko/internal/acquisition/terminal/activity"
	acquisitionsettlement "bansoko/internal/acquisition/terminal/settlement"
	animeapplication "bansoko/internal/anime/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	automationapplication "bansoko/internal/automation/application"
	catalogadapter "bansoko/internal/catalog/adapter"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	configurationdomain "bansoko/internal/configuration/domain"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/downloadcompletion"
	"bansoko/internal/eventactions"
	"bansoko/internal/events"
	"bansoko/internal/filemanager"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	hardeningtransport "bansoko/internal/hardening/transport"
	identitymetadataadapter "bansoko/internal/identitymetadata/adapter"
	identitymetadatapersistence "bansoko/internal/identitymetadata/adapter/persistence"
	metadatapersistence "bansoko/internal/metadata/adapter/persistence"
	"bansoko/internal/metadata/matcher"
	metadatarefresh "bansoko/internal/metadata/refresh"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parser"
	"bansoko/internal/parser/reltitletags"
	"bansoko/internal/parser/titleparser"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	"bansoko/internal/provider/outbound"
	outboundpersistence "bansoko/internal/provider/outbound/persistence"
	retentionapplication "bansoko/internal/recordretention/application"
	"bansoko/internal/rtruntime"
	"bansoko/internal/runtime/bootsecrets"
	bootstrapcmd "bansoko/internal/runtime/bootstrap"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/configdir"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/deploymentprovenance"
	"bansoko/internal/runtime/identity"
	maintenancepkg "bansoko/internal/runtime/maintenance"
	"bansoko/internal/runtime/migratecmd"
	"bansoko/internal/runtime/migrations"
	"bansoko/internal/runtime/readiness"
	"bansoko/internal/transport/httpapi"
	"bansoko/internal/update/adapter/pgexec"
	updateapplication "bansoko/internal/update/application"
	localupdate "bansoko/internal/update/local"
	"bansoko/internal/workitems"
)

// fatalStartup reports a startup failure as a structured runtime event and
// exits with status 1. The message and reason code are stable identifiers;
// raw error details, DSNs and paths are intentionally omitted.
func fatalStartup(logger observabilityport.RuntimeLogger, message, reasonCode string) {
	fatalStartupDetail(logger, message, reasonCode, "")
}

// fatalStartupDetail emits a bounded, non-secret startup detail. Callers must
// pass only already-redacted context such as a configuration path and field.
func fatalStartupDetail(logger observabilityport.RuntimeLogger, message, reasonCode, detail string) {
	if detail != "" {
		message += ": " + detail
	}
	logger.Error(context.Background(), observabilityport.RuntimeEvent{
		Message:    message,
		Component:  "runtime",
		Operation:  "startup",
		Outcome:    "failure",
		ReasonCode: reasonCode,
		ErrorClass: "startup_fatal",
	})
	os.Exit(1)
}

func main() {
	argv, err := exportConfigDirFlag(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	mode, args := runtimeCommand(argv)
	if err := buildinfo.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	switch mode {
	case "start":
		// Native lifecycle: the unified PID1 flow without container privilege
		// management; it re-execs this binary for migrate/serve children.
		os.Exit(bootstrapcmd.Start("server"))
	case "bootstrap":
		os.Exit(bootstrapcmd.Main(args))
	case bootstrapcmd.PrivateReadinessCommand:
		os.Exit(runServe(nil, true))
	case "migrate":
		migratecmd.Main(args)
		return
	case "update":
		os.Exit(localupdate.Main(args))
	case "recover":
		os.Exit(bootstrapcmd.Recover(args, os.Stdout, os.Stderr))
	case "reset-password":
		os.Exit(bootstrapcmd.ResetPassword(args, os.Stdin, os.Stdout, os.Stderr))
	case "version":
		os.Exit(runVersion(os.Stdout, args...))
	case "doctor":
		os.Exit(runDoctor(args, os.Stdout))
	default:
		os.Exit(runServe(args, false))
	}
}

// exportConfigDirFlag strips the global option before any subcommand parses
// its flags. Exporting the explicit value also passes it to re-exec'd children.
func exportConfigDirFlag(args []string) ([]string, error) {
	var dir string
	remaining := make([]string, 0, len(args))
	name := "--" + configdir.FlagName
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			remaining = append(remaining, args[i:]...)
			i = len(args)
		case arg == name:
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
				return nil, fmt.Errorf("%s requires a directory", name)
			}
			i++
			dir = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, name+"="):
			dir = strings.TrimSpace(strings.TrimPrefix(arg, name+"="))
		default:
			remaining = append(remaining, arg)
			continue
		}
		if arg != "--" && dir == "" {
			return nil, fmt.Errorf("%s requires a directory", name)
		}
	}
	if dir != "" {
		if err := os.Setenv(configdir.EnvConfigDir, dir); err != nil {
			return nil, fmt.Errorf("set %s: %w", configdir.EnvConfigDir, err)
		}
	}
	return remaining, nil
}

func runtimeCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "serve", args
	}
	switch args[0] {
	case bootstrapcmd.PrivateReadinessCommand:
		return args[0], args[1:]
	case "bootstrap", "migrate", "serve", "update", "start", "recover", "reset-password", "version", "doctor":
		return args[0], args[1:]
	default:
		return "serve", args
	}
}

func runServe(args []string, probe bool) int {
	runtimeLogger := observabilityapplication.NewTeedRuntimeLogger(os.Stderr)
	// Native standalone serving (no start wrapper): load the serving facts the
	// deployment start persisted in the config directory. The load is read-only
	// and explicit environment values always win; a missing file falls through
	// to the standard configuration checks below.
	if strings.TrimSpace(os.Getenv(bootsecrets.EnvSessionSecret)) == "" ||
		strings.TrimSpace(os.Getenv(bootsecrets.EnvSecretEncryptionKey)) == "" ||
		strings.TrimSpace(os.Getenv(bootsecrets.EnvRuntimeDatabaseURL)) == "" {
		configDir := configdir.Resolve(configdir.FlagValue(args), os.Getenv)
		if err := bootsecrets.LoadServingEnvironment(configDir, os.Getenv, os.Setenv); err != nil && !errors.Is(err, os.ErrNotExist) {
			fatalStartup(runtimeLogger, "deployment secrets in the config directory are not usable", "serving_environment_unusable")
		}
	}
	cfg, err := config.FromFlags(args)
	if err != nil {
		fatalStartup(runtimeLogger, "configuration parse failed", "config_parse_failed")
	}
	// Once the configuration root is known, the same logger also writes the
	// bounded on-disk history read by the owner-only log dialog. An unwritable
	// directory degrades to the in-memory tail without blocking startup.
	if !probe {
		runtimeLogger = observabilityapplication.NewPersistentRuntimeLogger(os.Stderr, filepath.Join(cfg.ConfigDir, observabilityapplication.RuntimeLogDirName))
	}
	defer func() { _ = runtimeLogger.Close() }()
	// Keep the deployment path in the operator's console, outside the
	// redacted application log exposed by the Web UI.
	fmt.Fprintf(os.Stderr, "[startup] resolved config_dir=%q\n", cfg.ConfigDir)
	// The persistent session secret binds owner sessions to this instance.
	// Development may run with skip-auth, but a real session secret is still
	// required so bootstrap/login behave identically across environments.
	if len(strings.TrimSpace(cfg.SessionSecret)) < 32 {
		fatalStartup(runtimeLogger, "BANSOKO_SESSION_SECRET must contain at least 32 characters", "session_secret_invalid")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A restore interrupted by a crash may have the live database renamed
	// aside; settle it before the serving pool connects.
	if !probe {
		recoverInterruptedRestore(ctx, cfg.DatabaseURL, runtimeLogger)
	}

	db, err := database.Open(ctx, database.Config{DSN: cfg.DatabaseURL, ReadOnly: probe})
	if err != nil {
		fatalStartup(runtimeLogger, "database open failed", "database_open_failed")
	}
	defer db.Close()
	if err := (migrations.Runner{DB: db, Dir: cfg.SchemaDir}).Verify(ctx); err != nil {
		fatalStartup(runtimeLogger, "migration verify failed", "migration_verify_failed")
	}
	// Production must connect with the segregated runtime role. A superuser or
	// schema-owner connection would hold DDL/trigger privileges the runtime is
	// never allowed to have, so the server refuses to serve instead of silently
	// running with the owner's capability.
	if probe || cfg.Environment == "production" {
		if err := database.AssertRuntimeRoleSeparated(ctx, db); err != nil {
			fatalStartup(runtimeLogger, "runtime database role separation violated", "runtime_role_separation_violated")
		}
	}
	// Adopted reltitle binary: a channel-installed build at
	// <config>/rt/reltitle overrides the compiled-in parser when it passes the
	// adoption chain (a corroborating installed.json, same major.minor line and
	// strictly newer than the builtin, -version, an rt-serve-1 hello and one
	// smoke parse). A manually placed or superseded build is never executed; a
	// refused file is a state to report, never a startup failure: the builtin
	// parser keeps working.
	var rtClient *rtruntime.Client
	var rtInfo rtruntime.Info
	if !probe {
		rtClient, rtInfo = rtruntime.Probe(ctx, cfg.ConfigDir, reltitletags.BuiltinVersion())
	}
	switch {
	case rtClient != nil:
		if adoptErr := reltitletags.AdoptExternal(rtClient); adoptErr != nil {
			_ = rtClient.Close()
			runtimeLogger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:   "the installed reltitle binary was refused: " + adoptErr.Error(),
				Component: "runtime", Operation: "reltitle_adopt", Outcome: "warning",
				ReasonCode: "reltitle_external_refused",
			})
		} else {
			defer func() { _ = rtClient.Close() }()
			runtimeLogger.Info(ctx, observabilityport.RuntimeEvent{
				Message:   "using the installed reltitle " + rtInfo.Version,
				Component: "runtime", Operation: "reltitle_adopt", Outcome: "success",
				ReasonCode: "reltitle_external_adopted",
			})
		}
	case rtInfo.Note != "":
		runtimeLogger.Warn(ctx, observabilityport.RuntimeEvent{
			Message:   rtInfo.Note,
			Component: "runtime", Operation: "reltitle_adopt", Outcome: "warning",
			ReasonCode: "reltitle_external_refused",
		})
	}

	var installID string
	if probe {
		installID, err = identity.ReadInstallID(ctx, db)
	} else {
		installID, err = identity.EnsureInstallID(ctx, db)
	}
	if err != nil {
		fatalStartup(runtimeLogger, "install id failed", "install_id_failed")
	}

	access := accessapplication.NewServiceWithSessionSecret(db, []byte(cfg.SessionSecret))
	eventHub := events.NewHub()
	defer eventHub.Close()

	workStore, err := workitems.NewStore(db, 0, runtimeLogger)
	if err != nil {
		fatalStartup(runtimeLogger, "work store failed", "work_store_failed")
	}
	// The closed parser registry owns one registered adapter per kind, and the
	// recognition registry's process default already installs the bundled
	// regex and reltitle parsers. Startup therefore asserts that both catalog
	// kinds (reltitle is the creation default) are resolvable instead of
	// registering a second adapter for the same kind: a duplicate registration
	// is rejected by the registry, and turning that rejection into a fatal
	// startup would break boot for every deployment.
	if !parser.IsRegistered(parser.ParserRegex) || !parser.IsRegistered(parser.ParserReltitle) {
		fatalStartup(runtimeLogger, "parser registry failed", "parser_registry_failed")
	}
	var dbSecretStore *configurationapplication.DatabaseSecretStore
	if len(strings.TrimSpace(cfg.SecretEncryptionKey)) >= 32 {
		store, err := configurationapplication.NewDatabaseSecretStore(db, []byte(cfg.SecretEncryptionKey), configurationpersistence.NewRepository(db))
		if err != nil {
			fatalStartup(runtimeLogger, "secret store failed", "secret_store_failed")
		}
		dbSecretStore = store
	} else {
		runtimeLogger.Warn(ctx, observabilityport.RuntimeEvent{
			Message:    "BANSOKO_SECRET_ENCRYPTION_KEY is not configured; connection secret writes will report SECRETS_UNAVAILABLE",
			Component:  "runtime",
			Operation:  "startup",
			Outcome:    "warning",
			ReasonCode: "secret_encryption_key_missing",
		})
	}
	if dbSecretStore != nil {
		// PAT raw values are encrypted at rest so the UI can keep showing the
		// key and allow copy for its lifetime; without the deployment key the
		// list simply hides raw values while hash authentication still works.
		access.SetRawTokenStore(dbSecretStore)
	}
	configService := configurationapplication.NewService(db, dbSecretStore)
	configService.SetEventHub(eventHub)
	configService.SetWorkItems(workStore)
	httpConfigService := configurationapplication.NewHTTPService(configService)
	runtimeConfiguration := configService
	// Seed the built-in provider/metadata connections (mikan/anibt/bgm/tmdb)
	// on a fresh instance so the settings UI shows editable rows and adapters
	// have real connection facts. The operation is idempotent and never
	// overwrites a user's existing rows. Mikan/AniBT adapters also fall back
	// to the official endpoint when no row exists, so a failed seed does not
	// block startup.
	if !probe {
		if err := configService.SeedDefaultConnections(ctx); err != nil {
			runtimeLogger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "default connection seed failed (provider fallback remains available)",
				Component:  "runtime",
				Operation:  "startup",
				Outcome:    "warning",
				ReasonCode: "connection_seed_failed",
				ErrorClass: "connection_seed",
			})
		}
	}

	providerOutbound, err := outbound.New(ctx, outbound.DefaultPolicies(), nil, outboundpersistence.New(db))
	if err != nil {
		fatalStartup(runtimeLogger, "provider outbound health restore failed", "provider_outbound_restore_failed")
	}

	fixtureAllowlist, fixtureResolver := cfg.TestFixtureNetwork()

	animeService, err := animeapplication.NewService(db, workStore, animeapplication.HTTPFeedFetcher{
		Outbound:          providerOutbound,
		AllowPrivateHosts: fixtureAllowlist,
		Resolver:          fixtureResolver,
	})
	if err != nil {
		fatalStartup(runtimeLogger, "anime service build failed", "anime_service_build_failed")
	}
	ownershipService, err := provideOwnershipService(db, workStore, configService, eventHub)
	if err != nil {
		fatalStartup(runtimeLogger, "ownership service build failed", "ownership_service_build_failed")
	}
	draftService, err := provideDraftService(db, workStore, configService)
	if err != nil {
		fatalStartup(runtimeLogger, "draft service build failed", "draft_service_build_failed")
	}
	// The file manager browses the container the deployment actually created.
	// Its refusal set is that deployment's own state directories, resolved from
	// runtime configuration here; the browser itself hardcodes no container path.
	denialZone := filemanager.NewDenialZone(config.ApplicationDataDirs(os.Getenv)...)
	fileMaintenanceGraph, err := initializeFileMaintenanceGraph(db, workStore, draftService, ownershipService, denialZone, runtimeLogger)
	if err != nil {
		fatalStartup(runtimeLogger, "file maintenance graph failed", "file_maintenance_graph_failed")
	}
	replacementTrigger := fileMaintenanceGraph.replacementTrigger
	cleanupService := fileMaintenanceGraph.cleanupService
	placementService := fileMaintenanceGraph.placementService
	alignmentOperations := fileMaintenanceGraph.alignmentOperations
	calibrationService := fileMaintenanceGraph.calibrationService
	replacementDecisions := fileMaintenanceGraph.replacementDecisions
	candidateFactsRepo := fileMaintenanceGraph.candidateFactsRepo

	coreGraph, err := initializeCoreApplicationGraph(
		db,
		providerOutbound,
		workStore,
		httpConfigService,
		runtimeConfiguration,
		fixtureAllowlist,
		fixtureResolver,
		eventHub,
		runtimeLogger,
		animeService,
		fileMaintenanceGraph,
	)
	if err != nil {
		fatalStartup(runtimeLogger, "core application graph failed", "core_application_graph_failed")
	}
	downloadService := coreGraph.downloadService
	rssService := coreGraph.rssService
	targetStore := coreGraph.targetStore
	artifactService := coreGraph.artifactService
	fileOperationService := coreGraph.fileOperationService
	reconciliationService := coreGraph.reconciliationService
	// The 刷新元数据 re-pull (the single positive-cache bypass) is shared by the
	// metadata binding surface and the 对齐（刷新元数据）alignment variant; the
	// alignment lane reaches it through the ownership application. The port is
	// planted on the graph before the file-maintenance service is built so the
	// provider can hand it to the concrete service.
	fileMaintenanceGraph.metadataRefresher = metadataRefresherOwnershipAdapter{ownership: ownershipService}

	// The provider episode titles (bgm.ep_title / tmdb.ep_title) share one
	// cache-first Querier across the whitelisted naming renders and
	// notification admission. Other renders and all delivery retries stay
	// network-free. The same persistent cache is the store the 刷新元数据
	// re-pull overwrites — the only positive-cache bypass.
	episodeTitleQueries := provideEpisodeTitleQueries(db, runtimeConfiguration, dbSecretStore)
	coreGraph.fileOperationService.SetEpisodeTitleQueries(episodeTitleQueries)
	fileMaintenanceGraph.namingRenderer.SetEpisodeTitleQueries(episodeTitleQueries)
	// The 刷新元数据 re-pull (the single positive-cache bypass) is shared by the
	// metadata binding surface and the 对齐（刷新元数据）alignment variant.
	metadataRefresher := identitymetadataadapter.ConfiguredWorkRefresher{
		Connections:   catalogadapter.DBConnectionSource{DB: db},
		Secrets:       dbSecretStore,
		Titles:        identitymetadatapersistence.NewRepository(db),
		EpisodeTitles: episodeTitleQueries.Cache,
	}
	ownershipService.SetMetadataWorkRefresher(metadataRefresher)

	applicationGraph, err := initializeApplicationGraph(
		db,
		providerOutbound,
		workStore,
		dbSecretStore,
		runtimeConfiguration,
		eventHub,
		runtimeLogger,
		fixtureAllowlist,
		fixtureResolver,
		animeService,
		ownershipService,
		draftService,
		rssService,
		downloadService,
		artifactService,
		fileOperationService,
		replacementDecisions,
		cleanupService,
		placementService,
		replacementTrigger,
		fileMaintenanceGraph,
		targetStore,
	)
	if err != nil {
		fatalStartup(runtimeLogger, "application graph failed", "application_graph_failed")
	}
	retryService := applicationGraph.retryService
	catalogDBSource := applicationGraph.catalogDBSource
	metadataSearcher := applicationGraph.metadataSearcher
	observabilityService := applicationGraph.observabilityService
	fileMaintenanceService := initializeFileMaintenanceService(db, fileMaintenanceGraph, reconciliationService, observabilityService, runtimeLogger)
	catalogService := applicationGraph.catalogService
	retentionJanitor := applicationGraph.retentionJanitor
	oneShotReclaim := applicationGraph.oneShotReclaim
	acquisitionSettle := applicationGraph.acquisitionSettle
	// N1: the run log of typed acquisitions is derived after commit by the
	// independent consumer; no acquisition stage writes Activity.
	acquisitionActivity := &acquisitionactivity.Consumer{DB: db, Writer: observabilityService, Diagnostics: observabilityService}
	oneShotService := applicationGraph.oneShotService

	readmodelService, err := provideReadmodelService(db, ownershipService, animeService, draftService, catalogService, applicationGraph.seedPreviewSource, fileMaintenanceGraph.mediaFactsStore)
	if err != nil {
		fatalStartup(runtimeLogger, "readmodel service build failed", "readmodel_service_build_failed")
	}
	// The plugin target owner commits secret references in the same transaction
	// as the target row. When the deployment key is not configured the secret
	// store is a true nil so secret writes report SECRETS_UNAVAILABLE.
	var pluginTargetSecrets plugintargetapplication.SecretStore
	if dbSecretStore != nil {
		pluginTargetSecrets = dbSecretStore
	}

	// The plugin deliver handler resolves secrets at delivery time. When the
	// deployment key is not configured the resolver is nil and deliveries that
	// require a secret will fail with secrets_unavailable.
	var pluginSecretResolver plugintargetapplication.SecretResolver
	if dbSecretStore != nil {
		pluginSecretResolver = dbSecretStore
	}

	pluginRuntime, err := initializePluginRuntime(
		db,
		pluginTargetSecrets,
		pluginSecretResolver,
		workStore,
		runtimeLogger,
		downloadService,
		fileOperationService,
		rssService,
		observabilityService,
		catalogDBSource,
		runtimeConfiguration,
		dbSecretStore,
		episodeTitleQueries,
	)
	if err != nil {
		fatalStartup(runtimeLogger, "plugin runtime build failed", "plugin_runtime_build_failed")
	}
	admissionResolver := pluginRuntime.admissionRunner
	if err := wireProductionBusinessPublishers(coreGraph, fileMaintenanceGraph, draftService, ownershipService, pluginRuntime.targetService, eventHub, observabilityService); err != nil {
		fatalStartup(runtimeLogger, "business publisher wiring is incomplete: "+err.Error(), "business_publisher_wiring_failed")
	}

	// The maintenance gate is the single runtime write gate for Web/CLI/MCP.
	// It is created BEFORE any background producer or worker exists: a boot in
	// maintenance (recovery run) must never start producers/workers at all.
	// BANSOKO_WORKER_DISABLED only skips WorkItem consumption (scratch restore /
	// rehearsals); it does NOT enter maintenance, so the bootstrap owner
	// creation and login flows still work on a restored instance.
	maintenanceGate := maintenancepkg.NewGate()
	// P2.1: deployment recovery gate sync. If a recovery run exists and is not
	// settled with a passed automation resume gate, the instance boots into
	// maintenance (writes rejected, automation disabled) and stays there until
	// bansoko-recovery completes the run. A missing checkpoint table (pre-030
	// backup) simply means "no recovery run": stay serving.
	if err := syncMaintenanceFromRecovery(ctx, db, maintenanceGate); err != nil {
		fatalStartup(runtimeLogger, "recovery gate sync failed", "recovery_gate_sync_failed")
	}

	// MF3: the download-completion action on the qBittorrent task of a
	// succeeded acquisition. The pass runs as a producer; the action is the
	// completion_execute WorkItem handler.
	completionService, err := downloadcompletion.NewService(
		&downloadcompletion.Store{DB: db, Work: workStore},
		fileMaintenanceGraph.mediaFactsStore,
		downloadcompletion.TorrentAdapter{Client: qbittorrent.ConfiguredClient{Source: httpConfigService}},
	)
	if err != nil {
		fatalStartup(runtimeLogger, "download completion build failed", "download_completion_build_failed")
	}
	completionService.SetEventHub(eventHub)
	downloadService.SetCancelRemover(cancelRemover{completion: completionService})

	workerGraph, err := initializeWorkerGraph(
		workStore,
		installID,
		runtimeLogger,
		rssService,
		downloadService,
		artifactService,
		fileOperationService,
		pluginRuntime,
		reconciliationService,
		calibrationService,
		alignmentOperations,
		replacementDecisions,
		cleanupService,
		fileMaintenanceGraph.userOperations,
		completionService,
	)
	if err != nil {
		fatalStartup(runtimeLogger, "worker graph failed", "worker_graph_failed")
	}
	workerRegistry := workerGraph.registry
	buildWorker := workerGraph.build
	// P3.3: FileMaintenance periodic scan loop and the automation dispatcher
	// are background producers owned by the BackgroundRuntime below (it is
	// wired after the dispatcher exists).

	// Metadata matching restores the last local bangumi-data snapshot only.
	// The background producer performs all CDN access after startup.
	metadataSnapshotStore := metadatapersistence.NewRepository(db)
	metadataIndex, err := metadatarefresh.Restore(ctx, metadataSnapshotStore)
	if err != nil {
		runtimeLogger.Warn(ctx, observabilityport.RuntimeEvent{
			Message:    "bangumi-data snapshot restore failed (API fallback will be used)",
			Component:  "runtime",
			Operation:  "startup",
			Outcome:    "warning",
			ReasonCode: "bangumi_index_load_failed",
			ErrorClass: "metadata_matcher",
		})
	}
	metadataTrimmer := matcher.NewTitleTrimmer()
	// The BGM layer reads the optional bgm connection (endpoint + egress route)
	// on each call; without one it keeps the public endpoint and a direct client.
	bgmMatcher := &matcher.BGMMatcher{
		Outbound:       providerOutbound,
		Connections:    catalogDBSource,
		ClientResolver: runtimeConfiguration,
		Client:         &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()},
	}
	tmdbMatcher := &matcher.TMDBMatcher{
		Outbound:       providerOutbound,
		Connections:    catalogDBSource,
		Secrets:        dbSecretStore,
		ClientResolver: runtimeConfiguration,
		Client:         &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()},
		Trimmer:        metadataTrimmer,
	}
	metadataMatcher := matcher.NewMatcher(metadataIndex, bgmMatcher, tmdbMatcher, runtimeLogger)
	metadataSearcher.SetMatcher(metadataMatcher)
	bangumiDataRefresher := metadatarefresh.NewService(metadataSnapshotStore, metadataMatcher, &http.Client{Timeout: 30 * time.Second, Transport: hardeningtransport.DirectTransport()}, runtimeLogger, metadataIndex != nil)
	metadataMatcher.SetDiagnostics(observabilityService)
	// B13 P2: the file manager's match lists the bound show's seasons and
	// episodes through the same connections and egress routes, with an
	// in-process TTL cache.
	tmdbEpisodes := episodetitleadapter.TMDBClient{
		Connections:    catalogDBSource,
		Secrets:        dbSecretStore,
		ClientResolver: runtimeConfiguration,
		Client:         &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()},
	}
	episodeTitleLookup := episodetitle.New(episodetitle.Options{
		Clients:       map[episodetitle.Source]episodetitle.Client{episodetitle.SourceTMDB: tmdbEpisodes},
		SeasonClients: map[episodetitle.Source]episodetitle.SeasonClient{episodetitle.SourceTMDB: tmdbEpisodes},
	})
	// Candidate browsing reads the same providers before a binding exists. It
	// uses its own in-process cache, never the persistent episode-title cache
	// naming renders read, and is read-only.
	bgmEpisodes := episodetitleadapter.BGMClient{
		Connections:    catalogDBSource,
		ClientResolver: runtimeConfiguration,
		Client:         &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()},
	}
	candidateEpisodeLookup := episodetitle.New(episodetitle.Options{
		Clients: map[episodetitle.Source]episodetitle.Client{
			episodetitle.SourceTMDB: tmdbEpisodes,
			episodetitle.SourceBGM:  bgmEpisodes,
		},
		SeasonClients:  map[episodetitle.Source]episodetitle.SeasonClient{episodetitle.SourceTMDB: tmdbEpisodes},
		SectionClients: map[episodetitle.Source]episodetitle.SectionedClient{episodetitle.SourceBGM: bgmEpisodes},
	})
	metadataSearcher.SetEpisodeListLookup(candidateEpisodeLookup)
	// B13 P1: the file manager's writes.
	if err := wireFileManagerCommands(fileMaintenanceGraph, db, workStore, observabilityService, observabilityService, eventHub, reconciliationService, episodeTitleLookup); err != nil {
		fatalStartup(runtimeLogger, "file manager commands failed", "file_manager_commands_failed")
	}
	automationGraph, err := initializeAutomationGraph(
		db,
		providerOutbound,
		workStore,
		eventHub,
		catalogDBSource,
		metadataMatcher,
		observabilityService,
		runtimeLogger,
		animeService,
		ownershipService,
		draftService,
		configService,
	)
	if err != nil {
		fatalStartup(runtimeLogger, "automation graph failed", "automation_graph_failed")
	}
	automationService := automationGraph.service
	dispatcher := automationGraph.dispatcher
	// catalog_refresh runs the provider catalog refresh inline; the catalog
	// service is built by the application graph after the automation graph's
	// providers, so it is attached here rather than through wire.
	dispatcher.CatalogRefresher = catalogService

	// Backup export (download) and restore (upload). A verified restore asks
	// for the handoff below.
	restartGate := &sync.Mutex{}
	backupService, err := buildBackupService(ctx, db, cfg.DatabaseURL, cfg.SchemaDir, runtimeLogger, restartGate)
	if err != nil {
		fatalStartup(runtimeLogger, "backup service build failed", "backup_build_failed")
	}

	// A settled acquisition root asks the one-shot lifecycle to drain its
	// marker now instead of on the next ten-minute tick, so a failed one-shot
	// offers 重试 / 重新规划 as soon as its record shows the failure.
	oneShotDrainDue := make(chan struct{}, 1)

	// The metadata refresh sweep reuses the ownership service's per-anime
	// re-pull. It runs one interval after boot, never at startup, so a restart
	// does not re-pull every bound work.
	metadataRefreshSweep := ownershipapplication.NewMetadataRefreshSweep(ownershipService, runtimeLogger)

	// BackgroundRuntime: composition-root lifecycle controller for the worker
	// runtime and every producer. Booting in maintenance (recovery run)
	// keeps it stopped; the runtime owns lifecycle transitions.
	eventActions := eventactions.New(db, eventHub, runtimeLogger)
	producers := []func(context.Context){
		eventActions.Run,
		// One bounded pass removes transient files an interrupted action left
		// beside a library target; nothing else ever revisits their names.
		func(ctx context.Context) { runTransientResidueSweep(ctx, db, runtimeLogger) },
		func(ctx context.Context) { go admissionResolver.Run(ctx) },
		func(ctx context.Context) { runQBSyncDispatcher(ctx, downloadService, runtimeLogger) },
		func(ctx context.Context) { runRecordRetentionMaintenance(ctx, retentionJanitor, runtimeLogger) },
		func(ctx context.Context) { runMetadataRefreshSweep(ctx, metadataRefreshSweep, runtimeLogger) },
		bangumiDataRefresher.Run,
		func(ctx context.Context) {
			runOneShotReclaimMaintenance(ctx, oneShotReclaim, oneShotDrainDue, eventHub, runtimeLogger)
		},
		func(ctx context.Context) {
			runAcquisitionSettlement(ctx, acquisitionSettle, eventHub, oneShotDrainDue, runtimeLogger)
		},
		func(ctx context.Context) { runAcquisitionActivity(ctx, acquisitionActivity, eventHub, runtimeLogger) },
		func(ctx context.Context) { runDownloadCompletion(ctx, completionService, runtimeLogger) },
		func(ctx context.Context) {
			startFileMaintenanceScheduler(ctx, db, fileMaintenanceGraph.policyService, reconciliationService, readmodelService, replacementDecisions, observabilityService, rssService, runtimeLogger)
		},
		func(ctx context.Context) { runAutomationDispatcher(ctx, dispatcher, runtimeLogger) },
		func(ctx context.Context) {
			runStrandedFileWorkRevival(ctx, fileOperationService.ReviveStrandedWork, runtimeLogger)
		},
		func(ctx context.Context) { runConfigWaitSweep(ctx, db, runtimeLogger) },
	}
	backgroundRuntime := NewBackgroundRuntime(maintenanceGate, buildWorker, producers, runtimeLogger)
	if probe {
		if !cfg.WorkerDisabled && !maintenanceGate.RejectWrites() {
			if _, err := buildWorker(); err != nil {
				fatalStartup(runtimeLogger, "worker validation failed", "background_runtime_start_failed")
			}
		}
	} else if cfg.WorkerDisabled {
		runtimeLogger.Warn(ctx, observabilityport.RuntimeEvent{
			Message:    "worker disabled; no WorkItem consumption",
			Component:  "runtime",
			Operation:  "startup",
			Outcome:    "warning",
			ReasonCode: "worker_disabled",
		})
	} else if !maintenanceGate.RejectWrites() {
		if err := backgroundRuntime.Start(); err != nil {
			fatalStartup(runtimeLogger, "background runtime start failed", "background_runtime_start_failed")
		}
	} else {
		runtimeLogger.Warn(ctx, observabilityport.RuntimeEvent{
			Message:    "booting in maintenance: background runtime stays stopped until the deployment acceptance passes",
			Component:  "runtime",
			Operation:  "startup",
			Outcome:    "warning",
			ReasonCode: "maintenance_boot",
		})
	}
	defer func() {
		if err := backgroundRuntime.Stop(); err != nil {
			fatalStartup(runtimeLogger, "background drain failed; handlers are not stopped", "background_runtime_drain_failed")
		}
	}()

	// Readiness evidence providers. Secret availability is proven by the
	// configured encryption key path; qB health is a degraded-only check. The
	// deployment-identity check consumes the bootstrap-injected three-layer
	// identity plus one live updates-volume pointer read (design §6).
	updatesRootForIdentity := deploymentidentity.UpdatesRoot(os.Getenv)
	provider := deploymentIdentityProvider{
		updatesRoot:   updatesRootForIdentity,
		requireSource: probe || cfg.Environment == "production" && cfg.UpdateMode == "embedded",
	}
	var deployment readiness.DeploymentIdentityProvider = provider
	if probe {
		deployment = privateDeploymentIdentity{provider}
	}
	readinessChecker := readiness.New(readiness.Dependencies{
		DB:                           db,
		Migrations:                   migrations.Runner{DB: db, Dir: cfg.SchemaDir},
		Build:                        buildinfo.Current(),
		Bootstrap:                    access,
		Secrets:                      secretAvailable{store: dbSecretStore},
		Workers:                      workerLanes{kinds: workerRegistry.Kinds()},
		WorkersIntentionallyDisabled: probe,
		Gate:                         maintenanceGate,
		QB:                           qbHealthProbe{connections: configService},
		Deployment:                   deployment,
	})

	// Embedded self-update channel (Sub B Step 3, BANSOKO_UPDATE_MODE=embedded).
	// Manual mode reads durable status but does not wire update networking or handoff.
	updatesRoot := deploymentidentity.UpdatesRoot(os.Getenv)
	var updateAPI httpapi.EmbeddedUpdateService = updateapplication.NewStatusReader(updatesRoot)
	var embeddedUpdateService interface {
		httpapi.EmbeddedUpdateService
		ExitRequested() <-chan struct{}
	}
	if cfg.UpdateMode == "embedded" {
		if err := updateapplication.CheckRetiredSourceEnv(os.Getenv); err != nil {
			fatalStartup(runtimeLogger, err.Error(), "embedded_update_retired_env")
		}
		updateSvc, svcErr := updateapplication.NewService(updateapplication.Deps{
			RestartGate:      restartGate,
			CheckBackupTools: func() error { return pgexec.CheckTools(pgexec.DefaultCandidatePaths) },
			DB:               db, UpdatesRoot: updatesRoot,
			Lane:             os.Getenv(updateapplication.EnvLane),
			ChannelBase:      os.Getenv(updateapplication.EnvChannelBase),
			Production:       cfg.Environment == "production",
			UpdateConnection: configService,
			RunningOrder:     buildinfo.Current().Order,
			BuildReleaseID:   buildinfo.Current().BuildID, BuildVersion: buildinfo.Current().Version,
			BuildCompatibilityVersion: buildinfo.Current().BaseCompatibilityVersion, BuildBootstrapVersion: os.Getenv(deploymentidentity.EnvBootstrapVersion),
			UpdateMode:     cfg.UpdateMode,
			TargetPlatform: runtime.GOOS, TargetArchitecture: runtime.GOARCH,
			Diagnostics: observabilityService, Logger: runtimeLogger,
		})
		if svcErr != nil {
			fatalStartup(runtimeLogger, "embedded update service failed", "embedded_update_service_failed")
		}
		embeddedUpdateService = updateSvc
		updateAPI = updateSvc
		configService.SetUpdateSourceProbe(updateSvc)
	}
	// The draft service remains the candidate/subscribe mutation API; owner
	// mutations are the ownership service's and one-shot seed admission has its
	// own application service, each injected as a separate transport dependency.
	apiServer, err := initializeHTTPServer(
		db,
		access,
		eventHub,
		draftService,
		ownershipService,
		readmodelService,
		rssService,
		oneShotService,
		catalogService,
		applicationGraph.seedSearch,
		httpConfigService,
		retryService,
		automationService,
		configService,
		metadataSearcher,
		observabilityService,
		observabilityService,
		observabilityService,
		updateAPI,
		reconciliationService,
		fileMaintenanceService,
		serverEnvironment(cfg.Environment),
		serverSkipAuth(cfg.SkipAuth),
		serverFrontendFS(httpapi.ResolveFrontendFS(cfg.FrontendDir, frontend.FS())),
		maintenanceGate,
		readinessChecker,
		runtimeLogger,
		configService,
		pluginRuntime,
		candidateFactsRepo,
		catalogDBSource,
		runtimeConfiguration,
		fileMaintenanceGraph.fileManager,
	)
	if err != nil {
		_ = db.Close()
		fatalStartup(runtimeLogger, "http server build failed", "http_server_build_failed")
	}
	apiServer.SetUpstreamHealthReader(outboundpersistence.HealthReader{Store: outboundpersistence.New(db), Live: providerOutbound.Health})
	importRetry, err := animeapplication.NewImportRetryService(db)
	if err != nil {
		_ = db.Close()
		fatalStartup(runtimeLogger, "import retry service build failed", "http_server_build_failed")
	}
	importRetry.SetEventHub(eventHub)
	apiServer.SetImportRetry(importRetry)
	downloadCancel, err := animeapplication.NewDownloadCancelService(db)
	if err != nil {
		_ = db.Close()
		fatalStartup(runtimeLogger, "download cancel service build failed", "http_server_build_failed")
	}
	downloadCancel.SetEventHub(eventHub)
	apiServer.SetDownloadCancel(downloadCancel)
	apiServer.SetBackup(backupService)
	apiServer.SetEventActions(eventActions)
	// The reltitle parser component has its own channel, independent of the
	// Bansoko application update. The startup probe's note and source are
	// passed through so the read model explains a refused binary. An
	// unknown lane value falls back to stable; the warning keeps that from
	// passing silently while the deployment reads the other branch.
	rtLane, rtLaneKnown := titleparser.NormalizeLane(os.Getenv(titleparser.EnvLane))
	if !rtLaneKnown {
		runtimeLogger.Warn(ctx, observabilityport.RuntimeEvent{
			Message:   "unknown " + titleparser.EnvLane + " value " + strconv.Quote(os.Getenv(titleparser.EnvLane)) + "; the reltitle channel uses the stable lane",
			Component: "runtime", Operation: "reltitle_lane", Outcome: "warning",
			ReasonCode: "reltitle_lane_unknown",
		})
	}
	apiServer.SetTitleParser(titleparser.New(titleparser.Options{
		ConfigRoot: cfg.ConfigDir,
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: hardeningtransport.DirectTransport()},
		Note:       rtInfo.Note,
		Source:     rtInfo.Source,
		Lane:       rtLane,
	}))

	if probe {
		return bootstrapcmd.RunPrivateReadiness(readinessChecker.Report)
	}

	// The local command talks to the updater in this actual base/overlay
	// process. It never derives a second updater from /app or the current
	// pointer. Manual mode and bootstrap maintenance intentionally have no
	// listener; the CLI reports those intervals as temporarily unavailable.
	var localUpdateServer *localupdate.Server
	if embeddedUpdateService != nil {
		localUpdateServer, err = localupdate.Listen(updatesRootForIdentity, embeddedUpdateService, localupdate.Options{Logger: runtimeLogger})
		if err != nil {
			fatalStartup(runtimeLogger, "local update control listener failed", "local_update_listener_failed")
		}
		defer func() {
			if closeErr := localUpdateServer.Close(); closeErr != nil {
				runtimeLogger.Warn(context.Background(), observabilityport.RuntimeEvent{Message: "local update socket cleanup failed", Component: "update", Operation: "local_close", Outcome: "warning", ReasonCode: "update_local_close_failed", ErrorClass: "update_local"})
			}
		}()
	}

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	// A durable pending marker transfers the update to PID1. Drain producers,
	// workers and HTTP handlers only after that marker has been published.
	if embeddedUpdateService != nil {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-embeddedUpdateService.ExitRequested():
				runtimeLogger.Info(context.Background(), observabilityport.RuntimeEvent{Message: "embedded update marker published; draining and exiting for bootstrap", Component: "runtime", Operation: "embedded_update_exit", Outcome: "success", ReasonCode: "embedded_update_handoff"})
				if localUpdateServer != nil {
					_ = localUpdateServer.Close()
				}
				if err := backgroundRuntime.Stop(); err != nil {
					fatalStartup(runtimeLogger, "update drain failed; handlers are not stopped", "embedded_update_drain_failed")
					return
				}
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdownCtx)
				_ = db.Close()
				os.Exit(0)
			}
		}()
	}
	// A verified restore hands off here: stop accepting writes, drain the
	// background runtime and HTTP, close the serving pool, switch databases and
	// exit so the restart (container restart policy) serves the restored data
	// with fresh caches and sessions. If the app cannot be stopped cleanly the
	// restored copy is dropped and the current data stays.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-backupService.HandoffRequested():
		}
		runtimeLogger.Warn(context.Background(), observabilityport.RuntimeEvent{Message: "backup restore ready; draining and switching databases", Component: "runtime", Operation: "backup_restore_exit", Outcome: "warning", ReasonCode: "backup_restore_handoff"})
		if localUpdateServer != nil {
			_ = localUpdateServer.Close()
		}
		maintenanceGate.EnterMaintenance()
		switchCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		drainErr := backgroundRuntime.Stop()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = server.Shutdown(shutdownCtx)
		shutdownCancel()
		_ = db.Close()
		if drainErr != nil {
			_ = backupService.Abandon(switchCtx)
			fatalStartup(runtimeLogger, "restore drain failed; the current data is kept", "backup_restore_drain_failed")
			return
		}
		if err := backupService.Activate(switchCtx); err != nil {
			fatalStartup(runtimeLogger, "restore switch could not be confirmed; restart will check the database state", "backup_restore_switch_failed")
			return
		}
		os.Exit(0)
	}()
	runtimeLogger.Info(ctx, observabilityport.RuntimeEvent{
		Message:    fmt.Sprintf("server listening on %s (install_id=%s, environment=%s)", cfg.ListenAddress, installID, cfg.Environment),
		Component:  "runtime",
		Operation:  "listen",
		Outcome:    "info",
		ReasonCode: "server_listening",
	})
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		if localUpdateServer != nil {
			_ = localUpdateServer.Close()
		}
		_ = db.Close()
		fatalStartup(runtimeLogger, "http server failed", "http_server_failed")
	}
	// The restore handoff shut the server down and owns the exit: returning
	// here would end the process in the middle of the database switch.
	select {
	case <-backupService.HandoffRequested():
		select {}
	default:
	}
	return 0
}

// qbSyncDispatchEvent describes one dispatch pass. The pass only reads the
// database and creates or merges WorkItems; it never contacts qBittorrent, so
// an empty scan is an idle pass rather than a failed connection.
func qbSyncDispatchEvent(result downloadapplication.QBSyncDispatchResult) observabilityport.RuntimeEvent {
	if result.Scanned == 0 {
		return observabilityport.RuntimeEvent{
			Message:    "qB sync dispatch found no tasks; no qBittorrent check was scheduled and no connection was made",
			Component:  "qb_sync",
			Operation:  "dispatch",
			Outcome:    "success",
			ReasonCode: "qb_sync_idle",
		}
	}
	return observabilityport.RuntimeEvent{
		Message:    fmt.Sprintf("qB sync dispatch scanned download tasks (scanned=%d queued=%d merged=%d skipped=%d); qBittorrent was not contacted", result.Scanned, result.Queued, result.Merged, result.Skipped),
		Component:  "qb_sync",
		Operation:  "dispatch",
		Outcome:    "success",
		ReasonCode: "qb_sync_completed",
	}
}

// qbSyncDispatchFailureEvent describes a pass that did not finish. It is not a
// downloader result: no task may have been scanned and qBittorrent was not
// contacted, so the body never claims a check or a task failure.
func qbSyncDispatchFailureEvent() observabilityport.RuntimeEvent {
	return observabilityport.RuntimeEvent{
		Message:    "qB sync dispatch did not complete this round; no qBittorrent check was made",
		Component:  "qb_sync",
		Operation:  "dispatch",
		Outcome:    "failure",
		ReasonCode: "qb_sync_failed",
		ErrorClass: "qb_sync",
	}
}

// qbSyncDispatchService is the one bounded dispatch pass the loop needs.
type qbSyncDispatchService interface {
	DispatchQBSync(context.Context) (downloadapplication.QBSyncDispatchResult, error)
}

func runQBSyncDispatcher(ctx context.Context, dispatcher qbSyncDispatchService, logger observabilityport.RuntimeLogger) {
	dispatch := func() {
		dispatchQBSyncOnce(ctx, dispatcher, logger)
	}
	dispatch()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatch()
		}
	}
}

func dispatchQBSyncOnce(ctx context.Context, dispatcher qbSyncDispatchService, logger observabilityport.RuntimeLogger) {
	result, err := dispatcher.DispatchQBSync(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn(ctx, qbSyncDispatchFailureEvent())
		}
		return
	}
	logger.Info(ctx, qbSyncDispatchEvent(result))
}

func runAutomationDispatcher(ctx context.Context, dispatcher *automationapplication.Dispatcher, logger observabilityport.RuntimeLogger) {
	dispatch := func() {
		if err := dispatcher.ReapStaleRuns(ctx, 10*time.Minute); err != nil && ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "automation stale-run reaper failed",
				Component:  "automation",
				Operation:  "reap",
				Outcome:    "failure",
				ReasonCode: "reap_failed",
				ErrorClass: "automation",
			})
		}
		if _, err := dispatcher.DispatchDue(ctx); err != nil && ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "automation scheduled dispatch failed",
				Component:  "automation",
				Operation:  "dispatch",
				Outcome:    "failure",
				ReasonCode: "dispatch_failed",
				ErrorClass: "automation",
			})
		}
		if _, err := dispatcher.DispatchManual(ctx); err != nil && ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "automation manual dispatch failed",
				Component:  "automation",
				Operation:  "dispatch_manual",
				Outcome:    "failure",
				ReasonCode: "manual_dispatch_failed",
				ErrorClass: "automation",
			})
		}
	}

	dispatch()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatch()
		}
	}
}

// runStrandedFileWorkRevival re-opens, once per background start, the file
// work an earlier release failed at its attempt budget with a writer-less
// retry, leaving the operation pending and its acquisition running; the
// recovery lane then converges it (a closed proof completes). The pass logs
// each operation's revival or refusal itself.
func runStrandedFileWorkRevival(ctx context.Context, revive func(context.Context) (fileoperationapplication.StrandedWorkRevival, error), logger observabilityport.RuntimeLogger) {
	if _, err := revive(ctx); err != nil && ctx.Err() == nil {
		logger.Error(ctx, observabilityport.RuntimeEvent{
			Message:    "stranded file work revival pass failed",
			Component:  "fileoperation",
			Operation:  "file_work_revival",
			Outcome:    "failure",
			ReasonCode: "file_work_revival_pass_failed",
			ErrorClass: "fileoperation",
		})
	}
}

// configWaitSweepInterval is the backstop cadence of the configuration wake:
// an import waiting for configuration that no write woke checks again within
// it.
const configWaitSweepInterval = 15 * time.Minute

func configWaitSweepEvent(woken int) observabilityport.RuntimeEvent {
	return observabilityport.RuntimeEvent{
		Message:    fmt.Sprintf("configuration wait sweep woke %d import(s) waiting for configuration; the sweep performed no import", woken),
		Component:  "fileoperation",
		Operation:  "config_wait_sweep",
		Outcome:    "success",
		ReasonCode: "config_wait_sweep_woken",
	}
}

// runConfigWaitSweep makes imports waiting for configuration due again on a
// fixed cadence, for configuration a write path changed without a wake. A
// pass that finds nothing new writes nothing; a woken plan that still waits
// logs nothing either.
func runConfigWaitSweep(ctx context.Context, db *sql.DB, logger observabilityport.RuntimeLogger) {
	ticker := time.NewTicker(configWaitSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			woken, err := animeapplication.SweepConfigWaitingPlans(ctx, db)
			if err != nil && ctx.Err() == nil {
				logger.Warn(ctx, observabilityport.RuntimeEvent{
					Message:    "configuration wait sweep failed",
					Component:  "fileoperation",
					Operation:  "config_wait_sweep",
					Outcome:    "failure",
					ReasonCode: "config_wait_sweep_failed",
					ErrorClass: "fileoperation",
				})
				continue
			}
			if woken > 0 {
				logger.Info(ctx, configWaitSweepEvent(woken))
			}
		}
	}
}

func runRecordRetentionMaintenance(ctx context.Context, janitor *retentionapplication.Service, logger observabilityport.RuntimeLogger) {
	run := func() {
		result, err := janitor.RunPass(ctx)
		if err != nil && ctx.Err() == nil && len(result.Results) == 0 {
			logger.Error(ctx, observabilityport.RuntimeEvent{
				Message:    "record retention pass failed",
				Component:  "retention",
				Operation:  "sweep",
				Outcome:    "failure",
				ReasonCode: "retention_pass_failed",
				ErrorClass: "retention",
			})
		}
	}
	run()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// runMetadataRefreshSweep refreshes the bound works of active subscriptions on
// a fixed cadence. The Global toggle is re-read on every pass, so disabling it
// takes effect without a restart. The sweep never runs at boot: a restart must
// not re-pull every bound work.
func runMetadataRefreshSweep(ctx context.Context, sweep *ownershipapplication.MetadataRefreshSweep, logger observabilityport.RuntimeLogger) {
	run := func() {
		result, err := sweep.RunPass(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "metadata refresh sweep failed",
				Component:  "metadata_refresh",
				Operation:  "sweep",
				Outcome:    "failure",
				ReasonCode: "metadata_refresh_pass_failed",
				ErrorClass: "metadata",
			})
			return
		}
		if result.Enabled {
			logger.Info(ctx, observabilityport.RuntimeEvent{
				Message:    fmt.Sprintf("metadata refresh sweep completed (refreshed=%d failed=%d)", result.Refreshed, result.Failed),
				Component:  "metadata_refresh",
				Operation:  "sweep",
				Outcome:    "success",
				ReasonCode: "metadata_refresh_completed",
			})
		}
	}
	ticker := time.NewTicker(ownershipapplication.MetadataRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// runAcquisitionActivity drives the independent acquisition Activity consumer
// on a short tick. A failed entry stays unwritten and is retried on the next
// pass; the consumer itself keeps the diagnostic episode. Written entries
// refresh open logs and records.
func runAcquisitionActivity(ctx context.Context, consumer *acquisitionactivity.Consumer, hub *events.Hub, logger observabilityport.RuntimeLogger) {
	run := func() {
		result, err := consumer.ConsumeOnce(ctx)
		if result.Written > 0 {
			hub.Publish(events.Event{Target: events.TargetRecords})
		}
		if err != nil && ctx.Err() == nil {
			logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "acquisition activity pass incomplete",
				Component:  "activity",
				Operation:  "consume",
				Outcome:    "failure",
				ReasonCode: "activity_consume_failed",
				ErrorClass: "activity",
			})
		}
	}
	run()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// downloadCompletionFailureEvent reports a failed pass without claiming that
// any completion action ran or did not run. A pass that already recorded rows
// says so; the action results are carried by those rows and their own work.
func downloadCompletionFailureEvent(created int) observabilityport.RuntimeEvent {
	message := "download completion pass failed before recording a completion row; it retries on the next pass"
	if created > 0 {
		message = fmt.Sprintf("download completion pass recorded %d completion row(s) before failing; the pass does not confirm an action result", created)
	}
	return observabilityport.RuntimeEvent{
		Message:    message,
		Component:  "acquisition",
		Operation:  "download_completion",
		Outcome:    "failure",
		ReasonCode: "download_completion_pass_failed",
		ErrorClass: "acquisition",
	}
}

func downloadCompletionSuccessEvent(created int) observabilityport.RuntimeEvent {
	return observabilityport.RuntimeEvent{
		Message:    fmt.Sprintf("download completion pass recorded %d completion row(s)", created),
		Component:  "acquisition",
		Operation:  "download_completion",
		Outcome:    "success",
		ReasonCode: "download_completion_completed",
	}
}

// runDownloadCompletion writes the completion row (and, for an action with work
// to do, its WorkItem) of every succeeded download acquisition on a short tick.
// A failed pass is logged and retried on the next tick; the row is the marker,
// so a pass never repeats an acquisition it already handled.
func runDownloadCompletion(ctx context.Context, service *downloadcompletion.Service, logger observabilityport.RuntimeLogger) {
	run := func() {
		result, err := service.RunPass(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error(ctx, downloadCompletionFailureEvent(result.Created))
			}
			return
		}
		if result.Created > 0 {
			logger.Info(ctx, downloadCompletionSuccessEvent(result.Created))
		}
	}
	run()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// runAcquisitionSettlement settles download roots whose settle work is due,
// disposes running roots of retired owners and compacts detail-expired roots
// on a short tick. A failed pass is logged and retried on the next tick. A
// pass that changed a root refreshes open records and anime pages: a failed
// root has no completion row, so nothing else announces it.
func runAcquisitionSettlement(ctx context.Context, service *acquisitionsettlement.SettlementService, hub *events.Hub, oneShotDrainDue chan<- struct{}, logger observabilityport.RuntimeLogger) {
	run := func() {
		result, err := service.RunPass(ctx)
		if result.Settled > 0 || result.Disposed > 0 || result.Compacted > 0 {
			hub.Publish(events.Event{Target: events.TargetRecords})
			hub.Publish(events.Event{Target: events.TargetRSS})
		}
		if result.Settled > 0 || result.Disposed > 0 {
			select {
			case oneShotDrainDue <- struct{}{}:
			default: // a drain is already due
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				logger.Error(ctx, observabilityport.RuntimeEvent{
					Message:    "acquisition settlement pass failed",
					Component:  "acquisition",
					Operation:  "acquisition_settlement",
					Outcome:    "failure",
					ReasonCode: "acquisition_settlement_failed",
					ErrorClass: "acquisition",
				})
			}
			return
		}
		if result.Settled > 0 || result.Disposed > 0 || result.Compacted > 0 {
			logger.Info(ctx, observabilityport.RuntimeEvent{
				Message: fmt.Sprintf("acquisition settlement pass completed (settled=%d deferred=%d disposed=%d compacted=%d)",
					result.Settled, result.Deferred, result.Disposed, result.Compacted),
				Component:  "acquisition",
				Operation:  "acquisition_settlement",
				Outcome:    "success",
				ReasonCode: "acquisition_settlement_completed",
			})
		}
	}
	run()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// runOneShotReclaimMaintenance drives the finite one-shot marker lifecycle
// on the same cadence as record retention, and right after the settlement
// pass settled a root (drainDue). Failures are logged and retried on the next
// tick; they never block the record-retention purge. A pass that moved a
// marker refreshes open records, whose recovery actions read the marker.
func runOneShotReclaimMaintenance(ctx context.Context, service *workitems.OneShotReclaimService, drainDue <-chan struct{}, hub *events.Hub, logger observabilityport.RuntimeLogger) {
	run := func() {
		result, err := service.RunPass(ctx)
		if result.Changed() {
			hub.Publish(events.Event{Target: events.TargetRecords})
			logger.Info(ctx, observabilityport.RuntimeEvent{
				Message: fmt.Sprintf("one-shot reclaim pass committed changes (drained=%d reclaimed=%d rechecked=%d extended=%d resumed=%d blocked=%d purged=%d)",
					result.Drained, result.Reclaimed, result.Rechecked, result.Extended, result.Revived, result.Blocked, result.Purged),
				Component:  "retention",
				Operation:  "oneshot_reclaim",
				Outcome:    "success",
				ReasonCode: "oneshot_reclaim_completed",
			})
		}
		if err != nil && ctx.Err() == nil {
			logger.Error(ctx, observabilityport.RuntimeEvent{
				Message:    "one-shot reclaim pass failed",
				Component:  "retention",
				Operation:  "oneshot_reclaim",
				Outcome:    "failure",
				ReasonCode: "oneshot_reclaim_failed",
				ErrorClass: "retention",
			})
		}
	}
	run()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		case <-drainDue:
			run()
		}
	}
}

// secretAvailable proves the secret encryption key path for readiness.
type secretAvailable struct {
	store *configurationapplication.DatabaseSecretStore
}

func (s secretAvailable) Available(ctx context.Context) error {
	if s.store == nil {
		return errors.New("secret encryption key not configured")
	}
	return nil
}

// qbHealthProbe is a degraded-only readiness check for qBittorrent.
type qbHealthProbe struct {
	connections *configurationapplication.Service
}

func (q qbHealthProbe) Healthy(ctx context.Context) (bool, string) {
	conns, err := q.connections.ListConnections(ctx)
	if err != nil {
		return false, "unreadable"
	}
	for _, c := range conns {
		if string(c.Kind) == "qbittorrent" && c.State != configurationdomain.ConnectionDisabled {
			return true, "ok"
		}
	}
	return false, "unavailable"
}

// deploymentIdentityProvider adapts the bootstrap-injected environment
// identity plus one live updates-volume pointer read into the readiness
// evidence port (design §6). Values are injected once by bootstrap mode before
// exec; the pointer is re-read on every report so a pointer drift after the
// process started is observable as a consistency breach.
type deploymentIdentityProvider struct {
	updatesRoot   string
	requireSource bool
}

func (p deploymentIdentityProvider) Identity() deploymentidentity.Identity {
	return deploymentidentity.FromEnv(os.Getenv)
}

func (p deploymentIdentityProvider) CurrentPointer() deploymentidentity.PointerState {
	return deploymentidentity.ReadCurrentPointer(p.updatesRoot)
}

func (p deploymentIdentityProvider) RequireSource() bool { return p.requireSource }

func (p deploymentIdentityProvider) Validate() error {
	identity := deploymentidentity.FromEnv(os.Getenv)
	// ProcessBuildID is runtime evidence supplied by BuildInfo, not by the
	// bootstrap environment. Validate the same combined identity surface that
	// readiness reports, so an overlay process with only expected-build env
	// provenance is not rejected for lacking an env copy of its actual build.
	identity.ProcessBuildID = buildinfo.Current().BuildID
	pointer := p.CurrentPointer()
	if err := identity.ValidateForRuntime(&pointer, p.requireSource); err != nil {
		return err
	}
	if identity.SelectedSource != deploymentidentity.SourceOverlay {
		return nil
	}
	return validateRunningOverlay(p.updatesRoot, identity)
}

func validateRunningOverlay(updatesRoot string, identity deploymentidentity.Identity) error {
	if strings.TrimSpace(identity.SelectedReleaseID) == "" {
		return errors.New("overlay identity has no selected release")
	}
	// Reuse the same tree verifier as bootstrap. This rereads the descriptor,
	// retained payload and extracted tree on every readiness call, so mutation
	// after process start cannot leave the instance ready.
	releaseDir := filepath.Join(updatesRoot, "releases", identity.SelectedReleaseID)
	metadata, manifest, err := updateapplication.VerifyReleaseTreeIdentity(releaseDir)
	if err != nil {
		return err
	}
	if manifest.EmbeddedUpdate == nil {
		return errors.New("signed overlay release has no embedded update identity")
	}
	provenance, err := deploymentprovenance.Read(updatesRoot, identity.SelectedReleaseID)
	if err != nil {
		return fmt.Errorf("read overlay deployment provenance: %w", err)
	}
	if err := provenance.Validate(identity.SelectedReleaseID, manifest.EmbeddedUpdate.PayloadDigest, metadata.BuildID); err != nil {
		return err
	}
	if provenance.PayloadDigest != identity.PayloadDigest || provenance.ProducingOperationID != identity.UpdateOperationID || provenance.ExpectedProcessBuildID != identity.ExpectedProcessBuildID {
		return errors.New("injected deployment identity differs from durable overlay provenance")
	}
	if metadata.BuildID != identity.ExpectedProcessBuildID {
		return fmt.Errorf("signed release build id %q differs from expected process build id %q", metadata.BuildID, identity.ExpectedProcessBuildID)
	}
	if metadata.BuildID != buildinfo.Current().BuildID {
		return fmt.Errorf("running build id %q differs from signed release build id %q", buildinfo.Current().BuildID, metadata.BuildID)
	}
	return nil
}

// workerLanes adapts the runtime worker to the readiness WorkerProvider.
type workerLanes struct{ kinds []string }

func (w workerLanes) AllowedKinds() []string { return w.kinds }

// serverWorkItemKinds is the static kind inventory of the composition root.
// It must stay in sync with DefaultPolicies() (every policy kind wired) and
// the worker allowlist (every wired kind claimable), otherwise startup
// fails at the lazy worker build — which no integration test exercised until
// the rc.5 rollout failure. TestWorkItemKindsCoverPoliciesAndAllowlist pins
// the three sets together; main() also asserts the registrations count.
func serverWorkItemKinds() []string {
	return []string{
		workitems.KindRSSRefresh,
		workitems.KindReleaseEvaluate,
		workitems.KindQBSubmit,
		workitems.KindQBSync,
		workitems.KindArtifactInspect,
		workitems.KindFileExecute,
		workitems.KindPostProcessPlan,
		workitems.KindPluginDeliver,
		workitems.KindReconciliationScan,
		workitems.KindReconciliationCalibrate,
		workitems.KindReconciliationConsumeAlignment,
		workitems.KindReconciliationConsumeReplacement,
		workitems.KindAlignmentExecute,
		workitems.KindReplacementEvaluate,
		workitems.KindSubscriptionCompetition,
		workitems.KindReplacementDelete,
		workitems.KindFileManagerExecute,
		workitems.KindCompletionExecute,
	}
}

// metadataRefresherOwnershipAdapter adapts the ownership application's
// RefreshAnimeMetadataApplication onto the file-maintenance Service's
// MetadataWorkRefresher port (the 对齐（刷新元数据）variant re-pull); the
// ownership adapter itself stays out of FileMaintenance's package graph.
type metadataRefresherOwnershipAdapter struct{ ownership *ownershipapplication.Service }

func (a metadataRefresherOwnershipAdapter) RefreshAnimeMetadataApplication(ctx context.Context, managedAnimeID string) (struct{}, error) {
	if _, err := a.ownership.RefreshAnimeMetadataApplication(ctx, managedAnimeID); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, nil
}
