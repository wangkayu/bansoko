package application

import (
	"bansoko/internal/acquisition/torrent"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	target "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/workitems"
)

// seedMovieMetadataBinding switches the seeded instance metadata to a movie
// binding, so a release evaluation must resolve the canonical movie identity.
// The owner year stays the one the seed set.
func seedMovieMetadataBinding(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
UPDATE anime_metadata
SET tmdb_media_kind = 'movie', tmdb_season = 0, version = version + 1
WHERE managed_anime_id = 'anime-rss-test'`); err != nil {
		t.Fatalf("switch metadata to a movie binding: %v", err)
	}
	animetest.SetOwnerYear(t, "anime-rss-test", observedWorkYear)
}

// seedReleaseWithVerifiedTorrent seeds one evaluable release whose stored feed
// facts carry complete verified torrent metadata (identity, filename, size and
// the exact per-entry file facts), so the evaluation runs the shared target
// boundary instead of the payload-missing path.
func seedReleaseWithVerifiedTorrent(t *testing.T, db *sql.DB, sourceID, releaseID, title, downloadURL string, file storedReleaseFile, torrentSize int64) {
	t.Helper()
	ctx := context.Background()
	if file.SizeBytes == nil || *file.SizeBytes != torrentSize {
		t.Fatal("fixture file size mismatch")
	}
	meta, err := torrent.Parse(nativeTorrentFile(file.SourcePath, *file.SizeBytes))
	if err != nil {
		t.Fatal(err)
	}
	internal, err := json.Marshal(storedFeedFacts{
		GUID: releaseID + "-guid", Title: title, DownloadURL: downloadURL,
		TorrentInfoHash: meta.InfoHashV1, TorrentInfoHashV1: meta.InfoHashV1,
		TorrentVersion: "v1", TorrentFilename: releaseID + ".torrent", TorrentSizeBytes: torrentSize,
		ReleaseFiles: []storedReleaseFile{file},
	})
	if err != nil {
		t.Fatal(err)
	}
	rawFacts, err := json.Marshal(map[string]any{"title": title, "download_url": downloadURL, "guid": releaseID + "-guid"})
	if err != nil {
		t.Fatal(err)
	}
	sourceFacts, err := json.Marshal(map[string]string{"identity_kind": "guid", "identity_value": releaseID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(
    id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts,
    verdict, reason_code, evaluation_version
)
VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb, 'discovered', 'discovered', NULL)`,
		releaseID, sourceID, "guid:"+releaseID, rawFacts, sourceFacts, internal); err != nil {
		t.Fatal(err)
	}
	seedNativeTorrentFile(t, db, "torrent-"+releaseID, releaseID, file.SourcePath, *file.SizeBytes, releaseID+".torrent")
}

// The movie chain regression: a movie metadata binding must drive the Release
// verdict, the frozen media snapshot, the candidate projection, and the frozen
// download policy with the canonical movie:0 identity — even when the title
// carries digits that look like TV episodes — and nothing downstream may
// re-derive a TV episode.
func TestMovieBindingReleaseChainPostgres(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	seedMovieMetadataBinding(t, db)
	insertReleaseForEvaluation(t, db, sourceID, "release-movie", "[Group] Some Movie 2024 [1080p]", "https://downloads.example.test/movie.torrent")
	seedNativeTorrentFile(t, db, "torrent-movie", "release-movie", "show-01.mkv", 1024, "movie.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	service.SetCandidateFactsRepository(facts.NewPGRepository(db))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-movie"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-movie"))
	finishReleaseClaim(t, store, service, claim)

	// The movie title has no episode and its digits must not become one: the
	// release is accepted on the canonical movie identity.
	assertReleaseState(t, db, "release-movie", "accepted_pending_download", "accepted", true)

	var identityRaw, releaseFactsRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT release_identity, release_facts
FROM media_fact_snapshots WHERE release_id = 'release-movie'`).
		Scan(&identityRaw, &releaseFactsRaw); err != nil {
		t.Fatal(err)
	}
	identity, err := facts.UnmarshalIdentity(identityRaw)
	if err != nil {
		t.Fatalf("decode frozen identity: %v", err)
	}
	if identity.State != episodeidentity.StateResolved || identity.Kind != episodeidentity.KindMovie ||
		identity.Episode == nil || string(*identity.Episode) != "0" {
		t.Fatalf("frozen identity = %+v, want the canonical movie:0 identity", identity)
	}
	if identity.CoverageKey == nil || *identity.CoverageKey != "movie:0" {
		t.Fatalf("frozen coverage = %v, want movie:0", identity.CoverageKey)
	}
	var frozenFacts facts.ReleaseFacts
	if err := json.Unmarshal(releaseFactsRaw, &frozenFacts); err != nil {
		t.Fatalf("decode frozen release facts: %v", err)
	}
	if frozenFacts.MediaKind != "movie" {
		t.Fatalf("frozen release facts media_kind=%q, want movie", frozenFacts.MediaKind)
	}
	if frozenFacts.ParserProvenance == nil || frozenFacts.ParserProvenance.MediaKind != "movie" {
		t.Fatalf("frozen provenance lost the media kind: %+v", frozenFacts.ParserProvenance)
	}

	// Candidate facts are the projection of the same movie identity.
	var coverageRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT coverage_keys FROM release_candidate_facts
WHERE release_id = 'release-movie' ORDER BY release_version DESC LIMIT 1`).Scan(&coverageRaw); err != nil {
		t.Fatal(err)
	}
	var candidateCoverage []string
	if err := json.Unmarshal(coverageRaw, &candidateCoverage); err != nil {
		t.Fatal(err)
	}
	if len(candidateCoverage) != 1 || candidateCoverage[0] != "movie:0" {
		t.Fatalf("candidate coverage = %v, want the frozen movie key", candidateCoverage)
	}

	// The frozen download policy carries the media kind evidence for retries.
	frozenPolicy := loadFrozenTaskPolicy(t, db, "release-movie")
	if frozenPolicy.Parser == nil || frozenPolicy.Parser.MediaKind != "movie" {
		t.Fatalf("frozen download policy parser = %+v, want media_kind movie", frozenPolicy.Parser)
	}

	// Submitted work is frozen: mutating the metadata binding afterwards must
	// not move the already-frozen snapshot, candidate projection, or policy.
	if _, err := db.ExecContext(ctx, `
UPDATE anime_metadata
SET tmdb_media_kind = 'tv', tmdb_season = 4, version = version + 1
WHERE managed_anime_id = 'anime-rss-test'`); err != nil {
		t.Fatal(err)
	}
	if err := facts.EnsureCurrentProfile(ctx, db, "release-movie"); err != nil {
		t.Fatalf("refresh candidate facts after metadata mutation: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT coverage_keys FROM release_candidate_facts
WHERE release_id = 'release-movie' ORDER BY release_version DESC LIMIT 1`).Scan(&coverageRaw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(coverageRaw, &candidateCoverage); err != nil {
		t.Fatal(err)
	}
	if len(candidateCoverage) != 1 || candidateCoverage[0] != "movie:0" {
		t.Fatalf("candidate coverage drifted after metadata mutation: %v", candidateCoverage)
	}
	frozenPolicy = loadFrozenTaskPolicy(t, db, "release-movie")
	if frozenPolicy.Parser == nil || frozenPolicy.Parser.MediaKind != "movie" {
		t.Fatalf("frozen policy drifted after metadata mutation: %+v", frozenPolicy.Parser)
	}
}

// A TV binding keeps the existing typed shape contract: the coverage key uses
// the bound season and the release-level evidence is unchanged.
func TestTVBindingReleaseChainKeepsSeasonCoveragePostgres(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	// The seeded fixture binds tv season 1; rebind to season 2 to prove the
	// bound season shapes the release coverage key.
	if _, err := db.ExecContext(ctx, `
UPDATE anime_metadata SET tmdb_season = 2, version = version + 1 WHERE managed_anime_id = 'anime-rss-test'`); err != nil {
		t.Fatal(err)
	}
	insertReleaseForEvaluation(t, db, sourceID, "release-tv-season", "[Group] Test Show - 07 1080p", "https://downloads.example.test/show-07.torrent")
	seedNativeTorrentFile(t, db, "torrent-tv-season", "release-tv-season", "show-07.mkv", 1024, "show-07.torrent")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	service.SetCandidateFactsRepository(facts.NewPGRepository(db))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-tv-season"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-tv-season"))
	finishReleaseClaim(t, store, service, claim)

	assertReleaseState(t, db, "release-tv-season", "accepted_pending_download", "accepted", true)
	var identityRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT release_identity FROM media_fact_snapshots WHERE release_id = 'release-tv-season'`).Scan(&identityRaw); err != nil {
		t.Fatal(err)
	}
	identity, err := facts.UnmarshalIdentity(identityRaw)
	if err != nil {
		t.Fatalf("decode frozen identity: %v", err)
	}
	if identity.Kind != episodeidentity.KindTVEpisode || identity.CoverageKey == nil || *identity.CoverageKey != "tv:s2:e7" {
		t.Fatalf("frozen TV identity = %+v, want tv:s2:e7 for the bound season 2", identity)
	}
	var releaseFactsRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT release_facts FROM media_fact_snapshots WHERE release_id = 'release-tv-season'`).Scan(&releaseFactsRaw); err != nil {
		t.Fatal(err)
	}
	var frozenFacts facts.ReleaseFacts
	if err := json.Unmarshal(releaseFactsRaw, &frozenFacts); err != nil {
		t.Fatal(err)
	}
	if frozenFacts.MediaKind != "tv" || frozenFacts.Season != 2 {
		t.Fatalf("frozen TV binding = %q/%d, want tv/2", frozenFacts.MediaKind, frozenFacts.Season)
	}
}

// The real PostgreSQL release -> target/naming chain for a movie binding: one
// evaluation resolves the canonical movie identity, renders the pre-download
// target through the movie naming branch (no season directory, no episode
// token) from the frozen identity, and persists that frozen target set with the
// movie parser provenance — with a title that carries a year and a torrent
// entry whose name carries digits.
func TestMovieBindingReleaseChainRendersAndPersistsMovieTargetPostgres(t *testing.T) {
	initTestNamingVars(t)
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	seedMovieMetadataBinding(t, db)
	seedReleaseWithVerifiedTorrent(t, db, sourceID, "release-movie-target", "[Group] Some Movie 2024 [1080p]",
		"https://downloads.example.test/movie-target.torrent",
		storedReleaseFile{
			SourcePath: "Some Movie 2024 [1080p].mkv", Filename: "Some Movie 2024 [1080p].mkv",
			SizeBytes: sizePointer(4096), MediaKind: "video",
		}, 4096)

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	service.SetCandidateFactsRepository(facts.NewPGRepository(db))
	service.SetTargetAcquisition(missingTargetProbe{})
	service.SetTargetStore(target.NewStore(targetpersistence.NewRepository(db)))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-movie-target"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-movie-target"))
	finishReleaseClaim(t, store, service, claim)
	assertReleaseState(t, db, "release-movie-target", "accepted_pending_download", "accepted", true)

	var targetPath string
	var configSnapshotRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT target_path, config_snapshot FROM acquisition_targets
WHERE release_id = 'release-movie-target' AND media_kind = 'video'`).Scan(&targetPath, &configSnapshotRaw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(targetPath, "Season 01") || strings.Contains(targetPath, " - E") {
		t.Fatalf("movie target %q used the TV naming branch", targetPath)
	}
	// The movie branch places the file directly in the title directory with the
	// file's own name and no episode token: the year in the release title and
	// the digits in the torrent entry never become an episode. The directory is
	// the instance title plus the year the bound work's provider observed.
	if want := fmt.Sprintf("Test Show (%d)", observedWorkYear); filepath.Base(filepath.Dir(targetPath)) != want {
		t.Fatalf("movie target %q is not the movie rendering of the instance title and year %q", targetPath, want)
	}
	if want := "Some Movie 2024 [1080p].mkv"; filepath.Base(targetPath) != want {
		t.Fatalf("movie target basename = %q, want the file's own name %q", filepath.Base(targetPath), want)
	}
	if strings.Contains(filepath.Base(targetPath), "E20") {
		t.Fatalf("movie target %q turned title digits into an episode", targetPath)
	}

	var configSnapshot struct {
		Parser *parser.Provenance `json:"parser"`
	}
	if err := json.Unmarshal(configSnapshotRaw, &configSnapshot); err != nil {
		t.Fatal(err)
	}
	if configSnapshot.Parser == nil {
		t.Fatalf("frozen target config snapshot carries no parser provenance: %s", configSnapshotRaw)
	}
	if configSnapshot.Parser.MediaKind != "movie" {
		t.Fatalf("frozen target parser media_kind = %q, want movie", configSnapshot.Parser.MediaKind)
	}
	// The one selected parse ran and produced typed evidence; the movie binding
	// short-circuits before it, so the parse status is never the scalar `value`
	// that would have made 2024 an episode.
	if configSnapshot.Parser.ParseStatus == string(parser.StatusValue) {
		t.Fatalf("frozen target parse status = %q: a movie binding must not admit a scalar episode", configSnapshot.Parser.ParseStatus)
	}
	if configSnapshot.Parser.ParseShape == string(parser.ShapeSingle) {
		t.Fatalf("frozen target parse shape = %q: a movie binding must not admit a single-episode shape", configSnapshot.Parser.ParseShape)
	}

	// The read model is explainable from the same frozen evidence: the
	// release-processing business log carries the frozen provenance (including
	// the movie media kind) and the resolved typed identity, with no raw
	// internal row id.
	var releaseLogDetails []byte
	if err := db.QueryRowContext(ctx, `
SELECT details FROM subscription_log_entries
WHERE release_id = 'release-movie-target' AND kind = 'release_processed'`).Scan(&releaseLogDetails); err != nil {
		t.Fatal(err)
	}
	var logDetails struct {
		Parser *parser.Provenance `json:"parser"`
		State  string             `json:"episode_state"`
		Shape  string             `json:"episode_shape"`
	}
	if err := json.Unmarshal(releaseLogDetails, &logDetails); err != nil {
		t.Fatalf("decode release log details: %v (raw=%s)", err, releaseLogDetails)
	}
	if logDetails.Parser == nil || logDetails.Parser.MediaKind != "movie" {
		t.Fatalf("release log provenance = %+v, want the frozen movie media kind", logDetails.Parser)
	}
	if logDetails.State != "resolved" {
		t.Fatalf("release log read model = %+v, want the resolved identity", logDetails)
	}
	var frozenSnapshotID string
	if err := db.QueryRowContext(ctx, `
SELECT id FROM media_fact_snapshots WHERE release_id = 'release-movie-target'`).Scan(&frozenSnapshotID); err != nil {
		t.Fatal(err)
	}
	for _, internalValue := range []string{frozenSnapshotID, targetPath} {
		if internalValue != "" && strings.Contains(string(releaseLogDetails), internalValue) {
			t.Fatalf("release log details leak an internal value %q: %s", internalValue, releaseLogDetails)
		}
	}

	// Submitted work is frozen: rebinding the instance metadata to TV after
	// submission must not move the persisted target or its provenance.
	if _, err := db.ExecContext(ctx, `
UPDATE anime_metadata SET tmdb_media_kind = 'tv', tmdb_season = 4, version = version + 1
WHERE managed_anime_id = 'anime-rss-test'`); err != nil {
		t.Fatal(err)
	}
	var rereadPath string
	if err := db.QueryRowContext(ctx, `
SELECT target_path FROM acquisition_targets
WHERE release_id = 'release-movie-target' AND media_kind = 'video'`).Scan(&rereadPath); err != nil {
		t.Fatal(err)
	}
	if rereadPath != targetPath {
		t.Fatalf("persisted movie target drifted after the metadata rebinding: %q -> %q", targetPath, rereadPath)
	}
}

// The real PostgreSQL release -> target/naming chain for a TV binding keeps the
// existing TV target shape: the season directory and episode token are rendered
// from the frozen typed identity.
func TestTVBindingReleaseChainRendersAndPersistsTVTargetPostgres(t *testing.T) {
	initTestNamingVars(t)
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	seedReleaseWithVerifiedTorrent(t, db, sourceID, "release-tv-target", "[Group] Test Show - 07 1080p",
		"https://downloads.example.test/show-07-target.torrent",
		storedReleaseFile{
			SourcePath: "Test Show - 07.mkv", Filename: "Test Show - 07.mkv",
			SizeBytes: sizePointer(2048), MediaKind: "video",
		}, 2048)

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	service.SetCandidateFactsRepository(facts.NewPGRepository(db))
	service.SetTargetAcquisition(missingTargetProbe{})
	service.SetTargetStore(target.NewStore(targetpersistence.NewRepository(db)))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-tv-target"}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, "release-tv-target"))
	finishReleaseClaim(t, store, service, claim)
	assertReleaseState(t, db, "release-tv-target", "accepted_pending_download", "accepted", true)

	var targetPath string
	var configSnapshotRaw []byte
	if err := db.QueryRowContext(ctx, `
SELECT target_path, config_snapshot FROM acquisition_targets
WHERE release_id = 'release-tv-target' AND media_kind = 'video'`).Scan(&targetPath, &configSnapshotRaw); err != nil {
		t.Fatal(err)
	}
	// The TV branch keeps the historical shape: a "Season 01" directory and an
	// episode token rendered from the frozen typed identity (never a re-parse).
	if !strings.Contains(filepath.Base(filepath.Dir(targetPath)), "Season") {
		t.Fatalf("TV target %q lost the season directory", targetPath)
	}
	if !strings.Contains(targetPath, "E07") {
		t.Fatalf("TV target %q did not render the typed episode from the frozen identity", targetPath)
	}
	var configSnapshot struct {
		Parser *parser.Provenance `json:"parser"`
	}
	if err := json.Unmarshal(configSnapshotRaw, &configSnapshot); err != nil {
		t.Fatal(err)
	}
	if configSnapshot.Parser == nil || configSnapshot.Parser.MediaKind != "tv" {
		t.Fatalf("frozen target parser provenance = %+v, want media_kind tv", configSnapshot.Parser)
	}
}
