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

// HTTPAniBTRSSFetcher is the boundary for AniBT's public anime RSS feed.
// The adapter keeps provider declarations as source facts; episode identity is
// still produced by the parser in the acquisition service.
type HTTPAniBTRSSFetcher struct {
	Endpoint          string
	Client            *http.Client
	AllowPrivateHosts []string
	Resolver          func(context.Context, string) ([]net.IP, error)
	MaxSize           int64
	TorrentReader     TorrentMetadataReader
}

// FetchAnime returns the usable items, failing when every item was skipped.
func (f HTTPAniBTRSSFetcher) FetchAnime(ctx context.Context, bgmID, groupSlug string) ([]domain.FeedItem, error) {
	return usableItems(f.FetchAnimeDetailed(ctx, bgmID, groupSlug))
}

// FetchAnimeDetailed fetches the provider-built AniBT URL and parses it with
// the AniBT adapter of the declared feed-site list, keeping item skip reasons.
func (f HTTPAniBTRSSFetcher) FetchAnimeDetailed(ctx context.Context, bgmID, groupSlug string) (RSSFetchResult, error) {
	if strings.TrimSpace(f.Endpoint) == "" {
		return RSSFetchResult{}, errors.New("AniBT endpoint is required")
	}
	parsedBGMID, err := strconv.ParseInt(strings.TrimSpace(bgmID), 10, 64)
	if err != nil || parsedBGMID <= 0 {
		return RSSFetchResult{}, errors.New("AniBT BGM ID must be a positive integer")
	}
	base, err := url.Parse(strings.TrimRight(f.Endpoint, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return RSSFetchResult{}, errors.New("AniBT endpoint must be an absolute HTTP URL without credentials")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/rss/anime.xml"
	query := base.Query()
	query.Set("bgmId", strconv.FormatInt(parsedBGMID, 10))
	if groupSlug = strings.TrimSpace(groupSlug); groupSlug != "" {
		query.Set("groupSlug", groupSlug)
	}
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
			return errors.New("AniBT RSS source exceeded redirect limit")
		}
		redirect, err := url.Parse(request.URL.String())
		if err != nil || (redirect.Scheme != "http" && redirect.Scheme != "https") || redirect.Hostname() == "" || redirect.User != nil || redirect.Fragment != "" {
			return errors.New("AniBT RSS redirect rejected")
		}
		return nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("create AniBT RSS request: %w", err)
	}
	request.Header.Set("Accept", "application/rss+xml, application/xml;q=0.9")
	request.Header.Set("User-Agent", buildinfo.UserAgent("acquisition"))
	applyFeedValidators(ctx, request)
	response, err := clientCopy.Do(request)
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("fetch AniBT RSS source: %w", redact.URLError(err))
	}
	defer response.Body.Close()
	if recordFeedResponse(ctx, response) {
		return RSSFetchResult{}, ErrFeedNotModified
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RSSFetchResult{}, fmt.Errorf("AniBT RSS source returned HTTP %d", response.StatusCode)
	}
	maxSize := f.MaxSize
	if maxSize <= 0 {
		maxSize = 10 << 20
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("read AniBT RSS source: %w", err)
	}
	if int64(len(payload)) > maxSize {
		return RSSFetchResult{}, errors.New("AniBT RSS source exceeds the configured size limit")
	}

	parsed, err := domain.ParseSiteFeed(domain.FeedSiteAniBT, payload)
	if err != nil {
		return RSSFetchResult{}, fmt.Errorf("parse AniBT RSS source: %w", err)
	}
	// The provider and the structured tags are feed facts, set before any
	// .torrent is fetched so the refresh's pre-fetch hook can judge the item
	// by the feed-layer filter exactly as its evaluation will.
	for index := range parsed.Items {
		item := &parsed.Items[index]
		item.Provider = "anibt"
		// The structured tags are source facts of this release. A legacy or
		// tag-less item yields an empty set plus a diagnostic, never an error.
		tags := domain.ExtractAniBTTags(item.Extensions)
		item.AniBTTags = &tags
	}
	items, err := enrichFeedItemsWithTorrentMetadata(ctx, parsed.Items, f.TorrentReader)
	if err != nil {
		return RSSFetchResult{}, err
	}
	for index := range items {
		// The source facts carry the torrent's identity and size.
		items[index].SourceFacts = anibtSourceFacts(items[index], groupSlug)
	}
	parsed.Items = items
	return RSSFetchResult{Items: items, Parse: parsed}, nil
}

func anibtSourceFacts(item domain.FeedItem, groupSlug string) []domain.SourceProvidedFact {
	observedAt := item.Published
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	infoHash := strings.TrimSpace(item.TorrentInfoHash)
	size := item.TorrentSizeBytes
	if size == 0 {
		size = item.EnclosureLength
	}
	values := []struct {
		name  string
		raw   string
		value any
	}{
		{"anibt.release_id", strings.TrimSpace(item.GUID), strings.TrimSpace(item.GUID)},
		{"anibt.release_url", strings.TrimSpace(item.Link), strings.TrimSpace(item.Link)},
		{"anibt.description", strings.TrimSpace(item.Description), strings.TrimSpace(item.Description)},
		{"anibt.torrent_url", strings.TrimSpace(item.DownloadURL()), strings.TrimSpace(item.DownloadURL())},
		{"anibt.info_hash", infoHash, infoHash},
		{"anibt.filename", strings.TrimSpace(item.TorrentFilename), strings.TrimSpace(item.TorrentFilename)},
		{"anibt.size_bytes", strconv.FormatInt(size, 10), size},
		{"anibt.group_slug", strings.TrimSpace(groupSlug), strings.TrimSpace(groupSlug)},
	}
	facts := make([]domain.SourceProvidedFact, 0, len(values))
	for _, value := range values {
		if value.raw == "" || (value.name == "anibt.size_bytes" && size == 0) {
			continue
		}
		facts = append(facts, domain.SourceProvidedFact{
			Provider:        "anibt",
			FieldName:       value.name,
			RawValue:        value.raw,
			NormalizedValue: map[string]any{"value": value.value},
			ObservedAt:      observedAt,
			SchemaVersion:   "anibt-rss-v1",
		})
	}
	return facts
}
