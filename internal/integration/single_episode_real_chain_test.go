package integration

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	accessapplication "bansoko/internal/accesscontrol/application"
	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	acquisitionactivity "bansoko/internal/acquisition/terminal/activity"
	acquisitionsettlement "bansoko/internal/acquisition/terminal/settlement"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	animeapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
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
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/rss/application"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/mkvfixture"
	"bansoko/internal/transport/animeworkspace"
	"bansoko/internal/transport/httpapi"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

func TestSingleEpisodeRealPostgresQBRSSFileOperation(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	username := strings.TrimSpace(os.Getenv("BANSOKO_QB_USERNAME"))
	password := strings.TrimSpace(os.Getenv("BANSOKO_QB_PASSWORD"))
	if endpoint == "" || username == "" || password == "" {
		t.Skip("BANSOKO_QB_ENDPOINT, BANSOKO_QB_USERNAME, and BANSOKO_QB_PASSWORD are required for the real integration test")
	}
	if endpoint != "http://127.0.0.1:18087" && endpoint != "http://localhost:18087" {
		t.Logf("using configured qB endpoint %q", endpoint)
	}
	// The downloaded file is a real Matroska container: the artifact owner reads
	// its resolution, codecs and container with the real mediainfo binary.
	requireMediaInfo(t)

	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	hostDownloadRoot := filepath.Join(root, ".local-artifacts", "real-chain-qb", "downloads")
	if harnessRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT")); harnessRoot != "" {
		hostDownloadRoot = harnessRoot
	}
	if err := os.MkdirAll(hostDownloadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// A realistic single-episode file name: the per-file parser must agree
	// with the release identity (E01). The run tag keeps qB names unique
	// without digits the episode parser could mistake for an episode.
	artifactName := fmt.Sprintf("Real Chain Show - 01 [%s].mkv", letterRunTag(time.Now().UnixNano()))
	// A tiny valid Matroska file (HEVC 1920x1080 video, AAC audio, no frames);
	// the title makes every run's bytes, and so its info hash, unique.
	artifactBytes := mkvfixture.HEVC1080pAAC("real chain real artifact " + time.Now().UTC().Format(time.RFC3339Nano))
	artifactDigest := sha256.Sum256(artifactBytes)
	targetRoot := t.TempDir()

	fixtureClient, fixtureURL, torrentBytes, closeFixture := startSingleEpisodeFixture(t, artifactName, artifactBytes)
	t.Cleanup(closeFixture)
	port := fixtureURL.Port
	feedURL := realChainFixtureURL(port, "/feed.xml")

	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte("test-secret-encryption-key-0123456789abcdef"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	configService := configurationapplication.NewHTTPService(configurationapplication.NewService(db, secrets))
	connectionID := "real-chain-qb-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	// Same-path deployment convention: the qB fixture mounts the host download
	// directory at the identical absolute path, so the qB-reported root is
	// directly readable by this process. Under the real-effect harness the qB
	// container exposes /downloads while the host root is a temp dir; the
	// harness supplies it via BANSOKO_HARNESS_DOWNLOAD_ROOT and the connection
	// carries downloader_root (host) + download_root (container /downloads).
	downloadRoot := hostDownloadRoot
	harnessRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT"))
	if harnessRoot != "" {
		hostDownloadRoot = harnessRoot
		downloadRoot = "/downloads" // qB container save path
	}
	var connectionInput generated.ConnectionInput
	var secret generated.ConnectionSecret
	if err := secret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: password}); err != nil {
		t.Fatal(err)
	}
	if err := connectionInput.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		BaseUrl:        endpoint,
		DownloadRoot:   downloadRoot,
		DownloaderRoot: &hostDownloadRoot,
		Enabled:        true,
		Kind:           generated.QBittorrentConnectionInputKindQbittorrent,
		Name:           connectionID,
		Secret:         secret,
		Username:       username,
	}); err != nil {
		t.Fatal(err)
	}
	// Fresh database: create the connection with expected version 0, then
	// update to the runtime config (downloader_root for the harness mapping)
	// with the bumped version read from the row.
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
	configuredQB, err := connection.AsQBittorrentConnection()
	if err != nil || !configuredQB.Enabled {
		t.Fatalf("typed qB connection was not configured: connection=%+v err=%v", connection, err)
	}
	if _, err := configService.GetQBittorrentRuntimeConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := (qbittorrent.Client{Endpoint: endpoint, Username: username, Password: password, Timeout: 20 * time.Second}).Health(ctx); err != nil {
		t.Fatalf("qB setup health check failed: %v", err)
	}
	var configuredConnections int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM connections WHERE kind = 'qbittorrent' AND state = 'configured'`).Scan(&configuredConnections); err != nil {
		t.Fatal(err)
	}
	if configuredConnections != 1 {
		t.Fatalf("expected exactly one configured qB connection, got %d", configuredConnections)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	feedFetcher := animeapplication.HTTPFeedFetcher{
		Client:            fixtureClient,
		AllowPrivateHosts: []string{realChainFixtureHost()},
	}
	animeService, err := animeapplication.NewService(db, store, feedFetcher)
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
	ownershipService.SetOffsetAlignment(filemaintenance.NewOffsetAlignmentService())
	// Mirror the production composition root: candidate activation must write
	// the revision-frozen instance parser policy, or the automatic refresh and
	// evaluate path fails closed with parser_policy_required.
	parserPolicies := parserpolicy.NewService(db)
	draftService.SetFrozenParserPolicy(parserPolicies)
	accessService := accessapplication.NewServiceWithSessionSecret(db, []byte("real-chain-session-secret-32-bytes"))
	// Keep this local readmodel assembly aligned with cmd/bansoko-server/provideReadmodelService;
	// it must be updated there too if the production collaborator set changes.
	// MediaFacts uses the production repository-backed store, preserving frozen file badges.
	readmodelService, err := readmodelapplication.NewService(db, readmodelapplication.Dependencies{
		Config: animeService, Owners: ownershipService, Revisions: draftService,
		MediaFacts: facts.NewStoreWithRepository(mediapersistence.NewRepository(db)),
	})
	if err != nil {
		t.Fatal(err)
	}
	rssFetcher := acquisitionapplication.HTTPRSSFetcher{
		Client:            fixtureClient,
		AllowPrivateHosts: []string{realChainFixtureHost()},
		TorrentReader: acquisitionapplication.HTTPTorrentMetadataReader{
			Client:            fixtureClient,
			AllowPrivateHosts: []string{realChainFixtureHost()},
		},
	}
	rssService, err := application.NewService(db, store, fixtureSourceFetcher{fetcher: rssFetcher})
	if err != nil {
		t.Fatal(err)
	}
	rssService.SetActivityWriter(observability)
	rssService.SetCandidateFactsRepository(facts.NewPGRepository(db))
	rssService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	httpServer, err := httpapi.NewServer(httpapi.Dependencies{
		DB: db, Access: accessService, Draft: draftService, Ownership: ownershipService, Readmodel: readmodelService, ReleaseRetry: rssService,
		Environment: "development", SkipAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpHandler := httpServer.Handler()
	// Ordinary RSS runs the shared production target boundary: the same
	// TargetSet planner, filesystem probe and persistence-backed target owner
	// the composition root wires, so this chain proves real target admission
	// (TargetSet rows, observations, exact entry size) rather than the legacy
	// payload-only path.
	rssService.SetTargetStore(acquisitiontarget.NewStore(targetpersistence.NewRepository(db)))
	rssService.SetTargetAcquisition(acquisitiontarget.FilesystemProbe{})
	// The TV video name carries the resolution and the video codec, so the run
	// shows what the placed file's name and its persisted facts hold.
	namingRules := rootNamingRules(targetRoot)
	namingRules.TV.VideoFilenameTemplate = realChainMediaFactsTemplate
	storeRealChainNamingRules(t, ctx, db, targetRoot, namingRules)
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
	// The artifact owner writes the per-file frozen media facts the placement
	// coverage reads; without this wiring the verified file has no frozen fact
	// row and the placement sink fails with placement_evidence_missing.
	artifactService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	// Same MediaInfo wiring as the server composition root (providers.go): the
	// real binary reads the real downloaded file, and the recording logger lets
	// the run prove that no degraded diagnostic was emitted.
	mediaProbeLogger := &recordingRuntimeLogger{}
	artifactService.SetMediaProber(artifactadapter.OSMediaProber{}, mediaProbeLogger)
	// Same media-fact wiring as the server composition root (providers.go).
	fileService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	// The placement sink admits verified file operations into placements and
	// records placement_changed; wire the same observable writer.
	placementService := filemaintenance.NewPlacementService(db, filemaintenance.NewFileOperationEvidenceReader(db), nil)
	placementService.SetActivityWriter(observability)
	fileService.SetPlacementSink(newPlacementSinkForTest(placementService, db))

	runtime := runtimeworker.NewWorker(store, "real-chain-worker", time.Minute)
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
	} {
		if err := runtime.Register(registration.kind, registration.handler); err != nil {
			t.Fatal(err)
		}
	}

	// A draft is created by copying the Global preset (one-time input): the
	// current_input preview reads only the draft's own sections and fails
	// closed on absent ones, so load before previewing, as the client does.
	draft := withDraftBusiness(t, db, singleEpisodeDraft(feedURL))
	previewBefore, err := readOwnerSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	previewStatus, preview := doJSON[generated.RSSListView](t, httpHandler, http.MethodPost, "/api/v1/anime/rss-list-view", generated.RSSListViewInput{InputSource: generated.RSSListViewInputInputSourceCurrentInput, Draft: &draft}, nil)
	if previewStatus != http.StatusOK {
		t.Fatalf("read model status = %d, want 200: %+v", previewStatus, preview)
	}
	if !preview.MetadataHold.Held || !containsMetadataMissing(preview.MetadataHold.Missing, generated.MetadataHoldMissingBgm) || !containsMetadataMissing(preview.MetadataHold.Missing, generated.MetadataHoldMissingTmdb) {
		t.Fatalf("read model must expose the sole metadata hold: %+v", preview.MetadataHold)
	}
	previewAfter, err := readOwnerSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if previewBefore != previewAfter {
		t.Fatalf("preview changed owner rows or versions: before=%+v after=%+v", previewBefore, previewAfter)
	}

	saveBefore, err := readOwnerSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" || candidate.ActivatedManagedAnimeId != nil {
		t.Fatalf("candidate save failed or returned an activated candidate: status=%d candidate=%+v", saveStatus, candidate)
	}
	saveAfter, err := readOwnerSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if saveBefore != saveAfter {
		t.Fatalf("candidate Save changed runtime owner rows or versions: before=%+v after=%+v", saveBefore, saveAfter)
	}

	if candidate.CurrentRevision == nil {
		t.Fatalf("saved candidate carries no immutable revision: %+v", candidate)
	}
	key := "real-chain-subscribe-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	subscribeURL := "/api/v1/anime-config-candidates/" + candidate.Id + "/subscribe"
	// The activation identity is the saved immutable revision, not the mutable
	// draft, so the request must name the exact revision it materializes.
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, httpHandler, http.MethodPost, subscribeURL, generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": key, "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" || result.Subscription.Id == "" || result.Metadata.ManagedAnimeId != result.ManagedAnime.Id || result.Metadata.Version != 1 || len(result.RssSources) != 1 || result.EffectiveConfigVersions.Parser < 1 || result.EffectiveConfigVersions.ReleaseFilter < 1 || result.EffectiveConfigVersions.FileFilter < 1 || result.EffectiveConfigVersions.Naming < 1 || result.EffectiveConfigVersions.FileEffect < 1 || result.EffectiveConfigVersions.Subgroups < 1 {
		t.Fatalf("candidate subscribe result is incomplete: status=%d result=%+v", subscribeStatus, result)
	}
	getStatus, activatedCandidate := doJSON[generated.AnimeConfigCandidate](t, httpHandler, http.MethodGet, "/api/v1/anime-config-candidates/"+candidate.Id, nil, nil)
	if getStatus != http.StatusOK || activatedCandidate.ActivatedManagedAnimeId == nil || *activatedCandidate.ActivatedManagedAnimeId != result.ManagedAnime.Id {
		t.Fatalf("candidate activation link is wrong: status=%d candidate=%+v anime=%q", getStatus, activatedCandidate, result.ManagedAnime.Id)
	}
	assertOwnerCounts(t, ctx, db, result.ManagedAnime.Id, result.Subscription.Id, result.RssSources[0].Id)
	var createdLogs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscription_log_entries WHERE subscription_id = $1 AND kind = 'subscription_created'`, result.Subscription.Id).Scan(&createdLogs); err != nil {
		t.Fatal(err)
	}
	if createdLogs != 1 {
		t.Fatalf("expected one subscription_created log, got %d", createdLogs)
	}
	replayBefore, err := readOwnerSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	// The replay must carry the identical activation identity (same key, same
	// revision) to hit the same idempotency record.
	replayStatus, replayed := doJSON[generated.SubscribeResult](t, httpHandler, http.MethodPost, subscribeURL, generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": key, "If-Match": "1"})
	if replayStatus != http.StatusCreated {
		t.Fatalf("same-key candidate Subscribe replay failed: status=%d", replayStatus)
	}
	if replayed.ManagedAnime.Id != result.ManagedAnime.Id || replayed.Subscription.Id != result.Subscription.Id || len(replayed.RssSources) != 1 || replayed.RssSources[0].Id != result.RssSources[0].Id {
		t.Fatalf("same-key candidate Subscribe replay changed IDs: first=%+v replay=%+v", result, replayed)
	}
	replayAfter, err := readOwnerSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if replayBefore != replayAfter {
		t.Fatalf("same-key candidate Subscribe replay changed runtime owners: before=%+v after=%+v", replayBefore, replayAfter)
	}
	var replayLogs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscription_log_entries WHERE subscription_id = $1 AND kind = 'subscription_created'`, result.Subscription.Id).Scan(&replayLogs); err != nil {
		t.Fatal(err)
	}
	if replayLogs != 1 {
		t.Fatalf("same-key candidate Subscribe replay duplicated creation log: %d", replayLogs)
	}

	if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
		t.Fatal(err)
	}
	var runtimeHealth, releaseVerdict, releaseReason, releaseDetail string
	if err := db.QueryRowContext(ctx, `
	SELECT rt.health, r.verdict, r.reason_code, COALESCE(r.reason_detail, '')
	FROM rss_source_runtime rt
	JOIN releases r ON r.rss_source_id = rt.rss_source_id
	WHERE rt.rss_source_id = $1`, result.RssSources[0].Id).Scan(&runtimeHealth, &releaseVerdict, &releaseReason, &releaseDetail); err != nil {
		t.Fatal(err)
	}
	if runtimeHealth != "ready" || releaseVerdict != string(generated.ReleaseVerdictWaitingMetadata) || releaseReason != "metadata_incomplete" || !strings.Contains(releaseDetail, "bgm") || !strings.Contains(releaseDetail, "tmdb") {
		t.Fatalf("initial owner chain facts are wrong: health=%q verdict=%q reason=%q detail=%q", runtimeHealth, releaseVerdict, releaseReason, releaseDetail)
	}
	var taskCount, qbSubmitCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE kind = 'qb_submit'`).Scan(&qbSubmitCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 || qbSubmitCount != 0 {
		t.Fatalf("metadata hold must not create download work: tasks=%d qb_submit=%d", taskCount, qbSubmitCount)
	}
	workspaceApplication, err := readmodelService.GetAnimeWorkspaceApplication(ctx, result.ManagedAnime.Id)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := animeworkspace.MapAnimeWorkspace(workspaceApplication)
	if err != nil {
		t.Fatal(err)
	}
	if !workspace.MetadataHold.Held || len(workspace.RssSources) != 1 || len(workspace.Releases) != 1 || len(workspace.Logs) < 3 {
		t.Fatalf("workspace does not expose initial owner chain: %+v", workspace)
	}
	assertLogKinds(t, workspace.Logs, generated.SubscriptionLogKindSubscriptionCreated, generated.SubscriptionLogKindRssNewContent, generated.SubscriptionLogKindReleaseProcessed)

	metadata, err := ownershipService.PatchAnimeMetadata(ctx, result.ManagedAnime.Id, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "real-chain-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "real-chain-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The default TV directory names the providers' year observation, so the
	// fixture records it before the release is accepted.
	observeBoundWorkYear(t, ctx, db, result.ManagedAnime.Id, realChainObservedYear)
	if metadata.Version != 2 {
		t.Fatalf("metadata patch should advance version to 2, got %d", metadata.Version)
	}
	if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
		t.Fatal(err)
	}
	var submittedVerdict string
	if err := db.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE rss_source_id = $1`, result.RssSources[0].Id).Scan(&submittedVerdict); err != nil {
		t.Fatal(err)
	}
	if submittedVerdict != string(generated.ReleaseVerdictSubmitted) {
		t.Fatalf("metadata completion did not submit release: %q", submittedVerdict)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("expected one download task after metadata completion, got %d", taskCount)
	}
	var taskID, taskState string
	var frozenInput []byte
	var taskCorrelation sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT id, state, frozen_input, correlation_id FROM download_tasks LIMIT 1`).Scan(&taskID, &taskState, &frozenInput, &taskCorrelation); err != nil {
		t.Fatal(err)
	}
	if !taskCorrelation.Valid || len(taskCorrelation.String) < 8 {
		t.Fatalf("download task must carry a media lineage correlation, got %q", taskCorrelation.String)
	}
	var causationType, causationID string
	if err := db.QueryRowContext(ctx, `SELECT causation_type, causation_id FROM download_tasks WHERE id = $1`, taskID).Scan(&causationType, &causationID); err != nil {
		t.Fatal(err)
	}
	if causationType != "release" || causationID == "" {
		t.Fatalf("download task causation=%q/%q, want release chain", causationType, causationID)
	}
	var frozenTorrentInput struct {
		TorrentInputID       string `json:"torrent_input_id"`
		TorrentPayloadDigest string `json:"torrent_payload_digest"`
		TorrentFilename      string `json:"torrent_filename"`
		DownloadURL          string `json:"download_url"`
	}
	if err := json.Unmarshal(frozenInput, &frozenTorrentInput); err != nil {
		t.Fatalf("decode download task frozen_input: %v", err)
	}
	wantTorrentFilename := artifactName + ".torrent"
	if frozenTorrentInput.TorrentInputID == "" || frozenTorrentInput.TorrentPayloadDigest == "" || frozenTorrentInput.TorrentFilename != wantTorrentFilename || frozenTorrentInput.DownloadURL != "" {
		t.Fatalf("download task must freeze a torrent input reference, not a URL: got=%+v want_filename=%q", frozenTorrentInput, wantTorrentFilename)
	}
	var storedPayload []byte
	var storedDigest, storedFilename string
	if err := db.QueryRowContext(ctx, `
	SELECT payload, payload_digest, filename
	FROM release_torrent_inputs
	WHERE id = $1`, frozenTorrentInput.TorrentInputID).Scan(&storedPayload, &storedDigest, &storedFilename); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedPayload, torrentBytes) || storedDigest != frozenTorrentInput.TorrentPayloadDigest || storedFilename != frozenTorrentInput.TorrentFilename {
		t.Fatalf("persisted torrent input differs from the fetched fixture: digest=%q filename=%q", storedDigest, storedFilename)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_submit'`, taskID).Scan(&qbSubmitCount); err != nil {
		t.Fatal(err)
	}
	if qbSubmitCount != 1 || (taskState != "queued" && taskState != "submitting" && taskState != "downloading" && taskState != "completed") {
		t.Fatalf("download task/qB submit chain is wrong: state=%q qb_submit=%d", taskState, qbSubmitCount)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE kind = 'qb_submit'`).Scan(&qbSubmitCount); err != nil {
		t.Fatal(err)
	}
	if qbSubmitCount != 1 {
		t.Fatalf("expected exactly one qb_submit work item, got %d", qbSubmitCount)
	}
	waitForArtifact(t, filepath.Join(hostDownloadRoot, artifactName), int64(len(artifactBytes)), 60*time.Second)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := downloadService.DispatchQBSync(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT state FROM download_tasks WHERE id = $1`, taskID).Scan(&taskState); err != nil {
			t.Fatal(err)
		}
		var artifactState string
		if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT state FROM artifacts WHERE download_task_id = $1), '')`, taskID).Scan(&artifactState); err != nil {
			t.Fatal(err)
		}
		if taskState == string(generated.DownloadTaskStateCompleted) && artifactState == string(generated.ArtifactStateVerified) {
			break
		}
		time.Sleep(time.Second)
	}
	if taskState != string(generated.DownloadTaskStateCompleted) {
		t.Fatalf("qB task did not complete within 60 seconds: %q", taskState)
	}
	var artifactID, artifactState string
	if err := db.QueryRowContext(ctx, `SELECT id, state FROM artifacts WHERE download_task_id = $1`, taskID).Scan(&artifactID, &artifactState); err != nil {
		t.Fatal(err)
	}
	if artifactState != string(generated.ArtifactStateVerified) {
		t.Fatalf("artifact is not verified: %q", artifactState)
	}
	// The artifact inherits the download task's media lineage correlation and
	// causally points at the task.
	var artifactCorrelation, artifactCausation string
	if err := db.QueryRowContext(ctx, `SELECT correlation_id, causation_id FROM artifacts WHERE id = $1`, artifactID).Scan(&artifactCorrelation, &artifactCausation); err != nil {
		t.Fatal(err)
	}
	if artifactCorrelation != taskCorrelation.String || artifactCausation != taskID {
		t.Fatalf("artifact lineage correlation=%q causation=%q, want %q/%q", artifactCorrelation, artifactCausation, taskCorrelation.String, taskID)
	}
	if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
		t.Fatal(err)
	}
	// S4 artifact files are keyed by a content-derived file_ref (sha256:...),
	// so the single-video artifact is addressed by its media kind.
	var sourcePath, sourceFingerprint string
	var sourceSize int64
	var fingerprintJSON []byte
	if err := db.QueryRowContext(ctx, `
	SELECT source_path, size_bytes, fingerprint->>'value', fingerprint
	FROM artifact_files WHERE artifact_id = $1 AND media_kind = 'video'`, artifactID).Scan(&sourcePath, &sourceSize, &sourceFingerprint, &fingerprintJSON); err != nil {
		t.Fatal(err)
	}
	if sourceSize != int64(len(artifactBytes)) || sourceFingerprint != hex.EncodeToString(artifactDigest[:]) {
		t.Fatalf("artifact facts mismatch: path=%q size=%d sha=%q want_size=%d want_sha=%q", sourcePath, sourceSize, sourceFingerprint, len(artifactBytes), hex.EncodeToString(artifactDigest[:]))
	}
	actual, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, artifactBytes) {
		t.Fatalf("downloaded artifact bytes differ: got=%q want=%q", actual, artifactBytes)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatal(err)
	}

	// The frozen TargetSet and its pre-download observation are the admission
	// evidence: exactly one video target carrying the selected torrent entry's
	// exact byte size, and a missing observation recorded before qB admission.
	var frozenTargetPath string
	var frozenTargetSize int64
	if err := db.QueryRowContext(ctx, `
SELECT t.target_path, t.expected_size_bytes
FROM acquisition_targets t
JOIN releases r ON r.id = t.release_id
WHERE r.download_task_id = $1 AND t.media_kind = 'video'`, taskID).Scan(&frozenTargetPath, &frozenTargetSize); err != nil {
		t.Fatal(err)
	}
	if frozenTargetSize != int64(len(artifactBytes)) {
		t.Fatalf("frozen target size=%d, want selected entry size %d", frozenTargetSize, len(artifactBytes))
	}
	var observedTargetState string
	if err := db.QueryRowContext(ctx, `
SELECT t.observation_state
FROM acquisition_targets t
JOIN releases r ON r.id = t.release_id
WHERE r.download_task_id = $1`, taskID).Scan(&observedTargetState); err != nil {
		t.Fatal(err)
	}
	if observedTargetState != "missing" {
		t.Fatalf("pre-download target observation=%q, want missing", observedTargetState)
	}

	if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
		t.Fatal(err)
	}
	workspaceApplication, err = readmodelService.GetAnimeWorkspaceApplication(ctx, result.ManagedAnime.Id)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err = animeworkspace.MapAnimeWorkspace(workspaceApplication)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.FileOperations) != 1 || workspace.FileOperations[0].State != generated.FileOperationStateCompleted {
		t.Fatalf("file operation did not complete: %+v", workspace.FileOperations)
	}
	targetPath := workspace.FileOperations[0].TargetPath
	// The admission TargetSet is observed before the file exists, so its path
	// has the title's resolution and no codec. The file operation renders the
	// target at execution from the artifact's persisted facts: the placed path
	// names the real resolution and the real video codec, and it is the typed
	// acquisition's canonical path.
	if strings.Contains(frozenTargetPath, "HEVC") {
		t.Fatalf("admission target path %q cannot name a codec before the file exists", frozenTargetPath)
	}
	if !strings.HasSuffix(targetPath, " [1080p] [HEVC].mkv") {
		t.Fatalf("file operation target=%q, want the real media.resolution and media.video_codec in its name (admission path %q)", targetPath, frozenTargetPath)
	}
	var acquisitionCanonicalPath string
	if err := db.QueryRowContext(ctx, `SELECT f.canonical_path FROM acquisition_files f JOIN artifacts a ON a.acquisition_id = f.acquisition_id WHERE a.id = $1 AND f.media_kind = 'video'`, artifactID).Scan(&acquisitionCanonicalPath); err != nil {
		t.Fatal(err)
	}
	if acquisitionCanonicalPath != targetPath {
		t.Fatalf("acquisition canonical path=%q, want the placed target %q", acquisitionCanonicalPath, targetPath)
	}
	target, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(target, artifactBytes) {
		t.Fatalf("target bytes differ from source: target=%q source=%q", target, artifactBytes)
	}
	if workspace.FileOperations[0].TargetFingerprint == nil || workspace.FileOperations[0].TargetFingerprint.Value != sourceFingerprint || workspace.FileOperations[0].TargetFingerprint.SizeBytes != sourceSize {
		t.Fatalf("target fingerprint does not match source: target_fingerprint=%+v source_sha=%q source_size=%d", workspace.FileOperations[0].TargetFingerprint, sourceFingerprint, sourceSize)
	}
	// Migration 186: the typed completion itself stores the target size; the
	// read-side fallback to expected_size_bytes is only for older rows.
	var storedTargetSize sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT (target_fingerprint->>'size_bytes')::bigint FROM file_operations WHERE id = $1`, workspace.FileOperations[0].Id).Scan(&storedTargetSize); err != nil {
		t.Fatal(err)
	}
	if !storedTargetSize.Valid || storedTargetSize.Int64 != sourceSize {
		t.Fatalf("typed completion stored target size %+v, want %d", storedTargetSize, sourceSize)
	}
	realTargetPath, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	realTargetRoot, err := filepath.EvalSymlinks(targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	relativeTarget, err := filepath.Rel(realTargetRoot, realTargetPath)
	if err != nil || relativeTarget == ".." || strings.HasPrefix(relativeTarget, ".."+string(os.PathSeparator)) || filepath.IsAbs(relativeTarget) {
		t.Fatalf("file operation target escaped test root: %q", targetPath)
	}
	if !strings.Contains(string(fingerprintJSON), sourceFingerprint) {
		t.Fatalf("stored artifact fingerprint is missing source digest: %s", fingerprintJSON)
	}
	// A typed (S4) FileOperation records its acquisition root as
	// correlation_id (plan commit 153, re-checked by 154/170); the Business Log
	// lineage continues through the task correlation asserted below.
	var operationCorrelation, operationRoot string
	if err := db.QueryRowContext(ctx, `
SELECT correlation_id, acquisition_id FROM file_operations WHERE artifact_id = $1`, artifactID).Scan(&operationCorrelation, &operationRoot); err != nil {
		t.Fatal(err)
	}
	if operationCorrelation != operationRoot {
		t.Fatalf("typed file operation correlation=%q, want its acquisition root %q", operationCorrelation, operationRoot)
	}

	// The full media chain must be observable in Business Logs: download
	// submission/completion, artifact verification, the delivered file and
	// placement admission all carry Activity records through the same lineage.
	// The typed stages write none themselves; the independent consumer
	// derives them from the committed facts.
	if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
		t.Fatal(err)
	}
	// T1: the settlement producer settles the root once every stage is
	// terminal, so the run log can show the acquisition's result.
	settlement, err := acquisitionsettlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settlement.RunPass(ctx); err != nil {
		t.Fatalf("settlement pass: %v", err)
	}
	if _, err := (&acquisitionactivity.Consumer{DB: db, Writer: observability}).ConsumeOnce(ctx); err != nil {
		t.Fatal(err)
	}
	workspaceApplication, err = readmodelService.GetAnimeWorkspaceApplication(ctx, result.ManagedAnime.Id)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err = animeworkspace.MapAnimeWorkspace(workspaceApplication)
	if err != nil {
		t.Fatal(err)
	}
	assertLogKinds(t, workspace.Logs, generated.SubscriptionLogKindSubscriptionCreated, generated.SubscriptionLogKindRssNewContent,
		generated.SubscriptionLogKindReleaseProcessed, generated.SubscriptionLogKindDownloadChanged,
		generated.SubscriptionLogKindArtifactVerified, generated.SubscriptionLogKindFileAlignmentCompleted,
		generated.SubscriptionLogKindPlacementChanged)
	// The delivered file shows as imported with its readable reason, and a
	// repeated pass writes nothing new.
	delivered := false
	for _, entry := range workspace.Logs {
		if entry.Kind == generated.SubscriptionLogKindFileAlignmentCompleted && entry.MessageCode == generated.SubscriptionLogMessageCodeFileImported &&
			entry.ReasonCode != nil && *entry.ReasonCode == "file_alignment_completed" {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("workspace log has no 文件已入库 entry: %+v", workspace.Logs)
	}
	var rootState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM acquisitions WHERE id = (SELECT acquisition_id FROM file_operations WHERE artifact_id = $1)`, artifactID).Scan(&rootState); err != nil {
		t.Fatal(err)
	}
	if rootState != "succeeded" {
		t.Fatalf("real chain root settled to %q, want succeeded", rootState)
	}
	var settled int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscription_log_entries WHERE managed_anime_id = $1 AND kind = 'acquisition_settled' AND outcome = 'success' AND message_code = 'acquisition_succeeded'`, result.ManagedAnime.Id).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if settled != 1 {
		t.Fatalf("succeeded root has %d 获取完成 entries, want 1", settled)
	}
	if again, err := (&acquisitionactivity.Consumer{DB: db, Writer: observability}).ConsumeOnce(ctx); err != nil || again.Written != 0 {
		t.Fatalf("repeated activity pass = %+v, %v; want nothing written", again, err)
	}
	// Every media-chain Activity shares the same lineage correlation id, so a
	// user can trace the release from admission to placement in one chain.
	var mediaChainCorrelations int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM subscription_log_entries
WHERE managed_anime_id = $1 AND kind IN ('release_processed', 'download_changed', 'artifact_verified', 'placement_changed')
  AND correlation_id = $2`, result.ManagedAnime.Id, taskCorrelation.String).Scan(&mediaChainCorrelations); err != nil {
		t.Fatal(err)
	}
	if mediaChainCorrelations < 4 {
		t.Fatalf("media chain Activities with one correlation = %d, want at least 4", mediaChainCorrelations)
	}
	assertNoDuplicateLiveWorkItems(t, ctx, db)

	// Execution-time media facts: the real file qB downloaded was read by the
	// real mediainfo binary, and the four facts are stored with the catalog
	// vocabulary in artifact_files.facts, the single source every render path and
	// notification reads.
	if events := mediaProbeEvents(mediaProbeLogger); len(events) != 0 {
		t.Fatalf("the media probe logged diagnostics on a readable file: %+v", events)
	}
	assertRealChainMediaFacts(t, ctx, db, artifactID)
	// The maintenance renderer reads the same persisted facts and must name the
	// same two values, so a later alignment pass sees the placed path as correct.
	renderedFromFacts := assertRenderedFromRealFacts(t, ctx, db, ownershipService, result.ManagedAnime.Id, targetPath, sourceSize)
	if renderedFromFacts != targetPath {
		t.Fatalf("maintenance render of the persisted facts=%q, want the placed target %q", renderedFromFacts, targetPath)
	}
	t.Logf("MEDIAINFO EVIDENCE admission TargetSet path (before the file exists)=%q", frozenTargetPath)
	t.Logf("MEDIAINFO EVIDENCE placed path (rendered at execution)=%q", targetPath)
	t.Logf("MEDIAINFO EVIDENCE template rendered from persisted facts=%q", renderedFromFacts)
}

func doJSON[T any](t *testing.T, handler http.Handler, method, path string, body any, headers map[string]string) (int, T) {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code >= http.StatusBadRequest {
		// Error bodies decode into T as an empty view; log the problem body so a
		// status assertion failure names the error code.
		t.Logf("%s %s -> %d: %s", method, path, recorder.Code, recorder.Body.String())
	}
	var result T
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode %s %s response (status %d): %v; body=%s", method, path, recorder.Code, err, recorder.Body.String())
	}
	return recorder.Code, result
}

type fixtureEndpoint struct{ Port int }

func realChainFixtureHost() string {
	if host := strings.TrimSpace(os.Getenv("BANSOKO_REAL_CHAIN_FIXTURE_HOST")); host != "" {
		return host
	}
	return "host.docker.internal"
}

func realChainFixtureURL(port int, path string) string {
	return fmt.Sprintf("http://%s:%d%s", realChainFixtureHost(), port, path)
}

func startSingleEpisodeFixture(t *testing.T, artifactName string, artifactBytes []byte) (*http.Client, fixtureEndpoint, []byte, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	artifactURL := realChainFixtureURL(port, "/artifact.mkv")
	torrent := singleFileTorrent(artifactName, artifactBytes, artifactURL)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			link := realChainFixtureURL(port, "/artifact.torrent")
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Real Chain Fixture</title><item><guid>single-episode-real-chain-stable-guid</guid><title>[RealGroup] Real Chain Show - 01 [1080p]</title><link>%s</link><pubDate>Sat, 11 Jul 2026 03:00:00 +0000</pubDate></item></channel></rss>`, link)
		case "/artifact.torrent":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(torrent)
		case "/artifact.mkv":
			w.Header().Set("Content-Type", "video/x-matroska")
			http.ServeContent(w, r, artifactName, time.Unix(0, 0), bytes.NewReader(artifactBytes))
		default:
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
	client := &http.Client{Timeout: 20 * time.Second, Transport: transport}
	return client, fixtureEndpoint{Port: port}, append([]byte(nil), torrent...), func() {
		_ = server.Shutdown(context.Background())
	}
}

func singleEpisodeDraft(feedURL string) generated.AnimeDraft {
	instanceLabel := "Real Chain Instance"
	return generated.AnimeDraft{
		InstanceLabel: &instanceLabel,
		Metadata:      generated.AnimeMetadataDraft{Bgm: nil, Tmdb: nil},
		Origin:        generated.SubscriptionOriginCustom,
		RssSources: []generated.RSSSourceDraft{{
			GroupName:  "RealGroup",
			Locator:    feedURL,
			SourceKind: generated.SourceKindCustomRss,
			Title:      "Real Chain Show",
		}},
		// The rules sections start absent: withDraftBusiness copies them from
		// the persisted Global rows, exactly as a new draft copies its preset.
		Rules: generated.RulesDraft{},
		Subgroups: generated.SubgroupsSection{
			Entries: []generated.SubgroupScoreEntry{{
				DisplayName: "Fixture", EffectiveSelected: true,
				GroupRef: "fixture", Provider: generated.ProviderKindMikan, Score: 50,
				SelectionMode: generated.SelectionModeAuto,
			}},
			MaxRss: 100,
		},
		Title: "Real Chain Show",
	}
}

type ownerCountSnapshot struct {
	anime        int
	subscription int
	source       int
	runtime      int
	release      int
	task         int
	artifact     int
	file         int
	work         int
	log          int
}

type ownerSnapshot struct {
	counts   ownerCountSnapshot
	versions string
}

func readOwnerCounts(ctx context.Context, db *sql.DB) (ownerCountSnapshot, error) {
	var counts ownerCountSnapshot
	queries := []*int{
		&counts.anime, &counts.subscription, &counts.source, &counts.runtime, &counts.release,
		&counts.task, &counts.artifact, &counts.file, &counts.work, &counts.log,
	}
	tables := []string{
		"managed_anime", "subscriptions", "rss_sources", "rss_source_runtime", "releases",
		"download_tasks", "artifacts", "file_operations", "work_items", "subscription_log_entries",
	}
	for index, table := range tables {
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(queries[index]); err != nil {
			return ownerCountSnapshot{}, err
		}
	}
	return counts, nil
}

func readOwnerSnapshot(ctx context.Context, db *sql.DB) (ownerSnapshot, error) {
	counts, err := readOwnerCounts(ctx, db)
	if err != nil {
		return ownerSnapshot{}, err
	}
	var versions string
	if err := db.QueryRowContext(ctx, `
SELECT concat_ws('|',
    COALESCE((SELECT string_agg(id || ':' || version::text, ',' ORDER BY id) FROM managed_anime), ''),
    COALESCE((SELECT string_agg(id || ':' || version::text, ',' ORDER BY id) FROM subscriptions), ''),
    COALESCE((SELECT string_agg(managed_anime_id || ':' || rules_version::text || ':' || subgroups_version::text, ',' ORDER BY managed_anime_id) FROM anime_business_configs), ''),
    COALESCE((SELECT string_agg(managed_anime_id || ':' || version::text, ',' ORDER BY managed_anime_id) FROM anime_metadata), ''),
    COALESCE((SELECT string_agg(id || ':' || version::text, ',' ORDER BY id) FROM rss_sources), ''),
    COALESCE((SELECT string_agg(rss_source_id || ':' || version::text, ',' ORDER BY rss_source_id) FROM rss_source_runtime), ''),
    COALESCE((SELECT string_agg(id || ':' || version::text, ',' ORDER BY id) FROM releases), ''),
    COALESCE((SELECT string_agg(id || ':' || version::text, ',' ORDER BY id) FROM download_tasks), ''),
    COALESCE((SELECT string_agg(id || ':' || version::text, ',' ORDER BY id) FROM artifacts), ''),
    COALESCE((SELECT string_agg(id || ':' || version::text, ',' ORDER BY id) FROM file_operations), ''),
    COALESCE((SELECT string_agg(id || ':' || version::text, ',' ORDER BY id) FROM connections), ''),
    COALESCE((SELECT string_agg(section_name || ':' || version::text, ',' ORDER BY section_name) FROM global_config_sections), '')
)`).Scan(&versions); err != nil {
		return ownerSnapshot{}, err
	}
	return ownerSnapshot{counts: counts, versions: versions}, nil
}

func assertOwnerCounts(t *testing.T, ctx context.Context, db *sql.DB, animeID, subscriptionID, sourceID string) {
	t.Helper()
	var totalAnime, totalSubscriptions, totalSources, totalRuntimes int
	if err := db.QueryRowContext(ctx, `
SELECT
    (SELECT COUNT(*) FROM managed_anime),
    (SELECT COUNT(*) FROM subscriptions),
    (SELECT COUNT(*) FROM rss_sources),
    (SELECT COUNT(*) FROM rss_source_runtime)`).Scan(&totalAnime, &totalSubscriptions, &totalSources, &totalRuntimes); err != nil {
		t.Fatal(err)
	}
	if totalAnime != 1 || totalSubscriptions != 1 || totalSources != 1 || totalRuntimes != 1 {
		t.Fatalf("expected exactly one anime/subscription/RSS source/runtime, got anime=%d subscriptions=%d sources=%d runtimes=%d", totalAnime, totalSubscriptions, totalSources, totalRuntimes)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id = $1`, animeID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("managed anime count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions WHERE id = $1 AND managed_anime_id = $2`, subscriptionID, animeID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("subscription count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_sources WHERE id = $1 AND subscription_id = $2`, sourceID, subscriptionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("RSS source count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("RSS runtime count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE owner_type = 'rss_source_runtime' AND owner_id = $1 AND kind = 'rss_refresh' AND state IN ('available','leased')`, sourceID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("initial RSS refresh live count=%d err=%v", count, err)
	}
}

func runWorkerUntilIdle(ctx context.Context, worker *runtimeworker.Worker, db *sql.DB, maxIterations int) error {
	if err := advanceQBSyncPollClock(ctx, db); err != nil {
		return err
	}
	for index := 0; index < maxIterations; index++ {
		handled, err := worker.RunOnce(ctx)
		if err != nil {
			return err
		}
		if !handled {
			return nil
		}
	}
	var live int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE state IN ('available','leased') AND available_at <= CURRENT_TIMESTAMP`).Scan(&live); err != nil {
		return err
	}
	if live != 0 {
		return fmt.Errorf("worker did not become idle after %d iterations: %d due live work items remain", maxIterations, live)
	}
	return nil
}

func containsMetadataMissing(values []generated.MetadataHoldMissing, want generated.MetadataHoldMissing) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertLogKinds(t *testing.T, logs []generated.SubscriptionLogEntry, required ...generated.SubscriptionLogKind) {
	t.Helper()
	seen := make(map[generated.SubscriptionLogKind]bool, len(logs))
	for _, entry := range logs {
		seen[entry.Kind] = true
	}
	for _, kind := range required {
		if !seen[kind] {
			t.Fatalf("workspace log is missing %q: %+v", kind, logs)
		}
	}
}

func assertNoDuplicateLiveWorkItems(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
	SELECT owner_type, owner_id, kind, COUNT(*)
	FROM work_items
	WHERE state IN ('available','leased')
	GROUP BY owner_type, owner_id, kind
	HAVING COUNT(*) > 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var ownerType, ownerID, kind string
		var count int
		if err := rows.Scan(&ownerType, &ownerID, &kind, &count); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("duplicate live work item owner=%s/%s kind=%s count=%d", ownerType, ownerID, kind, count)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func waitForArtifact(t *testing.T, path string, wantSize int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() == wantSize {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("qB artifact did not appear at %q with size %d within %s", path, wantSize, timeout)
}

func singleFileTorrent(name string, content []byte, webSeedURL string) []byte {
	pieceHash := sha1.Sum(content)
	info := []byte("d6:lengthi" + strconv.Itoa(len(content)) + "e4:name" + strconv.Itoa(len(name)) + ":" + name + "12:piece lengthi16384e6:pieces20:")
	info = append(info, pieceHash[:]...)
	info = append(info, 'e')
	meta := []byte("d4:info")
	meta = append(meta, info...)
	meta = append(meta, []byte("8:url-listl"+strconv.Itoa(len(webSeedURL))+":"+webSeedURL+"ee")...)
	return meta
}

// placementSinkForTest admits the verified video file operation into a
// placement using the frozen media fact per-file coverage, mirroring the
// production composition-root sink without importing package main.
type placementSinkForTest struct {
	placements *filemaintenance.PlacementService
	evidence   filemaintenance.FileOperationEvidenceSource
	mediaFacts filemaintenance.MediaFileFactsReader
}

func newPlacementSinkForTest(placements *filemaintenance.PlacementService, db *sql.DB) *placementSinkForTest {
	return &placementSinkForTest{
		placements: placements,
		evidence:   filemaintenance.NewFileOperationEvidenceReader(db),
		mediaFacts: facts.NewStoreWithRepository(mediapersistence.NewRepository(db)),
	}
}

func (a *placementSinkForTest) AdmitFromFileOperation(ctx context.Context, fileOperationID string) error {
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
	_, err = a.placements.AdmitFromFileOperation(ctx, fileOperationID, coverage, factsVersion)
	return err
}

// torrentFile is one physical file of the multi-file torrent fixture.
type torrentFile struct {
	Name    string
	Content []byte
}

// multiFileTorrent builds a valid multi-file bencoded torrent. The
// url-list is a single web-seed ROOT URL (not a per-file list): libtorrent
// composes each file URL as <root>/<info name>/<file path>, which is the
// layout the qBittorrent fixture serves. Piece length is 16 KiB and pieces are
// hashed over the concatenated file stream, exactly like qBittorrent.
func multiFileTorrent(dirName string, files []torrentFile, webSeedRoot string) []byte {
	var stream []byte
	for _, file := range files {
		stream = append(stream, file.Content...)
	}
	const pieceLength = 16384
	var pieces []byte
	for off := 0; off < len(stream); off += pieceLength {
		end := off + pieceLength
		if end > len(stream) {
			end = len(stream)
		}
		hash := sha1.Sum(stream[off:end])
		pieces = append(pieces, hash[:]...)
	}
	var b bytes.Buffer
	b.WriteString("d4:info")
	b.WriteString("d4:name" + strconv.Itoa(len(dirName)) + ":" + dirName)
	b.WriteString("12:piece lengthi" + strconv.Itoa(pieceLength) + "e")
	b.WriteString("6:pieces" + strconv.Itoa(len(pieces)) + ":")
	b.Write(pieces)
	b.WriteString("5:filesl")
	for _, file := range files {
		b.WriteString("d6:lengthi" + strconv.Itoa(len(file.Content)) + "e4:pathl" + strconv.Itoa(len(file.Name)) + ":" + file.Name + "ee")
	}
	b.WriteString("ee")
	b.WriteString("8:url-list" + strconv.Itoa(len(webSeedRoot)) + ":" + webSeedRoot)
	b.WriteString("e")
	return b.Bytes()
}

// startMultiFileFixture serves one RSS item (the given title) whose
// torrent contains every fixture file; files are downloadable at the
// web-seed layout http://<fixture-host>:<port>/M1 Multi Real Show/<name>.
func startMultiFileFixture(t *testing.T, title string, files []torrentFile) (*http.Client, fixtureEndpoint, []byte, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	rootURL := fmt.Sprintf("http://%s:%d", realChainFixtureHost(), port)
	torrent := multiFileTorrent("M1 Multi Real Show", files, rootURL)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			link := realChainFixtureURL(port, "/fixture.torrent")
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>M1 Multi Fixture</title><item><guid>m1-multi-stable-guid</guid><title>%s</title><link>%s</link><pubDate>Sat, 11 Jul 2026 03:00:00 +0000</pubDate></item></channel></rss>`, title, link)
		case "/fixture.torrent":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(torrent)
		default:
			for _, file := range files {
				if r.URL.Path == "/M1 Multi Real Show/"+url.PathEscape(file.Name) || r.URL.Path == "/M1 Multi Real Show/"+file.Name {
					http.ServeContent(w, r, file.Name, time.Unix(0, 0), bytes.NewReader(file.Content))
					return
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
	client := &http.Client{Timeout: 20 * time.Second, Transport: transport}
	return client, fixtureEndpoint{Port: port}, append([]byte(nil), torrent...), func() {
		_ = server.Shutdown(context.Background())
	}
}

// letterRunTag encodes a run discriminator in letters only, so it can sit in
// a media file name without looking like an episode, season or year.
func letterRunTag(n int64) string {
	if n < 0 {
		n = -n
	}
	tag := make([]byte, 0, 14)
	for n > 0 {
		tag = append(tag, byte('a'+n%26))
		n /= 26
	}
	return "run" + string(tag)
}
