package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
)

// seedFrozenMovieNamingSnapshot freezes the release media fact snapshot the
// FileOperation naming consumes: a movie binding plus one resolved movie file
// identity, exactly what release evaluation would have frozen at admission.
func seedFrozenMovieNamingSnapshot(t *testing.T, db *sql.DB, mediaKind string, season int) {
	t.Helper()
	provenance := parser.Provenance{
		Kind:      string(parser.ParserRegex),
		MediaKind: mediaKind,
		Season:    season,
		Offset:    0,
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    mediaKind,
		Season:       season,
		FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "1"),
	})
	releaseFacts := facts.ReleaseFacts{
		Title:            "[Test Group] Fixture Anime - 01",
		MediaKind:        mediaKind,
		Season:           season,
		ParserProvenance: &provenance,
		BatchPlan:        fixtureBatchPlan(t, db, map[string]episodeidentity.EpisodeIdentity{"primary": identity}),
	}
	factsRaw, err := json.Marshal(releaseFacts)
	if err != nil {
		t.Fatal(err)
	}
	identityRaw, err := facts.MarshalIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO media_fact_snapshots(
    id, release_id, schema_version, resolver_version, parser_kind,
    parser_config_fingerprint, input_digest, release_identity, release_facts
)
VALUES ('snapshot-file-movie', 'release-file-test', 'media-facts-v1', $1, 'regex',
        $2, 'sha256:input', $3::jsonb, $4::jsonb)`,
		episodeidentity.ResolverVersion, parser.NilConfigFingerprint(), identityRaw, factsRaw); err != nil {
		t.Fatalf("seed frozen naming snapshot: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO media_fact_files(
    snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts
)
VALUES ('snapshot-file-movie', 'primary', 'video', 14, NULL, '{}'::jsonb, $1::jsonb)`,
		mustMarshalEffectiveFacts(t, identity)); err != nil {
		t.Fatalf("seed frozen naming file row: %v", err)
	}
}

func mustMarshalEffectiveFacts(t *testing.T, identity episodeidentity.EpisodeIdentity) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(facts.EffectiveFileFacts{Identity: identity, MappingVersion: facts.MappingVersion})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The naming render must consume the media kind frozen on the snapshot: after
// submission, editing the current metadata binding (movie -> tv, season drift)
// must not move the render back to the TV naming branch.
func TestPrepareForArtifactIgnoresPostSubmissionMetadataDrift(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		sections := policy["effective_sections"].(map[string]any)
		sections["naming_rules"] = map[string]any{
			"language": "js-naming-1",
			"tv": map[string]any{
				"directory_template":      "TVBranch",
				"video_filename_template": "tv-branch.mkv",
			},
			"movie": map[string]any{
				"directory_template":      "MovieBranch",
				"video_filename_template": "movie-branch.mkv",
			},
			"subtitle_suffix_template": "",
		}
	})
	defer db.Close()
	seedFrozenMovieNamingSnapshot(t, db, "movie", 0)
	// The current metadata row drifts to a TV binding with a different season
	// before prepare runs; the frozen snapshot must win.
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season)
VALUES ('anime-file-test', 'bgm-1', 'tmdb-1', 'tv', 7)
ON CONFLICT (managed_anime_id) DO UPDATE
SET tmdb_work_id = EXCLUDED.tmdb_work_id,
    tmdb_media_kind = EXCLUDED.tmdb_media_kind, tmdb_season = EXCLUDED.tmdb_season`); err != nil {
		t.Fatal(err)
	}

	service := newFixtureService(t, db, fixture)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	prepareFixture(t, db, service, fixture.artifactID)

	var targetPath string
	if err := db.QueryRowContext(context.Background(), `
SELECT target_path FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&targetPath); err != nil {
		t.Fatal(err)
	}
	if targetPath == "" {
		t.Fatal("no planned target path")
	}
	if !strings.Contains(targetPath, "MovieBranch") || !strings.Contains(targetPath, "movie-branch.mkv") {
		t.Fatalf("target %q did not use the frozen movie naming branch", targetPath)
	}
	if strings.Contains(targetPath, "TVBranch") {
		t.Fatalf("target %q drifted to the TV naming branch after the metadata edit", targetPath)
	}
}
