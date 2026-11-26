package matcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestMatchRequestInteractivePreviewLoadsExistingTMDBDetails(t *testing.T) {
	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/42" {
			t.Fatalf("existing work must be queried directly: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"name":"节目","original_name":"Show","seasons":[{"season_number":1,"name":"S1","episode_count":12},{"season_number":2,"name":"S2","episode_count":13}]}`)
	}))
	defer tmdbServer.Close()
	matched := NewMatcher(nil, nil, newReferenceTMDBMatcher(tmdbServer), nil)
	season := 2
	result, err := matched.MatchRequest(context.Background(), MatchRequest{
		Title: "Show", TmdbWorkID: "42", TmdbKind: "tv", TmdbSeason: &season,
		SkipBgm: true, LoadTmdbDetails: true,
	})
	if err != nil || result.TmdbSeason == nil || *result.TmdbSeason != 2 || result.TmdbTitleCN != "节目" || len(result.TmdbSeasons) != 2 || result.TmdbSeasons[1].EpisodeCount != 13 {
		t.Fatalf("interactive preview must load facts and retain the existing season: result=%+v err=%v", result, err)
	}
}

func TestMatchRequestLooksUpBangumiDataAfterBGMTitleSearch(t *testing.T) {
	bgmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/search/subjects":
			_, _ = fmt.Fprint(w, `{"data":[{"id":123,"name":"Show","name_cn":"节目"}]}`)
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

func TestMatchRequestRelaysOnlyTrustedBangumiTitles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		request     MatchRequest
		bgmSearch   string
		firstStatus int
		wantQueries []string
		wantSeason  int
		wantBgm     bool
	}{
		{"unique alias", MatchRequest{Title: "Fool Night"}, foolNightBGM + otherFoolSubjects, 0, []string{"tv:フールナイト", "movie:フールナイト", "tv:愚者之夜"}, 1, true},
		{"confirmed ID uses request season", MatchRequest{Title: "Fool Night S2", BgmID: "661692"}, "", 0, []string{"tv:フールナイト", "tv:愚者之夜"}, 2, true},
		{"fuzzy does not relay", MatchRequest{Title: "Fools"}, `{"id":661692,"name":"Fools Night"}`, 0, []string{"tv:Fools", "movie:Fools"}, 0, false},
		{"rate limit stops relay", MatchRequest{Title: "Fool Night"}, foolNightBGM, http.StatusTooManyRequests, []string{"tv:フールナイト"}, 0, true},
		{"authorization failure stops relay", MatchRequest{Title: "Fool Night"}, foolNightBGM, http.StatusUnauthorized, []string{"tv:フールナイト"}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bgm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v0/search/subjects":
					if tc.request.BgmID != "" {
						t.Errorf("existing ID unexpectedly searched")
					}
					fmt.Fprint(w, `{"data":[`+tc.bgmSearch+`]}`)
				case "/v0/subjects/661692":
					fmt.Fprint(w, foolNightBGM)
				default:
					t.Errorf("unexpected BGM request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer bgm.Close()
			var queries []string
			tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("language") != "zh-CN" {
					t.Errorf("unexpected locale: %s", r.URL)
				}
				switch r.URL.Path {
				case "/3/search/tv", "/3/search/movie":
					q := r.URL.Query().Get("query")
					queries = append(queries, r.URL.Path[len("/3/search/"):]+":"+q)
					if tc.firstStatus != 0 {
						w.WriteHeader(tc.firstStatus)
						return
					}
					if q == "愚者之夜" {
						fmt.Fprint(w, `{"results":[{"id":42,"name":"愚者之夜","original_name":"フールナイト","overview":"中文简介","genre_ids":[16],"first_air_date":"2026-11-26","original_language":"ja"}]}`)
						return
					}
					fmt.Fprint(w, `{"results":[]}`)
				case "/3/tv/42":
					fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2026-11-26"},{"season_number":2,"air_date":"2027-04-01"}]}`)
				default:
					t.Errorf("unexpected TMDB request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer tmdb.Close()
			matcher := NewMatcher(nil, &BGMMatcher{Endpoint: bgm.URL, Client: bgm.Client()}, newReferenceTMDBMatcher(tmdb), nil)
			got, err := matcher.MatchRequest(t.Context(), tc.request)
			if (err != nil) != (tc.firstStatus != 0) || !slices.Equal(queries, tc.wantQueries) {
				t.Fatalf("result = %+v, error = %v, queries = %v", got, err, queries)
			}
			if tc.wantBgm {
				if got.BgmID == nil || *got.BgmID != "661692" || got.BgmTitleCN != "愚者之夜" {
					t.Fatalf("trusted BGM result missing: %+v", got)
				}
			} else if got.BgmID != nil {
				t.Fatalf("untrusted BGM result must not bind: %+v", got)
			}
			if tc.wantSeason == 0 {
				if got.TmdbWorkID != nil {
					t.Fatalf("unexpected TMDB binding: %+v", got)
				}
			} else if !got.IsComplete() || *got.TmdbSeason != tc.wantSeason || got.TmdbTitleCN != "愚者之夜" || got.TmdbOverview != "中文简介" {
				t.Fatalf("unexpected localized match: %+v", got)
			}
		})
	}
}
