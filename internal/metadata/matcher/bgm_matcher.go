package matcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	"bansoko/internal/runtime/buildinfo"
	"golang.org/x/text/unicode/norm"
)

// specialCharPattern matches punctuation and whitespace for title
// normalization.
var specialCharPattern = regexp.MustCompile(
	`[!#$%&'()*+,/:;=?@\[\]^` + "`" + `{|}\\<>"\\s\-\x{3001}\x{3002}\x{FF01}\x{FF1F}\x{FF0C}]`)
var underscoreRunPattern = regexp.MustCompile(`_+`)

// BGMSubject holds the subset of subject detail used by the matcher.
type BGMSubject struct {
	ID       int               `json:"id"`
	Name     string            `json:"name"`
	NameCN   string            `json:"name_cn"`
	AirDate  string            `json:"date"`
	Platform string            `json:"platform"`
	Infobox  []bgmInfoboxField `json:"infobox"`
	Eps      *int              `json:"eps"` // total episodes (null for movies/open-ended)
	Images   struct {
		Common string `json:"common"`
	} `json:"images"`
}

// BGMMatchResult holds a successful BGM match.
type BGMMatchResult struct {
	BgmID         string
	Subject       *BGMSubject
	Confidence    float64
	Method        string
	trusted       bool
	airing        dateClue
	TotalEpisodes *int // from subject.Eps; sole source of total_episodes for auto-backfill
	// Year is the subject's first-air year parsed from its provider date, nil
	// when the date is absent or not a plausible release year.
	Year *int
}

// BGMMatcher scores Bangumi v0 search results before loading details. When Connections is
// set, the single enabled bgm connection supplies the endpoint and, through
// ClientResolver, the egress route client on each call so configuration
// changes apply without a restart. With no usable bgm connection the matcher
// keeps its default behavior: the public API endpoint and a direct client.
type BGMMatcher struct {
	Client         *http.Client
	Endpoint       string // default "https://api.bgm.tv"
	Connections    ConnectionSource
	ClientResolver HTTPClientResolver
}

func (m *BGMMatcher) endpoint() string {
	if m.Endpoint != "" {
		return m.Endpoint
	}
	return "https://api.bgm.tv"
}

func (m *BGMMatcher) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
}

// resolve returns the endpoint and client for one call. A missing, ambiguous
// or unresolvable bgm connection is not an error: BGM is the middle layer of
// the matcher pipeline and must stay available with its default direct
// behavior.
func (m *BGMMatcher) resolve(ctx context.Context) (string, *http.Client) {
	endpoint, client := m.endpoint(), m.client()
	if m.Connections == nil {
		return endpoint, client
	}
	connections, err := m.Connections.ListConnections(ctx)
	if err != nil {
		return endpoint, client
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionBGM)
	if err != nil {
		return endpoint, client
	}
	if strings.TrimSpace(connection.Endpoint) != "" {
		endpoint = strings.TrimSpace(connection.Endpoint)
	}
	if m.ClientResolver != nil {
		if resolved, resolveErr := m.ClientResolver.ClientForConnection(ctx, connection); resolveErr == nil && resolved != nil {
			client = resolved
		}
	}
	return endpoint, client
}

// MatchByID fetches the actual subject facts for an already resolved ID.
func (m *BGMMatcher) MatchByID(ctx context.Context, bgmID string) (*BGMMatchResult, error) {
	id, err := strconv.Atoi(strings.TrimSpace(bgmID))
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid bangumi subject id")
	}
	endpoint, client := m.resolve(ctx)
	subject, err := m.getSubjectDetail(ctx, endpoint, client, id)
	if err != nil {
		return nil, fmt.Errorf("bgm subject detail %d: %w", id, err)
	}
	return newBGMMatch(id, subject, 1, "id", true), nil
}

func newBGMMatch(id int, subject *BGMSubject, confidence float64, method string, trusted bool) *BGMMatchResult {
	airing := subject.airingDate()
	return &BGMMatchResult{BgmID: strconv.Itoa(id), Subject: subject, Confidence: confidence, Method: method,
		TotalEpisodes: subject.Eps, Year: identitymetadatadomain.NormalizeYear(&airing.year), trusted: trusted, airing: airing}
}

// Match resolves at most one subject, trying the original title first.
func (m *BGMMatcher) Match(ctx context.Context, title string) (*BGMMatchResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}
	endpoint, client := m.resolve(ctx)
	items, err := m.searchAPI(ctx, endpoint, client, title)
	if err != nil {
		return nil, fmt.Errorf("bgm subject search: %w", err)
	}
	best, score, ambiguous := bestBGMTitleMatch(items, title, 0)
	var fallbackYear int
	if best == nil {
		base, year := splitTrailingTitleYear(title)
		if year == 0 {
			return nil, nil
		}
		fallbackYear = year
		best, score, ambiguous = bestBGMTitleMatch(items, base, year)
		if best == nil {
			items, err = m.searchAPI(ctx, endpoint, client, base)
			if err != nil {
				return nil, fmt.Errorf("bgm subject fallback search: %w", err)
			}
			best, score, ambiguous = bestBGMTitleMatch(items, base, year)
		}
	}
	if best == nil || ambiguous {
		return nil, nil
	}
	subject, err := m.getSubjectDetail(ctx, endpoint, client, best.ID)
	if err != nil {
		return nil, fmt.Errorf("bgm subject detail %d: %w", best.ID, err)
	}
	if subject.Infobox == nil {
		subject.Infobox = best.Infobox
	}
	if subject.AirDate == "" {
		subject.AirDate = best.AirDate
	}
	if subject.Platform == "" {
		subject.Platform = best.Platform
	}
	method := "normalized"
	if score == 1 {
		method = "direct"
	}
	result := newBGMMatch(best.ID, subject, score, method, true)
	// Detail can contradict the year reported by the search response.
	if fallbackYear != 0 && (result.airing.year != fallbackYear || result.airing.conflict) {
		return nil, nil
	}
	return result, nil
}

func bestBGMTitleMatch(items []BGMSubject, title string, year int) (*BGMSubject, float64, bool) {
	key, normalized := bgmTitleKey(title), normalizeTitle(title)
	var best *BGMSubject
	var score float64
	ambiguous := false
	for i := range items {
		item := &items[i]
		if item.ID <= 0 {
			continue
		}
		confidence := 0.95
		if item.matchesTitle(key) {
			confidence = 1
		} else if normalized == "" || !item.matchesNormalizedTitle(normalized) {
			continue
		}
		if year != 0 {
			airing := item.airingDate()
			if airing.year != year || airing.conflict {
				continue
			}
		}
		switch {
		case confidence > score:
			best, score, ambiguous = item, confidence, false
		case confidence == score && best.ID != item.ID:
			ambiguous = true
		}
	}
	return best, score, ambiguous
}

// providerYearFromDate parses the first air year of a provider date such as
// "2024-03-01". A date the provider did not give, or a year outside the
// supported range, is unknown (nil); nothing is inferred.
func providerYearFromDate(date string) *int {
	return identitymetadatadomain.NormalizeYear(extractYearFromDate(strings.TrimSpace(date)))
}

func (m *BGMMatcher) searchAPI(ctx context.Context, base string, client *http.Client, title string) ([]BGMSubject, error) {
	endpoint, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse bgm endpoint: %w", err)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v0/search/subjects"
	q := endpoint.Query()
	q.Set("limit", "25")
	endpoint.RawQuery = q.Encode()

	body, err := json.Marshal(map[string]any{"keyword": title, "filter": map[string][]int{"type": {2}}})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []BGMSubject `json:"data"`
	}
	if err := m.requestJSON(ctx, client, http.MethodPost, endpoint.String(), bytes.NewReader(body), &payload); err != nil {
		return nil, err
	}
	return payload.Data, nil
}

func (m *BGMMatcher) getSubjectDetail(ctx context.Context, base string, client *http.Client, bgmID int) (*BGMSubject, error) {
	endpoint, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse bgm endpoint: %w", err)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v0/subjects/" + strconv.Itoa(bgmID)

	var subject BGMSubject
	if err := m.requestJSON(ctx, client, http.MethodGet, endpoint.String(), nil, &subject); err != nil {
		return nil, err
	}
	return &subject, nil
}

func (m *BGMMatcher) requestJSON(ctx context.Context, client *http.Client, method, rawURL string, body io.Reader, target any) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", buildinfo.UserAgent("metadata-matcher"))
	resp, err := client.Do(req)
	if err != nil {
		return requestFailure(client, req, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusFailure(fmt.Errorf("bgm API returned HTTP %d", resp.StatusCode), client, req, resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target)
}

// --- Confidence calculation ---

func normalizeTitle(title string) string {
	title = norm.NFKC.String(title)
	title = strings.ToLower(title)
	normalized := specialCharPattern.ReplaceAllString(title, "_")
	normalized = underscoreRunPattern.ReplaceAllString(normalized, "_")
	return strings.Trim(normalized, "_")
}

func levenshteinSimilarity(s1, s2 string) float64 {
	if s1 == s2 {
		return 1.0
	}
	if len(s1) == 0 || len(s2) == 0 {
		return 0.0
	}
	distance := levenshteinDistance(s1, s2)
	maxLen := len([]rune(s1))
	if r := len([]rune(s2)); r > maxLen {
		maxLen = r
	}
	return 1.0 - float64(distance)/float64(maxLen)
}

func levenshteinDistance(s1, s2 string) int {
	r1 := []rune(s1)
	r2 := []rune(s2)
	rows := len(r1) + 1
	cols := len(r2) + 1
	dist := make([][]int, rows)
	for i := range dist {
		dist[i] = make([]int, cols)
	}
	for i := 1; i < rows; i++ {
		dist[i][0] = i
	}
	for j := 1; j < cols; j++ {
		dist[0][j] = j
	}
	for i := 1; i < rows; i++ {
		for j := 1; j < cols; j++ {
			cost := 0
			if r1[i-1] != r2[j-1] {
				cost = 1
			}
			dist[i][j] = minInt3(
				dist[i-1][j]+1,
				dist[i][j-1]+1,
				dist[i-1][j-1]+cost,
			)
		}
	}
	return dist[rows-1][cols-1]
}

func minInt3(a, b, c int) int {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}
