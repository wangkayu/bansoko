package application

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
)

// authRequestRecord is one request the mock source host observed, including the
// authentication headers that reached it. It is the evidence base for every
// counterexample in this file: the tests assert what the host actually saw, not
// what the client intended.
type authRequestRecord struct {
	path          string
	authorization string
	deployToken   string
	privateToken  string
	jobToken      string
}

// authRecordingServer wraps httptest with a request log. Every handler body is
// user-supplied so a test can serve valid signed bytes with a refusing status:
// that combination is what makes an auth failure test discriminating, because an
// implementation that ignored the HTTP status would otherwise "succeed" on the
// valid body and a broken implementation would look green for the wrong reason.
type authRecordingServer struct {
	mu       sync.Mutex
	requests []authRequestRecord
	server   *httptest.Server
}

func newAuthRecordingServer(behavior func(w http.ResponseWriter, r *http.Request)) *authRecordingServer {
	recording := &authRecordingServer{}
	recording.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recording.mu.Lock()
		recording.requests = append(recording.requests, authRequestRecord{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			deployToken:   r.Header.Get("DEPLOY-TOKEN"),
			privateToken:  r.Header.Get("PRIVATE-TOKEN"),
			jobToken:      r.Header.Get("JOB-TOKEN"),
		})
		recording.mu.Unlock()
		behavior(w, r)
	}))
	return recording
}

func (s *authRecordingServer) URL() string {
	if s == nil || s.server == nil {
		return ""
	}
	return s.server.URL
}

func (s *authRecordingServer) Close() {
	if s == nil || s.server == nil {
		return
	}
	s.server.Close()
}

func (s *authRecordingServer) snapshot() []authRequestRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authRequestRecord(nil), s.requests...)
}

func (s *authRecordingServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *authRecordingServer) requested(pathSuffix string) []authRequestRecord {
	var matched []authRequestRecord
	for _, request := range s.snapshot() {
		if strings.HasSuffix(request.path, pathSuffix) {
			matched = append(matched, request)
		}
	}
	return matched
}

func (s *authRecordingServer) requestedPayload() []authRequestRecord {
	return s.requested(PayloadFileName)
}

func writeGiteaCredentialFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// signedManifestFixture builds a fully valid signed release manifest body. The
// counterexample tests serve these bytes with a refusing status, so a body
// failure can never be the reason the client rejects the response.
func signedManifestFixture(t *testing.T) (hardeningdomain.ReleaseManifest, []byte, ed25519.PublicKey) {
	t.Helper()
	payload := []byte("counterexample payload fixture")
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
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, body, publicKey
}

// TestPrivateSourceAuthRefusalsFailClosedThroughCheckUpdate is the counterexample
// for a false-green auth failure test: the body served with every refusing
// status is a fully valid signed manifest, so the status is the only possible
// reason to reject. The test drives both the source-level fetch and the
// CheckUpdate entry point and proves neither retries anonymously, produces a
// candidate, fetches the payload, or writes staging state.
func TestPrivateSourceAuthRefusalsFailClosedThroughCheckUpdate(t *testing.T) {
	_, manifestBody, publicKey := signedManifestFixture(t)
	credentialFile := writeGiteaCredentialFile(t, "gitea-pat-test-only")
	const manifestPath = "/packages/v1/release-manifest.json"

	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusInternalServerError,
		http.StatusNotFound,
	} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			recording := newAuthRecordingServer(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				// Valid signed bytes: ignoring the status would verify fine.
				_, _ = w.Write(manifestBody)
			})
			defer recording.Close()

			newSource := func() *UpdateHTTPSource {
				source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
					ManifestURL:    recording.URL() + manifestPath,
					AuthMode:       UpdateAuthModeGiteaPAT,
					CredentialFile: credentialFile,
				})
				if err != nil {
					t.Fatal(err)
				}
				return source
			}

			before := recording.count()
			_, _, err := fetchAndVerifyManifest(context.Background(), newSource(), publicKey)
			if err == nil {
				t.Fatalf("HTTP %d manifest body was admitted", status)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("failure does not name the refusing status %d: %v", status, err)
			}
			if got := recording.count() - before; got != 1 {
				t.Fatalf("source-level requests = %d, want 1: the client retried after the refusal", got)
			}

			updatesRoot := t.TempDir()
			service, err := NewService(Deps{
				UpdateMode:     "embedded",
				PublicKey:      publicKey,
				UpdatesRoot:    updatesRoot,
				ManifestURL:    recording.URL() + manifestPath,
				AuthMode:       UpdateAuthModeGiteaPAT,
				CredentialFile: credentialFile,
			})
			if err != nil {
				t.Fatal(err)
			}
			before = recording.count()
			check, err := service.CheckUpdate(context.Background())
			if err == nil {
				t.Fatal("CheckUpdate admitted a source that answered with a refusal")
			}
			if check.Status != "check_failed" {
				t.Fatalf("CheckUpdate status = %q, want check_failed", check.Status)
			}
			if check.Candidate != nil {
				t.Fatal("a refused source produced an update candidate")
			}
			if got := recording.count() - before; got != 1 {
				t.Fatalf("CheckUpdate requests = %d, want 1: the entry point retried or fell back", got)
			}
			if payloads := recording.requestedPayload(); len(payloads) != 0 {
				t.Fatalf("a refused source still fetched the payload: %+v", payloads)
			}
			if entries, readErr := os.ReadDir(updatesRoot); readErr != nil || len(entries) != 0 {
				t.Fatalf("a refused source wrote staging state under %s (entries=%d, err=%v)", updatesRoot, len(entries), readErr)
			}
		})
	}
}

// TestPrivateSourceNeverRetriesAnonymouslyAfterAuthRefusal closes the
// anonymous-fallback hole directly: the mock host serves the valid signed
// manifest only to an unauthenticated request, so any implementation that drops
// the credential and retries would observe a successful verification.
func TestPrivateSourceNeverRetriesAnonymouslyAfterAuthRefusal(t *testing.T) {
	_, manifestBody, publicKey := signedManifestFixture(t)
	credentialFile := writeGiteaCredentialFile(t, "gitea-pat-test-only")

	var mu sync.Mutex
	anonymousServed := false
	recording := newAuthRecordingServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			mu.Lock()
			anonymousServed = true
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(manifestBody)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	defer recording.Close()

	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL:    recording.URL() + "/packages/v1/release-manifest.json",
		AuthMode:       UpdateAuthModeGiteaPAT,
		CredentialFile: credentialFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fetchAndVerifyManifest(context.Background(), source, publicKey); err == nil {
		t.Fatal("an authenticated refusal was resolved by an anonymous fallback")
	}
	mu.Lock()
	fellBack := anonymousServed
	mu.Unlock()
	if fellBack {
		t.Fatal("the source retried without a credential after an authentication refusal")
	}
	if got := recording.count(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	for _, request := range recording.snapshot() {
		if request.authorization != "token gitea-pat-test-only" {
			t.Fatalf("request %s carried Authorization %q", request.path, request.authorization)
		}
	}
}

// TestPublicNoneSourceSendsNoAuthenticationHeaders locks the clarified none
// contract: an empty or Authorization header name may be configured, but with no
// credential file the source must not load a credential, must send no
// authentication header, and must clear any header a caller supplied.
func TestPublicNoneSourceSendsNoAuthenticationHeaders(t *testing.T) {
	for _, header := range []string{"", "Authorization"} {
		t.Run(fmt.Sprintf("configured header %q", header), func(t *testing.T) {
			recording := newAuthRecordingServer(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			defer recording.Close()

			for _, mode := range []string{UpdateAuthModeNone, ""} {
				source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
					ManifestURL:      recording.URL() + "/packages/v1/release-manifest.json",
					AuthMode:         mode,
					CredentialHeader: header,
				})
				if err != nil {
					t.Fatal(err)
				}
				if source.HasCredential() {
					t.Fatal("none mode loaded a credential")
				}
				if source.CredentialHeader() != "" {
					t.Fatalf("none mode kept credential header %q", source.CredentialHeader())
				}
				request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.ManifestURL(), nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer caller-supplied")
				request.Header.Set("DEPLOY-TOKEN", "caller-supplied")
				request.Header.Set("PRIVATE-TOKEN", "caller-supplied")
				request.Header.Set("JOB-TOKEN", "caller-supplied")
				response, err := source.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				for _, seen := range recording.snapshot() {
					if seen.authorization != "" || seen.deployToken != "" || seen.privateToken != "" || seen.jobToken != "" {
						t.Fatalf("none mode sent authentication headers: %+v", seen)
					}
				}
			}
			if got := recording.count(); got != 2 {
				t.Fatalf("requests = %d, want 2", got)
			}
		})
	}
}
