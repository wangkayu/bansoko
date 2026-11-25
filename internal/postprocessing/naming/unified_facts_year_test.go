package naming

import "testing"

// TestUnifiedFactsYearIsTheOwnerValue pins anime.year to the owner-edited year:
// a provider year on the bound works never stands in, and an unset year is
// empty. naming-render-1 replays the old provider projection.
func TestUnifiedFactsYearIsTheOwnerValue(t *testing.T) {
	year := func(value int) *int { return &value }
	for _, tc := range []struct {
		name  string
		owner *int
		want  string
	}{
		{"owner year", year(2024), "2024"},
		{"unset stays empty despite provider years", nil, ""},
		{"out of range is empty", year(1800), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := NamingContext{Anime: AnimeNamingFacts{
				Title: "Show", Year: tc.owner,
				BGM:  BGMNamingFacts{WorkID: "bgm", Year: year(2019)},
				TMDB: TMDBNamingFacts{WorkID: "tmdb", MediaKind: "tv", Year: year(2020)},
			}}
			facts := UnifiedFacts(ctx)
			if facts.Anime.Year != tc.want {
				t.Fatalf("year = %q; want %q", facts.Anime.Year, tc.want)
			}
			if legacy := renderFacts(ctx, 1); legacy.Anime.Year != "2020" {
				t.Fatalf("naming-render-1 replay year = %q; want the TMDB provider year", legacy.Anime.Year)
			}
		})
	}
}
