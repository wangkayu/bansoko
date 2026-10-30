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

const jackettSearchContractVersion = "jackett-torznab-search-v1"

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
