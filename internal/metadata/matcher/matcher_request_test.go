package matcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestMatchRequestReusesExistingBGMIDAndResolvesMappedSeason(t *testing.T) {
	bgmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/subjects/123" {
			t.Fatalf("existing BGM ID must fetch details directly: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"id":123,"name":"Show 2","name_cn":"节目 第二季","date":"2025-04-01","eps":12}`)
	}))
	defer bgmServer.Close()
	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/42" {
			t.Fatalf("index reference must not start a TMDB search: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"name":"节目","original_name":"Show","seasons":[{"season_number":1,"air_date":"2024-04-01"},{"season_number":2,"air_date":"2025-04-01"}]}`)
	}))
	defer tmdbServer.Close()

	index := NewBangumiDataIndex([]DataItem{{BgmID: "123", TmdbID: "tv/42/season/2"}})
	matched := NewMatcher(index, &BGMMatcher{Endpoint: bgmServer.URL, Client: bgmServer.Client()}, newReferenceTMDBMatcher(tmdbServer), nil)
	result, err := matched.MatchRequest(context.Background(), MatchRequest{Title: "Show 2", BgmID: "123", SkipBgm: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsComplete() || result.BgmID == nil || *result.BgmID != "123" || result.TmdbSeason == nil || *result.TmdbSeason != 2 || result.TotalEpisodes == nil || *result.TotalEpisodes != 12 || result.BgmTitleCN != "节目 第二季" {
		t.Fatalf("existing BGM ID must drive index lookup and fact backfill: %+v", result)
	}
}

func TestMatchRequestLooksUpBangumiDataAfterBGMTitleSearch(t *testing.T) {
	bgmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/subject/Show":
			_, _ = fmt.Fprint(w, `{"results":1,"list":[{"id":123,"name":"Show","name_cn":"节目"}]}`)
		case "/v0/subjects/123":
			_, _ = fmt.Fprint(w, `{"id":123,"name":"Show","name_cn":"节目","date":"2024-04-01","eps":12}`)
		default:
			t.Fatalf("unexpected BGM request: %s", r.URL.Path)
		}
	}))
	defer bgmServer.Close()
	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/42" {
			t.Fatalf("BGM result must be resolved through the index to TMDB 42: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2024-04-01"}]}`)
	}))
	defer tmdbServer.Close()

	index := NewBangumiDataIndex([]DataItem{{BgmID: "123", TmdbID: "tv/42"}})
	matched := NewMatcher(index, &BGMMatcher{Endpoint: bgmServer.URL, Client: bgmServer.Client()}, newReferenceTMDBMatcher(tmdbServer), nil)
	result, err := matched.MatchRequest(context.Background(), MatchRequest{Origin: "mikan", EntryRef: "not-in-index", Title: "Show"})
	if err != nil || !result.IsComplete() || result.TmdbWorkID == nil || *result.TmdbWorkID != "42" || result.TmdbSeason == nil || *result.TmdbSeason != 1 {
		t.Fatalf("BGM title search must feed the BGM ID index: result=%+v err=%v", result, err)
	}
}

func TestMatchRequestDoesNotMixExistingBGMWithAnotherWork(t *testing.T) {
	index := NewBangumiDataIndex([]DataItem{{BgmID: "456", TmdbID: "tv/42", Titles: []string{"Show"}}})
	matched := NewMatcher(index, nil, nil, nil)
	result, err := matched.MatchRequest(context.Background(), MatchRequest{Title: "Show", BgmID: "123", SkipBgm: true})
	if err != nil || result.BgmID == nil || *result.BgmID != "123" || result.TmdbWorkID != nil {
		t.Fatalf("a title hit for BGM 456 must not lend TMDB 42 to BGM 123: result=%+v err=%v", result, err)
	}
}

func TestMatchRequestKeepsCompletedTMDBWhenBGMDetailFails(t *testing.T) {
	bgmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary", http.StatusBadGateway)
	}))
	defer bgmServer.Close()
	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":2,"air_date":"2025-04-01"}]}`)
	}))
	defer tmdbServer.Close()
	index := NewBangumiDataIndex([]DataItem{{BgmID: "123", TmdbID: "tv/42/season/2"}})
	matched := NewMatcher(index, &BGMMatcher{Endpoint: bgmServer.URL, Client: bgmServer.Client()}, newReferenceTMDBMatcher(tmdbServer), nil)
	result, err := matched.MatchRequest(context.Background(), MatchRequest{Title: "Show 2", BgmID: "123", SkipBgm: true})
	if err != nil || !result.IsComplete() || result.TmdbSeason == nil || *result.TmdbSeason != 2 {
		t.Fatalf("supplemental BGM failure must not discard the completed TMDB tuple: result=%+v err=%v", result, err)
	}
}

func TestMatcherPublishesIndexAtomically(t *testing.T) {
	matched := NewMatcher(NewBangumiDataIndex([]DataItem{{BgmID: "1", Titles: []string{"Show"}}}), nil, nil, nil)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				result, err := matched.Match(context.Background(), "custom", "", "Show", true, true)
				if err != nil || result.BgmID == nil {
					t.Errorf("in-flight match must retain a complete old index: result=%+v err=%v", result, err)
					return
				}
			}
		}()
	}
	for n := 2; n < 102; n++ {
		matched.ReplaceBangumiDataIndex(NewBangumiDataIndex([]DataItem{{BgmID: fmt.Sprint(n), Titles: []string{"Show"}}}))
	}
	wg.Wait()
}
