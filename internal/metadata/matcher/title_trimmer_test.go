package matcher

import "testing"

func TestTrimTitleRemovesOnlySeasonMarker(t *testing.T) {
	trimmer := NewTitleTrimmer()
	for _, tc := range []struct{ input, want string }{
		{"Show S2: Subtitle", "Show: Subtitle"},
		{"Show 第三季【副标题】", "Show【副标题】"},
		{"Show - 2nd Season", "Show -"},
		{"Part 2: Show", "Part 2: Show"},
		{"第2部分 Show", "第2部分 Show"},
		{"第2クール Show", "第2クール Show"},
		{"Cour 2 Show", "Cour 2 Show"},
	} {
		if got := trimmer.TrimTitle(tc.input); got != tc.want {
			t.Errorf("TrimTitle(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

func TestExtractSeasonNumberBoundariesAndRange(t *testing.T) {
	trimmer := NewTitleTrimmer()
	for _, tc := range []struct {
		title string
		want  int
		ok    bool
	}{
		{"Show S0", 0, true}, {"Show 第三季", 3, true}, {"Show 第20期", 20, true},
		{"Show Season 2", 2, true}, {"Show 2nd Season", 2, true},
		{"第2部分 Show", 0, false}, {"第2クール Show", 0, false},
		{"Part 2 Show", 0, false}, {"Cour 2 Show", 0, false}, {"Show S21", 0, false},
	} {
		got := trimmer.ExtractSeasonNumber(tc.title)
		if (got != nil) != tc.ok || got != nil && *got != tc.want {
			t.Errorf("ExtractSeasonNumber(%q) = %v; want %d, ok=%t", tc.title, got, tc.want, tc.ok)
		}
	}
}

func TestExtractSeasonNumberRejectsInvalidChineseNumeral(t *testing.T) {
	if got := NewTitleTrimmer().ExtractSeasonNumber("第十十季"); got != nil {
		t.Fatalf("invalid Chinese season marker must be rejected: got %v", *got)
	}
}

func TestMediaPreferenceUsesBoundedOriginalTitleMarkers(t *testing.T) {
	trimmer := NewTitleTrimmer()
	for _, tc := range []struct {
		title string
		want  mediaPreference
	}{
		{"电影版：作品", mediaPrefMovieOnly}, {"劇場版 作品", mediaPrefMovieOnly},
		{"Show Movie", mediaPrefMovieOnly}, {"Show (Movie)", mediaPrefMovieOnly},
		{"电影少女", mediaPrefNone}, {"电影研究社", mediaPrefNone},
		{"在电影院里遇到的她", mediaPrefNone}, {"映画監督", mediaPrefNone},
		{"映画館", mediaPrefNone}, {"Movie Star", mediaPrefNone},
		{"Show S2", mediaPrefTVOnly}, {"电影版：作品 S2", mediaPrefMovieOnly},
	} {
		if got := mediaPreferenceForTitle(tc.title, trimmer); got != tc.want {
			t.Errorf("mediaPreferenceForTitle(%q) = %v; want %v", tc.title, got, tc.want)
		}
	}
}
