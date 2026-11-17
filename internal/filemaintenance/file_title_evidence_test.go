package filemaintenance

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/postprocessing/naming"
)

func TestMatchFileTitleEvidenceDoesNotReparsePublishedNames(t *testing.T) {
	file := naming.FileNamingFacts{OriginalName: "[Renamed] Example - 01 [2160p].mkv", Basename: "[Renamed] Example - 01 [2160p]"}
	for _, tc := range []struct {
		name    string
		lineage matchLineage
		want    string
		wantErr bool
	}{
		{"detached", matchLineage{}, "2160p", false},
		{"unpublished original", matchLineage{found: true, originalFileName: "original/Example - 01 [720p].mkv"}, "720p", false},
		{"missing original", matchLineage{found: true}, "", false},
		{"frozen", matchLineage{found: true, hasRenderSnapshot: true, renderSnapshot: []byte(`{"file_title_tags":{"schema":"reltitle-attrs-v1","implementation":"frozen","tags":["resolution:1080p"]}}`)}, "1080p", false},
		{"legacy absent", matchLineage{found: true, hasRenderSnapshot: true, renderSnapshot: []byte(`{}`)}, "", false},
		{"explicit absent", matchLineage{found: true, hasRenderSnapshot: true, renderSnapshot: []byte(`{"file_title_tags":null}`)}, "", false},
		{"prepared empty", matchLineage{found: true, hasRenderSnapshot: true, renderSnapshot: []byte(`{"file_title_tags":{"tags":[]}}`)}, "", false},
		{"invalid", matchLineage{found: true, hasRenderSnapshot: true, renderSnapshot: []byte(`{"file_title_tags":42}`)}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, err := tc.lineage.fileTitleTags(file)
			if tc.wantErr {
				if !errors.Is(err, naming.ErrRenderFactsUnavailable) {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := naming.FileReltitleValuesFromTags(set).Get("Resolution"); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRecognitionPreparesOnlyFileTitleEvidence(t *testing.T) {
	r := &NamingTargetRenderer{}
	ctx, err := r.recognitionContext(context.Background(), RecognitionRenderInput{FileKind: "video"}, "[ExampleGroup] Example - 01 [1080p].mkv")
	if err != nil {
		t.Fatal(err)
	}
	facts := naming.UnifiedFacts(naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: ctx}).Context)
	if got, _ := facts.FileRt.Get("Resolution"); got != "1080p" {
		t.Fatalf("frt resolution = %q", got)
	}
	if got, _ := facts.Rt.Get("Resolution"); got != "" {
		t.Fatalf("file evidence leaked into rt: %q", got)
	}
	if ctx.File.Media.Resolution != "" {
		t.Fatal("file title leaked into measured media")
	}
}
