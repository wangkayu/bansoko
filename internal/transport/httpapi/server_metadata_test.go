package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	"bansoko/internal/transport/httpapi/generated"
)

type metadataSearchSpy struct {
	calls     int
	provider  string
	query     string
	limit     int
	autoInput identitymetadatadomain.AutomaticSearchInput
	auto      []identitymetadatadomain.SearchCandidate
}

func (s *metadataSearchSpy) SearchMetadata(ctx context.Context, provider, query string, limit int) ([]identitymetadatadomain.SearchCandidate, error) {
	s.calls++
	s.provider, s.query, s.limit = provider, query, limit
	return []identitymetadatadomain.SearchCandidate{}, nil
}

func (s *metadataSearchSpy) SearchMetadataAutomatically(_ context.Context, input identitymetadatadomain.AutomaticSearchInput) ([]identitymetadatadomain.SearchCandidate, error) {
	s.calls++
	s.autoInput = input
	return s.auto, nil
}

func (s *metadataSearchSpy) TMDBCandidateEpisodes(context.Context, string, int) ([]identitymetadatadomain.CandidateEpisode, error) {
	return []identitymetadatadomain.CandidateEpisode{}, nil
}

func (s *metadataSearchSpy) BGMCandidateEpisodes(context.Context, string) ([]identitymetadatadomain.CandidateEpisode, error) {
	return []identitymetadatadomain.CandidateEpisode{}, nil
}

func TestSearchMetadataAutomaticallyPassesPipelineContextWithoutApplying(t *testing.T) {
	season := 2
	spy := &metadataSearchSpy{auto: []identitymetadatadomain.SearchCandidate{{
		Provider: "tmdb", ProviderWorkID: "42", Title: "Show", MediaKind: "tv",
		DetailsStatus:   identitymetadatadomain.DetailsStatusAvailable,
		Seasons:         []identitymetadatadomain.SearchSeason{{SeasonNumber: 2, Name: "Season 2"}},
		SuggestedSeason: &season, Score: 1,
	}}}
	server := &Server{deps: Dependencies{MetadataSearch: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/metadata/search/automatic", strings.NewReader(`{
		"provider":"tmdb","title":"Show S2","origin":"mikan","origin_entry_ref":"3967",
		"bgm_work_id":"123","tmdb_work_id":"42","tmdb_media_kind":"tv","tmdb_season":1
	}`))

	server.SearchMetadataAutomatically(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if spy.autoInput.Origin != "mikan" || spy.autoInput.EntryRef != "3967" || spy.autoInput.BgmID != "123" || spy.autoInput.TmdbWorkID != "42" || spy.autoInput.TmdbSeason == nil || *spy.autoInput.TmdbSeason != 1 {
		t.Fatalf("automatic input=%+v", spy.autoInput)
	}
	var response generated.MetadataSearchResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Candidates) != 1 || response.Candidates[0].SuggestedSeason == nil || *response.Candidates[0].SuggestedSeason != 2 {
		t.Fatalf("response=%+v", response)
	}
}

// violations (unknown provider enum, out-of-range limit) surface as the
// structured 422 Unprocessable response declared by api/openapi.yaml
// instead of a generic 500 INTERNAL, and never leak stack traces.
func TestSearchMetadataRejectsInvalidInputWithTyped422(t *testing.T) {
	cases := []struct {
		name       string
		provider   generated.SearchMetadataParamsProvider
		limit      *int
		detailCode string
	}{
		{name: "unknown provider enum", provider: "bad", limit: intPtr(1), detailCode: "PROVIDER_UNSUPPORTED"},
		{name: "limit zero", provider: "bgm", limit: intPtr(0), detailCode: "LIMIT_OUT_OF_RANGE"},
		{name: "limit above maximum", provider: "bgm", limit: intPtr(51), detailCode: "LIMIT_OUT_OF_RANGE"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spy := &metadataSearchSpy{}
			server := &Server{deps: Dependencies{MetadataSearch: spy}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/metadata/search?query=test", nil)

			server.SearchMetadata(recorder, request, generated.SearchMetadataParams{
				Provider: testCase.provider,
				Query:    "test",
				Limit:    testCase.limit,
			})

			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details []struct {
					Code string `json:"code"`
				} `json:"details"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("body must be structured JSON error: %v (%s)", err, recorder.Body.String())
			}
			if response.Code != "METADATA_SEARCH_INVALID" {
				t.Fatalf("code=%q", response.Code)
			}
			if len(response.Details) != 1 || response.Details[0].Code != testCase.detailCode {
				t.Fatalf("details=%+v want %s", response.Details, testCase.detailCode)
			}
			if strings.Contains(recorder.Body.String(), "goroutine") || strings.Contains(recorder.Body.String(), ".go:") {
				t.Fatalf("response must not leak stack traces: %s", recorder.Body.String())
			}
			if spy.calls != 0 {
				t.Fatalf("service must not be called for invalid input, calls=%d", spy.calls)
			}
		})
	}
}

// TestSearchMetadataAcceptsContractRanges verifies the valid combinations do
// not regress: provider bgm/tmdb with explicit limit 1 and 50, plus the unset
// default of 20.
func TestSearchMetadataAcceptsContractRanges(t *testing.T) {
	for _, provider := range []generated.SearchMetadataParamsProvider{
		generated.SearchMetadataParamsProviderBgm,
		generated.SearchMetadataParamsProviderTmdb,
	} {
		for _, limit := range []*int{intPtr(1), intPtr(50), nil} {
			spy := &metadataSearchSpy{}
			server := &Server{deps: Dependencies{MetadataSearch: spy}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/metadata/search?query=test", nil)

			server.SearchMetadata(recorder, request, generated.SearchMetadataParams{
				Provider: provider,
				Query:    "test",
				Limit:    limit,
			})

			if recorder.Code != http.StatusOK {
				t.Fatalf("provider=%s limit=%v status=%d body=%s", provider, limit, recorder.Code, recorder.Body.String())
			}
			if spy.calls != 1 || spy.limit != defaultInt(limit, 20) {
				t.Fatalf("service called with provider=%q query=%q limit=%d calls=%d", spy.provider, spy.query, spy.limit, spy.calls)
			}
		}
	}
}

func TestMapMetadataCandidatesPreservesHTTPShape(t *testing.T) {
	season := 2
	candidates := []identitymetadatadomain.SearchCandidate{{
		Provider:        "tmdb",
		ProviderWorkID:  "123",
		Title:           "Example",
		OriginalTitle:   "Original",
		Overview:        "Overview",
		PosterURL:       "https://image.example/poster.jpg",
		MediaKind:       "tv",
		DetailsStatus:   identitymetadatadomain.DetailsStatusAvailable,
		Seasons:         []identitymetadatadomain.SearchSeason{{SeasonNumber: 2, Name: "Season Two", EpisodeCount: 12, AirDate: "2025-01-01", IsSpecial: false}},
		SuggestedSeason: &season,
		Year:            2026,
		Score:           8.5,
	}}
	mapped := mapMetadataCandidates(candidates)
	if len(mapped) != 1 {
		t.Fatalf("mapped candidates=%d, want 1", len(mapped))
	}
	value := mapped[0]
	if value.Provider != generated.MetadataSearchCandidateProviderTmdb ||
		value.ProviderWorkId != "123" || value.Title != "Example" ||
		value.MediaKind != generated.MetadataSearchCandidateMediaKindTv ||
		value.DetailsStatus != generated.MetadataDetailsStatusAvailable ||
		value.Score != float32(8.5) {
		t.Fatalf("unexpected mapped candidate: %+v", value)
	}
	if value.OriginalTitle == nil || *value.OriginalTitle != "Original" ||
		value.Overview == nil || *value.Overview != "Overview" ||
		value.PosterUrl == nil || *value.PosterUrl != "https://image.example/poster.jpg" ||
		value.SuggestedSeason == nil || *value.SuggestedSeason != 2 ||
		value.Seasons == nil || len(*value.Seasons) != 1 || (*value.Seasons)[0].IsSpecial ||
		value.Year == nil || *value.Year != 2026 {
		t.Fatalf("optional fields not preserved: %+v", value)
	}
}

func TestMapMetadataCandidatesOnlyProjectsRealTVSeasonSuggestion(t *testing.T) {
	seasonTwo := 2
	seasonNine := 9
	candidates := []identitymetadatadomain.SearchCandidate{
		{
			Provider: "tmdb", ProviderWorkID: "valid", Title: "Valid", MediaKind: "tv",
			DetailsStatus: identitymetadatadomain.DetailsStatusAvailable,
			Seasons:       []identitymetadatadomain.SearchSeason{{SeasonNumber: 2}}, SuggestedSeason: &seasonTwo,
		},
		{
			Provider: "tmdb", ProviderWorkID: "missing", Title: "Missing", MediaKind: "tv",
			DetailsStatus: identitymetadatadomain.DetailsStatusAvailable,
			Seasons:       []identitymetadatadomain.SearchSeason{{SeasonNumber: 2}}, SuggestedSeason: &seasonNine,
		},
		{
			Provider: "tmdb", ProviderWorkID: "error", Title: "Error", MediaKind: "tv",
			DetailsStatus: identitymetadatadomain.DetailsStatusError,
			Seasons:       []identitymetadatadomain.SearchSeason{{SeasonNumber: 2}}, SuggestedSeason: &seasonTwo,
		},
		{
			Provider: "tmdb", ProviderWorkID: "movie", Title: "Movie", MediaKind: "movie",
			DetailsStatus: identitymetadatadomain.DetailsStatusNotApplicable,
			Seasons:       []identitymetadatadomain.SearchSeason{{SeasonNumber: 2}}, SuggestedSeason: &seasonTwo,
		},
		{
			Provider: "tmdb", ProviderWorkID: "empty", Title: "Empty", MediaKind: "tv",
			DetailsStatus: identitymetadatadomain.DetailsStatusEmpty,
			Seasons:       []identitymetadatadomain.SearchSeason{}, SuggestedSeason: &seasonTwo,
		},
		{
			Provider: "tmdb", ProviderWorkID: "special", Title: "Special", MediaKind: "tv",
			DetailsStatus: identitymetadatadomain.DetailsStatusAvailable,
			Seasons:       []identitymetadatadomain.SearchSeason{{SeasonNumber: 0, IsSpecial: true}},
		},
	}

	mapped := mapMetadataCandidates(candidates)
	if mapped[0].SuggestedSeason == nil || *mapped[0].SuggestedSeason != 2 {
		t.Fatalf("real TV suggestion must be preserved: %+v", mapped[0])
	}
	for _, index := range []int{1, 2, 3, 4, 5} {
		if mapped[index].SuggestedSeason != nil {
			t.Fatalf("candidate %d must not project an unproven suggestion: %+v", index, mapped[index])
		}
	}
	if mapped[2].Seasons != nil {
		t.Fatalf("details error must omit seasons: %+v", mapped[2])
	}
	if mapped[3].Seasons != nil {
		t.Fatalf("movie must omit TV seasons: %+v", mapped[3])
	}
	if mapped[4].Seasons == nil || len(*mapped[4].Seasons) != 0 {
		t.Fatalf("successful empty details must project an explicit empty list: %+v", mapped[4])
	}
	if mapped[5].Seasons == nil || len(*mapped[5].Seasons) != 1 || !(*mapped[5].Seasons)[0].IsSpecial {
		t.Fatalf("special-only details must preserve the real season without inventing a suggestion: %+v", mapped[5])
	}
}

func intPtr(value int) *int { return &value }

func defaultInt(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

type candidateEpisodeSearch struct {
	tmdb, bgm []identitymetadatadomain.CandidateEpisode
	err       error
	workID    string
	season    int
}

func (s *candidateEpisodeSearch) SearchMetadata(context.Context, string, string, int) ([]identitymetadatadomain.SearchCandidate, error) {
	return nil, nil
}

func (s *candidateEpisodeSearch) SearchMetadataAutomatically(context.Context, identitymetadatadomain.AutomaticSearchInput) ([]identitymetadatadomain.SearchCandidate, error) {
	return nil, nil
}

func (s *candidateEpisodeSearch) TMDBCandidateEpisodes(_ context.Context, workID string, season int) ([]identitymetadatadomain.CandidateEpisode, error) {
	s.workID, s.season = workID, season
	return s.tmdb, s.err
}

func (s *candidateEpisodeSearch) BGMCandidateEpisodes(_ context.Context, workID string) ([]identitymetadatadomain.CandidateEpisode, error) {
	s.workID = workID
	return s.bgm, s.err
}

func TestTMDBCandidateEpisodesMapTheSeasonAndRejectBadWorkIDs(t *testing.T) {
	service := &candidateEpisodeSearch{tmdb: []identitymetadatadomain.CandidateEpisode{{Number: 1, Title: "第一话", AirDate: "2024-01-01"}}}
	server := &Server{deps: Dependencies{MetadataSearch: service}}
	recorder := httptest.NewRecorder()
	server.ListTMDBCandidateEpisodes(recorder, httptest.NewRequest(http.MethodGet, "/metadata/tmdb/works/265010/seasons/2/episodes", nil), "265010", 2)
	if recorder.Code != http.StatusOK || service.workID != "265010" || service.season != 2 {
		t.Fatalf("status %d, query %q/%d", recorder.Code, service.workID, service.season)
	}
	var body generated.TMDBSeasonEpisodeList
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Season != 2 || len(body.Episodes) != 1 || body.Episodes[0].Title == nil || *body.Episodes[0].Title != "第一话" {
		t.Fatalf("body = %+v", body)
	}

	recorder = httptest.NewRecorder()
	server.ListTMDBCandidateEpisodes(recorder, httptest.NewRequest(http.MethodGet, "/metadata/tmdb/works/0265010/seasons/2/episodes", nil), "0265010", 2)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("leading zero work id: status %d", recorder.Code)
	}
}

func TestBGMCandidateEpisodesKeepSectionsAndMapUnavailable(t *testing.T) {
	service := &candidateEpisodeSearch{bgm: []identitymetadatadomain.CandidateEpisode{
		{Number: 1, Title: "第一话", Section: "main"},
		{Number: 1, Title: "SP", Section: "special"},
	}}
	server := &Server{deps: Dependencies{MetadataSearch: service}}
	recorder := httptest.NewRecorder()
	server.ListBGMCandidateEpisodes(recorder, httptest.NewRequest(http.MethodGet, "/metadata/bgm/works/486109/episodes", nil), "486109")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	var body generated.BGMEpisodeList
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Episodes) != 2 || body.Episodes[0].Section != generated.BGMEpisodeSectionMain || body.Episodes[1].Section != generated.BGMEpisodeSectionSpecial {
		t.Fatalf("body = %+v", body)
	}

	service.err = errors.New("provider down")
	recorder = httptest.NewRecorder()
	server.ListBGMCandidateEpisodes(recorder, httptest.NewRequest(http.MethodGet, "/metadata/bgm/works/486109/episodes", nil), "486109")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable: status %d", recorder.Code)
	}
}
