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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	accessapplication "bansoko/internal/accesscontrol/application"
	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	animeapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/apperrors"
	artifactadapter "bansoko/internal/artifact/adapter"
	artifactapplication "bansoko/internal/artifact/application"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filemaintenance"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/metadata/matcher"
	observabilityapplication "bansoko/internal/observability/application"
	parserpolicy "bansoko/internal/parserpolicy"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/rss/application"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

const movieChainTitle = "[RealGroup] Real Chain Movie 2024 [1080p]"

// movieTargetBranch reports whether a rendered target is the movie naming
// branch of the fixture instance title: the file lands in the movie kind
// directory under "<title> <year>" (no season segment, no episode token) and
// keeps its own file name.
func movieTargetBranch(rendered, artifactName string) bool {
	if rendered == "" {
		return false
	}
	dir, base := filepath.Split(rendered)
	cleaned := filepath.Clean(dir)
	if filepath.Base(cleaned) != fmt.Sprintf("Real Chain Movie (%d)", realChainObservedYear) {
		return false
	}
	if filepath.Base(filepath.Dir(cleaned)) != "Movie" {
		return false
	}
	return base == artifactName
}

// startMovieFixture serves one RSS item whose title carries a year but no
// episode, plus the single-file torrent and web seed that qBittorrent will
// fetch. The artifact name deliberately keeps the year so a regression that
// re-opens file-name parsing would read 2024 as an episode.
func startMovieFixture(t *testing.T, artifactName string, artifactBytes []byte) (*http.Client, fixtureEndpoint, []byte, func()) {
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
			_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Real Chain Movie Fixture</title><item><guid>real-chain-movie-stable-guid</guid><title>%s</title><link>%s</link><pubDate>Sat, 11 Jul 2026 03:00:00 +0000</pubDate></item></channel></rss>`, movieChainTitle, link)
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

// movieChainServices is the wired composition root one movie chain runs on.
type movieChainServices struct {
	db           *sql.DB
	runtime      *runtimeworker.Worker
	draft        *draftapplication.Service
	ownership    *ownershipapplication.Service
	rss          *application.Service
	download     *downloadapplication.Service
	artifact     *artifactapplication.Service
	fileService  *fileoperationapplication.Service
	httpHandler  http.Handler
	hostDownload string
}

// TestMovieBindingRealChainPostgres drives the natural movie chain end to end
// on a real PostgreSQL database with a real qBittorrent: RSS evaluation →
// frozen target/naming → qB submit → artifact verification → file operation →
// placement. A movie metadata binding must resolve the canonical movie:0
// identity at every hop (never an episode from the title year or the file name),
// and rebinding the instance metadata to TV after submission must not move the
// frozen snapshot, the target, the file effect, or the placement coverage.
func TestMovieBindingRealChainPostgres(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	username := strings.TrimSpace(os.Getenv("BANSOKO_QB_USERNAME"))
	password := strings.TrimSpace(os.Getenv("BANSOKO_QB_PASSWORD"))
	if endpoint == "" || username == "" || password == "" {
		t.Skip("BANSOKO_QB_ENDPOINT/USERNAME/PASSWORD are required for the real-effect chain")
	}
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()

	hostDownloadRoot := filepath.Join("..", "..", ".local-artifacts", "m1-movie-qb", "downloads")
	if harnessRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT")); harnessRoot != "" {
		hostDownloadRoot = harnessRoot
	}
	if err := os.MkdirAll(hostDownloadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()
	artifactName := fmt.Sprintf("Real Chain Movie 2024 [1080p] - %d.mkv", time.Now().UnixNano())
	artifactBytes := []byte("real chain movie artifact " + time.Now().UTC().Format(time.RFC3339Nano))

	fixtureClient, fixtureURL, _, closeFixture := startMovieFixture(t, artifactName, artifactBytes)
	t.Cleanup(closeFixture)
	feedURL := realChainFixtureURL(fixtureURL.Port, "/feed.xml")

	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte("test-secret-encryption-key-0123456789abcdef"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	configService := configurationapplication.NewHTTPService(configurationapplication.NewService(db, secrets))
	connectionID := "m1-movie-qb-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	downloadRoot := hostDownloadRoot
	if harnessRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT")); harnessRoot != "" {
		hostDownloadRoot = harnessRoot
		downloadRoot = "/downloads"
	}
	var connectionInput generated.ConnectionInput
	var secret generated.ConnectionSecret
	if err := secret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: password}); err != nil {
		t.Fatal(err)
	}
	if err := connectionInput.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		BaseUrl: endpoint, DownloadRoot: downloadRoot, DownloaderRoot: &hostDownloadRoot,
		Enabled: true, Kind: generated.QBittorrentConnectionInputKindQbittorrent,
		Name: connectionID, Secret: secret, Username: username,
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
	if _, err := putGeneratedConnection(t, configService, ctx, connectionID, connVersion, connectionInput); err != nil {
		t.Fatal(err)
	}
	if _, err := configService.GetQBittorrentRuntimeConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := (qbittorrent.Client{Endpoint: endpoint, Username: username, Password: password, Timeout: 20 * time.Second}).Health(ctx); err != nil {
		t.Fatalf("qB setup health check failed: %v", err)
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
	// Mirror the production composition root: candidate activation must write
	// the revision-frozen instance parser policy, or the automatic refresh and
	// evaluate path fails closed with parser_policy_required.
	parserPolicies := parserpolicy.NewService(db)
	draftService.SetFrozenParserPolicy(parserPolicies)
	ownershipService, err := ownershipapplication.NewService(db, ownershipapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	ownershipService.SetActivityWriter(observability)
	ownershipService.SetOneShotMarkerRepository(animepersistence.NewRepository(db))
	ownershipService.SetMetadataMatcher(matcher.NewMatcher(matcher.NewBangumiDataIndex(nil), nil, nil, nil))
	ownershipService.SetObservabilityRecorder(observability)
	ownershipService.SetOffsetMigrationAdmission(filemaintenance.NewOffsetMigrationAdmissionService())
	accessService := accessapplication.NewServiceWithSessionSecret(db, []byte("m1-movie-real-session-secret-32-bytes"))
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
	rssService.SetTargetStore(acquisitiontarget.NewStore(targetpersistence.NewRepository(db)))
	rssService.SetTargetAcquisition(acquisitiontarget.FilesystemProbe{})
	httpServer, err := httpapi.NewServer(httpapi.Dependencies{
		DB: db, Access: accessService, Draft: draftService, Ownership: ownershipService, ReleaseRetry: rssService,
		Environment: "development", SkipAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpHandler := httpServer.Handler()
	configureRealChainNaming(t, ctx, db, targetRoot)
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
	artifactService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	fileService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	// The planning stage creates the typed Plan; no downstream legacy Prepare sink.
	placementService := filemaintenance.NewPlacementService(db, filemaintenance.NewFileOperationEvidenceReader(db), nil)
	placementService.SetActivityWriter(observability)
	fileService.SetPlacementSink(newPlacementSinkForTest(placementService, db))

	runtime := runtimeworker.NewWorker(store, "m1-movie-real-worker", time.Minute)
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

	draft := singleEpisodeDraft(feedURL)
	draft.Title = "Real Chain Movie"
	draft.RssSources[0].Title = "Real Chain Movie"
	draft.Subgroups.Entries[0].GroupRef = "fixture"
	draft = withDraftBusiness(t, db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" || candidate.CurrentRevision == nil {
		t.Fatalf("movie candidate save failed: status=%d candidate=%+v", saveStatus, candidate)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, httpHandler, http.MethodPost,
		"/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe",
		generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)},
		map[string]string{"Idempotency-Key": "m1-movie-subscribe-1", "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" || len(result.RssSources) != 1 {
		t.Fatalf("movie subscribe failed: status=%d result=%+v", subscribeStatus, result)
	}
	animeID := result.ManagedAnime.Id
	sourceID := result.RssSources[0].Id

	// The metadata hold is released with a movie binding: no episode is implied
	// by the binding, and the release title carries only a year.
	if _, err := ownershipService.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "m1-movie-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "m1-movie-tmdb", MediaKind: generated.TMDBMediaKindMovie, Season: 0},
	}); err != nil {
		t.Fatal(err)
	}
	// The movie directory is "<title> <year>", and the year is the providers'
	// own observation of the bound work — deliberately not the year the release
	// title carries, so the assertion below proves where the value came from.
	observeBoundWorkYear(t, ctx, db, animeID, realChainObservedYear)
	if err := runWorkerUntilIdle(ctx, runtime, db, 64); err != nil {
		t.Fatal(err)
	}

	var verdict string
	var taskID string
	if err := db.QueryRowContext(ctx, `SELECT verdict, COALESCE(download_task_id, '') FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&verdict, &taskID); err != nil {
		t.Fatal(err)
	}
	if verdict != string(generated.ReleaseVerdictSubmitted) || taskID == "" {
		t.Fatalf("movie release did not submit: verdict=%q task=%q", verdict, taskID)
	}

	// Hop 1 — the frozen Release identity and media snapshot are the canonical
	// movie identity, and the pre-download target rendered from it uses the movie
	// naming branch (no season directory, no episode token, no year-as-episode).
	var identityRaw, releaseFactsRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT release_identity, release_facts FROM media_fact_snapshots WHERE release_id IN (
    SELECT id FROM releases WHERE rss_source_id = $1
) ORDER BY created_at DESC, id DESC LIMIT 1`, sourceID).Scan(&identityRaw, &releaseFactsRaw); err != nil {
		t.Fatal(err)
	}
	identity, err := facts.UnmarshalIdentity(identityRaw)
	if err != nil {
		t.Fatalf("decode frozen movie identity: %v", err)
	}
	if identity.State != episodeidentity.StateResolved || identity.Kind != episodeidentity.KindMovie ||
		identity.Episode == nil || string(*identity.Episode) != "0" ||
		identity.CoverageKey == nil || *identity.CoverageKey != "movie:0" {
		t.Fatalf("frozen release identity = %+v, want the canonical movie:0 identity", identity)
	}
	var releaseFacts facts.ReleaseFacts
	if err := json.Unmarshal(releaseFactsRaw, &releaseFacts); err != nil {
		t.Fatalf("decode frozen release facts: %v", err)
	}
	if releaseFacts.MediaKind != "movie" {
		t.Fatalf("frozen release facts media_kind = %q, want movie", releaseFacts.MediaKind)
	}
	if releaseFacts.ParserProvenance == nil || releaseFacts.ParserProvenance.MediaKind != "movie" {
		t.Fatalf("frozen release provenance lost the media kind: %+v", releaseFacts.ParserProvenance)
	}

	var frozenTargetPath string
	var configSnapshotRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT target_path, config_snapshot FROM acquisition_targets
WHERE release_id IN (SELECT id FROM releases WHERE rss_source_id = $1) AND media_kind = 'video'`, sourceID).
		Scan(&frozenTargetPath, &configSnapshotRaw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frozenTargetPath, "Season 01") || strings.Contains(frozenTargetPath, " - E") {
		t.Fatalf("frozen movie target %q used the TV naming branch", frozenTargetPath)
	}
	if !movieTargetBranch(frozenTargetPath, artifactName) {
		t.Fatalf("frozen movie target %q is not the movie rendering of the instance title", frozenTargetPath)
	}
	var targetSnapshot struct {
		Parser *struct {
			MediaKind string `json:"media_kind"`
		} `json:"parser"`
	}
	if err := json.Unmarshal(configSnapshotRaw, &targetSnapshot); err != nil {
		t.Fatalf("decode frozen target config snapshot: %v", err)
	}
	if targetSnapshot.Parser == nil || targetSnapshot.Parser.MediaKind != "movie" {
		t.Fatalf("frozen target parser provenance = %+v, want media_kind movie", targetSnapshot.Parser)
	}

	// Hop 2 — qB downloads the bytes and the artifact freezes the same binding.
	waitForArtifact(t, filepath.Join(hostDownloadRoot, artifactName), int64(len(artifactBytes)), 60*time.Second)
	deadline := time.Now().Add(90 * time.Second)
	var taskState, artifactID, artifactState string
	for time.Now().Before(deadline) {
		if _, err := downloadService.DispatchQBSync(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runWorkerUntilIdle(ctx, runtime, db, 64); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT state FROM download_tasks WHERE id = $1`, taskID).Scan(&taskState); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT id FROM artifacts WHERE download_task_id = $1), ''), COALESCE((SELECT state FROM artifacts WHERE download_task_id = $1), '')`, taskID).Scan(&artifactID, &artifactState); err != nil {
			t.Fatal(err)
		}
		if taskState == string(generated.DownloadTaskStateCompleted) && artifactState == string(generated.ArtifactStateVerified) {
			break
		}
		time.Sleep(time.Second)
	}
	if taskState != string(generated.DownloadTaskStateCompleted) || artifactState != string(generated.ArtifactStateVerified) {
		t.Fatalf("movie download/artifact did not complete: task=%q artifact=%q", taskState, artifactState)
	}

	var effectiveRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT effective_facts FROM media_fact_files WHERE snapshot_id = (
    SELECT id FROM media_fact_snapshots WHERE release_id IN (
        SELECT id FROM releases WHERE rss_source_id = $1
    ) ORDER BY created_at DESC, id DESC LIMIT 1
)`, sourceID).Scan(&effectiveRaw); err != nil {
		t.Fatal(err)
	}
	var effective facts.EffectiveFileFacts
	if err := json.Unmarshal(effectiveRaw, &effective); err != nil {
		t.Fatalf("decode frozen per-file facts: %v", err)
	}
	if effective.Identity.Kind != episodeidentity.KindMovie || effective.Identity.CoverageKey == nil || *effective.Identity.CoverageKey != "movie:0" {
		t.Fatalf("frozen per-file identity = %+v, want the canonical movie:0 identity (the file name year must never become an episode)", effective.Identity)
	}

	// Hop 3 — the file operation renders the movie naming branch and the
	// placement admits exactly the frozen movie coverage.
	fileOperationTarget, fileOperationState := "", ""
	var releaseID string
	if err := db.QueryRowContext(ctx, `
SELECT fo.target_path, fo.state, r.id
FROM file_operations fo
JOIN artifacts a ON a.id = fo.artifact_id
JOIN download_tasks dt ON dt.id = a.download_task_id
JOIN releases r ON r.id = dt.release_id
WHERE dt.id = $1`, taskID).Scan(&fileOperationTarget, &fileOperationState, &releaseID); err != nil {
		t.Fatal(err)
	}
	if fileOperationState != string(generated.FileOperationStateCompleted) {
		t.Fatalf("movie file operation state = %q, want completed", fileOperationState)
	}
	if fileOperationTarget != frozenTargetPath {
		t.Fatalf("movie file operation target = %q, want the frozen movie target %q", fileOperationTarget, frozenTargetPath)
	}
	if strings.Contains(fileOperationTarget, "Season 01") || strings.Contains(fileOperationTarget, " - E") {
		t.Fatalf("movie file operation target %q used the TV naming branch", fileOperationTarget)
	}
	written, err := os.ReadFile(fileOperationTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, artifactBytes) {
		t.Fatalf("movie target bytes differ from the downloaded artifact")
	}

	var placementCoverage string
	var placementFactsVersion string
	if err := db.QueryRowContext(ctx, `
SELECT c.observation_key, p.facts_version
FROM placements p
JOIN placement_coverage c ON c.placement_id = p.id
WHERE p.managed_anime_id = $1`, animeID).Scan(&placementCoverage, &placementFactsVersion); err != nil {
		t.Fatal(err)
	}
	if placementCoverage != "movie:0" {
		t.Fatalf("movie placement coverage = %q, want movie:0", placementCoverage)
	}
	if placementFactsVersion == "unknown" {
		t.Fatalf("movie placement was admitted as an unknown file: facts_version=%q", placementFactsVersion)
	}

	// The frozen evidence is authoritative: rebinding the instance metadata to
	// TV after submission must not move any hop of the chain. A rebinding that
	// would backfill the kind to tv is refused while the movie file is in the
	// library; the owner kind stays movie when the patch keeps it.
	var metadataVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&metadataVersion); err != nil {
		t.Fatal(err)
	}
	_, err = ownershipService.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: metadataVersion,
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "m1-movie-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 3},
	})
	var blocked *apperrors.Business
	if !errors.As(err, &blocked) || blocked.Code != animeapplication.IdentityKindChangeBlockedCode {
		t.Fatalf("rebinding to tv without the owner kind: err = %v, want %s", err, animeapplication.IdentityKindChangeBlockedCode)
	}
	keepMovie := generated.AnimeMetadataPatchMediaKindOverrideMovie
	if _, err := ownershipService.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion:   metadataVersion,
		Tmdb:              &generated.NullableTMDBBinding{WorkId: "m1-movie-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 3},
		MediaKindOverride: &keepMovie,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkerUntilIdle(ctx, runtime, db, 64); err != nil {
		t.Fatal(err)
	}

	var afterIdentityRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT release_identity FROM media_fact_snapshots WHERE release_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, releaseID).Scan(&afterIdentityRaw); err != nil {
		t.Fatal(err)
	}
	afterIdentity, err := facts.UnmarshalIdentity(afterIdentityRaw)
	if err != nil {
		t.Fatalf("decode frozen identity after the metadata rebinding: %v", err)
	}
	if afterIdentity.Kind != episodeidentity.KindMovie || afterIdentity.CoverageKey == nil || *afterIdentity.CoverageKey != "movie:0" {
		t.Fatalf("frozen identity drifted after the metadata rebinding: %+v", afterIdentity)
	}
	var afterTarget string
	if err := db.QueryRowContext(ctx, `
SELECT target_path FROM acquisition_targets WHERE release_id = $1 AND media_kind = 'video'`, releaseID).Scan(&afterTarget); err != nil {
		t.Fatal(err)
	}
	if afterTarget != frozenTargetPath {
		t.Fatalf("frozen target drifted after the metadata rebinding: %q -> %q", frozenTargetPath, afterTarget)
	}
	var afterFileTarget string
	if err := db.QueryRowContext(ctx, `
SELECT target_path FROM file_operations WHERE target_path = $1`, fileOperationTarget).Scan(&afterFileTarget); err != nil {
		t.Fatal(err)
	}
	if afterFileTarget != fileOperationTarget {
		t.Fatalf("file operation target drifted: %q -> %q", fileOperationTarget, afterFileTarget)
	}
	var afterCoverage string
	if err := db.QueryRowContext(ctx, `
SELECT c.observation_key FROM placements p
JOIN placement_coverage c ON c.placement_id = p.id
WHERE p.managed_anime_id = $1`, animeID).Scan(&afterCoverage); err != nil {
		t.Fatal(err)
	}
	if afterCoverage != "movie:0" {
		t.Fatalf("placement coverage drifted after the metadata rebinding: %q", afterCoverage)
	}
}
