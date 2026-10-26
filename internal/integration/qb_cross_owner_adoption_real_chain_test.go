package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"bansoko/internal/acquisition/torrent"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	downloadadmission "bansoko/internal/download/admission"
	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/transport/httpapi/generated"
)

func TestQBCrossOwnerAdoptionAfterRetirementRealChain(t *testing.T) {
	video := []torrentFile{
		{Name: "Adoption Real Show - 01.mkv", Content: bytes.Repeat([]byte("cross-owner-adoption-"), 2<<20)},
		{Name: "Adoption Real Show - 01.nfo", Content: []byte("cross-owner adoption nfo")},
	}
	fixture := startRevisionFixture(t,
		map[string]rssItem{"A": {GUID: "adoption-real-guid", Title: "[RealGroup] Adoption Real Show - 01 [1080p]"}},
		map[string][]torrentFile{"A": video},
	)
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedReplacementSingleCopy(t, svc.db)

	subscribe := func(title, key string) generated.SubscribeResult {
		draft := singleEpisodeDraft(fixture.FeedURL())
		draft.Title = title
		draft.RssSources[0].Title = title
		draft.Subgroups.Entries[0].GroupRef = "1"
		draft.Subgroups.Entries[0].DisplayName = "RealGroup"
		draft = withDraftBusiness(t, svc.db, draft)
		status, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
		if status != http.StatusCreated || candidate.CurrentRevision == nil {
			t.Fatalf("save %s candidate: status=%d candidate=%+v", title, status, candidate)
		}
		status, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": key, "If-Match": "1"})
		if status != http.StatusCreated || result.ManagedAnime.Id == "" {
			t.Fatalf("subscribe %s: status=%d result=%+v", title, status, result)
		}
		return result
	}
	completeMetadata := func(animeID, prefix string) {
		if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
			ExpectedVersion: 1,
			Bgm:             &generated.NullableBGMBinding{WorkId: prefix + "-bgm"},
			Tmdb:            &generated.NullableTMDBBinding{WorkId: prefix + "-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
			// The naming season is the owner value, not the TMDB season.
			Season: new(1),
		}); err != nil {
			t.Fatal(err)
		}
		// The default TV directory names the providers' year observation, so the
		// fixture records it before the release is accepted.
		observeBoundWorkYear(t, ctx, svc.db, animeID, realChainObservedYear)
	}

	ownerA := subscribe("Adoption Real Show A", "adoption-owner-a")
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	completeMetadata(ownerA.ManagedAnime.Id, "adoption-a")
	qb := qbittorrent.Client{Endpoint: os.Getenv("BANSOKO_QB_ENDPOINT"), Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD"), Timeout: 20 * time.Second}
	var torrentHash string
	for index := 0; index < 8 && torrentHash == ""; index++ {
		handled, err := svc.runtime.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !handled {
			t.Fatal("owner A worker became idle before qB admission")
		}
		torrents, err := qb.ListTorrents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(torrents) == 1 {
			torrentHash = torrents[0].Hash
			if err := qb.PauseTorrent(ctx, torrentHash); err != nil {
				t.Fatal(err)
			}
		}
	}
	if torrentHash == "" {
		t.Fatal("owner A qB torrent was not admitted")
	}
	var taskA, stateA string
	if err := svc.db.QueryRowContext(ctx, `
SELECT task.id, task.state
FROM download_tasks task
JOIN releases release ON release.id = task.release_id
JOIN rss_sources source ON source.id = release.rss_source_id
JOIN subscriptions subscription ON subscription.id = source.subscription_id
WHERE subscription.managed_anime_id = $1`, ownerA.ManagedAnime.Id).Scan(&taskA, &stateA); err != nil {
		t.Fatal(err)
	}
	if stateA == string(generated.DownloadTaskStateCompleted) {
		t.Fatalf("owner A completed locally before retirement: %s", taskA)
	}
	if err := svc.ownership.DeleteAnimeSubscription(ctx, ownerA.ManagedAnime.Id, ownershipapplication.SubscriptionDeleteInput{ExpectedVersion: int64(ownerA.Subscription.Version)}); err != nil {
		t.Fatal(err)
	}
	if err := qb.ResumeTorrent(ctx, torrentHash); err != nil {
		t.Fatal(err)
	}
	for _, file := range video {
		waitForArtifact(t, svc.hostDownloadRoot+"/A/"+file.Name, int64(len(file.Content)), 60*time.Second)
	}
	dispatch, err := svc.download.DispatchQBSync(ctx)
	if err != nil || dispatch.Scanned != 0 || dispatch.Queued != 0 {
		t.Fatalf("retired owner A remained eligible for qB sync: result=%+v err=%v", dispatch, err)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	var artifactsA int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE download_task_id = $1`, taskA).Scan(&artifactsA); err != nil {
		t.Fatal(err)
	}
	if artifactsA != 0 {
		t.Fatalf("retired owner A created %d late artifacts", artifactsA)
	}

	ownerB := subscribe("Adoption Real Show B", "adoption-owner-b")
	if ownerB.ManagedAnime.Id == ownerA.ManagedAnime.Id {
		t.Fatal("owner B reused owner A identity")
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	completeMetadata(ownerB.ManagedAnime.Id, "adoption-b")
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	svc.driveToPlacement(t, ctx, ownerB.ManagedAnime.Id, "A", video, 1)
	waitForPlacementCount(t, ctx, svc.db, ownerB.ManagedAnime.Id, 1)

	var taskB, artifactB, operationB, frozenB string
	if err := svc.db.QueryRowContext(ctx, `
SELECT task.id, artifact.id, operation.id, task.frozen_input::text
FROM download_tasks task
JOIN artifacts artifact ON artifact.download_task_id = task.id
JOIN file_operations operation ON operation.artifact_id = artifact.id
JOIN releases release ON release.id = task.release_id
JOIN rss_sources source ON source.id = release.rss_source_id
JOIN subscriptions subscription ON subscription.id = source.subscription_id
WHERE subscription.managed_anime_id = $1
LIMIT 1`, ownerB.ManagedAnime.Id).Scan(&taskB, &artifactB, &operationB, &frozenB); err != nil {
		t.Fatal(err)
	}
	if taskB == taskA || artifactB == "" || operationB == "" {
		t.Fatalf("owner B chain is not independent: A=%q B=%q artifact=%q operation=%q", taskA, taskB, artifactB, operationB)
	}
	if !strings.Contains(frozenB, `"kind": "unique_infohash_path"`) || !strings.Contains(strings.ToLower(frozenB), strings.ToLower(torrentHash)) || strings.Contains(frozenB, taskA) {
		t.Fatalf("owner B adoption provenance is incomplete or copied local facts: %s", frozenB)
	}
	var qBTorrents []qbittorrent.TorrentInfo
	qBTorrents, err = qb.ListTorrents(ctx)
	if err != nil || len(qBTorrents) != 1 || !strings.EqualFold(qBTorrents[0].Hash, torrentHash) {
		t.Fatalf("owner B changed the retained qB task: torrents=%+v err=%v", qBTorrents, err)
	}
	var ownerAArtifact sql.NullString
	if err := svc.db.QueryRowContext(ctx, `SELECT (SELECT id FROM artifacts WHERE download_task_id = $1 LIMIT 1)`, taskA).Scan(&ownerAArtifact); err != nil {
		t.Fatal(err)
	}
	if ownerAArtifact.Valid {
		t.Fatalf("owner A gained artifact %q after retirement", ownerAArtifact.String)
	}
}

// TestQBCrossOwnerAdoptionAmbiguousHashesRealChain proves the many-match branch
// against a real qBittorrent: two retained provider tasks each matching one of
// the frozen hash candidates make the lookup ambiguous, which stays a typed
// fail-closed outcome and creates no owner work at all. Both external tasks stay
// untouched.
func TestQBCrossOwnerAdoptionAmbiguousHashesRealChain(t *testing.T) {
	videoA := []torrentFile{{Name: "Ambiguous Real Show - 01.mkv", Content: bytes.Repeat([]byte("ambiguous-owner-a-"), 1<<20)}}
	videoC := []torrentFile{{Name: "Other Ambiguous Show - 01.mkv", Content: bytes.Repeat([]byte("ambiguous-owner-c-"), 1<<20)}}
	fixture := startRevisionFixture(t,
		map[string]rssItem{
			"A": {GUID: "ambiguous-real-a", Title: "[RealGroup] Ambiguous Real Show - 01 [1080p]"},
			"C": {GUID: "ambiguous-real-c", Title: "[RealGroup] Other Ambiguous Show - 01 [1080p]"},
		},
		map[string][]torrentFile{"A": videoA, "C": videoC},
	)
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedReplacementSingleCopy(t, svc.db)

	draft := singleEpisodeDraft(fixture.FeedURL())
	draft.Title = "Ambiguous Real Show"
	draft.RssSources[0].Title = "Ambiguous Real Show"
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	draft = withDraftBusiness(t, svc.db, draft)
	status, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if status != http.StatusCreated || candidate.CurrentRevision == nil {
		t.Fatalf("save candidate: status=%d candidate=%+v", status, candidate)
	}
	status, subscribed := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "ambiguous-owner-a", "If-Match": "1"})
	if status != http.StatusCreated || subscribed.ManagedAnime.Id == "" {
		t.Fatalf("subscribe: status=%d result=%+v", status, subscribed)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, subscribed.ManagedAnime.Id, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "ambiguous-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "ambiguous-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	// The default TV directory names the providers' year observation, so the
	// fixture records it before the release is accepted.
	observeBoundWorkYear(t, ctx, svc.db, subscribed.ManagedAnime.Id, realChainObservedYear)

	qb := qbittorrent.Client{Endpoint: os.Getenv("BANSOKO_QB_ENDPOINT"), Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD"), Timeout: 20 * time.Second}
	var firstHash string
	for index := 0; index < 8 && firstHash == ""; index++ {
		handled, err := svc.runtime.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !handled {
			break
		}
		torrents, err := qb.ListTorrents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(torrents) == 1 {
			firstHash = torrents[0].Hash
		}
	}
	if firstHash == "" {
		t.Fatal("owner A never admitted a real qB torrent")
	}

	// A second, independent external task with its own frozen infohash. It is
	// submitted directly to qB so it exists without any local owner work.
	second := fixture.TorrentBytes("C")
	secondMetadata, err := torrent.Parse(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(secondMetadata.InfoHashV1, firstHash) {
		t.Fatal("fixture produced two identical infohashes")
	}
	if _, err := qb.SubmitFileWithSnapshot(ctx, second, "ambiguous-second.torrent", domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: svc.hostDownloadRoot}, "ambiguous-second"); err != nil {
		t.Fatal(err)
	}
	var taskCountBefore int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks`).Scan(&taskCountBefore); err != nil {
		t.Fatal(err)
	}

	_, err = svc.download.MatchAdmission(ctx, downloadadmission.Request{
		OperationKey: "qb:ambiguous-real-owner", InfoHashV1: firstHash, InfoHashV2: secondMetadata.InfoHashV1,
	})
	if !errors.Is(err, downloadadmission.ErrAmbiguousMatch) {
		t.Fatalf("two real qB hash matches = %v, want typed ambiguity", err)
	}
	var taskCountAfter int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks`).Scan(&taskCountAfter); err != nil {
		t.Fatal(err)
	}
	if taskCountAfter != taskCountBefore {
		t.Fatalf("ambiguous admission created owner work: before=%d after=%d", taskCountBefore, taskCountAfter)
	}
	retained, err := qb.ListTorrents(ctx)
	if err != nil || len(retained) != 2 {
		t.Fatalf("ambiguous admission changed external state: torrents=%+v err=%v", retained, err)
	}
}
