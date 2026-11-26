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

func TestIdentifySeasonDoesNotGuessAmongMultipleRegularSeasons(t *testing.T) {
	season, ok := identifySeason([]tmdbSeason{{SeasonNumber: 1, AirDate: "2000-01-01"}, {SeasonNumber: 2, AirDate: "2010-01-01"}}, nil)
	if ok || season != 0 {
		t.Fatalf("multiple seasons without clues must remain unresolved: season=%d ok=%v", season, ok)
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

func TestTMDBSearchDoesNotLoadWeakOrAmbiguousCandidates(t *testing.T) {
	for name, results := range map[string]string{
		"weak title with missing original": `[{"id":1,"name":"Different","genre_ids":[16],"popularity":100,"original_language":"zh"}]`,
		"equal scores on distinct works":   `[{"id":1,"name":"Show","genre_ids":[16]},{"id":2,"name":"Show","genre_ids":[16]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/3/search/tv":
					fmt.Fprint(w, `{"results":`+results+`}`)
				case "/3/search/movie":
					fmt.Fprint(w, `{"results":[]}`)
				default:
					t.Errorf("untrusted candidate triggered request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			got, err := newReferenceTMDBMatcher(server).Match(t.Context(), "Show")
			if err != nil || got != nil {
				t.Fatalf("result = %+v, error = %v", got, err)
			}
		})
	}
}

func TestTMDBReferenceResolvesOnlyUnambiguousDateWindows(t *testing.T) {
	for _, tc := range []struct {
		name, title, date, seasons string
		want                       int // -1 means unresolved
	}{
		{"winter crosses calendar years", "Show", "2026年冬", `[{"season_number":1,"air_date":"2025-01-04"},{"season_number":2,"air_date":"2025-12-28"}]`, 2},
		{"month end is exclusive", "Show", "2026年11月", `[{"season_number":1,"air_date":"2026-11-26"},{"season_number":2,"air_date":"2026-12-01"}]`, 1},
		{"one regular season in year", "Show", "2026", `[{"season_number":0,"air_date":"2026-02-01"},{"season_number":1,"air_date":"2025-12-28"},{"season_number":2,"air_date":"2026-01-01"}]`, 2},
		{"same-year ambiguity", "Show", "2026", `[{"season_number":1,"air_date":"2026-04-01"},{"season_number":2,"air_date":"2026-11-26"}]`, -1},
		{"dateless competitor", "Show", "2026", `[{"season_number":1,"air_date":"2026-04-01"},{"season_number":2,"air_date":null}]`, -1},
		{"invalid competitor date", "Show", "2026", `[{"season_number":1,"air_date":"2026-04-01"},{"season_number":2,"air_date":"2025-02-29"}]`, -1},
		{"specials cannot be inferred", "Show", "2026", `[{"season_number":0,"air_date":"2026-04-01"},{"season_number":1,"air_date":"2025-04-01"}]`, -1},
		{"missing explicit season", "Show S3", "2026", `[{"season_number":2,"air_date":"2026-04-01"}]`, -1},
		{"explicit specials may disagree with window", "Show S0", "2026", `[{"season_number":0,"air_date":"2025-04-01"},{"season_number":1,"air_date":"2026-04-01"}]`, 0},
		{"weak year corroborates sole season", "Show [2026] [1080P]", "", `[{"season_number":1,"air_date":"2026-04-01"}]`, 1},
		{"weak year cannot infer sequel", "Show [2026] [1080P]", "", `[{"season_number":1,"air_date":"2021-04-01"},{"season_number":2,"air_date":"2026-04-01"}]`, -1},
		{"weak year does not replace trusted year", "Show [2025] [1080P]", "2026", `[{"season_number":1,"air_date":"2025-04-01"},{"season_number":2,"air_date":"2026-04-01"}]`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/3/tv/42" {
					t.Errorf("window and weak-year resolution must not request episodes or search: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				fmt.Fprintf(w, `{"seasons":%s}`, tc.seasons)
			}))
			defer server.Close()

			got, err := newReferenceTMDBMatcher(server).MatchReference(t.Context(), tc.title, "tv", "42", nil, tc.date)
			if err != nil || tc.want == -1 && got != nil || tc.want != -1 && (got == nil || got.Season != tc.want) {
				t.Fatalf("match = %+v, err = %v; want season %d", got, err, tc.want)
			}
		})
	}
}

func TestTMDBSearchDoesNotUseTitleYearAsWorkYear(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/search/tv":
			q := r.URL.Query()
			if q.Has("first_air_date_year") || q.Has("year") {
				t.Errorf("season year must not filter work search: %s", r.URL.RawQuery)
			}
			if q.Get("query") != "Show [2026]" {
				fmt.Fprint(w, `{"results":[]}`)
				return
			}
			fmt.Fprint(w, `{"results":[{"id":42,"name":"节目","original_name":"Show [2026]","genre_ids":[16],"first_air_date":"2021-04-01","original_language":"ja"}]}`)
		case "/3/tv/42":
			fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2021-04-01"},{"season_number":2,"air_date":"2022-04-01"},{"season_number":3,"air_date":"2026-04-01"}]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	got, err := newReferenceTMDBMatcher(server).Match(t.Context(), "Show S3 [2026]")
	if err != nil || got == nil || got.Season != 3 || got.Year == nil || *got.Year != 2021 {
		t.Fatalf("match = %+v, err = %v; want season 3 and work year 2021", got, err)
	}
}

func TestTMDBSearchDoesNotSwitchWorkAfterUnresolvedSeason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/search/movie":
			fmt.Fprint(w, `{"results":[]}`)
		case "/3/search/tv":
			if r.URL.Query().Get("query") != "フールナイト" {
				t.Errorf("must not relay another title after work resolution: %s", r.URL)
			}
			fmt.Fprint(w, `{"results":[{"id":42,"name":"愚者之夜","original_name":"フールナイト","genre_ids":[16]}]}`)
		case "/3/tv/42":
			fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2026-04-01"}]}`)
		case "/3/tv/42/season/1":
			fmt.Fprint(w, `{"episodes":[{"air_date":"2026-04-01"}]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	got, err := newReferenceTMDBMatcher(server).matchTitles(t.Context(), "Fool Night", []string{"フールナイト", "愚者之夜", "Fool Night"}, parseAiringDate("2026-11-26", "bangumi.date"))
	if err != nil || got != nil {
		t.Fatalf("April episodes cannot confirm a November premiere: match = %+v, error = %v", got, err)
	}
}

func TestTMDBSearchTitleGroupsReserveDistinctRelayTerms(t *testing.T) {
	m := new(TMDBMatcher)
	got := m.searchTitleGroups([]string{" Show S3 ", "SHOW S3", "番組 (TV)", "Localized Show"})
	if len(got) != 3 || len(got[0]) != 1 || got[0][0] != "Show S3" ||
		len(got[1]) != 1 || got[1][0] != "番組 (TV)" || len(got[2]) != 1 || got[2][0] != "Localized Show" {
		t.Fatalf("three distinct original terms must leave no room for trimmed variants: %q", got)
	}
}
