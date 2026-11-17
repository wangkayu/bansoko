package naming

import (
	"errors"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func TestFrozenFileTitleTagsPreservesAbsentAndEmptyEvidence(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `{"file_title_tags":null}`} {
		set, err := FrozenFileTitleTags([]byte(raw))
		if err != nil || set != nil {
			t.Fatalf("%s: got %#v, %v", raw, set, err)
		}
	}
	set, err := FrozenFileTitleTags([]byte(`{"file_title_tags":{"schema":"reltitle-attrs-v1","implementation":"frozen-parser","tags":[],"diagnostic":"no matching tags"}}`))
	if err != nil || set == nil || set.Implementation != "frozen-parser" || set.Diagnostic != "no matching tags" || len(set.Tags) != 0 {
		t.Fatalf("empty prepared result = %#v, %v", set, err)
	}
}

func TestFrozenFileTitleTagsRejectsCorruptEvidence(t *testing.T) {
	for _, raw := range []string{"{", "[]", `{"file_title_tags":"broken"}`, `{"file_title_tags":[]}`, `{"file_title_tags":{"tags":[7]}}`} {
		_, err := FrozenFileTitleTags([]byte(raw))
		if !errors.Is(err, ErrRenderFactsUnavailable) {
			t.Fatalf("%s: want unavailable, got %v", raw, err)
		}
	}
}

func TestUnifiedFactsNeverParsesMissingOrEmptyFileEvidence(t *testing.T) {
	for _, set := range []*acquisitiondomain.TitleTagSet{nil, {}, {Diagnostic: "parse failed"}} {
		ctx := sampleTVContext()
		ctx.File = FileNamingFacts{OriginalName: "[OtherGroup] Example - 01 [2160p].mkv", Basename: "[OtherGroup] Example - 01 [2160p]"}
		ctx.FileTitleTags = set
		facts := UnifiedFacts(DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context)
		if value, _ := facts.FileRt.Get("Resolution"); value != "" {
			t.Fatalf("file name was reparsed for %#v: %q", set, value)
		}
	}
}
