package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/runtime/buildinfo"
	"github.com/PuerkitoBio/goquery"
)

const aniBTGroupSchemaVersion = "anibt-anime-groups-v1"

// aniBTGroupDirectoryPath is the single server-rendered page that carries every
// AniBT subtitle group. The page has no pagination: `?page=2` and `?page=999`
// return the same document, so one request is the whole AniBT group directory.
const aniBTGroupDirectoryPath = "/en/group"

// aniBTGroupCardAriaPrefix is the accessible name each group card link
// carries: `aria-label="Open subtitle group: <display name>"`.
const aniBTGroupCardAriaPrefix = "Open subtitle group: "

// maxAniBTGroupDirectorySize bounds the directory page separately from the
// shared provider response bound: the page is far larger than an API response
// (about 300 KB) and a runaway body must still be
// refused.
const maxAniBTGroupDirectorySize = 8 << 20

// maxAniBTGroupNameLength bounds one display name taken from the page.
const maxAniBTGroupNameLength = 256

// aniBTIslandGroupPattern mirrors the dataset scraper's read of the TanStack
// Router data island: in the `$R[n]` reference stream each group object
// carries `_id:"…"`, then `name:"…"`, then `slug:"…"` within a bounded window.
// The island is the second channel the card attributes are checked against.
var aniBTIslandGroupPattern = regexp.MustCompile(`_id:"([A-Za-z0-9_]+)"[\s\S]{0,400}?name:"((?:[^"\\]|\\.)+)"[\s\S]{0,200}?slug:"([a-z0-9-]+)"`)

type AniBTGroupReader struct {
	Endpoint string
	Client   *http.Client
	Now      func() time.Time
}

func (r AniBTGroupReader) ReadGroups(ctx context.Context, catalogEntryRef string) (providerdomain.GroupReadResult, error) {
	bgmID, err := strconv.ParseUint(strings.TrimSpace(catalogEntryRef), 10, 64)
	if err != nil || bgmID == 0 || strconv.FormatUint(bgmID, 10) != catalogEntryRef {
		return providerdomain.GroupReadResult{}, errors.New("AniBT BGM ID must be a normalized positive integer")
	}
	endpoint, err := providerEndpoint(r.Endpoint, "/api/anime/groups")
	if err != nil {
		return providerdomain.GroupReadResult{}, err
	}
	query := endpoint.Query()
	query.Set("bgmId", catalogEntryRef)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return providerdomain.GroupReadResult{}, fmt.Errorf("create AniBT group request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", buildinfo.UserAgent("provider"))
	response, err := httpClient(r.Client).Do(request)
	if err != nil {
		return providerdomain.GroupReadResult{}, fmt.Errorf("fetch AniBT groups: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providerdomain.GroupReadResult{}, fmt.Errorf("AniBT groups returned HTTP %d", response.StatusCode)
	}
	payload, err := readBoundedResponse(response)
	if err != nil {
		return providerdomain.GroupReadResult{}, fmt.Errorf("read AniBT groups: %w", err)
	}
	var document struct {
		OK   *bool `json:"ok"`
		Data struct {
			BGMID  int64 `json:"bgmId"`
			Total  int   `json:"total"`
			Groups []struct {
				Slug          string `json:"slug"`
				Name          string `json:"name"`
				Status        string `json:"status"`
				TotalReleases int    `json:"totalReleases"`
				LastUpdatedAt int64  `json:"lastUpdatedAt"`
			} `json:"groups"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&document); err != nil {
		return providerdomain.GroupReadResult{}, fmt.Errorf("decode AniBT groups: %w", err)
	}
	if document.OK != nil && !*document.OK {
		return providerdomain.GroupReadResult{}, errors.New("AniBT group response is not marked ok")
	}
	if document.Data.BGMID != int64(bgmID) {
		return providerdomain.GroupReadResult{}, errors.New("AniBT group response BGM ID does not match request")
	}
	if document.Data.Total != len(document.Data.Groups) {
		return providerdomain.GroupReadResult{}, errors.New("AniBT group response total does not match groups")
	}
	observedAt := time.Now().UTC()
	if r.Now != nil {
		observedAt = r.Now().UTC()
	}
	result := providerdomain.GroupReadResult{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: catalogEntryRef, ObservedAt: observedAt, SourceVersion: response.Header.Get("ETag"), SchemaVersion: aniBTGroupSchemaVersion, Groups: make([]providerdomain.GroupSnapshotEntry, 0, len(document.Data.Groups))}
	result.Complete = document.OK != nil && *document.OK
	result.CompletenessEvidence = "anibt-entry-total-v1"
	seen := make(map[string]struct{}, len(document.Data.Groups))
	for _, item := range document.Data.Groups {
		ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, item.Slug)
		if err != nil {
			return providerdomain.GroupReadResult{}, fmt.Errorf("invalid AniBT group: %w", err)
		}
		if strings.TrimSpace(item.Name) == "" {
			return providerdomain.GroupReadResult{}, fmt.Errorf("AniBT group %q has no display name", ref.ExternalID)
		}
		if _, exists := seen[ref.Key()]; exists {
			return providerdomain.GroupReadResult{}, fmt.Errorf("AniBT groups contain duplicate slug %q", ref.ExternalID)
		}
		seen[ref.Key()] = struct{}{}
		active := strings.EqualFold(item.Status, "active")
		if item.Status == "" {
			result.Complete = false
		}
		result.Groups = append(result.Groups, providerdomain.GroupSnapshotEntry{
			Ref: ref, DisplayName: strings.TrimSpace(item.Name), ObservedAt: observedAt,
			Active:      &active,
			FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: catalogEntryRef, Group: ref},
		})
	}
	return result, nil
}

// ReadGroupCatalog reads the AniBT group directory once and returns every group
// on it. It is the whole AniBT group set as the site publishes it: one request,
// no pagination, no per-group follow-up.
//
// The cards are the primary channel (`data-group-slug` plus the
// `Open subtitle group: <name>` accessible name); the server-rendered data
// island is the second channel, and a slug whose island name disagrees with its
// card name is refused as a page-structure change instead of being resolved by
// guesswork. An absent island degrades to the card channel alone, which is
// enough on its own; an empty card channel is an error, because a directory
// page that publishes no group is a page this reader does not understand.
func (r AniBTGroupReader) ReadGroupCatalog(ctx context.Context) ([]providerdomain.GroupEntry, error) {
	endpoint, err := providerEndpoint(r.Endpoint, aniBTGroupDirectoryPath)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create AniBT group directory request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", buildinfo.UserAgent("provider"))
	response, err := httpClient(r.Client).Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch AniBT group directory: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("AniBT group directory returned HTTP %d", response.StatusCode)
	}
	payload, err := readBoundedBody(response, maxAniBTGroupDirectorySize)
	if err != nil {
		return nil, fmt.Errorf("read AniBT group directory: %w", err)
	}
	document, err := goquery.NewDocumentFromReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("parse AniBT group directory HTML: %w", err)
	}
	islandNames := aniBTIslandNames(payload)
	entries := make([]providerdomain.GroupEntry, 0, 64)
	seen := make(map[string]struct{})
	var parseErr error
	document.Find("[data-group-slug]").EachWithBreak(func(_ int, card *goquery.Selection) bool {
		slug := strings.TrimSpace(card.AttrOr("data-group-slug", ""))
		name := aniBTGroupCardName(card)
		if slug == "" || name == "" {
			parseErr = errors.New("AniBT group directory card is missing its slug or display name")
			return false
		}
		ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, slug)
		if err != nil {
			parseErr = fmt.Errorf("invalid AniBT group slug: %w", err)
			return false
		}
		if len(name) > maxAniBTGroupNameLength {
			parseErr = fmt.Errorf("AniBT group %q has an unbounded display name", ref.ExternalID)
			return false
		}
		if _, exists := seen[ref.Key()]; exists {
			parseErr = fmt.Errorf("AniBT group directory contains duplicate slug %q", ref.ExternalID)
			return false
		}
		if islandName, ok := islandNames[ref.ExternalID]; ok && islandName != name {
			parseErr = fmt.Errorf("AniBT group %q is named %q on its card and %q in the page data island", ref.ExternalID, name, islandName)
			return false
		}
		seen[ref.Key()] = struct{}{}
		entries = append(entries, providerdomain.GroupEntry{
			Ref: ref, DisplayName: name, Available: true,
		})
		return true
	})
	if parseErr != nil {
		return nil, parseErr
	}
	if len(entries) == 0 {
		return nil, errors.New("AniBT group directory published no group cards")
	}
	return entries, nil
}

// aniBTGroupCardName reads the display name of one group card from its
// accessible name, on the card itself or on a descendant link.
func aniBTGroupCardName(card *goquery.Selection) string {
	if name, ok := aniBTGroupAriaName(card.AttrOr("aria-label", "")); ok {
		return name
	}
	name := ""
	card.Find("[aria-label]").EachWithBreak(func(_ int, labelled *goquery.Selection) bool {
		if candidate, ok := aniBTGroupAriaName(labelled.AttrOr("aria-label", "")); ok {
			name = candidate
			return false
		}
		return true
	})
	return name
}

func aniBTGroupAriaName(label string) (string, bool) {
	if !strings.HasPrefix(label, aniBTGroupCardAriaPrefix) {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimPrefix(label, aniBTGroupCardAriaPrefix))
	return name, name != ""
}

// aniBTIslandNames maps slug → display name from the server-rendered data
// island. It returns nil when the island is absent or unreadable, because the
// card channel stands on its own. A name the island escaped as a JavaScript
// string literal is skipped: it cannot be compared to the decoded card text
// without re-implementing the escape decoder.
func aniBTIslandNames(payload []byte) map[string]string {
	matches := aniBTIslandGroupPattern.FindAllSubmatch(payload, -1)
	if len(matches) == 0 {
		return nil
	}
	names := make(map[string]string, len(matches))
	for _, match := range matches {
		slug := string(match[3])
		name := string(match[2])
		if strings.ContainsAny(name, `\`) {
			continue
		}
		names[slug] = name
	}
	return names
}
