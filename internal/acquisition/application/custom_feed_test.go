package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

type recordingCustomHTTPClientResolver struct {
	client *http.Client
	seen   []configurationdomain.Connection
}

func (r *recordingCustomHTTPClientResolver) ClientForConnection(_ context.Context, connection configurationdomain.Connection) (*http.Client, error) {
	r.seen = append(r.seen, connection)
	return r.client, nil
}

func TestConfiguredCustomRSSFetcherForwardsExplicitHTTPProxy(t *testing.T) {
	var proxyRequests int
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests++
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>feed</title></channel></rss>`))
	}))
	defer proxyServer.Close()

	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	resolver := &recordingCustomHTTPClientResolver{client: &http.Client{Transport: transport}}
	connections := fixtureConnectionSource{connections: []configurationdomain.Connection{{
		ID: "custom-feed", Kind: configurationdomain.ConnectionHTTPFeed,
		State: configurationdomain.ConnectionConfigured, NetworkRouteID: "route-custom",
	}}}
	fetcher := ConfiguredCustomRSSFetcher{Connections: connections, ClientResolver: resolver}
	_, err = fetcher.Fetch(context.Background(), subscriptiondomain.RSSLocator{
		URL: "http://203.0.113.10/feed.xml", HTTPFeedConnection: "custom-feed",
	})
	if err != nil {
		t.Fatalf("explicit route feed should succeed through the proxy: %v", err)
	}
	if proxyRequests != 1 {
		t.Fatalf("explicit HTTP proxy received %d request(s), want 1", proxyRequests)
	}
	if len(resolver.seen) != 1 || resolver.seen[0].ID != "custom-feed" || resolver.seen[0].NetworkRouteID != "route-custom" {
		t.Fatalf("custom feed resolved the wrong connection: %+v", resolver.seen)
	}
}

func TestConfiguredCustomRSSFetcherSupportsExplicitRouteWithoutConnection(t *testing.T) {
	resolver := &recordingCustomHTTPClientResolver{}
	fetcher := ConfiguredCustomRSSFetcher{ClientResolver: resolver}
	client, err := fetcher.resolveClient(context.Background(), subscriptiondomain.RSSLocator{URL: "https://example.test/feed.xml", NetworkRoute: "route-only"})
	if err != nil {
		t.Fatal(err)
	}
	if client != nil || len(resolver.seen) != 1 || resolver.seen[0].ID != "custom-rss-route" || resolver.seen[0].NetworkRouteID != "route-only" {
		t.Fatalf("explicit route was not resolved through a typed synthetic connection: client=%v seen=%+v", client, resolver.seen)
	}
}

func TestConfiguredCustomRSSFetcherRejectsImplicitOrConflictingConnection(t *testing.T) {
	fetcher := ConfiguredCustomRSSFetcher{Connections: fixtureConnectionSource{connections: []configurationdomain.Connection{{
		ID: "provider", Kind: configurationdomain.ConnectionMikan,
		State: configurationdomain.ConnectionConfigured,
	}}}}
	if _, err := fetcher.resolveClient(context.Background(), subscriptiondomain.RSSLocator{URL: "https://example.test/feed.xml", HTTPFeedConnection: "provider"}); err == nil {
		t.Fatal("custom RSS must reject a connection from another provider kind")
	}
	fetcher = ConfiguredCustomRSSFetcher{Connections: fixtureConnectionSource{connections: []configurationdomain.Connection{{
		ID: "custom-feed", Kind: configurationdomain.ConnectionHTTPFeed,
		State: configurationdomain.ConnectionConfigured, NetworkRouteID: "route-a",
	}}}}
	if _, err := fetcher.resolveClient(context.Background(), subscriptiondomain.RSSLocator{URL: "https://example.test/feed.xml", HTTPFeedConnection: "custom-feed", NetworkRoute: "route-b"}); err == nil {
		t.Fatal("locator route must not silently override a connection route")
	}
}
