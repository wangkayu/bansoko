package transport

import (
	"encoding/json"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// TestGitHubRouteInputMapsThroughTheRouteUnion proves the new GitHub route kind
// decodes from the discriminated union and keeps its own enabled flag, endpoint
// and credential triple-state.
func TestGitHubRouteInputMapsThroughTheRouteUnion(t *testing.T) {
	var input generated.NetworkRouteInput
	body := `{"kind":"github","name":"Updates proxy","enabled":true,"proxy_url":"http://proxy.internal:8080","username":"route-user","secret":{"mode":"replace","value":"route-secret"}}`
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatal(err)
	}
	mapped, err := NetworkRouteInput(input, "route-1")
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Kind != configurationdomain.NetworkRouteGitHub {
		t.Fatalf("kind = %q, want %q", mapped.Kind, configurationdomain.NetworkRouteGitHub)
	}
	if !mapped.Enabled {
		t.Fatal("enabled = false, want true")
	}
	if mapped.Endpoint != "http://proxy.internal:8080" {
		t.Fatalf("endpoint = %q", mapped.Endpoint)
	}
	if mapped.Secret.Action != configurationdomain.SecretReplace || mapped.Secret.Value != "route-user:route-secret" {
		t.Fatalf("secret = %+v, want a replace mutation of the username and value", mapped.Secret)
	}
}

// TestRouteEnabledFlagsReachTheDomain proves every kind's enabled flag survives
// the mapper. Before this, only the domain default was written, so a disabled
// route could never be stored disabled.
func TestRouteEnabledFlagsReachTheDomain(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"direct", `{"kind":"direct","name":"Off","enabled":false}`},
		{"http_proxy", `{"kind":"http_proxy","name":"Off","enabled":false,"proxy_url":"http://proxy.internal:8080"}`},
		{"socks5", `{"kind":"socks5","name":"Off","enabled":false,"host":"proxy.internal","port":1080}`},
		{"github", `{"kind":"github","name":"Off","enabled":false,"proxy_url":"http://proxy.internal:8080"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var input generated.NetworkRouteInput
			if err := json.Unmarshal([]byte(tc.body), &input); err != nil {
				t.Fatal(err)
			}
			mapped, err := NetworkRouteInput(input, "route-1")
			if err != nil {
				t.Fatal(err)
			}
			if mapped.Enabled {
				t.Fatal("enabled = true, want false")
			}
		})
	}
}

// TestGitHubRouteViewKeepsItsDiscriminator proves a stored GitHub route renders
// as the github union member with its disabled state and stored credential.
func TestGitHubRouteViewKeepsItsDiscriminator(t *testing.T) {
	view, err := NetworkRouteView(configurationdomain.NetworkRoute{
		ID:        "route-1",
		Kind:      configurationdomain.NetworkRouteGitHub,
		Name:      "Updates proxy",
		Endpoint:  "http://proxy.internal:8080",
		SecretRef: "secret-ref",
		State:     configurationdomain.ConnectionDisabled,
		Version:   4,
	})
	if err != nil {
		t.Fatal(err)
	}
	kind, err := view.Discriminator()
	if err != nil {
		t.Fatal(err)
	}
	if kind != "github" {
		t.Fatalf("discriminator = %q, want github", kind)
	}
	route, err := view.AsGitHubRoute()
	if err != nil {
		t.Fatal(err)
	}
	if route.Enabled {
		t.Fatal("enabled = true, want false for a disabled route")
	}
	if !route.CredentialConfigured {
		t.Fatal("credential_configured = false, want true")
	}
	if route.ProxyUrl != "http://proxy.internal:8080" {
		t.Fatalf("proxy_url = %q", route.ProxyUrl)
	}
}
