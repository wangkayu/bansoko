package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	accessapplication "bansoko/internal/accesscontrol/application"
	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	animeapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/apperrors"
	artifactadapter "bansoko/internal/artifact/adapter"
	artifactapplication "bansoko/internal/artifact/application"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/filemaintenance"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	"bansoko/internal/library/adapter/filesystem"
	matcher "bansoko/internal/metadata/matcher"
	observabilityapplication "bansoko/internal/observability/application"
	parserpolicy "bansoko/internal/parserpolicy"
	"bansoko/internal/postprocessing/naming"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/rss/application"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// ---------------------------------------------------------------------------
// Revision fixture: one HTTP server, one current RSS item, per-revision
// torrents with distinct info names so qB saves into distinct directories.
// ---------------------------------------------------------------------------

type rssItem struct {
	GUID  string
	Title string
}

type revisionFixture struct {
	mu        sync.Mutex
	current   string
	items     map[string]rssItem
	files     map[string][]torrentFile
	port      int
	torrents  map[string][]byte
	client    *http.Client
	closeFunc func()
}

func (f *revisionFixture) SetCurrent(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = name
}

func (f *revisionFixture) FeedURL() string {
	return fmt.Sprintf("http://%s:%d/feed.xml", realChainFixtureHost(), f.port)
}

func (f *revisionFixture) TorrentBytes(name string) []byte {
	return append([]byte(nil), f.torrents[name]...)
}

func (f *revisionFixture) Close() {
	if f.closeFunc != nil {
		f.closeFunc()
	}
}

func startRevisionFixture(t *testing.T, items map[string]rssItem, files map[string][]torrentFile) *revisionFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	fixture := &revisionFixture{
		current:  "A",
		items:    items,
		files:    files,
		port:     port,
		torrents: map[string][]byte{},
	}
	for key := range items {
		rootURL := fmt.Sprintf("http://%s:%d", realChainFixtureHost(), port)
		// The item key is also the torrent info name (qB saves into
		// /downloads/<key>/), keeping the web-seed root conflict-free.
		fixture.torrents[key] = multiFileTorrent(key, files[key], rootURL)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		current := fixture.current
		fixture.mu.Unlock()
		switch r.URL.Path {
		case "/feed.xml":
			item := items[current]
			link := fmt.Sprintf("http://%s:%d/%s.torrent", realChainFixtureHost(), port, item.GUID)
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Repl Fixture</title><item><guid>%s</guid><title>%s</title><link>%s</link><pubDate>Sat, 11 Jul 2026 03:00:00 +0000</pubDate></item></channel></rss>`, item.GUID, item.Title, link)
		case "/" + items[current].GUID + ".torrent":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(fixture.torrents[current])
		default:
			// libtorrent serves a single-file "multi-file" torrent (one files
			// entry) through the plain <root>/<file> URL, while true multi-file
			// torrents use <root>/<info name>/<file>; accept both.
			for name, fileSet := range files {
				for _, file := range fileSet {
					if r.URL.Path == "/"+name+"/"+url.PathEscape(file.Name) || r.URL.Path == "/"+name+"/"+file.Name ||
						r.URL.Path == "/"+url.PathEscape(file.Name) || r.URL.Path == "/"+file.Name {
						http.ServeContent(w, r, file.Name, time.Unix(0, 0), bytes.NewReader(file.Content))
						return
					}
				}
			}
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, portValue, splitErr := net.SplitHostPort(address)
		if splitErr == nil && strings.EqualFold(host, realChainFixtureHost()) && portValue == strconv.Itoa(port) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", portValue))
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, address)
	}
	fixture.client = &http.Client{Timeout: 20 * time.Second, Transport: transport}
	fixture.closeFunc = func() { _ = server.Shutdown(context.Background()) }
	return fixture
}

// ---------------------------------------------------------------------------
// Real chain services: the same composition the production root wires, but
// with the fixture HTTP fetcher and the real qB from the harness env.
// ---------------------------------------------------------------------------

type realChainServices struct {
	db               *sql.DB
	httpHandler      http.Handler
	draft            *draftapplication.Service
	ownership        *ownershipapplication.Service
	oneShot          *oneshotapplication.Service
	download         *downloadapplication.Service
	file             *fileoperationapplication.Service
	runtime          *runtimeworker.Worker
	replacement      *filemaintenance.ReplacementDecisionService
	trigger          *filemaintenance.ReplacementTriggerService
	cleanup          *filemaintenance.CleanupService
	store            *workitems.Store
	placements       *filemaintenance.PlacementService
	recon            *reconciliationapplication.Service
	calibration      *filemaintenance.CalibrationService
	alignment        *filemaintenance.AlignmentService
	policies         *filemaintenance.PolicyService
	namingRenderer   *filemaintenance.NamingTargetRenderer
	hostDownloadRoot string
	targetRoot       string
}

func newRealChainServices(t *testing.T, client *http.Client) *realChainServices {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	username := strings.TrimSpace(os.Getenv("BANSOKO_QB_USERNAME"))
	password := strings.TrimSpace(os.Getenv("BANSOKO_QB_PASSWORD"))
	if endpoint == "" || username == "" || password == "" {
		t.Skip("BANSOKO_QB_ENDPOINT/USERNAME/PASSWORD are required for the real-effect chain")
	}
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()

	hostDownloadRoot := filepath.Join("..", "..", ".local-artifacts", "repl-qb", "downloads")
	if harnessRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT")); harnessRoot != "" {
		hostDownloadRoot = harnessRoot
	}
	if err := os.MkdirAll(hostDownloadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()

	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte("test-secret-encryption-key-0123456789abcdef"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	configService := configurationapplication.NewHTTPService(configurationapplication.NewService(db, secrets))
	connectionID := "repl-qb-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	downloadRoot := hostDownloadRoot
	harnessRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT"))
	if harnessRoot != "" {
		hostDownloadRoot = harnessRoot
		downloadRoot = "/downloads"
	}
	var connectionInput generated.ConnectionInput
	var connectionSecret generated.ConnectionSecret
	if err := connectionSecret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: password}); err != nil {
		t.Fatal(err)
	}
	if err := connectionInput.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		BaseUrl: endpoint, DownloadRoot: &downloadRoot, DownloaderRoot: &hostDownloadRoot,
		Enabled: true, Kind: generated.QBittorrentConnectionInputKindQbittorrent,
		Name: connectionID, Secret: connectionSecret, Username: username,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := putGeneratedConnection(t, configService, ctx, connectionID, 0, connectionInput); err != nil {
		t.Fatal(err)
	}
	var connVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM connections WHERE id = $1`, connectionID).Scan(&connVersion); err != nil {
		t.Fatal(err)
	}
	connection, err := putGeneratedConnection(t, configService, ctx, connectionID, connVersion, connectionInput)
	if err != nil {
		t.Fatal(err)
	}
	if configured, err := connection.AsQBittorrentConnection(); err != nil || !configured.Enabled {
		t.Fatalf("typed qB connection was not configured: %+v err=%v", connection, err)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	draftService, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	draftService.SetActivityWriter(observability)
	ownershipService, err := ownershipapplication.NewService(db, ownershipapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	ownershipService.SetActivityWriter(observability)
	ownershipService.SetOneShotMarkerRepository(animepersistence.NewRepository(db))
	ownershipService.SetMetadataMatcher(matcher.NewMatcher(matcher.NewBangumiDataIndex(nil), nil, nil, nil))
	ownershipService.SetObservabilityRecorder(observability)
	ownershipService.SetOffsetMigrationAdmission(filemaintenance.NewOffsetMigrationAdmissionService())
	// Mirror the production composition root: candidate activation must write
	// the revision-frozen instance parser policy, or the automatic refresh and
	// evaluate path fails closed with parser_policy_required.
	parserPolicies := parserpolicy.NewService(db)
	draftService.SetFrozenParserPolicy(parserPolicies)
	rssFetcher := acquisitionapplication.HTTPRSSFetcher{
		Client:            client,
		AllowPrivateHosts: []string{realChainFixtureHost()},
		TorrentReader: acquisitionapplication.HTTPTorrentMetadataReader{
			Client: client, AllowPrivateHosts: []string{realChainFixtureHost()},
		},
	}
	rssService, err := application.NewService(db, store, fixtureSourceFetcher{fetcher: rssFetcher})
	if err != nil {
		t.Fatal(err)
	}
	rssService.SetActivityWriter(observability)
	rssService.SetCandidateFactsRepository(facts.NewPGRepository(db))
	rssService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	qbClient := qbittorrent.ConfiguredClient{Source: configService, Timeout: 20 * time.Second}
	downloadService, err := downloadapplication.NewService(db, store, qbClient, configService)
	if err != nil {
		t.Fatal(err)
	}
	artifactService, err := artifactapplication.NewService(db, artifactadapter.OSArtifactInspector{})
	if err != nil {
		t.Fatal(err)
	}
	fileService, err := fileoperationapplication.NewService(db, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	if err != nil {
		t.Fatal(err)
	}
	fileService.SetActivityWriter(observability)
	// The planning stage creates the typed Plan; no downstream legacy Prepare sink.
	// The production composition root wires the same P4 per-file facts owner into
	// the artifact verifier and the file-operation naming source, so the harness
	// must too: without it the frozen snapshot never gains file rows and the
	// placement stage can never resolve frozen coverage.
	artifactService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	fileService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))

	placementService := filemaintenance.NewPlacementService(db, filemaintenance.NewFileOperationEvidenceReader(db), nil)
	placementService.SetActivityWriter(observability)
	replacementTrigger := filemaintenance.NewReplacementTriggerService(db, store)
	ownershipService.SetReplacementTrigger(replacementTrigger)
	replacementDecisions := filemaintenance.NewReplacementDecisionService(db)
	replacementDecisions.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	// Mirror the production composition root: persist frozen targets, then wire
	// pre-download probing and the frozen replacement intent before B is evaluated.
	rssService.SetTargetStore(acquisitiontarget.NewStore(targetpersistence.NewRepository(db)))
	rssService.SetTargetAcquisition(acquisitiontarget.FilesystemProbe{})
	configureRealChainNaming(t, ctx, db, targetRoot)
	rssService.SetDownloaderAdmission(downloadService)
	rssService.SetReplacementAdmission(replacementDecisions)
	// One-shot uses its own service and the same frozen target boundary as RSS.
	provisioner := animeapplication.NewProvisioner(parserPolicies)
	oneShotService, err := oneshotapplication.NewService(db, oneshotapplication.Dependencies{
		WorkItems: store, OneShotMarkers: animepersistence.NewRepository(db), Provisioner: &provisioner,
		TorrentFetcher: animeapplication.NewTorrentPayloadFetcher([]string{realChainFixtureHost()}, func(ctx context.Context, host string) ([]net.IP, error) {
			if strings.EqualFold(host, realChainFixtureHost()) {
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			}
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}),
		CandidateRevisions: draftService,
		// TargetStore + TargetProbe are the production pair that makes one-shot
		// admission freeze its target set; the probe alone decides
		// targetAcquisitionConfigured, so the fixture wires both exactly as
		// cmd/bansoko-server does.
		TargetStore: acquisitiontarget.NewStore(targetpersistence.NewRepository(db)),
		TargetProbe: acquisitiontarget.FilesystemProbe{},
	})
	if err != nil {
		t.Fatal(err)
	}
	accessService := accessapplication.NewServiceWithSessionSecret(db, []byte("repl-session-secret-32-bytes"))
	httpServer, err := httpapi.NewServer(httpapi.Dependencies{
		DB: db, Access: accessService, Draft: draftService, Ownership: ownershipService, ReleaseRetry: rssService,
		OneShot: oneShotService, Environment: "development", SkipAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupService := filemaintenance.NewCleanupService(db)
	cleanupService.SetFileExecutor(filesystem.NewOSCleanupExecutor(targetRoot))
	cleanupService.SetWorkItems(store)
	replacementDecisions.SetCleanupHandoff(cleanupService)
	replacementDecisions.SetActivityWriter(observability)
	cleanupService.SetActivityWriter(observability)
	fileService.SetPlacementSink(newPlacementSinkForTestWithTrigger(placementService, db, replacementTrigger))

	// Reconciliation/alignment wiring (the natural-chain scan calibrates the
	// placement-owned library and alignment converges drifted files).
	reconService, err := reconciliationapplication.NewService(db, store, filesystem.OSFileScanner{})
	if err != nil {
		t.Fatal(err)
	}
	snapshotReader := filemaintenance.NewReconciliationSnapshotReader(db)
	namingRenderer := filemaintenance.NewNamingTargetRenderer(db, ownershipService)
	reconPlanner := filemaintenance.NewPlanner(snapshotReader, namingRenderer)
	policyService := filemaintenance.NewPolicyService(db)
	calibrationService := filemaintenance.NewCalibrationService(db, placementService, policyService, reconPlanner, store)
	alignmentOps := filemaintenance.NewAlignmentService(db)
	alignmentOps.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	calibrationService.SetAlignmentConsumer(alignmentOps)
	calibrationService.SetCompanionCollisionSource(ownershipService)
	reconService.SetEventHub(nil)

	runtime := runtimeworker.NewWorker(store, "repl-worker", time.Minute)
	for _, registration := range []struct {
		kind    string
		handler runtimeworker.Handler
	}{
		{workitems.KindRSSRefresh, rssService.RSSRefreshHandler()},
		{workitems.KindReleaseEvaluate, rssService.ReleaseEvaluateHandler()},
		{workitems.KindQBSubmit, downloadService.SubmitHandler()},
		{workitems.KindQBSync, downloadService.SyncHandler()},
		{workitems.KindArtifactInspect, artifactService.InspectHandler()},
		{workitems.KindFileExecute, fileService},
		{workitems.KindPostProcessPlan, fileService.PostProcessingPlanHandler()},
		{workitems.KindReplacementEvaluate, replacementDecisions.EvaluateHandler()},
		{workitems.KindCleanupDelete, cleanupService.DeleteHandler()},
		{workitems.KindReconciliationScan, reconService.ScanHandler()},
		{workitems.KindReconciliationCalibrate, calibrationService.CalibrateHandler()},
		{workitems.KindReconciliationConsumeAlignment, calibrationService.ConsumeAlignmentHandler()},
		{workitems.KindAlignmentExecute, alignmentOps.ExecuteHandler()},
	} {
		if err := runtime.Register(registration.kind, registration.handler); err != nil {
			t.Fatal(err)
		}
	}
	return &realChainServices{
		db: db, httpHandler: httpServer.Handler(), draft: draftService, ownership: ownershipService, oneShot: oneShotService,
		download: downloadService, file: fileService, runtime: runtime,
		replacement: replacementDecisions, trigger: replacementTrigger, cleanup: cleanupService,
		store: store, placements: placementService, recon: reconService,
		calibration: calibrationService, alignment: alignmentOps, policies: policyService,
		namingRenderer: namingRenderer, hostDownloadRoot: hostDownloadRoot, targetRoot: targetRoot,
	}
}

// placementSinkForTestWithTrigger admits verified video operations into
// placements and triggers the incremental replacement evaluation exactly like
// the production sink. Coverage comes from the same frozen per-file media fact
// identity the production sink resolves, never from the candidate-facts
// projection.
type placementSinkForTestWithTrigger struct {
	placements *filemaintenance.PlacementService
	evidence   filemaintenance.FileOperationEvidenceSource
	mediaFacts filemaintenance.MediaFileFactsReader
	trigger    *filemaintenance.ReplacementTriggerService
}

func newPlacementSinkForTestWithTrigger(placements *filemaintenance.PlacementService, db *sql.DB, trigger *filemaintenance.ReplacementTriggerService) *placementSinkForTestWithTrigger {
	return &placementSinkForTestWithTrigger{
		placements: placements,
		evidence:   filemaintenance.NewFileOperationEvidenceReader(db),
		mediaFacts: facts.NewStoreWithRepository(mediapersistence.NewRepository(db)),
		trigger:    trigger,
	}
}

func (a *placementSinkForTestWithTrigger) AdmitFromFileOperation(ctx context.Context, fileOperationID string) error {
	evidence, found, err := a.evidence.ReadVerifiedEvidence(ctx, fileOperationID)
	if err != nil {
		return err
	}
	if !found || !evidence.ArtifactVerified || evidence.TargetFingerprint == "" {
		return fmt.Errorf("file operation %s is not verified evidence", fileOperationID)
	}
	if evidence.FileKind == "subtitle" {
		return a.placements.AdmitSidecarFromFileOperation(ctx, fileOperationID)
	}
	if evidence.FileKind != "video" {
		return nil
	}
	if evidence.UnknownFile {
		_, err = a.placements.AdmitUnknownFromFileOperation(ctx, fileOperationID)
		return err
	}
	coverage, factsVersion, ok, coverageErr := filemaintenance.FrozenFileCoverage(ctx, a.mediaFacts, evidence.ReleaseID, evidence.FileRef)
	if coverageErr != nil {
		return coverageErr
	}
	if !ok {
		// Mirror the production sink: an unresolved identity is an unknown
		// file, released at admission.
		_, err = a.placements.AdmitUnknownFromFileOperation(ctx, fileOperationID)
		return err
	}
	placementID, err := a.placements.AdmitFromFileOperation(ctx, fileOperationID, coverage, factsVersion)
	if err != nil {
		return err
	}
	if a.trigger != nil && placementID != "" {
		return a.trigger.TriggerPlacementCommitted(ctx, evidence.ManagedAnimeID, evidence.ReleaseID)
	}
	return nil
}

// driveToPlacement runs the media chain until the given artifact files appear
// and the instance has at least wantOperations completed file operations.
func (s *realChainServices) driveToPlacement(t *testing.T, ctx context.Context, animeID string, infoName string, files []torrentFile, wantOperations int) {
	t.Helper()
	for _, file := range files {
		waitForArtifact(t, filepath.Join(s.hostDownloadRoot, infoName, file.Name), int64(len(file.Content)), 60*time.Second)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.download.DispatchQBSync(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runWorkerUntilIdle(ctx, s.runtime, s.db, 32); err != nil {
			t.Fatal(err)
		}
		var done int
		if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM file_operations
WHERE state = 'completed' AND artifact_id IN (
  SELECT a.id FROM artifacts a JOIN download_tasks t ON t.id = a.download_task_id
  JOIN releases r ON r.id = t.release_id JOIN rss_sources src ON src.id = r.rss_source_id
  WHERE src.subscription_id = (SELECT sub.id FROM subscriptions sub WHERE sub.managed_anime_id = $1))`, animeID).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done >= wantOperations {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("file operations did not reach %d for %s", wantOperations, animeID)
}

// waitForPlacementCount polls until the anime has exactly the given number of
// verified active placements.
func waitForPlacementCount(t *testing.T, ctx context.Context, db *sql.DB, animeID string, want int) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1 AND verification_state = 'verified' AND state = 'active'`, animeID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		time.Sleep(time.Second)
	}
	var count int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1`, animeID).Scan(&count)
	t.Fatalf("placements did not reach %d (all=%d)", want, count)
}

func waitForCleanupState(t *testing.T, ctx context.Context, db *sql.DB, animeID, state string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleanup_operations WHERE managed_anime_id = $1 AND state = $2`, animeID, state).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("cleanup operation never reached %q", state)
}

// TestReplacementRealChainActiveAndStopped proves the natural replacement
// chain: Revision A (720p) completes first, Revision B (1080p, same coverage)
// wins the single_copy pool before acquisition even though the naming template
// renders the same target, and a stopped subscription blocks the next
// replacement until resume.
func TestReplacementRealChainActiveAndStopped(t *testing.T) {
	// A and B have different resolution quality, but the wash naming override
	// omits resolution, so both render the identical target. The
	// pre-acquisition replacement admission must therefore freeze B as replace
	// from candidate facts rather than relying on a later size tie-breaker.
	items := map[string]rssItem{
		"A": {GUID: "repl-a-guid", Title: "[RealGroup] Repl Real Show - 01 [720p]"},
		"B": {GUID: "repl-b-guid", Title: "[RealGroup] Repl Real Show - 01 [1080p]"},
		"C": {GUID: "repl-c-guid", Title: "[RealGroup] Repl Real Show - 01 [1440p]"},
	}
	// Each revision carries a second file so the torrent is a true multi-file
	// torrent; libtorrent then serves web-seed files at <root>/<info name>/<file>
	// (the single-file layout uses a different URL shape and is flaky here).
	videoA := []torrentFile{
		{Name: "Repl Real Show - 01.mkv", Content: []byte("revision A 720p video " + time.Now().UTC().Format(time.RFC3339Nano))},
		{Name: "Repl Real Show - 01.nfo", Content: []byte("revision A nfo")},
	}
	videoB := []torrentFile{
		{Name: "Repl Real Show - 01.mkv", Content: []byte("revision B 1080p video larger payload for the size tie-breaker " + time.Now().UTC().Format(time.RFC3339Nano))},
		{Name: "Repl Real Show - 01.nfo", Content: []byte("revision B nfo")},
	}
	videoC := []torrentFile{
		{Name: "Repl Real Show - 01.mkv", Content: []byte("revision C 1440p video " + time.Now().UTC().Format(time.RFC3339Nano))},
		{Name: "Repl Real Show - 01.nfo", Content: []byte("revision C nfo")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{
		"A": videoA, "B": videoB, "C": videoC,
	})
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedRetentionSingleCopy(t, svc.db)

	draft := singleEpisodeDraft(fixture.FeedURL())
	draft.Title = "Repl Real Show"
	draft.RssSources[0].Title = "Repl Real Show"
	// Mikan group refs must be positive integers for the selection policy
	// decoder (the replacement evaluate path reads the discovery policy).
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	// The wash scenario overwrites the incumbent at the same rendered target:
	// Revision B lands on top of A, then the replacement decision supersedes A
	// and cleanup skips the already-replaced bytes by path ownership.
	var namingOverride generated.NamingRulesOverride
	if err := namingOverride.FromNamingRulesReplace(generated.NamingRulesReplace{
		Mode:  generated.NamingRulesReplaceModeReplace,
		Value: flatNamingRules(svc.targetRoot),
	}); err != nil {
		t.Fatal(err)
	}
	draft.Rules.Sections.Naming = &namingOverride
	var fileEffect generated.FileEffectRulesOverride
	if err := fileEffect.FromFileEffectRulesReplace(generated.FileEffectRulesReplace{
		Mode: generated.FileEffectRulesReplaceModeReplace,
		Value: generated.FileEffectRules{
			Operation:       generated.FileEffectRulesOperationMove,
			CollisionPolicy: generated.CollisionPolicyOverwrite,
		},
	}); err != nil {
		t.Fatal(err)
	}
	draft.Rules.Sections.FileEffect = &fileEffect
	// Only the video enters the effect chain: the .nfo is not a video or
	// subtitle, so it is never processed. An overwrite delivery admits exactly
	// one approved replacement incumbent; a second existing target
	// (Revision A's .nfo) would fail the whole typed plan.
	draft = withDraftBusiness(t, svc.db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" {
		t.Fatalf("candidate save failed: status=%d", saveStatus)
	}
	if candidate.CurrentRevision == nil {
		t.Fatalf("saved candidate carries no immutable revision: %+v", candidate)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "repl-subscribe-1", "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" || len(result.RssSources) != 1 {
		t.Fatalf("subscribe failed: status=%d", subscribeStatus)
	}
	animeID := result.ManagedAnime.Id
	sourceID := result.RssSources[0].Id

	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "repl-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "repl-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	svc.driveToPlacement(t, ctx, animeID, "A", videoA, 1)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	var placementA string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementA); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(placementA, "Repl Real Show/Repl Real Show - E01@RealGroup.mkv") {
		t.Fatalf("revision A placement path is wrong: %q", placementA)
	}

	// --- Revision B (better quality) replaces A through the real lanes. ---
	fixture.SetCurrent("B")
	triggerRefresh(t, ctx, svc, sourceID, animeID)
	// B's video is the replacement effect (the .nfo is filtered out).
	svc.driveToPlacement(t, ctx, animeID, "B", videoB, 2)
	var replacementIntent string
	if err := svc.db.QueryRowContext(ctx, `
SELECT fo.intent
FROM file_operations fo
JOIN artifacts a ON a.id = fo.artifact_id
JOIN artifact_files af ON af.id = fo.artifact_file_id
JOIN download_tasks dt ON dt.id = a.download_task_id
JOIN releases r ON r.id = dt.release_id
WHERE r.stable_item_key = 'repl-b-guid' AND af.media_kind = 'video'
ORDER BY fo.created_at DESC, fo.id DESC LIMIT 1`).Scan(&replacementIntent); err != nil {
		t.Fatal(err)
	}
	if replacementIntent != "replace" {
		t.Fatalf("same-target wash must freeze replacement intent before file planning, got %q", replacementIntent)
	}
	// The incremental trigger evaluates and settles: B active, A superseded.
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	var placementB string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementB); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(placementB, "Repl Real Show/Repl Real Show - E01@RealGroup.mkv") {
		t.Fatalf("revision B must win the active slot, got %q", placementB)
	}
	var superseded int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1 AND state = 'superseded'`, animeID).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if superseded != 1 {
		t.Fatalf("revision A placement must be superseded, got %d", superseded)
	}
	var settled int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = $1 AND state = 'settled'`, animeID).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if settled < 1 {
		t.Fatalf("replacement decision must settle, got %d", settled)
	}
	// Same-target settlement supersedes A but deliberately does not hand its
	// physical path to cleanup: the path now contains the active B bytes.
	var cleanupCount int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleanup_operations WHERE managed_anime_id = $1`, animeID).Scan(&cleanupCount); err != nil {
		t.Fatal(err)
	}
	if cleanupCount != 0 {
		t.Fatalf("same-path loser must not enter cleanup, got %d operations", cleanupCount)
	}
	// The active target still carries the Revision B bytes.
	targetBytes, err := os.ReadFile(placementB)
	if err != nil {
		t.Fatalf("active B target missing after cleanup: %v", err)
	}
	if !bytes.Contains(targetBytes, []byte("revision B 1080p")) {
		t.Fatalf("active target bytes were replaced/deleted by cleanup: %q", targetBytes)
	}

	// --- Stopped gate: Revision C appears but the stopped subscription neither
	// auto-downloads nor evaluates any replacement. ---
	fixture.SetCurrent("C")
	patchStatus, _ := doJSON[generated.Subscription](t, svc.httpHandler, http.MethodPatch, "/api/v1/anime/"+animeID+"/subscription", generated.SubscriptionPatch{State: generated.SubscriptionPatchStateStopped, ExpectedVersion: result.Subscription.Version}, nil)
	if patchStatus != http.StatusOK {
		t.Fatalf("stop subscription failed: status=%d", patchStatus)
	}
	triggerRefresh(t, ctx, svc, sourceID, animeID)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	// The C release is observed but never submitted/downloaded while stopped.
	var stoppedVerdict string
	if err := svc.db.QueryRowContext(ctx, `SELECT verdict FROM releases ORDER BY updated_at DESC LIMIT 1`).Scan(&stoppedVerdict); err != nil {
		t.Fatal(err)
	}
	if stoppedVerdict != "subscription_stopped" {
		t.Fatalf("stopped release must be blocked, got %q", stoppedVerdict)
	}
	var cTasks int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks`).Scan(&cTasks); err != nil {
		t.Fatal(err)
	}
	if cTasks != 2 {
		t.Fatalf("stopped refresh must not create a download task, got %d", cTasks)
	}
	var stoppedSettled int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = $1 AND state = 'settled'`, animeID).Scan(&stoppedSettled); err != nil {
		t.Fatal(err)
	}
	if stoppedSettled != settled {
		t.Fatalf("stopped instance must not settle new replacement decisions: before=%d after=%d", settled, stoppedSettled)
	}
	// The active slot still belongs to Revision B.
	var activePath string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&activePath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(activePath, "Repl Real Show/Repl Real Show - E01@RealGroup.mkv") {
		t.Fatalf("active slot must remain Revision B, got %q", activePath)
	}
}

// triggerRefresh submits a manual RSS refresh work item for the source.
func triggerRefresh(t *testing.T, ctx context.Context, svc *realChainServices, sourceID, animeID string) {
	t.Helper()
	var sourceVersion int64
	if err := svc.db.QueryRowContext(ctx, `SELECT version FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&sourceVersion); err != nil {
		t.Fatal(err)
	}
	tx, err := svc.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID},
		Kind:                 workitems.KindRSSRefresh,
		IdempotencyKey:       "manual-refresh:" + sourceID + ":" + fmt.Sprint(time.Now().UnixNano()),
		InputSnapshot:        json.RawMessage(`{"trigger":"manual"}`),
		MaxAttempts:          3,
		ExpectedOwnerVersion: sourceVersion,
	}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
}

func seedRetentionSingleCopy(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO global_config_sections
(section_name, schema_version, value, version, updated_at)
VALUES ('retention', 'retention-policy-v1', jsonb_build_object('admission_mode', 'single_copy', 'quality_profile', $1::text::jsonb), 1, CURRENT_TIMESTAMP)
ON CONFLICT (section_name) DO UPDATE
SET value = EXCLUDED.value, version = global_config_sections.version + 1, updated_at = CURRENT_TIMESTAMP`, string(filemaintenance.DefaultQualityProfileJSON)); err != nil {
		t.Fatal(err)
	}
}

// flatNamingRules renders videos without a Season directory (the "old"
// template of the wash scenario).
func flatNamingRules(root string) generated.NamingRules {
	prefix := filepath.ToSlash(root)
	return generated.NamingRules{
		Language: naming.Language,
		Tv: generated.NamingVariant{
			DirectoryTemplate:     prefix + "/${sanitize(anime.title)}",
			VideoFilenameTemplate: "${sanitize(anime.title)} - E${pad(ep.display, 2)}@${anime.group}.${file.ext}",
		},
		Movie: generated.NamingVariant{
			DirectoryTemplate:     prefix + "/${sanitize(anime.title)}",
			VideoFilenameTemplate: "${sanitize(anime.title)}.${file.ext}",
		},
		Unknown: generated.NamingVariant{
			DirectoryTemplate:     prefix + "/${sanitize(anime.title)}/extras",
			VideoFilenameTemplate: "${sanitize(file.name)}",
		},
	}
}

// patchNamingRules replaces the instance naming section with the given rules.
func patchNamingRules(t *testing.T, ctx context.Context, svc *realChainServices, animeID string, rules generated.NamingRules, expectedOwnerVersion int) {
	t.Helper()
	// An owner rules write carries every section: keep the stored values and
	// replace only naming.
	owner, err := svc.ownership.ReadRulesOwnerApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	var naming generated.NamingRulesOverride
	if err := naming.FromNamingRulesReplace(generated.NamingRulesReplace{Mode: generated.NamingRulesReplaceModeReplace, Value: rules}); err != nil {
		t.Fatal(err)
	}
	namingRaw, err := naming.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	applicationSections := owner.Sections
	applicationSections.Naming = namingRaw
	applicationSections.TagFilters = nil
	_, err = svc.ownership.PatchAnimeRules(ctx, animeID, ownershipapplication.RulesPatchInput{
		ExpectedVersion: int64(expectedOwnerVersion),
		Sections:        applicationSections,
	})
	if err != nil {
		var validation *apperrors.Validation
		if errors.As(err, &validation) {
			t.Fatalf("patch naming rules: %v details=%+v", err, validation.Details)
		}
		t.Fatalf("patch naming rules: %v", err)
	}
}

// defaultNamingRulesSource serves the shipped default naming rules for the
// reconciliation planner (mirrors the production workspace source when no
// instance override exists).
type defaultNamingRulesSource struct{}

func (defaultNamingRulesSource) EffectiveNamingRules(context.Context, string) (naming.NamingRules, error) {
	return naming.DefaultNamingRules(), nil
}

// subscribeRevisionScenario subscribes one single-episode instance with the
// given naming rules and collision policy, binds its metadata and returns its
// managed anime and RSS source ids.
func subscribeRevisionScenario(t *testing.T, ctx context.Context, svc *realChainServices, feedURL, title string, rules generated.NamingRules, collision generated.CollisionPolicy, key string) (string, string) {
	t.Helper()
	draft := singleEpisodeDraft(feedURL)
	draft.Title = title
	draft.RssSources[0].Title = title
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	var namingOverride generated.NamingRulesOverride
	if err := namingOverride.FromNamingRulesReplace(generated.NamingRulesReplace{
		Mode:  generated.NamingRulesReplaceModeReplace,
		Value: rules,
	}); err != nil {
		t.Fatal(err)
	}
	draft.Rules.Sections.Naming = &namingOverride
	var fileEffect generated.FileEffectRulesOverride
	if err := fileEffect.FromFileEffectRulesReplace(generated.FileEffectRulesReplace{
		Mode: generated.FileEffectRulesReplaceModeReplace,
		Value: generated.FileEffectRules{
			Operation:       generated.FileEffectRulesOperationMove,
			CollisionPolicy: collision,
		},
	}); err != nil {
		t.Fatal(err)
	}
	draft.Rules.Sections.FileEffect = &fileEffect
	draft = withDraftBusiness(t, svc.db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" || candidate.CurrentRevision == nil {
		t.Fatalf("candidate save failed: status=%d", saveStatus)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": key + "-subscribe", "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" || len(result.RssSources) != 1 {
		t.Fatalf("subscribe failed: status=%d", subscribeStatus)
	}
	animeID, sourceID := result.ManagedAnime.Id, result.RssSources[0].Id
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: key + "-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: key + "-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		Season:          new(1),
	}); err != nil {
		t.Fatal(err)
	}
	observeBoundWorkYear(t, ctx, svc.db, animeID, realChainObservedYear)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	return animeID, sourceID
}

func activeVideoPath(t *testing.T, ctx context.Context, db *sql.DB, animeID string) (string, string) {
	t.Helper()
	var id, path string
	if err := db.QueryRowContext(ctx, `SELECT id, physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&id, &path); err != nil {
		t.Fatal(err)
	}
	return id, path
}

// TestReplacementLoserIdentityChangedRealChain proves the deletion identity
// check: the superseded revision's file was replaced on disk by a file of
// another size after it was placed, so the deletion is skipped
// (loser_identity_changed) and the file at that path stays untouched.
func TestReplacementLoserIdentityChangedRealChain(t *testing.T) {
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	items := map[string]rssItem{
		"A": {GUID: "repl-ident-a-guid", Title: "[RealGroup] Ident Show - 01 [720p]"},
		"B": {GUID: "repl-ident-b-guid", Title: "[RealGroup] Ident Show - 01 [1080p]"},
	}
	filesA := []torrentFile{
		{Name: "Ident Show - 01.mkv", Content: []byte("revision A 720p video " + stamp)},
		{Name: "Ident Show - 01.nfo", Content: []byte("revision A nfo")},
	}
	filesB := []torrentFile{
		{Name: "Ident Show - 01.mkv", Content: []byte("revision B 1080p video with a larger payload " + stamp)},
		{Name: "Ident Show - 01.nfo", Content: []byte("revision B nfo")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"A": filesA, "B": filesB})
	t.Cleanup(fixture.Close)
	svc := newRealChainServices(t, fixture.client)
	t.Cleanup(func() { _ = svc.db.Close() })
	seedRetentionSingleCopy(t, svc.db)
	animeID, sourceID := subscribeRevisionScenario(t, ctx, svc, fixture.FeedURL(), "Ident Show", resolutionNamingRules(svc.targetRoot), generated.CollisionPolicyFail, "repl-ident")

	svc.driveToPlacement(t, ctx, animeID, "A", filesA, 1)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	placementA, videoA := activeVideoPath(t, ctx, svc.db, animeID)
	// The owner replaced the file at A's path with a different file.
	foreign := []byte("a different file the owner put here, of another size " + stamp)
	if err := os.WriteFile(videoA, foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	fixture.SetCurrent("B")
	triggerRefresh(t, ctx, svc, sourceID, animeID)
	svc.driveToPlacement(t, ctx, animeID, "B", filesB, 2)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	placementB, videoB := activeVideoPath(t, ctx, svc.db, animeID)
	if placementB == placementA || videoB == videoA {
		t.Fatalf("revision B must win at its own path: %q (A at %q)", videoB, videoA)
	}
	waitForCleanupState(t, ctx, svc.db, animeID, string(filemaintenance.CleanupSkipped))
	var state, code, source string
	if err := svc.db.QueryRowContext(ctx, `SELECT state, COALESCE(failure_code, ''), source_path FROM cleanup_operations WHERE managed_anime_id = $1 AND placement_id = $2`, animeID, placementA).Scan(&state, &code, &source); err != nil {
		t.Fatal(err)
	}
	if state != string(filemaintenance.CleanupSkipped) || code != filemaintenance.CleanupSkipLoserIdentityChanged || source != videoA {
		t.Fatalf("cleanup = %s/%s at %q, want skipped/%s at %q", state, code, source, filemaintenance.CleanupSkipLoserIdentityChanged, videoA)
	}
	if kept, err := os.ReadFile(videoA); err != nil || !bytes.Equal(kept, foreign) {
		t.Fatalf("the changed file must stay untouched: err=%v", err)
	}
	if kept, err := os.ReadFile(videoB); err != nil || !bytes.Equal(kept, filesB[0].Content) {
		t.Fatalf("the winner's video must be untouched: err=%v", err)
	}
}

// TestReplacementSamePathUnderSkipPolicyRealChain proves that an approved
// replacement at the incumbent's exact path is an atomic replacement even
// under the skip (fail) collision policy: B takes A's path, A is superseded
// without a deletion obligation, and the path carries B's bytes.
func TestReplacementSamePathUnderSkipPolicyRealChain(t *testing.T) {
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	items := map[string]rssItem{
		"A": {GUID: "repl-skip-a-guid", Title: "[RealGroup] Skip Show - 01 [720p]"},
		"B": {GUID: "repl-skip-b-guid", Title: "[RealGroup] Skip Show - 01 [1080p]"},
	}
	filesA := []torrentFile{
		{Name: "Skip Show - 01.mkv", Content: []byte("revision A 720p video " + stamp)},
		{Name: "Skip Show - 01.nfo", Content: []byte("revision A nfo")},
	}
	filesB := []torrentFile{
		{Name: "Skip Show - 01.mkv", Content: []byte("revision B 1080p video with a larger payload " + stamp)},
		{Name: "Skip Show - 01.nfo", Content: []byte("revision B nfo")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"A": filesA, "B": filesB})
	t.Cleanup(fixture.Close)
	svc := newRealChainServices(t, fixture.client)
	t.Cleanup(func() { _ = svc.db.Close() })
	seedRetentionSingleCopy(t, svc.db)
	animeID, sourceID := subscribeRevisionScenario(t, ctx, svc, fixture.FeedURL(), "Skip Show", flatNamingRules(svc.targetRoot), generated.CollisionPolicyFail, "repl-skip")

	svc.driveToPlacement(t, ctx, animeID, "A", filesA, 1)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	placementA, videoA := activeVideoPath(t, ctx, svc.db, animeID)

	fixture.SetCurrent("B")
	triggerRefresh(t, ctx, svc, sourceID, animeID)
	svc.driveToPlacement(t, ctx, animeID, "B", filesB, 2)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	placementB, videoB := activeVideoPath(t, ctx, svc.db, animeID)
	if placementB == placementA || videoB != videoA {
		t.Fatalf("revision B must replace A at its path: %q (A at %q)", videoB, videoA)
	}
	var loserState string
	if err := svc.db.QueryRowContext(ctx, `SELECT state FROM placements WHERE id = $1`, placementA).Scan(&loserState); err != nil {
		t.Fatal(err)
	}
	if loserState != "superseded" {
		t.Fatalf("revision A must be superseded, got %q", loserState)
	}
	var cleanups int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleanup_operations WHERE managed_anime_id = $1`, animeID).Scan(&cleanups); err != nil {
		t.Fatal(err)
	}
	if cleanups != 0 {
		t.Fatalf("a same-path loser must not enter cleanup, got %d operations", cleanups)
	}
	if kept, err := os.ReadFile(videoB); err != nil || !bytes.Equal(kept, filesB[0].Content) {
		t.Fatalf("the shared path must carry B's bytes: err=%v", err)
	}
	var failed int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_operations WHERE state = 'failed'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("the skip policy must not fail the approved replacement, got %d failed operations", failed)
	}
}
