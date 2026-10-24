package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/episodetitle"
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

func tmdbClient(endpoint, apiKey string) TMDBClient {
	return TMDBClient{
		Connections: testConnections{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: endpoint, SecretRef: "ref", State: configurationdomain.ConnectionConfigured}},
		Secrets:     testSecrets{"ref": apiKey},
	}
}

var tmdbKey = episodetitle.Key{Source: episodetitle.SourceTMDB, WorkID: "42", Season: 2}

func TestTMDBParsesSeasonWithAPIKeyAndBearer(t *testing.T) {
	for _, apiKey := range []string{"v3secret", "eyJbearer"} {
		t.Run(apiKey, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/3/tv/42/season/2" || r.URL.Query().Get("language") != "zh-CN" {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				gotKey, gotAuth := r.URL.Query().Get("api_key"), r.Header.Get("Authorization")
				if strings.HasPrefix(apiKey, "eyJ") {
					if gotKey != "" || gotAuth != "Bearer "+apiKey {
						t.Errorf("bearer token misrouted: api_key=%q auth=%q", gotKey, gotAuth)
					}
				} else if gotKey != apiKey || gotAuth != "" {
					t.Errorf("api key misrouted: api_key=%q auth=%q", gotKey, gotAuth)
				}
				_, _ = fmt.Fprint(w, `{"episodes":[{"episode_number":1,"name":" 第一话 "},{"episode_number":2,"name":""},{"episode_number":12,"name":"Twelve"}]}`)
			}))
			defer server.Close()

			list, err := tmdbClient(server.URL+"/3/configuration", apiKey).Episodes(context.Background(), tmdbKey)
			if err != nil {
				t.Fatal(err)
			}
			want := []episodetitle.Episode{{Number: 1, Title: "第一话"}, {Number: 2}, {Number: 12, Title: "Twelve"}}
			if fmt.Sprint(list.Episodes) != fmt.Sprint(want) {
				t.Fatalf("episodes = %v, want %v", list.Episodes, want)
			}
		})
	}
}

func TestStatusMapping(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		_, tmdbErr := tmdbClient(server.URL, "v3secret").Episodes(context.Background(), tmdbKey)
		server.Close()
		for name, err := range map[string]error{"tmdb": tmdbErr} {
			if err == nil {
				t.Fatalf("%s HTTP %d: expected error", name, status)
			}
			if isNotFound := errors.Is(err, episodetitle.ErrNotFound); isNotFound != (status == http.StatusNotFound) {
				t.Fatalf("%s HTTP %d: ErrNotFound=%v err=%v", name, status, isNotFound, err)
			}
			if strings.Contains(err.Error(), "v3secret") {
				t.Fatalf("%s error leaks api key: %v", name, err)
			}
		}
	}
}

func TestErrorsDoNotLeakAPIKey(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint := server.URL
	server.Close()
	_, err := tmdbClient(endpoint, "v3secret").Episodes(context.Background(), tmdbKey)
	if err == nil || errors.Is(err, episodetitle.ErrNotFound) || strings.Contains(err.Error(), "v3secret") {
		t.Fatalf("expected a redacted network error, got %v", err)
	}
}

func TestDecodeErrorIsNotNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{not json`)
	}))
	defer server.Close()
	if _, err := tmdbClient(server.URL, "v3secret").Episodes(context.Background(), tmdbKey); err == nil || errors.Is(err, episodetitle.ErrNotFound) {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestRejectsNonNumericWorkID(t *testing.T) {
	if _, err := tmdbClient("http://127.0.0.1:1", "k").Episodes(context.Background(), episodetitle.Key{Source: episodetitle.SourceTMDB, WorkID: "a"}); err == nil {
		t.Fatal("expected error")
	}
}
