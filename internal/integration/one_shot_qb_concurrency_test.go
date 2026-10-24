package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	acquisitiontorrent "bansoko/internal/acquisition/torrent"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	animetransport "bansoko/internal/anime/transport"
)

// TestOneShotQBConcurrentSameKeyAdmitsOnce proves the finite command's
// idempotency under real concurrency: two simultaneous admissions carrying one
// request key must converge on a single marker, a single DownloadTask and a
// single qb_submit WorkItem, and both callers must observe the same identity.
func TestOneShotQBConcurrentSameKeyAdmitsOnce(t *testing.T) {
	artifactName := "One Shot Concurrent - 01.mkv"
	artifactBytes := []byte("one-shot concurrent artifact " + time.Now().UTC().Format(time.RFC3339Nano))
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
	label := "one-shot-concurrent"
	draft, err := animetransport.Draft(singleEpisodeDraft(realChainFixtureURL(fixtureURL.Port, "/feed.xml")))
	if err != nil {
		t.Fatal(err)
	}
	draft.Origin = animedomain.SubscriptionOriginOneshot
	draft.OriginEntryRef = nil
	draft.InstanceLabel = &label
	bgm := animedomain.BGMBinding{WorkId: "one-shot-concurrent-bgm"}
	tmdb := animedomain.TMDBBinding{WorkId: "one-shot-concurrent-tmdb", MediaKind: animedomain.TMDBMediaKindTv, Season: 1}
	draft.Metadata.Bgm = &bgm
	draft.Metadata.Tmdb = &tmdb
	// The default TV directory names the owner year, set on the draft.
	ownerYear := realChainObservedYear
	draft.Metadata.Year = &ownerYear
	seed := oneshotapplication.SelectedSeed{
		Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "70033",
		ProviderGroupRef: "678", Locator: realChainFixtureURL(fixtureURL.Port, "/artifact.torrent"),
		Title: "[RealGroup] One Shot Concurrent - 01 [1080p]", PublishedAt: time.Now().UTC(),
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
			Agent: &oneshotapplication.AgentSeedAuthorization{GrantedBy: "integration", AuthorizationRef: "one-shot-concurrent"}},
	}
	key := "one-shot-concurrent-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")

	type admission struct {
		result oneshotapplication.OneShotAdmissionResult
		err    error
	}
	results := make(chan admission, 2)
	var group sync.WaitGroup
	for attempt := 0; attempt < 2; attempt++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := svc.oneShot.AcquireSeedOnce(ctx, input, key)
			results <- admission{result: result, err: err}
		}()
	}
	group.Wait()
	close(results)
	identities := make(map[string]int)
	for admitted := range results {
		if admitted.err != nil {
			t.Fatalf("concurrent admission failed: %v", admitted.err)
		}
		downloadTaskID := ""
		if admitted.result.DownloadTaskID != nil {
			downloadTaskID = *admitted.result.DownloadTaskID
		}
		identities[admitted.result.OneShotID+"|"+downloadTaskID]++
	}
	if len(identities) != 1 {
		t.Fatalf("concurrent same-key admissions must converge on one identity, got %v", identities)
	}
	var markerCount, taskCount, submitCount int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM one_shot_acquisitions`).Scan(&markerCount); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type = 'download_task' AND kind = 'qb_submit'`).Scan(&submitCount); err != nil {
		t.Fatal(err)
	}
	if markerCount != 1 || taskCount != 1 || submitCount != 1 {
		t.Fatalf("concurrent admission markers=%d tasks=%d qb_submit=%d, want 1/1/1", markerCount, taskCount, submitCount)
	}
	// The single accepted admission is fully durable for a restarted worker or
	// server: the marker is running and the one qb_submit item is still
	// unstarted, so a fresh process resumes (rather than re-admits) the chain.
	var markerState, submitState string
	var submitAttempt int
	if err := svc.db.QueryRowContext(ctx, `SELECT state FROM one_shot_acquisitions`).Scan(&markerState); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRowContext(ctx, `SELECT state, attempt FROM work_item_history WHERE owner_type = 'download_task' AND kind = 'qb_submit'`).Scan(&submitState, &submitAttempt); err != nil {
		t.Fatal(err)
	}
	if markerState != "running" || submitState != "available" || submitAttempt != 0 {
		t.Fatalf("accepted admission must be resumable: marker=%s qb_submit=%s attempt=%d", markerState, submitState, submitAttempt)
	}
	assertNoDuplicateLiveWorkItems(t, ctx, svc.db)
}
