package matcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
)

func newReferenceTMDBMatcher(server *httptest.Server) *TMDBMatcher {
	return &TMDBMatcher{
		Connections: tmdbTestConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3", SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured}},
		Secrets:     tmdbTestSecrets{"tmdb-secret": "v3-key"},
		Client:      server.Client(),
	}
}

func TestTMDBReferenceUsesMappedSeasonAndNeverSearches(t *testing.T) {
	mapped := 2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/42" {
			t.Fatalf("direct reference must not search or query another work: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"name":"节目","original_name":"Show","overview":"summary","first_air_date":"2024-04-01","seasons":[{"season_number":1,"air_date":"2024-04-01"},{"season_number":2,"air_date":"2025-04-01"}]}`)
	}))
	defer server.Close()
	result, err := newReferenceTMDBMatcher(server).MatchReference(context.Background(), "Show S9", "tv", "42", &mapped, "2025-04-02")
	if err != nil || result == nil || result.Season != 2 || result.WorkID != "42" || result.TitleCN != "节目" || result.Overview != "summary" {
		t.Fatalf("mapped season must resolve against the exact work: result=%+v err=%v", result, err)
	}
}

func TestTMDBReferenceHonorsExplicitSpecialsSeason(t *testing.T) {
	mapped := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":0,"air_date":"2024-01-01"},{"season_number":1,"air_date":"2024-04-01"}]}`)
	}))
	defer server.Close()
	result, err := newReferenceTMDBMatcher(server).MatchReference(context.Background(), "Show", "tv", "42", &mapped, "")
	if err != nil || result == nil || result.Season != 0 {
		t.Fatalf("an explicit bangumi-data specials reference must be preserved: result=%+v err=%v", result, err)
	}
}

func TestTMDBReferenceRejectsRequestedSeasonWithConflictingDates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/42":
			_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2020-01-01"}]}`)
		case "/3/tv/42/season/1":
			_, _ = fmt.Fprint(w, `{"episodes":[{"air_date":"2020-01-01"},{"air_date":"2020-03-01"}]}`)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result, err := newReferenceTMDBMatcher(server).MatchReference(context.Background(), "Show 第2季", "tv", "42", nil, "2025-04-01")
	if err != nil || result != nil {
		t.Fatalf("a missing requested season and unrelated dates must stay unresolved: result=%+v err=%v", result, err)
	}
}

func TestTMDBReferenceAllowsMergedSeasonWhenDatesMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2025-04-01"}]}`)
	}))
	defer server.Close()
	result, err := newReferenceTMDBMatcher(server).MatchReference(context.Background(), "Show S2", "tv", "42", nil, "2025-04-02")
	if err != nil || result == nil || result.Season != 1 {
		t.Fatalf("matching dates may identify a BGM split entry within TMDB season 1: result=%+v err=%v", result, err)
	}
}

func TestTMDBReferenceUsesEpisodeDateForSplitCourWithoutOffset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/42":
			_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2024-01-01"},{"season_number":2,"air_date":"2025-01-01"}]}`)
		case "/3/tv/42/season/1":
			_, _ = fmt.Fprint(w, `{"episodes":[{"air_date":"2024-01-01"},{"air_date":"2024-07-02"}]}`)
		default:
			t.Fatalf("the nearest season's episodes should confirm the split cour: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result, err := newReferenceTMDBMatcher(server).MatchReference(context.Background(), "Show", "tv", "42", nil, "2024-07-01")
	if err != nil || result == nil || result.Season != 1 {
		t.Fatalf("episode air date should confirm TMDB season 1: result=%+v err=%v", result, err)
	}
}

func TestTMDBReferenceUsesNearestSeasonAirDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/42" {
			t.Fatalf("season air-date hit must not fetch episodes: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2024-04-01"},{"season_number":2,"air_date":"2024-04-03"}]}`)
	}))
	defer server.Close()
	result, err := newReferenceTMDBMatcher(server).MatchReference(context.Background(), "Show", "tv", "42", nil, "2024-04-04T00:00:00Z")
	if err != nil || result == nil || result.Season != 2 {
		t.Fatalf("nearest season within three days must win: result=%+v err=%v", result, err)
	}
}

func TestIdentifySeasonUsesLatestAiredWithoutAnAgeLimit(t *testing.T) {
	season, ok := identifySeason([]tmdbSeason{{SeasonNumber: 1, AirDate: "2000-01-01"}, {SeasonNumber: 2, AirDate: "2010-01-01"}}, nil)
	if !ok || season != 2 {
		t.Fatalf("old works must still select the latest aired season: season=%d ok=%v", season, ok)
	}
}

func TestTMDBSearchKeepsOriginalTitleSeasonAfterTrimming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/search/tv":
			if got := r.URL.Query().Get("query"); got == "Show S2" {
				_, _ = fmt.Fprint(w, `{"results":[]}`)
				return
			}
			if got := r.URL.Query().Get("query"); got != "Show" {
				t.Fatalf("expected the trimmed retry query, got %q", got)
			}
			_, _ = fmt.Fprint(w, `{"results":[{"id":42,"name":"Show","original_name":"Show","genre_ids":[16],"popularity":50,"original_language":"ja"}]}`)
		case "/3/tv/42":
			_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2024-01-01"},{"season_number":2,"air_date":"2025-01-01"}]}`)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result, err := newReferenceTMDBMatcher(server).Match(context.Background(), "Show S2")
	if err != nil || result == nil || result.Season != 2 {
		t.Fatalf("trimming the search query must not erase the original season: result=%+v err=%v", result, err)
	}
}
