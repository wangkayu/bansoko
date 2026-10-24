package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/update/channel"
)

// TestChannelResolverAuthenticatesDocumentAndManifestWithGiteaPAT proves the
// discovery path uses the same single PAT identity as the package source: the
// channel document and the manifest it names are both fetched with
// `Authorization: token <PAT>` when the document lives on the package host.
func TestChannelResolverAuthenticatesDocumentAndManifestWithGiteaPAT(t *testing.T) {
	const pat = "gitea-channel-pat-test-only"
	const wantAuthorization = "token " + pat

	payload := []byte("channel payload fixture")
	publicKey, privateKey, err := hardeningdomain.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := hardeningdomain.HashArtifact(payload)
	payloadSignature, err := hardeningdomain.SignArtifactDigest(privateKey, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	manifest := signedChannelManifestFixture(payloadDigest, payloadSignature)
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := manifest.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}

	type observation struct {
		path          string
		authorization string
	}
	var mu sync.Mutex
	var seen []observation
	var documentBytes []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, observation{path: r.URL.Path, authorization: r.Header.Get("Authorization")})
		mu.Unlock()
		switch r.URL.Path {
		case "/packages/channel/channel-v1.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(documentBytes)
		case "/packages/v1/release-manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(manifestBody)
		case "/packages/v1/" + PayloadFileName:
			_, _ = w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	document := channel.Document{
		SchemaVersion:   channel.SchemaVersion,
		Channel:         string(hardeningdomain.ReleaseChannelStable),
		ReleaseID:       manifest.ReleaseID,
		Version:         manifest.Version,
		ReleaseSequence: manifest.ReleaseSequence,
		ManifestURL:     server.URL + "/packages/v1/release-manifest.json",
		ManifestDigest:  manifestDigest,
		SourceCommit:    manifest.SourceCommit,
		PublishedAt:     time.Now().UTC(),
	}
	if err := document.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	documentBytes, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	credentialFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credentialFile, []byte(pat+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := newChannelResolver(
		server.URL+"/packages/channel/channel-v1.json",
		server.URL+"/packages",
		UpdateHTTPSourceConfig{
			AuthMode:       UpdateAuthModeGiteaPAT,
			CredentialFile: credentialFile,
		},
		publicKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.resolve(context.Background())
	if err != nil {
		t.Fatalf("channel resolve failed: %v", err)
	}
	if resolved.manifest.ReleaseID != manifest.ReleaseID {
		t.Fatalf("resolved release = %q, want %q", resolved.manifest.ReleaseID, manifest.ReleaseID)
	}

	mu.Lock()
	got := append([]observation(nil), seen...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("channel requests = %v, want the document and the manifest", got)
	}
	for _, o := range got {
		if o.authorization != wantAuthorization {
			t.Fatalf("request %s carried Authorization %q, want %q", o.path, o.authorization, wantAuthorization)
		}
	}
}

// TestChannelResolverKeepsPatOffTheDocumentHost locks the host scoping: a
// channel document served on a different host is fetched unauthenticated, so
// the deployment PAT is never sent anywhere but the configured package host.
func TestChannelResolverKeepsPatOffTheDocumentHost(t *testing.T) {
	const pat = "gitea-channel-pat-test-only"

	payload := []byte("channel payload fixture")
	publicKey, privateKey, err := hardeningdomain.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := hardeningdomain.HashArtifact(payload)
	payloadSignature, err := hardeningdomain.SignArtifactDigest(privateKey, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	manifest := signedChannelManifestFixture(payloadDigest, payloadSignature)
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := manifest.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	documentAuthorization := ""
	packageAuthorization := ""
	packageDeployToken := ""
	packageRequests := 0
	var documentBytes []byte
	packageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/packages/v1/release-manifest.json" {
			mu.Lock()
			packageAuthorization = r.Header.Get("Authorization")
			packageDeployToken = r.Header.Get("DEPLOY-TOKEN")
			packageRequests++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(manifestBody)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer packageServer.Close()
	documentHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		documentAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(documentBytes)
	}))
	defer documentHost.Close()

	document := channel.Document{
		SchemaVersion:   channel.SchemaVersion,
		Channel:         string(hardeningdomain.ReleaseChannelStable),
		ReleaseID:       manifest.ReleaseID,
		Version:         manifest.Version,
		ReleaseSequence: manifest.ReleaseSequence,
		ManifestURL:     packageServer.URL + "/packages/v1/release-manifest.json",
		ManifestDigest:  manifestDigest,
		SourceCommit:    manifest.SourceCommit,
		PublishedAt:     time.Now().UTC(),
	}
	if err := document.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	documentBytes, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	credentialFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credentialFile, []byte(pat+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := newChannelResolver(
		documentHost.URL+"/channel/channel-v1.json",
		packageServer.URL+"/packages",
		UpdateHTTPSourceConfig{
			AuthMode:       UpdateAuthModeGiteaPAT,
			CredentialFile: credentialFile,
		},
		publicKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.resolve(context.Background())
	if err != nil {
		t.Fatalf("cross-host channel resolve failed: %v", err)
	}
	if resolved.manifest.ReleaseID != manifest.ReleaseID {
		t.Fatalf("resolved release = %q, want %q", resolved.manifest.ReleaseID, manifest.ReleaseID)
	}
	if got := documentAuthorization; got != "" {
		t.Fatalf("channel document on another host carried Authorization %q", got)
	}
	mu.Lock()
	gotPackage := packageAuthorization
	gotDeployToken := packageDeployToken
	packageCount := packageRequests
	mu.Unlock()
	// The document host must never see the PAT, and the package host must
	// always receive exactly the configured credential. A resolver that stopped
	// sending it here (or sent a legacy header) must not look green.
	if packageCount != 1 {
		t.Fatalf("package manifest requests = %d, want 1", packageCount)
	}
	if want := "token " + pat; gotPackage != want {
		t.Fatalf("package manifest carried Authorization %q, want %q", gotPackage, want)
	}
	if gotDeployToken != "" {
		t.Fatalf("package manifest carried the retired DEPLOY-TOKEN header %q", gotDeployToken)
	}
}

func signedChannelManifestFixture(payloadDigest, payloadSignature string) hardeningdomain.ReleaseManifest {
	return hardeningdomain.ReleaseManifest{
		SchemaVersion:          "release-manifest-1",
		ReleaseSequence:        1004,
		ReleaseID:              "release-test-1004",
		Version:                "v2.0.0",
		Channel:                hardeningdomain.ReleaseChannelStable,
		SourceCommit:           strings.Repeat("a", 40),
		APIContractVersion:     "v2",
		TargetMigrationVersion: "001",
		RequiredDowntime:       true,
		BackupRequired:         true,
		Rollback:               hardeningdomain.RestoreRequired,
		Artifacts: []hardeningdomain.ArtifactRef{{
			Platform:     "linux",
			Architecture: "amd64",
			Digest:       strings.Repeat("b", 64),
			Size:         1,
			Signature:    "artifact-signature",
		}},
		SBOMRef:        "sbom.json",
		ProvenanceRef:  "provenance.json",
		ArtifactDigest: strings.Repeat("c", 64),
		Immutable:      true,
		EmbeddedUpdate: &hardeningdomain.EmbeddedUpdateSegment{
			FormatVersion:       hardeningdomain.EmbeddedUpdateFormatV1,
			Platform:            "linux",
			Architecture:        "amd64",
			PayloadDigest:       payloadDigest,
			PayloadSize:         22,
			PayloadSignature:    payloadSignature,
			BootstrapMinVersion: "embedded-bootstrap-v1",
			MinImageVersion:     "base-v1",
			TargetReleaseID:     "release-test-1004",
			TargetVersion:       "v2.0.0",
			TargetSchemaVersion: "001",
			ReleaseSequence:     1004,
		},
	}
}
