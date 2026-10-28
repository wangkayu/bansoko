package application

import (
	"bansoko/internal/observability/outbound"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"bansoko/internal/apperrors"
	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/update/channel"
)

// Release discovery. A deployment may configure a stable, version-free channel
// document (`BANSOKO_UPDATE_CHANNEL_URL`) instead of naming one release in
// `BANSOKO_UPDATE_MANIFEST_URL`. The document only selects which immutable signed
// manifest to fetch; every authority decision stays with the manifest, the
// package namespace and the durable fence.
//
// The two modes are mutually exclusive by configuration: a deployment that sets
// both fails closed at construction instead of silently preferring one, because
// "which release am I allowed to install" must have exactly one answer.
const (
	// EnvChannelURL names the deployment-owned, version-free discovery document.
	EnvChannelURL = "BANSOKO_UPDATE_CHANNEL_URL"
	// EnvPackageRoot names the deployment-owned package namespace a channel
	// document may select a manifest from.
	EnvPackageRoot = "BANSOKO_UPDATE_PACKAGE_ROOT"
	// EnvManifestURL names the legacy exact-release manifest source.
	EnvManifestURL = "BANSOKO_UPDATE_MANIFEST_URL"
)

var (
	// ErrChannelOutsideRoot reports a channel document that names a manifest
	// outside the deployment-owned package namespace. The URL is refused before
	// any request is built, so a deployment credential can never be sent to a
	// host or path the operator did not configure.
	ErrChannelOutsideRoot = errors.New("update channel manifest URL is outside the configured package root")
	// ErrChannelNotConfigured reports a channel mode without its package root.
	ErrChannelNotConfigured = errors.New("update channel discovery requires the package root")
)

// resolution is one resolved candidate: the verified signed manifest, its
// canonical digest, the exact immutable bytes that were verified, the immutable
// manifest URL they came from, and the package source that proved them. Both
// check and admit answer from a resolution; the chain and the operator recovery
// boundary rebuild their payload source from the same URL.
type resolution struct {
	manifest    hardeningdomain.ReleaseManifest
	digest      string
	raw         []byte
	manifestURL string
	source      *UpdateHTTPSource
	fromChannel bool
}

// channelResolver owns the deployment's discovery configuration. It is built
// once per process (the channel URL is deployment configuration, not runtime
// state) and performs no I/O at construction.
type channelResolver struct {
	documentURL    string
	packageRoot    string
	publicKey      ed25519.PublicKey
	documentSource *UpdateHTTPSource
	packageConfig  UpdateHTTPSourceConfig
	rootScheme     string
	rootHost       string
	rootDirectory  string
}

// newChannelResolver validates the discovery configuration and builds the two
// request identities it needs: the channel document source (authenticated only
// when it lives on the package host, so a package token is never sent to a
// different host) and the package source template for the manifest the document
// names.
func newChannelResolver(documentURL, packageRoot string, config UpdateHTTPSourceConfig, publicKey ed25519.PublicKey) (*channelResolver, error) {
	documentURL = strings.TrimSpace(documentURL)
	packageRoot = strings.TrimSpace(packageRoot)
	if documentURL == "" {
		return nil, fmt.Errorf("%w: channel URL is required", ErrUpdateSourceURL)
	}
	if packageRoot == "" {
		return nil, fmt.Errorf("%w: %s is required when %s is configured", ErrChannelNotConfigured,
			EnvPackageRoot, EnvChannelURL)
	}
	root, err := parseUpdateURL(packageRoot)
	if err != nil {
		return nil, err
	}
	if config.Production && !strings.EqualFold(root.Scheme, "https") {
		return nil, fmt.Errorf("%w: production update sources require HTTPS", ErrUpdateSourceURL)
	}

	documentConfig := config
	documentConfig.ManifestURL = documentURL
	parsedDocument, err := parseUpdateURL(documentURL)
	if err != nil {
		return nil, err
	}
	if config.Production && !strings.EqualFold(parsedDocument.Scheme, "https") {
		return nil, fmt.Errorf("%w: production update sources require HTTPS", ErrUpdateSourceURL)
	}
	documentSource, err := NewUpdateHTTPSource(documentConfig)
	if err != nil {
		return nil, err
	}
	packageConfig := config
	packageConfig.ManifestURL = ""
	return &channelResolver{
		documentURL:    documentURL,
		packageRoot:    packageRoot,
		publicKey:      publicKey,
		documentSource: documentSource,
		packageConfig:  packageConfig,
		rootScheme:     strings.ToLower(root.Scheme),
		rootHost:       updateHostIdentity(root),
		rootDirectory:  updatePackageDirectory(root.Path),
	}, nil
}

// resolve reads the channel document, verifies it, fetches the immutable
// manifest it names through the deployment's package policy, and proves the two
// agree. Every failure is a typed refusal: discovery never falls back to a
// cached, previous or guessed candidate.
func (r *channelResolver) resolve(ctx context.Context) (resolution, error) {
	if r == nil {
		return resolution{}, &apperrors.Business{Status: 503, Code: "SERVICE_NOT_CONFIGURED", Message: "update channel source is not configured."}
	}
	document, err := r.fetchDocument(ctx)
	if err != nil {
		return resolution{}, err
	}
	source, err := r.packageSourceFor(document.ManifestURL)
	if err != nil {
		if errors.Is(err, ErrChannelOutsideRoot) {
			return resolution{}, &apperrors.Business{
				Status:  503,
				Code:    "UPDATE_CHANNEL_MISMATCH",
				Message: "The update channel does not point inside the configured package namespace.",
			}
		}
		return resolution{}, &apperrors.Business{
			Status:  503,
			Code:    "UPDATE_CHANNEL_INVALID",
			Message: "The update channel document is not usable.",
		}
	}
	manifest, digest, raw, err := FetchAndVerifyManifestFromSourceRaw(ctx, source, r.publicKey)
	if err != nil {
		business := &apperrors.Business{
			Status:  503,
			Code:    "UPDATE_CHANNEL_UNAVAILABLE",
			Message: "The update channel names a release that could not be verified.",
		}
		if failure, ok := outbound.FailureOf(err); ok {
			return resolution{}, outbound.WithFailure(business, failure)
		}
		return resolution{}, business
	}
	if err := document.Matches(manifest.ReleaseID, manifest.Version, digest, manifest.ReleaseSequence); err != nil {
		return resolution{}, &apperrors.Business{
			Status:  503,
			Code:    "UPDATE_CHANNEL_MISMATCH",
			Message: "The update channel and its signed release manifest disagree.",
		}
	}
	return resolution{manifest: manifest, digest: digest, raw: raw, manifestURL: source.ManifestURL(), source: source, fromChannel: true}, nil
}

// fetchDocument downloads and verifies the bounded channel document.
func (r *channelResolver) fetchDocument(ctx context.Context) (channel.Document, error) {
	resp, err := r.documentSource.Do(ctx, http.MethodGet, r.documentURL)
	if err != nil {
		business := &apperrors.Business{
			Status:  503,
			Code:    "UPDATE_CHANNEL_UNAVAILABLE",
			Message: "The update channel could not be read.",
		}
		if failure, ok := outbound.FailureOf(err); ok {
			return channel.Document{}, outbound.WithFailure(business, failure)
		}
		return channel.Document{}, business
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return channel.Document{}, outbound.WithFailure(&apperrors.Business{
			Status:  503,
			Code:    "UPDATE_CHANNEL_UNAVAILABLE",
			Message: "The update channel could not be read.",
		}, outbound.Describe(nil, resp.Request, resp, ""))
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, channel.MaxDocumentBytes+1))
	if readErr != nil {
		return channel.Document{}, &apperrors.Business{
			Status:  503,
			Code:    "UPDATE_CHANNEL_UNAVAILABLE",
			Message: "The update channel could not be read.",
		}
	}
	document, decodeErr := channel.Decode(body)
	if decodeErr != nil {
		return channel.Document{}, &apperrors.Business{
			Status:  503,
			Code:    "UPDATE_CHANNEL_INVALID",
			Message: "The update channel document is not usable.",
		}
	}
	if verifyErr := document.VerifySignature(r.publicKey); verifyErr != nil {
		return channel.Document{}, &apperrors.Business{
			Status:  503,
			Code:    "UPDATE_CHANNEL_INVALID",
			Message: "The update channel document is not usable.",
		}
	}
	return document, nil
}

// packageSourceFor builds the request identity for one immutable manifest URL
// and proves it stays inside the deployment-owned package namespace.
func (r *channelResolver) packageSourceFor(manifestURL string) (*UpdateHTTPSource, error) {
	if r == nil {
		return nil, ErrChannelNotConfigured
	}
	return ResolvePackageSource(manifestURL, r.packageRoot, r.packageConfig)
}

// ResolvePackageSource builds the source for one immutable manifest URL. When
// packageRoot is configured the URL must stay inside it (same scheme, same host,
// same package directory subtree), so a resolved or replayed URL can never
// redirect a deployment credential to an unconfigured host or path.
func ResolvePackageSource(manifestURL, packageRoot string, config UpdateHTTPSourceConfig) (*UpdateHTTPSource, error) {
	manifestURL = strings.TrimSpace(manifestURL)
	if manifestURL == "" {
		return nil, fmt.Errorf("%w: manifest URL is required", ErrUpdateSourceURL)
	}
	if strings.TrimSpace(packageRoot) == "" {
		config.ManifestURL = manifestURL
		return NewUpdateHTTPSource(config)
	}
	root, err := parseUpdateURL(packageRoot)
	if err != nil {
		return nil, err
	}
	target, err := parseUpdateURL(manifestURL)
	if err != nil {
		return nil, err
	}
	if err := packageRootContains(root, target); err != nil {
		return nil, err
	}
	config.ManifestURL = manifestURL
	return NewUpdateHTTPSource(config)
}

// packageRootContains proves target is inside the configured package namespace.
func packageRootContains(root, target *url.URL) error {
	if root == nil || target == nil {
		return fmt.Errorf("%w: missing package root or target", ErrChannelOutsideRoot)
	}
	if !strings.EqualFold(target.Scheme, root.Scheme) || updateHostIdentity(target) != updateHostIdentity(root) {
		return fmt.Errorf("%w: target host or scheme differs from the package root", ErrChannelOutsideRoot)
	}
	if !pathWithinDirectory(target.Path, updatePackageDirectory(root.Path)) {
		return fmt.Errorf("%w: target path is outside the package root", ErrChannelOutsideRoot)
	}
	return nil
}
