package naming

import (
	"testing"

	"bansoko/internal/templatefacts"
)

// TestEpisodeSourceFollowsTheFrozenEvidence pins ep.source to the evidence
// owner that resolved the episode. It used to be the constant "custom" for
// every resolved episode, which made it a hardcoded classification rather
// than a provenance fact: a template reading it could not tell a file-name
// match from a release-title match. (Namespace consistency audit D1.)
func TestEpisodeSourceFollowsTheFrozenEvidence(t *testing.T) {
	context := func(source string) NamingContext {
		ctx := NamingContext{
			Anime: AnimeNamingFacts{Title: "Show"},
			File:  FileNamingFacts{OriginalName: "[Grp] Show - 07.mkv", Basename: "[Grp] Show - 07", Extension: "mkv"},
			Release: ReleaseNamingFacts{
				FrozenEpisode: &FrozenEpisodeNamingFacts{Raw: "07", Number: "7", Display: "7", Source: source},
			},
		}
		return DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
	}
	for _, tc := range []struct {
		name   string
		source string
		want   any
	}{
		// The four evidence owners plus the file-manager override.
		{"release title parse", "release_parser", "release_parser"},
		{"file name parse", "file_parser", "file_parser"},
		{"provider tag", "anibt", "anibt"},
		{"movie", "movie", "movie"},
		{"manual match", "manual", "manual"},
		// No evidence owner carried over: the fact is empty, never a class the
		// system made up.
		{"no frozen source", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := UnifiedFacts(context(tc.source))
			fact, ok := templatefacts.Lookup("ep.source")
			if !ok {
				t.Fatal("ep.source is not registered")
			}
			if got := fact.Value(&facts); got != tc.want {
				t.Fatalf("ep.source = %#v, want %#v", got, tc.want)
			}
		})
	}
}
