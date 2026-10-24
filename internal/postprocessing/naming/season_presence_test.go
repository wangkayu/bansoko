package naming

import "testing"

func TestSeasonZeroPresenceIsNotMissing(t *testing.T) {
	for _, tc := range []struct {
		present bool
		want    string
	}{{true, ResolverStateValue}, {false, ResolverStateNull}} {
		facts := NamingContext{}
		facts.Anime.TMDB.Season = 0
		facts.Anime.TMDB.SeasonPresent = tc.present
		result := (seasonFolderResolver{}).Resolve(ResolverInput{Facts: facts})
		if result.State != tc.want {
			t.Fatalf("present=%v state=%s", tc.present, result.State)
		}
		if tc.present && result.Value.(seasonFolderValue).SeasonFolder != "Season 00" {
			t.Fatalf("value=%v", result.Value)
		}
	}
}
