package integration

import (
	"context"
	"errors"
	"fmt"
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
	"bansoko/internal/reconciliation/application"
	"bansoko/internal/transport/httpapi/generated"
)

// TestRetirementRecoveryRealChain proves retirement on natural data: the
// complete local owner graph and physical bytes stay inspectable, no synthetic
// recovery trace is fabricated, and the retired owner admits no new scan.
func TestRetirementRecoveryRealChain(t *testing.T) {
	items := map[string]rssItem{
		"A": {GUID: "recov-a-guid", Title: "[RealGroup] Recov Real Show - 01 [1080p]"},
		"C": {GUID: "recov-c-guid", Title: "[RealGroup] Other Real Show - 01 [1080p]"},
	}
	videoA := []torrentFile{
		{Name: "Recov Real Show - 01.mkv", Content: []byte("recovery video " + time.Now().UTC().Format(time.RFC3339Nano))},
		{Name: "Recov Real Show - 01.nfo", Content: []byte("recovery nfo")},
	}
	videoC := []torrentFile{
		{Name: "Other Real Show - 01.mkv", Content: []byte("second qB candidate " + time.Now().UTC().Format(time.RFC3339Nano))},
		{Name: "Other Real Show - 01.nfo", Content: []byte("second qB candidate nfo")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"A": videoA, "C": videoC})
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedRetentionSingleCopy(t, svc.db)

	draft := singleEpisodeDraft(fixture.FeedURL())
	draft.Title = "Recov Real Show"
	draft.RssSources[0].Title = "Recov Real Show"
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	draft = withDraftBusiness(t, svc.db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" {
		t.Fatalf("candidate save failed: status=%d", saveStatus)
	}
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no current revision")
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "recov-subscribe-1", "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" {
		t.Fatalf("subscribe failed: status=%d", subscribeStatus)
	}
	animeID := result.ManagedAnime.Id

	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "recov-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "recov-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	// The default TV directory names the providers' year observation, so the
	// fixture records it before the release is accepted.
	observeBoundWorkYear(t, ctx, svc.db, animeID, realChainObservedYear)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	svc.driveToPlacement(t, ctx, animeID, "A", videoA, 1)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	var placementPath string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementPath); err != nil {
		t.Fatal(err)
	}
	videoBytes, err := os.ReadFile(placementPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.ownership.DeleteAnimeSubscription(ctx, animeID, ownershipapplication.SubscriptionDeleteInput{ExpectedVersion: int64(result.Subscription.Version)}); err != nil {
		t.Fatalf("retirement failed: %v", err)
	}
	var lifecycleState string
	var epoch int64
	if err := svc.db.QueryRowContext(ctx, `SELECT lifecycle_state, fence_epoch FROM managed_anime WHERE id=$1`, animeID).Scan(&lifecycleState, &epoch); err != nil {
		t.Fatal(err)
	}
	if lifecycleState != "retired" || epoch != 2 {
		t.Fatalf("retirement lifecycle = %s/%d", lifecycleState, epoch)
	}
	var placements int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1`, animeID).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if placements != 1 {
		t.Fatalf("retirement evidence placements=%d", placements)
	}
	preserved, err := os.ReadFile(placementPath)
	if err != nil || string(preserved) != string(videoBytes) {
		t.Fatalf("retirement changed physical file: err=%v", err)
	}
	manual := application.ScanSubmitTriggerManual
	treeTop := scanTreeTop(t, ctx, svc.db, animeID)
	requestID := fmt.Sprintf("retired-scan-%d", time.Now().UnixNano())
	if _, err := svc.recon.SubmitScan(ctx, application.ScanSubmitInput{
		ManagedAnimeID: animeID, TreeTop: &treeTop, Trigger: &manual, RequestID: &requestID,
	}); err == nil {
		t.Fatal("retired anime admitted a new scan")
	}

	var taskA, artifactA, operationA, providerTaskID, providerHash string
	if err := svc.db.QueryRowContext(ctx, `
SELECT task.id, artifact.id, operation.id,
       task.provider_correlation->>'external_id', task.provider_correlation->>'info_hash'
FROM download_tasks task
JOIN artifacts artifact ON artifact.download_task_id = task.id
JOIN file_operations operation ON operation.artifact_id = artifact.id
WHERE task.release_id IN (
  SELECT release.id FROM releases release
  JOIN rss_sources source ON source.id = release.rss_source_id
  JOIN subscriptions subscription ON subscription.id = source.subscription_id
  WHERE subscription.managed_anime_id = $1
)
LIMIT 1`, animeID).Scan(&taskA, &artifactA, &operationA, &providerTaskID, &providerHash); err != nil {
		t.Fatal(err)
	}
	qb := qbittorrent.Client{Endpoint: os.Getenv("BANSOKO_QB_ENDPOINT"), Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD"), Timeout: 20 * time.Second}
	before, err := qb.ListTorrents(ctx)
	if err != nil || len(before) != 1 {
		t.Fatalf("qB evidence before B: torrents=%d err=%v", len(before), err)
	}

	draftB := singleEpisodeDraft(fixture.FeedURL())
	draftB.Title = "Recov Real Show B"
	draftB.RssSources[0].Title = "Recov Real Show B"
	draftB.Subgroups.Entries[0].GroupRef = "1"
	draftB.Subgroups.Entries[0].DisplayName = "RealGroup"
	draftB = withDraftBusiness(t, svc.db, draftB)
	saveBStatus, candidateB := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draftB}, nil)
	if saveBStatus != http.StatusCreated || candidateB.CurrentRevision == nil {
		t.Fatalf("candidate B save failed: status=%d candidate=%+v", saveBStatus, candidateB)
	}
	subscribeBStatus, resultB := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidateB.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidateB.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "recov-subscribe-b", "If-Match": "1"})
	if subscribeBStatus != http.StatusCreated || resultB.ManagedAnime.Id == "" || resultB.ManagedAnime.Id == animeID {
		t.Fatalf("subscribe B failed: status=%d result=%+v", subscribeBStatus, resultB)
	}
	animeB := resultB.ManagedAnime.Id
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeB, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "recov-b-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "recov-b-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	// The default TV directory names the providers' year observation, so the
	// fixture records it before the release is accepted.
	observeBoundWorkYear(t, ctx, svc.db, animeB, realChainObservedYear)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	svc.driveToPlacement(t, ctx, animeB, "A", videoA, 1)
	waitForPlacementCount(t, ctx, svc.db, animeB, 1)

	var taskB, artifactB, operationB, frozenB string
	if err := svc.db.QueryRowContext(ctx, `
SELECT task.id, artifact.id, operation.id, task.frozen_input::text
FROM download_tasks task
JOIN artifacts artifact ON artifact.download_task_id = task.id
JOIN file_operations operation ON operation.artifact_id = artifact.id
WHERE task.release_id IN (
  SELECT release.id FROM releases release
  JOIN rss_sources source ON source.id = release.rss_source_id
  JOIN subscriptions subscription ON subscription.id = source.subscription_id
  WHERE subscription.managed_anime_id = $1
)
LIMIT 1`, animeB).Scan(&taskB, &artifactB, &operationB, &frozenB); err != nil {
		t.Fatal(err)
	}
	if taskB == taskA || artifactB == artifactA || operationB == operationA {
		t.Fatalf("B reused A owner facts: tasks=%q/%q artifacts=%q/%q operations=%q/%q", taskA, taskB, artifactA, artifactB, operationA, operationB)
	}
	if !strings.Contains(frozenB, `"kind": "unique_infohash_path"`) || !strings.Contains(frozenB, `"provider_task_id": "hash:`+providerHash+`"`) || strings.Contains(frozenB, taskA) || strings.Contains(frozenB, artifactA) || strings.Contains(frozenB, operationA) {
		t.Fatalf("B frozen adoption provenance is unsafe or incomplete: %s (A provider task %q)", frozenB, providerTaskID)
	}
	after, err := qb.ListTorrents(ctx)
	if err != nil || len(after) != 1 || !strings.EqualFold(after[0].Hash, providerHash) {
		t.Fatalf("cross-owner adoption changed qB identity: before=%+v after=%+v err=%v", before, after, err)
	}
	preservedAfterB, err := os.ReadFile(placementPath)
	if err != nil || string(preservedAfterB) != string(videoBytes) {
		t.Fatalf("B adoption changed A physical file: err=%v", err)
	}

	metadataC, err := torrent.Parse(fixture.TorrentBytes("C"))
	if err != nil {
		t.Fatal(err)
	}
	pathSnapshot := domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: svc.hostDownloadRoot}
	if _, err := qb.SubmitFileWithSnapshot(ctx, fixture.TorrentBytes("C"), "recov-c.torrent", pathSnapshot, "ambiguous-second"); err != nil {
		t.Fatal(err)
	}
	var taskCountBefore int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks`).Scan(&taskCountBefore); err != nil {
		t.Fatal(err)
	}
	_, err = svc.download.MatchAdmission(ctx, downloadadmission.Request{
		OperationKey: "qb:ambiguous-new-owner", InfoHashV1: providerHash, InfoHashV2: metadataC.InfoHashV1,
	})
	if !errors.Is(err, downloadadmission.ErrAmbiguousMatch) {
		t.Fatalf("real qB multi-candidate match = %v, want typed ambiguity", err)
	}
	var taskCountAfter int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks`).Scan(&taskCountAfter); err != nil {
		t.Fatal(err)
	}
	if taskCountAfter != taskCountBefore {
		t.Fatalf("ambiguous qB admission created a local task: before=%d after=%d", taskCountBefore, taskCountAfter)
	}
	torrentsAfterAmbiguity, err := qb.ListTorrents(ctx)
	if err != nil || len(torrentsAfterAmbiguity) != 2 {
		t.Fatalf("ambiguous qB evidence was not preserved: torrents=%+v err=%v", torrentsAfterAmbiguity, err)
	}
}
