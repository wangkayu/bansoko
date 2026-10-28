package application

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// A metadata PATCH that carries a TMDB binding and a title_override writes both
// in one version step: the binding and the fixed title land together, and the
// metadata version moves once.
func TestPatchAnimeMetadataWritesBindingAndTitleInOneStep(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	draft := ownershipTestDraft(t)
	draft.Metadata.Tmdb = nil
	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "subscribe-key-title-with-binding")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID := subscribed.ManagedAnime.Id
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&version); err != nil {
		t.Fatal(err)
	}

	title := "拼接后的标题"
	tmdb := &generated.TMDBBinding{WorkId: "777001", MediaKind: generated.TMDBMediaKindTv, Season: 2}
	patched, err := service.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{
		ExpectedVersion: version, Tmdb: tmdb, TitleOverride: &title,
	})
	if err != nil {
		t.Fatalf("patch binding with title: %v", err)
	}
	if patched.Version != version+1 {
		t.Fatalf("version = %d, want one step from %d", patched.Version, version)
	}
	if patched.TitleOverride == nil || *patched.TitleOverride != title || patched.Tmdb == nil || patched.Tmdb.WorkId != "777001" {
		t.Fatalf("response lacks the binding or the title: %+v", patched)
	}
	var storedTitle, storedTMDB sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT ma.title_override, m.tmdb_work_id FROM managed_anime ma JOIN anime_metadata m ON m.managed_anime_id = ma.id WHERE ma.id = $1`, animeID).Scan(&storedTitle, &storedTMDB); err != nil {
		t.Fatal(err)
	}
	if !storedTitle.Valid || storedTitle.String != title || storedTMDB.String != "777001" {
		t.Fatalf("stored title=%+v tmdb=%+v, want the title and the binding", storedTitle, storedTMDB)
	}

	// Clearing the binding afterwards leaves the title alone.
	cleared, err := service.PatchAnimeMetadata(ctx, animeID, generated.AnimeMetadataPatch{ExpectedVersion: patched.Version})
	if err != nil {
		t.Fatalf("clear binding: %v", err)
	}
	if cleared.TitleOverride == nil || *cleared.TitleOverride != title {
		t.Fatalf("clearing the binding changed the title: %+v", cleared.TitleOverride)
	}
}
