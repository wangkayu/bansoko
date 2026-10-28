package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bansoko/internal/acquisition/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/observability/outbound"
	"bansoko/internal/redact"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

type RSSFetcher interface {
	Fetch(context.Context, subscriptiondomain.RSSLocator) ([]domain.FeedItem, error)
}

// RSSDetailedFetcher is an optional extension that retains item-level parse
// diagnostics while RSSFetcher keeps the ordinary source-refresh contract.
type RSSDetailedFetcher interface {
	FetchDetailed(context.Context, subscriptiondomain.RSSLocator) (RSSFetchResult, error)
}

// RSSFetchResult is one fetched feed: the accepted items (torrent metadata
// enriched) and the parse result with every skipped item and its reason.
// Provider and custom sources both produce it.
type RSSFetchResult struct {
	Items []domain.FeedItem
	Parse domain.FeedParseResult
}

type HTTPRSSFetcher struct {
	Client            *http.Client
	MaxSize           int64
	Resolver          func(context.Context, string) ([]net.IP, error)
	AllowPrivateHosts []string
	TorrentReader     TorrentMetadataReader
}

// Outbound HTTP identity is fixed code, not user configuration: the
// browser UA keeps picky feed/torrent hosts answering, requests time out at
// 15s and responses stay under the size clamp below (see MaxSize default).
// RSS feed fetches and torrent metadata fetches share the same disguise.
const browserDisguiseUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
const rssHTTPRequestTimeout = 15 * time.Second

func (f HTTPRSSFetcher) Fetch(ctx context.Context, locator subscriptiondomain.RSSLocator) ([]domain.FeedItem, error) {
	return usableItems(f.FetchDetailed(ctx, locator))
}

// usableItems applies the shared zero-usable rule to a detailed fetch: a feed
// whose every item was skipped is a *domain.FeedFailure with a summary.
func usableItems(result RSSFetchResult, err error) ([]domain.FeedItem, error) {
	if err != nil {
		return nil, err
	}
	if err := domain.RequireUsableItems(result.Parse); err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (f HTTPRSSFetcher) FetchDetailed(ctx context.Context, locator subscriptiondomain.RSSLocator) (RSSFetchResult, error) {
	if f.MaxSize <= 0 {
		f.MaxSize = 10 << 20
	}
	normalized, err := locator.Normalize()
	if err != nil {
		return RSSFetchResult{}, err
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: rssHTTPRequestTimeout}
	}
	clientCopy := *client
	transport, err := f.secureTransport(clientCopy.Transport, normalized.URL)
	if err != nil {
		return RSSFetchResult{}, err
	}
	clientCopy.Transport = transport
	clientCopy.CheckRedirect = validateRSSRedirect
	client = &clientCopy
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized.URL, nil)
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("create RSS request: %w", err)
	}
	request.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9")
	request.Header.Set("User-Agent", browserDisguiseUserAgent)
	applyFeedValidators(ctx, request)
	response, err := client.Do(request)
	if err != nil {
		return RSSFetchResult{}, outbound.WithFailure(fmt.Errorf("fetch RSS source: %w", redact.URLError(err)), outbound.Describe(err, request, nil, outbound.ViaOf(client, request)))
	}
	defer response.Body.Close()
	if recordFeedResponse(ctx, response) {
		return RSSFetchResult{}, ErrFeedNotModified
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RSSFetchResult{}, outbound.WithFailure(fmt.Errorf("RSS source returned HTTP %d", response.StatusCode), outbound.Describe(nil, request, response, outbound.ViaOf(client, request)))
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, f.MaxSize+1))
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("read RSS source: %w", redact.URLError(err))
	}
	if int64(len(payload)) > f.MaxSize {
		return RSSFetchResult{}, errors.New("RSS source exceeds the configured size limit")
	}
	// One feed pipeline: the configured URL's host picks the adapter of the
	// declared list; an unlisted host is parsed generically.
	parsed, err := domain.ParseFeedForURL(normalized.URL, payload)
	if err != nil {
		return RSSFetchResult{}, err
	}
	items, err := enrichFeedItemsWithTorrentMetadata(ctx, parsed.Items, f.TorrentReader)
	if err != nil {
		return RSSFetchResult{}, err
	}
	parsed.Items = items
	return RSSFetchResult{Items: items, Parse: parsed}, nil
}

func validateRSSRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("RSS source exceeded redirect limit")
	}
	if _, err := (subscriptiondomain.RSSLocator{URL: request.URL.String()}).Normalize(); err != nil {
		return fmt.Errorf("RSS redirect rejected: %w", err)
	}
	if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && strings.EqualFold(request.URL.Scheme, "http") {
		return errors.New("RSS redirect rejected: HTTPS downgrade is not allowed")
	}
	request.Header.Del("Authorization")
	request.Header.Del("Cookie")
	return nil
}

type rssOriginAuthTransport interface {
	RSSBaseTransport() http.RoundTripper
	WithRSSRequestOrigin(http.RoundTripper, string) http.RoundTripper
}

// rssSafetyTransport applies the SSRF guard at request time and then routes
// through either the direct transport or, when an explicit network route
// selected an HTTP proxy, the proxy transport. The proxy transport dials the
// (possibly private) proxy address directly; the request target is validated
// to be public before either transport is used.
type rssSafetyTransport struct {
	base          http.RoundTripper
	proxied       http.RoundTripper
	proxySelector func(*http.Request) (*url.URL, error)
	resolver      func(context.Context, string) ([]net.IP, error)
	allowed       map[string]struct{}
}

func (t rssSafetyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var proxyURL *url.URL
	if t.proxySelector != nil {
		selected, err := t.proxySelector(request)
		if err != nil {
			return nil, fmt.Errorf("select RSS proxy route: %w", err)
		}
		proxyURL = selected
	}
	if err := validateRSSRequestTarget(request, proxyURL, t.resolver, t.allowed); err != nil {
		return nil, err
	}
	if proxyURL != nil && t.proxied != nil {
		return t.proxied.RoundTrip(request)
	}
	return t.base.RoundTrip(request)
}

func (f HTTPRSSFetcher) secureTransport(existing http.RoundTripper, configuredURLs ...string) (http.RoundTripper, error) {
	if existing == nil {
		// The fallback transport inherits http.ProxyFromEnvironment so a
		// deployment or developer shell that sets HTTP_PROXY/HTTPS_PROXY gets
		// a working egress path. An explicit NetworkRoute resolved through
		// ClientForConnection still takes precedence (its transport carries
		// its own Proxy). The SSRF-safe dial wrapper below validates the
		// request target to a public address before any proxy route is used.
		existing = hardeningtransport.DirectTransport()
	}
	var auth rssOriginAuthTransport
	if candidate, ok := existing.(rssOriginAuthTransport); ok {
		auth = candidate
		existing = candidate.RSSBaseTransport()
	}
	base, ok := existing.(*http.Transport)
	if !ok {
		return nil, errors.New("RSS HTTP client must use an *http.Transport for SSRF-safe dialing")
	}
	transport := base.Clone()
	proxySelector := transport.Proxy
	// The base transport dials the target directly; private-address dial
	// rejection is enforced below. When an env or route proxy is present it
	// is preserved as a separate proxied transport so the proxy host (which
	// may be private, e.g. 127.0.0.1:7897) is dialed without SSRF rejection
	// while the request target is still validated to a public address.
	transport.Proxy = nil
	originalDial := transport.DialContext
	if originalDial == nil {
		dialer := &net.Dialer{Timeout: 20 * time.Second}
		originalDial = dialer.DialContext
	}
	resolver := f.Resolver
	if resolver == nil {
		resolver = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	allowed := make(map[string]struct{}, len(f.AllowPrivateHosts))
	for _, host := range f.AllowPrivateHosts {
		allowed[strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")] = struct{}{}
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse RSS dial address: %w", err)
		}
		normalizedHost := normalizeRSSHost(host)
		if _, ok := allowed[normalizedHost]; ok {
			if f.Resolver == nil {
				return originalDial(ctx, network, address /* fixture allowlisted */)
			}
			ips, resolveErr := resolver(ctx, normalizedHost)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolve RSS fixture host: %w", resolveErr)
			}
			if len(ips) == 0 {
				return nil, errors.New("RSS fixture host has no addresses")
			}
			return originalDial(ctx, network, net.JoinHostPort(ips[0].String(), port))
		}
		ips, err := resolveSafeRSSHost(ctx, normalizedHost, resolver)
		if err != nil {
			return nil, err
		}
		return originalDial(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	// Explicit HTTP proxy route: a proxy transport dials the (possibly
	// private) proxy host directly. The request target is still SSRF-validated
	// to a public address before routing (spec: "a selected HTTP/SOCKS5 proxy
	// is an explicit egress boundary and may have a private dial address").
	var proxied *http.Transport
	if proxySelector != nil {
		proxied = base.Clone()
		proxied.Proxy = proxySelector
		proxied.DialContext = originalDial
	}
	safe := rssSafetyTransport{base: transport, proxied: proxied, proxySelector: proxySelector, resolver: resolver, allowed: allowed}
	if auth == nil {
		return safe, nil
	}
	if len(configuredURLs) == 0 {
		return safe, nil
	}
	configured, err := url.Parse(configuredURLs[0])
	if err != nil {
		return nil, errors.New("RSS authentication origin is invalid")
	}
	return auth.WithRSSRequestOrigin(safe, normalizedRSSOrigin(configured)), nil
}

func validateRSSRequestTarget(request *http.Request, proxyURL *url.URL, resolver func(context.Context, string) ([]net.IP, error), allowed map[string]struct{}) error {
	if request == nil || request.URL == nil || (request.URL.Scheme != "http" && request.URL.Scheme != "https") || request.URL.Hostname() == "" {
		return errors.New("RSS request target must be an absolute http or https URL")
	}
	// A proxy (whether from an explicit NetworkRoute or from the environment
	// via http.ProxyFromEnvironment) is a trusted egress boundary: the proxy
	// may dial a private address, but the request target must still resolve
	// to a public address.
	return validatePublicTarget(request, resolver, allowed)
}

func validatePublicTarget(request *http.Request, resolver func(context.Context, string) ([]net.IP, error), allowed map[string]struct{}) error {
	host := normalizeRSSHost(request.URL.Hostname())
	if _, ok := allowed[host]; ok {
		return nil
	}
	_, err := resolveSafeRSSHost(request.Context(), host, resolver)
	return err
}

func resolveSafeRSSHost(ctx context.Context, host string, resolver func(context.Context, string) ([]net.IP, error)) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if subscriptiondomain.IsPrivateHost(ip.String()) {
			return nil, errors.New("RSS source resolved to a private or local address")
		}
		return []net.IP{ip}, nil
	}
	ips, err := resolver(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve RSS source host: %w", err)
	}
	if len(ips) == 0 {
		return nil, errors.New("RSS source host has no addresses")
	}
	for _, ip := range ips {
		if subscriptiondomain.IsPrivateHost(ip.String()) {
			return nil, errors.New("RSS source resolved to a private or local address")
		}
	}
	return ips, nil
}

func normalizeRSSHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func normalizedRSSOrigin(target *url.URL) string {
	scheme := strings.ToLower(target.Scheme)
	port := target.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(normalizeRSSHost(target.Hostname()), port)
}
