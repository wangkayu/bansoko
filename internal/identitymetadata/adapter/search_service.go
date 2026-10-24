package adapter

import (
	"context"

	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
)

// SearchService adapts ConfiguredCandidateSearcher to the application boundary
// by exposing normalized identity-metadata candidate types.
type SearchService struct {
	searcher ConfiguredCandidateSearcher
	titles   WorkTitleRecorder
}

// WorkTitleRecorder records the native titles a search observed.
type WorkTitleRecorder interface {
	RecordWorkTitles(context.Context, []identitymetadatadomain.WorkTitles) error
}

// SetTitleRecorder makes every successful search record its candidates'
// native titles, so an acceptance can freeze the titles of a bound work.
func (s *SearchService) SetTitleRecorder(recorder WorkTitleRecorder) { s.titles = recorder }

// NewSearchService wires the configured connection source and secret resolver
// into a metadata search service. A nil secret resolver is acceptable when
// only Bangumi (public) searches are used.
func NewSearchService(connections ConnectionSource, secrets SecretResolver, clientResolver ...HTTPClientResolver) *SearchService {
	var resolver HTTPClientResolver
	if len(clientResolver) > 0 {
		resolver = clientResolver[0]
	}
	return &SearchService{
		searcher: ConfiguredCandidateSearcher{
			Connections:    connections,
			Secrets:        secrets,
			ClientResolver: resolver,
		},
	}
}

func (s *SearchService) SearchMetadata(ctx context.Context, provider, query string, limit int) ([]identitymetadatadomain.SearchCandidate, error) {
	candidates, err := s.searcher.Search(ctx, provider, query, limit)
	if err == nil && s.titles != nil {
		titles := make([]identitymetadatadomain.WorkTitles, 0, len(candidates))
		for _, candidate := range candidates {
			titles = append(titles, candidate.Native)
		}
		// Recording is best effort: a search result never fails because its
		// titles could not be stored; the work's titles then stay unknown.
		_ = s.titles.RecordWorkTitles(ctx, titles)
	}
	return candidates, err
}
