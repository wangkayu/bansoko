package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/postprocessing/naming"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"os"
	"path/filepath"

	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/testutil/mediafactsfixture"
)

// seedUnresolvedFactSnapshot freezes a media fact snapshot whose video file
// identity is unresolved, so the renderer has no frozen episode to work with,
// with the batch plan accepted under the given unknown-file route.
func seedUnresolvedFactSnapshot(t *testing.T, db *sql.DB, size int64, fingerprint, route string) {
	t.Helper()
	ctx := context.Background()
	evidence, _ := json.Marshal(map[string]any{"name": "source.mkv", "media_kind": "video", "size_bytes": size})
	effective, _ := json.Marshal(map[string]any{"identity": map[string]any{"State": "unresolved"}})
	unresolved := episodeidentity.EpisodeIdentity{State: episodeidentity.StateUnresolved}
	releaseFacts := mediafactsfixture.ReleaseFactsJSON(t, nil, route, fixturePlanVideos(t, db, map[string]episodeidentity.EpisodeIdentity{"primary": unresolved}, nil)...)
	if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snap-unresolved', 'release-file-test', 'media-facts-v1', 'episode-identity-v2', 'regex', 'digest-u', '{"State":"unresolved"}'::jsonb, $1::jsonb)`, releaseFacts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ('snap-unresolved', 'primary', 'video', $1, $2, $3::jsonb, $4::jsonb)`,
		size, fingerprint, evidence, effective); err != nil {
		t.Fatal(err)
	}
	// The feed title carries no episode, so the renderer has no episode to
	// resolve from the release evidence either (identical to the real
	// unresolved content case).
	if _, err := db.ExecContext(ctx, `
UPDATE releases
SET raw_facts = '{"title":"[Test Group] Fixture Anime [1080p]"}'::jsonb,
    parsed_facts = '{"group_name":"Test Group"}'::jsonb
WHERE id = 'release-file-test'`); err != nil {
		t.Fatal(err)
	}
}

// setUnclassifiedRoute writes the live instance unknown-file switch. Prepare
// never reads it: the route is the one frozen in the accepted batch plan.
func setUnclassifiedRoute(t *testing.T, db *sql.DB, animeID, route string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO anime_business_configs
(managed_anime_id, unclassified_route, file_effect_source_default_version, file_effect_version, updated_at)
VALUES ($1, $2, 0, 1, CURRENT_TIMESTAMP)
ON CONFLICT (managed_anime_id) DO UPDATE SET unclassified_route = EXCLUDED.unclassified_route, file_effect_source_default_version = COALESCE(anime_business_configs.file_effect_source_default_version, 0), file_effect_version = COALESCE(anime_business_configs.file_effect_version, 0) + 1, updated_at = CURRENT_TIMESTAMP`,
		animeID, route); err != nil {
		t.Fatal(err)
	}
}

// TestUnclassifiedRouteSkipFiltersByDefault proves the frozen
// unclassified_route=skip behavior: an unrecognized video (no frozen episode
// identity) is filtered — no operation and no work item are planned — even
// when the live instance switch now says route.
func TestUnclassifiedRouteSkipFiltersByDefault(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	seedUnresolvedFactSnapshot(t, db, fixture.size, fixture.fingerprint, batchplan.RouteSkip)
	// The live switch changed after acceptance; the frozen plan route wins.
	setUnclassifiedRoute(t, db, fixture.animeID, batchplan.RouteRoute)
	service := newFixtureService(t, db, fixture)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	prepareFixture(t, db, service, fixture.artifactID)

	var operations, workItems int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type = 'file_operation'`).Scan(&workItems); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || workItems != 0 {
		t.Fatalf("default skip mode must filter the unresolved file: operations=%d work_items=%d", operations, workItems)
	}
}

// Unknown route uses only explicit user templates; no SP/Season00 identity.
func TestUnclassifiedRouteUsesExplicitUserTemplatesWithoutCoverage(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		sections := policy["effective_sections"].(map[string]any)
		rules := naming.DefaultNamingRules()
		rules.Unknown = naming.NamingVariant{DirectoryTemplate: "Unsorted/${sanitize(anime.title_base)}", VideoFilenameTemplate: "${sanitize(file.name)}"}
		sections[animedomain.SectionNamingRules] = rules
	})
	defer db.Close()
	seedUnresolvedFactSnapshot(t, db, fixture.size, fixture.fingerprint, batchplan.RouteRoute)
	// The live switch changed after acceptance; the frozen plan route wins.
	setUnclassifiedRoute(t, db, fixture.animeID, batchplan.RouteSkip)
	service := newFixtureService(t, db, fixture)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	prepareFixture(t, db, service, fixture.artifactID)

	var (
		state      string
		targetPath string
	)
	if err := db.QueryRow(`SELECT state, target_path FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).
		Scan(&state, &targetPath); err != nil {
		t.Fatalf("expected one Season 00 operation: %v", err)
	}
	if state != ownerStatePending {
		t.Fatalf("state=%s, want pending", state)
	}
	if targetPath != filepath.Join(fixture.root, "Unsorted", "Fixture Anime", "source.mkv") {
		t.Fatalf("target=%q, want explicit unknown directory/original filename", targetPath)
	}
	var identityState string
	var coverage sql.NullString
	if err := db.QueryRow(`SELECT effective_facts->'identity'->>'State',effective_facts->'identity'->>'CoverageKey' FROM media_fact_files WHERE snapshot_id='snap-unresolved' AND file_ref='primary'`).Scan(&identityState, &coverage); err != nil {
		t.Fatal(err)
	}
	if identityState != "unresolved" || coverage.Valid {
		t.Fatalf("unknown fabricated identity/coverage: %s %v", identityState, coverage)
	}
}

func contains(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}

// Under the default skip route an unknown video (its own filename carries no
// episode) and the subtitle that follows it are declared excluded with their
// own reasons: no operation, no failed result. The recognized video is still
// planned from its frozen identity.
func TestUnknownVideoUnderSkipIsExcludedNotFailed(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["fixture_manifest"] = []map[string]any{
			{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}},
			{"file_ref": "unknown", "ordinal": 1, "relative_path": "Special.mkv", "exact_size": 5, "media_kind": "video", "identity_evidence": map[string]any{}},
			{"file_ref": "unknown-sub", "ordinal": 2, "relative_path": "Special.chs.ass", "exact_size": 8, "media_kind": "subtitle", "identity_evidence": map[string]any{}},
		}
	})
	defer db.Close()
	dir := t.TempDir()
	unknownPath := filepath.Join(dir, "Special.mkv")
	subtitlePath := filepath.Join(dir, "Special.chs.ass")
	for path, content := range map[string]string{unknownPath: "video", subtitlePath: "subtitle"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	addArtifactFile(t, db, fixture, "artifact-file-unknown", "unknown", "Special.mkv", unknownPath, "video", 5)
	addArtifactFile(t, db, fixture, "artifact-file-unknown-sub", "unknown-sub", "Special.chs.ass", subtitlePath, "subtitle", 8)

	ctx := context.Background()
	resolved := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    "tv",
		FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "1"),
	})
	unresolved := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    "tv",
		FileEvidence: episodeidentity.NewMissingEvidence(episodeidentity.EpisodeSourceFileParser, "file_name"),
	})
	identityRaw, err := facts.MarshalIdentity(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snap-unknown-skip', 'release-file-test', 'media-facts-v1', $1, 'regex', 'digest-us', $2::jsonb, $3::jsonb)`,
		episodeidentity.ResolverVersion, identityRaw, mediafactsfixture.ReleaseFactsJSON(t, nil, batchplan.RouteSkip,
			fixturePlanVideos(t, db, map[string]episodeidentity.EpisodeIdentity{"primary": resolved, "unknown": unresolved}, nil)...)); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		ref      string
		kind     string
		size     int64
		identity episodeidentity.EpisodeIdentity
	}{{"primary", "video", fixture.size, resolved}, {"unknown", "video", 5, unresolved}} {
		evidence, _ := json.Marshal(map[string]any{"name": row.ref, "media_kind": row.kind, "size_bytes": row.size})
		if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ('snap-unknown-skip', $1, $2, $3, NULL, $4::jsonb, $5::jsonb)`,
			row.ref, row.kind, row.size, evidence, mustMarshalEffectiveFacts(t, row.identity)); err != nil {
			t.Fatal(err)
		}
	}

	service := newFixtureService(t, db, fixture)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	prepareFixture(t, db, service, fixture.artifactID)

	operations := loadOperations(t, db, fixture.artifactID)
	if len(operations) != 1 || operations[0].artifactFileID != fixture.fileID {
		t.Fatalf("only the recognized video may be planned, got %+v", operations)
	}
	assertRequirement(t, db, fixture.artifactID, "primary", "required", "file_rules_selected")
	assertRequirement(t, db, fixture.artifactID, "unknown", "excluded", "unknown_skipped")
	assertRequirement(t, db, fixture.artifactID, "unknown-sub", "excluded", "subtitle_video_excluded")
	if failed := failedDeliveryResults(t, db, fixture.artifactID); failed != 0 {
		t.Fatalf("skipped unknown files must not fail the acquisition: %d failed results", failed)
	}
}
