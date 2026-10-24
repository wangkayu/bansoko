package naming

import (
	"reflect"
	"sort"
	"testing"

	providerdomain "bansoko/internal/provider/domain"
)

// The replay fact set is enumerated in three places that must never diverge: the
// naming snapshot writer (ReplayFactValues), the FileFact projection
// (fileoperation) and the FileFact registry (acquisition/terminal/domain), plus
// the database's source-relation validator. Every one of them now derives from
// ReplayFactPaths; these tests pin the two properties the validator relies on:
// the set is exactly the documented names, and every path has a value in the
// snapshot map.
func TestReplayFactPathsIsTheCanonicalSet(t *testing.T) {
	paths := ReplayFactPaths()
	// A duplicate would silently shrink the set: the snapshot map would drop one
	// name while ReplayFactPaths still listed it.
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, dup := seen[path]; dup {
			t.Fatalf("duplicate replay fact path %q", path)
		}
		seen[path] = struct{}{}
	}
	// The DB validator enumerates the same names in a literal array; keep the
	// count pinned so adding a fact forces a look at that array too.
	if len(paths) != 11 {
		t.Fatalf("replay fact set has %d entries, want 11 (update the source-relation migration as well)", len(paths))
	}
	want := []string{
		"Anime.BGM.WorkID", "Anime.TMDB.WorkID",
		"File.Media.Container", "File.Media.Resolution", "File.Media.VideoCodec",
		"Parsed.Episode.Display", "Parsed.Language.Code", "Release.FrozenEpisode.Display",
		"Release.OriginalTitle", "Source.Group", "Source.GroupName",
	}
	got := append([]string(nil), paths...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replay fact set changed:\n got %v\nwant %v", got, want)
	}
}

func TestReplayFactValuesCoversEveryPath(t *testing.T) {
	values := ReplayFactValues(NamingContext{})
	for _, path := range ReplayFactPaths() {
		if _, ok := values[path]; !ok {
			t.Fatalf("ReplayFactValues is missing %q", path)
		}
	}
	if len(values) != len(ReplayFactPaths()) {
		t.Fatalf("ReplayFactValues has %d entries, want %d", len(values), len(ReplayFactPaths()))
	}
}

func TestSourceGroupKeyNamespacesTheProvider(t *testing.T) {
	cases := []struct {
		name  string
		group providerdomain.GroupRef
		want  string
	}{
		{"zero", providerdomain.GroupRef{}, ""},
		{"partial", providerdomain.GroupRef{Provider: providerdomain.ProviderMikan}, ""},
		{"mikan", providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "58"}, "mikan:58"},
		{"anibt", providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "samplegroup"}, "anibt:samplegroup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := ReplayFactValues(NamingContext{Source: SourceNamingFacts{Group: tc.group}})
			if got := values["Source.Group"]; got != tc.want {
				t.Fatalf("Source.Group = %q, want %q", got, tc.want)
			}
		})
	}
}
