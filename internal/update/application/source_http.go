package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode"
)

const (
	// UpdateAuthModeNone marks a public source that never carries a
	// credential or an authentication header.
	UpdateAuthModeNone = "none"
	// UpdateAuthModeGiteaPAT marks a private Gitea generic-package source
	// authenticated with a read-only PAT sent as `Authorization: token <PAT>`.
	// It is the only private mode: legacy GitLab deploy-token modes and any
	// other authentication header are rejected at construction.
	UpdateAuthModeGiteaPAT = "gitea_pat"
	// giteaCredentialHeader is the single header a Gitea PAT may be sent on. A
	// deployment may configure it explicitly (empty means the same header), but
	// no other header is accepted, so a legacy DEPLOY-TOKEN/PRIVATE-TOKEN shape
	// cannot be reintroduced through configuration.
	giteaCredentialHeader = "Authorization"
	// giteaCredentialPrefix is the authentication scheme Gitea expects before the
	// raw PAT value.
	giteaCredentialPrefix    = "token "
	maxUpdateCredentialBytes = 4096
)

var (
	// ErrUpdateSourceURL identifies a deployment source URL that cannot be used
	// by the update client. The error intentionally carries no credential or
	// complete URL details.
	ErrUpdateSourceURL = errors.New("update source URL rejected")
	// ErrUpdateCredential identifies malformed deployment update credentials.
	ErrUpdateCredential = errors.New("update credential rejected")
	// ErrUpdateRedirect identifies a redirect outside the configured package.
	ErrUpdateRedirect = errors.New("update redirect rejected")
)

// UpdateHTTPSourceConfig describes the deployment-owned source policy. A private
// source must name UpdateAuthModeGiteaPAT and point CredentialFile at a
// single-line read-only Gitea PAT file; an unset file with an unset or `none`
// mode describes a public source that carries no credential. Production controls
// the HTTPS requirement; development/test fixtures may use HTTP explicitly.
type UpdateHTTPSourceConfig struct {
	ManifestURL      string
	CredentialFile   string
	CredentialHeader string
	AuthMode         string
	Production       bool
	HTTPClient       *http.Client
}

// UpdateSourceConfig is a descriptive alias for the HTTP source settings.
type UpdateSourceConfig = UpdateHTTPSourceConfig

// UpdateHTTPSource is the sole request builder for an embedded update source.
// It fixes the scheme, host, and package directory at construction time and
// applies the Gitea PAT credential only to requests that remain within that
// source identity.
type UpdateHTTPSource struct {
	manifestURL string
	scheme      string
	host        string
	packagePath string
	header      string
	token       string
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

	mode := strings.TrimSpace(config.AuthMode)
	if mode == "" {
		// An empty mode is equivalent to none. A credential file alone never
		// selects a private mode, so a partially removed GitLab-style deployment
		// fails closed instead of silently authenticating.
		mode = UpdateAuthModeNone
	}
	if mode != UpdateAuthModeNone && mode != UpdateAuthModeGiteaPAT {
		// Retired GitLab deploy-token modes are refused explicitly rather than
		// mapped onto the Gitea PAT mode.
		return nil, fmt.Errorf("%w: unsupported update auth mode", ErrUpdateCredential)
	}
	if mode == UpdateAuthModeGiteaPAT {
		if strings.TrimSpace(config.CredentialFile) == "" {
			return nil, fmt.Errorf("%w: private update source requires a credential file", ErrUpdateCredential)
		}
	}
	if mode == UpdateAuthModeNone && strings.TrimSpace(config.CredentialFile) != "" {
		return nil, fmt.Errorf("%w: public update source cannot use a credential file: name %s explicitly", ErrUpdateCredential, UpdateAuthModeGiteaPAT)
	}

	header, err := parseUpdateCredentialHeader(config.CredentialHeader)
	if err != nil {
		return nil, err
	}
	if mode == UpdateAuthModeNone {
		// A public source forbids credentials and any authentication header.
		header = ""
	} else if mode == UpdateAuthModeGiteaPAT {
		// The header is built internally; a configuration value can never
		// move the PAT onto another header name.
		header = giteaCredentialHeader
	}
	token, err := readUpdateCredentialFile(config.CredentialFile)
	if err != nil {
		return nil, err
	}

	source := &UpdateHTTPSource{
		manifestURL: parsed.String(),
		scheme:      strings.ToLower(parsed.Scheme),
		host:        updateHostIdentity(parsed),
		packagePath: updatePackageDirectory(parsed.Path),
		header:      header,
		token:       token,
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
		// package identity. Rebuild the Gitea PAT header explicitly instead of
		// trusting net/http's header-copy behavior.
		if err := source.validateTarget(req.URL, true); err != nil {
			return err
		}
		source.applyCredential(req)
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
	s.applyCredential(req)
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

// CredentialHeader returns the allowlisted header name without exposing the
// secret value.
func (s *UpdateHTTPSource) CredentialHeader() string {
	if s == nil {
		return ""
	}
	return s.header
}

// HasCredential reports whether a Gitea PAT was loaded, without returning
// the token itself.
func (s *UpdateHTTPSource) HasCredential() bool {
	return s != nil && s.token != ""
}

// credentialValue assembles the Gitea authentication header value from the
// in-memory PAT. It is built at request time so the raw token never appears
// in a stored header value, a log line, or an error string.
func (s *UpdateHTTPSource) credentialValue() string {
	if s == nil || s.token == "" {
		return ""
	}
	return giteaCredentialPrefix + s.token
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

// applyCredential builds the authentication headers for one source-local
// request. A caller-supplied Authorization and the retired GitLab
// DEPLOY-TOKEN / PRIVATE-TOKEN / JOB-TOKEN headers are always cleared, so a
// private source can only ever send a credential it produced itself and a
// public source can only ever send none.
func (s *UpdateHTTPSource) applyCredential(req *http.Request) {
	if s == nil || req == nil {
		return
	}
	req.Header.Del("Authorization")
	req.Header.Del("DEPLOY-TOKEN")
	req.Header.Del("PRIVATE-TOKEN")
	req.Header.Del("JOB-TOKEN")
	if s.token != "" && s.header == giteaCredentialHeader {
		req.Header.Set(s.header, s.credentialValue())
	}
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
	t.source.applyCredential(request)
	return t.base.RoundTrip(request)
}

// parseUpdateCredentialHeader resolves the one header a Gitea PAT may travel
// on. An empty configuration means the canonical Authorization header; every
// other value, including the retired GitLab DEPLOY-TOKEN header, is refused so
// a deployment cannot reintroduce a non-Gitea authentication shape.
func parseUpdateCredentialHeader(raw string) (string, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("%w: credential header contains a line break", ErrUpdateCredential)
	}
	header := strings.TrimSpace(raw)
	if header == "" {
		return giteaCredentialHeader, nil
	}
	if header != giteaCredentialHeader {
		return "", fmt.Errorf("%w: unsupported credential header: only %s is accepted", ErrUpdateCredential, giteaCredentialHeader)
	}
	return header, nil
}

// ParseUpdateCredential validates the contents of a deployment credential
// file. A single final LF is accepted for normal POSIX secret files; CR, CRLF,
// empty values, and additional lines are rejected.
func ParseUpdateCredential(raw string) (string, error) {
	if strings.ContainsAny(raw, "\r\n") {
		if strings.ContainsRune(raw, '\r') {
			return "", fmt.Errorf("%w: credential contains CR", ErrUpdateCredential)
		}
		if strings.Count(raw, "\n") > 1 || !strings.HasSuffix(raw, "\n") {
			return "", fmt.Errorf("%w: credential contains multiple lines", ErrUpdateCredential)
		}
		raw = strings.TrimSuffix(raw, "\n")
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%w: credential is empty", ErrUpdateCredential)
	}
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("%w: credential contains control characters", ErrUpdateCredential)
	}
	return value, nil
}

func readUpdateCredentialFile(filename string) (string, error) {
	if strings.TrimSpace(filename) == "" {
		return "", nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("%w: credential file could not be read", ErrUpdateCredential)
	}
	if len(data) > maxUpdateCredentialBytes {
		return "", fmt.Errorf("%w: credential file is too large", ErrUpdateCredential)
	}
	return ParseUpdateCredential(string(data))
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
