package application

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/acquisition/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/redact"
	"bansoko/internal/runtime/buildinfo"
)

// AniBT exposes its torrent search as anonymous Torznab. The reader below is
// the only code that speaks that protocol, and it is deliberately narrower than
// the protocol: one fixed function (`t=search`), one free-text keyword, the
// documented window limit, and a normalized FeedItem per item.
//
// It does not offer `cat`, `tvsearch`, `movie`, seeders or any other
// source-specific filter: both sites are queried the same way (作品 + 字幕组 /
// 集数 / 分辨率 as free text), and a source-specific query surface would make
// the two providers produce results a user cannot compare.
const (
	// torznabMaxItemSize is the shared per-item size ceiling the seed mapping
	// applies. A published size above it is not a torrent fact worth storing.
	torznabMaxItemSize int64 = 1 << 50
	// torznabSearchLimit is the documented keyword window. AniBT answers at most
	// 100 items for a keyword search and returns an empty list beyond that
	// window, so the reader asks for exactly the window it can render.
	torznabSearchLimit = 100
	// torznabSearchWindow is the hard ceiling the normalizer accepts. A response
	// carrying more items than the documented window is not the shape the
	// protocol promises and fails closed instead of being truncated silently.
	torznabSearchWindow = 100
	// torznabContractVersion names this reader's output so a persisted snapshot
	// can state which parser produced its seed facts.
	torznabContractVersion = "anibt-torznab-search-v1"
	// maxTorznabResponseSize bounds the response body. A keyword window of 100
	// items with attributes is tens of kilobytes; the bound only stops a runaway
	// body from being read at all.
	maxTorznabResponseSize = 4 << 20
)

// ErrTorznabError is a Torznab protocol error: HTTP 200 whose root element is
// `<error code="…" description="…"/>`. The protocol reports failures inside a
// successful transport response, so it must be detected before any item is
// read; treating such a body as an empty result would turn an upstream refusal
// into a silently successful empty search.
var ErrTorznabError = errors.New("torznab returned a protocol error")

// TorznabError carries the upstream error code and description.
type TorznabError struct {
	Code        string
	Description string
}

func (e *TorznabError) Error() string {
	description := strings.TrimSpace(e.Description)
	if description == "" {
		description = "no description"
	}
	return fmt.Sprintf("torznab error %s: %s", e.Code, description)
}

func (e *TorznabError) Unwrap() error { return ErrTorznabError }

// ProviderRateLimited is a typed upstream refusal that names how long a caller
// should wait. Torznab answers 429/503 with Retry-After; the duration travels
// with the error so the transport can answer with the same header instead of
// inventing a retry time.
type ProviderRateLimited struct {
	Upstream     string
	RetryAfter   time.Duration
	Status       int
	Descriptions string
}

func (e *ProviderRateLimited) Error() string {
	return fmt.Sprintf("%s is rate limiting keyword search (HTTP %d): %s", e.Upstream, e.Status, safeReasonText(e.Descriptions))
}

// ErrProviderRateLimited is the sentinel every rate-limited search wraps.
var ErrProviderRateLimited = errors.New("provider rate limited keyword search")

func (e *ProviderRateLimited) Unwrap() error { return ErrProviderRateLimited }

// HTTPAniBTTorznabSearcher queries the anonymous AniBT Torznab endpoint with one
// free-text keyword. The endpoint and the client are resolved by the caller from
// typed connection configuration, never from a request.
type HTTPAniBTTorznabSearcher struct {
	Endpoint          string
	Client            *http.Client
	AllowPrivateHosts []string
	Resolver          func(context.Context, string) ([]net.IP, error)
	MaxSize           int64
}

// SeedSearchResult is one provider keyword search: normalized RSS-shaped items,
// the parser that produced them, and the window the upstream serves.
type SeedSearchResult struct {
	Items             []domain.FeedItem
	Parse             domain.FeedParseResult
	ParserVersion     string
	Query             string
	Window            int
	SourceItemCount   int
	UpstreamRetryWait time.Duration
}

// SearchAniBTKeywords runs one `t=search` request and normalizes its items.
//
// Every failure class stays distinguishable: a transport failure, an HTTP
// status failure and a Torznab `<error>` are typed upstream outcomes; a window
// the protocol does not promise is refused; and an item that cannot be named as
// a seed is skipped with a reason, so a page of unusable rows is a typed feed
// failure rather than a successful empty search.
func (s HTTPAniBTTorznabSearcher) SearchAniBTKeywords(ctx context.Context, query string) (SeedSearchResult, error) {
	query = NormalizeSearchQuery(query)
	if query == "" {
		return SeedSearchResult{}, fmt.Errorf("%w: query is required", ErrSearchQueryInvalid)
	}
	endpoint, err := torznabSearchEndpoint(s.Endpoint, query)
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
		return SeedSearchResult{}, fmt.Errorf("%w: create Torznab request: %v", ErrProviderUpstream, err)
	}
	request.Header.Set("Accept", "application/xml, application/rss+xml;q=0.9")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	response, err := clientCopy.Do(request)
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: fetch Torznab search: %v", ErrProviderUpstream, redact.URLError(err))
	}
	defer response.Body.Close()
	retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable {
		return SeedSearchResult{}, &ProviderRateLimited{Upstream: "AniBT", RetryAfter: retryAfter, Status: response.StatusCode, Descriptions: response.Status}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return SeedSearchResult{}, fmt.Errorf("%w: Torznab search returned HTTP %d", ErrProviderUpstream, response.StatusCode)
	}
	maxSize := s.MaxSize
	if maxSize <= 0 {
		maxSize = maxTorznabResponseSize
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: read Torznab search: %v", ErrProviderUpstream, err)
	}
	if int64(len(payload)) > maxSize {
		return SeedSearchResult{}, fmt.Errorf("%w: Torznab search response exceeds the size limit", ErrProviderUpstream)
	}
	result, err := parseTorznabSearch(payload)
	if err != nil {
		return SeedSearchResult{}, err
	}
	result.Query = query
	return result, nil
}

// torznabSearchEndpoint builds the one documented keyword search URL. The query
// is carried as a single url.Values-encoded parameter, so spaces, `&`, `%`, `+`
// and multibyte CJK text are never split into extra parameters.
func torznabSearchEndpoint(rawEndpoint, query string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawEndpoint), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return nil, fmt.Errorf("%w: Torznab endpoint must be an absolute HTTP URL without credentials", ErrProviderUpstream)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/torznab/api"
	values := base.Query()
	values.Set("t", "search")
	values.Set("q", query)
	values.Set("limit", strconv.Itoa(torznabSearchLimit))
	base.RawQuery = values.Encode()
	return base, nil
}

func validateTorznabRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("Torznab redirect limit exceeded")
	}
	redirect := request.URL
	if (redirect.Scheme != "http" && redirect.Scheme != "https") || redirect.Hostname() == "" || redirect.User != nil || redirect.Fragment != "" {
		return errors.New("Torznab redirect rejected")
	}
	if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && strings.EqualFold(redirect.Scheme, "http") {
		return errors.New("Torznab redirect rejected: HTTPS downgrade is not allowed")
	}
	request.Header.Del("Authorization")
	request.Header.Del("Cookie")
	return nil
}

// torznabItem is one `<item>` of a Torznab RSS document. It is declared
// explicitly, instead of reusing the generic RSS normalizer, because the
// identity and size of a Torznab item live in `<torznab:attr name=… value=…/>`
// XML attributes the generic normalizer deliberately drops.
type torznabItem struct {
	Title    string `xml:"title"`
	GUID     string `xml:"guid"`
	Link     string `xml:"link"`
	PubDate  string `xml:"pubDate"`
	Enclosur []struct {
		URL    string `xml:"url,attr"`
		Type   string `xml:"type,attr"`
		Length string `xml:"length,attr"`
	} `xml:"enclosure"`
	Attrs []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"attr"`
}

type torznabFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string        `xml:"title"`
		Items []torznabItem `xml:"item"`
		// Response is the newznab/torznab pagination header. It is read only to
		// state the upstream window; it never widens what is normalized.
		Response []struct {
			Offset string `xml:"offset,attr"`
			Total  string `xml:"total,attr"`
		} `xml:"response"`
	} `xml:"channel"`
}

// torznabRootName returns the document's root element name so an `<error>` body
// is recognized before any item is read.
func torznabRootName(payload []byte) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(payload)))
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", errors.New("Torznab response has no XML root element")
			}
			return "", fmt.Errorf("Torznab response is not XML: %w", err)
		}
		if start, ok := token.(xml.StartElement); ok {
			return start.Name.Local, nil
		}
	}
}

func parseTorznabSearch(payload []byte) (SeedSearchResult, error) {
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
			return SeedSearchResult{}, fmt.Errorf("%w: decode Torznab error: %v", ErrProviderUpstream, err)
		}
		code := strings.TrimSpace(failure.Code)
		// The protocol reuses HTTP status numbers for its rate-limit errors; the
		// description is bounded like any other upstream text.
		if code == strconv.Itoa(http.StatusTooManyRequests) {
			return SeedSearchResult{}, &ProviderRateLimited{Upstream: "AniBT", Status: http.StatusTooManyRequests, Descriptions: failure.Description}
		}
		return SeedSearchResult{}, &TorznabError{Code: code, Description: failure.Description}
	}
	if !strings.EqualFold(root, "rss") {
		return SeedSearchResult{}, fmt.Errorf("%w: Torznab response root element is %q, not rss", ErrProviderUpstream, root)
	}
	var document torznabFeed
	if err := xml.Unmarshal(payload, &document); err != nil {
		return SeedSearchResult{}, fmt.Errorf("%w: decode Torznab search: %v", ErrProviderUpstream, err)
	}
	total := len(document.Channel.Items)
	if total > torznabSearchWindow {
		return SeedSearchResult{}, fmt.Errorf("%w: Torznab search returned %d items, above the %d-item keyword window", ErrProviderUpstream, total, torznabSearchWindow)
	}
	result := SeedSearchResult{
		ParserVersion:   torznabContractVersion,
		Window:          torznabSearchLimit,
		SourceItemCount: total,
		Parse: domain.FeedParseResult{
			Format: domain.FeedFormatRSS,
			Site:   &domain.FeedSite{ID: domain.FeedSiteAniBT, Name: "AniBT", Identity: domain.FeedIdentityFromFeed, Match: domain.FeedSiteMatchHost},
			Items:  make([]domain.FeedItem, 0, total),
		},
	}
	for index, raw := range document.Channel.Items {
		item, reason := torznabSearchItem(raw)
		if reason != "" {
			result.Parse.Skipped = append(result.Parse.Skipped, domain.FeedItemSkip{Index: index, Title: raw.Title, Reason: reason})
			continue
		}
		result.Parse.Items = append(result.Parse.Items, item)
	}
	result.Items = result.Parse.Items
	if err := domain.RequireUsableItems(result.Parse); err != nil {
		return SeedSearchResult{}, err
	}
	return result, nil
}

// torznabSearchItem normalizes one item into a FeedItem, or names why it cannot
// be one. Identity comes from the published `infohash` attribute only: the
// location's own claims are never used as content identity, and a missing
// identity is not guessed from the title.
func torznabSearchItem(raw torznabItem) (domain.FeedItem, string) {
	item := domain.FeedItem{GUID: strings.TrimSpace(raw.GUID), Title: strings.TrimSpace(raw.Title), Link: strings.TrimSpace(raw.Link), Provider: "anibt"}
	if item.Title == "" {
		return domain.FeedItem{}, "title_missing"
	}
	published := parseTorznabTime(raw.PubDate)
	if published.IsZero() {
		return domain.FeedItem{}, "published_missing"
	}
	item.Published = published
	torrentURL, enclosureLength, contentType := torznabDownloadURL(raw)
	if torrentURL == "" {
		return domain.FeedItem{}, "download_url_missing"
	}
	item.TorrentURL = torrentURL
	item.EnclosureType = contentType
	var infoHash string
	var sizeBytes int64
	for _, attr := range raw.Attrs {
		value := strings.TrimSpace(attr.Value)
		switch strings.ToLower(strings.TrimSpace(attr.Name)) {
		case "infohash":
			infoHash = strings.ToLower(value)
		case "size":
			sizeBytes = parseTorznabSize(value)
		}
	}
	if infoHash != "" && !domain.IsCanonicalInfoHash(infoHash) {
		// The published identity is unusable: the row cannot be downloaded
		// safely, so it is reported instead of being stored untrustworthy.
		return domain.FeedItem{}, "infohash_invalid"
	}
	item.TorrentInfoHash = infoHash
	if sizeBytes == 0 {
		sizeBytes = parseTorznabSize(enclosureLength)
	}
	if sizeBytes > 0 {
		item.TorrentSizeBytes = sizeBytes
		item.EnclosureLength = sizeBytes
	}
	return item, ""
}

func torznabDownloadURL(raw torznabItem) (string, string, string) {
	var enclosureURL, enclosureLength, contentType string
	for _, enclosure := range raw.Enclosur {
		candidate := strings.TrimSpace(enclosure.URL)
		if candidate == "" {
			continue
		}
		enclosureURL, enclosureLength, contentType = candidate, strings.TrimSpace(enclosure.Length), strings.TrimSpace(enclosure.Type)
		break
	}
	for _, candidate := range []string{enclosureURL, strings.TrimSpace(raw.Link)} {
		if !isTorznabHTTPURL(candidate) {
			continue
		}
		return candidate, enclosureLength, contentType
	}
	return "", "", ""
}

func isTorznabHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == ""
}

func parseTorznabSize(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 || parsed > torznabMaxItemSize {
		return 0
	}
	return parsed
}

func parseTorznabTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// parseRetryAfter reads the Retry-After header in both documented forms: delay
// seconds and an HTTP date. A missing or unusable value is no wait, never a
// made-up one.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 || seconds > 86400 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		wait := when.Sub(now)
		if wait <= 0 || wait > 24*time.Hour {
			return 0
		}
		return wait
	}
	return 0
}

// NormalizeSearchQuery is the one query normalization both providers share: trim,
// collapse internal whitespace runs into one space, and keep every other
// character verbatim. The upstream text is never split into "title / group /
// episode" fields here: the two sites both accept a space-composed query and
// decide the match themselves.
func NormalizeSearchQuery(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func safeReasonText(value string) string {
	if bounded := strings.TrimSpace(value); bounded != "" {
		if len(bounded) > 512 {
			return strings.ToValidUTF8(bounded[:512], "")
		}
		return bounded
	}
	return "no description"
}
