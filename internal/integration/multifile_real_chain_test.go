package integration

import (
	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	accessapplication "bansoko/internal/accesscontrol/application"
	acquisitionapplication "bansoko/internal/acquisition/application"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	artifactadapter "bansoko/internal/artifact/adapter"
	artifactapplication "bansoko/internal/artifact/application"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/filemaintenance"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	"bansoko/internal/library/adapter/filesystem"
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

// TestMultiFileRealPostgresChain is the natural multi-file media chain:
// RSS -> evaluation -> qB -> artifact inspection -> file effect. The torrent
// carries a primary video, a bindable subtitle, and an extra file excluded by
// the instance file filter. It proves: one frozen EpisodeIdentity per video,
// deterministic subtitle binding with the video basename, the excluded file
// never entering the effect chain, and exactly one Placement for the video.
func TestMultiFileRealPostgresChain(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	username := strings.TrimSpace(os.Getenv("BANSOKO_QB_USERNAME"))
	password := strings.TrimSpace(os.Getenv("BANSOKO_QB_PASSWORD"))
	if endpoint == "" || username == "" || password == "" {
		t.Skip("BANSOKO_QB_ENDPOINT/USERNAME/PASSWORD are required for the real-effect chain")
	}
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()

	hostDownloadRoot := filepath.Join("..", "..", ".local-artifacts", "m1-multi-qb", "downloads")
	if harnessRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT")); harnessRoot != "" {
		hostDownloadRoot = harnessRoot
	}
	if err := os.MkdirAll(hostDownloadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()

	videoContent := []byte("multi-file video payload " + time.Now().UTC().Format(time.RFC3339Nano))
	subtitleContent := []byte("Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,M1 multi subtitle")
	extraContent := []byte("extra audio that must be filtered out")
	videoName := "M1 Multi Real Show - 01.mkv"
	subtitleName := "M1 Multi Real Show - 01.ass"
	extraName := "M1 Multi Real Show - 01.extra.mp3"
	files := []torrentFile{
		{Name: videoName, Content: videoContent},
		{Name: subtitleName, Content: subtitleContent},
		{Name: extraName, Content: extraContent},
	}
	videoDigest := sha256.Sum256(videoContent)

	fixtureClient, fixtureURL, torrentBytes, closeFixture := startMultiFileFixture(t, "[RealGroup] M1 Multi Real Show - 01 [1080p][CHS]", files)
	t.Cleanup(closeFixture)
	port := fixtureURL.Port
	feedURL := realChainFixtureURL(port, "/feed.xml")

	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte("test-secret-encryption-key-0123456789abcdef"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	configService := configurationapplication.NewHTTPService(configurationapplication.NewService(db, secrets))
	connectionID := "m1-multi-qb-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
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
		BaseUrl: endpoint, DownloadRoot: downloadRoot, DownloaderRoot: &hostDownloadRoot,
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
	configuredQB, err := connection.AsQBittorrentConnection()
	if err != nil || !configuredQB.Enabled {
		t.Fatalf("typed qB connection was not configured: %+v err=%v", connection, err)
	}
	if _, err := (qbittorrent.Client{Endpoint: endpoint, Username: username, Password: password, Timeout: 20 * time.Second}).Health(ctx); err != nil {
		t.Fatalf("qB health check failed: %v", err)
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
	ownershipService.SetOffsetMigrationAdmission(filemaintenance.NewOffsetMigrationAdmissionService())
	accessService := accessapplication.NewServiceWithSessionSecret(db, []byte("m1-multi-session-secret-32-bytes"))
	rssFetcher := acquisitionapplication.HTTPRSSFetcher{
		Client:            fixtureClient,
		AllowPrivateHosts: []string{realChainFixtureHost()},
		TorrentReader: acquisitionapplication.HTTPTorrentMetadataReader{
			Client: fixtureClient, AllowPrivateHosts: []string{realChainFixtureHost()},
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
		DB: db, Access: accessService, Draft: draftService, Ownership: ownershipService, ReleaseRetry: rssService,
		Environment: "development", SkipAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpHandler := httpServer.Handler()
	// Wire the same target admission the composition root does: it freezes
	// the Release's Vars evaluation into the DownloadTask policy that prepare
	// renders multi-file targets from.
	rssService.SetTargetStore(acquisitiontarget.NewStore(targetpersistence.NewRepository(db)))
	rssService.SetTargetAcquisition(acquisitiontarget.FilesystemProbe{})
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
	// The planning stage creates the typed Plan; no downstream legacy Prepare sink.
	// The placement coverage reads the artifact owner's frozen per-file facts,
	// so the same media-facts owner the composition root wires must be set here.
	artifactService.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	placementService := filemaintenance.NewPlacementService(db, filemaintenance.NewFileOperationEvidenceReader(db), nil)
	placementService.SetActivityWriter(observability)
	fileService.SetPlacementSink(newPlacementSinkForTest(placementService, db))

	runtime := runtimeworker.NewWorker(store, "m1-multi-worker", time.Minute)
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

	// Default (empty) file rules: the video and its prefix-bound subtitle enter
	// the effect chain; the .extra.mp3 is not a video or subtitle and is never
	// processed.
	draft := singleEpisodeDraft(feedURL)
	draft.Title = "M1 Multi Real Show"
	draft.RssSources[0].Title = "M1 Multi Real Show"

	key := "m1-multi-subscribe-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	// Candidate save -> subscribe through the same HTTP contract as M1.
	draft = withDraftBusiness(t, db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" {
		t.Fatalf("candidate save failed: status=%d candidate=%+v", saveStatus, candidate)
	}
	subscribeURL := "/api/v1/anime-config-candidates/" + candidate.Id + "/subscribe"
	var result generated.SubscribeResult
	if candidate.CurrentRevision == nil {
		t.Fatalf("saved candidate carries no immutable revision: %+v", candidate)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, httpHandler, http.MethodPost, subscribeURL, generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": key, "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" || result.Subscription.Id == "" || len(result.RssSources) != 1 {
		t.Fatalf("subscribe failed: status=%d result=%+v", subscribeStatus, result)
	}

	if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
		t.Fatal(err)
	}
	// Complete the sole metadata hold, then let the chain run.
	if _, err := ownershipService.PatchAnimeMetadata(ctx, result.ManagedAnime.Id, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "m1-multi-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "m1-multi-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	// The default TV directory names the providers' year observation, so the
	// fixture records it before the release is accepted.
	observeBoundWorkYear(t, ctx, db, result.ManagedAnime.Id, realChainObservedYear)
	if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
		t.Fatal(err)
	}
	var submittedVerdict string
	if err := db.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE rss_source_id = $1`, result.RssSources[0].Id).Scan(&submittedVerdict); err != nil {
		t.Fatal(err)
	}
	if submittedVerdict != string(generated.ReleaseVerdictSubmitted) {
		var failReason, failDetail string
		_ = db.QueryRowContext(ctx, `SELECT reason_code, COALESCE(reason_detail, '') FROM releases WHERE rss_source_id = $1`, result.RssSources[0].Id).Scan(&failReason, &failDetail)
		var inputCount int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_torrent_inputs`).Scan(&inputCount)
		t.Fatalf("metadata completion did not submit the release: %q reason=%q detail=%q torrent_inputs=%d", submittedVerdict, failReason, failDetail, inputCount)
	}
	var taskID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM download_tasks LIMIT 1`).Scan(&taskID); err != nil {
		t.Fatal(err)
	}

	// Wait for the qB download of every fixture file, then drive sync/artifact.
	waitForArtifact(t, filepath.Join(hostDownloadRoot, "M1 Multi Real Show", videoName), int64(len(videoContent)), 60*time.Second)
	waitForArtifact(t, filepath.Join(hostDownloadRoot, "M1 Multi Real Show", subtitleName), int64(len(subtitleContent)), 30*time.Second)
	waitForArtifact(t, filepath.Join(hostDownloadRoot, "M1 Multi Real Show", extraName), int64(len(extraContent)), 30*time.Second)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := downloadService.DispatchQBSync(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
			t.Fatal(err)
		}
		var artifactState string
		if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT state FROM artifacts WHERE download_task_id = $1), '')`, taskID).Scan(&artifactState); err != nil {
			t.Fatal(err)
		}
		if artifactState == string(generated.ArtifactStateVerified) {
			break
		}
		time.Sleep(time.Second)
	}
	var artifactID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE download_task_id = $1`, taskID).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}

	// Artifact inspection must have resolved one frozen identity per video and
	// retained every physical file fact (video + subtitle + filtered extra).
	var artifactFiles int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_files WHERE artifact_id = $1`, artifactID).Scan(&artifactFiles); err != nil {
		t.Fatal(err)
	}
	if artifactFiles != 3 {
		t.Fatalf("artifact must carry 3 physical files (video+subtitle+extra), got %d", artifactFiles)
	}

	// File effect: exactly the video and the subtitle run; the extra is
	// content-admission excluded and never enters the effect chain. A subtitle
	// claimed before its video completes releases its slot and retries later
	// (subtitle_video_pending), so drive the worker until no operation of this
	// artifact is still pending.
	effectDeadline := time.Now().Add(90 * time.Second)
	for {
		if err := runWorkerUntilIdle(ctx, runtime, db, 32); err != nil {
			t.Fatal(err)
		}
		var pending int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_operations WHERE artifact_id = $1 AND state = 'pending'`, artifactID).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		if time.Now().After(effectDeadline) {
			t.Fatalf("file operations still pending after the subtitle dependency retry window\n%s", describeArtifactEffects(ctx, db, artifactID))
		}
		time.Sleep(time.Second)
	}
	var videoOpID, videoOpState, videoTarget string
	var subtitleOpID, subtitleOpState, subtitleTarget string
	if err := db.QueryRowContext(ctx, `
SELECT id, state, target_path FROM file_operations
WHERE artifact_id = $1 AND target_path LIKE '%.mkv'`, artifactID).Scan(&videoOpID, &videoOpState, &videoTarget); err != nil {
		t.Fatalf("video file operation: %v\n%s", err, describeArtifactEffects(ctx, db, artifactID))
	}
	if err := db.QueryRowContext(ctx, `
SELECT id, state, target_path FROM file_operations
WHERE artifact_id = $1 AND target_path LIKE '%.ass'`, artifactID).Scan(&subtitleOpID, &subtitleOpState, &subtitleTarget); err != nil {
		t.Fatalf("subtitle file operation: %v\n%s", err, describeArtifactEffects(ctx, db, artifactID))
	}
	if videoOpState != string(generated.FileOperationStateCompleted) || subtitleOpState != string(generated.FileOperationStateCompleted) {
		t.Fatalf("video/subtitle operations must complete: video=%q subtitle=%q", videoOpState, subtitleOpState)
	}
	var extraOps int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_operations WHERE artifact_id = $1 AND target_path LIKE '%.mp3'`, artifactID).Scan(&extraOps); err != nil {
		t.Fatal(err)
	}
	if extraOps != 0 {
		t.Fatalf("filtered extra file must not enter the effect chain, got %d operations", extraOps)
	}
	// Subtitle target basename must be locked to the bound video: the video
	// stem is a prefix of the subtitle stem (language suffix + subtitle
	// extension follow).
	videoStem := strings.TrimSuffix(filepath.Base(videoTarget), filepath.Ext(videoTarget))
	subtitleBase := filepath.Base(subtitleTarget)
	if !strings.HasPrefix(subtitleBase, videoStem) {
		t.Fatalf("subtitle must bind to the video basename: subtitle=%q video=%q", subtitleTarget, videoTarget)
	}
	// Target bytes verified.
	videoTargetBytes, err := os.ReadFile(videoTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(videoTargetBytes, videoContent) {
		t.Fatalf("video target bytes differ from source")
	}
	subtitleTargetBytes, err := os.ReadFile(subtitleTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(subtitleTargetBytes, subtitleContent) {
		t.Fatalf("subtitle target bytes differ from source")
	}

	// Exactly one Placement for the video; the subtitle is a bound companion,
	// not a placement.
	var placements int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1 AND verification_state = 'verified' AND state = 'active'`, result.ManagedAnime.Id).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if placements != 1 {
		t.Fatalf("expected exactly one verified active Placement for the video, got %d", placements)
	}
	var placementPath string
	if err := db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND verification_state = 'verified' AND state = 'active'`, result.ManagedAnime.Id).Scan(&placementPath); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(placementPath, ".mkv") {
		t.Fatalf("placement must point at the primary video, got %q", placementPath)
	}
	var placementFingerprintJSON string
	if err := db.QueryRowContext(ctx, `SELECT source_fingerprint FROM placements WHERE managed_anime_id = $1`, result.ManagedAnime.Id).Scan(&placementFingerprintJSON); err != nil {
		t.Fatal(err)
	}
	var placementFingerprint struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(placementFingerprintJSON), &placementFingerprint); err != nil || placementFingerprint.Value == "" {
		t.Fatalf("placement fingerprint must be a typed JSON fingerprint, got %q", placementFingerprintJSON)
	}
	if placementFingerprint.Value != hex.EncodeToString(videoDigest[:]) {
		t.Fatalf("placement fingerprint must match the video digest: got %q", placementFingerprint.Value)
	}

	// The bound subtitle is registered as a companion of the video
	// placement at its delivered path, with its original extension.
	var placementID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, result.ManagedAnime.Id).Scan(&placementID); err != nil {
		t.Fatal(err)
	}
	assertSubtitleCompanion := func(wantRegistration string) {
		t.Helper()
		companions, err := placementService.ListCompanions(ctx, placementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(companions) != 1 {
			t.Fatalf("expected one companion for the video placement, got %+v", companions)
		}
		c := companions[0]
		subtitleDigest := sha256.Sum256(subtitleContent)
		if c.State != filemaintenance.CompanionStateActive || c.MediaKind != "subtitle" || c.OriginalExtension != "ass" ||
			c.CurrentPath != subtitleTarget || c.SourceRef != subtitleOpID || c.Registration != wantRegistration ||
			c.SizeBytes != int64(len(subtitleContent)) || c.Fingerprint != hex.EncodeToString(subtitleDigest[:]) {
			t.Fatalf("subtitle companion mismatch: %+v (target=%q op=%q)", c, subtitleTarget, subtitleOpID)
		}
		if got := filemaintenance.CompanionPath(placementPath, c.Suffix, c.OriginalExtension); got != subtitleTarget {
			t.Fatalf("companion path must derive from the video placement: got %q want %q", got, subtitleTarget)
		}
	}
	assertSubtitleCompanion("intake")

	// Torrent input was frozen with the exact fixture digest.
	var storedDigest string
	if err := db.QueryRowContext(ctx, `SELECT payload_digest FROM release_torrent_inputs ORDER BY created_at DESC LIMIT 1`).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(torrentBytes)
	if storedDigest != "sha256:"+hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("stored torrent digest=%q want sha256:%s", storedDigest, hex.EncodeToString(wantDigest[:]))
	}
}

// describeArtifactEffects renders the artifact's plans, file operations and
// files so a missing effect names its actual state instead of only
// "no rows".
func describeArtifactEffects(ctx context.Context, db *sql.DB, artifactID string) string {
	var b strings.Builder
	dump := func(title, query string) {
		fmt.Fprintf(&b, "%s:\n", title)
		rows, err := db.QueryContext(ctx, query, artifactID)
		if err != nil {
			fmt.Fprintf(&b, "  query failed: %v\n", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				fmt.Fprintf(&b, "  scan failed: %v\n", err)
				return
			}
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	dump("plans", `SELECT row_to_json(p)::text FROM postprocessing_plans p WHERE artifact_id = $1`)
	dump("file operations", `SELECT row_to_json(o)::text FROM file_operations o WHERE artifact_id = $1`)
	dump("artifact files", `SELECT concat_ws(' ', file_ref, relative_path, media_kind, subtitle_binding_state) FROM artifact_files WHERE artifact_id = $1`)
	return b.String()
}
