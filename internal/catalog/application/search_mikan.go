package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	catalogdomain "bansoko/internal/catalog/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/redact"
	"bansoko/internal/runtime/buildinfo"
	"github.com/PuerkitoBio/goquery"
)

// Mikan exposes two search surfaces, and this file owns exactly both of them:
//
//   - Home/Search carries the site's own program recommendations (bangumiId,
//     title, cover). Only that block is read: the page also carries up to a
//     thousand torrent rows plus a magnet and tracker string per row, which is
//     redundant with RSS/Search and would need a second, larger HTML parser.
//   - RSS/Search carries the last 100 torrents matching the query as ordinary
//     RSS items. It is parsed with the declared Mikan feed adapter, so a search
//     seed is the same shape as a subscribed feed seed and goes through the same
//     snapshot mapper.
//
// Neither surface is scraped into a synthetic RSS document, and neither parses a
// magnet link or a btih: only the `.torrent` enclosure is a download source.
const (
	// mikanSearchContractVersion covers both surfaces: one versioned parser per
	// site, recorded in the snapshot evidence of every search snapshot.
	mikanSearchContractVersion = "mikan-search-v1"
	// mikanSearchWindow is the RSS/Search window: the site answers with at most
	// 100 items, matching the AniBT keyword window.
	mikanSearchWindow = 100
	// maxMikanSearchHTMLSize bounds Home/Search. The recommendation block is a
	// few kilobytes of a page that also carries the torrent rows, so the bound is
	// generous for the page but still refuses a runaway body.
	maxMikanSearchHTMLSize = 16 << 20
	// maxMikanSearchRSSSize bounds RSS/Search: measured at roughly 95 KB for a
	// 100-item answer.
	maxMikanSearchRSSSize = 8 << 20
)

// Search is the Searcher implementation of the Mikan catalog reader: Mikan's
// program keyword search is the Home/Search recommendation block, so the
// existing provider search endpoint keeps working and stops answering
// SEARCH_NOT_SUPPORTED for Mikan.
func (r MikanHTTPReader) Search(ctx context.Context, query string, _ catalogdomain.ReadHints) (catalogdomain.ReadResult, error) {
	return (HTTPMikanSearcher{Endpoint: r.Endpoint, Client: r.Client}).SearchMikanPrograms(ctx, query)
}

// HTTPMikanSearcher reads both Mikan search surfaces. The client is resolved by
// the caller from typed connection configuration, so a request never chooses an
// upstream endpoint.
type HTTPMikanSearcher struct {
	Endpoint          string
	Client            *http.Client
	AllowPrivateHosts []string
	Resolver          func(context.Context, string) ([]net.IP, error)
	MaxHTMLSize       int64
	MaxRSSSize        int64
}

// newSearchClient returns this reader's own client. The concrete SSRF-safe
// transport is applied by the composition root through CatalogSearchEndpoints
// (it owns the network route, resolver and fixture allow-list), so this reader
// never dials a host on its own.
func (s HTTPMikanSearcher) newSearchClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
}

// SearchMikanPrograms reads the Home/Search recommendation block and maps each
// program onto the shared catalog entry shape, so an existing Mikan discovery,
// draft and subscription path keeps working unchanged.
func (s HTTPMikanSearcher) SearchMikanPrograms(ctx context.Context, query string) (catalogdomain.ReadResult, error) {
	query = NormalizeSearchQuery(query)
	if query == "" {
		return catalogdomain.ReadResult{}, fmt.Errorf("%w: query is required", ErrSearchQueryInvalid)
	}
	request, err := s.mikanSearchRequest(ctx, "/Home/Search", query)
	if err != nil {
		return catalogdomain.ReadResult{}, err
	}
	response, err := s.newSearchClient().Do(request)
	if err != nil {
		return catalogdomain.ReadResult{}, fmt.Errorf("%w: fetch Mikan program search: %v", ErrProviderUpstream, redact.URLError(err))
	}
	defer response.Body.Close()
	if err := mikanSearchStatus(response, "program search"); err != nil {
		return catalogdomain.ReadResult{}, err
	}
	maxSize := s.MaxHTMLSize
	if maxSize <= 0 {
		maxSize = maxMikanSearchHTMLSize
	}
	payload, err := readBoundedBody(response.Body, maxSize)
	if err != nil {
		return catalogdomain.ReadResult{}, fmt.Errorf("%w: read Mikan program search: %v", ErrProviderUpstream, err)
	}
	document, err := goquery.NewDocumentFromReader(strings.NewReader(string(payload)))
	if err != nil {
		return catalogdomain.ReadResult{}, fmt.Errorf("%w: parse Mikan program search HTML: %v", ErrProviderUpstream, err)
	}
	entries, err := mikanProgramEntries(document)
	if err != nil {
		return catalogdomain.ReadResult{}, &catalogdomain.ReadError{Code: catalogdomain.ReadFailureSchemaMismatch, Err: err}
	}
	return catalogdomain.ReadResult{Entries: entries, ContractVersion: mikanSearchContractVersion}, nil
}

// SearchMikanSeeds reads RSS/Search and normalizes its items exactly like a
// subscribed Mikan feed: the same declared adapter, the same key rules, the same
// zero-usable rule.
func (s HTTPMikanSearcher) SearchMikanSeeds(ctx context.Context, query string) (SeedSearchResult, error) {
	query = NormalizeSearchQuery(query)
	if query == "" {
		return SeedSearchResult{}, fmt.Errorf("%w: query is required", ErrSearchQueryInvalid)
	}
	request, err := s.mikanSearchRequest(ctx, "/RSS/Search", query)
	if err != nil {
		return SeedSearchResult{}, err
	}
	response, err := s.newSearchClient().Do(request)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: fetch Mikan seed search: %v", ErrProviderUpstream, redact.URLError(err))
	}
	defer response.Body.Close()
	retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable {
		return SeedSearchResult{}, &ProviderRateLimited{Upstream: "Mikan", RetryAfter: retryAfter, Status: response.StatusCode, Descriptions: response.Status}
	}
	if err := mikanSearchStatus(response, "seed search"); err != nil {
		return SeedSearchResult{}, err
	}
	maxSize := s.MaxRSSSize
	if maxSize <= 0 {
		maxSize = maxMikanSearchRSSSize
	}
	payload, err := readBoundedBody(response.Body, maxSize)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: read Mikan seed search: %v", ErrProviderUpstream, err)
	}
	parsed, err := acquisitiondomain.ParseSiteFeed(acquisitiondomain.FeedSiteMikan, payload)
	if err != nil {
		return SeedSearchResult{}, err
	}
	// One pass decides which rows a snapshot can carry, and every dropped row is
	// reported with its reason: a page whose every row is unusable is the shared
	// typed feed failure, never a successful empty search. A row the site did not
	// date is dropped instead of being dated by this process, because the seed
	// mapping refuses a seed without a published time.
	usable := make([]acquisitiondomain.FeedItem, 0, len(parsed.Items))
	for index, item := range parsed.Items {
		item.Provider = "mikan"
		reason := ""
		switch {
		case item.Published.IsZero():
			reason = "published_missing"
		case strings.TrimSpace(item.Title) == "":
			reason = "title_missing"
		case strings.TrimSpace(item.DownloadURL()) == "":
			reason = "download_url_missing"
		}
		if reason != "" {
			parsed.Skipped = append(parsed.Skipped, acquisitiondomain.FeedItemSkip{Index: index, GUID: item.GUID, Title: item.Title, Reason: reason})
			continue
		}
		usable = append(usable, item)
	}
	parsed.Items = usable
	sourceItemCount := len(usable) + len(parsed.Skipped)
	if sourceItemCount > mikanSearchWindow {
		return SeedSearchResult{}, fmt.Errorf("%w: Mikan seed search returned %d items, above the %d-item window", ErrProviderUpstream, sourceItemCount, mikanSearchWindow)
	}
	if err := acquisitiondomain.RequireUsableItems(parsed); err != nil {
		return SeedSearchResult{}, err
	}
	return SeedSearchResult{
		Items:           parsed.Items,
		Parse:           parsed,
		ParserVersion:   mikanSearchContractVersion,
		Query:           query,
		Window:          mikanSearchWindow,
		SourceItemCount: sourceItemCount,
	}, nil
}

func (s HTTPMikanSearcher) mikanSearchRequest(ctx context.Context, path, query string) (*http.Request, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(s.Endpoint), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return nil, fmt.Errorf("%w: Mikan endpoint must be an absolute HTTP URL without credentials", ErrProviderUpstream)
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	values := base.Query()
	values.Set("searchstr", query)
	base.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create Mikan search request: %v", ErrProviderUpstream, err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/rss+xml;q=0.9")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	return request, nil
}

func mikanSearchStatus(response *http.Response, surface string) error {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: Mikan %s returned HTTP %d", ErrProviderUpstream, surface, response.StatusCode)
	}
	return nil
}

func readBoundedBody(body io.Reader, maxSize int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxSize {
		return nil, errors.New("response exceeds the configured size limit")
	}
	return payload, nil
}

// mikanProgramEntries reads the recommendation block of Home/Search. Only the
// block the site labels as related programs is read: the page's torrent rows are
// never consulted here, and a page whose block lost its shape is a schema
// mismatch instead of an empty program list.
func mikanProgramEntries(document *goquery.Document) ([]catalogdomain.Entry, error) {
	block := document.Find(".search-result-recommend, [data-search-recommend], .relevant-recommend").First()
	if block.Length() == 0 {
		return nil, errors.New("Mikan program search recommendation block is missing")
	}
	candidates := block.Find("a[href*='/Home/Bangumi/']")
	entries := make([]catalogdomain.Entry, 0, candidates.Length())
	seen := make(map[string]struct{}, candidates.Length())
	candidates.EachWithBreak(func(_ int, link *goquery.Selection) bool {
		ref, ok := mikanBangumiRef(selectionAttr(link, "href"))
		if !ok {
			return true
		}
		if _, exists := seen[ref]; exists {
			return true
		}
		title := strings.TrimSpace(selectionAttr(link, "title"))
		if title == "" {
			title = strings.TrimSpace(link.Text())
		}
		if title == "" {
			return true
		}
		seen[ref] = struct{}{}
		cover := ""
		if image := link.Find("img").First(); image.Length() > 0 {
			cover = strings.TrimSpace(selectionAttr(image, "data-src"))
			if cover == "" {
				cover = strings.TrimSpace(selectionAttr(image, "src"))
			}
		}
		entries = append(entries, catalogdomain.Entry{
			ProviderEntryRef: ref, Title: title, State: catalogdomain.EntryPresent,
			SourceHint: map[string]any{"provider": "mikan", "bangumi_id": ref, "cover": cover},
		})
		return true
	})
	return entries, nil
}

// mikanBangumiRef extracts the numeric bangumi id from /Home/Bangumi/<id>.
func mikanBangumiRef(href string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", false
	}
	const prefix = "/Home/Bangumi/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return "", false
	}
	value := strings.Trim(strings.TrimPrefix(parsed.Path, prefix), "/")
	if index := strings.Index(value, "/"); index >= 0 {
		value = value[:index]
	}
	parsedID, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsedID == 0 || strconv.FormatUint(parsedID, 10) != value {
		return "", false
	}
	return value, true
}
