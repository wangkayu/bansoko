package application

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
)

// Lazy seed expansion is a request about one provider's upstream, so it must
// leave through that provider's saved connection and network route. These tests
// pin the two halves of that contract: a route is used when the connection has
// one, and the fetch is unchanged (direct) when it does not.

// seedTorrentFixtureBody is a valid single-file torrent, so the reader's parse
// step is exercised rather than bypassed.
var seedTorrentFixtureBody = []byte("d4:infod4:name8:show.mkv6:lengthi12345eee")

type seedTorrentConnections struct {
	connections []configurationdomain.Connection
	err         error
}

func (s seedTorrentConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.connections, nil
}

// seedTorrentRouteResolver hands back one routed client and records which
// connection it was asked for, so a test can prove the route was selected (or
// never consulted).
type seedTorrentRouteResolver struct {
	mu     sync.Mutex
	client *http.Client
	err    error
	asked  []configurationdomain.Connection
}

func (r *seedTorrentRouteResolver) ClientForConnection(_ context.Context, connection configurationdomain.Connection) (*http.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, connection)
	if r.err != nil {
		return nil, r.err
	}
	return r.client, nil
}

func (r *seedTorrentRouteResolver) askedConnections() []configurationdomain.Connection {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]configurationdomain.Connection(nil), r.asked...)
}

// seedTorrentRoutedClient is the client a network route resolves to. Its
// transport dials the given test server and counts the requests it carried, so a
// test can tell "the routed client fetched it" from "something dialed directly".
func seedTorrentRoutedClient(t *testing.T, target net.Addr) (*http.Client, func() int) {
	t.Helper()
	var mu sync.Mutex
	requests := 0
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		mu.Lock()
		requests++
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, target.String())
	}
	return &http.Client{Transport: transport}, func() int {
		mu.Lock()
		defer mu.Unlock()
		return requests
	}
}

func seedTorrentServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestConfiguredSeedTorrentFetcherUsesTheRouteOfTheSnapshotProvider covers every
// provider that reaches lazy expansion with a routable connection. Each selects
// its own connection kind, and the request must be carried by the client that
// connection's route resolved.
func TestConfiguredSeedTorrentFetcherUsesTheRouteOfTheSnapshotProvider(t *testing.T) {
	for provider, wantKind := range map[string]configurationdomain.ConnectionKind{
		"anibt": configurationdomain.ConnectionAniBT,
		"mikan": configurationdomain.ConnectionMikan,
		"nyaa":  configurationdomain.ConnectionNyaa,
	} {
		t.Run(provider, func(t *testing.T) {
			server := seedTorrentServer(t, seedTorrentFixtureBody)
			client, routedRequests := seedTorrentRoutedClient(t, server.Listener.Addr())
			resolver := &seedTorrentRouteResolver{client: client}
			fetcher := ConfiguredSeedTorrentFetcher{
				Connections: seedTorrentConnections{connections: []configurationdomain.Connection{
					{ID: "route-" + provider, Kind: wantKind, State: configurationdomain.ConnectionConfigured, NetworkRouteID: "route-1"},
				}},
				ClientResolver: resolver,
			}

			// The locator names a public address: the routed client dials the test
			// server, so a direct dial would not reach it at all.
			payload, err := fetcher.FetchPayload(context.Background(), provider, "http://203.0.113.10/release.torrent")
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if string(payload.Bytes) != string(seedTorrentFixtureBody) {
				t.Fatalf("payload bytes = %q, want the routed response body", payload.Bytes)
			}
			if payload.Metadata.TorrentName != "show.mkv" {
				t.Fatalf("payload metadata = %+v", payload.Metadata)
			}
			if routedRequests() != 1 {
				t.Fatalf("routed client requests = %d, want exactly 1", routedRequests())
			}
			asked := resolver.askedConnections()
			if len(asked) != 1 || asked[0].Kind != wantKind || asked[0].NetworkRouteID != "route-1" {
				t.Fatalf("resolved connections = %+v, want the %s connection with route-1", asked, wantKind)
			}
		})
	}
}

// TestConfiguredSeedTorrentFetcherDialsDirectlyWhenNoRouteApplies pins the
// unchanged behavior: without a route the fetch is exactly the old direct one,
// and the route resolver is never consulted. Both ways a source can end up
// routeless are covered — an enabled connection with no route, no enabled
// connection at all (the code default), and a custom source whose connection a
// snapshot identity does not carry.
func TestConfiguredSeedTorrentFetcherDialsDirectlyWhenNoRouteApplies(t *testing.T) {
	for name, testCase := range map[string]struct {
		provider    string
		connections []configurationdomain.Connection
	}{
		"enabled connection without a route": {
			provider: "mikan",
			connections: []configurationdomain.Connection{
				{ID: "c1", Kind: configurationdomain.ConnectionMikan, State: configurationdomain.ConnectionConfigured},
			},
		},
		"no enabled connection": {provider: "nyaa"},
		"custom source": {
			provider: "custom",
			connections: []configurationdomain.Connection{
				{ID: "feed", Kind: configurationdomain.ConnectionHTTPFeed, State: configurationdomain.ConnectionConfigured, NetworkRouteID: "route-1"},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := seedTorrentServer(t, seedTorrentFixtureBody)
			resolver := &seedTorrentRouteResolver{err: errors.New("a routeless fetch must not resolve a route client")}
			fetcher := ConfiguredSeedTorrentFetcher{
				Connections:       seedTorrentConnections{connections: testCase.connections},
				ClientResolver:    resolver,
				AllowPrivateHosts: []string{"127.0.0.1"},
			}

			// The locator is a fixture-allowlisted local address, so the direct
			// transport the reader builds reaches the test server and a routed
			// client would be the only way the resolver is touched.
			payload, err := fetcher.FetchPayload(context.Background(), testCase.provider, server.URL+"/release.torrent")
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if string(payload.Bytes) != string(seedTorrentFixtureBody) {
				t.Fatalf("payload bytes = %q, want the direct response body", payload.Bytes)
			}
			if asked := resolver.askedConnections(); len(asked) != 0 {
				t.Fatalf("route clients resolved = %+v, want none for a routeless fetch", asked)
			}
		})
	}
}

// TestConfiguredSeedTorrentFetcherRefusesAmbiguousOrFailedRouting keeps a
// misconfigured route a reported failure instead of a silent direct dial: an
// ambiguous connection set or a route resolution error must reach the caller.
func TestConfiguredSeedTorrentFetcherRefusesAmbiguousOrFailedRouting(t *testing.T) {
	t.Run("ambiguous connections", func(t *testing.T) {
		fetcher := ConfiguredSeedTorrentFetcher{Connections: seedTorrentConnections{connections: []configurationdomain.Connection{
			{ID: "a", Kind: configurationdomain.ConnectionMikan, State: configurationdomain.ConnectionConfigured, NetworkRouteID: "route-1"},
			{ID: "b", Kind: configurationdomain.ConnectionMikan, State: configurationdomain.ConnectionConfigured, NetworkRouteID: "route-2"},
		}}}
		_, err := fetcher.FetchPayload(context.Background(), "mikan", "http://203.0.113.10/release.torrent")
		if err == nil || !strings.Contains(err.Error(), "multiple enabled") {
			t.Fatalf("ambiguous connections = %v, want a multiple-enabled-connection error", err)
		}
	})

	t.Run("route resolution fails", func(t *testing.T) {
		fetcher := ConfiguredSeedTorrentFetcher{
			Connections: seedTorrentConnections{connections: []configurationdomain.Connection{
				{ID: "a", Kind: configurationdomain.ConnectionMikan, State: configurationdomain.ConnectionConfigured, NetworkRouteID: "route-1"},
			}},
			ClientResolver: &seedTorrentRouteResolver{err: errors.New("route unreachable")},
		}
		_, err := fetcher.FetchPayload(context.Background(), "mikan", "http://203.0.113.10/release.torrent")
		if err == nil || !strings.Contains(err.Error(), "resolve seed torrent network route") {
			t.Fatalf("failed route resolution = %v, want the wrapped route error", err)
		}
	})
}
