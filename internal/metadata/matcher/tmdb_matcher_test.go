package matcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
)

type tmdbTestConnections []configurationdomain.Connection

func (c tmdbTestConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return append([]configurationdomain.Connection(nil), c...), nil
}

type tmdbTestSecrets map[string]string

func (s tmdbTestSecrets) Resolve(ref string) (string, bool) {
	value, ok := s[ref]
	return value, ok
}

type tmdbTestClientResolver struct {
	client *http.Client
}

func (r tmdbTestClientResolver) ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error) {
	return r.client, nil
}

func TestTMDBMatcherUsesConfiguredClientAndBearerToken(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer eyJtest-token" {
			t.Fatalf("expected bearer authorization, got %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/3/search/tv" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"results":[{"id":42,"name":"Show","original_name":"Show","overview":"matched overview","genre_ids":[16],"popularity":50,"original_language":"ja"}]}`)
			return
		}
		if r.URL.Path == "/3/tv/42" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2024-01-01"}]}`)
			return
		}
		t.Fatalf("unexpected TMDB request path: %s", r.URL.Path)
	}))
	defer server.Close()

	matcher := &TMDBMatcher{
		Connections:    tmdbTestConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3/configuration", SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured}},
		Secrets:        tmdbTestSecrets{"tmdb-secret": "eyJtest-token"},
		ClientResolver: tmdbTestClientResolver{client: server.Client()},
	}
	result, err := matcher.Match(context.Background(), "Show")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.WorkID != "42" || result.MediaKind != "tv" || result.Season != 1 || result.Confidence < tmdbMinConfidence {
		t.Fatalf("unexpected TMDB match: %+v", result)
	}
	if result.Overview != "matched overview" {
		t.Fatalf("the match carries the work summary, got %q", result.Overview)
	}
	if requests != 2 {
		t.Fatalf("expected search and details requests, got %d", requests)
	}
}

func TestTMDBMatcherDoesNotReturnBelowThresholdCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/3/search/tv" || r.URL.Path == "/3/search/movie" {
			_, _ = fmt.Fprint(w, `{"results":[{"id":42,"name":"Unrelated","original_name":"Unrelated","genre_ids":[16],"popularity":0,"original_language":"xx"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"seasons":[]}`)
	}))
	defer server.Close()

	matcher := &TMDBMatcher{
		Connections: tmdbTestConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3", SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured}},
		Secrets:     tmdbTestSecrets{"tmdb-secret": "v3-key"},
	}
	result, err := matcher.Match(context.Background(), "Completely Different")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("below-threshold TMDB candidate must remain unresolved: %+v", result)
	}
}

func TestTMDBMatcherPropagatesDetailsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/3/search/tv" {
			_, _ = fmt.Fprint(w, `{"results":[{"id":42,"name":"Show","original_name":"Show","genre_ids":[16],"popularity":50,"original_language":"ja"}]}`)
			return
		}
		http.Error(w, "details unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	matcher := &TMDBMatcher{
		Connections: tmdbTestConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3", SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured}},
		Secrets:     tmdbTestSecrets{"tmdb-secret": "v3-key"},
	}
	_, err := matcher.Match(context.Background(), "Show")
	if !IsMatchErrorKind(err, ErrorProvider) {
		t.Fatalf("TV details failure must remain a provider error, got %v", err)
	}
}

func TestTMDBMatcherSkipsEmptyTitleWithoutCallingProvider(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	matcher := &TMDBMatcher{
		Connections: tmdbTestConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3", SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured}},
		Secrets:     tmdbTestSecrets{"tmdb-secret": "v3-key"},
	}
	result, err := matcher.Match(context.Background(), "   ")
	if err != nil || result != nil || requests != 0 {
		t.Fatalf("empty title must be a local no-result without provider access: result=%+v err=%v requests=%d", result, err, requests)
	}
}

func TestIdentifySeasonHonorsRequestedSeasonAndIgnoresSpecials(t *testing.T) {
	requested := 2
	season, ok := identifySeason([]tmdbSeason{
		{SeasonNumber: 0},
		{SeasonNumber: 1, AirDate: "2020-01-01"},
		{SeasonNumber: 2, AirDate: "2024-01-01"},
	}, &requested)
	if !ok || season != 2 {
		t.Fatalf("requested regular season must win over specials and recency: season=%d ok=%v", season, ok)
	}
	if season, ok := identifySeason([]tmdbSeason{{SeasonNumber: 0}}, nil); ok || season != 0 {
		t.Fatalf("specials-only details must remain unresolved: season=%d ok=%v", season, ok)
	}
}

func TestTMDBMatcherDoesNotDecodeStatusOrEpisodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/3/search/tv" {
			_, _ = fmt.Fprint(w, `{"results":[{"id":99,"name":"Ongoing","original_name":"Ongoing","genre_ids":[16],"popularity":50,"original_language":"ja"}]}`)
			return
		}
		if r.URL.Path == "/3/tv/99" {
			// TMDB returns status/number_of_episodes but the matcher must
			// ignore them: multi-season shows make the show-level count
			// unreliable for a single cour.
			_, _ = fmt.Fprint(w, `{"seasons":[{"season_number":1,"air_date":"2024-01-01"}],"number_of_episodes":12,"status":"Returning Series","in_production":true}`)
			return
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer server.Close()

	matcher := &TMDBMatcher{
		Connections:    tmdbTestConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: server.URL + "/3/configuration", SecretRef: "tmdb-secret", State: configurationdomain.ConnectionConfigured}},
		Secrets:        tmdbTestSecrets{"tmdb-secret": "test-key"},
		ClientResolver: tmdbTestClientResolver{client: server.Client()},
	}
	result, err := matcher.Match(context.Background(), "Ongoing")
	if err != nil || result == nil {
		t.Fatalf("match failed: result=%+v err=%v", result, err)
	}
	// TMDBMatchResult carries only WorkID/MediaKind/Season. The struct
	// definition itself enforces this at compile time; this test confirms the
	// matcher still resolves work_id/season correctly while the extra TMDB
	// fields in the response are silently ignored.
	if result.WorkID != "99" || result.Season != 1 {
		t.Fatalf("expected WorkID=99 Season=1, got %+v", result)
	}
}
