package application

import (
	"net/http"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
)

// TestUpdateRouteClientSelectsOnlyOneEnabledGitHubRoute pins the selection rule
// the update channel relies on: no row or a disabled row keeps the deployment
// default, a single configured row becomes the update client, and a store that
// somehow holds two rows is refused instead of guessed.
func TestUpdateRouteClientSelectsOnlyOneEnabledGitHubRoute(t *testing.T) {
	service := &Service{}
	configured := networkRouteRecord{Kind: "github", Endpoint: "http://proxy.example.test:8080", State: string(configurationdomain.ConnectionConfigured)}
	disabled := configured
	disabled.State = string(configurationdomain.ConnectionDisabled)

	client, err := service.updateRouteClient(nil)
	if err != nil || client != nil {
		t.Fatalf("no route = %v, %v; want nil, nil", client, err)
	}
	client, err = service.updateRouteClient([]networkRouteRecord{disabled})
	if err != nil || client != nil {
		t.Fatalf("disabled route = %v, %v; want nil, nil", client, err)
	}
	client, err = service.updateRouteClient([]networkRouteRecord{configured, configured})
	if err == nil || client != nil {
		t.Fatalf("two routes = %v, %v; want an error", client, err)
	}

	client, err = service.updateRouteClient([]networkRouteRecord{configured})
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != updateRouteTimeout {
		t.Fatalf("timeout = %v, want %v", client.Timeout, updateRouteTimeout)
	}
	// The client is a forward proxy for both update target schemes: net/http
	// sends absolute-form requests to HTTP endpoints and CONNECT to HTTPS ones.
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatal("GitHub route did not configure an HTTP transport proxy")
	}
	for _, target := range []string{
		"http://raw.githubusercontent.com/example/channel/channel.json",
		"https://raw.githubusercontent.com/example/channel/manifest.json",
	} {
		request, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		proxyURL, err := transport.Proxy(request)
		if err != nil {
			t.Fatal(err)
		}
		if proxyURL == nil || proxyURL.Host != "proxy.example.test:8080" {
			t.Fatalf("proxy for %s = %v, want proxy.example.test:8080", target, proxyURL)
		}
	}
}

// TestUpdateRouteClientFailsClosedOnAnUnusableRoute proves a saved route that
// cannot build a working client is an error rather than a silent direct call.
func TestUpdateRouteClientFailsClosedOnAnUnusableRoute(t *testing.T) {
	service := &Service{}
	if _, err := service.updateRouteClient([]networkRouteRecord{{
		Kind: "github", Endpoint: "socks5://proxy.example.test:1080", State: string(configurationdomain.ConnectionConfigured),
	}}); err == nil {
		t.Fatal("GitHub route with a SOCKS5 endpoint should fail explicitly")
	}
	if _, err := service.updateRouteClient([]networkRouteRecord{{
		Kind: "github", Endpoint: "http://proxy.example.test:8080", SecretRef: "missing-secret", State: string(configurationdomain.ConnectionConfigured),
	}}); err == nil {
		t.Fatal("GitHub route with unresolvable credentials should fail explicitly")
	}
}
