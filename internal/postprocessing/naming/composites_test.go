package naming

import (
	"errors"
	"strings"
	"testing"

	"bansoko/internal/templatefacts"
)

// The system composites state their output for every catalog row case.
func TestSeasonEpisodeComposite(t *testing.T) {
	one, zero, big := templatefacts.Int64(1), templatefacts.Int64(0), templatefacts.Int64(12)
	for _, tc := range []struct {
		kind              string
		season            *int64
		number, numberEnd string
		want              string
	}{
		{"tv", one, "7", "", "S01E07"},
		{"tv", one, "7", "8", "S01E07-E08"},
		{"tv", one, "7", "7", "S01E07"},
		{"tv", zero, "1", "", "S00E01"},
		{"tv", nil, "7", "", "E07"},
		{"tv", nil, "7", "8", "E07-E08"},
		{"tv", nil, "", "", ""},
		{"tv", big, "107", "", "S12E107"},
		{"tv", one, "", "", ""},
		{"movie", one, "7", "", ""},
	} {
		if got := SeasonEpisode(tc.kind, tc.season, tc.number, tc.numberEnd); got != tc.want {
			t.Errorf("SeasonEpisode(%s, %v, %q, %q) = %q, want %q", tc.kind, tc.season, tc.number, tc.numberEnd, got, tc.want)
		}
	}
	for _, tc := range []struct {
		kind   string
		season *int64
		want   string
	}{
		{"tv", one, "/Season 01"}, {"tv", zero, "/Season 00"}, {"tv", nil, ""}, {"movie", one, ""},
	} {
		if got := SeasonPath(tc.kind, tc.season); got != tc.want {
			t.Errorf("SeasonPath(%s, %v) = %q, want %q", tc.kind, tc.season, got, tc.want)
		}
	}
}

// naming-render-3 path cleanup, compared with FileBot: control characters,
// leading dots and trailing dots/spaces go; Windows-reserved characters stay
// (sanitize() is the opt-in); an over-long segment fails.
func TestSanitizeSegmentFollowsFileBot(t *testing.T) {
	for in, want := range map[string]string{
		"Show...":         "Show",
		"Show . ":         "Show",
		".hidden":         "hidden",
		"Re:Zero?":        "Re:Zero?",
		"Line\nbreak\tok": "Linebreakok",
		"Show - .":        "Show",
		"...":             "",
	} {
		if got := sanitizeSegment(in); got != want {
			t.Errorf("sanitizeSegment(%q) = %q, want %q", in, got, want)
		}
	}
	engine := NewEngine()
	directory, err := engine.Compile(KindDirectory, "/library/${anime.title}./x")
	if err != nil {
		t.Fatal(err)
	}
	ctx := goldenContext("tv", "5")
	if got, err := engine.Render(directory, ctx); err != nil || got != "/library/Show/x" {
		t.Fatalf("trailing dot directory = %q, %v", got, err)
	}
	render2, err := engine.ForRenderer("naming-render-2")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := render2.Render(directory, ctx); err != nil || got != "/library/Show./x" {
		t.Fatalf("naming-render-2 replay must keep its normalization: %q, %v", got, err)
	}
	filename, err := engine.Compile(KindVideoFilename, "${anime.title}.${file.ext}")
	if err != nil {
		t.Fatal(err)
	}
	noExt := ctx
	noExt.File.Extension = ""
	if got, err := engine.Render(filename, noExt); err != nil || got != "Show" {
		t.Fatalf("empty extension = %q, %v", got, err)
	}
	long := ctx
	long.Derived.EffectiveTitle = strings.Repeat("长", 90)
	long.Anime.Title, long.Anime.TitleOverride = long.Derived.EffectiveTitle, long.Derived.EffectiveTitle
	var namingErr *Error
	if _, err := engine.Render(filename, long); !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingTargetInvalid {
		t.Fatalf("over-long filename = %v", err)
	}
}

// The literal directory text a template opens with is the owner's own path
// (the library root): the current renderer keeps it as written, so a root
// under a dot directory such as CI's $GITHUB_WORKSPACE/.gotmp keeps its dot.
// Only the segments rendered after it are cleaned.
func TestDirectoryLiteralRootIsKeptVerbatim(t *testing.T) {
	engine := NewEngine()
	ctx := goldenContext("tv", "5")
	for source, want := range map[string]string{
		"/work/.gotmp/lib/${anime.kind}/${anime.title}":   "/work/.gotmp/lib/tv/Show",
		"/work/.gotmp/lib/${anime.kind}/.${anime.title}.": "/work/.gotmp/lib/tv/Show",
		"/work/.gotmp/${anime.kind} ${anime.year}/x":      "/work/.gotmp/tv/x",
		"/work/.gotmp/lib/.Plain.":                        "/work/.gotmp/lib/Plain",
	} {
		compiled, err := engine.Compile(KindDirectory, source)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if got, err := engine.Render(compiled, ctx); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", source, got, err, want)
		}
	}
	// An empty rendered segment after the root is still invalid.
	compiled, err := engine.Compile(KindDirectory, "/work/.gotmp/${anime.year}")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := engine.Render(compiled, ctx); err == nil {
		t.Fatalf("an empty segment after the root must fail, got %q", got)
	}
	// A root that is not the template's own leading literal text (here it
	// comes from an expression) is rendered text and is cleaned as before.
	compiled, err = engine.Compile(KindDirectory, "${'/work/.gotmp/' + anime.kind}")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := engine.Render(compiled, ctx); err != nil || got != "/work/gotmp/tv" {
		t.Fatalf("expression root = %q, %v", got, err)
	}
	// naming-render-2 replay is unchanged: it never removed dots.
	render2, err := engine.ForRenderer("naming-render-2")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err = render2.Compile(KindDirectory, "/work/.gotmp/lib/${anime.title}")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := render2.Render(compiled, ctx); err != nil || got != "/work/.gotmp/lib/Show" {
		t.Fatalf("render-2 = %q, %v", got, err)
	}
}
