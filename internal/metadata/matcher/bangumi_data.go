package matcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/runtime/buildinfo"
)

// bangumiDataURL is the CDN source for the bangumi-data project's static JSON
// dataset (~7 MB, ~8.5k anime entries). The dataset maps titles and external
// site IDs (mikan, bgm, tmdb, etc.) cross-reference.
const bangumiDataURL = "https://unpkg.com/bangumi-data@0.3/dist/data.json"

// DataItem is a single bangumi-data entry indexed by mikan ID and/or title.
type DataItem struct {
	BgmID  string // bangumi subject id (numeric string)
	TmdbID string // "tv/85937" or "movie/12345"
	Titles []string
}

// BangumiDataIndex is an in-memory lookup over the bangumi-data dataset. It is
// built once at startup and reused across dispatcher cycles.
type BangumiDataIndex struct {
	byMikanID map[string]*DataItem
	byTitle   map[string]*DataItem
}

// NewBangumiDataIndex builds the same normalized lookup used by the remote
// bangumi-data loader. It is also useful for deterministic application tests.
func NewBangumiDataIndex(items []DataItem) *BangumiDataIndex {
	idx := &BangumiDataIndex{
		byMikanID: make(map[string]*DataItem),
		byTitle:   make(map[string]*DataItem),
	}
	for i := range items {
		item := &items[i]
		if item.BgmID == "" && item.TmdbID == "" {
			continue
		}
		if item.Titles == nil {
			continue
		}
		for _, title := range item.Titles {
			key := normalizeLookupKey(title)
			if key != "" {
				if _, exists := idx.byTitle[key]; !exists {
					idx.byTitle[key] = item
				}
			}
		}
	}
	return idx
}

type bgmDataPayload struct {
	Items []bgmDataItem `json:"items"`
}

type bgmDataItem struct {
	Title          string              `json:"title"`
	TitleTranslate map[string][]string `json:"titleTranslate"`
	Sites          []bgmDataSite       `json:"sites"`
}

type bgmDataSite struct {
	Site string `json:"site"`
	ID   string `json:"id"`
}

// LoadBangumiData downloads the bangumi-data JSON from CDN and builds the
// mikan-ID and title indexes. A nil index (no error) is returned when the
// dataset is temporarily unavailable so callers can proceed with API fallback.
func LoadBangumiData(ctx context.Context, client *http.Client) (*BangumiDataIndex, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bangumiDataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build bangumi-data request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", buildinfo.UserAgent("metadata-matcher"))
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch bangumi-data: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bangumi-data CDN returned HTTP %d", resp.StatusCode)
	}

	var payload bgmDataPayload
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)) // 32 MB cap
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode bangumi-data JSON: %w", err)
	}

	idx := &BangumiDataIndex{
		byMikanID: make(map[string]*DataItem),
		byTitle:   make(map[string]*DataItem),
	}
	for i := range payload.Items {
		item := &payload.Items[i]
		var bgmID, mikanID, tmdbID string
		for _, site := range item.Sites {
			switch site.Site {
			case "bangumi":
				bgmID = site.ID
			case "mikan":
				mikanID = site.ID
			case "tmdb":
				tmdbID = site.ID
			}
		}
		// Only index entries that have at least one useful ID.
		if bgmID == "" && tmdbID == "" {
			continue
		}
		dataItem := &DataItem{
			BgmID:  bgmID,
			TmdbID: tmdbID,
			Titles: collectTitles(item),
		}
		if mikanID != "" {
			idx.byMikanID[mikanID] = dataItem
		}
		for _, title := range dataItem.Titles {
			key := normalizeLookupKey(title)
			if key != "" {
				// First-write-wins: earlier entries keep the slot.
				if _, exists := idx.byTitle[key]; !exists {
					idx.byTitle[key] = dataItem
				}
			}
		}
	}
	return idx, nil
}

// LookupByMikanID returns the bangumi-data entry for the given mikan anime ID,
// or nil if no entry maps to it.
func (idx *BangumiDataIndex) LookupByMikanID(mikanID string) *DataItem {
	if idx == nil {
		return nil
	}
	return idx.byMikanID[mikanID]
}

// LookupByTitle returns the bangumi-data entry whose title (or translated
// title) matches the given query after normalization, or nil if no match.
func (idx *BangumiDataIndex) LookupByTitle(title string) *DataItem {
	if idx == nil {
		return nil
	}
	return idx.byTitle[normalizeLookupKey(title)]
}

func collectTitles(item *bgmDataItem) []string {
	titles := []string{item.Title}
	for _, variants := range item.TitleTranslate {
		titles = append(titles, variants...)
	}
	return titles
}

func normalizeLookupKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
