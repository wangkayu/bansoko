package application

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/update/channel"
)

const (
	channelCounterexampleDocumentPath = "/packages/channel/channel-v1.json"
	channelCounterexampleManifestPath = "/packages/v1/release-manifest.json"
	channelCounterexamplePayloadPath  = "/packages/v1/" + PayloadFileName
	channelCounterexamplePAT          = "gitea-channel-pat-test-only"
	channelCounterexampleCredential   = "token " + channelCounterexamplePAT
)

// channelCounterexampleRig is one mock package host. It records every request
// with the authentication headers that actually reached it and serves
// caller-supplied bodies with caller-supplied statuses. The document body is
// settable after construction because the signed document names the very URL
// the rig serves it from.
type channelCounterexampleRig struct {
	recording    *authRecordingServer
	documentBody []byte
	manifestBody []byte
	statuses     map[string]int
	mu           sync.Mutex
}

func newChannelCounterexampleRig(t *testing.T, manifestBody []byte, statuses map[string]int) *channelCounterexampleRig {
	t.Helper()
	rig := &channelCounterexampleRig{manifestBody: manifestBody, statuses: statuses}
	rig.recording = newAuthRecordingServer(func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusOK
		if override, ok := statuses[r.URL.Path]; ok {
			status = override
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		rig.mu.Lock()
		documentBody := rig.documentBody
		rig.mu.Unlock()
		switch r.URL.Path {
		case channelCounterexampleDocumentPath:
			_, _ = w.Write(documentBody)
		case channelCounterexampleManifestPath:
			_, _ = w.Write(manifestBody)
		case channelCounterexamplePayloadPath:
			_, _ = w.Write([]byte("channel payload fixture"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	})
	t.Cleanup(rig.recording.Close)
	return rig
}

func (r *channelCounterexampleRig) setDocumentBody(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.documentBody = body
}

func (r *channelCounterexampleRig) credentialConfig(t *testing.T) UpdateHTTPSourceConfig {
	t.Helper()
	return UpdateHTTPSourceConfig{
		AuthMode:       UpdateAuthModeGiteaPAT,
		CredentialFile: writeGiteaCredentialFile(t, channelCounterexamplePAT),
	}
}

// newResolver builds a channel resolver whose package root and channel document
// both live on this rig (same host), so the deployment PAT is expected on both.
func (r *channelCounterexampleRig) newResolver(t *testing.T, publicKey ed25519.PublicKey) *channelResolver {
	t.Helper()
	resolver, err := newChannelResolver(
		r.recording.URL()+channelCounterexampleDocumentPath,
		r.recording.URL()+"/packages",
		r.credentialConfig(t),
		publicKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func (r *channelCounterexampleRig) newService(t *testing.T, publicKey ed25519.PublicKey) *Service {
	t.Helper()
	service, err := NewService(Deps{
		UpdateMode:     "embedded",
		PublicKey:      publicKey,
		UpdatesRoot:    t.TempDir(),
		ChannelURL:     r.recording.URL() + channelCounterexampleDocumentPath,
		PackageRoot:    r.recording.URL() + "/packages",
		AuthMode:       UpdateAuthModeGiteaPAT,
		CredentialFile: writeGiteaCredentialFile(t, channelCounterexamplePAT),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// buildManifestBody signs one well-formed embedded release manifest with the
// given key, so a forged manifest is a decode-valid document with an untrusted
// signature rather than a broken body.
func buildManifestBody(t *testing.T, signingKey ed25519.PrivateKey) (string, []byte) {
	t.Helper()
	payload := []byte("channel counterexample payload")
	payloadDigest := hardeningdomain.HashArtifact(payload)
	payloadSignature, err := hardeningdomain.SignArtifactDigest(signingKey, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	manifest := signedChannelManifestFixture(payloadDigest, payloadSignature)
	if err := manifest.Sign(signingKey); err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return digest, body
}

// channelDocumentFixture signs one discovery document. The signing key and the
// named identity are parameters so a test can forge either surface.
func channelDocumentFixture(t *testing.T, signingKey ed25519.PrivateKey, manifestURL, manifestDigest, releaseID string, mutate func(*channel.Document)) []byte {
	t.Helper()
	document := channel.Document{
		SchemaVersion:   channel.SchemaVersion,
		Channel:         string(hardeningdomain.ReleaseChannelStable),
		ReleaseID:       releaseID,
		Version:         "v2.0.0",
		ReleaseSequence: 1004,
		ManifestURL:     manifestURL,
		ManifestDigest:  manifestDigest,
		SourceCommit:    strings.Repeat("a", 40),
		PublishedAt:     time.Now().UTC(),
	}
	if mutate != nil {
		mutate(&document)
	}
	if err := document.Sign(signingKey); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// assertRequestGraph proves the resolver stopped exactly where the case says it
// should: the recorded requests are exactly the expected document plus manifest
// counts, none of them anonymous or carrying a retired header, and the payload
// was never touched.
func assertRequestGraph(t *testing.T, rig *channelCounterexampleRig, wantDocument, wantManifest int) {
	t.Helper()
	requests := rig.recording.snapshot()
	documentRequests, manifestRequests, payloadRequests := 0, 0, 0
	for _, request := range requests {
		switch request.path {
		case channelCounterexampleDocumentPath:
			documentRequests++
		case channelCounterexampleManifestPath:
			manifestRequests++
		case channelCounterexamplePayloadPath:
			payloadRequests++
		}
		if request.authorization != channelCounterexampleCredential {
			t.Fatalf("request %s carried Authorization %q, want the configured PAT", request.path, request.authorization)
		}
		if request.deployToken != "" || request.privateToken != "" || request.jobToken != "" {
			t.Fatalf("request %s carried a retired credential header", request.path)
		}
	}
	if documentRequests != wantDocument {
		t.Fatalf("document requests = %d, want %d", documentRequests, wantDocument)
	}
	if manifestRequests != wantManifest {
		t.Fatalf("manifest requests = %d, want %d", manifestRequests, wantManifest)
	}
	if payloadRequests != 0 {
		t.Fatalf("payload was requested after the failure (%d requests)", payloadRequests)
	}
	if len(requests) != wantDocument+wantManifest {
		t.Fatalf("total requests = %d, want %d: the client retried", len(requests), wantDocument+wantManifest)
	}
}

// TestChannelRefusalsAndForgeriesFailClosed is the channel-side counterexample
// set. Every case serves byte-valid content so the failure can only be the one
// under test, asserts the typed failure, and proves the request graph stopped
// exactly where it should: a refused document never reaches the manifest, a
// refused or forged manifest never reaches the payload, and no request was ever
// sent without the configured PAT.
func TestChannelRefusalsAndForgeriesFailClosed(t *testing.T) {
	trustedPublic, trustedPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, forgerPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	trustedDigest, trustedManifest := buildManifestBody(t, trustedPrivate)
	_, forgedManifest := buildManifestBody(t, forgerPrivate)

	cases := []struct {
		name           string
		documentSigner ed25519.PrivateKey
		documentMutate func(*channel.Document)
		manifestBody   []byte
		statuses       map[string]int
		wantCode       string
		wantDocument   int
		wantManifest   int
	}{
		{
			name:           "document HTTP 401",
			documentSigner: trustedPrivate,
			manifestBody:   trustedManifest,
			statuses:       map[string]int{channelCounterexampleDocumentPath: http.StatusUnauthorized},
			wantCode:       "UPDATE_CHANNEL_UNAVAILABLE",
			wantDocument:   1,
			wantManifest:   0,
		},
		{
			name:           "document HTTP 403",
			documentSigner: trustedPrivate,
			manifestBody:   trustedManifest,
			statuses:       map[string]int{channelCounterexampleDocumentPath: http.StatusForbidden},
			wantCode:       "UPDATE_CHANNEL_UNAVAILABLE",
			wantDocument:   1,
			wantManifest:   0,
		},
		{
			name:           "manifest HTTP 401",
			documentSigner: trustedPrivate,
			manifestBody:   trustedManifest,
			statuses:       map[string]int{channelCounterexampleManifestPath: http.StatusUnauthorized},
			wantCode:       "UPDATE_CHANNEL_UNAVAILABLE",
			wantDocument:   1,
			wantManifest:   1,
		},
		{
			name:           "manifest HTTP 403",
			documentSigner: trustedPrivate,
			manifestBody:   trustedManifest,
			statuses:       map[string]int{channelCounterexampleManifestPath: http.StatusForbidden},
			wantCode:       "UPDATE_CHANNEL_UNAVAILABLE",
			wantDocument:   1,
			wantManifest:   1,
		},
		{
			name:           "document forged signature",
			documentSigner: forgerPrivate,
			manifestBody:   trustedManifest,
			wantCode:       "UPDATE_CHANNEL_INVALID",
			wantDocument:   1,
			wantManifest:   0,
		},
		{
			name:           "manifest forged signature",
			documentSigner: trustedPrivate,
			manifestBody:   forgedManifest,
			wantCode:       "UPDATE_CHANNEL_UNAVAILABLE",
			wantDocument:   1,
			wantManifest:   1,
		},
		{
			name:           "document names another release",
			documentSigner: trustedPrivate,
			documentMutate: func(d *channel.Document) { d.ReleaseID = "release-not-published" },
			manifestBody:   trustedManifest,
			wantCode:       "UPDATE_CHANNEL_MISMATCH",
			wantDocument:   1,
			wantManifest:   1,
		},
		{
			name:           "document names another digest",
			documentSigner: trustedPrivate,
			documentMutate: func(d *channel.Document) { d.ManifestDigest = strings.Repeat("d", 64) },
			manifestBody:   trustedManifest,
			wantCode:       "UPDATE_CHANNEL_MISMATCH",
			wantDocument:   1,
			wantManifest:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newChannelCounterexampleRig(t, tc.manifestBody, tc.statuses)
			documentURL := rig.recording.URL() + channelCounterexampleManifestPath
			rig.setDocumentBody(channelDocumentFixture(t, tc.documentSigner, documentURL, trustedDigest, "release-test-1004", tc.documentMutate))

			resolver := rig.newResolver(t, trustedPublic)
			_, resolveErr := resolver.resolve(context.Background())
			if resolveErr == nil {
				t.Fatalf("%s: channel resolve unexpectedly succeeded", tc.name)
			}
			var business *apperrors.Business
			if !errors.As(resolveErr, &business) {
				t.Fatalf("%s: error %v is not a typed business failure", tc.name, resolveErr)
			}
			if business.Code != tc.wantCode {
				t.Fatalf("%s: failure code = %q, want %q", tc.name, business.Code, tc.wantCode)
			}
			assertRequestGraph(t, rig, tc.wantDocument, tc.wantManifest)

			service := rig.newService(t, trustedPublic)
			check, checkErr := service.CheckUpdate(context.Background())
			if checkErr == nil {
				t.Fatalf("%s: CheckUpdate admitted the channel", tc.name)
			}
			if check.Status != "check_failed" {
				t.Fatalf("%s: CheckUpdate status = %q, want check_failed", tc.name, check.Status)
			}
			if check.Candidate != nil {
				t.Fatalf("%s: CheckUpdate produced a candidate from a refused channel", tc.name)
			}
			if entries, readErr := os.ReadDir(service.updatesRoot); readErr != nil || len(entries) != 0 {
				t.Fatalf("%s: CheckUpdate wrote state under %s (entries=%d, err=%v)", tc.name, service.updatesRoot, len(entries), readErr)
			}
		})
	}
}

// TestChannelNeverRetriesAnonymously closes the anonymous-fallback hole for
// discovery: the mock host answers 401 to every document request, and the test
// fails if any request ever arrives without the credential, if the resolver
// resolves anyway, or if it retries.
func TestChannelNeverRetriesAnonymously(t *testing.T) {
	trustedPublic, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	anonymousServed := false
	recording := newAuthRecordingServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			mu.Lock()
			anonymousServed = true
			mu.Unlock()
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	defer recording.Close()

	resolver, err := newChannelResolver(
		recording.URL()+channelCounterexampleDocumentPath,
		recording.URL()+"/packages",
		UpdateHTTPSourceConfig{
			AuthMode:       UpdateAuthModeGiteaPAT,
			CredentialFile: writeGiteaCredentialFile(t, channelCounterexamplePAT),
		},
		trustedPublic,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.resolve(context.Background()); err == nil {
		t.Fatal("an authenticated refusal was resolved by an anonymous fallback")
	}
	mu.Lock()
	fellBack := anonymousServed
	mu.Unlock()
	if fellBack {
		t.Fatal("the channel resolver retried the document without a credential")
	}
	if got := recording.count(); got != 1 {
		t.Fatalf("document requests = %d, want 1", got)
	}
	if got := recording.requested(channelCounterexampleManifestPath); len(got) != 0 {
		t.Fatalf("the manifest was fetched after the document refused: %+v", got)
	}
}
