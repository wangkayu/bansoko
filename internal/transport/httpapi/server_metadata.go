package httpapi

import (
	"net/http"
	"strings"

	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func (s *Server) metadataService(w http.ResponseWriter, r *http.Request) MetadataSearchService {
	return s.deps.MetadataSearch
}

// metadataSearchInvalid builds the typed 422 used when the search input fails
// contract validation. The endpoint declares only the Unprocessable error
// response in api/openapi.yaml, so client mistakes must surface there
// instead of falling through to a generic 500 INTERNAL.
func metadataSearchInvalid(detail generated.ErrorDetail) *APIError {
	return unprocessable("METADATA_SEARCH_INVALID", "The metadata search input failed typed validation.", []generated.ErrorDetail{detail})
}

// SearchMetadata proxies a title query to the configured metadata provider
// (bgm or tmdb) and returns normalized candidates.
func (s *Server) SearchMetadata(w http.ResponseWriter, r *http.Request, params generated.SearchMetadataParams) {
	service := s.metadataService(w, r)
	if service == nil {
		return
	}
	// The generated wrapper binds query parameters but does not enforce the
	// OpenAPI enum/range constraints, so validate them explicitly here.
	switch params.Provider {
	case generated.SearchMetadataParamsProviderBgm, generated.SearchMetadataParamsProviderTmdb:
	default:
		writeError(w, r, metadataSearchInvalid(generated.ErrorDetail{
			Code:    "PROVIDER_UNSUPPORTED",
			Message: "provider must be one of: bgm, tmdb.",
		}))
		return
	}
	limit := 20
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 50 {
		writeError(w, r, metadataSearchInvalid(generated.ErrorDetail{
			Code:    "LIMIT_OUT_OF_RANGE",
			Message: "limit must be between 1 and 50.",
		}))
		return
	}
	result, err := service.SearchMetadata(r.Context(), string(params.Provider), params.Query, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	candidates := mapMetadataCandidates(result)
	s.proxyCandidatePosters(r.Context(), candidates)
	writeJSON(w, r.Context(), http.StatusOK, generated.MetadataSearchResult{
		Provider:   generated.MetadataSearchResultProvider(params.Provider),
		Query:      params.Query,
		Candidates: candidates,
	})
}

func mapMetadataCandidates(candidates []identitymetadatadomain.SearchCandidate) []generated.MetadataSearchCandidate {
	mapped := make([]generated.MetadataSearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		mediaKind := generated.MetadataSearchCandidateMediaKindTv
		if strings.EqualFold(candidate.MediaKind, "movie") {
			mediaKind = generated.MetadataSearchCandidateMediaKindMovie
		}
		value := generated.MetadataSearchCandidate{
			Provider:       generated.MetadataSearchCandidateProvider(candidate.Provider),
			ProviderWorkId: candidate.ProviderWorkID,
			Title:          candidate.Title,
			MediaKind:      mediaKind,
			DetailsStatus:  generated.MetadataDetailsStatus(candidate.DetailsStatus),
			Score:          float32(candidate.Score),
		}
		if strings.TrimSpace(candidate.OriginalTitle) != "" {
			originalTitle := candidate.OriginalTitle
			value.OriginalTitle = &originalTitle
		}
		if strings.TrimSpace(candidate.Overview) != "" {
			overview := candidate.Overview
			value.Overview = &overview
		}
		if strings.TrimSpace(candidate.PosterURL) != "" {
			posterURL := candidate.PosterURL
			value.PosterUrl = &posterURL
		}
		if candidate.Seasons != nil && mediaKind == generated.MetadataSearchCandidateMediaKindTv &&
			(candidate.DetailsStatus == identitymetadatadomain.DetailsStatusAvailable || candidate.DetailsStatus == identitymetadatadomain.DetailsStatusEmpty) {
			seasons := make([]generated.MetadataSearchSeason, 0, len(candidate.Seasons))
			for _, season := range candidate.Seasons {
				seasons = append(seasons, generated.MetadataSearchSeason{
					SeasonNumber: season.SeasonNumber,
					Name:         season.Name,
					EpisodeCount: season.EpisodeCount,
					AirDate:      season.AirDate,
					IsSpecial:    season.IsSpecial,
				})
			}
			value.Seasons = &seasons
			if candidate.DetailsStatus == identitymetadatadomain.DetailsStatusAvailable && candidate.SuggestedSeason != nil {
				for _, season := range candidate.Seasons {
					if season.SeasonNumber == *candidate.SuggestedSeason {
						suggested := *candidate.SuggestedSeason
						value.SuggestedSeason = &suggested
						break
					}
				}
			}
		}
		if candidate.Year > 0 {
			year := candidate.Year
			value.Year = &year
		}
		if candidate.ReleaseFormat != "" {
			format := generated.ReleaseFormat(candidate.ReleaseFormat)
			value.ReleaseFormat = &format
		}
		mapped = append(mapped, value)
	}
	return mapped
}
