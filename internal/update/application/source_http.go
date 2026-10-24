package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

var (
	// ErrUpdateSourceURL identifies a deployment source URL that cannot be used
	// by the update client. The error intentionally carries no complete URL
	// details.
	ErrUpdateSourceURL = errors.New("update source URL rejected")
	// ErrUpdateRedirect identifies a redirect outside the configured package.
	ErrUpdateRedirect = errors.New("update redirect rejected")
)

// UpdateHTTPSourceConfig describes the deployment-owned source policy. An
// update source is always public and anonymous: there is no credential
// surface, so nothing a deployment sets can make the updater authenticate.
// Production controls the HTTPS requirement; development/test fixtures may
// use HTTP explicitly.
type UpdateHTTPSourceConfig struct {
	ManifestURL string
	Production  bool
	HTTPClient  *http.Client
}

// UpdateSourceConfig is a descriptive alias for the HTTP source settings.
type UpdateSourceConfig = UpdateHTTPSourceConfig

// UpdateHTTPSource is the sole request builder for an embedded update source.
// It fixes the scheme, host, and package directory at construction time and
// strips every credential header from its requests.
type UpdateHTTPSource struct {
	manifestURL string
	scheme      string
	host        string
	packagePath string
	client      *http.Client
}

// HTTPSource is a descriptive alias for the update source type.
type HTTPSource = UpdateHTTPSource

// NewUpdateHTTPSource validates deployment source settings and builds a client
// with a redirect policy that cannot be weakened by a caller's HTTP client.
func NewUpdateHTTPSource(config UpdateHTTPSourceConfig) (*UpdateHTTPSource, error) {
	manifestURL := strings.TrimSpace(config.ManifestURL)
	if manifestURL == "" {
		return nil, fmt.Errorf("%w: manifest URL is required", ErrUpdateSourceURL)
	}

	parsed, err := parseUpdateURL(manifestURL)
	if err != nil {
		return nil, err
	}
	if config.Production && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("%w: production update sources require HTTPS", ErrUpdateSourceURL)
	}

	source := &UpdateHTTPSource{
		manifestURL: parsed.String(),
		scheme:      strings.ToLower(parsed.Scheme),
		host:        updateHostIdentity(parsed),
		packagePath: updatePackageDirectory(parsed.Path),
	}

	baseClient := config.HTTPClient
	if baseClient == nil {
		baseClient = &http.Client{Timeout: 5 * time.Minute}
	}
	client := *baseClient
	baseTransport := baseClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		// A redirect is safe only when it remains inside the deployment-owned
		// package identity. Credentials are stripped explicitly instead of
		// trusting net/http's header-copy behavior.
		if err := source.validateTarget(req.URL, true); err != nil {
			return err
		}
		source.stripCredentials(req)
		return nil
	}
	source.client = &client
	source.client.Transport = updateSourceTransport{source: source, base: baseTransport}
	return source, nil
}

// NewHTTPSource is a convenience alias for NewUpdateHTTPSource.
func NewHTTPSource(config UpdateHTTPSourceConfig) (*UpdateHTTPSource, error) {
	return NewUpdateHTTPSource(config)
}

// NewUpdateSource is a convenience alias for NewUpdateHTTPSource.
func NewUpdateSource(config UpdateHTTPSourceConfig) (*UpdateHTTPSource, error) {
	return NewUpdateHTTPSource(config)
}

// NewRequest constructs a source-local HTTP request. Callers normally use Do;
// this method is exported for tests and narrowly scoped update adapters.
func (s *UpdateHTTPSource) NewRequest(ctx context.Context, method, rawURL string) (*http.Request, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("update HTTP source is not configured")
	}
	parsed, err := s.parseTarget(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build update source request: %w", ErrUpdateSourceURL)
	}
	s.stripCredentials(req)
	return req, nil
}

// Request is a convenience alias for NewRequest.
func (s *UpdateHTTPSource) Request(ctx context.Context, method, rawURL string) (*http.Request, error) {
	return s.NewRequest(ctx, method, rawURL)
}

// Do executes a request built under the source policy.
func (s *UpdateHTTPSource) Do(ctx context.Context, method, rawURL string) (*http.Response, error) {
	req, err := s.NewRequest(ctx, method, rawURL)
	if err != nil {
		return nil, err
	}
	return s.client.Do(req)
}

// Client returns the policy-bound HTTP client. The returned client must not be
// replaced with an ordinary client because that would remove redirect checks.
func (s *UpdateHTTPSource) Client() *http.Client {
	if s == nil {
		return nil
	}
	return s.client
}

// ManifestURL returns the canonical configured manifest URL. It contains no
// credential because URL query, fragment, and userinfo are rejected.
func (s *UpdateHTTPSource) ManifestURL() string {
	if s == nil {
		return ""
	}
	return s.manifestURL
}

// PayloadURL resolves the fixed payload sibling inside the configured package
// directory. It does not trust any URL or file name from the manifest body.
func (s *UpdateHTTPSource) PayloadURL() (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("update HTTP source is not configured")
	}
	return s.localURL(path.Join(strings.TrimSuffix(s.packagePath, "/"), PayloadFileName))
}

func (s *UpdateHTTPSource) parseTarget(rawURL string) (*url.URL, error) {
	parsed, err := parseUpdateURL(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	if err := s.validateTarget(parsed, false); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (s *UpdateHTTPSource) localURL(targetPath string) (string, error) {
	parsed, err := url.Parse(s.manifestURL)
	if err != nil {
		return "", fmt.Errorf("%w: configured source cannot be rebuilt", ErrUpdateSourceURL)
	}
	parsed.Path = targetPath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String(), nil
}

func (s *UpdateHTTPSource) validateTarget(target *url.URL, redirect bool) error {
	if target == nil {
		return fmt.Errorf("%w: missing redirect target", ErrUpdateRedirect)
	}
	if err := validateURLParts(target); err != nil {
		if redirect {
			return fmt.Errorf("%w: unsafe redirect", ErrUpdateRedirect)
		}
		return err
	}
	if strings.ToLower(target.Scheme) != s.scheme || updateHostIdentity(target) != s.host {
		if redirect {
			return fmt.Errorf("%w: redirect changes source identity", ErrUpdateRedirect)
		}
		return fmt.Errorf("%w: URL is outside the configured source", ErrUpdateSourceURL)
	}
	if !pathWithinDirectory(target.Path, s.packagePath) {
		if redirect {
			return fmt.Errorf("%w: redirect leaves the package path", ErrUpdateRedirect)
		}
		return fmt.Errorf("%w: URL is outside the configured package path", ErrUpdateSourceURL)
	}
	return nil
}

// stripCredentials removes every credential header from one source-local
// request. An update source is anonymous by construction: a caller-supplied
// Authorization or any well-known forge or CI token header is always
// cleared, so the updater can only ever send a request with no credential
// at all.
func (s *UpdateHTTPSource) stripCredentials(req *http.Request) {
	if s == nil || req == nil {
		return
	}
	req.Header.Del("Authorization")
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("DEPLOY-TOKEN")
	req.Header.Del("PRIVATE-TOKEN")
	req.Header.Del("JOB-TOKEN")
}

type updateSourceTransport struct {
	source *UpdateHTTPSource
	base   http.RoundTripper
}

func (t updateSourceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.source == nil {
		return nil, errors.New("update HTTP source is not configured")
	}
	if err := t.source.validateTarget(req.URL, false); err != nil {
		return nil, err
	}
	// Clone before changing headers so a redirect implementation or caller
	// cannot observe mutations on a request it still owns.
	request := req.Clone(req.Context())
	t.source.stripCredentials(request)
	return t.base.RoundTrip(request)
}

func parseUpdateURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: manifest URL is empty", ErrUpdateSourceURL)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest URL is invalid", ErrUpdateSourceURL)
	}
	if err := validateURLParts(parsed); err != nil {
		return nil, err
	}
	if !canonicalUpdatePath(parsed.Path) {
		return nil, fmt.Errorf("%w: URL path is not canonical", ErrUpdateSourceURL)
	}
	return parsed, nil
}

func validateURLParts(parsed *url.URL) error {
	if parsed == nil || parsed.Scheme == "" || parsed.Host == "" || !parsed.IsAbs() || parsed.Opaque != "" {
		return fmt.Errorf("%w: URL must be absolute", ErrUpdateSourceURL)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: URL userinfo is forbidden", ErrUpdateSourceURL)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("%w: URL query is forbidden", ErrUpdateSourceURL)
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return fmt.Errorf("%w: URL fragment is forbidden", ErrUpdateSourceURL)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("%w: URL scheme is not HTTP(S)", ErrUpdateSourceURL)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("%w: URL host is empty", ErrUpdateSourceURL)
	}
	if strings.Contains(parsed.Path, "\\") || strings.Contains(parsed.RawPath, "\\") {
		return fmt.Errorf("%w: URL path contains a backslash", ErrUpdateSourceURL)
	}
	return nil
}

func canonicalUpdatePath(rawPath string) bool {
	if rawPath == "" {
		return true
	}
	if !strings.HasPrefix(rawPath, "/") {
		return false
	}
	cleaned := path.Clean(rawPath)
	if strings.HasSuffix(rawPath, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return rawPath == cleaned
}

func updatePackageDirectory(rawPath string) string {
	if rawPath == "" {
		return "/"
	}
	directory := path.Dir(rawPath)
	if directory == "." || directory == "" {
		directory = "/"
	}
	if !strings.HasPrefix(directory, "/") {
		directory = "/" + directory
	}
	if !strings.HasSuffix(directory, "/") {
		directory += "/"
	}
	return directory
}

func pathWithinDirectory(rawPath, directory string) bool {
	if !canonicalUpdatePath(rawPath) {
		return false
	}
	cleanedDirectory := updatePackageDirectory(strings.TrimSuffix(directory, "/") + "/file")
	if cleanedDirectory == "/" {
		return strings.HasPrefix(rawPath, "/")
	}
	return strings.HasPrefix(rawPath, cleanedDirectory)
}

func updateHostIdentity(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" {
		return host
	}
	return host + ":" + port
}
