package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/catalog/domain"
	"bansoko/internal/events"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/runtime/buildinfo"
	"golang.org/x/sync/singleflight"
)

type Reader interface {
	ReadSeason(context.Context, string, domain.ReadHints) (domain.ReadResult, error)
}

// Searcher is the optional side-effect-free provider keyword search port.
// AniBT exposes a public /api/bgm/search endpoint; Mikan has no public search
// page, so only providers that implement Search advertise it. The transport
// surfaces SEARCH_NOT_SUPPORTED for providers without it.
type Searcher interface {
	Search(context.Context, string, domain.ReadHints) (domain.ReadResult, error)
}

// ProviderItemReader overlays subscription linkage onto provider catalog
// entries. The retired wide-V2 item/summary readers (plan and operational
// overlays, BGM-keyed custom items, enrichment summaries) are gone; the only
// overlay left is subscription state.
//
// The overlay map returns EVERY existing formal-owner association for an
// external provider entry, in stable order (active first, then creation order).
// It never collapses several owners to one, and the external ref does not create
// or identify any owner.
type ProviderItemReader interface {
	ReadProviderCatalogItems(context.Context, string, []string) (map[string][]domain.InstanceLinkage, []domain.Item, error)
}

type HTTPReader struct {
	Endpoint string
	Client   *http.Client
}

const anibtSeasonContractVersion = "anibt-season-list-v1"

func (r HTTPReader) ReadSeason(ctx context.Context, season string, hints domain.ReadHints) (domain.ReadResult, error) {
	if strings.TrimSpace(r.Endpoint) == "" || strings.TrimSpace(season) == "" {
		return domain.ReadResult{}, errors.New("AniBT catalog endpoint and season are required")
	}
	client := r.Client
	if client == nil {
		// Direct-by-default: the nil-client transport still honors the
		// container's HTTP_PROXY/HTTPS_PROXY/NO_PROXY egress proxy
		// (hardeningtransport.DirectTransport), so a deployment with a
		// proxy gets a working egress path. An explicit client from an
		// instance's saved network route takes precedence (its own Proxy
		// wins), identical for AniBT/Mikan/BGM/TMDB.
		client = &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	endpoint, err := url.Parse(strings.TrimRight(r.Endpoint, "/") + "/api/seasons/anime")
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("parse AniBT catalog endpoint: %w", err)
	}
	query := endpoint.Query()
	query.Set("season", season)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("create AniBT catalog request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	if strings.TrimSpace(hints.ETag) != "" {
		request.Header.Set("If-None-Match", hints.ETag)
	}
	if strings.TrimSpace(hints.LastModified) != "" {
		request.Header.Set("If-Modified-Since", hints.LastModified)
	}
	response, err := client.Do(request)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("fetch AniBT catalog: %w", err)
	}
	defer response.Body.Close()
	etag := response.Header.Get("ETag")
	lastModified := response.Header.Get("Last-Modified")
	if response.StatusCode == http.StatusNotModified {
		return domain.ReadResult{ETag: etag, LastModified: lastModified, Cursor: hints.Cursor, ContractVersion: anibtSeasonContractVersion, NotModified: true}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.ReadResult{}, fmt.Errorf("AniBT catalog returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		OK   bool `json:"ok"`
		Data struct {
			RequestedSeason string `json:"requestedSeason"`
			Total           int    `json:"total"`
			ByWeekday       []struct {
				Animes []json.RawMessage `json:"animes"`
			} `json:"byWeekday"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return domain.ReadResult{}, fmt.Errorf("decode AniBT catalog: %w", err)
	}
	if !payload.OK {
		return domain.ReadResult{}, errors.New("AniBT catalog response is not marked ok")
	}
	entries := make([]domain.Entry, 0, payload.Data.Total)
	seen := make(map[string]struct{}, payload.Data.Total)
	for _, weekday := range payload.Data.ByWeekday {
		for _, rawItem := range weekday.Animes {
			var item struct {
				BGMID           int64             `json:"bgmId"`
				AnimeID         string            `json:"animeId"`
				Cover           string            `json:"cover"`
				Title           map[string]string `json:"title"`
				Format          string            `json:"format"`
				Kind            string            `json:"kind"`
				Episodes        *int              `json:"episodes"`
				Rating          *float64          `json:"rating"`
				AiringAt        *int64            `json:"airingAt"`
				Weekday         *int              `json:"weekday"`
				WeekdayLabel    string            `json:"weekdayLabel"`
				ScheduleStatus  string            `json:"scheduleStatus"`
				RSSReleaseCount int               `json:"rssReleaseCount"`
				HasRelease      bool              `json:"hasRelease"`
			}
			if err := json.Unmarshal(rawItem, &item); err != nil {
				return domain.ReadResult{}, fmt.Errorf("decode AniBT catalog entry: %w", err)
			}
			bgmID := strconv.FormatInt(item.BGMID, 10)
			title := firstTitle(item.Title)
			if item.BGMID <= 0 || title == "" {
				return domain.ReadResult{}, errors.New("AniBT catalog entry has invalid BGM ID or title")
			}
			if _, exists := seen[bgmID]; exists {
				return domain.ReadResult{}, fmt.Errorf("AniBT catalog contains duplicate BGM ID %s", bgmID)
			}
			seen[bgmID] = struct{}{}
			var sourceHint map[string]any
			if err := json.Unmarshal(rawItem, &sourceHint); err != nil {
				return domain.ReadResult{}, fmt.Errorf("preserve AniBT catalog entry: %w", err)
			}
			if item.ScheduleStatus == "" {
				return domain.ReadResult{}, fmt.Errorf("AniBT catalog entry %s has no schedule status", bgmID)
			}
			entries = append(entries, domain.Entry{BGMID: bgmID, Title: title, OriginalTitle: strings.TrimSpace(item.Title["japanese"]), SourceHint: sourceHint, State: domain.EntryPresent})
		}
	}
	requestedSeason := strings.TrimSpace(payload.Data.RequestedSeason)
	if requestedSeason == "" {
		requestedSeason = season
	}
	return domain.ReadResult{Entries: entries, SourceVersion: requestedSeason, Cursor: requestedSeason, ETag: etag, LastModified: lastModified, ContractVersion: anibtSeasonContractVersion}, nil
}

// SearchAniBT queries the public AniBT BGM search endpoint and maps results
// onto the shared catalog entry shape. Side-effect free: no snapshot row, no
// WorkItem, no product write. Rate limits and ETag caching are handled by the
// endpoint and the shared SSRF-safe client.
//
// Every failure that originates at or behind the provider upstream (connection,
// non-2xx status, unreadable payload) wraps ErrProviderUpstream so the
// transport can surface a structured provider error instead of a generic 500
// INTERNAL.
func (r HTTPReader) Search(ctx context.Context, query string, hints domain.ReadHints) (domain.ReadResult, error) {
	query = strings.TrimSpace(query)
	if strings.TrimSpace(r.Endpoint) == "" || query == "" {
		return domain.ReadResult{}, fmt.Errorf("%w: AniBT catalog endpoint and search query are required", ErrProviderUpstream)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	endpoint, err := url.Parse(strings.TrimRight(r.Endpoint, "/") + "/api/bgm/search")
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("%w: parse AniBT search endpoint: %w", ErrProviderUpstream, err)
	}
	queryParams := endpoint.Query()
	queryParams.Set("q", query)
	endpoint.RawQuery = queryParams.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("%w: create AniBT search request: %w", ErrProviderUpstream, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", buildinfo.UserAgent("catalog"))
	response, err := client.Do(request)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("%w: fetch AniBT search: %w", ErrProviderUpstream, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.ReadResult{}, fmt.Errorf("%w: AniBT search returned HTTP %d", ErrProviderUpstream, response.StatusCode)
	}
	var payload struct {
		OK   bool `json:"ok"`
		Data []struct {
			BGMID  int64    `json:"bgmId"`
			Name   string   `json:"name"`
			NameCN string   `json:"nameCn"`
			Rating *float64 `json:"rating"`
			URL    string   `json:"url"`
			Image  string   `json:"image"`
			Date   *string  `json:"date"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return domain.ReadResult{}, fmt.Errorf("%w: decode AniBT search: %w", ErrProviderUpstream, err)
	}
	if !payload.OK {
		return domain.ReadResult{}, fmt.Errorf("%w: AniBT search response is not marked ok", ErrProviderUpstream)
	}
	entries := make([]domain.Entry, 0, len(payload.Data))
	for _, item := range payload.Data {
		if item.BGMID <= 0 || item.Name == "" {
			continue
		}
		bgmID := strconv.FormatInt(item.BGMID, 10)
		title := item.Name
		if item.NameCN != "" {
			title = item.NameCN
		}
		entries = append(entries, domain.Entry{
			BGMID: bgmID, Title: title, OriginalTitle: item.Name,
			SourceHint: map[string]any{
				"provider": "anibt", "bangumi_id": bgmID,
				"cover": item.Image, "rating": item.Rating, "date": item.Date,
			},
			State: domain.EntryPresent,
		})
	}
	return domain.ReadResult{Entries: entries, ContractVersion: "anibt-search-v1"}, nil
}

func firstTitle(titles map[string]string) string {
	for _, key := range []string{"primary", "chinese", "japanese", "english", "romaji"} {
		if title := strings.TrimSpace(titles[key]); title != "" {
			return title
		}
	}
	return ""
}

var ErrPreferenceVersionConflict = errors.New("catalog preference version conflict")

// ErrSearchNotSupported reports a provider with no public keyword search
// surface. The transport maps it to a typed 422 SEARCH_NOT_SUPPORTED.
var ErrSearchNotSupported = errors.New("provider does not support keyword search")

// ErrProviderUpstream reports a provider request that failed at or behind the
// upstream endpoint (unreachable upstream, non-2xx status, unreadable or
// rejected payload). It is an outcome of the provider integration, not of the
// caller's input; the transport maps it to a structured 422
// SEARCH_UPSTREAM_FAILED instead of falling through to a generic 500 INTERNAL
// (issue #9).
var ErrProviderUpstream = errors.New("provider upstream request failed")

// ErrSearchQueryInvalid reports a search query that violates the OpenAPI q
// contract (minLength 1, maxLength 256). It is distinct from
// ErrSearchNotSupported, which only describes provider capability.
var ErrSearchQueryInvalid = errors.New("search query is invalid")

type Service struct {
	db                  *sql.DB
	reader              Reader
	providerReaders     map[string]Reader
	items               ProviderItemReader
	now                 func() time.Time
	eventHub            *events.Hub
	groupReaders        map[string]GroupReader
	feedFetcher         ProviderFeedFetcher
	feedSnapshots       FeedSnapshotStore
	providerConnections ProviderConnectionSource
	customSnapshots     *CustomSnapshotApplication
	groupDirectory      GroupDirectory
	groupResolver       GroupResolver
	groupDictionary     GroupDictionaryStore
	seedTorrentCache    SeedTorrentCacheStore
	seedTorrentFetcher  SeedTorrentFetcher
	// seedFlights collapses concurrent expansions of one locator into a single
	// provider request. It is per-Service so two instances (and two tests) never
	// share an in-flight fetch.
	seedFlights singleflight.Group
}

func NewService(db *sql.DB, reader Reader, itemReaders ...ProviderItemReader) *Service {
	readers := map[string]Reader{}
	if reader != nil {
		readers[ProviderAniBT] = reader
	}
	var itemReader ProviderItemReader
	if len(itemReaders) > 0 {
		itemReader = itemReaders[0]
	}
	return NewProviderService(db, readers, itemReader)
}

func NewProviderService(db *sql.DB, readers map[string]Reader, itemReaders ...ProviderItemReader) *Service {
	var itemReader ProviderItemReader
	if len(itemReaders) > 0 {
		itemReader = itemReaders[0]
	}
	copyReaders := make(map[string]Reader, len(readers))
	for provider, reader := range readers {
		if reader != nil {
			copyReaders[strings.TrimSpace(strings.ToLower(provider))] = reader
		}
	}
	return &Service{db: db, reader: copyReaders[ProviderAniBT], providerReaders: copyReaders, items: itemReader, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetProviderReader(provider string, reader Reader) {
	if s == nil || reader == nil {
		return
	}
	if s.providerReaders == nil {
		s.providerReaders = make(map[string]Reader)
	}
	s.providerReaders[strings.TrimSpace(strings.ToLower(provider))] = reader
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

func catalogSnapshotHealth(snapshot domain.Snapshot) *domain.SourceHealthSummary {
	state := "degraded"
	if snapshot.HealthState == domain.HealthReady {
		state = "active"
	}
	result := &domain.SourceHealthSummary{State: state}
	if snapshot.Status != domain.StatusOutage && !snapshot.ObservedAt.IsZero() {
		observedAt := snapshot.ObservedAt
		result.LastSuccessAt = &observedAt
	}
	return result
}

func subscriptionActions(state string) []string {
	switch state {
	case "active":
		return []string{"open_workbench", "stop"}
	case "pending", "stopped":
		// start is the first start of a pending subscription and the resume
		// of a stopped one.
		return []string{"open_workbench", "start"}
	default:
		return []string{"open_workbench"}
	}
}
