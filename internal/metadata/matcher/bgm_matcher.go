package matcher

import (
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
)

// specialCharPattern matches punctuation and whitespace for title
// normalization.
var specialCharPattern = regexp.MustCompile(
	`[!#$%&'()*+,/:;=?@\[\]^` + "`" + `{|}\\<>"\\s\-\x{3001}\x{3002}\x{FF01}\x{FF1F}\x{FF0C}]`)
var underscoreRunPattern = regexp.MustCompile(`_+`)

// BGMSubject holds the subset of subject detail used by the matcher.
type BGMSubject struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	NameCN  string `json:"name_cn"`
	AirDate string `json:"date"`
	Eps     *int   `json:"eps"` // total episodes (null for movies/open-ended)
	Images  struct {
		Common string `json:"common"`
	} `json:"images"`
}

// BGMMatchResult holds a successful BGM match.
type BGMMatchResult struct {
	BgmID         string
	Subject       *BGMSubject
	Confidence    float64
	Method        string
	TotalEpisodes *int // from subject.Eps; sole source of total_episodes for auto-backfill
	// Year is the subject's first-air year parsed from its provider date, nil
	// when the date is absent or not a plausible release year.
	Year *int
}

// BGMMatcher searches the Bangumi API by title using a three-tier
// strategy: direct -> normalized -> fuzzy Levenshtein. When Connections is
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
	return &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()}
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

// Match searches Bangumi by title. Returns nil (no error) when no result
// meets the confidence threshold.
func (m *BGMMatcher) Match(ctx context.Context, title string) (*BGMMatchResult, error) {
	if strings.TrimSpace(title) == "" {
		return nil, nil
	}
	endpoint, client := m.resolve(ctx)

	// 1. Direct search.
	result, err := m.tryDirectMatch(ctx, endpoint, client, title)
	if err != nil {
		return nil, fmt.Errorf("bgm direct match: %w", err)
	}
	if result != nil {
		result.Method = "direct"
		return result, nil
	}

	// 2. Normalized title re-search.
	normalized := normalizeTitle(title)
	if normalized != title {
		result, err = m.tryDirectMatch(ctx, endpoint, client, normalized)
		if err != nil {
			return nil, fmt.Errorf("bgm normalized match: %w", err)
		}
		if result != nil {
			result.Method = "normalized"
			return result, nil
		}
	}

	// 3. Fuzzy Levenshtein match.
	result, err = m.tryFuzzyMatch(ctx, endpoint, client, title, normalized)
	if err != nil {
		return nil, fmt.Errorf("bgm fuzzy match: %w", err)
	}
	if result != nil {
		result.Method = "fuzzy"
		return result, nil
	}

	return nil, nil
}

// tryDirectMatch searches the API and returns the first result with
// confidence >= 0.95.
func (m *BGMMatcher) tryDirectMatch(ctx context.Context, endpoint string, client *http.Client, title string) (*BGMMatchResult, error) {
	results, err := m.searchAPI(ctx, endpoint, client, title)
	if err != nil {
		return nil, err
	}
	for _, item := range results {
		confidence := bgmConfidence(title, item.Name, item.NameCN)
		if confidence >= 0.95 {
			subject, err := m.getSubjectDetail(ctx, endpoint, client, item.ID)
			if err != nil {
				return nil, fmt.Errorf("bgm subject detail %d: %w", item.ID, err)
			}
			return &BGMMatchResult{
				BgmID:         strconv.Itoa(item.ID),
				Subject:       subject,
				Confidence:    confidence,
				TotalEpisodes: subject.Eps,
				Year:          providerYearFromDate(subject.AirDate),
			}, nil
		}
	}
	return nil, nil
}

// tryFuzzyMatch searches the API and picks the best result with
// confidence >= 0.56 (0.7 similarity threshold * 0.8 penalty).
func (m *BGMMatcher) tryFuzzyMatch(ctx context.Context, endpoint string, client *http.Client, original, normalized string) (*BGMMatchResult, error) {
	results, err := m.searchAPI(ctx, endpoint, client, original)
	if err != nil {
		return nil, err
	}
	var bestResult *bgmSearchItem
	var bestSubject *BGMSubject
	var bestConf float64
	for _, item := range results {
		conf := bgmFuzzyConfidence(original, normalized, item.Name, item.NameCN)
		if conf > bestConf && conf >= 0.56 {
			subject, err := m.getSubjectDetail(ctx, endpoint, client, item.ID)
			if err != nil {
				return nil, fmt.Errorf("bgm subject detail %d: %w", item.ID, err)
			}
			bestResult = &item
			bestSubject = subject
			bestConf = conf
		}
	}
	if bestResult != nil {
		return &BGMMatchResult{
			BgmID:         strconv.Itoa(bestResult.ID),
			Subject:       bestSubject,
			Confidence:    bestConf,
			TotalEpisodes: bestSubject.Eps,
			Year:          providerYearFromDate(bestSubject.AirDate),
		}, nil
	}
	return nil, nil
}

type bgmSearchItem struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	NameCN  string `json:"name_cn"`
	AirDate string `json:"air_date"`
}

// providerYearFromDate parses the first air year of a provider date such as
// "2024-03-01". A date the provider did not give, or a year outside the
// supported range, is unknown (nil); nothing is inferred.
func providerYearFromDate(date string) *int {
	return identitymetadatadomain.NormalizeYear(extractYearFromDate(strings.TrimSpace(date)))
}

func (m *BGMMatcher) searchAPI(ctx context.Context, base string, client *http.Client, title string) ([]bgmSearchItem, error) {
	endpoint, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse bgm endpoint: %w", err)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/search/subject/" + url.PathEscape(title)
	q := endpoint.Query()
	q.Set("type", "2")
	q.Set("responseGroup", "small")
	endpoint.RawQuery = q.Encode()

	var payload struct {
		Count int             `json:"results"`
		List  []bgmSearchItem `json:"list"`
	}
	if err := m.getJSON(ctx, client, endpoint.String(), &payload); err != nil {
		return nil, err
	}
	return payload.List, nil
}

func (m *BGMMatcher) getSubjectDetail(ctx context.Context, base string, client *http.Client, bgmID int) (*BGMSubject, error) {
	endpoint, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse bgm endpoint: %w", err)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v0/subjects/" + strconv.Itoa(bgmID)

	var subject BGMSubject
	if err := m.getJSON(ctx, client, endpoint.String(), &subject); err != nil {
		return nil, err
	}
	return &subject, nil
}

func (m *BGMMatcher) getJSON(ctx context.Context, client *http.Client, rawURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
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
	normalized := specialCharPattern.ReplaceAllString(title, "_")
	normalized = underscoreRunPattern.ReplaceAllString(normalized, "_")
	return strings.Trim(normalized, "_")
}

func bgmConfidence(searchTitle, bgmName, bgmNameCN string) float64 {
	searchTitle = strings.TrimSpace(strings.ToLower(searchTitle))
	bgmName = strings.TrimSpace(strings.ToLower(bgmName))
	bgmNameCN = strings.TrimSpace(strings.ToLower(bgmNameCN))
	if searchTitle == "" {
		return 0.0
	}

	if (bgmName != "" && searchTitle == bgmName) || (bgmNameCN != "" && searchTitle == bgmNameCN) {
		return 1.0
	}
	normalizedSearch := normalizeTitle(searchTitle)
	normalizedBGM := normalizeTitle(bgmName)
	normalizedBGMCN := normalizeTitle(bgmNameCN)
	if (bgmName != "" && normalizedSearch == normalizedBGM) ||
		(bgmNameCN != "" && normalizedSearch == normalizedBGMCN) {
		return 0.95
	}
	if (bgmName != "" && strings.Contains(bgmName, searchTitle)) ||
		(bgmNameCN != "" && strings.Contains(bgmNameCN, searchTitle)) {
		return 0.8
	}
	if (bgmName != "" && strings.Contains(searchTitle, bgmName)) ||
		(bgmNameCN != "" && strings.Contains(searchTitle, bgmNameCN)) {
		return 0.75
	}
	return 0.0
}

func bgmFuzzyConfidence(original, normalized, bgmName, bgmNameCN string) float64 {
	if strings.TrimSpace(original) == "" {
		return 0.0
	}
	if c := bgmConfidence(original, bgmName, bgmNameCN); c > 0 {
		return c
	}
	if c := bgmConfidence(normalized, bgmName, bgmNameCN); c > 0 {
		return c * 0.9
	}
	s1 := levenshteinSimilarity(original, bgmName)
	s2 := levenshteinSimilarity(original, bgmNameCN)
	s3 := levenshteinSimilarity(normalized, normalizeTitle(bgmName))
	s4 := levenshteinSimilarity(normalized, normalizeTitle(bgmNameCN))
	maxSim := s1
	if s2 > maxSim {
		maxSim = s2
	}
	if s3 > maxSim {
		maxSim = s3
	}
	if s4 > maxSim {
		maxSim = s4
	}
	if maxSim >= 0.7 {
		return maxSim * 0.8
	}
	return 0.0
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
