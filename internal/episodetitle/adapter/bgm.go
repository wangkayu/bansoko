package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/episodetitle"
)

// BGMClient fetches the episode list of one bound Bangumi subject. Like
// TMDBClient it resolves the bgm connection, secret and egress client per
// call, so configuration changes apply without a restart. A Bangumi subject is
// the whole lookup scope: one subject covers one broadcast course and its
// episodes number 1..n, so the cache keys BGM lookups with season 0.
type BGMClient struct {
	Connections    ConnectionSource
	Secrets        SecretResolver
	ClientResolver HTTPClientResolver
	Client         *http.Client
}

var _ episodetitle.Client = BGMClient{}

// prepare resolves the BGM connection and its egress client.
func (c BGMClient) prepare(ctx context.Context) (string, *http.Client, error) {
	if c.Connections == nil {
		return "", nil, providerError(episodetitle.SourceBGM, episodetitle.LookupUnconfigured, errors.New("BGM connection source is not configured"))
	}
	connections, err := c.Connections.ListConnections(ctx)
	if err != nil {
		return "", nil, configurationError(ctx, fmt.Errorf("list BGM connections: %w", err))
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionBGM)
	if err != nil {
		return "", nil, connectionError(ctx, episodetitle.SourceBGM, err)
	}
	endpoint := strings.TrimSpace(connection.Endpoint)
	if endpoint == "" {
		endpoint = defaultBGMEndpoint
	}
	client, err := resolveClient(ctx, c.Client, c.ClientResolver, connection)
	if err != nil {
		return "", nil, configurationError(ctx, fmt.Errorf("resolve BGM network route: %w", err))
	}
	return endpoint, client, nil
}

// defaultBGMEndpoint is the public Bangumi API endpoint, matching
// configurationdomain.ConnectionBGM default and the metadata matcher default.
const defaultBGMEndpoint = "https://api.bgm.tv"

// Episodes implements episodetitle.Client. The subject's episode list is the
// whole answer; HTTP 404 (subject gone) maps to episodetitle.ErrNotFound, and
// every other failure is transient. Episode titles prefer the provider's
// Chinese name (name_cn) and fall back to the native name (name), the same
// preference bgm.title_cn / bgm.title read in.
func (c BGMClient) Episodes(ctx context.Context, key episodetitle.Key) (episodetitle.List, error) {
	workID := strings.TrimSpace(key.WorkID)
	if !isDigits(workID) {
		return episodetitle.List{}, errors.New("BGM episode lookup requires a numeric subject id")
	}
	parsed, client, err := c.subjectEpisodesEndpoint(ctx, workID)
	if err != nil {
		return episodetitle.List{}, err
	}
	query := parsed.Query()
	query.Set("type", strconv.Itoa(bgmEpisodeTypeMain))
	parsed.RawQuery = query.Encode()
	items, err := fetchBGMMainPages(ctx, client, parsed)
	if err != nil {
		return episodetitle.List{}, err
	}
	episodes := make([]episodetitle.Episode, 0, len(items))
	for _, item := range items {
		if item.Type != bgmEpisodeTypeMain {
			// The type query already asks for main episodes; a proxy or a
			// provider change that ignores it must not slip specials in.
			continue
		}
		number, ok := bgmEpisodeNumber(item.Ep)
		if !ok || number == 0 {
			// OP/ED entries, fractional episodes and special tags (非正片 or
			// decimal ep numbers like 1.5) are not tracked regular episodes.
			continue
		}
		episodes = append(episodes, episodetitle.Episode{
			Number: number, Title: item.title(), OriginalTitle: strings.TrimSpace(item.Name),
			AirDate: strings.TrimSpace(item.Airdate), Duration: item.Duration,
		})
	}
	return episodetitle.List{Episodes: episodes}, nil
}

// fetchBGMMainPages returns a complete list or an error. Even a 404 after
// the first page cannot establish absence for episodes we have not seen.
func fetchBGMMainPages(ctx context.Context, client *http.Client, endpoint *url.URL) ([]bgmEpisode, error) {
	invalid := func(message string) error {
		return providerError(episodetitle.SourceBGM, episodetitle.LookupInvalidResponse, errors.New(message))
	}
	var items []bgmEpisode
	total := -1
	for offset := 0; offset < bgmEpisodeMaxCount; offset += bgmEpisodePageSize {
		page := *endpoint
		query := page.Query()
		query.Set("limit", strconv.Itoa(bgmEpisodePageSize))
		query.Set("offset", strconv.Itoa(offset))
		page.RawQuery = query.Encode()
		var payload struct {
			Data  []bgmEpisode `json:"data"`
			Total *int         `json:"total"`
		}
		if err := getJSON(ctx, episodetitle.SourceBGM, client, &page, "", &payload); err != nil {
			if offset > 0 && errors.Is(err, episodetitle.ErrNotFound) {
				return nil, invalid("BGM episode page disappeared during pagination")
			}
			return nil, fmt.Errorf("request BGM subject episodes: %w", err)
		}
		if payload.Data == nil || len(payload.Data) > bgmEpisodePageSize {
			return nil, invalid("BGM episode page has invalid data")
		}
		if payload.Total != nil {
			if *payload.Total < 0 || *payload.Total > bgmEpisodeMaxCount || (total >= 0 && total != *payload.Total) {
				return nil, invalid("BGM episode page total is inconsistent or exceeds the limit")
			}
			total = *payload.Total
		}
		items = append(items, payload.Data...)
		if total >= 0 {
			if len(items) == total {
				return items, nil
			}
			if len(items) > total || len(payload.Data) < bgmEpisodePageSize {
				return nil, invalid("BGM episode list is incomplete")
			}
		} else if len(payload.Data) < bgmEpisodePageSize {
			return items, nil
		}
	}
	return nil, invalid("BGM episode list reached the pagination limit")
}

// subjectEpisodesEndpoint resolves the episode list URL of one subject on the
// configured bgm connection together with its egress client. Bangumi v0 lists
// episodes at /v0/episodes?subject_id=...; it has no per-subject episodes path.
func (c BGMClient) subjectEpisodesEndpoint(ctx context.Context, workID string) (*url.URL, *http.Client, error) {
	endpoint, client, err := c.prepare(ctx)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, nil, errors.New("BGM endpoint is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v0/episodes"
	query := parsed.Query()
	query.Set("subject_id", workID)
	parsed.RawQuery = query.Encode()
	return parsed, client, nil
}

var _ episodetitle.SectionedClient = BGMClient{}

const (
	// A subject's whole episode list is paged at 100 entries. The page cap
	// bounds a long-running subject; candidate browsing never needs more.
	bgmEpisodePageSize = 100
	bgmEpisodeMaxCount = 2000
	// Bangumi episode item types. Only the two tracked sections are listed;
	// OP/ED, trailers, ads and MADs stay out of the candidate reference.
	bgmEpisodeTypeMain    = 0
	bgmEpisodeTypeSpecial = 1
)

// bgmEpisode is one item of the v0 subject episode payload. ep has been
// observed as both a JSON number and a string, so it stays raw here.
type bgmEpisode struct {
	Type     int             `json:"type"`
	Name     string          `json:"name"`
	NameCN   string          `json:"name_cn"`
	Airdate  string          `json:"airdate"`
	Duration string          `json:"duration"`
	Ep       json.RawMessage `json:"ep"`
}

func (item bgmEpisode) title() string {
	if title := strings.TrimSpace(item.NameCN); title != "" {
		return title
	}
	return strings.TrimSpace(item.Name)
}

// SectionedEpisodes implements episodetitle.SectionedClient. The whole subject
// is one list: main episodes (type 0) and specials (type 1), each keeping the
// provider's order. An item whose ep is not a whole number falls back to its
// ordinal within the section, so a recap numbered 12.5 still has a distinct
// row. HTTP 404 maps to episodetitle.ErrNotFound.
func (c BGMClient) SectionedEpisodes(ctx context.Context, key episodetitle.Key) (episodetitle.SectionedList, error) {
	workID := strings.TrimSpace(key.WorkID)
	if !isDigits(workID) {
		return episodetitle.SectionedList{}, errors.New("BGM episode lookup requires a numeric subject id")
	}
	parsed, client, err := c.subjectEpisodesEndpoint(ctx, workID)
	if err != nil {
		return episodetitle.SectionedList{}, err
	}
	episodes := make([]episodetitle.SectionedEpisode, 0)
	ordinals := map[episodetitle.Section]int{}
	for offset := 0; offset < bgmEpisodeMaxCount; offset += bgmEpisodePageSize {
		page := *parsed
		query := page.Query()
		query.Set("limit", strconv.Itoa(bgmEpisodePageSize))
		query.Set("offset", strconv.Itoa(offset))
		page.RawQuery = query.Encode()
		var payload struct {
			Data  []bgmEpisode `json:"data"`
			Total int          `json:"total"`
		}
		if err := getJSON(ctx, episodetitle.SourceBGM, client, &page, "", &payload); err != nil {
			return episodetitle.SectionedList{}, fmt.Errorf("request BGM subject episodes: %w", err)
		}
		for _, item := range payload.Data {
			section, tracked := bgmEpisodeSection(item.Type)
			if !tracked {
				continue
			}
			ordinals[section]++
			number, ok := bgmEpisodeNumber(item.Ep)
			if !ok || number == 0 {
				number = ordinals[section]
			}
			episodes = append(episodes, episodetitle.SectionedEpisode{
				Number: number, Title: item.title(), OriginalTitle: strings.TrimSpace(item.Name),
				AirDate: strings.TrimSpace(item.Airdate), Duration: item.Duration, Section: section,
			})
		}
		if len(payload.Data) < bgmEpisodePageSize || (payload.Total > 0 && offset+bgmEpisodePageSize >= payload.Total) {
			break
		}
	}
	return episodetitle.SectionedList{Episodes: episodes}, nil
}

// bgmEpisodeSection maps a Bangumi item type to a browsable section.
func bgmEpisodeSection(kind int) (episodetitle.Section, bool) {
	switch kind {
	case bgmEpisodeTypeMain:
		return episodetitle.SectionMain, true
	case bgmEpisodeTypeSpecial:
		return episodetitle.SectionSpecial, true
	default:
		return "", false
	}
}

// bgmEpisodeNumber reads a whole non-negative ep from either JSON form.
func bgmEpisodeNumber(raw json.RawMessage) (int, bool) {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	number := int(value)
	if float64(number) != value || number < 0 {
		return 0, false
	}
	return number, true
}

var _ episodetitle.SeasonClient = BGMClient{}

// Seasons implements episodetitle.SeasonClient. A Bangumi subject has exactly
// one implicit season; other sources of the same show are separate subject
// ids, so the answer is one entry and no request is made.
func (c BGMClient) Seasons(ctx context.Context, workID string) ([]episodetitle.Season, error) {
	workID = strings.TrimSpace(workID)
	if !isDigits(workID) {
		return nil, errors.New("BGM season lookup requires a numeric subject id")
	}
	return []episodetitle.Season{{Number: 0}}, nil
}
