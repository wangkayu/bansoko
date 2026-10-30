package application

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/apperrors"
	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/observability/outbound"
)

// routeSourceHost is a host name the test process cannot resolve. Every update
// request that does not travel through the GitHub route fails at DNS, so a
// successful operation is evidence the proxy carried it and the test never has
// to trust a "no direct connection" claim.
const routeSourceHost = "github-route.invalid"

// proxyRequest is one request a forward proxy actually carried.
type proxyRequest struct {
	method   string
	url      string
	absolute bool
}

// routeForwardProxy is a plain HTTP forward proxy. It records every request it
// receives and replays it against the mock package host, which is how the test
// proves the update chain spoke to the proxy instead of the origin.
type routeForwardProxy struct {
	mu       sync.Mutex
	requests []proxyRequest
	server   *httptest.Server
	target   string
}

func newRouteForwardProxy(t *testing.T, target string) *routeForwardProxy {
	t.Helper()
	proxy := &routeForwardProxy{target: target}
	proxy.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.mu.Lock()
		proxy.requests = append(proxy.requests, proxyRequest{method: r.Method, url: r.URL.String(), absolute: r.URL.IsAbs()})
		proxy.mu.Unlock()
		if !r.URL.IsAbs() {
			http.Error(w, "forward proxy requires an absolute request URI", http.StatusBadRequest)
			return
		}
		target := *r.URL
		target.Scheme = "http"
		target.Host = proxy.target
		forwarded, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), nil)
		if err != nil {
			http.Error(w, "forward proxy cannot rebuild the request", http.StatusBadGateway)
			return
		}
		response, err := (&http.Transport{}).RoundTrip(forwarded)
		if err != nil {
			http.Error(w, "forward proxy cannot reach the origin", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(proxy.server.Close)
	return proxy
}

func (p *routeForwardProxy) client() *http.Client {
	proxyURL := &url.URL{Scheme: "http", Host: strings.TrimPrefix(p.server.URL, "http://")}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
}

func (p *routeForwardProxy) snapshot() []proxyRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]proxyRequest(nil), p.requests...)
}

func (p *routeForwardProxy) requestedPaths() []string {
	var paths []string
	for _, request := range p.snapshot() {
		parsed, err := url.Parse(request.url)
		if err != nil {
			paths = append(paths, request.url)
			continue
		}
		paths = append(paths, parsed.Path)
	}
	return paths
}

// stubGitHubRoute is the configuration service as the update channel sees it:
// one resolution per operation, answered from a queue so a test can change the
// route between operations exactly as saving settings would.
type stubGitHubRoute struct {
	mu       sync.Mutex
	calls    int
	answers  []routeAnswer
	fallback routeAnswer
}

type routeAnswer struct {
	client *http.Client
	err    error
}

func (s *stubGitHubRoute) UpdateRouteClient(context.Context) (*http.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.answers) == 0 {
		return s.fallback.client, s.fallback.err
	}
	answer := s.answers[0]
	s.answers = s.answers[1:]
	return answer.client, answer.err
}

func (s *stubGitHubRoute) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// routeSourceRig is one mock package host plus the signed channel document and
// manifest that name it. host is the name the document and package root use:
// the unroutable route host in proxy tests, the server's own address when the
// test exercises the default egress.
type routeSourceRig struct {
	rig         *channelCounterexampleRig
	documentURL string
	packageRoot string
	manifestURL string
}

func newRouteSourceRig(t *testing.T, privateKey ed25519.PrivateKey, host string) *routeSourceRig {
	t.Helper()
	payloadBytes := []byte("channel payload fixture")
	payloadDigest := hardeningdomain.HashArtifact(payloadBytes)
	payloadSignature, err := hardeningdomain.SignArtifactDigest(privateKey, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	manifest := signedChannelManifestFixture(payloadDigest, payloadSignature)
	manifest.EmbeddedUpdate.PayloadSize = int64(len(payloadBytes))
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	rig := newChannelCounterexampleRig(t, manifestBody, nil)
	if host == "" {
		host = strings.TrimPrefix(rig.recording.URL(), "http://")
	}
	source := &routeSourceRig{
		rig:         rig,
		documentURL: "http://" + host + channelCounterexampleDocumentPath,
		packageRoot: "http://" + host + "/packages",
		manifestURL: "http://" + host + channelCounterexampleManifestPath,
	}
	rig.setDocumentBody(channelDocumentFixture(t, privateKey, source.manifestURL, digest, manifest.ReleaseID, nil))
	return source
}

func (r *routeSourceRig) originHost(t *testing.T) string {
	t.Helper()
	return strings.TrimPrefix(r.rig.recording.URL(), "http://")
}

func (r *routeSourceRig) service(t *testing.T, publicKey ed25519.PublicKey, route GitHubRouteResolver) *Service {
	t.Helper()
	service, err := NewService(Deps{
		UpdateMode:            "embedded",
		PublicKey:             publicKey,
		UpdatesRoot:           t.TempDir(),
		ChannelURL:            r.documentURL,
		PackageRoot:           r.packageRoot,
		GitHubRoute:           route,
		BuildReleaseID:        "release-running",
		BuildVersion:          "v1.0.0",
		BuildBootstrapVersion: "embedded-bootstrap-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// TestUpdateCheckUsesTheEnabledGitHubRoute proves the check path resolves the
// route once per operation and reaches the channel document and signed manifest
// only through it: the origin host does not resolve, so a direct attempt fails.
func TestUpdateCheckUsesTheEnabledGitHubRoute(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	route := &stubGitHubRoute{}
	source := newRouteSourceRig(t, privateKey, routeSourceHost)
	proxy := newRouteForwardProxy(t, source.originHost(t))
	route.fallback = routeAnswer{client: proxy.client()}
	service := source.service(t, publicKey, route)

	result, err := service.checkUpdate(context.Background())
	if err != nil {
		t.Fatalf("checkUpdate error = %v", err)
	}
	if result.Status != "update_available" {
		t.Fatalf("check status = %q, want update_available", result.Status)
	}
	if got := route.callCount(); got != 1 {
		t.Fatalf("route resolutions = %d, want exactly one per operation", got)
	}
	want := []string{channelCounterexampleDocumentPath, channelCounterexampleManifestPath}
	if got := proxy.requestedPaths(); !equalStrings(got, want) {
		t.Fatalf("proxy carried %v, want %v", got, want)
	}
	for _, request := range proxy.snapshot() {
		if !request.absolute {
			t.Fatalf("proxy request %s was not an absolute-form proxy request", request.url)
		}
		parsed, err := url.Parse(request.url)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Host != routeSourceHost {
			t.Fatalf("proxy request host = %q, want %q", parsed.Host, routeSourceHost)
		}
	}
	if got := len(source.rig.recording.snapshot()); got != 2 {
		t.Fatalf("origin saw %d requests, want 2 (relayed by the proxy)", got)
	}
}

// TestAdmittedUpdateDownloadsThePayloadThroughTheSameRoute proves the payload
// leg of an admission uses the source resolved once for that operation, so the
// channel document, manifest and payload all travel through the same route.
func TestAdmittedUpdateDownloadsThePayloadThroughTheSameRoute(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	route := &stubGitHubRoute{}
	source := newRouteSourceRig(t, privateKey, routeSourceHost)
	proxy := newRouteForwardProxy(t, source.originHost(t))
	route.fallback = routeAnswer{client: proxy.client()}
	service := source.service(t, publicKey, route)

	ctx := context.Background()
	client, err := service.operationClient(ctx)
	if err != nil {
		t.Fatalf("operationClient error = %v", err)
	}
	resolved, err := service.resolve(ctx, client)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	payload, err := service.ensurePayload(ctx, resolved.source, "embedded-op-route", resolved.manifest.EmbeddedUpdate)
	if err != nil {
		t.Fatalf("ensurePayload error = %v", err)
	}
	if payload == "" {
		t.Fatal("ensurePayload returned no path")
	}
	want := []string{channelCounterexampleDocumentPath, channelCounterexampleManifestPath, channelCounterexamplePayloadPath}
	if got := proxy.requestedPaths(); !equalStrings(got, want) {
		t.Fatalf("proxy carried %v, want %v", got, want)
	}
}

// TestEnabledGitHubRouteFailureFailsClosed proves a route that cannot be built
// stops the operation instead of silently going direct.
func TestEnabledGitHubRouteFailureFailsClosed(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	source := newRouteSourceRig(t, privateKey, routeSourceHost)
	route := &stubGitHubRoute{fallback: routeAnswer{err: errors.New("stored route is not usable")}}
	service := source.service(t, publicKey, route)

	result, err := service.checkUpdate(context.Background())
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "UPDATE_ROUTE_INVALID" {
		t.Fatalf("checkUpdate error = %v, want UPDATE_ROUTE_INVALID", err)
	}
	if result.Status != "check_failed" {
		t.Fatalf("check status = %q, want check_failed", result.Status)
	}
	if got := len(source.rig.recording.snapshot()); got != 0 {
		t.Fatalf("origin saw %d requests, want 0: the update fell back to a direct call", got)
	}
}

// TestUnconfiguredGitHubRouteKeepsTheDeploymentDefault proves an absent route
// leaves the update channel on its existing configuration: the same check
// succeeds over the deployment default, and the resolver reporting "no route"
// is not turned into a hidden proxy decision.
func TestUnconfiguredGitHubRouteKeepsTheDeploymentDefault(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	source := newRouteSourceRig(t, privateKey, "")
	route := &stubGitHubRoute{}
	service := source.service(t, publicKey, route)

	result, err := service.checkUpdate(context.Background())
	if err != nil {
		t.Fatalf("checkUpdate error = %v", err)
	}
	if result.Status != "update_available" {
		t.Fatalf("check status = %q, want update_available", result.Status)
	}
	if got := route.callCount(); got != 1 {
		t.Fatalf("route resolutions = %d, want 1", got)
	}
}

// TestRouteChangesApplyToTheNextOperation proves the route is read per
// operation: saving a new route takes effect on the next check without a
// restart, and a new route never rewrites an operation already in flight.
func TestRouteChangesApplyToTheNextOperation(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	source := newRouteSourceRig(t, privateKey, routeSourceHost)
	first := newRouteForwardProxy(t, source.originHost(t))
	second := newRouteForwardProxy(t, source.originHost(t))
	route := &stubGitHubRoute{answers: []routeAnswer{{client: first.client()}, {client: second.client()}}}
	service := source.service(t, publicKey, route)

	ctx := context.Background()
	if _, err := service.checkUpdate(ctx); err != nil {
		t.Fatalf("first checkUpdate error = %v", err)
	}
	if _, err := service.checkUpdate(ctx); err != nil {
		t.Fatalf("second checkUpdate error = %v", err)
	}
	if got := len(first.snapshot()); got != 2 {
		t.Fatalf("first route carried %d requests, want 2 (its own operation only)", got)
	}
	if got := len(second.snapshot()); got != 2 {
		t.Fatalf("second route carried %d requests, want 2", got)
	}
	if got := route.callCount(); got != 2 {
		t.Fatalf("route resolutions = %d, want one per operation", got)
	}
}

// TestUpdateSourceReportsTheUnderlyingProxyRoute proves the outbound
// diagnostic sees the proxy beneath the update source wrapper instead of
// reporting every update call as direct.
func TestUpdateSourceReportsTheUnderlyingProxyRoute(t *testing.T) {
	proxyURL, err := url.Parse("http://proxy.internal:3128")
	if err != nil {
		t.Fatal(err)
	}
	routeClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: "http://updates.internal/packages/v1/release-manifest.json",
		HTTPClient:  routeClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := source.NewRequest(context.Background(), http.MethodGet, source.ManifestURL())
	if err != nil {
		t.Fatal(err)
	}
	if got := outbound.ViaOf(source.Client(), request); got != outbound.ViaProxy {
		t.Fatalf("outbound route = %q, want %q", got, outbound.ViaProxy)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
