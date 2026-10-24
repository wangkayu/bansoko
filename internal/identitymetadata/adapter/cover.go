package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	identitymetadataport "bansoko/internal/identitymetadata/port"
	"golang.org/x/sync/singleflight"
)

// Provider names the cover proxy accepts. They match provider_image_cache's
// provider CHECK and select the connection (and so the network route) the
// image is fetched through.
const (
	CoverProviderMikan = "mikan"
	CoverProviderAniBT = "anibt"
	CoverProviderTMDB  = "tmdb"
)

// CoverPathPrefix is the same-origin path the API emits for a proxied image.
const CoverPathPrefix = "/api/v1/images/cover/"

var (
	// ErrCoverNotFound covers an unknown key, a source the policy no longer
	// allows, and an upstream 404. The transport maps it to 404.
	ErrCoverNotFound = errors.New("cover not found")
	// ErrCoverUnavailable is any other upstream failure; it maps to 502.
	ErrCoverUnavailable = errors.New("cover upstream unavailable")

	coverKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	maxCoverBytes        = 2 << 20
	maxCoverURLBytes     = 2048
	maxCoverRedirects    = 3
	coverFetchTimeout    = 15 * time.Second
	defaultCoverTTL      = 7 * 24 * time.Hour
	coverNegativeTTL     = 5 * time.Minute
	maxCoverNegativeKeys = 1024
	tmdbImageHost        = "image.tmdb.org"
)

// CoverKey is the opaque identifier the browser sees for one provider image:
// the SHA-256 of the provider and canonical source URL. It reveals nothing
// about the upstream URL and cannot be used to request one.
func CoverKey(provider, canonicalURL string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + canonicalURL))
	return hex.EncodeToString(sum[:])
}

// ConfiguredCoverProxy serves provider cover images (Mikan entries without a
// Bangumi binding, TMDB candidate posters) through the same-origin API. The
// upstream URL is only ever read from the server's own registry; a request
// carries just an opaque key. Fetches are limited to the provider's own hosts,
// go out through the provider connection's network route, and are bounded in
// size, time, redirects and content type.
type ConfiguredCoverProxy struct {
	Connections    ConnectionSource
	ClientResolver HTTPClientResolver
	Client         *http.Client
	Store          identitymetadataport.ProviderImageStore
	TTL            time.Duration

	flights singleflight.Group
	mu      sync.Mutex
	failed  map[string]time.Time
}

func NewCoverProxy(connections ConnectionSource, clientResolver HTTPClientResolver, store identitymetadataport.ProviderImageStore) *ConfiguredCoverProxy {
	return &ConfiguredCoverProxy{Connections: connections, ClientResolver: clientResolver, Store: store, TTL: defaultCoverTTL}
}

func coverConnectionKind(provider string) (configurationdomain.ConnectionKind, bool) {
	switch provider {
	case CoverProviderMikan:
		return configurationdomain.ConnectionMikan, true
	case CoverProviderAniBT:
		return configurationdomain.ConnectionAniBT, true
	case CoverProviderTMDB:
		return configurationdomain.ConnectionTMDB, true
	}
	return "", false
}

// coverPolicy is the set of origins one provider's images may come from.
type coverPolicy struct {
	connection configurationdomain.Connection
	origins    []coverOrigin
}

type coverOrigin struct {
	scheme, host, port string
	subdomains         bool
}

func (o coverOrigin) allows(u *url.URL) bool {
	if u.Scheme != o.scheme || portOf(u) != o.port {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == o.host {
		return true
	}
	return o.subdomains && strings.HasSuffix(host, "."+o.host)
}

func portOf(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func (p coverPolicy) allows(u *url.URL) bool {
	if u == nil || u.User != nil || u.Hostname() == "" {
		return false
	}
	for _, origin := range p.origins {
		if origin.allows(u) {
			return true
		}
	}
	return false
}

func (p *ConfiguredCoverProxy) policy(ctx context.Context, provider string) (coverPolicy, error) {
	kind, ok := coverConnectionKind(provider)
	if !ok {
		return coverPolicy{}, ErrCoverNotFound
	}
	if p.Connections == nil {
		return coverPolicy{}, errors.New("cover connection source is not configured")
	}
	connections, err := p.Connections.ListConnections(ctx)
	if err != nil {
		return coverPolicy{}, fmt.Errorf("list %s connections: %w", kind, err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, kind)
	if errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
		fallback, ok := configurationdomain.DefaultConnectionForKind(kind)
		if !ok {
			return coverPolicy{}, err
		}
		connection, err = fallback, nil
	}
	if err != nil {
		return coverPolicy{}, err
	}
	policy := coverPolicy{connection: connection}
	if provider == CoverProviderTMDB {
		// TMDB serves images from a fixed CDN host, separate from its API.
		policy.origins = []coverOrigin{{scheme: "https", host: tmdbImageHost, port: "443"}}
		return policy, nil
	}
	endpoint, err := url.Parse(strings.TrimSpace(connection.Endpoint))
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return coverPolicy{}, errors.New("provider endpoint is invalid")
	}
	host := strings.ToLower(endpoint.Hostname())
	policy.origins = []coverOrigin{{
		scheme: endpoint.Scheme, host: host, port: portOf(endpoint),
		// Provider image CDNs live on subdomains (r2.anibt.net); an IP literal has none.
		subdomains: net.ParseIP(host) == nil,
	}}
	return policy, nil
}

// canonicalCoverURL normalizes a provider image URL; "" means unusable.
func canonicalCoverURL(raw string) (*url.URL, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxCoverURLBytes {
		return nil, ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Opaque != "" {
		return nil, ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	canonical := parsed.String()
	if len(canonical) > maxCoverURLBytes {
		return nil, ""
	}
	return parsed, canonical
}

// mikanCoverRendition is the resize query Mikan's image host serves without
// cropping: the homepage stores `?width=400&height=400&format=webp`, a
// centre-cropped square, while width alone keeps the 848x1200 aspect ratio.
const mikanCoverRendition = "width=600&format=webp"

// providerCoverRendition rewrites a provider cover URL to the rendition the
// cards should load. It runs before canonicalisation and the host policy, so
// it only ever changes the query of a URL the policy then judges as usual;
// covers already stored in catalog hints benefit at read time.
func providerCoverRendition(provider, raw string) string {
	if provider != CoverProviderMikan {
		return raw
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return raw
	}
	parsed.RawQuery = mikanCoverRendition
	return parsed.String()
}

// CoverPaths registers the allowed upstream image URLs and returns each one's
// same-origin proxy path. A URL the provider policy rejects is absent from the
// result, so a caller never emits a path (or the URL) for it. On a registry
// failure the result is empty: callers show placeholders rather than leaking
// the third-party URL.
func (p *ConfiguredCoverProxy) CoverPaths(ctx context.Context, provider string, rawURLs []string) map[string]string {
	result := map[string]string{}
	if p == nil || p.Store == nil || len(rawURLs) == 0 {
		return result
	}
	policy, err := p.policy(ctx, provider)
	if err != nil {
		return result
	}
	refs := make([]identitymetadataport.ProviderImageRef, 0, len(rawURLs))
	paths := make(map[string]string, len(rawURLs))
	seen := map[string]struct{}{}
	for _, raw := range rawURLs {
		if _, dup := seen[raw]; dup {
			continue
		}
		seen[raw] = struct{}{}
		parsed, canonical := canonicalCoverURL(providerCoverRendition(provider, raw))
		if parsed == nil || !policy.allows(parsed) {
			continue
		}
		key := CoverKey(provider, canonical)
		refs = append(refs, identitymetadataport.ProviderImageRef{Key: key, SourceURL: canonical})
		paths[raw] = CoverPathPrefix + key
	}
	for start := 0; start < len(refs); start += 500 {
		end := min(start+500, len(refs))
		if err := p.Store.Register(ctx, provider, refs[start:end]); err != nil {
			return map[string]string{}
		}
	}
	return paths
}

// FetchCover returns the image bytes for a registered key, from the durable
// cache when fresh and from the provider otherwise. A stale cached image is
// served when the refresh fails.
func (p *ConfiguredCoverProxy) FetchCover(ctx context.Context, key string) (io.ReadCloser, string, error) {
	if p == nil || p.Store == nil || !coverKeyPattern.MatchString(key) {
		return nil, "", ErrCoverNotFound
	}
	entry, found, err := p.Store.Get(ctx, key)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", ErrCoverNotFound
	}
	if entry.HasBytes && entry.ExpiresAt.After(time.Now()) {
		return io.NopCloser(bytes.NewReader(entry.ImageBytes)), entry.ContentType, nil
	}
	stale := func(cause error) (io.ReadCloser, string, error) {
		if entry.HasBytes {
			return io.NopCloser(bytes.NewReader(entry.ImageBytes)), entry.ContentType, nil
		}
		return nil, "", cause
	}
	if p.recentlyFailed(key) {
		return stale(ErrCoverUnavailable)
	}
	value, err, _ := p.flights.Do(key, func() (any, error) {
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), coverFetchTimeout+5*time.Second)
		defer cancel()
		return p.refresh(flightCtx, entry)
	})
	if err != nil {
		p.markFailed(key)
		if errors.Is(err, ErrCoverNotFound) {
			return nil, "", err
		}
		return stale(fmt.Errorf("%w: %v", ErrCoverUnavailable, err))
	}
	fetched := value.(fetchedCover)
	return io.NopCloser(bytes.NewReader(fetched.data)), fetched.contentType, nil
}

type fetchedCover struct {
	data        []byte
	contentType string
}

func (p *ConfiguredCoverProxy) refresh(ctx context.Context, entry identitymetadataport.ProviderImageEntry) (fetchedCover, error) {
	policy, err := p.policy(ctx, entry.Provider)
	if err != nil {
		return fetchedCover{}, err
	}
	source, canonical := canonicalCoverURL(entry.SourceURL)
	if source == nil || canonical != entry.SourceURL || !policy.allows(source) {
		// Registered under an endpoint or host the provider no longer allows.
		return fetchedCover{}, ErrCoverNotFound
	}
	client, err := p.client(ctx, policy)
	if err != nil {
		return fetchedCover{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, coverFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return fetchedCover{}, ErrCoverNotFound
	}
	request.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif")
	response, err := client.Do(request)
	if err != nil {
		return fetchedCover{}, fmt.Errorf("request cover image: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return fetchedCover{}, ErrCoverNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fetchedCover{}, fmt.Errorf("cover image host returned status %d", response.StatusCode)
	}
	if response.ContentLength > maxCoverBytes {
		return fetchedCover{}, fmt.Errorf("cover image exceeds %d bytes", maxCoverBytes)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCoverBytes+1))
	if err != nil {
		return fetchedCover{}, fmt.Errorf("read cover image: %w", err)
	}
	if len(data) > maxCoverBytes {
		return fetchedCover{}, fmt.Errorf("cover image exceeds %d bytes", maxCoverBytes)
	}
	contentType, ok := sniffCoverImage(response.Header.Get("Content-Type"), data)
	if !ok {
		return fetchedCover{}, errors.New("cover response is not a supported image")
	}
	now := time.Now().UTC()
	ttl := p.TTL
	if ttl <= 0 {
		ttl = defaultCoverTTL
	}
	// The bytes are good; serve them even if the cache write fails.
	_ = p.Store.Store(ctx, entry.Key, data, contentType, now, now.Add(ttl))
	return fetchedCover{data: data, contentType: contentType}, nil
}

// client returns the provider connection's routed client with redirects
// pinned to the same policy, so an allowed host cannot bounce the fetch to an
// internal or foreign one.
func (p *ConfiguredCoverProxy) client(ctx context.Context, policy coverPolicy) (*http.Client, error) {
	base := p.Client
	if p.ClientResolver != nil {
		resolved, err := p.ClientResolver.ClientForConnection(ctx, policy.connection)
		if err != nil {
			return nil, fmt.Errorf("resolve %s network route: %w", policy.connection.Kind, err)
		}
		if resolved != nil {
			base = resolved
		}
	}
	if base == nil {
		base = &http.Client{Transport: hardeningtransport.DirectTransport()}
	}
	pinned := *base
	pinned.Timeout = 0 // the request context carries the deadline
	pinned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxCoverRedirects {
			return errors.New("too many cover redirects")
		}
		if !policy.allows(req.URL) {
			return errors.New("cover redirect leaves the provider hosts")
		}
		return nil
	}
	return &pinned, nil
}

// sniffCoverImage decides the stored content type from the bytes, never from
// the upstream header alone. SVG, HTML and anything unrecognized are refused.
func sniffCoverImage(header string, data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	switch sniffed := http.DetectContentType(data); sniffed {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return sniffed, true
	}
	// AVIF is not recognised by net/http's sniffer: require the ISO-BMFF
	// "ftyp" box with an avif brand and an image header.
	if len(data) >= 12 && string(data[4:8]) == "ftyp" && (string(data[8:12]) == "avif" || string(data[8:12]) == "avis") &&
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(header)), "image/") {
		return "image/avif", true
	}
	return "", false
}

func (p *ConfiguredCoverProxy) recentlyFailed(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.failed[key]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(p.failed, key)
		return false
	}
	return true
}

func (p *ConfiguredCoverProxy) markFailed(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed == nil || len(p.failed) >= maxCoverNegativeKeys {
		p.failed = map[string]time.Time{}
	}
	p.failed[key] = time.Now().Add(coverNegativeTTL)
}
