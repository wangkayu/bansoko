package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	acquisitiontorrent "bansoko/internal/acquisition/torrent"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	animetransport "bansoko/internal/anime/transport"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
)

// TestOneShotQBSameTorrentSecondSubmissionOccupied submits one torrent twice
// against the real qB. qB keeps one task per info hash, so:
//   - an add prepared while qB did not hold the torrent yet, and sent after
//     the first one-shot added it, is refused by the real qB and attributed to
//     the occupying task (the race fallback);
//   - a second one-shot of the same seed with another configuration is not
//     sent at all and fails with the explicit downloader_hash_occupied reason.
//
// Either way qB keeps exactly the first task.
func TestOneShotQBSameTorrentSecondSubmissionOccupied(t *testing.T) {
	artifactName := "One Shot Occupied - 01.mkv"
	artifactBytes := []byte("one-shot occupied artifact " + time.Now().UTC().Format(time.RFC3339Nano))
	fixtureClient, fixtureURL, torrentBytes, closeFixture := startSingleEpisodeFixture(t, artifactName, artifactBytes)
	t.Cleanup(closeFixture)

	svc := newRealChainServices(t, fixtureClient)
	defer svc.db.Close()
	ctx := context.Background()
	metadata, err := acquisitiontorrent.Parse(torrentBytes)
	if err != nil {
		t.Fatal(err)
	}
	infoHash := metadata.InfoHashV1
	if infoHash == "" {
		t.Fatalf("fixture torrent has no v1 info hash: %+v", metadata)
	}
	payloadDigest := sha256.Sum256(torrentBytes)
	digest := hex.EncodeToString(payloadDigest[:])
	seed := oneshotapplication.SelectedSeed{
		Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "70034",
		ProviderGroupRef: "678", Locator: realChainFixtureURL(fixtureURL.Port, "/artifact.torrent"),
		Title: "[RealGroup] One Shot Occupied - 01 [1080p]", PublishedAt: time.Now().UTC(),
		InfoHash: &infoHash, ExpectedPayloadDigest: &digest,
	}
	recordOneShotSeedGroup(t, svc.db, seed, "RealGroup")

	admit := func(label string) string {
		t.Helper()
		draft, err := animetransport.Draft(singleEpisodeDraft(realChainFixtureURL(fixtureURL.Port, "/feed.xml")))
		if err != nil {
			t.Fatal(err)
		}
		draft.Origin = animedomain.SubscriptionOriginOneshot
		draft.OriginEntryRef = nil
		draft.InstanceLabel = &label
		bgm := animedomain.BGMBinding{WorkId: label + "-bgm"}
		tmdb := animedomain.TMDBBinding{WorkId: label + "-tmdb", MediaKind: animedomain.TMDBMediaKindTv, Season: 1}
		draft.Metadata.Bgm = &bgm
		draft.Metadata.Tmdb = &tmdb
		ownerYear := realChainObservedYear
		draft.Metadata.Year = &ownerYear
		draft = withDomainDraftBusiness(t, svc.db, draft)
		candidate, err := svc.draft.SaveAnimeConfigCandidate(ctx, draftapplication.CandidateDraftInput{Draft: draft})
		if err != nil {
			t.Fatal(err)
		}
		if candidate.CurrentRevision == nil {
			t.Fatal("saved candidate has no current revision")
		}
		result, err := svc.oneShot.AcquireSeedOnce(ctx, oneshotapplication.AcquireSeedInput{
			CandidateID: candidate.Id, Revision: *candidate.CurrentRevision, Seed: seed,
			Caller: oneshotapplication.AcquireSeedCaller{Kind: oneshotapplication.AcquireSeedCallerAgent,
				Agent: &oneshotapplication.AgentSeedAuthorization{GrantedBy: "integration", AuthorizationRef: label}},
		}, label+"-"+strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-"))
		if err != nil {
			t.Fatal(err)
		}
		if result.DownloadTaskID == nil || *result.DownloadTaskID == "" {
			t.Fatalf("one-shot admission returned no download task: %+v", result)
		}
		return *result.DownloadTaskID
	}

	qb := qbittorrent.Client{Endpoint: os.Getenv("BANSOKO_QB_ENDPOINT"), Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD"), Timeout: 20 * time.Second}
	snapshot := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: svc.hostDownloadRoot}
	// Prepared before qB holds the torrent: the pre-check passes.
	racing, _, found, err := qb.PrepareSubmission(ctx, torrentBytes, "occupied-race.torrent", snapshot, "occupied-race", []string{infoHash})
	if err != nil || found || racing == nil {
		t.Fatalf("race prepare found=%v err=%v", found, err)
	}

	first := admit("one-shot-occupied-first")
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	var firstState string
	if err := svc.db.QueryRowContext(ctx, `SELECT state FROM download_tasks WHERE id=$1`, first).Scan(&firstState); err != nil {
		t.Fatal(err)
	}
	if firstState == "failed" || firstState == "queued" {
		t.Fatalf("first one-shot was not submitted: state=%q", firstState)
	}

	result, err := racing.Execute(ctx)
	if !errors.Is(err, qbittorrent.ErrHashOccupied) || result.Accepted || !result.ResponseReceived {
		t.Fatalf("real qB refusal of a held torrent: result=%+v err=%v", result, err)
	}

	second := admit("one-shot-occupied-second")
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	var state, code string
	var dispatched bool
	if err := svc.db.QueryRowContext(ctx, `SELECT state,COALESCE(failure_code,''),submit_dispatch_started_at IS NOT NULL FROM download_tasks WHERE id=$1`, second).Scan(&state, &code, &dispatched); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != downloadapplication.ReasonDownloaderHashOccupied || dispatched {
		t.Fatalf("second one-shot state=%q code=%q dispatched=%v", state, code, dispatched)
	}

	torrents, err := qb.ListTorrents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	held := 0
	for _, item := range torrents {
		if strings.EqualFold(item.Hash, infoHash) {
			held++
			if strings.Contains(item.Tags, "occupied-race") {
				t.Fatalf("the refused add tagged the occupying task: %+v", item)
			}
		}
	}
	if held != 1 {
		t.Fatalf("qB holds %d tasks for the torrent, want the first one only", held)
	}
}
