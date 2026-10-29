package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/runtime/worker"
)

// seedMovieFactSnapshot freezes one Release media fact snapshot whose binding
// and parser evidence are fully movie-shaped. The frozen release evidence
// deliberately claims a TV episode (7) so the test proves the movie binding
// short-circuits before any release/file evidence is consulted.
func seedMovieFactSnapshot(t *testing.T, db *sql.DB, snapshotID, releaseID, mediaKind string, season int, releaseEpisode string, fingerprint string, config string) {
	t.Helper()
	releaseEvidence := episodeidentity.NewValueEvidence(
		episodeidentity.EpisodeSourceReleaseParser, "release_title", releaseEpisode)
	provenance := parser.Provenance{
		Kind:      string(parser.ParserRegex),
		MediaKind: mediaKind,
		Season:    season,
		Offset:    0,
	}
	releaseFacts := facts.ReleaseFacts{
		Title:            "[Group] Frozen Release",
		MediaKind:        mediaKind,
		Season:           season,
		EpisodeEvidence:  &releaseEvidence,
		ParserProvenance: &provenance,
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    mediaKind,
		FileEvidence: releaseEvidence,
	})
	factsRaw, err := json.Marshal(releaseFacts)
	if err != nil {
		t.Fatal(err)
	}
	identityRaw, err := facts.MarshalIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	var configSQL interface{}
	if config != "" {
		configSQL = config
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO media_fact_snapshots(
    id, release_id, schema_version, resolver_version, parser_kind,
    parser_config_fingerprint, parser_config, input_digest, release_identity, release_facts
)
VALUES ($1, $2, 'media-facts-v1', $3, 'regex', $4, $5, 'sha256:input', $6::jsonb, $7::jsonb)`,
		snapshotID, releaseID, episodeidentity.ResolverVersion, nullIfEmpty(fingerprint), configSQL, identityRaw, factsRaw); err != nil {
		t.Fatalf("seed frozen snapshot: %v", err)
	}
}

func nullIfEmpty(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// seedCurrentMetadataBinding inserts the current metadata row a drift attempt
// would edit after submission; the owner kind is the one the binding backfilled.
func seedCurrentMetadataBinding(t *testing.T, db *sql.DB, animeID, mediaKind string, season int) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, media_kind)
VALUES ($1, 'bgm-1', 'tmdb-1', $2, $3, $2)`, animeID, mediaKind, season); err != nil {
		t.Fatalf("seed current metadata binding: %v", err)
	}
}

// Per-file recognition must consume the frozen snapshot binding: a movie
// snapshot resolves every file to the canonical movie:0 identity even when the
// frozen release evidence claims a TV episode, the filename carries digits,
// and the current metadata row was mutated to a TV binding after submission.
func TestMovieBindingFileFactsUseFrozenBindingPostgres(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "Some Movie 2024 [1080p].mkv")
	if err := os.MkdirAll(filepath.Dir(videoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoPath, []byte("movie-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := filepath.EvalSymlinks(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	// The frozen file path is the torrent manifest path (a single-file torrent:
	// the file itself, first level), not the absolute path on disk.
	correlation := `{"external_id":"tag:movie-artifact","artifact_path":"` + canonicalPath + `","file_count":1,"files":[{"path":"Some Movie 2024 [1080p].mkv","size_bytes":11,"media_kind":"video","progress":1}]}`
	seedArtifactOwner(t, db, "artifact-movie", "task-movie", "completed", "pending", correlation, `{}`)
	seedMovieFactSnapshot(t, db, "snapshot-movie", "release-artifact-test", "movie", 0, "7", parser.NilConfigFingerprint(), "")
	seedCurrentMetadataBinding(t, db, "anime-artifact-test", "movie", 0)

	claim, store := claimArtifactWorkItem(t, db, "artifact-movie", []byte(`{"download_task_id":"task-movie","provider_task_id":"tag:movie-artifact","local_root":"`+root+`"}`))
	at := time.Date(2026, 9, 12, 8, 30, 0, 0, time.UTC)
	inspector := &fakeInspector{facts: domain.ArtifactFacts{Path: canonicalPath, SizeBytes: 11, ObservedAt: at, MediaKind: "video"}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))

	// Post-submission metadata drift: the current binding becomes TV season 9
	// before the artifact is inspected. The frozen snapshot must win.
	if _, err := db.ExecContext(context.Background(), `
UPDATE anime_metadata SET media_kind = 'tv', tmdb_media_kind = 'tv', tmdb_season = 9, version = version + 1
WHERE managed_anime_id = 'anime-artifact-test'`); err != nil {
		t.Fatal(err)
	}

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "verified" {
		t.Fatalf("movie inspection outcome=%+v", outcome)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)

	var effectiveRaw []byte
	if err := db.QueryRowContext(context.Background(), `
SELECT effective_facts FROM media_fact_files WHERE snapshot_id = 'snapshot-movie'`).Scan(&effectiveRaw); err != nil {
		t.Fatal(err)
	}
	var effective facts.EffectiveFileFacts
	if err := json.Unmarshal(effectiveRaw, &effective); err != nil {
		t.Fatal(err)
	}
	identity := effective.Identity
	if identity.State != episodeidentity.StateResolved || identity.Kind != episodeidentity.KindMovie ||
		identity.Episode == nil || string(*identity.Episode) != "0" {
		t.Fatalf("per-file identity = %+v, want the canonical movie:0 identity despite the drifted metadata", identity)
	}
	if identity.CoverageKey == nil || *identity.CoverageKey != "movie:0" {
		t.Fatalf("per-file coverage = %v, want movie:0", identity.CoverageKey)
	}
}

// A TV snapshot resolves per-file coverage from the frozen recognition alone:
// the coverage key carries no season, so mutating the binding season after
// submission must not move it.
func TestTVBindingFileFactsIgnoreMetadataSeasonPostgres(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "Show", "Show - 07 [1080p].mkv")
	if err := os.MkdirAll(filepath.Dir(videoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoPath, []byte("tv-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := filepath.EvalSymlinks(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	// The frozen file path is the torrent manifest path (torrent.name/<file>,
	// first level), not the absolute path on disk.
	correlation := `{"external_id":"tag:tv-artifact","artifact_path":"` + canonicalPath + `","file_count":1,"files":[{"path":"Show/Show - 07 [1080p].mkv","size_bytes":8,"media_kind":"video","progress":1}]}`
	seedArtifactOwner(t, db, "artifact-tv", "task-tv", "completed", "pending", correlation, `{}`)
	seedMovieFactSnapshot(t, db, "snapshot-tv", "release-artifact-test", "tv", 5, "7", parser.NilConfigFingerprint(), "")
	seedCurrentMetadataBinding(t, db, "anime-artifact-test", "tv", 5)

	claim, store := claimArtifactWorkItem(t, db, "artifact-tv", []byte(`{"download_task_id":"task-tv","provider_task_id":"tag:tv-artifact","local_root":"`+root+`"}`))
	at := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	inspector := &fakeInspector{facts: domain.ArtifactFacts{Path: canonicalPath, SizeBytes: 8, ObservedAt: at, MediaKind: "video"}}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))

	if _, err := db.ExecContext(context.Background(), `
UPDATE anime_metadata SET tmdb_season = 9, version = version + 1
WHERE managed_anime_id = 'anime-artifact-test'`); err != nil {
		t.Fatal(err)
	}

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "verified" {
		t.Fatalf("tv inspection outcome=%+v", outcome)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)

	var effectiveRaw []byte
	if err := db.QueryRowContext(context.Background(), `
SELECT effective_facts FROM media_fact_files WHERE snapshot_id = 'snapshot-tv'`).Scan(&effectiveRaw); err != nil {
		t.Fatal(err)
	}
	var effective facts.EffectiveFileFacts
	if err := json.Unmarshal(effectiveRaw, &effective); err != nil {
		t.Fatal(err)
	}
	identity := effective.Identity
	if identity.Kind != episodeidentity.KindTVEpisode || identity.CoverageKey == nil || *identity.CoverageKey != "tv:e7" {
		t.Fatalf("per-file identity = %+v, want tv:e7 (current metadata season 9 is not read)", identity)
	}
}
