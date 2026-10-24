package matcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
)

type bgmTestConnections struct {
	items []configurationdomain.Connection
	err   error
}

func (c bgmTestConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	if c.err != nil {
		return nil, c.err
	}
	return append([]configurationdomain.Connection(nil), c.items...), nil
}

type bgmTestClientResolver struct {
	client *http.Client
}

func (r bgmTestClientResolver) ClientForConnection(_ context.Context, _ configurationdomain.Connection) (*http.Client, error) {
	return r.client, nil
}

// Fallback wiring: without a bgm connection (or when the connection source
// is missing, listing fails, or connections are ambiguous) keep the default —
// the official endpoint and a direct client.
func TestBGMMatcherFallsBackToDefaultWithoutConnection(t *testing.T) {
	fallbackClient := &http.Client{}
	cases := map[string]ConnectionSource{
		"nil source":          nil,
		"no bgm connection":   bgmTestConnections{items: []configurationdomain.Connection{{ID: "tmdb", Kind: configurationdomain.ConnectionTMDB, Endpoint: "https://tmdb.invalid", State: configurationdomain.ConnectionConfigured}}},
		"list failure":        bgmTestConnections{err: fmt.Errorf("database down")},
		"ambiguous selection": bgmTestConnections{items: []configurationdomain.Connection{{ID: "bgm-1", Kind: configurationdomain.ConnectionBGM, Endpoint: "https://a.invalid", State: configurationdomain.ConnectionConfigured}, {ID: "bgm-2", Kind: configurationdomain.ConnectionBGM, Endpoint: "https://b.invalid", State: configurationdomain.ConnectionConfigured}}},
		"disabled connection": bgmTestConnections{items: []configurationdomain.Connection{{ID: "bgm", Kind: configurationdomain.ConnectionBGM, Endpoint: "https://bgm.invalid", State: configurationdomain.ConnectionDisabled}}},
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			matcher := &BGMMatcher{Connections: source, ClientResolver: bgmTestClientResolver{client: &http.Client{}}, Client: fallbackClient}
			endpoint, client := matcher.resolve(context.Background())
			if endpoint != "https://api.bgm.tv" {
				t.Fatalf("expected default endpoint, got %q", endpoint)
			}
			if client != fallbackClient {
				t.Fatalf("expected the configured direct client fallback, got %p", client)
			}
		})
	}
}

// With a single enabled bgm connection: the endpoint comes from the
// connection base_url and the client goes through route resolution.
func TestBGMMatcherUsesConfiguredConnectionEndpointAndRoutedClient(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"results":0,"list":[]}`)
	}))
	defer server.Close()

	routed := server.Client()
	matcher := &BGMMatcher{
		Connections:    bgmTestConnections{items: []configurationdomain.Connection{{ID: "bgm", Kind: configurationdomain.ConnectionBGM, Endpoint: server.URL, State: configurationdomain.ConnectionConfigured}}},
		ClientResolver: bgmTestClientResolver{client: routed},
	}
	endpoint, client := matcher.resolve(context.Background())
	if endpoint != server.URL {
		t.Fatalf("expected connection endpoint %q, got %q", server.URL, endpoint)
	}
	if client != routed {
		t.Fatal("expected the route-resolved client")
	}

	// End to end: Match hits the connection endpoint (an empty result is
	// nil, nil).
	result, err := matcher.Match(context.Background(), "Some Title")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected no match, got %+v", result)
	}
	if hits == 0 {
		t.Fatal("expected the matcher to call the configured bgm endpoint")
	}
}

// Route resolution failure is not fatal: keep the direct-client fallback and
// the BGM layer stays enabled.
func TestBGMMatcherKeepsDirectClientWhenRouteResolutionFails(t *testing.T) {
	fallbackClient := &http.Client{}
	matcher := &BGMMatcher{
		Connections:    bgmTestConnections{items: []configurationdomain.Connection{{ID: "bgm", Kind: configurationdomain.ConnectionBGM, Endpoint: "https://bgm.invalid", State: configurationdomain.ConnectionConfigured}}},
		ClientResolver: failingClientResolver{},
		Client:         fallbackClient,
	}
	endpoint, client := matcher.resolve(context.Background())
	if endpoint != "https://bgm.invalid" {
		t.Fatalf("expected the connection endpoint to still apply, got %q", endpoint)
	}
	if client != fallbackClient {
		t.Fatal("expected the direct client fallback when route resolution fails")
	}
}

type failingClientResolver struct{}

func (failingClientResolver) ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error) {
	return nil, fmt.Errorf("route unavailable")
}

func TestBGMMatcherDecodesTotalEpisodesFromSubjectDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search/subject/Ongoing" {
			_, _ = fmt.Fprint(w, `{"results":1,"list":[{"id":777,"name":"Ongoing","name_cn":"连载中"}]}`)
			return
		}
		if r.URL.Path == "/v0/subjects/777" {
			_, _ = fmt.Fprint(w, `{"id":777,"name":"Ongoing","name_cn":"连载中","eps":24}`)
			return
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer server.Close()

	matcher := &BGMMatcher{Endpoint: server.URL, Client: server.Client()}
	result, err := matcher.Match(context.Background(), "Ongoing")
	if err != nil || result == nil {
		t.Fatalf("match failed: result=%+v err=%v", result, err)
	}
	if result.TotalEpisodes == nil || *result.TotalEpisodes != 24 {
		t.Fatalf("expected TotalEpisodes=24, got %+v", result.TotalEpisodes)
	}
	if result.Subject == nil || result.Subject.Eps == nil || *result.Subject.Eps != 24 {
		t.Fatalf("expected subject.Eps=24, got %+v", result.Subject)
	}
}
