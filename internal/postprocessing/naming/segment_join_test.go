package naming

import (
	"reflect"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func renderSuffixResult(source string, ctx NamingContext) (string, error) {
	engine := NewEngine()
	compiled, err := engine.Compile(KindSubtitleSuffix, source)
	if err != nil {
		return "", err
	}
	return engine.Render(compiled, ctx)
}

func TestChainIsNoLongerATemplateFunction(t *testing.T) {
	if _, err := renderSuffixResult("${chain(' ', media.resolution)}", NamingContext{}); err == nil || !strings.Contains(err.Error(), "unknown template identifier chain") {
		t.Fatalf("retired chain error = %v", err)
	}
}


func TestSegmentPreservesExplicitValuesAndDecorations(t *testing.T) {
	ctx := NamingContext{FileTitleTags: &acquisitiondomain.TitleTagSet{Schema: acquisitiondomain.TitleTagSchema, ValueTags: []string{"group:@"}}}
	if got := renderSuffix(t, "Show${segment(frt.group, ' [', ']')}", ctx); got != "Show [@]" {
		t.Fatalf("symbol segment = %q", got)
	}
	if got := renderSuffix(t, "Show${segment(' ', ' [', ']')}", NamingContext{}); got != "Show [ ]" {
		t.Fatalf("whitespace segment = %q", got)
	}
	ctx.FileTitleTags.ValueTags = nil
	if got := renderSuffix(t, "Show${segment(frt.group, ' [', ']')}", ctx); got != "Show" {
		t.Fatalf("empty segment = %q", got)
	}
	for _, tc := range []struct{ source, want string }{
		{"${segment(null, ' [', ']')}", ""},
		{"${segment(undefined, ' [', ']')}", ""},
		{"${segment('', ' [', ']')}", ""},
		{"${segment(0, ' [', ']')}", " [0]"},
		{"${segment(false, ' [', ']')}", " [false]"},
		{"${segment('A+B')}", "A+B"},
		{"${segment('&#47;&sol;')}", "&#47;&sol;"},
	} {
		if got := renderSuffix(t, tc.source, NamingContext{}); got != tc.want {
			t.Errorf("render %q = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestJoinNonEmptySkipsOnlyMissingAndEmptyItems(t *testing.T) {
	source := "${joinNonEmpty(['', null, '@', undefined, ' ', false, 0, '', '@'], ' ／ ')}"
	if got := renderSuffix(t, source, NamingContext{}); got != "@ ／   ／ false ／ 0 ／ @" {
		t.Fatalf("joinNonEmpty scalar matrix = %q", got)
	}
	ctx := NamingContext{}
	ctx.File.Basename = "a  b"
	if got := renderSuffix(t, "${joinNonEmpty(file.stem.split(' '), '-')}", ctx); got != "a-b" {
		t.Fatalf("native list with an empty middle item = %q", got)
	}
	for _, source := range []string{
		"${joinNonEmpty([], ',')}",
		"${joinNonEmpty([null, undefined, ''], ',')}",
		"${joinNonEmpty(null, ',')}",
		"${joinNonEmpty(undefined, ',')}",
	} {
		if got := renderSuffix(t, source, NamingContext{}); got != "" {
			t.Errorf("render %q = %q, want empty", source, got)
		}
	}
}

func TestSegmentAndJoinNonEmptyRejectNonScalarInputs(t *testing.T) {
	for _, source := range []string{
		"${segment([], ' [', ']')}",
		"${segment({}, ' [', ']')}",
		"${segment(/x/, ' [', ']')}",
		"${joinNonEmpty('abc', ',')}",
		"${joinNonEmpty({}, ',')}",
		"${joinNonEmpty([[]], ',')}",
		"${joinNonEmpty([{}], ',')}",
	} {
		if _, err := renderSuffixResult(source, NamingContext{}); err == nil {
			t.Errorf("render %q unexpectedly accepted a non-scalar input", source)
		}
	}
}

func TestSegmentKeepsFilenamePathSafety(t *testing.T) {
	for _, source := range []string{"${segment('1/2')}", "${segment('&#47;/&sol;')}"} {
		if _, err := renderSuffixResult(source, NamingContext{}); err == nil || !strings.Contains(err.Error(), "directory separator") {
			t.Errorf("render %q path safety error = %v", source, err)
		}
	}
}

func TestJoinNonEmptyBounds(t *testing.T) {
	tooMany := "${joinNonEmpty([" + strings.TrimSuffix(strings.Repeat("null,", MaxCollectionLen+1), ",") + "], ',')}"
	if _, err := renderSuffixResult(tooMany, NamingContext{}); err == nil || !strings.Contains(err.Error(), "collection exceeds") {
		t.Fatalf("overlong collection error = %v", err)
	}
	part := strings.Repeat("x", MaxOutputBytes/3+1)
	ctx := NamingContext{}
	ctx.File.Basename = part
	tooLarge := "${joinNonEmpty([file.stem,file.stem,file.stem], '-')}"
	engine := NewEngine()
	compiled, err := engine.Compile(KindVideoFilename, tooLarge)
	if err != nil {
		t.Fatalf("compile overlarge join: %v", err)
	}
	if _, err := engine.Render(compiled, ctx); err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("overlarge join error = %v", err)
	}
}

func TestSegmentAndJoinNonEmptyReferenceAnalysis(t *testing.T) {
	engine := NewEngine()
	for _, tc := range []struct {
		source     string
		required   []string
		referenced []string
	}{
		{
			source:     "${segment(media.resolution, anime.title, file.ext)}",
			required:   []string{"anime.title", "file.ext"},
			referenced: []string{"anime.title", "file.ext", "media.resolution"},
		},
		{
			source:     "${joinNonEmpty([media.resolution, frt.group], anime.title)}",
			required:   []string{"anime.title"},
			referenced: []string{"anime.title", "frt.group", "media.resolution"},
		},
	} {
		compiled, err := engine.Compile(KindSubtitleSuffix, tc.source)
		if err != nil {
			t.Fatalf("compile %q: %v", tc.source, err)
		}
		if !reflect.DeepEqual(compiled.RequiredFacts, tc.required) {
			t.Errorf("required facts for %q = %v, want %v", tc.source, compiled.RequiredFacts, tc.required)
		}
		if !reflect.DeepEqual(compiled.ReferencedFacts, tc.referenced) {
			t.Errorf("referenced facts for %q = %v, want %v", tc.source, compiled.ReferencedFacts, tc.referenced)
		}
	}
	if _, err := engine.Compile(KindSubtitleSuffix, "${segment(media.resolution, unknown.title)}"); err == nil {
		t.Fatal("unknown prefix reference was swallowed by segment")
	}
	if _, err := engine.Compile(KindSubtitleSuffix, "${joinNonEmpty([media.resolution], unknown.title)}"); err == nil {
		t.Fatal("unknown separator reference was swallowed by joinNonEmpty")
	}
}
