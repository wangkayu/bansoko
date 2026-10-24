package application

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"

	hardeningdomain "bansoko/internal/hardening/domain"
)

// maxManifestBytes bounds the signed manifest document; release manifests are
// small JSON files and anything larger is hostile input.
const maxManifestBytes = 1 << 20

// payloadSource resolves the payload archive URL as a sibling of the signed
// manifest document: <manifest dir>/<PayloadFileName>. This is the layout
// build-release.sh publishes.
func payloadSource(manifestURL string) (string, error) {
	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{ManifestURL: manifestURL})
	if err != nil {
		return "", err
	}
	return source.PayloadURL()
}

// fetchAndVerifyManifestRaw downloads the release manifest through the shared
// source builder, enforces the full embedded contract, and verifies both
// signature surfaces against the built-in public key. The received document
// bytes are returned alongside the decoded manifest and its canonical signing
// digest: admission persists exactly those bytes, so the durable copy is the
// document the deployment actually verified rather than a re-encoding of it.
func fetchAndVerifyManifestRaw(ctx context.Context, source *UpdateHTTPSource, publicKey ed25519.PublicKey) (hardeningdomain.ReleaseManifest, string, []byte, error) {
	if source == nil {
		return hardeningdomain.ReleaseManifest{}, "", nil, errors.New("release manifest HTTP source is not configured")
	}
	resp, err := source.Do(ctx, http.MethodGet, source.ManifestURL())
	if err != nil {
		return hardeningdomain.ReleaseManifest{}, "", nil, fmt.Errorf("fetch release manifest: %w", err)
	}
	return decodeAndVerifyManifest(resp, publicKey)
}

// fetchAndVerifyManifest is the bytes-free form retained for callers that only
// need the decoded identity.
func fetchAndVerifyManifest(ctx context.Context, source *UpdateHTTPSource, publicKey ed25519.PublicKey) (hardeningdomain.ReleaseManifest, string, error) {
	manifest, digest, _, err := fetchAndVerifyManifestRaw(ctx, source, publicKey)
	return manifest, digest, err
}

// VerifyManifestDocument strictly decodes one bounded signed manifest document
// and proves every surface the deployment trusts: strict schema, canonical
// signing digest, manifest signature, embedded-update contract and payload
// signature. It is the single manifest verification path shared by the HTTP
// source, the durable copy and the release tooling, so a publisher can never
// bless bytes the deployment would refuse, and the deployment can never accept
// a document a signer would not have produced.
func VerifyManifestDocument(data []byte, publicKey ed25519.PublicKey) (hardeningdomain.ReleaseManifest, string, error) {
	if len(data) > maxManifestBytes {
		return hardeningdomain.ReleaseManifest{}, "", errors.New("release manifest exceeds the bounded size")
	}
	manifest, err := decodeManifestDocument(data)
	if err != nil {
		return hardeningdomain.ReleaseManifest{}, "", err
	}
	digest, err := manifest.CanonicalDigest()
	if err != nil {
		return hardeningdomain.ReleaseManifest{}, "", fmt.Errorf("canonicalize release manifest: %w", err)
	}
	if err := manifest.VerifySignature(publicKey); err != nil {
		return hardeningdomain.ReleaseManifest{}, "", fmt.Errorf("release manifest signature rejected: %w", err)
	}
	if err := manifest.ValidateForEmbeddedUpdate(); err != nil {
		return hardeningdomain.ReleaseManifest{}, "", fmt.Errorf("embedded update segment rejected: %w", err)
	}
	if err := manifest.VerifyEmbeddedPayloadSignature(publicKey); err != nil {
		return hardeningdomain.ReleaseManifest{}, "", fmt.Errorf("payload signature rejected: %w", err)
	}
	return manifest, digest, nil
}

// decodeAndVerifyManifest keeps response parsing separate from source policy;
// it is also useful to ensure authentication failures never reach signature
// or staging code. The returned bytes are the bounded response document that
// was decoded and verified.
func decodeAndVerifyManifest(resp *http.Response, publicKey ed25519.PublicKey) (hardeningdomain.ReleaseManifest, string, []byte, error) {
	if resp == nil {
		return hardeningdomain.ReleaseManifest{}, "", nil, errors.New("release manifest response is empty")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return hardeningdomain.ReleaseManifest{}, "", nil, fmt.Errorf(
			"release manifest source answered HTTP %d; refusing admission", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return hardeningdomain.ReleaseManifest{}, "", nil, fmt.Errorf("read release manifest: %w", err)
	}
	manifest, digest, err := VerifyManifestDocument(body, publicKey)
	if err != nil {
		return hardeningdomain.ReleaseManifest{}, "", nil, err
	}
	return manifest, digest, body, nil
}

// FetchAndVerifyManifest retains the original URL-based helper for package
// callers and tests. Production update flow uses the source-aware helper below
// so manifest and payload share one authentication/redirect policy.
func FetchAndVerifyManifest(ctx context.Context, client *http.Client, publicKey ed25519.PublicKey, manifestURL string) (hardeningdomain.ReleaseManifest, string, error) {
	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{ManifestURL: manifestURL, HTTPClient: client})
	if err != nil {
		return hardeningdomain.ReleaseManifest{}, "", err
	}
	return fetchAndVerifyManifest(ctx, source, publicKey)
}

// FetchAndVerifyManifestFromSource is the source-aware public entry point for
// replay checks and other callers that must reuse deployment authentication.
func FetchAndVerifyManifestFromSource(ctx context.Context, source *UpdateHTTPSource, publicKey ed25519.PublicKey) (hardeningdomain.ReleaseManifest, string, error) {
	return fetchAndVerifyManifest(ctx, source, publicKey)
}

// FetchAndVerifyManifestFromSourceRaw is the same verification with the
// received document bytes. The embedded updater uses it so the persisted
// manifest copy is byte-identical to what the deployment verified.
func FetchAndVerifyManifestFromSourceRaw(ctx context.Context, source *UpdateHTTPSource, publicKey ed25519.PublicKey) (hardeningdomain.ReleaseManifest, string, []byte, error) {
	return fetchAndVerifyManifestRaw(ctx, source, publicKey)
}
