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

func bgmTestClient(endpoint string) BGMClient {
	return BGMClient{
		Connections: testConnections{{ID: "bgm", Kind: configurationdomain.ConnectionBGM, Endpoint: endpoint, State: configurationdomain.ConnectionConfigured}},
	}
}

func TestBGMSectionedEpisodesKeepMainAndSpecials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/episodes" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		if r.URL.Query().Get("subject_id") != "486109" {
			t.Errorf("unexpected subject %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("offset") != "0" {
			t.Errorf("unexpected paging %s", r.URL.RawQuery)
		}
		_, _ = fmt.Fprint(w, `{"data":[
			{"type":0,"ep":1,"name":"第一话","name_cn":"第一话","airdate":"2025-01-04"},
			{"type":2,"ep":1,"name":"OP"},
			{"type":1,"ep":0,"name":"SP1"},
			{"type":0,"ep":"2","name":"第二话","airdate":"2025-01-11"},
			{"type":1,"ep":null,"name":"SP2"}
		],"total":5}`)
	}))
	defer server.Close()

	list, err := bgmTestClient(server.URL).SectionedEpisodes(context.Background(), episodetitle.Key{Source: episodetitle.SourceBGM, WorkID: "486109"})
	if err != nil {
		t.Fatal(err)
	}
	want := []episodetitle.SectionedEpisode{
		{Number: 1, Title: "第一话", AirDate: "2025-01-04", Section: episodetitle.SectionMain},
		{Number: 1, Title: "SP1", Section: episodetitle.SectionSpecial},
		{Number: 2, Title: "第二话", AirDate: "2025-01-11", Section: episodetitle.SectionMain},
		{Number: 2, Title: "SP2", Section: episodetitle.SectionSpecial},
	}
	if fmt.Sprint(list.Episodes) != fmt.Sprint(want) {
		t.Fatalf("episodes = %v, want %v", list.Episodes, want)
	}
}

func TestBGMSectionedEpisodesFetchEveryPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		var body strings.Builder
		body.WriteString(`{"data":[`)
		first, count := 0, 100
		if offset == "100" {
			first, count = 100, 1
		}
		for index := first; index < first+count; index++ {
			if index > first {
				body.WriteString(",")
			}
			_, _ = fmt.Fprintf(&body, `{"type":0,"ep":%d,"name":"E%d"}`, index+1, index+1)
		}
		_, _ = fmt.Fprintf(&body, `],"total":101}`)
		_, _ = fmt.Fprint(w, body.String())
	}))
	defer server.Close()

	list, err := bgmTestClient(server.URL).SectionedEpisodes(context.Background(), episodetitle.Key{Source: episodetitle.SourceBGM, WorkID: "486109"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Episodes) != 101 || list.Episodes[100].Number != 101 {
		t.Fatalf("got %d episodes, last %+v", len(list.Episodes), list.Episodes[len(list.Episodes)-1])
	}
}

func TestBGMEpisodesUsesSubjectScopedEndpointAndMainType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/episodes" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		if r.URL.Query().Get("subject_id") != "363957" || r.URL.Query().Get("type") != "0" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		// ep arrives as a JSON number in the real payload. The special and the
		// OP prove a proxy that ignores the type query cannot slip one in.
		_, _ = fmt.Fprint(w, `{"data":[
			{"type":0,"ep":1,"name":"第一话","name_cn":"第一话","airdate":"2025-04-07"},
			{"type":1,"ep":0,"name":"SP"},
			{"type":0,"ep":"2","name":"第二话","airdate":"2025-04-14"},
			{"type":2,"ep":1,"name":"OP"}
		]}`)
	}))
	defer server.Close()

	list, err := bgmTestClient(server.URL).Episodes(context.Background(), episodetitle.Key{Source: episodetitle.SourceBGM, WorkID: "363957"})
	if err != nil {
		t.Fatal(err)
	}
	want := []episodetitle.Episode{
		{Number: 1, Title: "第一话", AirDate: "2025-04-07"},
		{Number: 2, Title: "第二话", AirDate: "2025-04-14"},
	}
	if fmt.Sprint(list.Episodes) != fmt.Sprint(want) {
		t.Fatalf("episodes = %v, want %v", list.Episodes, want)
	}
}
