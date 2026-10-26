package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/redact"
	"bansoko/internal/runtime/buildinfo"
)

// Nyaa has no work catalog, so it is a torrent-search-only source: keyword
// search returns the site's own RSS answer and nothing else. The answer is
// parsed with the declared Nyaa feed adapter (nyaa.si is on the declared site
// list), so a search seed has the same shape as a Nyaa feed seed:
//
//   - the `<link>` is the /download/<id>.torrent location and the only download
//     source; a magnet link or a btih is never read;
//   - `nyaa:infoHash` is the feed identity the lazy expansion verifies the
//     fetched bytes against;
//   - `nyaa:size` is a human string ("1.2 GiB") normalized to bytes.
//
// Only the anime category (`c=1_0`) is requested, with no trusted/remake
// filter (`f=0`).
const (
	nyaaSearchContractVersion = "nyaa-rss-search-v1"
	// nyaaSearchWindow is the RSS window: Nyaa answers one page of at most 75
	// items for a keyword search (measured on 2026-10-26) and the RSS endpoint
	// has no usable pagination. A response above it is not the shape the site
	// promises and fails closed instead of being truncated silently.
	nyaaSearchWindow     = 75
	maxNyaaSearchRSSSize = 4 << 20
	nyaaAnimeCategory    = "1_0"
	nyaaNoFilter         = "0"
)

// HTTPNyaaSearcher reads Nyaa's RSS keyword search. The endpoint and client are
// resolved by the caller from typed connection configuration (including its
// network route), so a request never chooses an upstream endpoint.
type HTTPNyaaSearcher struct {
	Endpoint          string
	Client            *http.Client
	AllowPrivateHosts []string
	Resolver          func(context.Context, string) ([]net.IP, error)
	MaxSize           int64
}

// SearchNyaaSeeds runs one RSS search and normalizes its items. A valid channel
// with no items is a successful empty search; transport failures, rate limits,
// non-RSS bodies and pages of only unusable rows stay distinguishable failures.
func (s HTTPNyaaSearcher) SearchNyaaSeeds(ctx context.Context, query string) (SeedSearchResult, error) {
	query = NormalizeSearchQuery(query)
	if query == "" {
		return SeedSearchResult{}, fmt.Errorf("%w: query is required", ErrSearchQueryInvalid)
	}
	endpoint, err := nyaaSearchEndpoint(s.Endpoint, query)
	if err != nil {
		return SeedSearchResult{}, err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = validateTorznabRedirect
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: create Nyaa search request: %v", ErrProviderUpstream, err)
	}
	request.Header.Set("Accept", "application/rss+xml, application/xml;q=0.9")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	response, err := clientCopy.Do(request)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: fetch Nyaa search: %v", ErrProviderUpstream, redact.URLError(err))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable {
		return SeedSearchResult{}, &ProviderRateLimited{Upstream: "Nyaa", RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC()), Status: response.StatusCode, Descriptions: response.Status}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return SeedSearchResult{}, fmt.Errorf("%w: Nyaa search returned HTTP %d", ErrProviderUpstream, response.StatusCode)
	}
	maxSize := s.MaxSize
	if maxSize <= 0 {
		maxSize = maxNyaaSearchRSSSize
	}
	payload, err := readBoundedBody(response.Body, maxSize)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: read Nyaa search: %v", ErrProviderUpstream, err)
	}
	result, err := parseNyaaSearch(payload)
	if err != nil {
		return SeedSearchResult{}, err
	}
	result.Query = query
	return result, nil
}

// nyaaSearchEndpoint builds the one documented RSS search URL. The query is a
// single encoded parameter, so spaces, `&`, `%`, `+` and CJK text never split
// into extra parameters.
func nyaaSearchEndpoint(rawEndpoint, query string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawEndpoint), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return nil, fmt.Errorf("%w: Nyaa endpoint must be an absolute HTTP URL without credentials", ErrProviderUpstream)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/"
	values := url.Values{}
	values.Set("page", "rss")
	values.Set("q", query)
	values.Set("c", nyaaAnimeCategory)
	values.Set("f", nyaaNoFilter)
	base.RawQuery = values.Encode()
	return base, nil
}

func parseNyaaSearch(payload []byte) (SeedSearchResult, error) {
	parsed, err := acquisitiondomain.ParseSiteFeed(acquisitiondomain.FeedSiteNyaa, payload)
	if err != nil {
		var failure *acquisitiondomain.FeedFailure
		if errors.As(err, &failure) {
			return SeedSearchResult{}, err
		}
		return SeedSearchResult{}, fmt.Errorf("%w: Nyaa search answer is not a usable RSS feed: %v", ErrProviderUpstream, err)
	}
	if len(parsed.Items)+len(parsed.Skipped) > nyaaSearchWindow {
		return SeedSearchResult{}, fmt.Errorf("%w: Nyaa search returned %d items, above the %d-item window", ErrProviderUpstream, len(parsed.Items)+len(parsed.Skipped), nyaaSearchWindow)
	}
	if err := acquisitiondomain.RequireUsableItems(parsed); err != nil {
		return SeedSearchResult{}, err
	}
	for i := range parsed.Items {
		parsed.Items[i].Provider = "nyaa"
	}
	return SeedSearchResult{
		Items:           parsed.Items,
		Parse:           parsed,
		ParserVersion:   nyaaSearchContractVersion,
		Window:          nyaaSearchWindow,
		SourceItemCount: len(parsed.Items) + len(parsed.Skipped),
	}, nil
}
