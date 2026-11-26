package httpapi

import (
	"encoding/json"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapAnimeListItemsProjectsApplicationValues(t *testing.T) {
	titleOverride := "Override"
	total := 12
	groups := []string{"Group A"}
	day := "wed"
	item := readmodelapplication.AnimeListItem{
		ManagedAnime: animeapplication.ManagedAnime{ID: "anime-1", Title: "Title", TitleOverride: &titleOverride, Version: 2},
		Metadata: animeapplication.AnimeMetadata{
			ManagedAnimeID: "anime-1",
			BGM:            &animeapplication.BGMBinding{WorkID: "bgm-1"},
			TMDB:           &animeapplication.TMDBBinding{WorkID: "tmdb-1", MediaKind: "tv", Season: 1},
			TotalEpisodes:  &total,
			Version:        3,
		},
		MetadataHold:     animeapplication.MetadataHold{Held: false, Missing: []string{}},
		BroadcastDay:     day,
		EpisodeProgress:  &readmodelapplication.EpisodeProgress{Done: true, Episodes: []int{1, 12}, Total: &total},
		SubscribedGroups: &groups,
		Subscription:     animeapplication.Subscription{ID: "sub-1", ManagedAnimeID: "anime-1", Origin: "mikan", State: "active", Version: 4},
	}
	mapped := mapAnimeListItems([]readmodelapplication.AnimeListItem{item})
	if len(mapped) != 1 || mapped[0].ManagedAnime.Id != "anime-1" || mapped[0].BroadcastDay != generated.BroadcastDayWed || mapped[0].Metadata.Bgm == nil || mapped[0].Metadata.Bgm.WorkId != "bgm-1" || mapped[0].Metadata.Tmdb.MediaKind != generated.TMDBMediaKindTv || mapped[0].EpisodeProgress == nil || !mapped[0].EpisodeProgress.Done || mapped[0].EpisodeProgress.Total == nil || *mapped[0].EpisodeProgress.Total != 12 {
		t.Fatalf("unexpected mapped item: %+v", mapped)
	}
	if mapped[0].ManagedAnime.TitleOverride == item.ManagedAnime.TitleOverride || mapped[0].EpisodeProgress.Total == item.EpisodeProgress.Total || mapped[0].SubscribedGroups == item.SubscribedGroups {
		t.Fatal("mapped list item aliases application pointers")
	}
	*item.ManagedAnime.TitleOverride = "changed"
	*item.EpisodeProgress.Total = 99
	(*item.SubscribedGroups)[0] = "changed"
	if *mapped[0].ManagedAnime.TitleOverride != "Override" || *mapped[0].EpisodeProgress.Total != 12 || (*mapped[0].SubscribedGroups)[0] != "Group A" {
		t.Fatal("mapped list item changed with application input")
	}
}

func TestMapAnimeListItemsSerializesWaitingMovieWithEmptyEpisodes(t *testing.T) {
	total := 1
	mapped := mapAnimeListItems([]readmodelapplication.AnimeListItem{{
		EpisodeProgress: &readmodelapplication.EpisodeProgress{Episodes: []int{}, Total: &total},
	}})
	got, err := json.Marshal(mapped[0].EpisodeProgress)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"done":false,"episodes":[],"total":1}`; string(got) != want {
		t.Fatalf("episode_progress = %s, want %s", got, want)
	}
}
