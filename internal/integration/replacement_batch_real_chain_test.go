package integration

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"bansoko/internal/transport/httpapi/generated"
)

// subscribeRealChainShow subscribes one show to the fixture feed with the
// given naming rules (and, when overwrite, a move/overwrite file effect), then
// binds TMDB season 1; episode keys are tv:eN. It returns the managed
// anime and RSS source ids.
func subscribeRealChainShow(t *testing.T, ctx context.Context, svc *realChainServices, feedURL, title string, rules generated.NamingRules, overwrite bool) (string, string) {
	t.Helper()
	draft := singleEpisodeDraft(feedURL)
	draft.Title = title
	draft.RssSources[0].Title = title
	draft.Subgroups.Entries[0].GroupRef = "1"
	draft.Subgroups.Entries[0].DisplayName = "RealGroup"
	var namingOverride generated.NamingRulesOverride
	if err := namingOverride.FromNamingRulesReplace(generated.NamingRulesReplace{Mode: generated.NamingRulesReplaceModeReplace, Value: rules}); err != nil {
		t.Fatal(err)
	}
	draft.Rules.Sections.Naming = &namingOverride
	if overwrite {
		var fileEffect generated.FileEffectRulesOverride
		if err := fileEffect.FromFileEffectRulesReplace(generated.FileEffectRulesReplace{
			Mode:  generated.FileEffectRulesReplaceModeReplace,
			Value: generated.FileEffectRules{Operation: generated.FileEffectRulesOperationMove, CollisionPolicy: generated.CollisionPolicyOverwrite},
		}); err != nil {
			t.Fatal(err)
		}
		draft.Rules.Sections.FileEffect = &fileEffect
	}
	draft = withDraftBusiness(t, svc.db, draft)
	saveStatus, candidate := doJSON[generated.AnimeConfigCandidate](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates", generated.SaveAnimeConfigCandidateInput{Draft: draft}, nil)
	if saveStatus != http.StatusCreated || candidate.Id == "" || candidate.CurrentRevision == nil {
		t.Fatalf("candidate save failed: status=%d", saveStatus)
	}
	subscribeStatus, result := doJSON[generated.SubscribeResult](t, svc.httpHandler, http.MethodPost, "/api/v1/anime-config-candidates/"+candidate.Id+"/subscribe", generated.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: new(true)}, map[string]string{"Idempotency-Key": "subscribe-" + candidate.Id, "If-Match": "1"})
	if subscribeStatus != http.StatusCreated || result.ManagedAnime.Id == "" || len(result.RssSources) != 1 {
		t.Fatalf("subscribe failed: status=%d", subscribeStatus)
	}
	animeID := result.ManagedAnime.Id
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ownership.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: 1,
		Bgm:             &generated.NullableBGMBinding{WorkId: "bgm-" + animeID},
		Tmdb:            &generated.NullableTMDBBinding{WorkId: "tmdb-" + animeID, MediaKind: generated.TMDBMediaKindTv, Season: 1},
		// The naming season is the owner value, not the TMDB season.
		Season: new(1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	return animeID, result.RssSources[0].Id
}

// TestReplacementRealChainBatchReplacesPerFile proves per-file replacement in
// a batch: Revision A (720p, E01) is placed first; batch Revision B
// (1080p, E01 and E02) replaces E01 in place under one approved overwrite and
// fills the missing E02 as an ordinary ensure_present file, in one
// acquisition. E01's decision settles and supersedes A; E02 has no loser.
func TestReplacementRealChainBatchReplacesPerFile(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	items := map[string]rssItem{
		"batch-a": {GUID: "repl-batch-a-guid", Title: "[RealGroup] Repl Batch Show - 01 [720p]"},
		"batch-b": {GUID: "repl-batch-b-guid", Title: "[RealGroup] Repl Batch Show [01-02][1080p]"},
	}
	filesA := []torrentFile{
		{Name: "Repl Batch Show - 01.mkv", Content: []byte("revision A 720p E01 " + stamp)},
		{Name: "Repl Batch Show - 01.nfo", Content: []byte("revision A nfo")},
	}
	filesB := []torrentFile{
		{Name: "Repl Batch Show - 01.mkv", Content: []byte("revision B 1080p E01 larger payload " + stamp)},
		{Name: "Repl Batch Show - 02.mkv", Content: []byte("revision B 1080p E02 larger payload " + stamp)},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"batch-a": filesA, "batch-b": filesB})
	fixture.SetCurrent("batch-a")
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedReplacementSingleCopy(t, svc.db)
	animeID, sourceID := subscribeRealChainShow(t, ctx, svc, fixture.FeedURL(), "Repl Batch Show", flatNamingRules(svc.targetRoot), true)

	svc.driveToPlacement(t, ctx, animeID, "batch-a", filesA, 1)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	var placementA string
	if err := svc.db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementA); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(placementA, "Repl Batch Show/Repl Batch Show - E01@RealGroup.mkv") {
		t.Fatalf("revision A placement path is wrong: %q", placementA)
	}

	fixture.SetCurrent("batch-b")
	triggerRefresh(t, ctx, svc, sourceID, animeID)
	svc.driveToPlacement(t, ctx, animeID, "batch-b", filesB, 3)
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	waitForPlacementCount(t, ctx, svc.db, animeID, 2)

	// One acquisition, input schema 7, one approval: only E01 had a present
	// incumbent.
	var schema, approvals int
	if err := svc.db.QueryRowContext(ctx, `
SELECT a.input_schema_version, jsonb_array_length(a.input->'delivery'->'replacements')
FROM acquisitions a
JOIN releases r ON r.id = a.source_release_ref
WHERE r.stable_item_key = 'repl-batch-b-guid'`).Scan(&schema, &approvals); err != nil {
		t.Fatal(err)
	}
	if schema != 1 || approvals != 1 {
		t.Fatalf("batch acquisition input schema=%d approvals=%d, want 1 and 1", schema, approvals)
	}
	// E01 is a replace operation; the absent E02 target is an ordinary
	// ensure_present one.
	rows, err := svc.db.QueryContext(ctx, `
SELECT fo.target_path, fo.intent
FROM file_operations fo
JOIN artifacts a ON a.id = fo.artifact_id
JOIN artifact_files af ON af.id = fo.artifact_file_id
JOIN download_tasks dt ON dt.id = a.download_task_id
JOIN releases r ON r.id = dt.release_id
WHERE r.stable_item_key = 'repl-batch-b-guid' AND af.media_kind = 'video'`)
	if err != nil {
		t.Fatal(err)
	}
	intents := map[string]string{}
	for rows.Next() {
		var target, intent string
		if err := rows.Scan(&target, &intent); err != nil {
			t.Fatal(err)
		}
		intents[target[strings.LastIndex(target, "/")+1:]] = intent
	}
	rows.Close()
	if intents["Repl Batch Show - E01@RealGroup.mkv"] != "replace" || intents["Repl Batch Show - E02@RealGroup.mkv"] != "ensure_present" {
		t.Fatalf("batch file intents = %v, want E01 replace and E02 ensure_present", intents)
	}

	// A is superseded by B's E01; both active placements are B's.
	var superseded int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1 AND state = 'superseded'`, animeID).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if superseded != 1 {
		t.Fatalf("revision A placement must be superseded, got %d", superseded)
	}
	var activeFromB int
	if err := svc.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM placements p JOIN releases r ON r.id = p.quality_facts_ref
WHERE p.managed_anime_id = $1 AND p.state = 'active' AND r.stable_item_key = 'repl-batch-b-guid'`, animeID).Scan(&activeFromB); err != nil {
		t.Fatal(err)
	}
	if activeFromB != 2 {
		t.Fatalf("both active placements must come from batch B, got %d", activeFromB)
	}
	// E01's per-key decision settled and superseded A.
	var e1State string
	if err := svc.db.QueryRowContext(ctx, `
SELECT d.state FROM replacement_decisions d JOIN releases r ON r.id = d.candidate_release_id
WHERE d.managed_anime_id = $1 AND r.stable_item_key = 'repl-batch-b-guid' AND d.coverage_key = 'tv:e1' AND d.state <> 'stale'
ORDER BY d.created_at DESC, d.id DESC LIMIT 1`, animeID).Scan(&e1State); err != nil {
		t.Fatal(err)
	}
	if e1State != "settled" {
		t.Fatalf("E01 decision state = %q, want settled", e1State)
	}
	// The in-place loser shares the active path, so it never enters cleanup.
	var cleanupCount int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM replacement_deletions WHERE managed_anime_id = $1`, animeID).Scan(&cleanupCount); err != nil {
		t.Fatal(err)
	}
	if cleanupCount != 0 {
		t.Fatalf("same-path loser must not enter cleanup, got %d operations", cleanupCount)
	}
	targetBytes, err := os.ReadFile(placementA)
	if err != nil {
		t.Fatalf("E01 target missing after replacement: %v", err)
	}
	if !bytes.Contains(targetBytes, []byte("revision B 1080p E01")) {
		t.Fatalf("E01 target does not hold the batch bytes: %q", targetBytes)
	}
}

// multiEpisodeNamingRules is the flat template plus the multi-episode suffix
// of the shipped default (E03-E04).
func multiEpisodeNamingRules(root string) generated.NamingRules {
	rules := flatNamingRules(root)
	rules.Tv.VideoFilenameTemplate = "${sanitize(anime.title)} - E${pad(ep.display, 2)}${ep.number_end ? `-E${pad(ep.number_end, 2)}` : ''}@${anime.group}.${file.ext}"
	return rules
}

// TestMultiEpisodeFileRealChain proves a double-episode file: the file
// name range 03-04 resolves to two coverage keys, the file is placed once on
// both keys, and ep.number_end names it E03-E04.
func TestMultiEpisodeFileRealChain(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	items := map[string]rssItem{
		"double-d": {GUID: "double-d-guid", Title: "[RealGroup] Repl Double Show - 03-04 [1080p]"},
	}
	files := []torrentFile{
		{Name: "Repl Double Show - 03-04.mkv", Content: []byte("double episode video " + stamp)},
		{Name: "Repl Double Show - 03-04.nfo", Content: []byte("double episode nfo")},
	}
	fixture := startRevisionFixture(t, items, map[string][]torrentFile{"double-d": files})
	fixture.SetCurrent("double-d")
	t.Cleanup(fixture.Close)

	svc := newRealChainServices(t, fixture.client)
	defer svc.db.Close()
	ctx := context.Background()
	seedReplacementSingleCopy(t, svc.db)
	animeID, _ := subscribeRealChainShow(t, ctx, svc, fixture.FeedURL(), "Repl Double Show", multiEpisodeNamingRules(svc.targetRoot), false)

	svc.driveToPlacement(t, ctx, animeID, "double-d", files, 1)
	waitForPlacementCount(t, ctx, svc.db, animeID, 1)
	var placementID, path string
	if err := svc.db.QueryRowContext(ctx, `SELECT id, physical_path FROM placements WHERE managed_anime_id = $1 AND state = 'active'`, animeID).Scan(&placementID, &path); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "Repl Double Show/Repl Double Show - E03-E04@RealGroup.mkv") {
		t.Fatalf("double-episode placement path = %q", path)
	}
	var coverage string
	if err := svc.db.QueryRowContext(ctx, `SELECT string_agg(observation_key, ',' ORDER BY observation_key) FROM placement_coverage WHERE placement_id = $1`, placementID).Scan(&coverage); err != nil {
		t.Fatal(err)
	}
	if coverage != "tv:e3,tv:e4" {
		t.Fatalf("double-episode coverage = %q, want tv:e3,tv:e4", coverage)
	}
}
