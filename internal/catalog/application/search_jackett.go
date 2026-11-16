package application

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/jackett"
	"bansoko/internal/redact"
	"bansoko/internal/runtime/buildinfo"
)

const jackettSearchContractVersion = "jackett-torznab-search-v2"

// HTTPJackettSearcher queries only Jackett's configured `all` aggregate. Site
// credentials, indexer selection and per-site failures stay inside Jackett; the
// returned jackettindexer is source attribution, never a subtitle-group fact.
type HTTPJackettSearcher struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
	MaxSize  int64
}

func (s HTTPJackettSearcher) SearchJackettSeeds(ctx context.Context, query string) (SeedSearchResult, error) {
	query = NormalizeSearchQuery(query)
	if query == "" {
		return SeedSearchResult{}, fmt.Errorf("%w: query is required", ErrSearchQueryInvalid)
	}
	endpoint, err := jackett.AggregateURL(s.Endpoint, s.APIKey, url.Values{
		"t": {"search"}, "q": {query}, "limit": {strconv.Itoa(torznabSearchLimit)},
	})
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: %v", ErrProviderUpstream, err)
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = validateTorznabRedirect
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: create Jackett search request", ErrProviderUpstream)
	}
	request.Header.Set("Accept", "application/xml, application/rss+xml;q=0.9")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	response, err := clientCopy.Do(request)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: fetch Jackett search: %v", ErrProviderUpstream, redact.URLError(err))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable {
		return SeedSearchResult{}, &ProviderRateLimited{Upstream: "Jackett", RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC()), Status: response.StatusCode, Descriptions: response.Status}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return SeedSearchResult{}, fmt.Errorf("%w: Jackett search returned HTTP %d", ErrProviderUpstream, response.StatusCode)
	}
	maxSize := s.MaxSize
	if maxSize <= 0 {
		maxSize = maxTorznabResponseSize
	}
	payload, err := readBoundedBody(response.Body, maxSize)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: read Jackett search: %v", ErrProviderUpstream, err)
	}
	result, err := parseJackettSearch(payload)
	if err != nil {
		var protocol *TorznabError
		if errors.As(err, &protocol) {
			// Upstream descriptions are untrusted and may echo the authenticated
			// request. Keep the protocol class but apply the shared redactor.
			return SeedSearchResult{}, &TorznabError{Code: redact.Text(protocol.Code, 64), Description: redact.Text(protocol.Description, 512)}
		}
		return SeedSearchResult{}, err
	}
	// Category names live in each indexer's caps, not in the search response.
	// This enrichment never fails the search: an unreadable caps document
	// leaves the rows without categories. Items shares its backing array with
	// Parse.Items, so both views carry the resolved categories.
	s.resolveSeedCategories(ctx, &clientCopy, result.Items)
	result.Query = query
	return result, nil
}

func parseJackettSearch(payload []byte) (SeedSearchResult, error) {
	root, err := torznabRootName(payload)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: %v", ErrProviderUpstream, err)
	}
	if strings.EqualFold(root, "error") {
		var failure struct {
			Code        string `xml:"code,attr"`
			Description string `xml:"description,attr"`
		}
		if err := xml.Unmarshal(payload, &failure); err != nil {
			return SeedSearchResult{}, fmt.Errorf("%w: decode Jackett error: %v", ErrProviderUpstream, err)
		}
		code := strings.TrimSpace(failure.Code)
		if code == strconv.Itoa(http.StatusTooManyRequests) {
			return SeedSearchResult{}, &ProviderRateLimited{Upstream: "Jackett", Status: http.StatusTooManyRequests, Descriptions: redact.Text(failure.Description, 512)}
		}
		return SeedSearchResult{}, &TorznabError{Code: code, Description: failure.Description}
	}
	if !strings.EqualFold(root, "rss") {
		return SeedSearchResult{}, fmt.Errorf("%w: Jackett response root element is %q, not rss", ErrProviderUpstream, root)
	}
	var document torznabFeed
	if err := xml.Unmarshal(payload, &document); err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: decode Jackett search: %v", ErrProviderUpstream, err)
	}
	total := len(document.Channel.Items)
	if total > torznabSearchWindow {
		return SeedSearchResult{}, fmt.Errorf("%w: Jackett search returned %d items, above the %d-item keyword window", ErrProviderUpstream, total, torznabSearchWindow)
	}
	result := SeedSearchResult{
		ParserVersion:   jackettSearchContractVersion,
		Window:          torznabSearchLimit,
		SourceItemCount: total,
		Parse: acquisitiondomain.FeedParseResult{
			Format: acquisitiondomain.FeedFormatRSS,
			Site:   &acquisitiondomain.FeedSite{ID: "jackett", Name: "Jackett", Identity: acquisitiondomain.FeedIdentityFromFeed, Match: acquisitiondomain.FeedSiteMatchHost},
			Items:  make([]acquisitiondomain.FeedItem, 0, total),
		},
	}
	for index, raw := range document.Channel.Items {
		item, reason := torznabSearchItem(raw)
		if reason == "" {
			item.Provider = ProviderJackett
			source := acquisitiondomain.SeedSourceSite{
				ID: strings.TrimSpace(raw.JackettIndexer.ID), Name: strings.TrimSpace(raw.JackettIndexer.Name),
				Type: strings.ToLower(strings.TrimSpace(raw.Type)),
			}
			if err := source.Validate(); err != nil {
				reason = "source_site_invalid"
			} else {
				item.SourceSite = &source
			}
		}
		if reason == "" {
			attachJackettSeedFacts(&item, raw)
		}
		if reason != "" {
			result.Parse.Skipped = append(result.Parse.Skipped, acquisitiondomain.FeedItemSkip{Index: index, Title: raw.Title, Reason: reason})
			continue
		}
		result.Parse.Items = append(result.Parse.Items, item)
	}
	result.Items = result.Parse.Items
	if err := acquisitiondomain.RequireUsableItems(result.Parse); err != nil {
		return SeedSearchResult{}, err
	}
	return result, nil
}

// attachJackettSeedFacts reads the Jackett-only torznab attributes of one
// already-accepted item: the swarm counts, the billing factors and the raw
// category ids. The shared torznab normalizer deliberately stays blind to
// them so the AniBT path cannot grow PT facts by accident. An unreadable
// value is an absent fact, never a skip reason and never a zero or a 1.
func attachJackettSeedFacts(item *acquisitiondomain.FeedItem, raw torznabItem) {
	var seeders, peers *int64
	for _, attr := range raw.Attrs {
		value := strings.TrimSpace(attr.Value)
		switch strings.ToLower(strings.TrimSpace(attr.Name)) {
		case "seeders":
			seeders = parseTorznabCount(value)
		case "peers":
			peers = parseTorznabCount(value)
		case "downloadvolumefactor":
			item.DownloadFactor = parseTorznabFactor(value)
		case "uploadvolumefactor":
			item.UploadFactor = parseTorznabFactor(value)
		case "category":
			if value != "" && len(value) <= 64 {
				item.SeedCategoryIDs = append(item.SeedCategoryIDs, value)
			}
		}
	}
	item.Seeders = seeders
	if seeders != nil && peers != nil {
		leechers := *peers - *seeders
		if leechers < 0 {
			leechers = 0
		}
		item.Leechers = &leechers
	}
}

// parseTorznabCount reads a tracker-reported count. A missing, negative or
// overflowing value is absent, and a count is bounded far beyond any real
// swarm so a garbage value cannot poison a persisted row.
func parseTorznabCount(value string) *int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || parsed > 1<<40 {
		return nil
	}
	return &parsed
}

// parseTorznabFactor reads a billing factor such as 0, 0.5 or 2. An
// unreadable or negative value is absent; absent is never rewritten to 1.
func parseTorznabFactor(value string) *float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 || parsed > 1e6 {
		return nil
	}
	return &parsed
}
