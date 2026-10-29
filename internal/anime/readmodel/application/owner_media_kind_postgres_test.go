package application

import (
	"context"
	"testing"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/testutil/animetest"
)

// The owner media kind must reach both metadata read models — the workspace
// metadata value and every anime list item — through the same set/read-back
// round trip title_override has. Subscribing a TV tuple backfills tv. While a
// movie kind is set, the read model must still report the TMDB tuple the
// matcher wrote: the kind is a separate owner field, never a rewrite of the
// binding.
func TestWorkspaceAndListProjectMediaKind(t *testing.T) {
	db := animetest.OpenTestDB(t)
	readmodel, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	ctx := context.Background()
	subscribed := readmodelSubscribeTestDraft(t, root, "subscribe-key-media-kind-override")
	animeID := subscribed.ManagedAnime.Id

	listOverride := func() *string {
		t.Helper()
		items, err := readmodel.ListAnimeApplication(ctx)
		if err != nil {
			t.Fatalf("list anime: %v", err)
		}
		for _, item := range items {
			if item.ManagedAnime.ID == animeID {
				return item.Metadata.MediaKind
			}
		}
		t.Fatalf("subscribed anime %q is missing from the list", animeID)
		return nil
	}

	workspace, err := readmodel.GetAnimeWorkspaceApplication(ctx, animeID)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if workspace.Metadata.MediaKind == nil || *workspace.Metadata.MediaKind != "tv" {
		t.Fatalf("a fresh TV subscription must read back the backfilled tv kind, got %v", workspace.Metadata.MediaKind)
	}
	if got := listOverride(); got == nil || *got != "tv" {
		t.Fatalf("a fresh TV subscription must list the backfilled tv kind, got %v", got)
	}

	movie := "movie"
	if _, err := root.PatchAnimeMetadataApplication(ctx, animeID, ownershipapplication.MetadataPatchInput{
		ExpectedVersion: int64(workspace.Metadata.Version),
		BGM:             &ownershipapplication.BGMBinding{WorkID: "bgm-123"},
		TMDB:            &ownershipapplication.TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
		MediaKind:       &movie,
	}); err != nil {
		t.Fatalf("pin media kind: %v", err)
	}

	workspace, err = readmodel.GetAnimeWorkspaceApplication(ctx, animeID)
	if err != nil {
		t.Fatalf("read workspace after pin: %v", err)
	}
	if workspace.Metadata.MediaKind == nil || *workspace.Metadata.MediaKind != "movie" {
		t.Fatalf("workspace metadata.media_kind must echo the pin, got %+v", workspace.Metadata.MediaKind)
	}
	if workspace.Metadata.TMDB == nil || workspace.Metadata.TMDB.MediaKind != "tv" || workspace.Metadata.TMDB.WorkID != "tmdb-456" {
		t.Fatalf("the pin must not rewrite the TMDB tuple, got %+v", workspace.Metadata.TMDB)
	}
	if got := listOverride(); got == nil || *got != "movie" {
		t.Fatalf("list item media_kind must echo the pin, got %v", got)
	}

	tv := "tv"
	if _, err := root.PatchAnimeMetadataApplication(ctx, animeID, ownershipapplication.MetadataPatchInput{
		ExpectedVersion: int64(workspace.Metadata.Version),
		BGM:             &ownershipapplication.BGMBinding{WorkID: "bgm-123"},
		TMDB:            &ownershipapplication.TMDBBinding{WorkID: "tmdb-456", MediaKind: "tv", Season: 1},
		MediaKind:       &tv,
	}); err != nil {
		t.Fatalf("set media kind back to tv: %v", err)
	}

	workspace, err = readmodel.GetAnimeWorkspaceApplication(ctx, animeID)
	if err != nil {
		t.Fatalf("read workspace after the tv edit: %v", err)
	}
	if workspace.Metadata.MediaKind == nil || *workspace.Metadata.MediaKind != "tv" {
		t.Fatalf("workspace metadata.media_kind must read back tv, got %v", workspace.Metadata.MediaKind)
	}
	if got := listOverride(); got == nil || *got != "tv" {
		t.Fatalf("list media_kind must read back tv, got %v", got)
	}
}
