package application

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/jackett"
	"bansoko/internal/redact"
	"bansoko/internal/runtime/buildinfo"
)

// Jackett seed categories: the torznab `category` attribute of a search item
// is only a numeric id; its name lives in the owning indexer's caps document
// (`/api/v2.0/indexers/<id>/results/torznab/api?t=caps`). Caps are read once
// per (endpoint, indexer) and cached for the process lifetime: they change
// rarely, and a keyword search must not pay one extra request per indexer per
// search. A fetch that fails is NOT cached — the next search retries it — and
// never fails the search: the rows then simply carry no categories.

// jackettCapsCacheBound caps the cache's site count so a hostile or broken
// aggregate inventing fresh indexer ids cannot grow memory without limit.
const jackettCapsCacheBound = 256

type jackettCapsCache struct {
	mu      sync.Mutex
	entries map[string]map[string]string
}

func newJackettCapsCache() *jackettCapsCache {
	return &jackettCapsCache{entries: make(map[string]map[string]string)}
}

func (c *jackettCapsCache) get(key string) (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	names, ok := c.entries[key]
	return names, ok
}

func (c *jackettCapsCache) put(key string, names map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok && len(c.entries) >= jackettCapsCacheBound {
		// The bound is a site count, not an eviction policy: resetting keeps one
		// misbehaving endpoint from pinning a full table of stale entries.
		c.entries = make(map[string]map[string]string, jackettCapsCacheBound)
	}
	c.entries[key] = names
}

// jackettCaps is the process-wide cache. HTTPJackettSearcher values are copied
// per search, so the cache lives here rather than on the reader.
var jackettCaps = newJackettCapsCache()

// resolveSeedCategories fills every item's caps-resolved categories. It is a
// display-fact enrichment: any failure leaves the item without categories.
func (s HTTPJackettSearcher) resolveSeedCategories(ctx context.Context, client *http.Client, items []acquisitiondomain.FeedItem) {
	fetched := make(map[string]map[string]string, 4)
	for index := range items {
		item := &items[index]
		if len(item.SeedCategoryIDs) == 0 || item.SourceSite == nil {
			continue
		}
		indexerID := item.SourceSite.ID
		names, attempted := fetched[indexerID]
		if !attempted {
			names = s.fetchIndexerCaps(ctx, client, indexerID)
			fetched[indexerID] = names
		}
		if len(names) == 0 {
			continue
		}
		categories := make([]acquisitiondomain.SeedCategory, 0, len(item.SeedCategoryIDs))
		seen := make(map[string]struct{}, len(item.SeedCategoryIDs))
		for _, id := range item.SeedCategoryIDs {
			name := strings.TrimSpace(names[id])
			if name == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			category := acquisitiondomain.SeedCategory{ID: id, Name: name}
			if err := category.Validate(); err != nil {
				continue
			}
			categories = append(categories, category)
		}
		item.SeedCategories = categories
	}
}

// fetchIndexerCaps reads one indexer's caps and returns its category id → name
// map. The cache key is the credential-free endpoint plus the indexer id: the
// API key is request-only and never keys state. Every failure — transport,
// status, protocol error, undecodable body — returns nil and is not cached.
func (s HTTPJackettSearcher) fetchIndexerCaps(ctx context.Context, client *http.Client, indexerID string) map[string]string {
	cacheKey := strings.TrimRight(strings.TrimSpace(s.Endpoint), "/") + "\x00" + indexerID
	if names, ok := jackettCaps.get(cacheKey); ok {
		return names
	}
	endpoint, err := jackett.IndexerURL(s.Endpoint, indexerID, s.APIKey, url.Values{"t": {"caps"}})
	if err != nil {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil
	}
	request.Header.Set("Accept", "application/xml;q=0.9")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	maxSize := s.MaxSize
	if maxSize <= 0 {
		maxSize = maxTorznabResponseSize
	}
	payload, err := readBoundedBody(response.Body, maxSize)
	if err != nil {
		return nil
	}
	names, err := parseTorznabCaps(payload)
	if err != nil {
		return nil
	}
	jackettCaps.put(cacheKey, names)
	return names
}

// torznabCapsDocument is the one part of a caps response this reader uses: the
// category id → name dictionary, including subcategories, which carry the ids
// search items actually report.
type torznabCapsDocument struct {
	XMLName    xml.Name `xml:"caps"`
	Categories struct {
		Categories []struct {
			ID      string `xml:"id,attr"`
			Name    string `xml:"name,attr"`
			Subcats []struct {
				ID   string `xml:"id,attr"`
				Name string `xml:"name,attr"`
			} `xml:"subcat"`
		} `xml:"category"`
	} `xml:"categories"`
}

func parseTorznabCaps(payload []byte) (map[string]string, error) {
	root, err := torznabRootName(payload)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(root, "caps") {
		return nil, fmt.Errorf("torznab caps root element is %q, not caps", redact.Text(root, 64))
	}
	var document torznabCapsDocument
	if err := xml.Unmarshal(payload, &document); err != nil {
		return nil, fmt.Errorf("decode torznab caps: %w", err)
	}
	names := make(map[string]string, len(document.Categories.Categories))
	for _, category := range document.Categories.Categories {
		if id, name := strings.TrimSpace(category.ID), strings.TrimSpace(category.Name); id != "" && name != "" {
			names[id] = name
		}
		for _, sub := range category.Subcats {
			if id, name := strings.TrimSpace(sub.ID), strings.TrimSpace(sub.Name); id != "" && name != "" {
				names[id] = name
			}
		}
	}
	return names, nil
}
