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

// prepare resolves the bgm connection and its egress client. A missing,
// ambiguous or unresolvable bgm connection is not swallowed: a template that
// reads the episode title asked for this query, so the failure surfaces as a
// transient lookup failure exactly like a network error.
func (c BGMClient) prepare(ctx context.Context) (string, *http.Client, error) {
	if c.Connections == nil {
		return "", nil, fmt.Errorf("BGM connection source is not configured: %w", episodetitle.ErrTransient)
	}
	connections, err := c.Connections.ListConnections(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("list BGM connections: %w", err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionBGM)
	if err != nil {
		return "", nil, fmt.Errorf("no enabled BGM connection: %w", episodetitle.ErrTransient)
	}
	endpoint := strings.TrimSpace(connection.Endpoint)
	if endpoint == "" {
		endpoint = defaultBGMEndpoint
	}
	client, err := resolveClient(ctx, c.Client, c.ClientResolver, connection)
	if err != nil {
		return "", nil, fmt.Errorf("resolve BGM network route: %w", err)
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
	var payload struct {
		Data []struct {
			Name    string `json:"name"`
			NameCN  string `json:"name_cn"`
			Airdate string `json:"airdate"`
			Ep      string `json:"ep"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, parsed, "", &payload); err != nil {
		return episodetitle.List{}, fmt.Errorf("request BGM subject episodes: %w", err)
	}
	episodes := make([]episodetitle.Episode, 0, len(payload.Data))
	for _, item := range payload.Data {
		number, parseErr := strconv.ParseInt(strings.TrimSpace(item.Ep), 10, 64)
		if parseErr != nil || number < 0 {
			// OP/ED entries, fractional episodes and special tags (非正片 or
			// decimal ep numbers like 1.5) are not tracked regular episodes.
			continue
		}
		title := strings.TrimSpace(item.NameCN)
		if title == "" {
			title = strings.TrimSpace(item.Name)
		}
		episodes = append(episodes, episodetitle.Episode{Number: int(number), Title: title, AirDate: strings.TrimSpace(item.Airdate)})
	}
	return episodetitle.List{Episodes: episodes}, nil
}

// subjectEpisodesEndpoint resolves the episodes URL of one subject on the
// configured bgm connection together with its egress client.
func (c BGMClient) subjectEpisodesEndpoint(ctx context.Context, workID string) (*url.URL, *http.Client, error) {
	endpoint, client, err := c.prepare(ctx)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, nil, errors.New("BGM endpoint is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v0/subjects/" + workID + "/episodes"
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

// bgmSectionedEpisode is one item of the v0 subject episode payload. ep has
// been observed as both a JSON number and a string, so it stays raw here.
type bgmSectionedEpisode struct {
	Type    int             `json:"type"`
	Name    string          `json:"name"`
	NameCN  string          `json:"name_cn"`
	Airdate string          `json:"airdate"`
	Ep      json.RawMessage `json:"ep"`
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
			Data  []bgmSectionedEpisode `json:"data"`
			Total int                   `json:"total"`
		}
		if err := getJSON(ctx, client, &page, "", &payload); err != nil {
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
			title := strings.TrimSpace(item.NameCN)
			if title == "" {
				title = strings.TrimSpace(item.Name)
			}
			episodes = append(episodes, episodetitle.SectionedEpisode{
				Number: number, Title: title, AirDate: strings.TrimSpace(item.Airdate), Section: section,
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
