package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/metadata/matcher"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// The effective media kind (NULL is tv) decides the episode key shape, so it
// changes between tv and movie only while nothing holds the identity. Without
// a file the change is allowed; with an active file the PATCH is refused with
// 409 IDENTITY_KIND_CHANGE_BLOCKED, the counts in details and nothing written,
// while a patch that keeps the effective kind still saves.
func TestPatchAnimeMetadataBlocksAMediaKindChangeWhileFilesExist(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: ownershipTestDraft(t), Start: new(true)}, "identity-kind-0001")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID := subscribed.ManagedAnime.Id
	movie, tv := "movie", "tv"
	patch := func(version int64, kind *string, episodes *int) (AnimeMetadata, error) {
		return service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
			ExpectedVersion: version,
			BGM:             &BGMBinding{WorkID: "bgm-123"},
			TMDB:            &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
			MediaKind:       kind,
			TotalEpisodes:   episodes,
		})
	}
	readKind := func() (sql.NullString, int64) {
		t.Helper()
		var kind sql.NullString
		var version int64
		if err := db.QueryRowContext(ctx, `SELECT media_kind, version FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&kind, &version); err != nil {
			t.Fatal(err)
		}
		return kind, version
	}

	// No file and no running acquisition: the kind may change both ways.
	if _, err := patch(1, &movie, nil); err != nil {
		t.Fatalf("a kind change without files must be allowed: %v", err)
	}
	if _, err := patch(2, &tv, nil); err != nil {
		t.Fatalf("a kind change back without files must be allowed: %v", err)
	}

	animetest.SeedPlacementCoverage(t, db, animeID, "plc-identity-kind", "verified", "tv:e1")
	_, err = patch(3, &movie, nil)
	animetest.AssertBusinessCode(t, err, 409, animeapplication.IdentityKindChangeBlockedCode)
	var business *apperrors.Business
	if !errors.As(err, &business) {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, detail := range business.Details {
		if detail.Count == nil {
			t.Fatalf("detail %s carries no count", detail.Code)
		}
		counts[detail.Code] = *detail.Count
	}
	if len(counts) != 2 || counts["active_placements"] != 1 || counts["pending_acquisitions"] != 0 {
		t.Fatalf("blocked details = %+v, want one active placement and no acquisition", counts)
	}
	if kind, version := readKind(); kind.String != "tv" || version != 3 {
		t.Fatalf("a refused patch wrote: kind=%v version=%d", kind, version)
	}

	// Clearing the kind keeps the effective tv (the tuple backfills tv), and a
	// patch that leaves the kind alone saves its other fields.
	twelve := 12
	saved, err := patch(3, nil, &twelve)
	if err != nil {
		t.Fatalf("a patch that keeps the effective kind must save: %v", err)
	}
	if saved.TotalEpisodes == nil || *saved.TotalEpisodes != 12 || saved.MediaKind == nil || *saved.MediaKind != "tv" {
		t.Fatalf("saved patch = %+v", saved)
	}
}

// A metadata match that binds a movie TMDB tuple on an instance with files
// keeps the unset kind (tv) and still writes the bindings it found; the
// response shows what was written.
func TestMatchAnimeMetadataKeepsABlockedMediaKindAndFillsTheRest(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	bgmID, tmdbID, kind, season := "bgm-kind-1", "tmdb-kind-1", "movie", 0
	service.SetMetadataMatcher(&fakeMetadataMatcher{result: &matcher.MatchResult{
		BgmID: &bgmID, TmdbWorkID: &tmdbID, TmdbKind: &kind, TmdbSeason: &season,
	}})

	draft := ownershipTestDraft(t)
	draft.Metadata.Bgm = nil
	draft.Metadata.Tmdb = nil
	draft.Metadata.MediaKind = nil
	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "identity-kind-match-0001")
	if err != nil {
		t.Fatal(err)
	}
	animeID := subscribed.ManagedAnime.Id
	animetest.SeedPlacementCoverage(t, db, animeID, "plc-identity-kind-match", "verified", "tv:e1")

	result, err := service.MatchAnimeMetadata(ctx, animeID, MetadataMatchInput{ExpectedVersion: 1}, "identity-kind-match-key")
	if err != nil {
		t.Fatalf("metadata match: %v", err)
	}
	if result.Metadata.TMDB == nil || result.Metadata.TMDB.MediaKind != "movie" || result.Metadata.BGM == nil {
		t.Fatalf("the bindings must still be written: %+v", result.Metadata)
	}
	if result.Metadata.MediaKind != nil {
		t.Fatalf("the response must keep the unset kind, got %v", *result.Metadata.MediaKind)
	}
	var override sql.NullString
	var tupleKind string
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT media_kind, tmdb_media_kind, version FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&override, &tupleKind, &version); err != nil {
		t.Fatal(err)
	}
	if override.Valid || tupleKind != "movie" || version != 2 {
		t.Fatalf("persisted kind=%v tuple=%q version=%d, want unset kind beside the movie tuple", override, tupleKind, version)
	}
}
