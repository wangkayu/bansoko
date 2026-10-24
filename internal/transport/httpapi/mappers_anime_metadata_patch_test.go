package httpapi

import (
	"testing"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapMetadataPatchInput(t *testing.T) {
	title := "Title"
	episodes := 12
	override := generated.AnimeMetadataPatchMediaKindOverrideMovie
	value := generated.AnimeMetadataPatch{
		Bgm:               &generated.NullableBGMBinding{WorkId: "bgm-1"},
		ExpectedVersion:   7,
		TitleOverride:     &title,
		Tmdb:              &generated.NullableTMDBBinding{WorkId: "tmdb-1", MediaKind: generated.TMDBMediaKindTv, Season: 2},
		TotalEpisodes:     &episodes,
		MediaKindOverride: &override,
	}
	mapped := mapMetadataPatchInput(value)
	if mapped.ExpectedVersion != 7 || mapped.BGM == nil || mapped.BGM.WorkID != "bgm-1" || mapped.TMDB == nil || mapped.TMDB.WorkID != "tmdb-1" || mapped.TMDB.MediaKind != "tv" || mapped.TMDB.Season != 2 || mapped.TitleOverride == nil || *mapped.TitleOverride != title || mapped.TotalEpisodes == nil || *mapped.TotalEpisodes != episodes || mapped.MediaKindOverride == nil || *mapped.MediaKindOverride != "movie" {
		t.Fatalf("unexpected mapped input: %+v", mapped)
	}
	if mapped.TitleOverride == value.TitleOverride || mapped.TotalEpisodes == value.TotalEpisodes || (*string)(mapped.MediaKindOverride) == (*string)(value.MediaKindOverride) {
		t.Fatal("mapped pointers alias generated input")
	}
}

func TestMapMetadataPatchResult(t *testing.T) {
	title := "Title"
	episodes := 12
	override := "movie"
	value := animeapplication.AnimeMetadata{
		ManagedAnimeID:    "anime-1",
		BGM:               &animeapplication.BGMBinding{WorkID: "bgm-1"},
		TMDB:              &animeapplication.TMDBBinding{WorkID: "tmdb-1", MediaKind: "tv", Season: 2},
		TitleOverride:     &title,
		TotalEpisodes:     &episodes,
		MediaKindOverride: &override,
		Version:           3,
	}
	mapped, err := mapMetadataPatchResult(value)
	if err != nil {
		t.Fatalf("map metadata patch result: %v", err)
	}
	if mapped.ManagedAnimeId != "anime-1" || mapped.Version != 3 || mapped.Bgm == nil || mapped.Bgm.WorkId != "bgm-1" || mapped.Tmdb == nil || mapped.Tmdb.MediaKind != generated.TMDBMediaKindTv || mapped.TitleOverride == nil || *mapped.TitleOverride != title || mapped.TotalEpisodes == nil || *mapped.TotalEpisodes != episodes || mapped.MediaKindOverride == nil || string(*mapped.MediaKindOverride) != "movie" {
		t.Fatalf("unexpected mapped result: %+v", mapped)
	}
	*value.TitleOverride = "changed"
	*value.TotalEpisodes = 99
	*value.MediaKindOverride = "tv"
	if *mapped.TitleOverride != "Title" || *mapped.TotalEpisodes != 12 || string(*mapped.MediaKindOverride) != "movie" {
		t.Fatal("mapped result aliases application input")
	}
}

func TestMapMetadataPatchResultRejectsUnknownMediaKind(t *testing.T) {
	_, err := mapMetadataPatchResult(animeapplication.AnimeMetadata{
		ManagedAnimeID: "anime-1",
		TMDB:           &animeapplication.TMDBBinding{WorkID: "tmdb-1", MediaKind: "unknown"},
	})
	if err == nil {
		t.Fatal("unknown TMDB media kind must fail closed")
	}
}
