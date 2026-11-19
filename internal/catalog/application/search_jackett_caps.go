package application

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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

const (
	jackettCapsCacheBound = 256
	jackettCapsWorkers    = 4
	jackettCapsBudget     = 2 * time.Second
	jackettCapsSiteBudget = 750 * time.Millisecond
)

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

type jackettCapsLookup struct {
	id    string
	names map[string]string
}

// resolveSeedCategories treats caps as display evidence. Cache hits do not
// consume worker slots, and cold sites share a budget independent of the search.
func (s HTTPJackettSearcher) resolveSeedCategories(ctx context.Context, client *http.Client, items []acquisitiondomain.FeedItem) {
	bySite := make(map[string]*jackettCapsLookup)
	var misses []*jackettCapsLookup
	endpointKey := strings.TrimRight(strings.TrimSpace(s.Endpoint), "/") + "\x00"
	for _, item := range items {
		if len(item.SeedCategoryIDs) == 0 || item.SourceSite == nil {
			continue
		}
		id := item.SourceSite.ID
		if _, seen := bySite[id]; seen {
			continue
		}
		names, cached := jackettCaps.get(endpointKey + id)
		lookup := &jackettCapsLookup{id: id, names: names}
		bySite[id] = lookup
		if !cached {
			misses = append(misses, lookup)
		}
	}
	if len(misses) > 0 {
		s.resolveColdCaps(ctx, client, endpointKey, misses)
	}
	for index := range items {
		item := &items[index]
		if item.SourceSite != nil {
			if lookup := bySite[item.SourceSite.ID]; lookup != nil {
				item.SeedCategories = selectJackettCategories(item.SeedCategoryIDs, lookup.names)
			}
		}
	}
}

func (s HTTPJackettSearcher) resolveColdCaps(ctx context.Context, client *http.Client, endpointKey string, misses []*jackettCapsLookup) {
	ctx, cancel := context.WithTimeout(ctx, jackettCapsBudget)
	defer cancel()
	pending := make(chan *jackettCapsLookup, len(misses))
	for _, lookup := range misses {
		pending <- lookup
	}
	close(pending)

	var workers sync.WaitGroup
	for range min(jackettCapsWorkers, len(misses)) {
		workers.Go(func() {
			for lookup := range pending {
				if ctx.Err() != nil {
					return
				}
				siteCtx, cancelSite := context.WithTimeout(ctx, jackettCapsSiteBudget)
				lookup.names = s.fetchIndexerCaps(siteCtx, client, lookup.id, endpointKey+lookup.id)
				cancelSite()
			}
		})
	}
	workers.Wait()
}

func selectJackettCategories(ids []string, names map[string]string) []acquisitiondomain.SeedCategory {
	if len(ids) == 0 || len(names) == 0 {
		return nil
	}
	var categories []acquisitiondomain.SeedCategory
	seen := make(map[string]struct{}, min(len(ids), acquisitiondomain.MaxSeedCategories))
	for _, id := range ids {
		if name := names[id]; name != "" {
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			category := acquisitiondomain.SeedCategory{ID: id, Name: name}
			if err := category.Validate(); err != nil {
				continue
			}
			seen[id] = struct{}{}
			categories = append(categories, category)
			if len(categories) == acquisitiondomain.MaxSeedCategories {
				break
			}
		}
	}
	return categories
}

// The cache key is the credential-free endpoint and indexer ID. Failed reads
// return nil without caching so a subsequent search can retry them.
func (s HTTPJackettSearcher) fetchIndexerCaps(ctx context.Context, client *http.Client, indexerID, cacheKey string) map[string]string {
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
