package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	subscriptiondomain "bansoko/internal/subscription/domain"
)

// The rss_http connection no longer carries UA, timeout or byte-size
// configuration; the transport policy is compiled in as constants — a
// browser-disguise user agent, a 15s request timeout, a 10MiB response
// size clamp (the existing hardening defaults).
func TestHTTPRSSFetcherUsesFixedBrowserUserAgent(t *testing.T) {
	var userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel/></rss>`)
	}))
	defer server.Close()
	serverAddress := server.Listener.Addr().String()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
	}

	fetcher := HTTPRSSFetcher{Client: &http.Client{Transport: transport}}
	if _, err := fetcher.Fetch(context.Background(), subscriptiondomain.RSSLocator{URL: "http://203.0.113.10/feed.xml"}); err != nil {
		t.Fatal(err)
	}
	if userAgent != browserDisguiseUserAgent {
		t.Fatalf("expected the fixed browser UA, got %q", userAgent)
	}
}

func TestRSSHTTPFixedTransportConstants(t *testing.T) {
	if browserDisguiseUserAgent != "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36" {
		t.Fatalf("browser-disguise UA drifted from the expected browser string: %q", browserDisguiseUserAgent)
	}
	if rssHTTPRequestTimeout != 15*time.Second {
		t.Fatalf("rss_http request timeout must stay 15s, got %s", rssHTTPRequestTimeout)
	}
}

func TestRSSRedirectRejectsDowngradeAndPrivateTarget(t *testing.T) {
	previous, _ := http.NewRequest(http.MethodGet, "https://feed.example.test/start", nil)
	for _, testCase := range []struct {
		name string
		url  string
	}{
		{name: "downgrade", url: "http://feed.example.test/next"},
		{name: "private", url: "https://127.0.0.1/private"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodGet, testCase.url, nil)
			request.Header.Set("Authorization", "Bearer must-not-survive")
			request.Header.Set("Cookie", "must-not-survive=true")
			if err := validateRSSRedirect(request, []*http.Request{previous}); err == nil {
				t.Fatalf("unsafe redirect %q was accepted", testCase.url)
			}
		})
	}
}

func TestRSSSafetyTransportEvaluatesDynamicProxyOnceOnRealRequest(t *testing.T) {
	var selectorCalls int
	var proxyRequests int
	proxyServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyRequests++
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = func(request *http.Request) (*url.URL, error) {
		selectorCalls++
		if selectorCalls > 1 {
			return proxyURL, nil
		}
		if request.URL.Hostname() != "feed.example.test" {
			t.Fatalf("selector received synthetic request %q", request.URL)
		}
		return nil, nil
	}
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == proxyServer.Listener.Addr().String() {
			proxyRequests++
			return nil, errors.New("unexpected proxy dial")
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			buffer := make([]byte, 4096)
			_, _ = server.Read(buffer)
			_, _ = server.Write([]byte("HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n"))
		}()
		return client, nil
	}
	fetcher := HTTPRSSFetcher{
		Client:   &http.Client{Transport: base},
		Resolver: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.10")}, nil },
	}
	secure, err := fetcher.secureTransport(base)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "http://feed.example.test/rss", nil)
	response, err := secure.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if selectorCalls != 1 {
		t.Fatalf("proxy selector calls = %d, want 1", selectorCalls)
	}
	if proxyRequests != 0 {
		t.Fatalf("proxy received %d request(s)", proxyRequests)
	}
}

func TestRSSSafetyTransportForwardsExplicitHTTPProxyToAllowlistedPrivateTarget(t *testing.T) {
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
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyURL(proxyURL)
	fetcher := HTTPRSSFetcher{
		Client:            &http.Client{Transport: base},
		AllowPrivateHosts: []string{"private.example.test"},
		Resolver:          func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil },
	}
	secure, err := fetcher.secureTransport(base)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "http://private.example.test/rss", nil)
	response, err := secure.RoundTrip(request)
	if err != nil {
		t.Fatalf("explicit route to allowlisted target should succeed through proxy: %v", err)
	}
	response.Body.Close()
	if proxyRequests != 1 {
		t.Fatalf("explicit HTTP proxy received %d request(s), want 1", proxyRequests)
	}
}

func TestRSSSafetyTransportRejectsDNSRebindBeforeRoute(t *testing.T) {
	var resolverCalls int
	var routeCalls int
	transport := rssSafetyTransport{
		resolver: func(context.Context, string) ([]net.IP, error) {
			resolverCalls++
			if resolverCalls == 1 {
				return []net.IP{net.ParseIP("203.0.113.10")}, nil
			}
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
		allowed: map[string]struct{}{},
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			routeCalls++
			return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header)}, nil
		}),
	}
	request, _ := http.NewRequest(http.MethodGet, "https://feed.example.test/rss", nil)
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("first public resolution failed: %v", err)
	}
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("DNS rebind to private address was accepted")
	}
	if resolverCalls != 2 || routeCalls != 1 {
		t.Fatalf("unexpected resolver/route calls: resolver=%d route=%d", resolverCalls, routeCalls)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
