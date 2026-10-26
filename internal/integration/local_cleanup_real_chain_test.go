package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	acquisitionsettlement "bansoko/internal/acquisition/terminal/settlement"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/transport/httpapi/generated"
)

// TestLocalCleanupPreservesRealExternalEffects proves on a real qBittorrent and
// filesystem chain that the explicit local cleanup of a retired instance
// deletes the complete local owner graph while leaving every external effect
// untouched: the qB torrent/data, the downloaded source bytes, and the placed
// media-library file all survive byte-for-byte.
func TestLocalCleanupPreservesRealExternalEffects(t *testing.T) {
	items := map[string]rssItem{
		"A": {GUID: "cleanup-a-guid", Title: "[RealGroup] Cleanup Real Show - 01 [1080p]"},
	}
	video := []torrentFile{
		{Name: "Cleanup Real Show - 01.mkv", Content: []byte("cleanup video " + time.Now().UTC().Format(time.RFC3339Nano))},
		{Name: "Cleanup Real Show - 01.nfo", Content: []byte("cleanup nfo")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"A": video})
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedReplacementSingleCopy(t, svc.db)

	draft := singleEpisodeDraft(fixture.FeedURL())
	draft.Title = "Cleanup Real Show"
	draft.RssSources[0].Title = "Cleanup Real Show"
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	draft = withDraftBusiness(t, svc.db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.CurrentRevision == nil {
		t.Fatalf("candidate save failed: status=%d", saveStatus)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "cleanup-subscribe-1", "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" {
		t.Fatalf("subscribe failed: status=%d", subscribeStatus)
	}
	animeID := result.ManagedAnime.Id

	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "cleanup-bgm"},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "cleanup-tmdb", MediaKind: generated.TMDBMediaKindTv, Season: 1},
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
	svc.driveToPlacement(t, ctx, animeID, "A", video, 1)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)

	var placementPath, sourcePath, providerHash string
	if err := svc.db.QueryRowContext(ctx, `
SELECT physical_path FROM placements
WHERE managed_anime_id = $1 AND state = 'active'
ORDER BY id LIMIT 1`, animeID).Scan(&placementPath); err != nil {
		t.Fatalf("read placement path: %v", err)
	}
	if err := svc.db.QueryRowContext(ctx, `
SELECT file.source_path FROM artifact_files file
JOIN artifacts artifact ON artifact.id = file.artifact_id
JOIN download_tasks task ON task.id = artifact.download_task_id
JOIN releases release ON release.id = task.release_id
JOIN rss_sources source ON source.id = release.rss_source_id
JOIN subscriptions subscription ON subscription.id = source.subscription_id
WHERE subscription.managed_anime_id = $1 AND file.media_kind = 'video'
ORDER BY file.id LIMIT 1`, animeID).Scan(&sourcePath); err != nil {
		t.Fatalf("read source path: %v", err)
	}
	if err := svc.db.QueryRowContext(ctx, `
SELECT provider_correlation->>'info_hash' FROM download_tasks task
WHERE task.release_id IN (
  SELECT release.id FROM releases release JOIN rss_sources source ON source.id = release.rss_source_id
  JOIN subscriptions subscription ON subscription.id = source.subscription_id
  WHERE subscription.managed_anime_id = $1)
ORDER BY task.id LIMIT 1`, animeID).Scan(&providerHash); err != nil {
		t.Fatalf("read provider hash: %v", err)
	}
	var releaseID, downloadTaskID, artifactID, fileOperationID string
	if err := svc.db.QueryRowContext(ctx, `
SELECT release.id, task.id, artifact.id, operation.id
FROM download_tasks task
JOIN releases release ON release.id = task.release_id
JOIN artifacts artifact ON artifact.download_task_id = task.id
JOIN file_operations operation ON operation.artifact_id = artifact.id
WHERE release.rss_source_id IN (
  SELECT source.id FROM rss_sources source JOIN subscriptions subscription ON subscription.id = source.subscription_id
  WHERE subscription.managed_anime_id = $1)
ORDER BY operation.id LIMIT 1`, animeID).Scan(&releaseID, &downloadTaskID, &artifactID, &fileOperationID); err != nil {
		t.Fatalf("read local owner ids: %v", err)
	}
	placementBefore := fileSHA256(t, placementPath)
	sourceBefore := fileSHA256(t, sourcePath)

	// T1: the real chain settles its root once every stage is terminal.
	settlement, err := acquisitionsettlement.NewSettlementService(svc.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settlement.RunPass(ctx); err != nil {
		t.Fatalf("settlement pass: %v", err)
	}
	var rootID, rootState string
	if err := svc.db.QueryRowContext(ctx, `SELECT acquisition_id, (SELECT state FROM acquisitions WHERE id=acquisition_id) FROM download_tasks WHERE id=$1`, downloadTaskID).Scan(&rootID, &rootState); err != nil {
		t.Fatal(err)
	}
	if rootState != "succeeded" {
		t.Fatalf("real chain root %s settled to %q, want succeeded", rootID, rootState)
	}

	qb := qbittorrent.Client{Endpoint: os.Getenv("BANSOKO_QB_ENDPOINT"), Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD"), Timeout: 20 * time.Second}
	// The qB instance is shared by the lane, so earlier tests may leave their
	// own torrents: assert this chain's torrent and an unchanged set.
	hasHash := func(torrents []qbittorrent.TorrentInfo) bool {
		for _, torrent := range torrents {
			if strings.EqualFold(torrent.Hash, providerHash) {
				return true
			}
		}
		return false
	}
	before, err := qb.ListTorrents(ctx)
	if err != nil || !hasHash(before) {
		t.Fatalf("qB evidence before cleanup: torrents=%d hash=%q err=%v", len(before), providerHash, err)
	}

	if err := svc.ownership.DeleteAnimeSubscription(ctx, animeID, ownershipapplication.SubscriptionDeleteInput{ExpectedVersion: int64(result.Subscription.Version)}); err != nil {
		t.Fatalf("retirement failed: %v", err)
	}
	var epoch int64
	if err := svc.db.QueryRowContext(ctx, `SELECT fence_epoch FROM managed_anime WHERE id = $1`, animeID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	cleanupErr := svc.ownership.CleanupAnimeLocalData(ctx, animeID, ownershipapplication.LocalDataCleanupInput{
		ExpectedLifecycleEpoch: epoch,
		Confirmation:           ownershipapplication.LocalDataCleanupConfirmation,
	}, "cleanup-real-chain-1")
	if cleanupErr != nil {
		t.Fatalf("local cleanup failed: %v", cleanupErr)
	}
	// The settled root outlives cleanup as a compact record (owner_cleanup).
	var record, trigger string
	if err := svc.db.QueryRowContext(ctx, `SELECT state, record_state, compact_trigger FROM acquisitions WHERE id=$1`, rootID).Scan(&rootState, &record, &trigger); err != nil {
		t.Fatalf("root must outlive cleanup: %v", err)
	}
	if rootState != "succeeded" || record != "compact" || trigger != "owner_cleanup" {
		t.Fatalf("root after cleanup = %s/%s/%s, want succeeded/compact/owner_cleanup", rootState, record, trigger)
	}

	// The complete local owner graph is gone.
	for _, check := range []struct {
		name  string
		query string
		args  []any
	}{
		{"managed anime", `SELECT COUNT(*) FROM managed_anime WHERE id = $1`, []any{animeID}},
		{"subscription", `SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id = $1`, []any{animeID}},
		{"rss sources", `SELECT COUNT(*) FROM rss_sources WHERE subscription_id IN (SELECT id FROM subscriptions WHERE managed_anime_id = $1)`, []any{animeID}},
		{"download tasks", `SELECT COUNT(*) FROM download_tasks WHERE release_id IN (SELECT id FROM releases WHERE id = $1)`, []any{releaseID}},
		{"artifacts", `SELECT COUNT(*) FROM artifacts WHERE download_task_id = $1`, []any{downloadTaskID}},
		{"file operations", `SELECT COUNT(*) FROM file_operations WHERE id = $1`, []any{fileOperationID}},
		{"placements", `SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1`, []any{animeID}},
		{"work items", `SELECT COUNT(*) FROM work_item_history WHERE managed_anime_id = $1`, []any{animeID}},
	} {
		var count int
		if err := svc.db.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("%s survived local cleanup: %d", check.name, count)
		}
	}

	// External effects survive byte-for-byte.
	after, err := qb.ListTorrents(ctx)
	if err != nil || len(after) != len(before) || !hasHash(after) {
		t.Fatalf("local cleanup changed qB state: before=%d after=%d err=%v", len(before), len(after), err)
	}
	if got := fileSHA256(t, placementPath); got != placementBefore {
		t.Fatalf("local cleanup changed the media-library file: %s != %s", got, placementBefore)
	}
	if got := fileSHA256(t, sourcePath); got != sourceBefore {
		t.Fatalf("local cleanup changed the downloaded source bytes: %s != %s", got, sourceBefore)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:])
}
