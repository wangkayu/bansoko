package naming

import (
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
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
	ctx.FileTitleTags = ParseFileTitleTags(ctx.File)
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
	ctx.FileTitleTags = ParseFileTitleTags(ctx.File)
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

// TestFileReltitleParseFromTheOriginalName: a caller that carries only the
// original name (with its extension) prepares the file parse from it, and a
// nameless file prepares no set at all.
func TestFileReltitleParseFromTheOriginalName(t *testing.T) {
	set := ParseFileTitleTags(FileNamingFacts{OriginalName: "[文件组] 进击的巨人 - 07 [1080p].mkv"})
	values := FileReltitleValuesFromTags(set)
	if got, _ := values.Get("Subgroup"); got != "文件组" {
		t.Fatalf("frt.group = %q, want the parse of the original name", got)
	}
	if set := ParseFileTitleTags(FileNamingFacts{}); set != nil {
		t.Fatalf("a nameless file has no frt set, got %#v", set)
	}
}

// TestUnifiedFactsPrefersThePreparedFileTitleTags: an operation preparation
// parses the original download file name once and leaves the set on the
// context. The render reads that set, not a fresh parse of whatever file name
// the context happens to carry (the renamed output, a maintenance replay).
func TestUnifiedFactsPrefersThePreparedFileTitleTags(t *testing.T) {
	ctx := sampleTVContext()
	ctx.File = FileNamingFacts{
		OriginalName: "[库内改] 进击的巨人 - 07 [2160p].mkv",
		Basename:     "[库内改] 进击的巨人 - 07 [2160p]",
		Extension:    "mkv",
		Kind:         "video",
	}
	prepared := ParseFileTitleTags(FileNamingFacts{
		OriginalName: "[文件组] 进击的巨人 - 07 [1080p].mkv",
		Basename:     "[文件组] 进击的巨人 - 07 [1080p]",
		Extension:    "mkv",
		Kind:         "video",
	})
	if prepared == nil || prepared.Schema != acquisitiondomain.TitleTagSchema || prepared.Implementation == "" {
		t.Fatalf("prepared set = %#v, want a frozen parse with its provenance", prepared)
	}
	ctx.FileTitleTags = prepared
	resolved := DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
	facts := UnifiedFacts(resolved)
	if got, _ := facts.FileRt.Get("Subgroup"); got != "文件组" {
		t.Fatalf("frt.group = %q, want the prepared parse", got)
	}
	if got, _ := facts.FileRt.Get("Resolution"); got != "1080p" {
		t.Fatalf("frt.res = %q, want the prepared parse, not the current file name", got)
	}
	if values := FileReltitleValuesFromTags(nil); values != nil {
		t.Fatalf("a nil set has no values, got %#v", values)
	}
}
