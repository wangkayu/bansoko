package matcher

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DataItem is the matching subset of one bangumi-data entry. Begin is only a
// season-selection clue; owner metadata facts still come from provider APIs.
type DataItem struct {
	BgmID   string
	MikanID string
	TmdbID  string // movie/ID or tv/ID[/season/N[/episode/N]]
	Titles  []string
	Begin   string
}

// BangumiDataIndex is an immutable in-memory lookup over one data snapshot.
// A refresh builds a new index and atomically publishes it as a whole.
type BangumiDataIndex struct {
	byBgmID   map[string]*DataItem
	byMikanID map[string]*DataItem
	byTitle   map[string]*DataItem
}

// NewBangumiDataIndex builds the same normalized lookup used by the CDN
// snapshot parser. It also keeps deterministic fixtures independent of HTTP.
func NewBangumiDataIndex(items []DataItem) *BangumiDataIndex {
	idx := &BangumiDataIndex{
		byBgmID:   make(map[string]*DataItem),
		byMikanID: make(map[string]*DataItem),
		byTitle:   make(map[string]*DataItem),
	}
	for i := range items {
		item := &items[i]
		item.BgmID = strings.TrimSpace(item.BgmID)
		item.MikanID = strings.TrimSpace(item.MikanID)
		item.TmdbID = strings.TrimSpace(item.TmdbID)
		item.Begin = strings.TrimSpace(item.Begin)
		if item.BgmID != "" {
			idx.byBgmID[item.BgmID] = item
		}
		if item.MikanID != "" {
			idx.byMikanID[item.MikanID] = item
		}
		for _, title := range item.Titles {
			key := normalizeLookupKey(title)
			if key != "" {
				// Keep the existing deterministic first-write-wins title policy.
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
	Begin          string              `json:"begin"`
	Sites          []bgmDataSite       `json:"sites"`
}

type bgmDataSite struct {
	Site string `json:"site"`
	ID   string `json:"id"`
}

// ParseBangumiData validates and indexes one complete CDN payload.
func ParseBangumiData(payload []byte) (*BangumiDataIndex, error) {
	var decoded bgmDataPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode bangumi-data JSON: %w", err)
	}
	if len(decoded.Items) == 0 {
		return nil, fmt.Errorf("bangumi-data payload has no items")
	}
	items := make([]DataItem, 0, len(decoded.Items))
	for i := range decoded.Items {
		item := &decoded.Items[i]
		dataItem := DataItem{
			Titles: collectTitles(item),
			Begin:  strings.TrimSpace(item.Begin),
		}
		for _, site := range item.Sites {
			switch site.Site {
			case "bangumi":
				dataItem.BgmID = strings.TrimSpace(site.ID)
			case "mikan":
				dataItem.MikanID = strings.TrimSpace(site.ID)
			case "tmdb":
				dataItem.TmdbID = strings.TrimSpace(site.ID)
			}
		}
		if dataItem.BgmID == "" && dataItem.MikanID == "" && dataItem.TmdbID == "" {
			continue
		}
		items = append(items, dataItem)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("bangumi-data payload has no usable items")
	}
	return NewBangumiDataIndex(items), nil
}

func (idx *BangumiDataIndex) LookupByBgmID(bgmID string) *DataItem {
	if idx == nil {
		return nil
	}
	return idx.byBgmID[strings.TrimSpace(bgmID)]
}

func (idx *BangumiDataIndex) LookupByMikanID(mikanID string) *DataItem {
	if idx == nil {
		return nil
	}
	return idx.byMikanID[strings.TrimSpace(mikanID)]
}

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
