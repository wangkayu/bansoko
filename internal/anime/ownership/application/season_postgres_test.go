package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// The owner season is its own nullable column, independent of the TMDB tuple:
// subscribe provisions the draft's season, a PATCH saves it with or without a
// TMDB binding, null clears it, a negative value is refused, and neither the
// season nor the TMDB season ever rewrites the other.
func TestOwnerSeasonPersistsIndependentlyOfTheTMDBTuple(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	draft := ownershipTestDraft(t)
	draftSeason := 4
	draft.Metadata.Season = &draftSeason
	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "subscribe-key-owner-season")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID := subscribed.ManagedAnime.Id
	readRow := func() (sql.NullInt64, sql.NullInt64, int64) {
		t.Helper()
		var season, tmdbSeason sql.NullInt64
		var version int64
		if err := db.QueryRowContext(ctx, `SELECT season, tmdb_season, version FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&season, &tmdbSeason, &version); err != nil {
			t.Fatal(err)
		}
		return season, tmdbSeason, version
	}
	season, _, version := readRow()
	if !season.Valid || season.Int64 != 4 {
		t.Fatalf("subscribe provisioned season %+v, want the draft's 4", season)
	}

	// A season without any TMDB binding saves.
	three := 3
	patched, err := service.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{ExpectedVersion: version, Season: &three})
	if err != nil {
		t.Fatalf("patch season without TMDB: %v", err)
	}
	if patched.Season == nil || *patched.Season != 3 {
		t.Fatalf("response season = %v, want 3", patched.Season)
	}
	if season, tmdbSeason, _ := readRow(); !season.Valid || season.Int64 != 3 || tmdbSeason.Valid {
		t.Fatalf("stored season=%+v tmdb_season=%+v, want 3 and no TMDB tuple", season, tmdbSeason)
	}

	// A TMDB tuple with a different season leaves the owner season alone.
	tmdb := &generated.TMDBBinding{WorkId: "265010", MediaKind: generated.TMDBMediaKindTv, Season: 1}
	bound, err := service.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{ExpectedVersion: patched.Version, Tmdb: tmdb, Season: &three})
	if err != nil {
		t.Fatalf("patch TMDB tuple: %v", err)
	}
	if season, tmdbSeason, _ := readRow(); season.Int64 != 3 || !tmdbSeason.Valid || tmdbSeason.Int64 != 1 {
		t.Fatalf("stored season=%+v tmdb_season=%+v, want 3 and 1", season, tmdbSeason)
	}

	// Null clears the owner season; the TMDB tuple keeps its own season.
	cleared, err := service.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{ExpectedVersion: bound.Version, Tmdb: tmdb})
	if err != nil {
		t.Fatalf("clear season: %v", err)
	}
	if cleared.Season != nil {
		t.Fatalf("response season = %v, want null", *cleared.Season)
	}
	if season, tmdbSeason, _ := readRow(); season.Valid || tmdbSeason.Int64 != 1 {
		t.Fatalf("stored season=%+v tmdb_season=%+v, want NULL and 1", season, tmdbSeason)
	}

	// Season 0 (specials) is a value; a negative season is refused.
	zero, negative := 0, -1
	specials, err := service.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{ExpectedVersion: cleared.Version, Season: &zero})
	if err != nil || specials.Season == nil || *specials.Season != 0 {
		t.Fatalf("season 0: %+v err=%v", specials.Season, err)
	}
	_, err = service.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{ExpectedVersion: specials.Version, Season: &negative})
	var invalid *apperrors.Validation
	if !errors.As(err, &invalid) || invalid.Code != "DRAFT_INVALID" {
		t.Fatalf("negative season err = %v, want DRAFT_INVALID", err)
	}
	if season, _, _ := readRow(); !season.Valid || season.Int64 != 0 {
		t.Fatalf("a refused patch changed the season to %+v", season)
	}
}
