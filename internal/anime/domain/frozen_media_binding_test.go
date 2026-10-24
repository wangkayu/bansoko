package domain

import (
	"testing"

	"bansoko/internal/postprocessing/naming"
)

// FrozenMediaBinding is the one projection the RSS snapshot, the saved-revision
// preview, the one-shot admission and the artifact chain resolve with. These
// cases pin the three-state matrix (override unset / tv / movie, each against
// an unbound, a TV-bound and a movie-bound metadata row) and the metadata-hold
// contract: BGM-only is the unbound TV default, a movie binding is never
// guessed from a title, and a TV binding keeps its season without claiming
// continuous-SP season 0 semantics.
func TestFrozenMediaBindingProjection(t *testing.T) {
	cases := []struct {
		name       string
		override   *string
		binding    *TMDBBinding
		wantKind   string
		wantSeason int
		wantBound  bool
	}{
		{name: "no override and no tmdb binding is the unbound tv default", binding: nil, wantKind: "tv", wantSeason: 0, wantBound: false},
		{name: "no override with a tv binding keeps the season", binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 2, WorkId: "1"}, wantKind: "tv", wantSeason: 2, wantBound: false},
		{name: "no override with a movie binding", binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, Season: 0, WorkId: "1"}, wantKind: "movie", wantSeason: 0, wantBound: false},
		{name: "no override with a movie binding keeps its season as evidence", binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, Season: 3, WorkId: "1"}, wantKind: "movie", wantSeason: 3, wantBound: false},
		{name: "no override with an unknown stored kind is the tv default, never movie", binding: &TMDBBinding{MediaKind: "", Season: 4, WorkId: "1"}, wantKind: "tv", wantSeason: 4, wantBound: false},
		{name: "no override with a tv season zero stays unbound", binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 0, WorkId: "1"}, wantKind: "tv", wantSeason: 0, wantBound: false},

		{name: "tv override without a tmdb binding stays the unbound tv default", override: stringPtr("tv"), binding: nil, wantKind: "tv", wantSeason: 0, wantBound: false},
		{name: "tv override with a tv binding keeps the tv season", override: stringPtr("tv"), binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 2, WorkId: "1"}, wantKind: "tv", wantSeason: 2, wantBound: false},
		{name: "tv override with a movie binding returns to the tv path", override: stringPtr("tv"), binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, Season: 3, WorkId: "1"}, wantKind: "tv", wantSeason: 3, wantBound: false},

		{name: "movie override without a tmdb binding by name only is enough", override: stringPtr("movie"), binding: nil, wantKind: "movie", wantSeason: 0, wantBound: false},
		{name: "movie override with a tv binding resolves exactly like a movie binding", override: stringPtr("movie"), binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 2, WorkId: "1"}, wantKind: "movie", wantSeason: 2, wantBound: false},
		{name: "movie override with a movie binding is idempotent", override: stringPtr("movie"), binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, Season: 3, WorkId: "1"}, wantKind: "movie", wantSeason: 3, wantBound: false},
		{name: "movie override keeps the season source of the binding", override: stringPtr("movie"), binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 0, WorkId: "1"}, wantKind: "movie", wantSeason: 0, wantBound: false},

		// A stored value outside tv|movie cannot be persisted by the writer; if
		// one is ever read back, the binding stays the resolution instead of the
		// unknown value silently pinning a branch.
		{name: "a blank override falls back to the binding", override: stringPtr(""), binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, Season: 1, WorkId: "1"}, wantKind: "movie", wantSeason: 1, wantBound: false},
		{name: "an unknown override falls back to the binding", override: stringPtr("unknown"), binding: &TMDBBinding{MediaKind: TMDBMediaKindTv, Season: 5, WorkId: "1"}, wantKind: "tv", wantSeason: 5, wantBound: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, season, bound := FrozenMediaBinding(tc.binding, tc.override)
			if kind != tc.wantKind || season != tc.wantSeason || bound != tc.wantBound {
				t.Fatalf("binding = %s/%d/%t, want %s/%d/%t", kind, season, bound, tc.wantKind, tc.wantSeason, tc.wantBound)
			}
		})
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
		{name: "unset override movie binding renders movie", binding: &TMDBBinding{MediaKind: TMDBMediaKindMovie, WorkId: "1"}, want: "movie"},
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
