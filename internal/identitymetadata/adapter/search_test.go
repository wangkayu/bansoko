package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
)

type testConnections []configurationdomain.Connection

func (c testConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return append([]configurationdomain.Connection(nil), c...), nil
}

type testSecrets map[string]string

func (s testSecrets) Resolve(ref string) (string, bool) {
	value, ok := s[ref]
	return value, ok
}

func TestConfiguredCandidateSearcherNormalizesTMDBResults(t *testing.T) {
	detailsCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/3/search/multi":
			if r.URL.Query().Get("query") != "Show" || r.URL.Query().Get("api_key") != "secret" {
				t.Fatalf("unexpected TMDB search request: %s", r.URL.String())
			}
			_, _ = fmt.Fprint(w, `{"results":[{"id":42,"media_type":"tv","name":"Show","original_name":"Original Show","overview":"剧集简介。","first_air_date":"2024-03-01","genre_ids":[16],"poster_path":"/poster.jpg"},{"id":43,"media_type":"movie","title":"Film","original_title":"Original Film","overview":"电影简介。","release_date":"2025-01-01"},{"id":44,"media_type":"person","name":"Ignored"}]}`)
		case "/3/tv/42":
			detailsCalls++
			_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":0,"name":"Specials","episode_count":2,"air_date":"2023-12-01"},{"season_number":2,"name":"Season Two","episode_count":12,"air_date":"2024-03-01"}]}`)
		default:
			t.Fatalf("unexpected TMDB request: %s", r.URL.String())
		}
	}))
	defer server.Close()

	searcher := ConfiguredCandidateSearcher{
		Connections: testConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3/configuration", SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured}},
		Secrets:     testSecrets{"tmdb-secret": "secret"},
	}
	candidates, err := searcher.Search(context.Background(), "tmdb", "Show", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || detailsCalls != 1 {
		t.Fatalf("unexpected candidates/details calls: candidates=%+v details=%d", candidates, detailsCalls)
	}
	tv := candidates[0]
	if tv.ProviderWorkID != "42" || tv.MediaKind != "tv" || tv.DetailsStatus != "available" || len(tv.Seasons) != 2 || tv.SuggestedSeason == nil || *tv.SuggestedSeason != 2 {
		t.Fatalf("unexpected normalized TV candidate: %+v", tv)
	}
	if !tv.Seasons[0].IsSpecial || tv.Seasons[0].SeasonNumber != 0 || tv.Seasons[1].IsSpecial || tv.Seasons[1].EpisodeCount != 12 {
		t.Fatalf("season facts were not normalized: %+v", tv.Seasons)
	}
	movie := candidates[1]
	if movie.MediaKind != "movie" || movie.DetailsStatus != "not_applicable" || movie.Seasons != nil || movie.SuggestedSeason != nil {
		t.Fatalf("movie must not receive TV details: %+v", movie)
	}
	// The year a result shows comes from first_air_date (TV) or release_date
	// (movie); it is the same year Native records.
	if tv.Year != 2024 || tv.Native.Year == nil || *tv.Native.Year != 2024 || movie.Year != 2025 || movie.Native.Year == nil || *movie.Native.Year != 2025 {
		t.Fatalf("TMDB candidate years were not mapped: tv=%+v movie=%+v", tv, movie)
	}
	if tv.PosterURL != "https://image.tmdb.org/t/p/w342/poster.jpg" {
		t.Fatalf("TMDB poster was not normalized: %+v", tv)
	}
	// The work summary is a TMDB-only fact: the recorded native surface of
	// each candidate keeps the overview the result carried.
	if tv.Native.Overview != "剧集简介。" || movie.Native.Overview != "电影简介。" {
		t.Fatalf("TMDB overviews were not recorded: %+v", candidates)
	}
}

func TestConfiguredCandidateSearcherDetailsStatusMatrixAndFailureIsolation(t *testing.T) {
	requests := map[string]int{}
	var requestsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests[r.URL.Path]++
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/3/search/multi":
			_, _ = fmt.Fprint(w, `{"results":[{"id":1,"media_type":"tv","name":"Empty"},{"id":2,"media_type":"tv","name":"Error"},{"id":3,"media_type":"tv","name":"Special Only"},{"id":4,"media_type":"tv","name":"Malformed"}]}`)
		case "/3/tv/1":
			_, _ = fmt.Fprint(w, `{"seasons":[]}`)
		case "/3/tv/2":
			http.Error(w, "upstream failure", http.StatusBadGateway)
		case "/3/tv/3":
			_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":0,"name":"Specials","episode_count":1,"air_date":"2024-01-01"}]}`)
		case "/3/tv/4":
			_, _ = fmt.Fprint(w, `{"seasons":`)
		default:
			t.Fatalf("unexpected TMDB request: %s", r.URL.String())
		}
	}))
	defer server.Close()

	searcher := ConfiguredCandidateSearcher{
		Connections: testConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3/configuration", SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured}},
		Secrets:     testSecrets{"tmdb-secret": "secret"},
	}
	candidates, err := searcher.Search(context.Background(), "tmdb", "Show", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 4 {
		t.Fatalf("failure in one details request must not drop candidates: %+v", candidates)
	}
	if candidates[0].DetailsStatus != "empty" || candidates[0].Seasons == nil || len(candidates[0].Seasons) != 0 || candidates[0].SuggestedSeason != nil {
		t.Fatalf("empty details projection = %+v", candidates[0])
	}
	if candidates[1].DetailsStatus != "error" || candidates[1].Seasons != nil || candidates[1].SuggestedSeason != nil {
		t.Fatalf("error details projection = %+v", candidates[1])
	}
	if candidates[2].DetailsStatus != "available" || len(candidates[2].Seasons) != 1 || candidates[2].SuggestedSeason != nil {
		t.Fatalf("special-only details projection = %+v", candidates[2])
	}
	if candidates[3].DetailsStatus != "error" || candidates[3].Seasons != nil || candidates[3].SuggestedSeason != nil {
		t.Fatalf("malformed details projection = %+v", candidates[3])
	}
	for _, candidate := range candidates {
		// No first_air_date: the year stays unknown, never defaulted.
		if candidate.Year != 0 || candidate.Native.Year != nil {
			t.Fatalf("a result without a date must carry no year: %+v", candidate)
		}
	}
	if requests["/3/tv/1"] != 1 || requests["/3/tv/2"] != 1 || requests["/3/tv/3"] != 1 || requests["/3/tv/4"] != 1 {
		t.Fatalf("each TV candidate must have at most one details request: %+v", requests)
	}
}

func TestConfiguredCandidateSearcherNormalizesBangumiResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Keyword string `json:"keyword"`
			Filter  struct {
				Type []int `json:"type"`
			} `json:"filter"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v0/search/subjects" || r.URL.Query().Get("limit") != "5" {
			t.Fatalf("unexpected Bangumi search request: %s %s", r.Method, r.URL.String())
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Keyword != "Show" || len(body.Filter.Type) != 1 || body.Filter.Type[0] != 2 {
			t.Fatalf("unexpected Bangumi search body: %+v, %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		// The v0 search carries the air date as `date`, which is the year the
		// searched work is recorded with.
		_, _ = fmt.Fprint(w, `{"total":2,"limit":5,"offset":0,"data":[{"id":123,"name":"Original","name_cn":"显示标题","date":"2025-01-02","platform":"剧场版","images":{"common":"https://img.example/123.jpg"}},{"id":124,"name":"Other","platform":"动态漫画"}]}`)
	}))
	defer server.Close()

	candidates, err := (ConfiguredCandidateSearcher{BGMEndpoint: server.URL}).Search(context.Background(), "bgm", "Show", 5)
	if err != nil {
		t.Fatal(err)
	}
	// The subject platform maps exactly; an unmapped platform stays unknown.
	if len(candidates) != 2 || candidates[0].ReleaseFormat != "movie" || candidates[1].ReleaseFormat != "" {
		t.Fatalf("unexpected Bangumi release formats: %+v", candidates)
	}
	candidates = candidates[:1]
	if len(candidates) != 1 || candidates[0].Provider != "bgm" || candidates[0].ProviderWorkID != "123" || candidates[0].Title != "显示标题" || candidates[0].Year != 2025 || candidates[0].Native.Year == nil || *candidates[0].Native.Year != 2025 {
		t.Fatalf("unexpected normalized Bangumi candidates: %+v", candidates)
	}
}

func TestSearchTMDBWithoutCredentialReturnsTypedConflict(t *testing.T) {
	searcher := ConfiguredCandidateSearcher{
		Connections: testConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: "https://api.example.test/3", State: configurationdomain.ConnectionConfigured}},
		Secrets:     testSecrets{},
	}
	_, err := searcher.Search(context.Background(), "tmdb", "Show", 5)
	if err == nil {
		t.Fatal("missing TMDB credential must fail")
	}
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "TMDB_CREDENTIAL_NOT_CONFIGURED" || business.Status != 409 {
		t.Fatalf("missing credential must be a typed 409 TMDB_CREDENTIAL_NOT_CONFIGURED, got: %v", err)
	}
}
