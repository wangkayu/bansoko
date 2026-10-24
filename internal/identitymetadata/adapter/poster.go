package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	identitymetadataport "bansoko/internal/identitymetadata/port"
	"golang.org/x/sync/singleflight"
)

// ErrPosterNotFound marks a Bangumi subject (or its image) that has no poster.
// The transport maps it to 404; other failures are upstream errors (502).
var ErrPosterNotFound = errors.New("poster not found")

var bgmIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

const (
	defaultPosterCacheTTL = 7 * 24 * time.Hour
	maxPosterCacheBytes   = 64 << 20
	maxPosterBytes        = 2 << 20
)

// ConfiguredPosterResolver serves catalog card posters. Bangumi image hosts
// (lain.bgm.tv) are unreachable from some browsers, so the frontend never
// references them directly: the resolver looks up the subject image through
// the configured BGM connection and streams the bytes back through the same
// network route. Successful poster bytes are retained in a bounded process
// cache and, when configured, a durable cache so repeated card reads avoid both
// upstream requests across resolver restarts.
type ConfiguredPosterResolver struct {
	Connections     ConnectionSource
	ClientResolver  HTTPClientResolver
	Client          *http.Client
	CacheTTL        time.Duration
	PersistentCache identitymetadataport.PosterCache

	mu            sync.Mutex
	cache         map[string]posterCacheEntry
	cacheBytes    int
	accessCounter uint64
	maxCacheBytes int
	flights       singleflight.Group
}

type posterCacheEntry struct {
	url             string
	urlExpiresAt    time.Time
	bytes           []byte
	contentType     string
	posterExpiresAt time.Time
	lastUsed        uint64
}

type posterFetchResult struct {
	bytes       []byte
	contentType string
	imageURL    string
	expiresAt   time.Time
}

// NewPosterResolver wires the poster boundary used by the image proxy route.
// The optional cache keeps existing memory-only callers source-compatible.
func NewPosterResolver(connections ConnectionSource, clientResolver HTTPClientResolver, persistentCache ...identitymetadataport.PosterCache) *ConfiguredPosterResolver {
	var cache identitymetadataport.PosterCache
	if len(persistentCache) > 0 {
		cache = persistentCache[0]
	}
	return &ConfiguredPosterResolver{
		Connections:     connections,
		ClientResolver:  clientResolver,
		CacheTTL:        defaultPosterCacheTTL,
		PersistentCache: cache,
		cache:           map[string]posterCacheEntry{},
		maxCacheBytes:   maxPosterCacheBytes,
	}
}

// FetchPoster resolves the subject's card-sized image and opens its byte stream.
// The caller owns closing the returned body. A stale cached poster is returned
// when a refresh fails, preserving the last-good image during upstream outage.
func (r *ConfiguredPosterResolver) FetchPoster(ctx context.Context, bgmID string) (io.ReadCloser, string, error) {
	bgmID = strings.TrimSpace(bgmID)
	if !bgmIDPattern.MatchString(bgmID) {
		return nil, "", ErrPosterNotFound
	}
	if body, contentType, ok := r.cachedPoster(bgmID, true); ok {
		return body, contentType, nil
	}
	if body, contentType, ok := r.persistentPoster(ctx, bgmID, true); ok {
		return body, contentType, nil
	}

	result, err := r.fetchPosterSingleflight(ctx, bgmID)
	if err == nil {
		return io.NopCloser(bytes.NewReader(result.bytes)), result.contentType, nil
	}
	if body, lastGoodType, ok := r.cachedPoster(bgmID, false); ok {
		return body, lastGoodType, nil
	}
	if body, lastGoodType, ok := r.persistentPoster(ctx, bgmID, false); ok {
		return body, lastGoodType, nil
	}
	return nil, "", err
}

func (r *ConfiguredPosterResolver) fetchPosterSingleflight(ctx context.Context, bgmID string) (posterFetchResult, error) {
	value, err, _ := r.flights.Do(bgmID, func() (any, error) {
		if cached, ok := r.cachedPosterData(bgmID, true); ok {
			return posterFetchResult{
				bytes:       cached.bytes,
				contentType: cached.contentType,
				imageURL:    cached.url,
				expiresAt:   cached.posterExpiresAt,
			}, nil
		}
		if cached, ok := r.persistentPosterData(ctx, bgmID, true); ok {
			return posterFetchResult{
				bytes:       cached.ImageBytes,
				contentType: cached.ContentType,
				imageURL:    cached.ImageURL,
				expiresAt:   cached.ExpiresAt,
			}, nil
		}

		result, fetchErr := r.fetchPoster(ctx, bgmID)
		if fetchErr != nil {
			return posterFetchResult{}, fetchErr
		}
		expiresAt := time.Now().UTC().Add(r.posterCacheTTL())
		result.expiresAt = expiresAt
		r.storePosterAt(bgmID, result.bytes, result.contentType, result.imageURL, expiresAt)
		if r.PersistentCache != nil {
			now := time.Now().UTC()
			_ = r.PersistentCache.Upsert(ctx, identitymetadataport.PosterCacheEntry{
				BGMSubjectID: bgmID,
				ImageBytes:   result.bytes,
				ContentType:  result.contentType,
				ImageURL:     result.imageURL,
				FetchedAt:    now,
				ExpiresAt:    expiresAt,
				LastUsed:     now,
			})
		}
		return result, nil
	})
	if err != nil {
		return posterFetchResult{}, err
	}
	result, ok := value.(posterFetchResult)
	if !ok {
		return posterFetchResult{}, errors.New("poster singleflight returned an invalid result")
	}
	return result, nil
}

func (r *ConfiguredPosterResolver) fetchPoster(ctx context.Context, bgmID string) (posterFetchResult, error) {
	connection, client, err := r.bgmClient(ctx)
	if err != nil {
		return posterFetchResult{}, err
	}
	imageURLs, err := r.subjectImageURLs(ctx, client, connection.Endpoint, bgmID)
	if err != nil {
		return posterFetchResult{}, err
	}
	// The resized rendition comes first; when the image host refuses it the
	// subject's own common image is still a usable poster.
	var lastErr error
	for _, imageURL := range imageURLs {
		result, fetchErr := fetchPosterImage(ctx, client, imageURL)
		if fetchErr == nil {
			return result, nil
		}
		lastErr = fetchErr
		if ctx.Err() != nil {
			break
		}
	}
	return posterFetchResult{}, lastErr
}

func fetchPosterImage(ctx context.Context, client *http.Client, imageURL string) (posterFetchResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return posterFetchResult{}, ErrPosterNotFound
	}
	request.Header.Set("Accept", "image/avif,image/webp,image/*")
	response, err := client.Do(request)
	if err != nil {
		return posterFetchResult{}, fmt.Errorf("request poster image: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return posterFetchResult{}, ErrPosterNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return posterFetchResult{}, fmt.Errorf("poster image host returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPosterBytes+1))
	if err != nil {
		return posterFetchResult{}, fmt.Errorf("read poster image: %w", err)
	}
	if len(data) > maxPosterBytes {
		return posterFetchResult{}, fmt.Errorf("poster image exceeds %d bytes", maxPosterBytes)
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return posterFetchResult{bytes: data, contentType: contentType, imageURL: imageURL}, nil
}

func (r *ConfiguredPosterResolver) bgmClient(ctx context.Context) (configurationdomain.Connection, *http.Client, error) {
	if r.Connections == nil {
		return configurationdomain.Connection{}, nil, errors.New("poster connection source is not configured")
	}
	connections, err := r.Connections.ListConnections(ctx)
	if err != nil {
		return configurationdomain.Connection{}, nil, fmt.Errorf("list BGM connections: %w", err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionBGM)
	if err != nil {
		return configurationdomain.Connection{}, nil, err
	}
	client := r.Client
	if r.ClientResolver != nil {
		resolved, resolveErr := r.ClientResolver.ClientForConnection(ctx, connection)
		if resolveErr != nil {
			return configurationdomain.Connection{}, nil, fmt.Errorf("resolve BGM network route: %w", resolveErr)
		}
		if resolved != nil {
			client = resolved
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	return connection, client, nil
}

// subjectImageURLs returns the cached subject image URL when fresh; otherwise
// it queries the legacy subject endpoint (no credential required, same shape
// the candidate search already consumes) and returns the image URLs to try in
// order: the r/400 resize of the large cover, then the common image. The URL
// that served the bytes is cached with them.
func (r *ConfiguredPosterResolver) subjectImageURLs(ctx context.Context, client *http.Client, baseEndpoint, bgmID string) ([]string, error) {
	if cached, ok := r.cachedURL(bgmID); ok {
		return []string{cached}, nil
	}
	endpoint, err := appendEndpointPath(baseEndpoint, "/subject/"+bgmID)
	if err != nil {
		return nil, ErrPosterNotFound
	}
	params := endpoint.Query()
	params.Set("responseGroup", "small")
	endpoint.RawQuery = params.Encode()
	var payload struct {
		ID     int `json:"id"`
		Images struct {
			Large  string `json:"large"`
			Common string `json:"common"`
		} `json:"images"`
	}
	if err := (ConfiguredCandidateSearcher{}).getJSON(ctx, client, endpoint, "", &payload); err != nil {
		return nil, fmt.Errorf("request Bangumi subject %s: %w", bgmID, err)
	}
	if payload.ID <= 0 || strconv.Itoa(payload.ID) != bgmID {
		return nil, ErrPosterNotFound
	}
	urls := make([]string, 0, 2)
	if resized := bgmResizedCoverURL(payload.Images.Large, payload.Images.Common); resized != "" {
		urls = append(urls, resized)
	}
	if common := strings.TrimSpace(payload.Images.Common); common != "" {
		urls = append(urls, common)
	}
	if len(urls) == 0 {
		return nil, ErrPosterNotFound
	}
	return urls, nil
}

// bgmCoverPathPattern matches Bangumi's sized cover paths
// (/pic/cover/{l,c,m,s,g}/<shard>/<file>) and captures the size-free tail.
var bgmCoverPathPattern = regexp.MustCompile(`^/pic/cover/[lcmsg]/(.+)$`)

// bgmPosterResize is the image host's documented resize prefix; r/400 of the
// large cover is 400 px wide (400x566 for a typical poster), sharp on a card
// at 2x DPR where the 150 px common image is upscaled.
const bgmPosterResize = "/r/400"

// bgmResizedCoverURL derives the r/400 rendition from the subject's large
// image (or, failing that, its common image) on the same host the API named.
// Anything not shaped like a Bangumi cover path yields "" so the caller keeps
// the common image.
func bgmResizedCoverURL(candidates ...string) string {
	for _, raw := range candidates {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			continue
		}
		match := bgmCoverPathPattern.FindStringSubmatch(parsed.Path)
		if match == nil {
			continue
		}
		resized := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: bgmPosterResize + "/pic/cover/l/" + match[1]}
		return resized.String()
	}
	return ""
}

func (r *ConfiguredPosterResolver) posterCacheTTL() time.Duration {
	if r.CacheTTL <= 0 {
		return defaultPosterCacheTTL
	}
	return r.CacheTTL
}

func (r *ConfiguredPosterResolver) cachedPoster(bgmID string, freshOnly bool) (io.ReadCloser, string, bool) {
	entry, ok := r.cachedPosterData(bgmID, freshOnly)
	if !ok {
		return nil, "", false
	}
	return io.NopCloser(bytes.NewReader(entry.bytes)), entry.contentType, true
}

func (r *ConfiguredPosterResolver) cachedPosterData(bgmID string, freshOnly bool) (posterCacheEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[bgmID]
	if !ok || len(entry.bytes) == 0 || len(entry.bytes) > maxPosterBytes || strings.TrimSpace(entry.contentType) == "" {
		return posterCacheEntry{}, false
	}
	if freshOnly && !entry.posterExpiresAt.After(time.Now()) {
		return posterCacheEntry{}, false
	}
	r.accessCounter++
	entry.lastUsed = r.accessCounter
	r.cache[bgmID] = entry
	entry.bytes = append([]byte(nil), entry.bytes...)
	return entry, true
}

func (r *ConfiguredPosterResolver) persistentPoster(ctx context.Context, bgmID string, freshOnly bool) (io.ReadCloser, string, bool) {
	entry, ok := r.persistentPosterData(ctx, bgmID, freshOnly)
	if !ok {
		return nil, "", false
	}
	return io.NopCloser(bytes.NewReader(entry.ImageBytes)), entry.ContentType, true
}

func (r *ConfiguredPosterResolver) persistentPosterData(ctx context.Context, bgmID string, freshOnly bool) (identitymetadataport.PosterCacheEntry, bool) {
	if r.PersistentCache == nil {
		return identitymetadataport.PosterCacheEntry{}, false
	}
	entry, found, err := r.PersistentCache.Get(ctx, bgmID)
	if err != nil || !found || entry.BGMSubjectID != bgmID || len(entry.ImageBytes) == 0 || len(entry.ImageBytes) > maxPosterBytes || strings.TrimSpace(entry.ContentType) == "" || strings.TrimSpace(entry.ImageURL) == "" || entry.ExpiresAt.IsZero() {
		return identitymetadataport.PosterCacheEntry{}, false
	}
	if freshOnly && !entry.ExpiresAt.After(time.Now()) {
		return identitymetadataport.PosterCacheEntry{}, false
	}
	r.storePosterAt(bgmID, entry.ImageBytes, entry.ContentType, entry.ImageURL, entry.ExpiresAt)
	entry.ImageBytes = append([]byte(nil), entry.ImageBytes...)
	return entry, true
}

func (r *ConfiguredPosterResolver) cachedURL(bgmID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[bgmID]
	if !ok || entry.url == "" || !entry.urlExpiresAt.After(time.Now()) {
		return "", false
	}
	return entry.url, true
}

func (r *ConfiguredPosterResolver) storePoster(bgmID string, data []byte, contentType string) {
	r.storePosterAt(bgmID, data, contentType, "", time.Now().UTC().Add(r.posterCacheTTL()))
}

func (r *ConfiguredPosterResolver) storePosterAt(bgmID string, data []byte, contentType, imageURL string, expiresAt time.Time) {
	if len(data) == 0 || len(data) > maxPosterBytes {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]posterCacheEntry{}
	}
	if r.maxCacheBytes <= 0 {
		r.maxCacheBytes = maxPosterCacheBytes
	}
	if previous, ok := r.cache[bgmID]; ok {
		r.cacheBytes -= len(previous.bytes)
	}
	r.accessCounter++
	entry := r.cache[bgmID]
	entry.bytes = append([]byte(nil), data...)
	entry.contentType = strings.TrimSpace(contentType)
	if imageURL != "" {
		entry.url = imageURL
	}
	entry.posterExpiresAt = expiresAt
	entry.urlExpiresAt = expiresAt
	entry.lastUsed = r.accessCounter
	r.cache[bgmID] = entry
	r.cacheBytes += len(data)
	for r.cacheBytes > r.maxCacheBytes {
		oldestID := ""
		var oldest uint64
		for id, candidate := range r.cache {
			if id == bgmID && len(r.cache) > 1 {
				continue
			}
			if oldestID == "" || candidate.lastUsed < oldest {
				oldestID = id
				oldest = candidate.lastUsed
			}
		}
		if oldestID == "" {
			delete(r.cache, bgmID)
			r.cacheBytes -= len(data)
			break
		}
		r.cacheBytes -= len(r.cache[oldestID].bytes)
		delete(r.cache, oldestID)
	}
}
