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

func (s *Server) SearchMetadataAutomatically(w http.ResponseWriter, r *http.Request) {
	service := s.metadataService(w, r)
	if service == nil {
		return
	}
	var body generated.MetadataAutomaticSearchInput
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(w, r, metadataSearchInvalid(generated.ErrorDetail{Code: "QUERY_REQUIRED", Message: "title must not be empty."}))
		return
	}
	switch body.Provider {
	case generated.MetadataAutomaticSearchInputProviderBgm, generated.MetadataAutomaticSearchInputProviderTmdb:
	default:
		writeError(w, r, metadataSearchInvalid(generated.ErrorDetail{Code: "PROVIDER_UNSUPPORTED", Message: "provider must be one of: bgm, tmdb."}))
		return
	}
	input := identitymetadatadomain.AutomaticSearchInput{
		Provider: string(body.Provider), Title: strings.TrimSpace(body.Title),
		Origin: string(body.Origin), TmdbSeason: body.TmdbSeason,
	}
	if body.OriginEntryRef != nil {
		input.EntryRef = *body.OriginEntryRef
	}
	if body.BgmWorkId != nil {
		input.BgmID = *body.BgmWorkId
	}
	if body.TmdbWorkId != nil {
		input.TmdbWorkID = *body.TmdbWorkId
	}
	if body.TmdbMediaKind != nil {
		input.TmdbKind = string(*body.TmdbMediaKind)
	}
	candidates, err := service.SearchMetadataAutomatically(r.Context(), input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped := mapMetadataCandidates(candidates)
	s.proxyCandidatePosters(r.Context(), mapped)
	writeJSON(w, r.Context(), http.StatusOK, generated.MetadataSearchResult{
		Provider:   generated.MetadataSearchResultProvider(body.Provider),
		Query:      input.Title,
		Candidates: mapped,
	})
}

// ListTMDBCandidateEpisodes lists one season of an arbitrary TMDB TV work
// returned by metadata search. Read-only and independent of any anime binding.
func (s *Server) ListTMDBCandidateEpisodes(w http.ResponseWriter, r *http.Request, workID string, season int) {
	service := s.metadataService(w, r)
	if service == nil {
		return
	}
	if !candidateWorkIDValid(workID) {
		writeError(w, r, metadataSearchInvalid(generated.ErrorDetail{
			Code: "WORK_ID_INVALID", Message: "work_id must be a positive decimal id.",
		}))
		return
	}
	if season < 0 || season > 9999 {
		writeError(w, r, metadataSearchInvalid(generated.ErrorDetail{
			Code: "SEASON_OUT_OF_RANGE", Message: "season must be between 0 and 9999.",
		}))
		return
	}
	episodes, err := service.TMDBCandidateEpisodes(r.Context(), workID, season)
	if err != nil {
		writeError(w, r, metadataUnavailable())
		return
	}
	result := generated.TMDBSeasonEpisodeList{Season: season, Episodes: make([]generated.TMDBEpisode, 0, len(episodes))}
	for _, episode := range episodes {
		mapped := generated.TMDBEpisode{Episode: episode.Number}
		if episode.Title != "" {
			title := episode.Title
			mapped.Title = &title
		}
		if episode.AirDate != "" {
			date := episode.AirDate
			mapped.AirDate = &date
		}
		result.Episodes = append(result.Episodes, mapped)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, result)
}

// ListBGMCandidateEpisodes lists an arbitrary Bangumi subject's main episodes
// and specials. Read-only; no season is involved.
func (s *Server) ListBGMCandidateEpisodes(w http.ResponseWriter, r *http.Request, workID string) {
	service := s.metadataService(w, r)
	if service == nil {
		return
	}
	if !candidateWorkIDValid(workID) {
		writeError(w, r, metadataSearchInvalid(generated.ErrorDetail{
			Code: "WORK_ID_INVALID", Message: "work_id must be a positive decimal id.",
		}))
		return
	}
	episodes, err := service.BGMCandidateEpisodes(r.Context(), workID)
	if err != nil {
		writeError(w, r, metadataUnavailable())
		return
	}
	result := generated.BGMEpisodeList{Episodes: make([]generated.BGMEpisode, 0, len(episodes))}
	for _, episode := range episodes {
		mapped := generated.BGMEpisode{Episode: episode.Number}
		switch episode.Section {
		case "special":
			mapped.Section = generated.BGMEpisodeSectionSpecial
		default:
			mapped.Section = generated.BGMEpisodeSectionMain
		}
		if episode.Title != "" {
			title := episode.Title
			mapped.Title = &title
		}
		if episode.AirDate != "" {
			date := episode.AirDate
			mapped.AirDate = &date
		}
		result.Episodes = append(result.Episodes, mapped)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, result)
}

// candidateWorkIDValid checks the contract's ^[1-9][0-9]{0,11}$ work-id
// pattern. The generated binder binds path values into Go types but does not
// enforce string patterns.
func candidateWorkIDValid(workID string) bool {
	if len(workID) == 0 || len(workID) > 12 {
		return false
	}
	for index := 0; index < len(workID); index++ {
		digit := workID[index]
		if digit < '0' || digit > '9' || (index == 0 && digit == '0') {
			return false
		}
	}
	return true
}

// metadataUnavailable is the typed 503 both candidate episode reads declare.
// Every lookup failure is transient by contract; a definite missing work is an
// empty list, not an error.
func metadataUnavailable() *APIError {
	return unavailable("METADATA_PROVIDER_UNAVAILABLE", "the metadata provider could not be reached")
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
