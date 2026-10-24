package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// The owner metadata PATCH is complete-owner replacement: the media-kind pin is
// persisted verbatim, a nil pin clears it, and the pin never rewrites the TMDB
// tuple it sits next to. A value outside the persisted vocabulary is refused
// before the transaction, so it cannot bump the owner version.
func TestPatchAnimeMetadataMediaKindOverrideRoundTrip(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: ownershipTestDraft(t), Start: new(true)}, "media-kind-override-0001")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID := subscribed.ManagedAnime.Id
	if subscribed.Metadata.MediaKindOverride != nil {
		t.Fatalf("a fresh subscribe must not pin a media kind, got %q", string(*subscribed.Metadata.MediaKindOverride))
	}

	readRow := func() (sql.NullString, string, int64) {
		t.Helper()
		var override, tupleKind sql.NullString
		var version int64
		if err := db.QueryRowContext(ctx, `
SELECT media_kind_override, tmdb_media_kind, version
FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&override, &tupleKind, &version); err != nil {
			t.Fatalf("read anime metadata: %v", err)
		}
		return override, tupleKind.String, version
	}

	movie := "movie"
	pinned, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion:   1,
		BGM:               &BGMBinding{WorkID: "bgm-123"},
		TMDB:              &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
		MediaKindOverride: &movie,
	})
	if err != nil {
		t.Fatalf("pin media kind: %v", err)
	}
	if pinned.MediaKindOverride == nil || *pinned.MediaKindOverride != "movie" || pinned.TMDB == nil || pinned.TMDB.MediaKind != "tv" {
		t.Fatalf("patch result must echo the pin and the untouched tuple: %+v", pinned)
	}
	override, tupleKind, version := readRow()
	if !override.Valid || override.String != "movie" {
		t.Fatalf("persisted pin = %v, want movie", override)
	}
	if tupleKind != "tv" {
		t.Fatalf("tmdb_media_kind = %q, want the untouched tv binding", tupleKind)
	}
	if version != 2 {
		t.Fatalf("metadata version = %d, want 2", version)
	}

	// A nil pin is the clear: the column returns to NULL and the tuple stays.
	cleared, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion:   2,
		BGM:               &BGMBinding{WorkID: "bgm-123"},
		TMDB:              &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
		MediaKindOverride: nil,
	})
	if err != nil {
		t.Fatalf("clear media kind pin: %v", err)
	}
	if cleared.MediaKindOverride != nil {
		t.Fatalf("cleared pin must read back nil, got %q", *cleared.MediaKindOverride)
	}
	override, tupleKind, version = readRow()
	if override.Valid {
		t.Fatalf("cleared pin must persist NULL, got %q", override.String)
	}
	if tupleKind != "tv" || version != 3 {
		t.Fatalf("clear must keep the tuple and bump the version once: tuple=%q version=%d", tupleKind, version)
	}

	// A value the column cannot store is refused before any write.
	invalid := "ova"
	if _, err := service.PatchAnimeMetadataApplication(ctx, animeID, MetadataPatchInput{
		ExpectedVersion:   3,
		BGM:               &BGMBinding{WorkID: "bgm-123"},
		TMDB:              &TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
		MediaKindOverride: &invalid,
	}); err == nil {
		t.Fatal("an out-of-vocabulary pin must be refused")
	}
	if override, _, version = readRow(); override.Valid || version != 3 {
		t.Fatalf("refused pin must not write: override=%v version=%d", override, version)
	}
}
