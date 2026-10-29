package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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

// A 300-file release (100 videos, a subtitle per video and 100 extras) runs
// the whole post-download chain: inspection accepts every manifest file with
// path-level fingerprints and a summary of counts, the plan and the file
// operations cover all 200 media files, and every video is placed with its
// subtitle as a companion. The extras stay in the download.
const largeReleaseEpisodes = 100

func largeReleaseContent(label, stamp string) []byte {
	return []byte("large release " + label + " " + stamp)
}

// TestLargeReleaseSubscriptionRealChain is the subscription path: a batch
// item of first-level files (a subscription imports first-level files only).
func TestLargeReleaseSubscriptionRealChain(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	var files []torrentFile
	for episode := 1; episode <= largeReleaseEpisodes; episode++ {
		name := fmt.Sprintf("Box Show - %03d", episode)
		files = append(files,
			torrentFile{Name: name + ".mkv", Content: largeReleaseContent("video "+name, stamp)},
			torrentFile{Name: name + ".chs.ass", Content: []byte("Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,," + name + " " + stamp)},
			torrentFile{Name: name + ".nfo", Content: largeReleaseContent("extra "+name, stamp)},
		)
	}
	items := map[string]rssItem{"large-box": {GUID: "large-box-guid", Title: fmt.Sprintf("[RealGroup] Box Show [001-%03d] [1080p]", largeReleaseEpisodes)}}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"large-box": files})
	fixture.SetCurrent("large-box")
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedReplacementSingleCopy(t, svc.db)
	animeID, _ := subscribeRealChainShow(t, ctx, svc, fixture.FeedURL(), "Box Show", flatNamingRules(svc.targetRoot), false)

	var taskID string
	if err := svc.db.QueryRowContext(ctx, `
SELECT t.id FROM download_tasks t JOIN releases r ON r.id = t.release_id
WHERE r.stable_item_key = 'large-box-guid'`).Scan(&taskID); err != nil {
		var verdict, reason string
		_ = svc.db.QueryRowContext(ctx, `SELECT verdict, reason_code FROM releases WHERE stable_item_key = 'large-box-guid'`).Scan(&verdict, &reason)
		t.Fatalf("the 300-file batch item was not submitted: verdict=%q reason=%q: %v", verdict, reason, err)
	}
	artifactID := driveLargeReleaseToFiles(t, ctx, svc, taskID)
	assertLargeReleasePlaced(t, ctx, svc, animeID, artifactID, files)
}

// TestLargeReleaseOneShotRealChain is the one-shot path with a target-planned
// manual selection at depth: every video and its same-folder subtitle in four
// disc folders, the scans folder left unselected.
func TestLargeReleaseOneShotRealChain(t *testing.T) {
	const torrentName = "OSBox Real Show"
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	var files []torrentFile
	var selection []string
	for episode := 1; episode <= largeReleaseEpisodes; episode++ {
		folder := fmt.Sprintf("Disc %d/", (episode-1)/25+1)
		name := fmt.Sprintf("OSBox Real Show - %03d", episode)
		video := torrentFile{Name: folder + name + ".mkv", Content: largeReleaseContent("video "+name, stamp)}
		subtitle := torrentFile{Name: folder + name + ".chs.ass", Content: []byte("Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,," + name + " " + stamp)}
		files = append(files, video, subtitle,
			torrentFile{Name: fmt.Sprintf("Extras/Scans/Booklet %d/scan-%03d.png", (episode-1)/50+1, episode), Content: largeReleaseContent("scan", stamp)})
		selection = append(selection, torrentName+"/"+video.Name, torrentName+"/"+subtitle.Name)
	}
	fixtureClient, fixtureURL, torrentBytes, closeFixture := startNestedFileFixture(t, torrentName, files)
	t.Cleanup(closeFixture)

	svc := newRealChainServices(t, fixtureClient)
	defer svc.db.Close()
	ctx := context.Background()

	metadata, err := acquisitiontorrent.Parse(torrentBytes)
	if err != nil {
		t.Fatal(err)
	}
	infoHash := metadata.InfoHashV1
	payloadDigest := sha256.Sum256(torrentBytes)
	digest := hex.EncodeToString(payloadDigest[:])
	label := "one-shot-large-release"
	draft, err := animetransport.Draft(singleEpisodeDraft(realChainFixtureURL(fixtureURL.Port, "/feed.xml")))
	if err != nil {
		t.Fatal(err)
	}
	draft.Title = torrentName
	draft.Origin = animedomain.SubscriptionOriginOneshot
	draft.OriginEntryRef = nil
	draft.InstanceLabel = &label
	bgm := animedomain.BGMBinding{WorkId: "osbox-bgm"}
	tmdb := animedomain.TMDBBinding{WorkId: "osbox-tmdb", MediaKind: animedomain.TMDBMediaKindTv, Season: 1}
	draft.Metadata.Bgm = &bgm
	draft.Metadata.Tmdb = &tmdb
	ownerYear := realChainObservedYear
	draft.Metadata.Year = &ownerYear
	ownerSeason := 1
	draft.Metadata.Season = &ownerSeason
	seed := oneshotapplication.SelectedSeed{
		Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "70033",
		ProviderGroupRef: "679", Locator: realChainFixtureURL(fixtureURL.Port, "/artifact.torrent"),
		Title:       fmt.Sprintf("[RealGroup] OSBox Real Show [001-%03d] [1080p]", largeReleaseEpisodes),
		PublishedAt: time.Now().UTC(), InfoHash: &infoHash, ExpectedPayloadDigest: &digest,
	}
	recordOneShotSeedGroup(t, svc.db, seed, "RealGroup")
	if draft.Rules.Sections.FileEffect, err = json.Marshal(generated.FileEffectRulesReplace{
		Mode:  generated.FileEffectRulesReplaceModeReplace,
		Value: generated.FileEffectRules{Operation: generated.FileEffectRulesOperationCopy, CollisionPolicy: generated.CollisionPolicyVersioned},
	}); err != nil {
		t.Fatal(err)
	}
	draft = withDomainDraftBusiness(t, svc.db, draft)
	candidate, err := svc.draft.SaveAnimeConfigCandidate(ctx, draftapplication.CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no current revision")
	}
	result, err := svc.oneShot.AcquireSeedOnce(ctx, oneshotapplication.AcquireSeedInput{
		CandidateID: candidate.Id, Revision: *candidate.CurrentRevision, Seed: seed, FileSelection: selection,
		Caller: oneshotapplication.AcquireSeedCaller{Kind: oneshotapplication.AcquireSeedCallerAgent,
			Agent: &oneshotapplication.AgentSeedAuthorization{GrantedBy: "integration", AuthorizationRef: label}},
	}, label+"-"+strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-"))
	if err != nil {
		t.Fatal(err)
	}
	if result.DownloadTaskID == nil || *result.DownloadTaskID == "" {
		t.Fatalf("one-shot admission returned no download task: %+v", result)
	}
	artifactID := driveLargeReleaseToFiles(t, ctx, svc, *result.DownloadTaskID)
	assertLargeReleasePlaced(t, ctx, svc, result.ManagedAnimeID, artifactID, files)
}

// driveLargeReleaseToFiles runs the chain until the task's download completes
// and every file operation of its artifact has settled, and returns the
// artifact.
func driveLargeReleaseToFiles(t *testing.T, ctx context.Context, svc *realChainServices, taskID string) string {
	t.Helper()
	const iterations = 4000
	deadline := time.Now().Add(180 * time.Second)
	var taskState string
	for time.Now().Before(deadline) {
		if _, err := svc.download.DispatchQBSync(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, iterations); err != nil {
			t.Fatal(err)
		}
		if err := svc.db.QueryRowContext(ctx, `SELECT state FROM download_tasks WHERE id = $1`, taskID).Scan(&taskState); err != nil {
			t.Fatal(err)
		}
		if taskState == string(generated.DownloadTaskStateCompleted) || taskState == string(generated.DownloadTaskStateFailed) {
			break
		}
		time.Sleep(time.Second)
	}
	if taskState != string(generated.DownloadTaskStateCompleted) {
		t.Fatalf("the 300-file download did not complete: state=%q", taskState)
	}
	var artifactID string
	effectDeadline := time.Now().Add(300 * time.Second)
	for {
		if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, iterations); err != nil {
			t.Fatal(err)
		}
		var state string
		var operations, pending int
		if err := svc.db.QueryRowContext(ctx, `
SELECT COALESCE(a.id, ''), COALESCE(a.state, ''),
       (SELECT COUNT(*) FROM file_operations o WHERE o.artifact_id = a.id),
       (SELECT COUNT(*) FROM file_operations o WHERE o.artifact_id = a.id AND o.state NOT IN ('completed', 'failed', 'cancelled'))
FROM download_tasks t LEFT JOIN artifacts a ON a.download_task_id = t.id WHERE t.id = $1`, taskID).Scan(&artifactID, &state, &operations, &pending); err != nil {
			t.Fatal(err)
		}
		if state == string(generated.ArtifactStateFailed) {
			var code, reason string
			_ = svc.db.QueryRowContext(ctx, `SELECT COALESCE(failure_code, ''), COALESCE(failure_reason, '') FROM artifacts WHERE id = $1`, artifactID).Scan(&code, &reason)
			t.Fatalf("inspection of the 300-file artifact failed: %s %s", code, reason)
		}
		if operations > 0 && pending == 0 {
			return artifactID
		}
		if time.Now().After(effectDeadline) {
			t.Fatalf("the 300-file artifact did not settle its file operations: artifact state=%q operations=%d pending=%d", state, operations, pending)
		}
		time.Sleep(time.Second)
	}
}

func assertLargeReleasePlaced(t *testing.T, ctx context.Context, svc *realChainServices, animeID, artifactID string, files []torrentFile) {
	t.Helper()
	var inventory []byte
	var rows, metadataRows int
	if err := svc.db.QueryRowContext(ctx, `
SELECT a.inventory_snapshot, COUNT(f.id), COUNT(f.id) FILTER (WHERE f.fingerprint->>'algorithm' = 'metadata')
FROM artifacts a JOIN artifact_files f ON f.artifact_id = a.id WHERE a.id = $1 AND a.state = 'verified'
GROUP BY a.id`, artifactID).Scan(&inventory, &rows, &metadataRows); err != nil {
		t.Fatalf("verified 300-file artifact: %v", err)
	}
	if rows != len(files) || metadataRows != len(files) {
		t.Fatalf("artifact rows = %d (metadata fingerprints %d), want %d", rows, metadataRows, len(files))
	}
	var summary map[string]any
	if err := json.Unmarshal(inventory, &summary); err != nil {
		t.Fatal(err)
	}
	if summary["schema_version"] != "artifact-inspect/v2" || summary["file_count"] != float64(len(files)) ||
		summary["video_count"] != float64(largeReleaseEpisodes) || summary["subtitle_count"] != float64(largeReleaseEpisodes) ||
		summary["other_count"] != float64(largeReleaseEpisodes) {
		t.Fatalf("inventory summary = %s", inventory)
	}
	for _, key := range []string{"files", "inspected_files", "inspected_primary_file"} {
		if _, ok := summary[key]; ok {
			t.Fatalf("inventory summary lists the files under %q", key)
		}
	}

	var completed, other, extras int
	if err := svc.db.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE o.state = 'completed'), COUNT(*) FILTER (WHERE o.state <> 'completed'),
       COUNT(*) FILTER (WHERE f.media_kind NOT IN ('video', 'subtitle'))
FROM file_operations o JOIN artifact_files f ON f.id = o.artifact_file_id WHERE o.artifact_id = $1`, artifactID).Scan(&completed, &other, &extras); err != nil {
		t.Fatal(err)
	}
	if completed != 2*largeReleaseEpisodes || other != 0 || extras != 0 {
		t.Fatalf("file operations: completed=%d other=%d extras=%d, want %d completed media operations\n%s",
			completed, other, extras, 2*largeReleaseEpisodes, describeArtifactEffects(ctx, svc.db, artifactID))
	}
	var placements, companions int
	if err := svc.db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT p.id), COUNT(c.id)
FROM file_operations o
JOIN placements p ON p.source_evidence_type = 'file_operation' AND p.source_owner_ref = o.id
LEFT JOIN placement_companions c ON c.placement_id = p.id AND c.state = 'active'
WHERE o.artifact_id = $1 AND p.managed_anime_id = $2 AND p.state = 'active' AND p.verification_state = 'verified'`, artifactID, animeID).Scan(&placements, &companions); err != nil {
		t.Fatal(err)
	}
	if placements != largeReleaseEpisodes || companions != largeReleaseEpisodes {
		t.Fatalf("placements=%d companions=%d, want every one of the %d videos placed with its subtitle", placements, companions, largeReleaseEpisodes)
	}
	var lastTarget, lastSource string
	if err := svc.db.QueryRowContext(ctx, `
SELECT o.target_path, o.source_path FROM file_operations o JOIN artifact_files f ON f.id = o.artifact_file_id
WHERE o.artifact_id = $1 AND f.media_kind = 'video' ORDER BY f.relative_path DESC LIMIT 1`, artifactID).Scan(&lastTarget, &lastSource); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(lastTarget)
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	for _, file := range files {
		if strings.HasSuffix(lastSource, "/"+file.Name) {
			want = file.Content
		}
	}
	if want == nil || !bytes.Equal(got, want) || !strings.Contains(lastTarget, fmt.Sprintf("E%03d", largeReleaseEpisodes)) {
		t.Fatalf("the last video %s was not placed as episode %d", lastTarget, largeReleaseEpisodes)
	}
	assertNoDuplicateLiveWorkItems(t, ctx, svc.db)
}
