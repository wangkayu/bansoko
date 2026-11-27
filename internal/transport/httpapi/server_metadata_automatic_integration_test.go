package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	configuration "bansoko/internal/configuration/domain"
	metadata "bansoko/internal/identitymetadata/adapter"
	"bansoko/internal/metadata/matcher"
	"bansoko/internal/transport/httpapi/generated"
)

type automaticSearchSecret string

func (s automaticSearchSecret) Resolve(string) (string, bool) { return string(s), true }

func TestAutomaticMetadataSearchKeepsTMDBCandidateAfterBGMTimeout(t *testing.T) {
	var bgmHits atomic.Int32
	bgm := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		bgmHits.Add(1)
		if r.URL.Path != "/v0/subjects/123" {
			t.Errorf("BGM path = %s", r.URL.Path)
		}
		<-r.Context().Done()
	}))
	defer bgm.Close()
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/42" {
			t.Errorf("TMDB path = %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"name":"节目","original_name":"Show","first_air_date":"2024-04-01","seasons":[{"season_number":1,"air_date":"2024-04-01"},{"season_number":2,"air_date":"2025-04-01"}]}`)
	}))
	defer tmdb.Close()
	bgmClient := bgm.Client()
	bgmClient.Timeout = 300 * time.Millisecond
	connections := previewConnections{{ID: "tmdb-test", Kind: configuration.ConnectionTMDB, Endpoint: tmdb.URL + "/3", State: configuration.ConnectionConfigured, SecretRef: "tmdb-test-secret"}}
	search := metadata.NewSearchService(connections, automaticSearchSecret("test-api-key"))
	search.SetMatcher(matcher.NewMatcher(nil, &matcher.BGMMatcher{Endpoint: bgm.URL, Client: bgmClient}, &matcher.TMDBMatcher{Connections: connections, Secrets: automaticSearchSecret("test-api-key"), Client: tmdb.Client()}, nil))
	server := &Server{deps: Dependencies{MetadataSearch: search}}
	request := httptest.NewRequest(http.MethodPost, "/metadata/search/automatic", strings.NewReader(`{"provider":"tmdb","title":"Show","origin":"custom","bgm_work_id":"123","tmdb_work_id":"42","tmdb_media_kind":"tv","tmdb_season":2}`))
	response := httptest.NewRecorder()
	server.SearchMetadataAutomatically(response, request)
	var result generated.MetadataSearchResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || bgmHits.Load() != 1 || result.Upstream.State != "available" || len(result.Candidates) != 1 {
		t.Fatalf("status=%d BGM requests=%d body=%s err=%v", response.Code, bgmHits.Load(), response.Body, err)
	}
	candidate := result.Candidates[0]
	if candidate.ProviderWorkId != "42" || candidate.SuggestedSeason == nil || *candidate.SuggestedSeason != 2 {
		t.Fatalf("unexpected candidate %+v", candidate)
	}
}
