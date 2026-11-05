package filemaintenance

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/postprocessing/naming"
)

func TestRecognitionRuntimeEpisodeTitlesAreExplicit(t *testing.T) {
	renderer := NewNamingTargetRenderer(nil, nil)
	input := RecognitionRenderInput{
		Rules: naming.NamingRules{
			TV:       naming.NamingVariant{DirectoryTemplate: "/library", VideoFilenameTemplate: "${file.stem}-${bgm.ep_title}.${file.ext}"},
			Language: "js-naming-1",
		},
		FileName: "episode.mkv", FileKind: "video", Episode: "1",
		AnimeDraft: &RecognitionAnimeDraft{Title: "Show", MediaKind: "tv", BGM: true, BGMWorkID: "1"},
	}
	// Without the explicit switch this is the old empty-fact render and does not
	// require the episode-title cache at all.
	if _, err := renderer.RenderRecognition(context.Background(), input); err != nil {
		t.Fatalf("default render: %v", err)
	}
	input.LoadRuntimeFacts = true
	_, err := renderer.RenderRecognition(context.Background(), input)
	var renderErr *RecognitionRenderError
	if !errors.As(err, &renderErr) || renderErr.Code != RecognitionRenderRuntimeFacts {
		t.Fatalf("runtime render error = %v", err)
	}
}

func TestRecognitionRuntimeEpisodeTitlesOnlyQueryReferencedTemplates(t *testing.T) {
	renderer := NewNamingTargetRenderer(nil, nil)
	input := RecognitionRenderInput{
		Rules: naming.NamingRules{
			TV:                     naming.NamingVariant{DirectoryTemplate: "/library", VideoFilenameTemplate: "${file.stem}.${file.ext}"},
			SubtitleSuffixTemplate: "sub", Language: "js-naming-1",
		},
		FileName: "episode.mkv", FileKind: "video", Episode: "1", LoadRuntimeFacts: true,
		AnimeDraft: &RecognitionAnimeDraft{Title: "Show", MediaKind: "tv", BGM: true, BGMWorkID: "1"},
	}
	// No eptitle reference means no lookup, so an unwired querier is harmless.
	if _, err := renderer.RenderRecognition(context.Background(), input); err != nil {
		t.Fatalf("video render: %v", err)
	}
	input.FileName, input.FileKind = "episode.zh.ass", "subtitle"
	if _, err := renderer.RenderRecognitionSuffix(context.Background(), input); err != nil {
		t.Fatalf("subtitle render: %v", err)
	}
}
