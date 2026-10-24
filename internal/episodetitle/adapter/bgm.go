package adapter

import (
	"context"
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
	endpoint, client, err := c.prepare(ctx)
	if err != nil {
		return episodetitle.List{}, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return episodetitle.List{}, errors.New("BGM endpoint is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v0/subjects/" + workID + "/episodes"
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
