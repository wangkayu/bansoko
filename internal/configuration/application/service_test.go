package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"bansoko/internal/configuration/domain"
	"bansoko/internal/jackett"
	"bansoko/internal/persistence/txscope"
)

type testResolver map[string]string

func (r testResolver) Resolve(ref string) (string, bool) { value, ok := r[ref]; return value, ok }

func TestMemorySecretStoreLifecycle(t *testing.T) {
	store := NewMemorySecretStore()
	ref, err := store.Put(context.Background(), "qbittorrent", "secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if ref == "" || len(ref) < len("mem:")+16 {
		t.Fatalf("secret reference is too short: %q", ref)
	}
	if value, ok := store.Resolve(ref); !ok || value != "secret-value" {
		t.Fatalf("secret was not resolvable: value=%q ok=%v", value, ok)
	}
	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Resolve(ref); ok {
		t.Fatal("deleted secret must not remain resolvable")
	}
}

func TestMemorySecretStoreRejectsInvalidInput(t *testing.T) {
	store := NewMemorySecretStore()
	if _, err := store.Put(context.Background(), "", "value"); err == nil {
		t.Fatal("secret purpose is required")
	}
	if _, err := store.Put(context.Background(), "purpose", ""); err == nil {
		t.Fatal("secret value is required")
	}
}

func TestMemorySecretStoreTransactionalMethodsIgnoreScope(t *testing.T) {
	store := NewMemorySecretStore()
	ref, err := store.PutTx(context.Background(), txscope.Tx{}, "purpose", "value")
	if err != nil {
		t.Fatalf("PutTx: %v", err)
	}
	if got, ok := store.Resolve(ref); !ok || got != "value" {
		t.Fatalf("PutTx did not persist value: got=%q ok=%v", got, ok)
	}
	if err := store.DeleteTx(context.Background(), txscope.Tx{}, ref); err != nil {
		t.Fatalf("DeleteTx: %v", err)
	}
	if _, ok := store.Resolve(ref); ok {
		t.Fatal("DeleteTx did not remove value")
	}
}

func TestProviderConnectionTesterQBittorrent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/auth/login" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("Ok."))
	}))
	defer server.Close()

	tester := ProviderConnectionTester{ResolveSecret: testResolver{"mem:password": "secret"}}
	snapshot := tester.Test(context.Background(), domain.Connection{ID: "qb", Kind: domain.ConnectionQBittorrent, Endpoint: server.URL, QBittorrentConfig: &domain.QBittorrentConnectionConfig{Username: "admin"}, SecretRef: "mem:password"})
	if snapshot.State != domain.ConnectionConfigured || snapshot.Code != "ok" {
		t.Fatalf("unexpected health snapshot: %+v", snapshot)
	}
	missing := tester.Test(context.Background(), domain.Connection{ID: "qb", Kind: domain.ConnectionQBittorrent, Endpoint: server.URL, QBittorrentConfig: &domain.QBittorrentConnectionConfig{Username: "admin"}})
	if missing.Code != "missing_credentials" {
		t.Fatalf("missing secret should be explicit: %+v", missing)
	}
}

func TestProviderConnectionTesterTMDB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "tmdb-secret" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()

	tester := ProviderConnectionTester{ResolveSecret: testResolver{"tmdb-ref": "tmdb-secret"}}
	snapshot := tester.Test(context.Background(), domain.Connection{ID: "tmdb", Kind: domain.ConnectionTMDB, Endpoint: server.URL, SecretRef: "tmdb-ref"})
	if snapshot.State != domain.ConnectionConfigured || snapshot.Code != "ok" {
		t.Fatalf("unexpected TMDB health snapshot: %+v", snapshot)
	}
	missing := tester.Test(context.Background(), domain.Connection{ID: "tmdb", Kind: domain.ConnectionTMDB, Endpoint: server.URL})
	if missing.Code != "missing_credentials" {
		t.Fatalf("missing TMDB secret should be explicit: %+v", missing)
	}
}

func TestProviderConnectionTesterJackett(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != jackett.AggregateTorznabPath || r.URL.Query().Get("t") != "caps" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("apikey") != "jackett-secret" {
			_, _ = w.Write([]byte(`<error code="100" description="unauthorized"/>`))
			return
		}
		_, _ = w.Write([]byte(`<caps><server title="Jackett"/></caps>`))
	}))
	defer server.Close()

	tester := ProviderConnectionTester{ResolveSecret: testResolver{"jackett-ref": "jackett-secret"}}
	snapshot := tester.Test(context.Background(), domain.Connection{ID: "jackett", Kind: domain.ConnectionJackett, Endpoint: server.URL, SecretRef: "jackett-ref"})
	if snapshot.State != domain.ConnectionConfigured || snapshot.Code != "ok" {
		t.Fatalf("unexpected Jackett health snapshot: %+v", snapshot)
	}
	missing := tester.Test(context.Background(), domain.Connection{ID: "jackett", Kind: domain.ConnectionJackett, Endpoint: server.URL})
	if missing.Code != "missing_credentials" {
		t.Fatalf("missing Jackett secret should be explicit: %+v", missing)
	}
	rejected := (ProviderConnectionTester{ResolveSecret: testResolver{"jackett-ref": "wrong-secret"}}).Test(
		context.Background(), domain.Connection{ID: "jackett", Kind: domain.ConnectionJackett, Endpoint: server.URL, SecretRef: "jackett-ref"},
	)
	if rejected.Code != "authentication_failed" {
		t.Fatalf("Jackett authentication failure should be explicit: %+v", rejected)
	}
}

func TestProviderConnectionTesterJackettRedirectDoesNotLeakCredentialURL(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if referer := r.Header.Get("Referer"); referer != "" {
			t.Errorf("redirect forwarded Referer: %q", referer)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("redirect forwarded ambient credentials")
		}
		_, _ = w.Write([]byte(`<caps><server title="Jackett"/></caps>`))
	}))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/caps", http.StatusFound)
	}))
	defer upstream.Close()

	tester := ProviderConnectionTester{ResolveSecret: testResolver{"jackett-ref": "jackett-secret"}}
	snapshot := tester.Test(context.Background(), domain.Connection{ID: "jackett", Kind: domain.ConnectionJackett, Endpoint: upstream.URL, SecretRef: "jackett-ref"})
	if snapshot.State != domain.ConnectionConfigured || snapshot.Code != "ok" {
		t.Fatalf("unexpected redirected Jackett health snapshot: %+v", snapshot)
	}
}

func TestProviderConnectionTesterAniBT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	snapshot := (ProviderConnectionTester{}).Test(context.Background(), domain.Connection{ID: "anibt", Kind: domain.ConnectionAniBT, Endpoint: server.URL + "/health"})
	if snapshot.State != domain.ConnectionConfigured || snapshot.Code != "ok" {
		t.Fatalf("unexpected AniBT health snapshot: %+v", snapshot)
	}
}

type staticHTTPClientResolver struct {
	client *http.Client
}

func (r staticHTTPClientResolver) ClientForConnection(context.Context, domain.Connection) (*http.Client, error) {
	return r.client, nil
}

type countingHTTPClientResolver struct {
	client *http.Client
	err    error
	calls  int
}

func (r *countingHTTPClientResolver) ClientForConnection(context.Context, domain.Connection) (*http.Client, error) {
	r.calls++
	return r.client, r.err
}

func TestProviderConnectionTesterUsesConfiguredNetworkRoute(t *testing.T) {
	var proxyRequests int
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests++
		if r.URL.Scheme != "http" || r.URL.Host == "" {
			t.Fatalf("proxy did not receive an absolute HTTP request: %s", r.URL.String())
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()

	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: transport}
	tester := ProviderConnectionTester{ClientResolver: staticHTTPClientResolver{client: client}}
	snapshot := tester.Test(context.Background(), domain.Connection{
		ID: "mikan-route", Kind: domain.ConnectionMikan, Endpoint: "http://provider.invalid/health", NetworkRouteID: "route-1",
	})
	if snapshot.State != domain.ConnectionConfigured || snapshot.Code != "ok" {
		t.Fatalf("unexpected routed Mikan health snapshot: %+v", snapshot)
	}
	if proxyRequests != 1 {
		t.Fatalf("expected one request through configured route, got %d", proxyRequests)
	}

	missingResolver := ProviderConnectionTester{}
	failed := missingResolver.Test(context.Background(), domain.Connection{
		ID: "mikan-route-missing", Kind: domain.ConnectionMikan, Endpoint: "http://provider.invalid/health", NetworkRouteID: "route-1",
	})
	if failed.State != domain.ConnectionDegraded || failed.Code != "network_route_invalid" {
		t.Fatalf("missing route resolver should be explicit: %+v", failed)
	}
}

func TestProviderConnectionTesterRejectsNilResolvedClientWithoutRetry(t *testing.T) {
	resolver := &countingHTTPClientResolver{}
	tester := ProviderConnectionTester{ClientResolver: resolver}
	snapshot := tester.Test(context.Background(), domain.Connection{
		ID: "mikan-route-nil", Kind: domain.ConnectionMikan, Endpoint: "http://provider.invalid/health", NetworkRouteID: "route-1",
	})
	if resolver.calls != 1 {
		t.Fatalf("nil route client should be resolved once, got %d calls", resolver.calls)
	}
	if snapshot.State != domain.ConnectionDegraded || snapshot.Code != "network_route_invalid" {
		t.Fatalf("nil route client should be an explicit route failure: %+v", snapshot)
	}
}

func TestProviderConnectionTesterHTTPFeedAuthRequiresResolver(t *testing.T) {
	tester := ProviderConnectionTester{}
	snapshot := tester.Test(context.Background(), domain.Connection{
		ID: "http-feed-auth-missing-resolver", Kind: domain.ConnectionHTTPFeed,
		Endpoint: "https://feed.example.test/rss.xml", SecretRef: "feed-secret",
		HTTPFeedConfig: &domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthBearer},
	})
	if snapshot.State != domain.ConnectionDegraded || snapshot.Code != "network_route_invalid" {
		t.Fatalf("authenticated HTTP feed must not fall back to the default client: %+v", snapshot)
	}
}

func TestBuildNetworkRouteClientUsesConfiguredHTTPProxyAndSecret(t *testing.T) {
	store := NewMemorySecretStore()
	store.values["route-secret"] = "proxy-user:proxy-password"
	service := &Service{secrets: store}
	client, err := service.buildNetworkRouteClient(networkRouteRecord{
		Kind: "http_proxy", Endpoint: "http://proxy.example.test:8080", SecretRef: "route-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatal("proxy route did not configure an HTTP transport proxy")
	}
	request, err := http.NewRequest(http.MethodGet, "https://provider.example.test/feed", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := transport.Proxy(request)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.Host != "proxy.example.test:8080" || proxyURL.User == nil {
		t.Fatalf("unexpected proxy URL: %v", proxyURL)
	}
	password, _ := proxyURL.User.Password()
	if proxyURL.User.Username() != "proxy-user" || password != "proxy-password" {
		t.Fatalf("proxy credentials were not resolved: %v", proxyURL.User)
	}
}

func TestBuildNetworkRouteClientRejectsUnsupportedRoute(t *testing.T) {
	service := &Service{}
	if _, err := service.buildNetworkRouteClient(networkRouteRecord{Kind: "unknown", Endpoint: "http://proxy.example.test"}); err == nil {
		t.Fatal("unsupported network route kind should fail explicitly")
	}
}

func TestHTTPFeedAuthTransportUsesSelectedMode(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		config domain.HTTPFeedConnectionConfig
		secret string
		check  func(*http.Request) bool
	}{
		{name: "basic", config: domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthBasic, Username: "feed-user"}, secret: "feed-password", check: func(request *http.Request) bool {
			username, password, ok := request.BasicAuth()
			return ok && username == "feed-user" && password == "feed-password"
		}},
		{name: "bearer", config: domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthBearer}, secret: "feed-token", check: func(request *http.Request) bool {
			return request.Header.Get("Authorization") == "Bearer feed-token"
		}},
		{name: "cookie", config: domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthCookie}, secret: "session=feed", check: func(request *http.Request) bool {
			return request.Header.Get("Cookie") == "session=feed"
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewMemorySecretStore()
			store.values["feed-secret"] = testCase.secret
			service := &Service{secrets: store}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if !testCase.check(request) {
					t.Errorf("HTTP feed auth headers were not applied: Authorization=%q Cookie=%q", request.Header.Get("Authorization"), request.Header.Get("Cookie"))
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			client, err := service.applyHTTPFeedAuth(context.Background(), domain.Connection{
				Kind: domain.ConnectionHTTPFeed, SecretRef: "feed-secret", HTTPFeedConfig: &testCase.config,
			}, &http.Client{})
			if err != nil {
				t.Fatal(err)
			}
			target, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			auth := client.Transport.(httpFeedAuthTransport)
			client.Transport = auth.WithRSSRequestOrigin(auth.RSSBaseTransport(), normalizedHTTPOrigin(target))
			response, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("unexpected authenticated feed status: %d", response.StatusCode)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestHTTPFeedAuthTransportUsesExactNormalizedOrigin(t *testing.T) {
	var seen []*http.Request
	base := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		seen = append(seen, request.Clone(request.Context()))
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	})
	transport := httpFeedAuthTransport{
		base: base, mode: domain.HTTPFeedAuthBearer, secret: "feed-token",
		origin: "https://feed.example.test:443",
	}
	for _, raw := range []string{
		"https://FEED.EXAMPLE.TEST/next",
		"https://feed.example.test:443/next",
		"https://feed.example.test:444/next",
		"https://other.example.test/next",
		"http://feed.example.test/next",
	} {
		request, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer inherited")
		request.Header.Set("Cookie", "inherited=cookie")
		if _, err := transport.RoundTrip(request); err != nil {
			t.Fatal(err)
		}
	}
	for index, request := range seen {
		wantAuth := index < 2
		if got := request.Header.Get("Authorization"); (got == "Bearer feed-token") != wantAuth {
			t.Fatalf("request %d Authorization=%q wantAuth=%v", index, got, wantAuth)
		}
		if request.Header.Get("Cookie") != "" {
			t.Fatalf("request %d retained a cookie across auth transport: %q", index, request.Header.Get("Cookie"))
		}
	}
}

func TestHTTPFeedAuthTransportRejectsImplicitOrMissingCredentials(t *testing.T) {
	store := NewMemorySecretStore()
	store.values["feed-secret"] = "token"
	service := &Service{secrets: store}
	if _, err := service.applyHTTPFeedAuth(context.Background(), domain.Connection{Kind: domain.ConnectionHTTPFeed, SecretRef: "feed-secret", HTTPFeedConfig: &domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthNone}}, &http.Client{}); err == nil {
		t.Fatal("HTTP feed secret was accepted without an explicit auth mode")
	}
	if _, err := service.applyHTTPFeedAuth(context.Background(), domain.Connection{Kind: domain.ConnectionHTTPFeed, HTTPFeedConfig: &domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthBearer}}, &http.Client{}); err == nil {
		t.Fatal("HTTP feed auth was accepted without a configured secret")
	}
	store.values["bad-header"] = "token\nmalicious"
	if _, err := service.applyHTTPFeedAuth(context.Background(), domain.Connection{Kind: domain.ConnectionHTTPFeed, SecretRef: "bad-header", HTTPFeedConfig: &domain.HTTPFeedConnectionConfig{AuthMode: domain.HTTPFeedAuthCookie}}, &http.Client{}); err == nil {
		t.Fatal("HTTP feed header injection was accepted")
	}
}
