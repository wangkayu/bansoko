package filemaintenance

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/testutil/disposabledb"
)

func seedScanOwner(t *testing.T, db *sql.DB, animeID string, state string, generation int64) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO reconciliation_scans
(managed_anime_id, state, generation, scan_scope, progress, capacity_stats, input_fingerprint, version, updated_at)
VALUES ($1, $2, $3, '{}', '{}', '{}', 'fp', 1, CURRENT_TIMESTAMP)`,
		animeID, state, generation); err != nil {
		t.Fatal(err)
	}
}

func seedObservation(t *testing.T, db *sql.DB, animeID string, generation, batch int64, filePath, fingerprint string, size int64) {
	t.Helper()
	// No coverage: this models an observation whose producer did not persist
	// provable coverage. The reader must report it unresolved, never re-parse
	// the path.
	seedObservationFacts(t, db, animeID, generation, batch, filePath, fingerprint, size, `{}`)
}

// seedObservationCoverage seeds one observation whose persisted facts carry
// canonical coverage, the shape the historical scan producer wrote. The reader
// consumes the persisted value; it never re-derives it from the path.
func seedObservationCoverage(t *testing.T, db *sql.DB, animeID string, generation, batch int64, filePath, fingerprint string, size int64, coverageKey string) {
	t.Helper()
	seedObservationFacts(t, db, animeID, generation, batch, filePath, fingerprint, size,
		`{"coverage":["`+coverageKey+`"],"coverage_unresolved":false}`)
}

func seedObservationFacts(t *testing.T, db *sql.DB, animeID string, generation, batch int64, filePath, fingerprint string, size int64, facts string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO reconciliation_files
(managed_anime_id, generation, batch_index, file_path, fingerprint, size_bytes, facts, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, CURRENT_TIMESTAMP)`,
		animeID, generation, batch, filePath, fingerprint, size, facts); err != nil {
		t.Fatal(err)
	}
}

// seedPlacementOwnedObservation seeds one Placement plus its frozen coverage set
// and the observation that points at it, the shape the current scan producer
// writes.
func seedPlacementOwnedObservation(t *testing.T, db *sql.DB, animeID string, generation, batch int64, placementID, filePath, fingerprint, coverageKey string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, snapshot_generation, version)
VALUES ($1, $2, $3, 'reconciliation', $4, $5, $5, 'facts-v1', $6, 1)`,
		placementID, filePath, animeID, placementID, fingerprint, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, 0, 'facts-v1', $1)`, placementID, coverageKey); err != nil {
		t.Fatal(err)
	}
	seedObservationFacts(t, db, animeID, generation, batch, filePath, fingerprint, 100,
		`{"placement_id":"`+placementID+`","disposition":"present","located_via":"current_path","located_path":"`+filePath+`"}`)
}

func seedMovieMetadata(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO managed_anime(id, title) VALUES ($1, 'Movie Title')
ON CONFLICT (id) DO NOTHING`, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season, media_kind_override, version)
VALUES ($1, 'mv-work-1', 'movie', 0, 'movie', 1)
ON CONFLICT (managed_anime_id) DO UPDATE SET tmdb_media_kind = EXCLUDED.tmdb_media_kind, tmdb_season = EXCLUDED.tmdb_season,
    media_kind_override = EXCLUDED.media_kind_override`,
		animeID); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotReaderGenerationUsable(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	reader := NewReconciliationSnapshotReader(db)
	ctx := context.Background()

	seedScanOwner(t, db, "anime-snap-1", "completed", 2)
	usable, err := reader.GenerationUsable(ctx, "anime-snap-1", 2)
	if err != nil || !usable {
		t.Fatalf("completed generation must be usable: %v %v", usable, err)
	}
	usable, err = reader.GenerationUsable(ctx, "anime-snap-1", 1)
	if err != nil || usable {
		t.Fatalf("stale generation must not be usable: %v %v", usable, err)
	}
	usable, err = reader.GenerationUsable(ctx, "anime-snap-unknown", 1)
	if err != nil || usable {
		t.Fatalf("missing owner must not be usable: %v %v", usable, err)
	}

	seedScanOwner(t, db, "anime-snap-2", "running", 1)
	usable, err = reader.GenerationUsable(ctx, "anime-snap-2", 1)
	if err != nil || usable {
		t.Fatalf("running generation must not be usable: %v %v", usable, err)
	}
}

// Coverage is resolved from frozen evidence only: the Placement-owned frozen
// coverage set, or persisted observation facts. A legacy observation with
// neither is unresolved and visible, never re-derived from its filename.
func TestSnapshotReaderCoverageComputation(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	reader := NewReconciliationSnapshotReader(db)
	ctx := context.Background()

	seedScanOwner(t, db, "anime-snap-3", "completed", 1)
	seedPlacementOwnedObservation(t, db, "anime-snap-3", 1, 0, "plc-snap-1", "/library/Anime/EP01.mkv", "fp-1", "tv:e1")
	seedObservationFacts(t, db, "anime-snap-3", 1, 1, "/library/Anime/Season 2/EP03.mkv", "fp-2", 200,
		`{"coverage":["tv:e3"],"coverage_unresolved":false}`)
	seedObservation(t, db, "anime-snap-3", 1, 2, "/library/Anime/S02E05.mkv", "fp-3", 300)

	observations, err := reader.ListObservations(ctx, "anime-snap-3", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 3 {
		t.Fatalf("expected 3 observations, got %d", len(observations))
	}
	byPath := map[string][]string{}
	for _, obs := range observations {
		byPath[obs.FilePath] = obs.Coverage
	}
	if got := byPath["/library/Anime/EP01.mkv"]; len(got) != 1 || got[0] != "tv:e1" {
		t.Fatalf("placement-owned frozen coverage = %v, want tv:e1", got)
	}
	if got := byPath["/library/Anime/Season 2/EP03.mkv"]; len(got) != 1 || got[0] != "tv:e3" {
		t.Fatalf("persisted coverage = %v, want tv:e3", got)
	}
	// A parseable-looking path carries no coverage when nothing persisted it.
	if got := byPath["/library/Anime/S02E05.mkv"]; len(got) != 1 || got[0] != "unresolved" {
		t.Fatalf("legacy observation coverage = %v, want unresolved (path must never be re-parsed)", got)
	}
}

func TestSnapshotReaderMovieAndUnresolved(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	reader := NewReconciliationSnapshotReader(db)
	ctx := context.Background()

	seedScanOwner(t, db, "anime-snap-movie", "completed", 1)
	seedMovieMetadata(t, db, "anime-snap-movie")
	seedObservation(t, db, "anime-snap-movie", 1, 0, "/library/Movie/MovieTitle.mkv", "fp-m", 500)
	seedObservation(t, db, "anime-snap-movie", 1, 1, "/library/Movie/Movie Special.mkv", "fp-s", 500)

	observations, err := reader.ListObservations(ctx, "anime-snap-movie", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 {
		t.Fatalf("expected 2 movie observations, got %d", len(observations))
	}
	// Movie media kind yields the single movie:0 observation key.
	for _, obs := range observations {
		if len(obs.Coverage) != 1 || obs.Coverage[0] != "movie:0" {
			t.Fatalf("movie coverage = %v, want movie:0", obs.Coverage)
		}
	}

	seedScanOwner(t, db, "anime-snap-4", "completed", 1)
	seedObservation(t, db, "anime-snap-4", 1, 0, "/library/Anime/Unparseable.mkv", "fp-u", 50)
	observations, err = reader.ListObservations(ctx, "anime-snap-4", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 || len(observations[0].Coverage) != 1 || observations[0].Coverage[0] != "unresolved" {
		t.Fatalf("unparseable coverage = %v, want unresolved", observations[0].Coverage)
	}
}

func TestSnapshotReaderReadObservationByRef(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	reader := NewReconciliationSnapshotReader(db)
	ctx := context.Background()

	seedScanOwner(t, db, "anime-snap-5", "completed", 7)
	seedObservationFacts(t, db, "anime-snap-5", 7, 0, "/library/Anime/S01E01.mkv", "fp-ref", 123,
		`{"coverage":["tv:e1"],"coverage_unresolved":false}`)

	obs, found, err := reader.ReadObservation(ctx, "anime-snap-5", 7, "anime-snap-5:7:/library/Anime/S01E01.mkv")
	if err != nil || !found {
		t.Fatalf("read observation by ref: found=%v err=%v", found, err)
	}
	if obs.FilePath != "/library/Anime/S01E01.mkv" || obs.Fingerprint != "fp-ref" || obs.SizeBytes != 123 {
		t.Fatalf("observation mismatch: %+v", obs)
	}
	if len(obs.Coverage) != 1 || obs.Coverage[0] != "tv:e1" {
		t.Fatalf("ref observation coverage = %v, want tv:e1", obs.Coverage)
	}

	obs, found, err = reader.ReadObservation(ctx, "anime-snap-5", 7, "anime-snap-5:7:/library/Anime/Missing.mkv")
	if err != nil || found {
		t.Fatalf("missing observation must not be found: found=%v err=%v", found, err)
	}
	obs, found, err = reader.ReadObservation(ctx, "anime-snap-5", 8, "anime-snap-5:8:/library/Anime/S01E01.mkv")
	if err != nil || found {
		t.Fatalf("wrong generation must not be found: found=%v err=%v", found, err)
	}
}

func TestSnapshotReaderPrefersPersistedCoverage(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	reader := NewReconciliationSnapshotReader(db)
	ctx := context.Background()

	seedScanOwner(t, db, "anime-snap-persisted", "completed", 1)
	// A file whose basename would never parse (e.g. renamed/obfuscated) but
	// whose persisted scan facts froze canonical coverage: the reader must use
	// the persisted facts, never re-parse the path.
	facts := `{"coverage":["tv:e12"],"coverage_unresolved":false,"episode":"12","season":3}`
	if _, err := db.ExecContext(ctx, `
INSERT INTO reconciliation_files
(managed_anime_id, generation, batch_index, file_path, fingerprint, size_bytes, facts, observed_at)
VALUES ('anime-snap-persisted', 1, 0, '/library/Anime/WEIRD NAME 7.mkv', 'fp-p', 123, $1::jsonb, CURRENT_TIMESTAMP)`, facts); err != nil {
		t.Fatal(err)
	}

	observations, err := reader.ListObservations(ctx, "anime-snap-persisted", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(observations))
	}
	if got := observations[0].Coverage; len(got) != 1 || got[0] != "tv:e12" {
		t.Fatalf("persisted coverage = %v, want tv:e12 (path must not be re-parsed)", got)
	}

	// Movie classification is metadata truth and bypasses parsing entirely.
	seedObservation(t, db, "anime-snap-persisted", 1, 1, "/library/Anime/movie.mkv", "fp-m", 456)
	seedMovieMetadata(t, db, "anime-snap-persisted")
	observations, err = reader.ListObservations(ctx, "anime-snap-persisted", 1)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string][]string{}
	for _, obs := range observations {
		byPath[obs.FilePath] = obs.Coverage
	}
	if got := byPath["/library/Anime/movie.mkv"]; len(got) != 1 || got[0] != "movie:0" {
		t.Fatalf("movie coverage = %v, want movie:0", got)
	}
}

// A Placement-owned observation keeps the coverage its selected parser froze at
// admission: switching the instance metadata to a movie binding afterwards must
// not re-classify an already-placed file (and vice versa). Only a legacy
// full-library observation without frozen coverage consumes the metadata movie
// classification.
func TestSnapshotReaderPlacementCoverageSurvivesMetadataRebinding(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	reader := NewReconciliationSnapshotReader(db)
	ctx := context.Background()

	seedScanOwner(t, db, "anime-snap-rebind", "completed", 1)
	seedPlacementOwnedObservation(t, db, "anime-snap-rebind", 1, 0, "plc-rebind-1", "/library/Anime/EP01.mkv", "fp-1", "tv:e1")
	seedPlacementOwnedObservation(t, db, "anime-snap-rebind", 1, 1, "plc-rebind-2", "/library/Anime/Movie.mkv", "fp-2", "movie:0")
	seedObservation(t, db, "anime-snap-rebind", 1, 2, "/library/Anime/S02E05.mkv", "fp-3", 300)

	coverageByPath := func() map[string][]string {
		t.Helper()
		observations, err := reader.ListObservations(ctx, "anime-snap-rebind", 1)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string][]string{}
		for _, obs := range observations {
			result[obs.FilePath] = obs.Coverage
		}
		return result
	}

	before := coverageByPath()
	if got := before["/library/Anime/EP01.mkv"]; len(got) != 1 || got[0] != "tv:e1" {
		t.Fatalf("frozen placement coverage = %v, want tv:e1", got)
	}
	if got := before["/library/Anime/Movie.mkv"]; len(got) != 1 || got[0] != "movie:0" {
		t.Fatalf("frozen movie placement coverage = %v, want movie:0", got)
	}

	// The instance metadata binding flips after admission. The frozen placement
	// coverage must not move in either direction.
	seedMovieMetadata(t, db, "anime-snap-rebind")
	after := coverageByPath()
	if got := after["/library/Anime/EP01.mkv"]; len(got) != 1 || got[0] != "tv:e1" {
		t.Fatalf("placement coverage drifted after the movie rebinding: %v", got)
	}
	if got := after["/library/Anime/Movie.mkv"]; len(got) != 1 || got[0] != "movie:0" {
		t.Fatalf("movie placement coverage drifted: %v", got)
	}
	if got := after["/library/Anime/S02E05.mkv"]; len(got) != 1 || got[0] != "movie:0" {
		t.Fatalf("legacy observation coverage = %v, want the movie classification", got)
	}

	// The single-observation reader resolves the same frozen value.
	obs, found, err := reader.ReadObservation(ctx, "anime-snap-rebind", 1, "anime-snap-rebind:1:/library/Anime/EP01.mkv")
	if err != nil || !found {
		t.Fatalf("read placement observation: found=%v err=%v", found, err)
	}
	if len(obs.Coverage) != 1 || obs.Coverage[0] != "tv:e1" {
		t.Fatalf("single-read placement coverage = %v, want the frozen tv:e1", obs.Coverage)
	}
}
