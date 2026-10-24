package qbittorrent

import (
	"strings"
	"testing"
)

func baseRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		ConnectionID:      "qb-main",
		ConnectionVersion: 7,
		Endpoint:          "http://127.0.0.1:8080",
		Username:          "admin",
		Password:          "super-secret",
		SaveRoot:          "/downloads",
		ApplicationRoot:   "",
	}
}

func TestNamespaceLocationStripsCredentialsFromTheEndpoint(t *testing.T) {
	config := baseRuntimeConfig()
	config.Endpoint = "http://admin:super-secret@127.0.0.1:8080/qb"
	location := NewNamespaceLocation(config)
	if location.Endpoint != "http://127.0.0.1:8080/qb" {
		t.Fatalf("endpoint must be credential-free: %q", location.Endpoint)
	}
	if strings.Contains(location.Endpoint, "super-secret") {
		t.Fatal("the endpoint anchor must never carry a credential")
	}
}

func TestNamespaceDigestIgnoresARotatedCredential(t *testing.T) {
	before := baseRuntimeConfig()
	after := baseRuntimeConfig()
	after.Password = "a-brand-new-password"
	if NewNamespaceLocation(before).Digest() != NewNamespaceLocation(after).Digest() {
		t.Fatal("rotating a credential must not rename the provider namespace")
	}
}

func TestNamespaceDigestChangesWhenTheEndpointIsRepointed(t *testing.T) {
	before := baseRuntimeConfig()
	after := baseRuntimeConfig()
	after.Endpoint = "http://127.0.0.1:9090"
	if NewNamespaceLocation(before).Digest() == NewNamespaceLocation(after).Digest() {
		t.Fatal("a new endpoint must not keep the old namespace identity")
	}
}

func TestNamespaceDigestChangesWhenTheConnectionIsReconfigured(t *testing.T) {
	before := baseRuntimeConfig()
	after := baseRuntimeConfig()
	after.ConnectionVersion = before.ConnectionVersion + 1
	if NewNamespaceLocation(before).Digest() == NewNamespaceLocation(after).Digest() {
		t.Fatal("reconfiguring the same connection must name a new namespace")
	}
}

func TestNamespaceDigestChangesWhenThePathMappingChanges(t *testing.T) {
	before := baseRuntimeConfig()
	after := baseRuntimeConfig()
	after.ApplicationRoot = "/srv/downloads"
	if NewNamespaceLocation(before).Digest() == NewNamespaceLocation(after).Digest() {
		t.Fatal("the path mapping is part of the namespace")
	}
}

// TestNamespaceLocationIsReconcilableAfterCompaction pins EF 4.2's requirement
// that the anchor not be flattened into an irreversible digest: the saved
// location still names the endpoint, the mapping and the trusted reference.
func TestNamespaceLocationIsReconcilableAfterCompaction(t *testing.T) {
	config := baseRuntimeConfig()
	config.ApplicationRoot = "/srv/downloads"
	location := NewNamespaceLocation(config)
	if location.Endpoint == "" || location.SaveRoot == "" || location.ApplicationRoot == "" {
		t.Fatalf("location must stay reconcilable: %+v", location)
	}
	if location.ConnectionRef != "qb-main" || location.ConnectionVersion != 7 {
		t.Fatalf("location must name the trusted connection reference and version: %+v", location)
	}
}

func TestNamespaceDigestIsStableForTheSameNamespace(t *testing.T) {
	first := NewNamespaceLocation(baseRuntimeConfig()).Digest()
	second := NewNamespaceLocation(baseRuntimeConfig()).Digest()
	if first != second {
		t.Fatalf("digest must be stable: %s vs %s", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("digest must be a sha256 reference: %s", first)
	}
}

func TestCredentialFreeEndpointRefusesToGuessAMalformedURL(t *testing.T) {
	for _, endpoint := range []string{"", "not-a-url", "//host-only"} {
		if got := credentialFreeEndpoint(endpoint); got != "" {
			t.Fatalf("credentialFreeEndpoint(%q) = %q, want empty", endpoint, got)
		}
	}
}
