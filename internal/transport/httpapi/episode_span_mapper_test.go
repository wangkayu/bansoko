package httpapi

import (
	"testing"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestEpisodeSpanHTTPMappers(t *testing.T) {
	span := 4
	patch := mapRSSSourceOffsetPatch(generated.RSSSourceOffsetPatch{EpisodeSpan: &span})
	if patch.EpisodeSpan != 4 {
		t.Fatalf("patch span = %d, want 4", patch.EpisodeSpan)
	}
	defaultPatch := mapRSSSourceOffsetPatch(generated.RSSSourceOffsetPatch{})
	if defaultPatch.EpisodeSpan != animedomain.DefaultEpisodeSpan {
		t.Fatalf("omitted patch span = %d, want 1", defaultPatch.EpisodeSpan)
	}

	mappedSource := mapRSSSource(animeapplication.RSSSource{EpisodeSpan: 3})
	if mappedSource.EpisodeSpan != 3 {
		t.Fatalf("RSS source span = %d, want 3", mappedSource.EpisodeSpan)
	}
	if got := mapRSSSource(animeapplication.RSSSource{}); got.EpisodeSpan != 1 {
		t.Fatalf("legacy RSS source span = %d, want default 1", got.EpisodeSpan)
	}

	candidateID := generated.ID("candidate-1")
	revision := generated.Version(2)
	read, err := mapRSSListReadInput(generated.RSSListViewInput{
		InputSource: generated.RSSListViewInputInputSourceCandidateRevision,
		CandidateId: &candidateID, Revision: &revision, EpisodeSpan: &span,
	})
	if err != nil {
		t.Fatal(err)
	}
	if read.PreviewEpisodeSpan != 4 {
		t.Fatalf("preview span = %d, want 4", read.PreviewEpisodeSpan)
	}

	if got := mapRSSListOffset(readmodelapplication.RSSListOffset{Span: 2}); got.Span != 2 {
		t.Fatalf("list offset span = %d, want 2", got.Span)
	}
	if got := mapRSSListOffset(readmodelapplication.RSSListOffset{}); got.Span != 1 {
		t.Fatalf("legacy list offset span = %d, want default 1", got.Span)
	}
}
