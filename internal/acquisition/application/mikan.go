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

	"bansoko/internal/acquisition/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/redact"
	"bansoko/internal/runtime/buildinfo"
)

type HTTPMikanRSSFetcher struct {
	Endpoint          string
	Client            *http.Client
	AllowPrivateHosts []string
	Resolver          func(context.Context, string) ([]net.IP, error)
	MaxSize           int64
	TorrentReader     TorrentMetadataReader
}

// FetchAnime returns the usable items, failing when every item was skipped.
func (f HTTPMikanRSSFetcher) FetchAnime(ctx context.Context, bangumiID, subgroupID string) ([]domain.FeedItem, error) {
	return usableItems(f.FetchAnimeDetailed(ctx, bangumiID, subgroupID))
}

// FetchAnimeDetailed fetches the provider-built Mikan URL and parses it with
// the Mikan adapter of the declared feed-site list, keeping item skip reasons.
func (f HTTPMikanRSSFetcher) FetchAnimeDetailed(ctx context.Context, bangumiID, subgroupID string) (RSSFetchResult, error) {
	bangumi, err := normalizedPositiveID(bangumiID, "Mikan bangumi ID")
	if err != nil {
		return RSSFetchResult{}, err
	}
	subgroup, err := normalizedPositiveID(subgroupID, "Mikan subgroup ID")
	if err != nil {
		return RSSFetchResult{}, err
	}
	base, err := url.Parse(strings.TrimRight(f.Endpoint, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return RSSFetchResult{}, errors.New("Mikan endpoint must be an absolute HTTP URL without credentials")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/RSS/Bangumi"
	query := base.Query()
	query.Set("bangumiId", bangumi)
	query.Set("subgroupid", subgroup)
	base.RawQuery = query.Encode()

	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	clientCopy := *client
	transport, err := (HTTPRSSFetcher{Client: &clientCopy, Resolver: f.Resolver, AllowPrivateHosts: f.AllowPrivateHosts}).secureTransport(clientCopy.Transport)
	if err != nil {
		return RSSFetchResult{}, err
	}
	clientCopy.Transport = transport
	clientCopy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("Mikan RSS source exceeded redirect limit")
		}
		redirect := request.URL
		if (redirect.Scheme != "http" && redirect.Scheme != "https") || redirect.Hostname() == "" || redirect.User != nil || redirect.Fragment != "" {
			return errors.New("Mikan RSS redirect rejected")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("create Mikan RSS request: %w", err)
	}
	request.Header.Set("Accept", "application/rss+xml, application/xml;q=0.9")
	request.Header.Set("User-Agent", buildinfo.UserAgent("acquisition"))
	applyFeedValidators(ctx, request)
	response, err := clientCopy.Do(request)
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("fetch Mikan RSS source: %w", redact.URLError(err))
	}
	defer response.Body.Close()
	if recordFeedResponse(ctx, response) {
		return RSSFetchResult{}, ErrFeedNotModified
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RSSFetchResult{}, fmt.Errorf("Mikan RSS source returned HTTP %d", response.StatusCode)
	}
	maxSize := f.MaxSize
	if maxSize <= 0 {
		maxSize = 10 << 20
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("read Mikan RSS source: %w", err)
	}
	if int64(len(payload)) > maxSize {
		return RSSFetchResult{}, errors.New("Mikan RSS source exceeds the configured size limit")
	}
	parsed, err := domain.ParseSiteFeed(domain.FeedSiteMikan, payload)
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("parse Mikan RSS source: %w", err)
	}
	// The provider is a feed fact, set before any .torrent is fetched so the
	// refresh's pre-fetch hook sees the item its evaluation will.
	for index := range parsed.Items {
		parsed.Items[index].Provider = "mikan"
	}
	items, err := enrichFeedItemsWithTorrentMetadata(ctx, parsed.Items, f.TorrentReader)
	if err != nil {
		return RSSFetchResult{}, err
	}
	for index := range items {
		item := &items[index]
		observedAt := item.Published
		if observedAt.IsZero() {
			observedAt = time.Now().UTC()
		}
		item.SourceFacts = append(item.SourceFacts,
			domain.SourceProvidedFact{Provider: "mikan", FieldName: "mikan.bangumi_id", RawValue: bangumi, NormalizedValue: map[string]any{"value": bangumi}, ObservedAt: observedAt, SchemaVersion: "mikan-rss-v1"},
			domain.SourceProvidedFact{Provider: "mikan", FieldName: "mikan.subgroup_id", RawValue: subgroup, NormalizedValue: map[string]any{"value": subgroup}, ObservedAt: observedAt, SchemaVersion: "mikan-rss-v1"},
			domain.SourceProvidedFact{Provider: "mikan", FieldName: "mikan.release_id", RawValue: strings.TrimSpace(item.GUID), NormalizedValue: map[string]any{"value": strings.TrimSpace(item.GUID)}, ObservedAt: observedAt, SchemaVersion: "mikan-rss-v1"},
			domain.SourceProvidedFact{Provider: "mikan", FieldName: "mikan.release_url", RawValue: strings.TrimSpace(item.Link), NormalizedValue: map[string]any{"value": strings.TrimSpace(item.Link)}, ObservedAt: observedAt, SchemaVersion: "mikan-rss-v1"},
		)
		if description := strings.TrimSpace(item.Description); description != "" {
			item.SourceFacts = append(item.SourceFacts, domain.SourceProvidedFact{Provider: "mikan", FieldName: "mikan.description", RawValue: description, NormalizedValue: map[string]any{"value": description}, ObservedAt: observedAt, SchemaVersion: "mikan-rss-v1"})
		}
		if size := item.EnclosureLength; size > 0 {
			item.SourceFacts = append(item.SourceFacts, domain.SourceProvidedFact{Provider: "mikan", FieldName: "mikan.size_bytes", RawValue: strconv.FormatInt(size, 10), NormalizedValue: map[string]any{"value": size}, ObservedAt: observedAt, SchemaVersion: "mikan-rss-v1"})
		}
		if torrentURL := strings.TrimSpace(item.DownloadURL()); torrentURL != "" {
			item.SourceFacts = append(item.SourceFacts, domain.SourceProvidedFact{Provider: "mikan", FieldName: "mikan.torrent_url", RawValue: torrentURL, NormalizedValue: map[string]any{"value": torrentURL}, ObservedAt: observedAt, SchemaVersion: "mikan-rss-v1"})
		}
		if pubDate := item.Published; !pubDate.IsZero() {
			item.SourceFacts = append(item.SourceFacts, domain.SourceProvidedFact{Provider: "mikan", FieldName: "mikan.pub_date", RawValue: pubDate.Format(time.RFC3339Nano), NormalizedValue: map[string]any{"value": pubDate.Format(time.RFC3339Nano)}, ObservedAt: observedAt, SchemaVersion: "mikan-rss-v1"})
		}
	}
	parsed.Items = items
	return RSSFetchResult{Items: items, Parse: parsed}, nil
}

func normalizedPositiveID(value, label string) (string, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed == 0 {
		return "", fmt.Errorf("%s must be a positive integer", label)
	}
	normalized := strconv.FormatUint(parsed, 10)
	if normalized != value {
		return "", fmt.Errorf("%s must be normalized", label)
	}
	return normalized, nil
}
