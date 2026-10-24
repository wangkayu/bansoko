package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/download/qbittorrent"
	providerdomain "bansoko/internal/provider/domain"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/transport/httpapi/generated"
)

// recordOneShotSeedGroup records the group observation the fixture seed names.
// A one-shot renders anime.group from the display name the newest observation of
// the seed's catalog entry published for that group — never from the seed's
// provider group identity — so a real-chain fixture that admits a provider seed
// has to record the observation its group was discovered through.
func recordOneShotSeedGroup(t *testing.T, db *sql.DB, seed oneshotapplication.SelectedSeed, displayName string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	active := true
	ref := providerdomain.GroupRef{Provider: providerdomain.Kind(seed.Provider), ExternalID: seed.ProviderGroupRef}
	result := providerdomain.GroupReadResult{
		Provider: providerdomain.Kind(seed.Provider), CatalogEntryRef: seed.ProviderEntryRef,
		ObservedAt: now, SchemaVersion: "mikan-detail-groups-v1", Complete: true,
		CompletenessEvidence: "mikan-detail-group-blocks-v1",
		Groups: []providerdomain.GroupSnapshotEntry{{
			Ref: ref, DisplayName: displayName, ObservedAt: now, Active: &active,
			FeedLocator: providerdomain.FeedLocator{Provider: ref.Provider, CatalogEntryRef: seed.ProviderEntryRef, Group: ref},
		}},
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := catalogapplication.PersistGroupObservationTx(ctx, tx, result); err != nil {
		t.Fatalf("record seed group observation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestOneShotQBRealChain exercises the finite acquisition entry point against
// the real qB and the existing artifact/file-operation chain. The selected
// seed is admitted through AcquireSeedOnce, not by seeding a WorkItem row.
func TestOneShotQBRealChain(t *testing.T) {
	artifactName := "One Shot Real Show - 01.mkv"
	artifactBytes := []byte("one-shot real artifact " + time.Now().UTC().Format(time.RFC3339Nano))
	fixtureClient, fixtureURL, torrentBytes, closeFixture := startSingleEpisodeFixture(t, artifactName, artifactBytes)
	t.Cleanup(closeFixture)

	svc := newRealChainServices(t, fixtureClient)
	defer svc.db.Close()
	ctx := context.Background()
	// The fixture host is explicitly allowlisted for this disposable test; qB
	// itself remains the real harness provider and is never replaced by a fake.

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
	label := "one-shot-real"
	draft, err := animetransport.Draft(singleEpisodeDraft(realChainFixtureURL(fixtureURL.Port, "/feed.xml")))
	if err != nil {
		t.Fatal(err)
	}
	draft.Origin = animedomain.SubscriptionOriginOneshot
	draft.OriginEntryRef = nil
	draft.InstanceLabel = &label
	bgm := animedomain.BGMBinding{WorkId: "one-shot-bgm"}
	tmdb := animedomain.TMDBBinding{WorkId: "one-shot-tmdb", MediaKind: animedomain.TMDBMediaKindTv, Season: 1}
	draft.Metadata.Bgm = &bgm
	draft.Metadata.Tmdb = &tmdb
	// The default TV directory names the owner year and season, set on the draft.
	ownerYear := realChainObservedYear
	draft.Metadata.Year = &ownerYear
	ownerSeason := 1
	draft.Metadata.Season = &ownerSeason
	seed := oneshotapplication.SelectedSeed{
		Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "70031",
		ProviderGroupRef: "678", Locator: realChainFixtureURL(fixtureURL.Port, "/artifact.torrent"),
		Title: "[RealGroup] One Shot Real Show - 01 [1080p]", PublishedAt: time.Now().UTC(),
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
			Agent: &oneshotapplication.AgentSeedAuthorization{GrantedBy: "integration", AuthorizationRef: "one-shot-real"}},
	}
	key := "one-shot-real-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	result, err := svc.oneShot.AcquireSeedOnce(ctx, input, key)
	if err != nil {
		t.Fatal(err)
	}
	if result.OneShotID == "" || result.DownloadTaskID == nil || *result.DownloadTaskID == "" {
		t.Fatalf("one-shot admission returned incomplete result: %+v", result)
	}
	downloadTaskID := *result.DownloadTaskID
	var submitCount int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_submit'`, downloadTaskID).Scan(&submitCount); err != nil {
		t.Fatal(err)
	}
	if submitCount != 1 {
		t.Fatalf("one-shot admission qb_submit count=%d, want 1", submitCount)
	}
	var markerCount int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM one_shot_acquisitions WHERE id = $1 AND download_task_id = $2 AND state = 'running'`, result.OneShotID, downloadTaskID).Scan(&markerCount); err != nil {
		t.Fatal(err)
	}
	if markerCount != 1 {
		t.Fatalf("one-shot marker count=%d, want 1", markerCount)
	}
	if err := svc.draft.DeleteAnimeConfigCandidate(ctx, candidate.Id); err != nil {
		t.Fatalf("delete admitted one-shot candidate: %v", err)
	}
	var candidateRevisionCount int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_config_candidate_revisions WHERE candidate_id=$1`, candidate.Id).Scan(&candidateRevisionCount); err != nil {
		t.Fatal(err)
	}
	if candidateRevisionCount != 0 {
		t.Fatalf("candidate cleanup left %d immutable revisions", candidateRevisionCount)
	}
	replayed, err := svc.oneShot.AcquireSeedOnce(ctx, input, key)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.OneShotID != result.OneShotID || replayed.DownloadTaskID == nil || *replayed.DownloadTaskID != downloadTaskID {
		t.Fatalf("same-key replay changed one-shot identity: first=%+v replay=%+v", result, replayed)
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
		t.Fatalf("one-shot task did not complete: state=%q", taskState)
	}
	// Real-effect E evidence: the runtime must have added the frozen torrent to
	// the downloader-side save root, tagged with the task's idempotency key. This
	// is the live qB fact the dropped audit columns used to describe.
	var taskKey string
	if err := svc.db.QueryRowContext(ctx, `SELECT idempotency_key FROM download_tasks WHERE id = $1`, downloadTaskID).Scan(&taskKey); err != nil {
		t.Fatal(err)
	}
	tag := "bansoko-" + taskKey
	qb := qbittorrent.Client{Endpoint: os.Getenv("BANSOKO_QB_ENDPOINT"), Username: os.Getenv("BANSOKO_QB_USERNAME"), Password: os.Getenv("BANSOKO_QB_PASSWORD"), Timeout: 20 * time.Second}
	torrents, err := qb.ListTorrents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var torrent *qbittorrent.TorrentInfo
	for i := range torrents {
		if torrents[i].Hash == strings.ToLower(infoHash) || strings.Contains(torrents[i].Tags, tag) {
			torrent = &torrents[i]
		}
	}
	if torrent == nil {
		t.Fatalf("qB holds no torrent for hash %s / tag %s: %+v", infoHash, tag, torrents)
	}
	if torrent.SavePath != "/downloads" {
		t.Fatalf("qB save_path=%q want /downloads", torrent.SavePath)
	}
	if !strings.Contains(torrent.Tags, tag) {
		t.Fatalf("qB tags=%q missing %q", torrent.Tags, tag)
	}
	t.Logf("namespace real-chain qb_hash=%s save_path=%s tags=%s", torrent.Hash, torrent.SavePath, torrent.Tags)
	var artifactID, artifactState string
	if err := svc.db.QueryRowContext(ctx, `SELECT id, state FROM artifacts WHERE download_task_id = $1`, downloadTaskID).Scan(&artifactID, &artifactState); err != nil {
		t.Fatal(err)
	}
	if artifactState != string(generated.ArtifactStateVerified) {
		t.Fatalf("one-shot artifact state=%q, want verified", artifactState)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	var operationState, targetPath string
	if err := svc.db.QueryRowContext(ctx, `SELECT state, target_path FROM file_operations WHERE artifact_id = $1`, artifactID).Scan(&operationState, &targetPath); err != nil {
		t.Fatal(err)
	}
	if operationState != string(generated.FileOperationStateCompleted) {
		t.Fatalf("one-shot file operation state=%q, want completed", operationState)
	}
	target, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(target) != string(artifactBytes) {
		t.Fatalf("one-shot target bytes differ: got=%q want=%q", target, artifactBytes)
	}
	if filepath.IsAbs(targetPath) == false {
		t.Fatalf("file operation target must be canonical absolute path: %q", targetPath)
	}
	// The one-shot admission froze the Release media fact snapshot, so the
	// video's own filename resolved episode 1 at inspection and the video was
	// placed as a managed episode, never as an unknown file.
	mediaFacts := facts.NewStore(svc.db)
	snapshot, err := mediaFacts.ReadSnapshot(ctx, result.ReleaseID)
	if err != nil || snapshot == nil {
		t.Fatalf("one-shot release media fact snapshot: %v %v", snapshot, err)
	}
	rows, err := mediaFacts.ListFileRows(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolvedVideos := 0
	for _, row := range rows {
		identity := row.EffectiveFacts.Identity
		if row.MediaKind == "video" && identity.State == "resolved" && identity.Episode != nil && string(*identity.Episode) == "1" {
			resolvedVideos++
		}
	}
	if resolvedVideos != 1 {
		t.Fatalf("one-shot video identity rows = %+v, want one resolved episode 1", rows)
	}
	var unknownFile bool
	var placementState, factsVersion string
	if err := svc.db.QueryRowContext(ctx, `
SELECT COALESCE(fo.frozen_render_snapshot->'unknown_file' = 'true'::jsonb, false), p.state, p.facts_version
FROM file_operations fo
JOIN placements p ON p.source_evidence_type = 'file_operation' AND p.source_owner_ref = fo.id
WHERE fo.artifact_id = $1`, artifactID).Scan(&unknownFile, &placementState, &factsVersion); err != nil {
		t.Fatal(err)
	}
	if unknownFile || placementState != "active" || factsVersion == "unknown" {
		t.Fatalf("one-shot video placed as unknown=%t state=%q facts=%q, want a managed episode", unknownFile, placementState, factsVersion)
	}
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_submit'`, downloadTaskID).Scan(&submitCount); err != nil {
		t.Fatal(err)
	}
	if submitCount != 1 {
		t.Fatalf("one-shot replay/chain created %d qb_submit rows, want 1", submitCount)
	}
	assertNoDuplicateLiveWorkItems(t, ctx, svc.db)
}
