package parser

import (
	"context"
	"strings"
	"testing"
)

func TestRecognizeAppliesEpisodeSpanWithoutMultiplyingExplicitRanges(t *testing.T) {
	registry := NewDefaultRegistry()
	result, err := Recognize(context.Background(), registry, []RecognitionItem{
		{ClientRef: "single", Name: "Show - 02.mkv"},
		{ClientRef: "range", Name: "Show - 01-02.mkv"},
	}, RecognitionContext{
		WorkKind: "tv", EpisodeOffset: 1, EpisodeSpan: 2, Parser: ParserSelection{ID: ParserRegex},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("recognition result count = %d, want 2", len(result))
	}
	single := result[0].Identity
	if single.Episode == nil || *single.Episode != "4" || single.EpisodeEnd == nil || *single.EpisodeEnd != "5" ||
		strings.Join(single.Keys(), ",") != "tv:e4,tv:e5" {
		t.Fatalf("single-number span identity = %+v, want episodes 4-5", single)
	}
	explicit := result[1].Identity
	if explicit.Episode == nil || *explicit.Episode != "2" || explicit.EpisodeEnd == nil || *explicit.EpisodeEnd != "3" ||
		strings.Join(explicit.Keys(), ",") != "tv:e2,tv:e3" {
		t.Fatalf("explicit range identity = %+v, want episodes 2-3", explicit)
	}
}

func TestRecognizeRejectsEpisodeSpanOutsideRange(t *testing.T) {
	for _, span := range []int{-1, 13} {
		_, err := Recognize(context.Background(), NewDefaultRegistry(), []RecognitionItem{{ClientRef: "file", Name: "Show - 01.mkv"}}, RecognitionContext{
			WorkKind: "tv", EpisodeSpan: span, Parser: ParserSelection{ID: ParserRegex},
		})
		if err == nil || !strings.Contains(err.Error(), "between 1 and 12") {
			t.Fatalf("span %d err = %v, want range error", span, err)
		}
	}
}
