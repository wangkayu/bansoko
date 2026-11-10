package adapter

import (
	"testing"

	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	"bansoko/internal/metadata/matcher"
)

func TestAutomaticSearchCandidateProjectsMatchedProviderAndSeason(t *testing.T) {
	season := 2
	year := 2026
	bgmID, workID, kind := "123", "42", "tv"
	result := &matcher.MatchResult{
		BgmID: &bgmID, BgmTitle: "Original", BgmTitleCN: "中文标题", BgmPosterURL: "https://bgm.tv/poster.jpg", BgmYear: &year,
		TmdbWorkID: &workID, TmdbKind: &kind, TmdbSeason: &season,
		TmdbTitleCN: "TMDB 标题", TmdbOriginalTitle: "TMDB Original", TmdbYear: &year,
		TmdbSeasons: []identitymetadatadomain.SearchSeason{{SeasonNumber: 2, Name: "Season 2", EpisodeCount: 12}},
	}
	bgm := automaticSearchCandidate(identitymetadatadomain.AutomaticSearchInput{Provider: "bgm", Title: "Input"}, result)
	if bgm == nil || bgm.ProviderWorkID != "123" || bgm.Title != "中文标题" || bgm.PosterURL == "" || bgm.Year != 2026 {
		t.Fatalf("BGM candidate=%+v", bgm)
	}
	tmdb := automaticSearchCandidate(identitymetadatadomain.AutomaticSearchInput{Provider: "tmdb", Title: "Input"}, result)
	if tmdb == nil || tmdb.ProviderWorkID != "42" || tmdb.SuggestedSeason == nil || *tmdb.SuggestedSeason != 2 || len(tmdb.Seasons) != 1 || tmdb.DetailsStatus != identitymetadatadomain.DetailsStatusAvailable {
		t.Fatalf("TMDB candidate=%+v", tmdb)
	}
}

func TestAutomaticSearchCandidateRejectsIncompleteOrConflictingMatch(t *testing.T) {
	workID, kind := "42", "tv"
	result := &matcher.MatchResult{TmdbWorkID: &workID, TmdbKind: &kind}
	if candidate := automaticSearchCandidate(identitymetadatadomain.AutomaticSearchInput{Provider: "tmdb", Title: "Show S2"}, result); candidate != nil {
		t.Fatalf("missing season must remain unselected: %+v", candidate)
	}
	if candidate := automaticSearchCandidate(identitymetadatadomain.AutomaticSearchInput{Provider: "bgm", Title: "Show"}, &matcher.MatchResult{}); candidate != nil {
		t.Fatalf("missing BGM ID must remain unselected: %+v", candidate)
	}
}
