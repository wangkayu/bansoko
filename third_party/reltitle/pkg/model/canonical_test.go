package model

import (
	"reflect"
	"testing"
)

func resultWithAttrs(attrs ...Attribute) *ParseResult {
	return &ParseResult{Attributes: attrs}
}

func TestCanonicalAttributeAccessors(t *testing.T) {
	r := resultWithAttrs(
		Attribute{Type: AttrTitle, Normalized: "  First  "},
		Attribute{Type: AttrTitle, Normalized: "Second"},
		Attribute{Type: AttrGroup, Normalized: "Group"},
		Attribute{Type: AttrTitleAlias, Normalized: "Alias A"},
		Attribute{Type: AttrTitleAlias, Normalized: "   "}, // blank values are skipped
		Attribute{Type: AttrTitleAlias, Normalized: "Alias B"},
		Attribute{Type: AttrSearchHint, Normalized: "hint"},
		Attribute{Type: AttrVersion, Normalized: "v2"},
		Attribute{Type: AttrPackKind, Normalized: "bd"},
		Attribute{Type: AttrPackProfile, Normalized: "movie_series_mix"},
		Attribute{Type: AttrMovieIndex, Normalized: "2"},
		Attribute{Type: AttrMovieIndexRange, Normalized: "1-3"},
	)

	if got := r.CanonicalTitle(); got != "First" {
		t.Errorf("CanonicalTitle=%q, want the first title, trimmed", got)
	}
	if got := r.CanonicalGroup(); got != "Group" {
		t.Errorf("CanonicalGroup=%q", got)
	}
	if got, want := r.CanonicalTitleAliases(), []string{"Alias A", "Alias B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CanonicalTitleAliases=%q, want %q", got, want)
	}
	if got, want := r.CanonicalSearchHints(), []string{"hint"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CanonicalSearchHints=%q, want %q", got, want)
	}
	for name, got := range map[string]string{
		"CanonicalVersion":         r.CanonicalVersion(),
		"CanonicalPackKind":        r.CanonicalPackKind(),
		"CanonicalPackProfile":     r.CanonicalPackProfile(),
		"CanonicalMovieIndex":      r.CanonicalMovieIndex(),
		"CanonicalMovieIndexRange": r.CanonicalMovieIndexRange(),
	} {
		want := map[string]string{
			"CanonicalVersion": "v2", "CanonicalPackKind": "bd", "CanonicalPackProfile": "movie_series_mix",
			"CanonicalMovieIndex": "2", "CanonicalMovieIndexRange": "1-3",
		}[name]
		if got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}
}

func TestCanonicalAccessorsOnEmptyAndNilResults(t *testing.T) {
	for name, r := range map[string]*ParseResult{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			if r.CanonicalTitle() != "" || r.CanonicalGroup() != "" || r.CanonicalVersion() != "" ||
				r.CanonicalEpisode() != "" || r.CanonicalEpisodeRange() != "" || r.CanonicalSeason() != "" ||
				r.CanonicalSeasonRange() != "" || r.CanonicalSPKind() != "" || r.CanonicalEpRoute() != "" {
				t.Fatal("expected empty strings")
			}
			if len(r.CanonicalTitleAliases()) != 0 || len(r.CanonicalSearchHints()) != 0 {
				t.Fatal("expected no aliases or hints")
			}
			if r.CanonicalEPSemantics() != nil {
				t.Fatal("expected nil semantics")
			}
		})
	}
}

func TestCanonicalEPAccessorsReadPrimary(t *testing.T) {
	r := &ParseResult{EPSemantics: &EPSemantics{Primary: &EPReference{
		Kind: "EP", Season: " 2 ", Episode: " 5 ", SeasonRange: "S1-S3", SPKind: "OVA",
	}}}
	if got := r.CanonicalEpisode(); got != "5" {
		t.Errorf("CanonicalEpisode=%q", got)
	}
	if got := r.CanonicalSeason(); got != "2" {
		t.Errorf("CanonicalSeason=%q", got)
	}
	if got := r.CanonicalSeasonRange(); got != "S1-S3" {
		t.Errorf("CanonicalSeasonRange=%q", got)
	}
	if got := r.CanonicalSPKind(); got != "OVA" {
		t.Errorf("CanonicalSPKind=%q", got)
	}
}

func TestCanonicalEpisodeRange(t *testing.T) {
	tests := []struct {
		name       string
		start, end string
		want       string
	}{
		{"both ends", "1", "12", "1-12"},
		{"trims", " 01 ", " 12 ", "01-12"},
		{"missing end", "1", "", ""},
		{"missing start", "", "12", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &ParseResult{EPSemantics: &EPSemantics{Primary: &EPReference{Kind: "RANGE", RangeStart: tc.start, RangeEnd: tc.end}}}
			if got := r.CanonicalEpisodeRange(); got != tc.want {
				t.Fatalf("CanonicalEpisodeRange=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestCanonicalEpRoute(t *testing.T) {
	tests := []struct {
		name string
		ref  EPReference
		want string
	}{
		{"range", EPReference{Kind: "RANGE"}, "RANGE"},
		{"episode list", EPReference{Kind: "EP_LIST"}, "EP_LIST"},
		{"special", EPReference{Kind: "SP"}, "SP_ONLY"},
		{"tagged episode wins over season", EPReference{Kind: "EP", Source: "TAGGED_EP_FINAL", Season: "2"}, "ABS_EP"},
		{"season and episode", EPReference{Kind: "EP", Season: "2"}, "SXXEXX"},
		{"plain episode", EPReference{Kind: "EP"}, "PLAIN_EP"},
		{"season only", EPReference{Kind: "SEASON"}, "NONE"},
		{"season range", EPReference{Kind: "SEASON_RANGE"}, "NONE"},
		{"kind is case-insensitive", EPReference{Kind: " ep "}, "PLAIN_EP"},
		{"unknown kind", EPReference{Kind: "WHATEVER"}, ""},
		{"no kind", EPReference{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := tc.ref
			r := &ParseResult{EPSemantics: &EPSemantics{Primary: &ref}}
			if got := r.CanonicalEpRoute(); got != tc.want {
				t.Fatalf("CanonicalEpRoute=%q, want %q", got, tc.want)
			}
		})
	}
}

// CanonicalEPSemantics hands out a copy, so callers cannot change the parse result.
func TestCanonicalEPSemanticsReturnsIndependentCopy(t *testing.T) {
	r := &ParseResult{EPSemantics: &EPSemantics{
		Primary:    &EPReference{Kind: "EP_LIST", EpisodeList: []string{"1", "2"}, Episode: " 1 "},
		Alternates: []EPReference{{Kind: "EP", EpisodeList: []string{"9"}}},
	}}

	got := r.CanonicalEPSemantics()
	if got == r.EPSemantics || got.Primary == r.EPSemantics.Primary {
		t.Fatal("expected a deep copy, got shared pointers")
	}
	got.Primary.EpisodeList[0] = "changed"
	got.Primary.Episode = "changed"
	got.Alternates[0].EpisodeList[0] = "changed"

	if r.EPSemantics.Primary.EpisodeList[0] != "1" || r.EPSemantics.Primary.Episode != " 1 " || r.EPSemantics.Alternates[0].EpisodeList[0] != "9" {
		t.Fatalf("mutating the copy changed the original: %+v / %+v", r.EPSemantics.Primary, r.EPSemantics.Alternates)
	}
}
