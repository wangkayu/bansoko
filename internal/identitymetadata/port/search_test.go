package port

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/identitymetadata/domain"
)

// CandidateSearcher: the external-metadata search boundary. Contract test pins
// the signature; behavioral tests verify the typed error path of an adapter
// written against the port.

type fakeSearcher struct {
	result []domain.SearchCandidate
	err    error
}

func (f fakeSearcher) Search(_ context.Context, _, _ string, _ int) ([]domain.SearchCandidate, error) {
	return f.result, f.err
}

func TestCandidateSearcherContract(t *testing.T) {
	var searcher CandidateSearcher = fakeSearcher{result: []domain.SearchCandidate{}}
	if _, err := searcher.Search(context.Background(), "bgm", "进击的巨人", 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCandidateSearcherErrorPropagation(t *testing.T) {
	want := errors.New("provider unavailable")
	searcher := fakeSearcher{err: want}
	if _, err := searcher.Search(context.Background(), "tmdb", "x", 3); !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}
