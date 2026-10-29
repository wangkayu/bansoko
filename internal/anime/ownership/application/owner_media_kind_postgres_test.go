package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// The owner media kind is an owner field next to the TMDB tuple. Subscribing a
// draft that binds a TMDB tuple without a kind backfills the kind from the
// tuple. The metadata PATCH is complete-owner replacement: a submitted kind is
// persisted verbatim and never rewrites the tuple; an unset kind is backfilled
// from the TMDB tuple the patch binds, and stays unset (tv) without one. A
// value outside the persisted vocabulary is refused before the transaction, so
// it cannot bump the owner version.
func TestPatchAnimeMetadataMediaKindRoundTrip(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: ownershipTestDraft(t), Start: new(true)}, "media-kind-override-0001")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID := subscribed.ManagedAnime.Id
	if subscribed.Metadata.MediaKind == nil || string(*subscribed.Metadata.MediaKind) != "tv" {
		t.Fatalf("subscribing a TV tuple without a kind must backfill tv, got %v", subscribed.Metadata.MediaKind)
	}

	readRow := func() (sql.NullString, string, int64) {
		t.Helper()
		var override, tupleKind sql.NullString
		var version int64
		if err := db.QueryRowContext(ctx, `
SELECT media_kind, tmdb_media_kind, version
FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&override, &tupleKind, &version); err != nil {
			t.Fatalf("read anime metadata: %v", err)
		}
		return override, tupleKind.String, version
	}
	if override, _, _ := readRow(); !override.Valid || override.String != "tv" {
		t.Fatalf("subscribed kind = %v, want the backfilled tv", override)
	}

	movie := "movie"
	pinned, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion: 1,
		BGM:             &BGMBinding{WorkID: "bgm-123"},
		TMDB:            &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
		MediaKind:       &movie,
	})
	if err != nil {
		t.Fatalf("set media kind: %v", err)
	}
	if pinned.MediaKind == nil || *pinned.MediaKind != "movie" || pinned.TMDB == nil || pinned.TMDB.MediaKind != "tv" {
		t.Fatalf("patch result must echo the kind and the untouched tuple: %+v", pinned)
	}
	override, tupleKind, version := readRow()
	if !override.Valid || override.String != "movie" {
		t.Fatalf("persisted kind = %v, want movie", override)
	}
	if tupleKind != "tv" {
		t.Fatalf("tmdb_media_kind = %q, want the untouched tv binding", tupleKind)
	}
	if version != 2 {
		t.Fatalf("metadata version = %d, want 2", version)
	}

	// An unset kind next to a bound tuple is backfilled from the tuple kind.
	backfilled, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion: 2,
		BGM:             &BGMBinding{WorkID: "bgm-123"},
		TMDB:            &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
		MediaKind:       nil,
	})
	if err != nil {
		t.Fatalf("patch an unset kind with a bound tuple: %v", err)
	}
	if backfilled.MediaKind == nil || *backfilled.MediaKind != "tv" {
		t.Fatalf("an unset kind with a tv tuple must read back tv, got %v", backfilled.MediaKind)
	}
	override, tupleKind, version = readRow()
	if !override.Valid || override.String != "tv" || tupleKind != "tv" || version != 3 {
		t.Fatalf("backfill must persist tv, keep the tuple and bump the version once: kind=%v tuple=%q version=%d", override, tupleKind, version)
	}

	// Clearing the tuple keeps a submitted kind.
	kept, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion: 3,
		BGM:             &BGMBinding{WorkID: "bgm-123"},
		MediaKind:       &movie,
	})
	if err != nil {
		t.Fatalf("clear the tuple: %v", err)
	}
	if kept.MediaKind == nil || *kept.MediaKind != "movie" || kept.TMDB != nil {
		t.Fatalf("clearing the tuple must keep the submitted kind: %+v", kept)
	}
	if override, _, version = readRow(); !override.Valid || override.String != "movie" || version != 4 {
		t.Fatalf("persisted kind after clearing the tuple = %v (version %d), want movie", override, version)
	}

	// Without a tuple an unset kind stays unset, which is tv.
	if _, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion: 4,
		BGM:             &BGMBinding{WorkID: "bgm-123"},
		MediaKind:       nil,
	}); err != nil {
		t.Fatalf("unset the kind: %v", err)
	}
	if override, _, version = readRow(); override.Valid || version != 5 {
		t.Fatalf("an unset kind without a tuple must persist NULL: kind=%v version=%d", override, version)
	}

	// A value the column cannot store is refused before any write.
	invalid := "ova"
	if _, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion: 5,
		BGM:             &BGMBinding{WorkID: "bgm-123"},
		TMDB:            &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
		MediaKind:       &invalid,
	}); err == nil {
		t.Fatal("an out-of-vocabulary kind must be refused")
	}
	if override, _, version = readRow(); override.Valid || version != 5 {
		t.Fatalf("a refused kind must not write: kind=%v version=%d", override, version)
	}
}
