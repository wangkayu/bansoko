package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acquisitiontorrent "bansoko/internal/acquisition/torrent"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// TestOneShotMovieQBRealChain drives the finite one-shot command end to end on
// a real PostgreSQL database with a real qBittorrent and a real filesystem
// target root. A movie revision must resolve the canonical movie:0 identity
// before the target/naming plan runs, so the artifact lands on the movie naming
// branch and the Release carries the canonical scalar — never an episode taken
// from the year or the episode-like token in the title/file name.
func TestOneShotMovieQBRealChain(t *testing.T) {
	artifactName := "One Shot Real Movie 2024 [1080p].mkv"
	artifactBytes := []byte("one-shot movie artifact " + time.Now().UTC().Format(time.RFC3339Nano))
	fixtureClient, fixtureURL, torrentBytes, closeFixture := startSingleEpisodeFixture(t, artifactName, artifactBytes)
	t.Cleanup(closeFixture)

	svc := newRealChainServices(t, fixtureClient)
	defer svc.db.Close()
	ctx := context.Background()
	metadata, err := acquisitiontorrent.Parse(torrentBytes)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := sha256.Sum256(torrentBytes)
	infoHash := metadata.InfoHashV1
	if infoHash == "" {
		t.Fatalf("fixture torrent has no v1 info hash: %+v", metadata)
	}
	digest := hex.EncodeToString(payloadDigest[:])

	label := "one-shot-movie-real"
	draft, err := animetransport.Draft(singleEpisodeDraft(realChainFixtureURL(fixtureURL.Port, "/feed.xml")))
	if err != nil {
		t.Fatal(err)
	}
	draft.Origin = animedomain.SubscriptionOriginOneshot
	draft.OriginEntryRef = nil
	draft.InstanceLabel = &label
	draft.Title = "One Shot Real Movie"
	draft.Metadata.Bgm = &animedomain.BGMBinding{WorkId: "one-shot-movie-bgm"}
	draft.Metadata.Tmdb = &animedomain.TMDBBinding{WorkId: "one-shot-movie-tmdb", MediaKind: animedomain.TMDBMediaKindMovie, Season: 0}
	// The default movie directory is "<title> <year>", and the year is the
	// owner year set on the draft, not the year the release title carries.
	ownerYear := realChainObservedYear
	draft.Metadata.Year = &ownerYear
	seed := oneshotapplication.SelectedSeed{
		Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "70032",
		ProviderGroupRef: "678", Locator: realChainFixtureURL(fixtureURL.Port, "/artifact.torrent"),
		Title: "[RealGroup] One Shot Real Movie 2024 [1080p]", PublishedAt: time.Now().UTC(),
		InfoHash: &infoHash, ExpectedPayloadDigest: &digest,
	}
	recordOneShotSeedGroup(t, svc.db, seed, "RealGroup")
	draft = withDomainDraftBusiness(t, svc.db, draft)
	candidate, err := svc.draft.SaveAnimeConfigCandidate(ctx, draftapplication.CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no current revision")
	}
	input := oneshotapplication.AcquireSeedInput{
		CandidateID: candidate.Id,
		Revision:    *candidate.CurrentRevision,
		Seed:        seed,
		Caller: oneshotapplication.AcquireSeedCaller{Kind: oneshotapplication.AcquireSeedCallerAgent,
			Agent: &oneshotapplication.AgentSeedAuthorization{GrantedBy: "integration", AuthorizationRef: "one-shot-movie-real"}},
	}
	key := "one-shot-movie-real-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	result, err := svc.oneShot.AcquireSeedOnce(ctx, input, key)
	if err != nil {
		t.Fatal(err)
	}
	if result.DownloadTaskID == nil || *result.DownloadTaskID == "" {
		t.Fatalf("movie one-shot admission returned no DownloadTask: %+v", result)
	}
	downloadTaskID := *result.DownloadTaskID

	// The frozen target set already decided the movie path before any qB work.
	var targetPath string
	if err := svc.db.QueryRowContext(ctx, `
SELECT target_path FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID).Scan(&targetPath); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(targetPath, "Season") || strings.Contains(targetPath, "- E") {
		t.Fatalf("movie one-shot target %q used the TV naming branch", targetPath)
	}
	if !strings.Contains(targetPath, "One Shot Real Movie") {
		t.Fatalf("movie one-shot target %q lost the anime title", targetPath)
	}
	var episode *int
	if err := svc.db.QueryRowContext(ctx, `
SELECT (parsed_facts->>'episode')::int FROM releases WHERE id = $1`, result.ReleaseID).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	if episode == nil || *episode != 0 {
		t.Fatalf("movie release episode = %v, want the canonical 0", episode)
	}

	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	var taskState string
	for time.Now().Before(deadline) {
		if _, err := svc.download.DispatchQBSync(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
			t.Fatal(err)
		}
		if err := svc.db.QueryRowContext(ctx, `SELECT state FROM download_tasks WHERE id = $1`, downloadTaskID).Scan(&taskState); err != nil {
			t.Fatal(err)
		}
		if taskState == string(generated.DownloadTaskStateCompleted) || taskState == string(generated.DownloadTaskStateFailed) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if taskState != string(generated.DownloadTaskStateCompleted) {
		t.Fatalf("movie one-shot task did not complete: state=%q", taskState)
	}
	var artifactID, artifactState string
	if err := svc.db.QueryRowContext(ctx, `SELECT id, state FROM artifacts WHERE download_task_id = $1`, downloadTaskID).Scan(&artifactID, &artifactState); err != nil {
		t.Fatal(err)
	}
	if artifactState != string(generated.ArtifactStateVerified) {
		t.Fatalf("movie one-shot artifact state=%q, want verified", artifactState)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	var operationState, operationTarget string
	if err := svc.db.QueryRowContext(ctx, `SELECT state, target_path FROM file_operations WHERE artifact_id = $1`, artifactID).Scan(&operationState, &operationTarget); err != nil {
		t.Fatal(err)
	}
	if operationState != string(generated.FileOperationStateCompleted) {
		t.Fatalf("movie one-shot file operation state=%q, want completed", operationState)
	}
	if operationTarget != targetPath {
		t.Fatalf("movie one-shot file operation target %q != the frozen target %q", operationTarget, targetPath)
	}
	if !filepath.IsAbs(operationTarget) {
		t.Fatalf("movie one-shot target must be a canonical absolute path: %q", operationTarget)
	}
	target, err := os.ReadFile(operationTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(target) != string(artifactBytes) {
		t.Fatalf("movie one-shot target bytes differ: got=%q want=%q", target, artifactBytes)
	}
}
