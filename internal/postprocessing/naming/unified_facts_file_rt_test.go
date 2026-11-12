package naming

import (
	"testing"

	"bansoko/internal/templatefacts"
)

// TestFileReltitleParseStaysOutOfTheReleaseNamespace pins the two sources
// apart: rt.* is the release title's reltitle parse and frt.* the parse of
// the file's own name. A render names the source it wants; neither value ever
// stands in for the other, so a template reading rt.group cannot silently
// pick up the file's group and vice versa.
func TestFileReltitleParseStaysOutOfTheReleaseNamespace(t *testing.T) {
	ctx := sampleTVContext()
	ctx.Release.OriginalTitle = "[发布组] 进击的巨人 - 07 [1080p]"
	ctx.Rt = templatefacts.Values{"Subgroup": "发布组"}
	ctx.File = FileNamingFacts{
		OriginalName: "[文件组] 进击的巨人 - 07 [1080p].mkv",
		Basename:     "[文件组] 进击的巨人 - 07 [1080p]",
		Extension:    "mkv",
		Kind:         "video",
	}
	resolved := DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
	facts := UnifiedFacts(resolved)
	if got, _ := facts.Rt.Get("Subgroup"); got != "发布组" {
		t.Fatalf("rt.group = %q, want the release parse", got)
	}
	if got, _ := facts.FileRt.Get("Subgroup"); got != "文件组" {
		t.Fatalf("frt.group = %q, want the file parse", got)
	}

	got, err := renderNamespaceTemplate(t, "${rt.group}|${frt.group}.${file.ext}", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := "发布组|文件组.mkv"; got != want {
		t.Fatalf("rendered %q, want %q", got, want)
	}
}

// TestFileReltitleParseWithoutARelease: a file whose release title has no
// reltitle parse (the recognition render) still names with its own parse, and
// rt.* stays undefined rather than borrowing the file's values.
func TestFileReltitleParseWithoutARelease(t *testing.T) {
	ctx := sampleTVContext()
	ctx.Rt = nil
	ctx.Release.OriginalTitle = ""
	ctx.File = FileNamingFacts{
		OriginalName: "[文件组] 进击的巨人 - 07 [1080p].mkv",
		Basename:     "[文件组] 进击的巨人 - 07 [1080p]",
		Extension:    "mkv",
		Kind:         "video",
	}
	resolved := DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
	facts := UnifiedFacts(resolved)
	if got, _ := facts.Rt.Get("Subgroup"); got != "" {
		t.Fatalf("rt.group = %q, want none", got)
	}
	if got, _ := facts.FileRt.Get("Subgroup"); got != "文件组" {
		t.Fatalf("frt.group = %q, want the file parse", got)
	}

	got, err := renderNamespaceTemplate(t, "${frt.group}.${file.ext}", ctx)
	if err != nil || got != "文件组.mkv" {
		t.Fatalf("file render = %q, %v", got, err)
	}
	// rt.* has no value and no fallback: outside a `??` guard the render is the
	// usual missing-fact refusal, never the file parse.
	if got, err := renderNamespaceTemplate(t, "${rt.group}.${file.ext}", ctx); err == nil {
		t.Fatalf("rt.group rendered %q without a release parse", got)
	}
}

// TestFileReltitleParseFallsBackToTheOriginalName: a caller that carries only
// the original name (with its extension) still gets the file parse.
func TestFileReltitleParseFallsBackToTheOriginalName(t *testing.T) {
	values := FileReltitleValues(FileNamingFacts{OriginalName: "[文件组] 进击的巨人 - 07 [1080p].mkv"})
	if got, _ := values.Get("Subgroup"); got != "文件组" {
		t.Fatalf("frt.group = %q, want the parse of the original name", got)
	}
	if values := FileReltitleValues(FileNamingFacts{}); values != nil {
		t.Fatalf("a nameless file has no frt values, got %#v", values)
	}
}
