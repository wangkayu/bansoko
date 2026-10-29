package application

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/storedsections"
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"errors"
	"time"
)

// TestReadArtifactsExposesSubtitleBinding proves the Artifact owner read path
// projects the persisted binder decision into ArtifactFile.subtitle_binding.
func TestReadArtifactsExposesSubtitleBinding(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()

	// This projection reads a complete workspace owner (metadata, lifecycle,
	// subgroup/rules owners, and their inherited overrides). The former shared-package
	// fixture accidentally inherited some rows from sibling tests; keep it self-contained.
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-binding', 'Binding Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('subscription-binding', 'anime-binding', 'custom', 'Binding', 'active')`); err != nil {
		t.Fatal(err)
	}
	for range []string{"subgroups", "rules"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-binding') ON CONFLICT (managed_anime_id) DO NOTHING`); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, "anime-binding")
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-binding')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-binding', 'subscription-binding', 'custom_rss', 'https://feed.example.test/rss', 'https://feed.example.test/rss', 'Group', 'Binding Show')`); err != nil {
		t.Fatal(err)
	}

	// The release/task pair has a deferred circular FK; seed it in one
	// transaction like the runtime does.
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ('release-binding', 'source-binding', 'guid:binding', '{"title":"[Group] Binding Show - 01"}', '{}', '{}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, tx, "binding-root", "download-binding", "release-binding", "download:binding", `{}`, `{}`, "completed", `null`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'download-binding' WHERE id = 'release-binding'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('artifact-binding', 'download-binding', (SELECT acquisition_id FROM download_tasks WHERE id='download-binding'), '{}', '{"fingerprints":[]}', 'verified', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('file-binding-video', 'artifact-binding', (SELECT acquisition_id FROM artifacts WHERE id='artifact-binding'), 'primary', 'show.mkv', '/downloads/show.mkv', 'video', 1, '{"algorithm":"metadata","value":"meta:size=1","size_bytes":1}', '{}', 'not_applicable')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state, bound_video_file_ref, subtitle_binding_snapshot)
VALUES ('file-binding-sub', 'artifact-binding', (SELECT acquisition_id FROM artifacts WHERE id='artifact-binding'), 'sub-1', 'show.chs.ass', '/downloads/show.chs.ass', 'subtitle', 1, '{"algorithm":"metadata","value":"meta:size=1","size_bytes":1}', '{}', 'bound', 'primary',
        '{"version":"subtitle-binding-1","method":"basename_match","confidence":1,"diagnostics":[]}')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	service := readmodelNewTestService(t, db, nil)
	workspace, err := service.GetAnimeWorkspaceApplication(ctx, "anime-binding")
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Artifacts) != 1 || len(workspace.Artifacts[0].Files) != 2 {
		t.Fatalf("unexpected artifacts: %+v", workspace.Artifacts)
	}
	byRef := map[string]int{}
	for index, file := range workspace.Artifacts[0].Files {
		byRef[file.Ref] = index
	}
	video := workspace.Artifacts[0].Files[byRef["primary"]]
	if video.SubtitleBinding == nil || video.SubtitleBinding.State != "not_applicable" {
		t.Fatalf("video binding = %+v", video.SubtitleBinding)
	}
	subtitle := workspace.Artifacts[0].Files[byRef["sub-1"]]
	if subtitle.SubtitleBinding == nil {
		t.Fatal("subtitle binding is missing")
	}
	binding := subtitle.SubtitleBinding
	if binding.State != "bound" || binding.BoundVideoFileRef == nil || *binding.BoundVideoFileRef != "primary" {
		t.Fatalf("subtitle binding = %+v", binding)
	}
	if binding.Method == nil || *binding.Method != "basename_match" {
		t.Fatalf("method = %+v", binding.Method)
	}
	if binding.Confidence == nil || *binding.Confidence != 1.0 {
		t.Fatalf("confidence = %+v", binding.Confidence)
	}
	if binding.Diagnostics == nil {
		t.Fatal("diagnostics must serialize as an array")
	}
}

// TestListAnimeEpisodeProgressAggregation proves the episode_progress array
// is what the library holds: the episode keys of the anime's active verified
// placements (a batch placement counts every key), special season 0 and
// unknown keys excluded, gaps preserved, and no placement -> null.
func TestListAnimeEpisodeProgressAggregation(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-progress', 'Progress Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('subscription-progress', 'anime-progress', 'custom', 'Progress', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_metadata(managed_anime_id)
VALUES ('anime-progress')`); err != nil {
		t.Fatal(err)
	}

	// Episodes 1, 2 and 3, the first two from one batch placement.
	animetest.SeedPlacementCoverage(t, db, "anime-progress", "placement-e1-2", "verified", "tv:e1", "tv:e2")
	animetest.SeedPlacementCoverage(t, db, "anime-progress", "placement-e3", "verified", "tv:e3")
	// Every episode key counts, a special included, also in the v1 shape a
	// stored row keeps; a movie key is not episode progress.
	animetest.SeedPlacementCoverage(t, db, "anime-progress", "placement-sp", "verified", "tv:s0:e7")
	animetest.SeedPlacementCoverage(t, db, "anime-progress", "placement-movie", "verified", "movie:0")
	// Episode 5 (gap at 4 -> array keeps [1,2,3,5]).
	animetest.SeedPlacementCoverage(t, db, "anime-progress", "placement-e5", "verified", "tv:e5")
	// An excluded placement of episode 6 is not counted.
	animetest.SeedPlacementCoverage(t, db, "anime-progress", "placement-e6", "excluded", "tv:e6")
	// Another season's file the seasonless key rewrite kept out of the
	// instance's identity (v1 key, not replacement-eligible) counts nothing:
	// the gap at 4 stays.
	animetest.SeedPlacementCoverage(t, db, "anime-progress", "placement-s1-e4", "verified", "tv:s1:e4")
	if _, err := db.ExecContext(ctx, `UPDATE placements SET replacement_eligible = false WHERE id = 'placement-s1-e4'`); err != nil {
		t.Fatal(err)
	}

	// anime-empty has a subscription and source but no placement.
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-empty', 'Empty Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('subscription-empty', 'anime-empty', 'custom', 'Empty', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-empty')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('source-empty', 'subscription-empty', 'custom_rss', 'https://feed.example.test/empty', 'https://feed.example.test/empty', 'Group', 'Empty Show')`); err != nil {
		t.Fatal(err)
	}

	service := readmodelNewTestService(t, db, nil)
	items, err := service.ListAnimeApplication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AnimeListItem{}
	for _, item := range items {
		byID[item.ManagedAnime.ID] = item
	}

	progress := byID["anime-progress"].EpisodeProgress
	if progress == nil {
		t.Fatal("anime-progress episode_progress is nil")
	}
	// Special (EP7) counted, gap at 4 preserved -> [1,2,3,5,7].
	want := []int{1, 2, 3, 5, 7}
	if !reflect.DeepEqual(progress.Episodes, want) {
		t.Fatalf("anime-progress episodes = %v, want %v", progress.Episodes, want)
	}
	if progress.Total != nil {
		t.Fatalf("anime-progress total = %v, want nil (no catalog hint)", *progress.Total)
	}
	if empty := byID["anime-empty"].EpisodeProgress; empty != nil {
		t.Fatalf("anime-empty episode_progress = %+v, want nil", empty)
	}
}

// TestListAnimeCoverURLFromCatalogSnapshot proves the subscribed-list cover
// resolution: cover_url comes from the provider's persisted catalog entry
// (source_hint.cover) matched by subscription origin + origin_entry_ref, so a
// mikan subscription without a BGM binding still has a card image; custom
// subscriptions have no catalog entry and must get a null cover_url.
func TestListAnimeCoverURLFromCatalogSnapshot(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at)
VALUES ('snapshot-mikan-cover', 'mikan', '2026-summer', 'fresh', CURRENT_TIMESTAMP, 'test', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_entries(id, snapshot_id, provider, provider_entry_ref, title, source_hint, state, observed_at)
VALUES ('entry-mikan-cover', 'snapshot-mikan-cover', 'mikan', '4012', 'Cover Show', '{"cover":"https://img.test/cover-4012.jpg"}', 'present', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-cover', 'Cover Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state)
VALUES ('subscription-cover', 'anime-cover', 'mikan', '4012', 'Cover', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-cover')`); err != nil {
		t.Fatal(err)
	}

	// Custom subscription: no provider catalog entry, cover_url must be null.
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-nocover', 'No Cover Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('subscription-nocover', 'anime-nocover', 'custom', 'NoCover', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ('anime-nocover')`); err != nil {
		t.Fatal(err)
	}

	service := readmodelNewTestService(t, db, nil)
	items, err := service.ListAnimeApplication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AnimeListItem{}
	for _, item := range items {
		byID[item.ManagedAnime.ID] = item
	}

	cover := byID["anime-cover"]
	if cover.CoverURL == nil || *cover.CoverURL != "https://img.test/cover-4012.jpg" {
		t.Fatalf("anime-cover cover_url = %v, want provider catalog cover", cover.CoverURL)
	}
	if cover.PosterURL != nil {
		t.Fatalf("anime-cover poster_url = %v, want nil (no BGM binding)", *cover.PosterURL)
	}
	if nocover := byID["anime-nocover"].CoverURL; nocover != nil {
		t.Fatalf("anime-nocover cover_url = %v, want nil (custom has no catalog entry)", nocover)
	}
}

func TestListAnimeExcludesOneShotTemporaryMounts(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()

	seedAgentAnime(t, db, "anime-agent-persistent", "subscription-agent-persistent")
	seedAgentAnime(t, db, "anime-one-shot", "subscription-one-shot")
	if _, err := db.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(
  id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
  correlation_id, request_idempotency_key, request_hash, result_snapshot, state)
VALUES (
  'osa-list-test', 'anime-one-shot', 'subscription-one-shot', 'source-one-shot',
  'release-one-shot', 'download-one-shot', 'corr-one-shot',
  'idempotency-key-1', $1, '{}', 'running')`, "sha256:"+strings.Repeat("0", 64)); err != nil {
		t.Fatalf("seed one-shot marker: %v", err)
	}

	service := readmodelNewTestService(t, db, nil)
	items, err := service.ListAnimeApplication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, item := range items {
		listed[item.ManagedAnime.ID] = true
	}
	if listed["anime-one-shot"] {
		t.Fatal("marker-anchored one-shot mount must not appear in the subscribed list")
	}
	if !listed["anime-agent-persistent"] {
		t.Fatal("persistent oneshot-origin subscription without a marker must stay listed")
	}
}

// seedAgentAnime inserts a minimal oneshot-origin owner triple so ListAnime can
// project it (managed_anime + subscription + metadata).
func seedAgentAnime(t *testing.T, db *sql.DB, animeID, subscriptionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Agent Show')`, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ($1, $2, 'oneshot', 'oneshot-label', 'stopped')`, subscriptionID, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ($1)`, animeID); err != nil {
		t.Fatal(err)
	}
}

// TestGetAnimeWorkspaceProjectsTitleOverride proves the workspace read path
// projects managed_anime.title_override into AnimeMetadata.title_override
// (both schemas declare the field; the metadata tab's save baseline reads
// the metadata side, and a missing projection would leave the tab
// permanently dirty).
func TestGetAnimeWorkspaceProjectsTitleOverride(t *testing.T) {
	db := animetest.OpenTestDB(t)
	readmodel, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	ctx := context.Background()
	subscribe := readmodelSubscribeTestDraft(t, root, "subscribe-key-workspace-title-override")
	animeID := subscribe.ManagedAnime.Id

	workspace, err := readmodel.GetAnimeWorkspaceApplication(ctx, animeID)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	// Subscribe fixes the title: a fresh subscription reads its draft title back.
	if workspace.Metadata.TitleOverride == nil || *workspace.Metadata.TitleOverride != workspace.ManagedAnime.Title {
		t.Fatalf("fresh subscription must carry the fixed title %q, got %+v", workspace.ManagedAnime.Title, workspace.Metadata.TitleOverride)
	}

	override := "工作区覆盖标题"
	if _, err := root.PatchAnimeMetadataApplication(ctx, animeID, ownershipapplication.MetadataPatchInput{
		ExpectedVersion: int64(workspace.Metadata.Version),
		TitleOverride:   &override,
	}); err != nil {
		t.Fatalf("set title override: %v", err)
	}
	workspace, err = readmodel.GetAnimeWorkspaceApplication(ctx, animeID)
	if err != nil {
		t.Fatalf("read workspace after set: %v", err)
	}
	if workspace.Metadata.TitleOverride == nil || *workspace.Metadata.TitleOverride != override {
		t.Fatalf("metadata.title_override must echo the saved override, got %+v", workspace.Metadata.TitleOverride)
	}
	if workspace.ManagedAnime.TitleOverride == nil || *workspace.ManagedAnime.TitleOverride != override {
		t.Fatalf("managed_anime.title_override must echo the saved override, got %+v", workspace.ManagedAnime.TitleOverride)
	}

	if _, err := root.PatchAnimeMetadataApplication(ctx, animeID, ownershipapplication.MetadataPatchInput{
		ExpectedVersion: int64(workspace.Metadata.Version),
		TitleOverride:   nil,
	}); err != nil {
		t.Fatalf("null title override: %v", err)
	}
	workspace, err = readmodel.GetAnimeWorkspaceApplication(ctx, animeID)
	if err != nil {
		t.Fatalf("read workspace after null: %v", err)
	}
	// A null keeps the fixed title; it is never cleared.
	if workspace.Metadata.TitleOverride == nil || *workspace.Metadata.TitleOverride != override {
		t.Fatalf("metadata.title_override must keep the fixed title, got %+v", workspace.Metadata.TitleOverride)
	}
	if workspace.ManagedAnime.TitleOverride == nil || *workspace.ManagedAnime.TitleOverride != override {
		t.Fatalf("managed_anime.title_override must keep the fixed title, got %+v", workspace.ManagedAnime.TitleOverride)
	}
}

// TestPlacementHandoffProjectedInWorkspace proves the workspace owner-chain read
// model exposes the durable placement stage of a completed file operation with
// only user-understandable facts: the public stage state (`admitted` becomes
// `completed`), the stable reason, the attempt budget and the live retry due
// time. The file operation itself keeps its own completed state.
func TestPlacementHandoffProjectedInWorkspace(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribed := readmodelSubscribeTestDraft(t, root, "subscribe-key-placement-handoff")
	sourceID := animetest.ReadWorkspaceSourceID(t, db, subscribed.Subscription.Id)
	animetest.SeedCompletedFileOp(t, db, sourceID, 1, false, "completed")
	animetest.SeedPlacementHandoff(t, db, "op-rel-ep-1", "waiting_retry", 2, 3, "available")

	workspace, err := service.GetAnimeWorkspaceApplication(ctx, subscribed.ManagedAnime.Id)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if len(workspace.PlacementHandoffs) != 1 {
		t.Fatalf("workspace must project one placement handoff, got %d", len(workspace.PlacementHandoffs))
	}
	handoff := workspace.PlacementHandoffs[0]
	if handoff.FileOperationID != "op-rel-ep-1" || handoff.State != "waiting_retry" {
		t.Fatalf("placement handoff = %s/%s", handoff.FileOperationID, handoff.State)
	}
	if handoff.Attempts != 2 || handoff.AttemptBudget != 3 {
		t.Fatalf("attempt budget = %d/%d, want 2/3", handoff.Attempts, handoff.AttemptBudget)
	}
	if handoff.ReasonCode == nil || *handoff.ReasonCode != "placement_admission_failed" {
		t.Fatalf("stable reason must be projected, got %v", handoff.ReasonCode)
	}
	if handoff.NextRetryAt == nil {
		t.Fatal("a waiting_retry stage must project the live retry due time")
	}
	for _, operation := range workspace.FileOperations {
		if operation.ID == "op-rel-ep-1" && operation.State != "completed" {
			t.Fatalf("the file operation must stay completed, got %q", operation.State)
		}
	}
	// The settled state maps to the user-facing `completed`, never `admitted`.
	if _, err := db.ExecContext(ctx, `
UPDATE placement_obligations
SET state = 'admitted', settled_at = CURRENT_TIMESTAMP, reason_code = NULL, reason = NULL
WHERE file_operation_id = 'op-rel-ep-1'`); err != nil {
		t.Fatal(err)
	}
	workspace, err = service.GetAnimeWorkspaceApplication(ctx, subscribed.ManagedAnime.Id)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.PlacementHandoffs) != 1 || workspace.PlacementHandoffs[0].State != "completed" {
		t.Fatalf("admitted obligation must project as completed, got %+v", workspace.PlacementHandoffs)
	}
	if workspace.PlacementHandoffs[0].NextRetryAt != nil {
		t.Fatal("a settled stage must not project a retry due time")
	}
}

func TestGetAnimeWorkspaceReadModel(t *testing.T) {
	db := animetest.OpenTestDB(t)
	readmodel, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribe := readmodelSubscribeTestDraft(t, root, "subscribe-key-0012")
	animeID := subscribe.ManagedAnime.Id
	workspace, err := readmodel.GetAnimeWorkspaceApplication(context.Background(), animeID)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if workspace.ManagedAnime.ID != animeID || workspace.Subscription.State != "active" {
		t.Fatalf("workspace must return the owner chain: %+v", workspace.ManagedAnime)
	}
	if workspace.MetadataHold.Held {
		t.Fatalf("complete metadata must not hold: %+v", workspace.MetadataHold)
	}
	if len(workspace.RSSSources) != 1 || len(workspace.RSSSourceRuntimes) != 1 {
		t.Fatalf("workspace must list sources and runtimes: %+v / %+v", workspace.RSSSources, workspace.RSSSourceRuntimes)
	}
	if workspace.Rules.Effective.Naming.Tv.VideoFilenameTemplate == "" {
		t.Fatalf("workspace must resolve effective rules: %+v", workspace.Rules.Effective)
	}
	if workspace.Subgroups.Subgroups.MaxRss != 3 {
		t.Fatalf("workspace must resolve the default selection: %+v", workspace.Subgroups.Subgroups)
	}
	if workspace.Subgroups.Subgroups.Entries == nil {
		t.Fatal("subgroup entries must serialize as an empty array, not null")
	}

	internalFacts, err := json.Marshal(map[string]any{
		"release_files": []map[string]any{
			{"source_path": "[Group] Anime - 01 [1080p].mkv", "filename": "[Group] Anime - 01 [1080p].mkv", "size_bytes": 123456789, "media_kind": "video"},
			{"source_path": "subs/CHS.ass", "filename": "CHS.ass", "size_bytes": 12345, "media_kind": "subtitle"},
		},
	})
	if err != nil {
		t.Fatalf("marshal internal facts: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts, verdict, reason_code)
VALUES ('rel-files', $1, 'key-files', '{}', '{}', $2, 'accepted_pending_download', 'accepted')`, subscribe.RssSources[0].Id, internalFacts); err != nil {
		t.Fatalf("insert release with files: %v", err)
	}
	workspace, err = readmodel.GetAnimeWorkspaceApplication(context.Background(), animeID)
	if err != nil {
		t.Fatalf("workspace with release files: %v", err)
	}
	var found *Release
	for _, r := range workspace.Releases {
		if r.ID == "rel-files" {
			found = &r
			break
		}
	}
	if found == nil {
		t.Fatalf("release with files missing from workspace")
	}
	if found.ReleaseFiles == nil || len(*found.ReleaseFiles) != 2 {
		t.Fatalf("release_files missing or wrong length: %+v", found.ReleaseFiles)
	}
	files := *found.ReleaseFiles
	if files[0].Path != "[Group] Anime - 01 [1080p].mkv" || files[0].MediaKind != "video" || files[0].SizeBytes == nil || *files[0].SizeBytes != 123456789 {
		t.Fatalf("first release file mismatch: %+v", files[0])
	}
	if files[1].Path != "subs/CHS.ass" || files[1].MediaKind != "subtitle" {
		t.Fatalf("second release file mismatch: %+v", files[1])
	}

	older := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO subscription_log_entries(
    id, subscription_id, managed_anime_id, kind, outcome, occurred_at,
    message_code, message_params, rss_source_id, release_id, group_ref, details,
    dedupe_key, retention_until
)
VALUES
    ('log-old', $1, $2, 'rss_new_content', 'info', $3::timestamptz,
     'rss_new_content', '{"count":1}'::jsonb, $4, NULL, NULL,
     '{"item_count":1}'::jsonb, 'dedupe-old', $3::timestamptz + INTERVAL '30 days'),
    ('log-new', $1, $2, 'policy_changed', 'failure', $5::timestamptz,
     'episode_offset_changed', '{"group_name":"Test Group"}'::jsonb, $4, NULL, 'test-group',
     '{"reason_code":"feed_fetch_failed","error_count":1}'::jsonb,
     'dedupe-new', $5::timestamptz + INTERVAL '30 days')`,
		subscribe.Subscription.Id, animeID, older, subscribe.RssSources[0].Id, newer); err != nil {
		t.Fatalf("insert subscription logs: %v", err)
	}
	beforeLogCount := animetest.CountRows(t, db, "subscription_log_entries")
	workspace, err = readmodel.GetAnimeWorkspaceApplication(context.Background(), animeID)
	if err != nil {
		t.Fatalf("workspace with logs: %v", err)
	}
	if after := animetest.CountRows(t, db, "subscription_log_entries"); after != beforeLogCount {
		t.Fatalf("workspace GET must not write subscription logs: before=%d after=%d", beforeLogCount, after)
	}
	if len(workspace.Logs) != 3 || workspace.Logs[0].Kind != "subscription_created" || workspace.Logs[1].ID != "log-new" || workspace.Logs[2].ID != "log-old" {
		t.Fatalf("workspace logs must be newest first: %+v", workspace.Logs)
	}
	newest := workspace.Logs[1]
	if newest.MessageCode != "episode_offset_changed" || newest.MessageParams.GroupName == nil || *newest.MessageParams.GroupName != "Test Group" ||
		newest.Source != "subscription" || newest.AnimeTitle != workspace.ManagedAnime.Title {
		t.Fatalf("newest subscription log message was not mapped: %+v", newest)
	}
	if older := workspace.Logs[2]; older.MessageParams.Count == nil || *older.MessageParams.Count != 1 {
		t.Fatalf("older subscription log params were not mapped: %+v", older.MessageParams)
	}
	if newest.Kind != "policy_changed" || newest.Outcome != "failure" || newest.RSSSourceID == nil || *newest.RSSSourceID != subscribe.RssSources[0].Id || newest.GroupRef == nil || *newest.GroupRef != "test-group" {
		t.Fatalf("newest subscription log was not mapped safely: %+v", newest)
	}
	if newest.Details == nil || newest.Details.ReasonCode == nil || *newest.Details.ReasonCode != "feed_fetch_failed" || newest.Details.ErrorCount == nil || *newest.Details.ErrorCount != 1 {
		t.Fatalf("subscription log details were not decoded: %+v", newest.Details)
	}
	if workspace.Logs[2].ReleaseID != nil || workspace.Logs[2].GroupRef != nil || workspace.Logs[2].Details == nil || workspace.Logs[2].Details.ItemCount == nil || *workspace.Logs[2].Details.ItemCount != 1 {
		t.Fatalf("nullable subscription log fields were not mapped: %+v", workspace.Logs[2])
	}

	if _, err := readmodel.GetAnimeWorkspaceApplication(context.Background(), "missing-anime"); err == nil {
		t.Fatal("unknown anime must be a typed not-found")
	} else {
		var notFound *apperrors.NotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("expected apperrors.NotFound, got %v", err)
		}
	}
}

// TestWorkspaceBatchedReleaseReads proves the batched release reads agree with
// the per-release shape they replaced: each release carries exactly its own
// target set and observations, the subscription-wide lists hold the same rows
// (observations newest first), and a per-source limit keeps the newest releases
// of each source while skipping the sections the RSS list never renders.
func TestWorkspaceBatchedReleaseReads(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribe := readmodelSubscribeTestDraft(t, root, "subscribe-key-batched")
	animeID := subscribe.ManagedAnime.Id
	sourceID := subscribe.RssSources[0].Id
	owner := disposabledb.Owner(t)
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= 4; i++ {
		id := "rel-batch-" + string(rune('0'+i))
		if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts, verdict, reason_code, discovered_at)
VALUES ($1, $2, $3, '{}', '{}', '{}', 'discovered', 'discovered', $4)`, id, sourceID, "key-"+id, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("insert release %s: %v", id, err)
		}
		// Releases 1 and 2 have targets; release 2 has two, release 1 one observation.
		for target := 0; target < i && i <= 2; target++ {
			targetID := "tg-" + id + "-" + string(rune('0'+target))
			if _, err := owner.ExecContext(ctx, `
INSERT INTO acquisition_targets(id, release_id, target_ref, source_path, media_kind, target_path, expected_size_bytes, intent,
                                collision_policy, render_snapshot, config_snapshot, spec_digest, version)
VALUES ($1, $2, $3, 'a.mkv', 'video', '/lib/'||$1||'.mkv', 10, 'ensure_present', 'block', '{}', '{}', $4, 1)`,
				targetID, id, "file-"+string(rune('0'+target)), strings.Repeat("d", 64)); err != nil {
				t.Fatalf("insert target %s: %v", targetID, err)
			}
			if i == 1 || target == 0 {
				if _, err := owner.ExecContext(ctx, `
UPDATE acquisition_targets
SET observation_state = 'present_matched', observed_size_bytes = 10, observed_at = $2, observation_version = 1
WHERE id = $1`, targetID, base.Add(time.Duration(i)*time.Minute)); err != nil {
					t.Fatalf("insert observation %s: %v", targetID, err)
				}
			}
		}
	}

	workspace, err := service.GetAnimeWorkspaceApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.AcquisitionTargets == nil || workspace.TargetObservations == nil || len(*workspace.AcquisitionTargets) != 3 || len(*workspace.TargetObservations) != 2 {
		t.Fatalf("subscription-wide targets/observations = %+v / %+v", workspace.AcquisitionTargets, workspace.TargetObservations)
	}
	if observations := *workspace.TargetObservations; observations[0].TargetID != "tg-rel-batch-2-0" || observations[1].TargetID != "tg-rel-batch-1-0" {
		t.Fatalf("workspace observations must be newest first: %+v", observations)
	}
	byID := map[string]Release{}
	for _, release := range workspace.Releases {
		byID[release.ID] = release
	}
	if r := byID["rel-batch-2"]; r.AcquisitionTargets == nil || len(*r.AcquisitionTargets) != 2 || r.TargetObservations == nil || len(*r.TargetObservations) != 1 {
		t.Fatalf("release 2 targets/observations = %+v / %+v", r.AcquisitionTargets, r.TargetObservations)
	}
	if r := byID["rel-batch-1"]; r.AcquisitionTargets == nil || len(*r.AcquisitionTargets) != 1 || (*r.AcquisitionTargets)[0].ReleaseID != "rel-batch-1" || r.TargetObservations == nil || len(*r.TargetObservations) != 1 {
		t.Fatalf("release 1 targets/observations = %+v / %+v", r.AcquisitionTargets, r.TargetObservations)
	}
	if r := byID["rel-batch-3"]; r.AcquisitionTargets != nil || r.TargetObservations != nil {
		t.Fatalf("a release without targets must carry none: %+v / %+v", r.AcquisitionTargets, r.TargetObservations)
	}

	lean, err := service.readAnimeWorkspace(ctx, animeID, workspaceReadOptions{releasesPerSource: 2, releasesOnly: true, varsEvidence: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(lean.Releases) != 2 || lean.Releases[0].ID != "rel-batch-4" || lean.Releases[1].ID != "rel-batch-3" {
		t.Fatalf("per-source limit must keep the newest releases first: %+v", lean.Releases)
	}
	if lean.Releases[0].rawSourceFacts == nil || lean.AcquisitionTargets != nil || len(lean.DownloadTasks) != 0 || len(lean.Logs) != 0 {
		t.Fatalf("a releases-only read must skip the other sections: %+v", lean)
	}
	if len(lean.RSSSources) != len(workspace.RSSSources) || lean.Rules.Version != workspace.Rules.Version {
		t.Fatalf("a releases-only read must keep the owner chain: %+v", lean.RSSSources)
	}
}
