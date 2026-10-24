package application

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
)

func TestParseUpdateCredential(t *testing.T) {
	if got, err := ParseUpdateCredential("token\n"); err != nil || got != "token" {
		t.Fatalf("single trailing LF: got %q, %v", got, err)
	}
	for _, raw := range []string{"", "\n", "token\nother", "token\r\n", "token\r", "token\x00"} {
		t.Run(strings.ReplaceAll(raw, "\n", "\\n"), func(t *testing.T) {
			if _, err := ParseUpdateCredential(raw); err == nil {
				t.Fatalf("credential %q should be rejected", raw)
			}
		})
	}
}

// TestUpdateHTTPSourceStripsCallerAuthHeaders locks the credential boundary:
// a caller-supplied Authorization and the retired GitLab headers are cleared
// on every source request, and a public source never sends any of them back.
func TestUpdateHTTPSourceStripsCallerAuthHeaders(t *testing.T) {
	credential := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credential, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type observation struct {
		authorization string
		deployToken   string
		privateToken  string
		jobToken      string
	}
	var mu sync.Mutex
	var seen []observation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, observation{
			authorization: r.Header.Get("Authorization"),
			deployToken:   r.Header.Get("DEPLOY-TOKEN"),
			privateToken:  r.Header.Get("PRIVATE-TOKEN"),
			jobToken:      r.Header.Get("JOB-TOKEN"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	private, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    server.URL + "/packages/v1/release-manifest.json",
		AuthMode:       UpdateAuthModeGiteaPAT,
		CredentialFile: credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	public, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: server.URL + "/packages/v1/release-manifest.json",
	})
	if err != nil {
		t.Fatal(err)
	}

	send := func(source *UpdateHTTPSource) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.ManifestURL(), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer caller-supplied")
		req.Header.Set("DEPLOY-TOKEN", "caller-supplied")
		req.Header.Set("PRIVATE-TOKEN", "caller-supplied")
		req.Header.Set("JOB-TOKEN", "caller-supplied")
		resp, err := source.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	send(private)
	send(public)

	mu.Lock()
	got := append([]observation(nil), seen...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("stripped-header requests = %d, want 2", len(got))
	}
	for _, o := range got {
		if o.deployToken != "" || o.privateToken != "" || o.jobToken != "" {
			t.Fatalf("a retired GitLab credential header survived: %+v", o)
		}
	}
	if got[0].authorization != "token secret-token" {
		t.Fatalf("private Authorization = %q, want the source-built credential", got[0].authorization)
	}
	if got[1].authorization != "" {
		t.Fatalf("public Authorization = %q, want empty", got[1].authorization)
	}
}

func TestNewUpdateHTTPSourceGiteaPatHeader(t *testing.T) {
	credential := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credential, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "token secret-token" {
			t.Errorf("Authorization = %q, want %q", got, "token secret-token")
		}
		for _, retired := range []string{"DEPLOY-TOKEN", "PRIVATE-TOKEN", "JOB-TOKEN"} {
			if r.Header.Get(retired) != "" {
				t.Errorf("retired header %s was sent", retired)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	// An empty header configuration is equivalent to the canonical Gitea
	// Authorization header; the header value is assembled internally.
	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    server.URL + "/packages/v1/release-manifest.json",
		AuthMode:       UpdateAuthModeGiteaPAT,
		CredentialFile: credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.CredentialHeader() != "Authorization" {
		t.Fatalf("default credential header = %q, want Authorization", source.CredentialHeader())
	}
	if !source.HasCredential() {
		t.Fatal("private source reported no credential")
	}
	if _, err := source.Do(context.Background(), http.MethodGet, source.ManifestURL()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if requests != 1 {
		t.Fatalf("source requests = %d, want 1", requests)
	}
	mu.Unlock()

	explicit, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:      server.URL + "/x",
		AuthMode:         UpdateAuthModeGiteaPAT,
		CredentialFile:   credential,
		CredentialHeader: "Authorization",
	})
	if err != nil {
		t.Fatalf("explicit Authorization rejected: %v", err)
	} else if explicit.CredentialHeader() != "Authorization" {
		t.Fatalf("credential header = %q, want Authorization", explicit.CredentialHeader())
	}
	// Only the empty or Authorization configuration is allowed; the retired
	// GitLab deploy-token header and every arbitrary header are refused.
	for _, header := range []string{"DEPLOY-TOKEN", "PRIVATE-TOKEN", "JOB-TOKEN", "X-Token", "authorization", " Bearer"} {
		if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
			ManifestURL:      server.URL + "/x",
			AuthMode:         UpdateAuthModeGiteaPAT,
			CredentialFile:   credential,
			CredentialHeader: header,
		}); err == nil {
			t.Fatalf("unsupported credential header %q was accepted", header)
		}
	}
}

func TestNewUpdateHTTPSourcePrivateAndPublicCredentialModes(t *testing.T) {
	credential := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credential, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	// The only private mode is gitea_pat, and it requires the PAT file.
	if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: server.URL + "/packages/v1/release-manifest.json",
		AuthMode:    UpdateAuthModeGiteaPAT,
	}); err == nil {
		t.Fatal("private source without credential file was accepted")
	}
	// The retired GitLab deploy-token mode is refused, not mapped onto the
	// Gitea PAT mode.
	if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    server.URL + "/packages/v1/release-manifest.json",
		AuthMode:       "gitlab_deploy_token",
		CredentialFile: credential,
	}); err == nil {
		t.Fatal("gitlab_deploy_token mode was accepted")
	}
	// An explicit public source can never carry a credential.
	if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    server.URL + "/packages/v1/release-manifest.json",
		AuthMode:       UpdateAuthModeNone,
		CredentialFile: credential,
	}); err == nil {
		t.Fatal("public source with credential file was accepted")
	}
	// An empty mode is equivalent to none: a credential file alone never
	// selects a private mode, so a half-migrated deployment fails closed.
	if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    server.URL + "/packages/v1/release-manifest.json",
		CredentialFile: credential,
	}); err == nil {
		t.Fatal("credential file without an explicit private mode was accepted")
	}
	if source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: server.URL + "/packages/v1/release-manifest.json",
	}); err != nil {
		t.Fatal(err)
	} else if source.HasCredential() || source.CredentialHeader() != "" {
		t.Fatal("public source unexpectedly loaded a credential")
	}
	// A public source keeps no authentication header even when one is named.
	if source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:      server.URL + "/packages/v1/release-manifest.json",
		AuthMode:         UpdateAuthModeNone,
		CredentialHeader: "Authorization",
	}); err != nil {
		t.Fatal(err)
	} else if source.HasCredential() || source.CredentialHeader() != "" {
		t.Fatal("public source kept an authentication header")
	}
}

func TestUpdateHTTPSourceRejectsUnsafeURLsAndRedirects(t *testing.T) {
	for _, raw := range []string{
		"https://user:pass@example.test/package/manifest.json",
		"https://example.test/package/manifest.json?token=secret",
		"https://example.test/package/manifest.json#secret",
		"https://example.test/package/./manifest.json",
		"https://example.test/package/../manifest.json",
		"https://example.test/package\\manifest.json",
	} {
		if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{ManifestURL: raw}); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}

	credential := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credential, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("cross-host redirect carried Authorization %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/package/release-manifest.json":
			w.Header().Set("Location", "/package/redirected-manifest.json")
			w.WriteHeader(http.StatusFound)
		case "/package/redirected-manifest.json":
			if got := r.Header.Get("Authorization"); got != "token secret-token" {
				t.Errorf("same-package redirect Authorization = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/package/cross-host.json":
			w.Header().Set("Location", other.URL+"/package/release-manifest.json")
			w.WriteHeader(http.StatusFound)
		case "/outside/release-manifest.json":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer redirect.Close()

	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    redirect.URL + "/package/release-manifest.json",
		AuthMode:       UpdateAuthModeGiteaPAT,
		CredentialFile: credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Do(context.Background(), http.MethodGet, source.ManifestURL()); err != nil {
		t.Fatalf("same-host package redirect rejected: %v", err)
	}
	if _, err := source.Do(context.Background(), http.MethodGet, redirect.URL+"/package/cross-host.json"); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("cross-host redirect result = %v", err)
	}
	if _, err := source.Do(context.Background(), http.MethodGet, redirect.URL+"/outside/release-manifest.json"); err == nil {
		t.Fatal("same-host out-of-package request was accepted")
	}
}

func TestUpdateHTTPSourceProductionRequiresHTTPS(t *testing.T) {
	if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: "http://example.test/package/manifest.json",
		Production:  true,
	}); err == nil {
		t.Fatal("production HTTP source accepted")
	}
}

func TestManifestAndPayloadUseSameSource(t *testing.T) {
	var publicKey ed25519.PublicKey
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("public source unexpectedly carried a credential")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()

	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: server.URL + "/packages/v1/release-manifest.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := source.PayloadURL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(payload, "/packages/v1/"+PayloadFileName) {
		t.Fatalf("payload URL = %q", payload)
	}
	if _, err := source.Do(context.Background(), http.MethodGet, payload); err != nil {
		t.Fatal(err)
	}
	_, _, _ = fetchAndVerifyManifest(context.Background(), source, publicKey)
}

func TestPrivateUpdateSourceAuthenticatesManifestAndPayload(t *testing.T) {
	const correctCredential = "gitea-pat-test-only"
	const wrongCredential = "wrong-gitea-pat-test-only"
	const correctHeader = "token " + correctCredential
	payload := []byte("signed payload fixture")
	publicKey, privateKey, err := hardeningdomain.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := hardeningdomain.HashArtifact(payload)
	payloadSignature, err := hardeningdomain.SignArtifactDigest(privateKey, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hardeningdomain.ReleaseManifest{
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
			PayloadSize:         int64(len(payload)),
			PayloadSignature:    payloadSignature,
			BootstrapMinVersion: "embedded-bootstrap-v1",
			MinImageVersion:     "base-v1",
			TargetReleaseID:     "release-test-1004",
			TargetVersion:       "v2.0.0",
			TargetSchemaVersion: "001",
			ReleaseSequence:     1004,
		},
	}
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	var requests []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != correctHeader {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
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

	credentialFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credentialFile, []byte(correctCredential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    server.URL + "/packages/v1/release-manifest.json",
		AuthMode:       UpdateAuthModeGiteaPAT,
		CredentialFile: credentialFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotManifest, gotDigest, err := fetchAndVerifyManifest(context.Background(), source, publicKey)
	if err != nil {
		t.Fatalf("signed private manifest rejected: %v", err)
	}
	if gotManifest.ReleaseID != manifest.ReleaseID || gotDigest == "" {
		t.Fatalf("verified manifest = %q, digest empty=%t", gotManifest.ReleaseID, gotDigest == "")
	}
	payloadURL, err := source.PayloadURL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(payloadURL, "/packages/v1/"+PayloadFileName) {
		t.Fatalf("payload URL = %q", payloadURL)
	}
	payloadResponse, err := source.Do(context.Background(), http.MethodGet, payloadURL)
	if err != nil {
		t.Fatal(err)
	}
	defer payloadResponse.Body.Close()
	if payloadResponse.StatusCode != http.StatusOK {
		t.Fatalf("payload status = %d, want %d", payloadResponse.StatusCode, http.StatusOK)
	}
	gotPayload, err := io.ReadAll(payloadResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotPayload) != string(payload) {
		t.Fatalf("payload body = %q, want fixture bytes", gotPayload)
	}
	mu.Lock()
	gotRequests := append([]string(nil), requests...)
	mu.Unlock()
	if len(gotRequests) != 2 {
		t.Fatalf("authenticated source requests = %d, want 2", len(gotRequests))
	}
	for _, request := range gotRequests {
		if !strings.HasSuffix(request, " "+correctHeader) {
			t.Fatal("manifest and payload did not use the same Authorization: token <PAT>")
		}
	}

	wrongFile := filepath.Join(t.TempDir(), "wrong-token")
	if err := os.WriteFile(wrongFile, []byte(wrongCredential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongSource, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    server.URL + "/packages/v1/release-manifest.json",
		AuthMode:       UpdateAuthModeGiteaPAT,
		CredentialFile: wrongFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongResponse, err := wrongSource.Do(context.Background(), http.MethodGet, wrongSource.ManifestURL())
	if err != nil {
		t.Fatal(err)
	}
	wrongResponse.Body.Close()
	if wrongResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong credential status = %d, want %d", wrongResponse.StatusCode, http.StatusUnauthorized)
	}
	_, _, wrongErr := fetchAndVerifyManifest(context.Background(), wrongSource, publicKey)
	if wrongErr == nil {
		t.Fatal("wrong credential unexpectedly verified a manifest")
	}
	if strings.Contains(wrongErr.Error(), wrongCredential) || strings.Contains(wrongErr.Error(), correctCredential) {
		t.Fatal("authentication failure exposed a credential")
	}
}

// TestPrivateUpdateSourceAuthFailuresFailClosed keeps the fail-closed
// boundary: a private source that the host refuses (401/403) or crashes (5xx)
// never yields a verified manifest, so check and admission cannot admit a
// release the deployment could not actually authenticate to.
func TestPrivateUpdateSourceAuthFailuresFailClosed(t *testing.T) {
	var publicKey ed25519.PublicKey
	credential := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(credential, []byte("gitea-pat-test-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		status := status
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
			ManifestURL:    server.URL + "/packages/v1/release-manifest.json",
			AuthMode:       UpdateAuthModeGiteaPAT,
			CredentialFile: credential,
		})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		if _, _, err := fetchAndVerifyManifest(context.Background(), source, publicKey); err == nil {
			t.Fatalf("status %d produced a verified manifest", status)
		}
		server.Close()
	}
}
