package domain

import (
	"testing"

	"bansoko/internal/postprocessing/naming"
)

// FrozenMediaBinding is the one projection the RSS snapshot, the saved-revision
// preview, the one-shot admission and the artifact chain resolve with. These
// cases pin the owner-kind matrix (owner kind unset / tv / movie, each against
// an unbound, a TV-bound and a movie-bound metadata row): the kind is the owner
// value with unset as tv and the TMDB kind never read, and the season is the
// TMDB binding's season, frozen for naming only.
func TestFrozenMediaBindingProjection(t *testing.T) {
	cases := []struct {
		name       string
		override   *string
		binding    *TMDBBinding
		wantKind   string
		wantSeason int
	}{
		{name: "unset kind and no tmdb binding is tv", binding: nil, wantKind: "tv", wantSeason: 0},
		{name: "unset kind with a tv binding keeps the season", binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 2, WorkId: "1"}, wantKind: "tv", wantSeason: 2},
		{name: "unset kind with a movie binding is tv, never the tmdb kind", binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, Season: 3, WorkId: "1"}, wantKind: "tv", wantSeason: 3},
		{name: "unset kind with a tv season zero", binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 0, WorkId: "1"}, wantKind: "tv", wantSeason: 0},

		{name: "tv kind without a tmdb binding", override: stringPtr("tv"), binding: nil, wantKind: "tv", wantSeason: 0},
		{name: "tv kind with a tv binding keeps the tv season", override: stringPtr("tv"), binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 2, WorkId: "1"}, wantKind: "tv", wantSeason: 2},
		{name: "tv kind with a movie binding stays tv", override: stringPtr("tv"), binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, Season: 3, WorkId: "1"}, wantKind: "tv", wantSeason: 3},

		{name: "movie kind without a tmdb binding", override: stringPtr("movie"), binding: nil, wantKind: "movie", wantSeason: 0},
		{name: "movie kind with a tv binding", override: stringPtr("movie"), binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 2, WorkId: "1"}, wantKind: "movie", wantSeason: 2},
		{name: "movie kind with a movie binding", override: stringPtr("movie"), binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, Season: 3, WorkId: "1"}, wantKind: "movie", wantSeason: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, season, bound := FrozenMediaBinding(tc.binding, tc.override)
			if kind != tc.wantKind || season != tc.wantSeason || bound {
				t.Fatalf("binding = %s/%d/%t, want %s/%d/false", kind, season, bound, tc.wantKind, tc.wantSeason)
			}
		})
	}
}

// The owner kind is filled once from the TMDB kind, only while it is unset:
// a set kind is kept whatever the binding, and no binding leaves it unset.
func TestBackfilledMediaKindFillsOnlyAnUnsetKind(t *testing.T) {
	tv := &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 1, WorkId: "1"}
	movie := &TMDBBinding{MediaKind: TMDBMediaKindMovie, WorkId: "2"}
	cases := []struct {
		name    string
		owner   *string
		binding *TMDBBinding
		want    *string
	}{
		{name: "unset kind takes the movie binding kind", binding: movie, want: stringPtr("movie")},
		{name: "unset kind takes the tv binding kind", binding: tv, want: stringPtr("tv")},
		{name: "unset kind without a binding stays unset", binding: nil, want: nil},
		{name: "a set tv kind is kept against a movie binding", owner: stringPtr("tv"), binding: movie, want: stringPtr("tv")},
		{name: "a set movie kind is kept against a tv binding", owner: stringPtr("movie"), binding: tv, want: stringPtr("movie")},
		{name: "a set kind is kept when the binding is cleared", owner: stringPtr("movie"), binding: nil, want: stringPtr("movie")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BackfilledMediaKind(tc.owner, tc.binding)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("kind = %v, want %v", got, tc.want)
			}
		})
	}
	if MediaKind(nil) != "tv" || MediaKind(stringPtr("tv")) != "tv" || MediaKind(stringPtr("movie")) != "movie" {
		t.Fatal("MediaKind must read unset as tv and the owner value otherwise")
	}
}

// TestFrozenMediaBindingSelectsTheNamingBranch pins the second consumer of the
// same decision: every frozen media kind this projection returns selects the
// naming branch the render actually uses (`anime.kind` in the naming fact set).
// The artifact chain feeds the frozen kind into the naming context's TMDB media
// kind (the naming authority frozen on the media fact snapshot), so a movie
// override must render under the movie templates exactly like a movie binding,
// and a tv override must render under the tv templates even for a movie
// binding.
func TestFrozenMediaBindingSelectsTheNamingBranch(t *testing.T) {
	cases := []struct {
		name     string
		override *string
		binding  *TMDBBinding
		want     string
	}{
		{name: "unset override unbound renders tv", want: "tv"},
		{name: "unset override movie binding renders tv", binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, WorkId: "1"}, want: "tv"},
		{name: "tv override movie binding renders tv", override: stringPtr("tv"), binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, WorkId: "1"}, want: "tv"},
		{name: "movie override tv binding renders movie", override: stringPtr("movie"), binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 2, WorkId: "1"}, want: "movie"},
		{name: "movie override without a binding renders movie", override: stringPtr("movie"), want: "movie"},
		{name: "tv override unbound renders tv", override: stringPtr("tv"), want: "tv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, _, _ := FrozenMediaBinding(tc.binding, tc.override)
			context := naming.NamingContext{
				Anime: naming.AnimeNamingFacts{
					ID:    "anime",
					Title: "Show",
					// The frozen media kind is what the file operation froze as
					// the naming authority; the naming unit receives it here.
					TMDB: naming.TMDBNamingFacts{WorkID: "1", MediaKind: kind},
				},
				File: naming.FileNamingFacts{OriginalName: "E01.mkv", Basename: "E01", Extension: "mkv", Kind: "video"},
			}
			// Resolve through the default registry directly: the naming-branch
			// resolver is a pure function of the frozen facts, and the vars
			// namespace is not needed to select the branch.
			resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: context})
			if got := resolved.Context.Derived.EffectiveMediaKind; got != tc.want {
				t.Fatalf("effective media kind = %q, want %q", got, tc.want)
			}
			facts := naming.UnifiedFacts(resolved.Context)
			if got := facts.Anime.Kind; got != tc.want {
				t.Fatalf("anime.kind = %q, want %q", got, tc.want)
			}
		})
	}
}
