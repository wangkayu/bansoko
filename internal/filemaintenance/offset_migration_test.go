package filemaintenance

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

type fakeRulesSource struct{}

func (fakeRulesSource) EffectiveNamingRules(context.Context, string) (naming.NamingRules, error) {
	return frozenRulesForTest(), nil
}

func seedOffsetMigrationChain(t *testing.T, db *sql.DB, rawEpisode string) {
	t.Helper()
	ctx := context.Background()
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	configSnapshot := string(frozenConfigSnapshotForTest(t, frozenRulesForTest()))
	if _, err := tx.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-offmig', 'Offset Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season)
VALUES ('anime-offmig', 'tmdb-1', 'tv', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-offmig', 'anime-offmig', 'custom', 'Offset', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-offmig', 'sub-offmig', 'custom_rss', 'https://feeds.example.com/rss.xml', 'https://feeds.example.com/rss.xml', 'Group', 'Offset Anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, internal_facts, verdict, reason_code, download_task_id, version)
VALUES ('rel-offmig', 'src-offmig', 'guid:offmig', '{"title":"Offset Anime"}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, 'submitted', 'submitted', 'task-offmig', 1)`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-offmig", "task-offmig", "rel-offmig", "qb:offmig", `{}`, `{}`, "completed", `null`, `[{"file_ref":"primary","ordinal":0,"relative_path":"Offset Anime - 01.mkv","exact_size":1024,"media_kind":"video","identity_evidence":{}}]`)
	if _, err := tx.ExecContext(ctx, `UPDATE download_tasks SET correlation_id = 'corr-offmig', causation_type = 'release', causation_id = 'rel-offmig' WHERE id = 'task-offmig'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at, correlation_id, causation_type, causation_id)
VALUES ('art-offmig', 'task-offmig', (SELECT acquisition_id FROM download_tasks WHERE id='task-offmig'), '{}'::jsonb, '{}'::jsonb, 'verified', CURRENT_TIMESTAMP, 'corr-offmig', 'download_task', 'task-offmig')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('af-offmig', 'art-offmig', (SELECT acquisition_id FROM artifacts WHERE id='art-offmig'), 'primary', 'Offset Anime - 01.mkv', '/downloads/Offset Anime - 01.mkv', 'video', 1024,
        '{"algorithm":"sha256","value":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'::jsonb, '{}'::jsonb, 'not_applicable')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.CommitOperations(t, tx, "art-offmig", []acquisitionfixture.PlanOperation{{ID: "op-offmig", FileID: "af-offmig", FileRef: "primary", Source: "/downloads/Offset Anime - 01.mkv", Target: "/library/Offset Anime/Season 01/Offset Anime - 01 [Group].mkv", Operation: "move", Key: "op-offmig", Size: 1024, Fingerprint: json.RawMessage(`{"algorithm":"sha256","value":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), Render: json.RawMessage(`{}`), Config: json.RawMessage(configSnapshot)}})
	if _, err := tx.ExecContext(ctx, `UPDATE file_operations SET state='completed',observed_at=CURRENT_TIMESTAMP,target_fingerprint=source_fingerprint,correlation_id='corr-offmig',causation_type='artifact',causation_id='art-offmig' WHERE id='op-offmig'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id='op-offmig'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint,
 target_fingerprint, quality_facts_ref, facts_version, correlation_id, causation_type, causation_id, replacement_eligible, verification_state, state, version)
VALUES ('plc-offmig', '/library/Offset Anime/Season 01/Offset Anime - 01 [Group].mkv', 'anime-offmig', 'file_operation', 'op-offmig',
 '{"algorithm":"sha256","value":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'::jsonb, '{"algorithm":"sha256","value":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'::jsonb, 'rel-offmig', 'facts-v1',
 'corr-offmig', 'file_operation', 'op-offmig', true, 'verified', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	// Media fact snapshot with the frozen raw episode for the video file.
	identity := episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode,
		RawEpisode: ptrEp(rawEpisode), Offset: 0, Episode: ptrEp(rawEpisode),
	}
	source := episodeidentity.EpisodeSourceFileParser
	identity.SelectedSource = &source
	coverage := "tv:s1:e" + rawEpisode
	identity.CoverageKey = &coverage
	identityRaw, _ := json.Marshal(identity)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snap-offmig', 'rel-offmig', 'media-facts-v1', 'episode-identity-v1', 'regex', 'digest-offmig', $1::jsonb, '{"title":"Offset Anime"}'::jsonb)`,
		identityRaw); err != nil {
		t.Fatal(err)
	}
	effective, _ := json.Marshal(map[string]any{
		"identity": identity,
	})
	evidence, _ := json.Marshal(map[string]any{"name": "Offset Anime - 01.mkv", "media_kind": "video", "size_bytes": 1024})
	if _, err := tx.ExecContext(ctx, `
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ('snap-offmig', 'primary', 'video', 1024, 'fp-offmig', $1::jsonb, $2::jsonb)`,
		evidence, effective); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	freezeRenderFixtureBusiness(t, db, "op-offmig")
}

func ptrEp(value string) *episodeidentity.EpisodeNumber {
	number := episodeidentity.EpisodeNumber(value)
	return &number
}

func TestPlanGenerationUsesFrozenIdentityKindNotCurrentMetadata(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")

	admit := func(policyVersion int64, offset int) string {
		t.Helper()
		admission := NewOffsetMigrationAdmissionService()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		generationID, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", policyVersion, 0, offset)
		if err != nil || !created || generationID == "" {
			t.Fatalf("admit link generation id=%q created=%v err=%v", generationID, created, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return generationID
	}
	seedFrozenIdentity := func(identity episodeidentity.EpisodeIdentity) {
		t.Helper()
		effective, err := json.Marshal(map[string]any{"identity": identity})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
UPDATE media_fact_files SET effective_facts = $1::jsonb WHERE snapshot_id = 'snap-offmig'`, effective); err != nil {
			t.Fatal(err)
		}
	}

	// The frozen file identity is the canonical movie identity while the
	// current metadata row still says tv.
	movieEpisode := episodeidentity.EpisodeNumber("0")
	movieCoverage := "movie:0"
	movieSource := episodeidentity.EpisodeSourceMovie
	seedFrozenIdentity(episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindMovie,
		RawEpisode: &movieEpisode, Episode: &movieEpisode, CoverageKey: &movieCoverage,
		SelectedSource: &movieSource,
	})
	renderer := NewNamingTargetRenderer(db, fakeRulesSource{})
	service := NewOffsetMigrationService(db, renderer)
	movieGeneration := admit(1, 1)
	if _, err := service.PlanGeneration(ctx, movieGeneration); err != nil {
		t.Fatalf("plan movie generation: %v", err)
	}
	var state, reason string
	if err := db.QueryRowContext(ctx, `
SELECT state, COALESCE(reason_code, '') FROM offset_migration_plans WHERE generation_id = $1`, movieGeneration).
		Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != string(OffsetMigrationOperationBlocked) || reason != "movie_noop" {
		t.Fatalf("frozen movie placement = %s/%s, want blocked/movie_noop", state, reason)
	}

	// The reverse rebinding: a frozen TV identity with a current movie metadata
	// row still plans the TV offset change.
	if _, err := db.ExecContext(ctx, `
UPDATE anime_metadata SET tmdb_media_kind = 'movie', tmdb_season = 0, version = version + 1
WHERE managed_anime_id = 'anime-offmig'`); err != nil {
		t.Fatal(err)
	}
	tvEpisode := episodeidentity.EpisodeNumber("1")
	tvCoverage := "tv:s1:e1"
	tvSource := episodeidentity.EpisodeSourceFileParser
	seedFrozenIdentity(episodeidentity.EpisodeIdentity{
		State: episodeidentity.StateResolved, Kind: episodeidentity.KindTVEpisode,
		RawEpisode: &tvEpisode, Episode: &tvEpisode, CoverageKey: &tvCoverage,
		SelectedSource: &tvSource,
	})
	tvGeneration := admit(2, 2)
	if _, err := service.PlanGeneration(ctx, tvGeneration); err != nil {
		t.Fatalf("plan tv generation: %v", err)
	}
	var mediaKind, newEpisode string
	if err := db.QueryRowContext(ctx, `
SELECT media_kind, COALESCE(new_episode, '') FROM offset_migration_plans WHERE generation_id = $1`, tvGeneration).
		Scan(&mediaKind, &newEpisode); err != nil {
		t.Fatal(err)
	}
	if mediaKind != "tv" || newEpisode != "3" {
		t.Fatalf("frozen tv placement planned as %q/%q, want tv/3 (raw 1 + offset 2; the current metadata movie binding must not win)", mediaKind, newEpisode)
	}
}

func TestOffsetMigrationOwnerAPI(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")

	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeRulesSource{}))
	service := NewService(nil, nil, nil, nil, nil)
	service.SetOffsetMigration(planner)

	// Plan through the durable chain (admission inside the policy CAS
	// transaction, then the planner), then read through the owner service.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, created, err := NewOffsetMigrationAdmissionService().AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil || !created {
		t.Fatalf("admit generation created=%v err=%v", created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.PlanGeneration(ctx, generationID); err != nil {
		t.Fatalf("plan generation: %v", err)
	}
	views, err := service.ListOffsetMigrations(ctx, "anime-offmig", nil, 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("listed=%d, want 1", len(views))
	}
	if views[0].State != "ready" || views[0].NewEpisode != "2" {
		t.Fatalf("view state=%s episode=%s, want ready/2", views[0].State, views[0].NewEpisode)
	}

	// Filter by policy version.
	views, err = service.ListOffsetMigrations(ctx, "anime-offmig", int64Ptr(1), 100, 0)
	if err != nil || len(views) != 1 {
		t.Fatalf("filtered list=%d err=%v, want 1", len(views), err)
	}
	views, err = service.ListOffsetMigrations(ctx, "anime-offmig", int64Ptr(99), 100, 0)
	if err != nil || len(views) != 0 {
		t.Fatalf("wrong policy list=%d err=%v, want 0", len(views), err)
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}
