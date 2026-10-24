package application

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bansoko/internal/anime/domain"
	configurationdomain "bansoko/internal/configuration/domain"
)

type sourceFetcherConnections struct {
	connections []configurationdomain.Connection
}

func (s sourceFetcherConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return s.connections, nil
}

type sourceFetcherResolver struct {
	client *http.Client
}

func (r sourceFetcherResolver) ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error) {
	return r.client, nil
}

func TestConfiguredSourceFetcherForwardsExplicitRouteClient(t *testing.T) {
	var proxyRequests int
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests++
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>feed</title><item><guid>g-1</guid><link>https://example.test/r/1</link><title>Show 01</title><enclosure url="https://example.test/1.torrent" type="application/x-bittorrent"/></item></channel></rss>`))
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	fetcher := ConfiguredSourceFetcher{
		Connections: sourceFetcherConnections{connections: []configurationdomain.Connection{{
			ID: "custom-feed", Kind: configurationdomain.ConnectionHTTPFeed, State: configurationdomain.ConnectionConfigured, NetworkRouteID: "route-custom",
		}}},
		ClientResolver: sourceFetcherResolver{client: &http.Client{Transport: transport}},
		Resolver:       func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.10")}, nil },
	}
	// An explicit network route is a trusted egress boundary: the request must
	// go through the resolved proxy client (asserted by proxyRequests) and the
	// feed body must parse end to end.
	items, err := fetcher.FetchSource(context.Background(), SourceLocator{
		Kind: domain.SourceKindCustomRss, URL: "http://custom.provider.test/feed.xml", HTTPFeedConnection: "custom-feed",
	})
	if err != nil {
		t.Fatalf("explicit route feed should succeed through the proxy: %v", err)
	}
	if proxyRequests == 0 {
		t.Fatal("explicit route client was not used for the request")
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 parsed feed item through proxy, got %d", len(items))
	}
}

func TestConfiguredSourceFetcherRejectsIncompleteProviderSource(t *testing.T) {
	_, err := (ConfiguredSourceFetcher{}).FetchSource(context.Background(), SourceLocator{
		Kind:             domain.SourceKindProviderGroup,
		Provider:         "anibt",
		ProviderEntryRef: "123",
		ProviderGroupRef: "anibt:",
	})
	if err == nil || !strings.Contains(err.Error(), "provider group source is invalid") {
		t.Fatalf("incomplete provider source error = %v", err)
	}
}
