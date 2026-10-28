package matcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/runtime/buildinfo"
)

// ConnectionSource lists configured connections. Satisfied by
// adapter.DBConnectionSource.
type ConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

// SecretResolver decrypts a secret ref into plaintext. Satisfied by
// *configurationapplication.DatabaseSecretStore.
type SecretResolver interface {
	Resolve(string) (string, bool)
}

// HTTPClientResolver resolves the network route attached to a connection.
// Direct routes may return nil, which means use the default client.
type HTTPClientResolver interface {
	ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error)
}

// TMDBMatchResult holds a successful TMDB match.
type TMDBMatchResult struct {
	WorkID     string // TMDB database id (numeric string)
	MediaKind  string // "tv" or "movie"
	Season     int
	Confidence float64
	Method     string
	// TitleCN and OriginalTitle are TMDB's names of the work: TitleCN is
	// the title returned for the request language (both request sites send
	// language=zh-CN), OriginalTitle is the native title.
	TitleCN, OriginalTitle string
	// Overview is the work summary TMDB reported, recorded with the titles
	// so acceptance freezes it (tmdb.summary).
	Overview string
	// Year is the work's first air/release year as TMDB reported it
	// (first_air_date for tv, release_date for movie); nil when the provider
	// gave no usable date.
	Year *int
}

// TMDBMatcher searches TMDB by title, scores candidates, and identifies the
// correct season via a five-rule strategy. It reads the TMDB connection
// endpoint and API key dynamically on each Match call so that config changes
// take effect without restarting the matcher.
type TMDBMatcher struct {
	Connections    ConnectionSource
	Secrets        SecretResolver
	ClientResolver HTTPClientResolver
	Client         *http.Client
	Trimmer        *TitleTrimmer
}

func (m *TMDBMatcher) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()}
}

func (m *TMDBMatcher) trimmer() *TitleTrimmer {
	if m.Trimmer != nil {
		return m.Trimmer
	}
	return NewTitleTrimmer()
}

const tmdbMinConfidence = 0.7
const tmdbYearTolerance = 1

// Match searches TMDB for the given title. Returns nil (no error) when no
// result meets the confidence threshold. Missing or ambiguous configuration is
// returned as a typed config error so automatic dispatch can distinguish it.
func (m *TMDBMatcher) Match(ctx context.Context, title string) (*TMDBMatchResult, error) {
	if strings.TrimSpace(title) == "" {
		return nil, nil
	}
	if m.Connections == nil {
		return nil, &MatchError{Kind: ErrorConfig, Layer: "tmdb", Err: errors.New("TMDB connection source is not configured")}
	}
	connections, err := m.Connections.ListConnections(ctx)
	if err != nil {
		return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("list tmdb connections: %w", err)}
	}
	conn, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionTMDB)
	if err != nil {
		return nil, &MatchError{Kind: ErrorConfig, Layer: "tmdb", Err: err}
	}
	if conn.SecretRef == "" || m.Secrets == nil {
		return nil, &MatchError{Kind: ErrorConfig, Layer: "tmdb", Err: errors.New("TMDB API key is not configured")}
	}
	apiKey, ok := m.Secrets.Resolve(conn.SecretRef)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return nil, &MatchError{Kind: ErrorConfig, Layer: "tmdb", Err: errors.New("TMDB API key is unavailable")}
	}
	client := m.Client
	if m.ClientResolver != nil {
		resolved, resolveErr := m.ClientResolver.ClientForConnection(ctx, conn)
		if resolveErr != nil {
			return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("resolve tmdb network route: %w", resolveErr)}
		}
		if resolved != nil {
			client = resolved
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	endpoint := stripConfigurationSuffix(conn.Endpoint)

	// 1. Try TV search first.
	result, err := m.searchAndScore(ctx, client, endpoint, apiKey, title, "tv")
	if err != nil {
		return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("tmdb tv search: %w", err)}
	}
	if result != nil && result.Confidence >= tmdbMinConfidence {
		result.Method = "tv"
		return result, nil
	}

	// 2. Fallback to movie search.
	movieResult, err := m.searchAndScore(ctx, client, endpoint, apiKey, title, "movie")
	if err != nil {
		return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("tmdb movie search: %w", err)}
	}
	if movieResult != nil && movieResult.Confidence >= tmdbMinConfidence {
		movieResult.Method = "movie"
		return movieResult, nil
	}

	// Neither media kind met the automatic-match threshold. Leave the owner
	// unresolved so the next schedule cycle or manual candidate selection can
	// decide; a weak guess must never be persisted as an automatic binding.
	return nil, nil
}

func (m *TMDBMatcher) searchAndScore(ctx context.Context, client *http.Client, endpoint, apiKey, query, mediaKind string) (*TMDBMatchResult, error) {
	// Try original title first.
	result, err := m.trySearch(ctx, client, endpoint, apiKey, query, mediaKind, nil)
	if err != nil {
		return nil, err
	}
	if result != nil && result.Confidence >= tmdbMinConfidence {
		return result, nil
	}

	// Try trimmed title.
	trimmed := m.trimmer().TrimTitle(query)
	if trimmed != query {
		r2, err := m.trySearch(ctx, client, endpoint, apiKey, trimmed, mediaKind, nil)
		if err != nil {
			return nil, err
		}
		if r2 != nil && r2.Confidence >= tmdbMinConfidence {
			return r2, nil
		}
		if r2 != nil && (result == nil || r2.Confidence > result.Confidence) {
			result = r2
		}
	}

	// Try extracting year from title for better matching.
	year := m.trimmer().ExtractYear(query)
	if year != nil {
		r3, err := m.trySearch(ctx, client, endpoint, apiKey, trimmed, mediaKind, year)
		if err != nil {
			return nil, err
		}
		if r3 != nil && r3.Confidence >= tmdbMinConfidence {
			return r3, nil
		}
		if r3 != nil && (result == nil || r3.Confidence > result.Confidence) {
			result = r3
		}
	}

	return result, nil
}

type tmdbSearchResult struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	Title            string  `json:"title"`
	OriginalName     string  `json:"original_name"`
	OriginalTitle    string  `json:"original_title"`
	Overview         string  `json:"overview"`
	FirstAirDate     string  `json:"first_air_date"`
	ReleaseDate      string  `json:"release_date"`
	GenreIDs         []int   `json:"genre_ids"`
	Popularity       float64 `json:"popularity"`
	OriginalLanguage string  `json:"original_language"`
}

type tmdbSearchResponse struct {
	Results []tmdbSearchResult `json:"results"`
}

type tmdbSeason struct {
	SeasonNumber int    `json:"season_number"`
	AirDate      string `json:"air_date"`
}

type tmdbDetailsResponse struct {
	Seasons []tmdbSeason `json:"seasons"`
}

func (m *TMDBMatcher) trySearch(ctx context.Context, client *http.Client, endpoint, apiKey, query, mediaKind string, year *int) (*TMDBMatchResult, error) {
	searchURL, err := buildTMDBSearchURL(endpoint, apiKey, query, mediaKind, year)
	if err != nil {
		return nil, err
	}

	var resp tmdbSearchResponse
	if err := m.getJSON(ctx, client, searchURL, apiKey, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, nil
	}

	// Filter to animation genre (genre_id=16) only.
	var animated []tmdbSearchResult
	for _, item := range resp.Results {
		if hasAnimationGenre(item.GenreIDs) {
			animated = append(animated, item)
		}
	}
	if len(animated) == 0 {
		return nil, nil
	}

	var best *tmdbSearchResult
	var bestConf float64
	for i := range animated {
		conf := m.calculateConfidence(query, &animated[i], year)
		if conf > bestConf {
			bestConf = conf
			best = &animated[i]
		}
	}
	if best == nil {
		return nil, nil
	}

	// Fetch details for season identification (TV only). A search result is
	// not a complete TV binding until its details provide a real season; do not
	// silently turn a failed or empty details response into season 1.
	season := 0 // movies have no season; zero is the explicit tuple value.
	if mediaKind == "tv" {
		details, err := m.getDetails(ctx, client, endpoint, apiKey, best.ID)
		if err != nil {
			return nil, fmt.Errorf("get tv details for %d: %w", best.ID, err)
		}
		if details == nil {
			return nil, nil
		}
		identified, ok := identifySeason(details.Seasons, m.trimmer().ExtractSeasonNumber(query))
		if !ok {
			return nil, nil
		}
		season = identified
	}

	titleCN, original := best.Name, best.OriginalName
	releaseDate := best.FirstAirDate
	if mediaKind == "movie" {
		titleCN, original = best.Title, best.OriginalTitle
		releaseDate = best.ReleaseDate
	}
	return &TMDBMatchResult{
		WorkID:        strconv.Itoa(best.ID),
		MediaKind:     mediaKind,
		Season:        season,
		Confidence:    bestConf,
		TitleCN:       strings.TrimSpace(titleCN),
		OriginalTitle: strings.TrimSpace(original),
		Overview:      strings.TrimSpace(best.Overview),
		Year:          providerYearFromDate(releaseDate),
	}, nil
}

func (m *TMDBMatcher) getDetails(ctx context.Context, client *http.Client, endpoint, apiKey string, tmdbID int) (*tmdbDetailsResponse, error) {
	detailsURL, err := buildTMDBDetailsURL(endpoint, apiKey, tmdbID)
	if err != nil {
		return nil, err
	}
	var resp tmdbDetailsResponse
	if err := m.getJSON(ctx, client, detailsURL, apiKey, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (m *TMDBMatcher) getJSON(ctx context.Context, client *http.Client, rawURL, apiKey string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", buildinfo.UserAgent("metadata-matcher"))
	if isBearerToken(apiKey) {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return requestFailure(client, req, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusFailure(fmt.Errorf("tmdb API returned HTTP %d", resp.StatusCode), client, req, resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target)
}

// --- Confidence calculation ---

func (m *TMDBMatcher) calculateConfidence(query string, result *tmdbSearchResult, year *int) float64 {
	var conf float64

	resultTitle := tmdbResultTitle(result)
	resultOriginalTitle := tmdbResultOriginalTitle(result)

	// 1. Title similarity (weight: 0.6)
	titleSim := m.titleSimilarity(query, resultTitle, resultOriginalTitle)
	conf += titleSim * 0.6

	// 2. Year similarity (weight: 0.2)
	conf += yearSimilarity(year, result) * 0.2

	// 3. Popularity (weight: 0.1)
	conf += popularityWeight(result.Popularity) * 0.1

	// 4. Language (weight: 0.1)
	conf += languageWeight(result.OriginalLanguage) * 0.1

	return math.Min(conf, 1.0)
}

func (m *TMDBMatcher) titleSimilarity(query, title, originalTitle string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	title = strings.ToLower(strings.TrimSpace(title))
	originalTitle = strings.ToLower(strings.TrimSpace(originalTitle))

	if query == title || query == originalTitle {
		return 1.0
	}
	if strings.Contains(title, query) || strings.Contains(originalTitle, query) {
		return 0.9
	}
	if strings.Contains(query, title) || strings.Contains(query, originalTitle) {
		return 0.8
	}
	titleSim := levenshteinSimilarity(query, title)
	originalSim := levenshteinSimilarity(query, originalTitle)
	return math.Max(titleSim, originalSim)
}

func yearSimilarity(queryYear *int, result *tmdbSearchResult) float64 {
	if queryYear == nil {
		return 0.5
	}
	var resultYear *int
	if result.FirstAirDate != "" {
		resultYear = extractYearFromDate(result.FirstAirDate)
	}
	if resultYear == nil && result.ReleaseDate != "" {
		resultYear = extractYearFromDate(result.ReleaseDate)
	}
	if resultYear == nil {
		return 0.3
	}
	diff := int(math.Abs(float64(*queryYear - *resultYear)))
	if diff == 0 {
		return 1.0
	}
	if diff <= tmdbYearTolerance {
		return 1.0 - float64(diff)*0.2
	}
	return 0.0
}

func popularityWeight(popularity float64) float64 {
	return math.Min(popularity/100.0, 1.0)
}

func languageWeight(language string) float64 {
	switch language {
	case "zh", "zh-cn", "zh-tw":
		return 1.0
	case "ja":
		return 0.9
	case "en":
		return 0.7
	default:
		return 0.5
	}
}

func extractYearFromDate(date string) *int {
	if len(date) >= 4 {
		return parseInt(date[:4])
	}
	return nil
}

func hasAnimationGenre(genreIDs []int) bool {
	const animationGenreID = 16
	for _, id := range genreIDs {
		if id == animationGenreID {
			return true
		}
	}
	return false
}

func tmdbResultTitle(r *tmdbSearchResult) string {
	if r.Name != "" {
		return r.Name
	}
	return r.Title
}

func tmdbResultOriginalTitle(r *tmdbSearchResult) string {
	if r.OriginalName != "" {
		return r.OriginalName
	}
	return r.OriginalTitle
}

// --- Season identification ---

// identifySeason applies a five-rule strategy to pick the correct season
// number from the TMDB details seasons array.
func identifySeason(seasons []tmdbSeason, requested *int) (int, bool) {
	if len(seasons) == 0 {
		return 0, false
	}

	// Rule 1: Ignore specials (season 0), then honor an explicit season marker
	// from the source title when TMDB contains that season.
	var regular []tmdbSeason
	for _, s := range seasons {
		if s.SeasonNumber > 0 {
			regular = append(regular, s)
		}
	}
	if len(regular) == 0 {
		return 0, false
	}
	if requested != nil {
		for _, s := range regular {
			if s.SeasonNumber == *requested {
				return s.SeasonNumber, true
			}
		}
	}
	if len(regular) == 1 {
		return regular[0].SeasonNumber, true
	}

	// Rule 2: Match by air date within ±3 days.
	// (We don't have the anime's air date here, so skip to rule 3.)
	// In a future enhancement we could pass the air date through.

	// Rule 3: Use the most recently aired season (avoid future seasons).
	now := time.Now()
	var bestSeason *tmdbSeason
	var minTimeDiff = time.Hour * 24 * 365 * 10
	for i := range regular {
		s := &regular[i]
		if s.AirDate == "" {
			continue
		}
		seasonDate, err := time.Parse("2006-01-02", s.AirDate)
		if err != nil {
			continue
		}
		if seasonDate.Before(now) || seasonDate.Equal(now) {
			diff := now.Sub(seasonDate)
			if diff < minTimeDiff {
				minTimeDiff = diff
				bestSeason = s
			}
		}
	}
	if bestSeason != nil {
		return bestSeason.SeasonNumber, true
	}

	// Rule 4: All seasons are in the future — use the earliest.
	earliest := regular[0]
	for _, s := range regular {
		if s.SeasonNumber < earliest.SeasonNumber {
			earliest = s
		}
	}
	return earliest.SeasonNumber, true
}

// --- URL builders ---

func buildTMDBSearchURL(endpoint, apiKey, query, mediaKind string, year *int) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse tmdb endpoint: %w", err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/search/" + mediaKind
	q := u.Query()
	if !isBearerToken(apiKey) {
		q.Set("api_key", apiKey)
	}
	q.Set("query", query)
	q.Set("language", "zh-CN")
	q.Set("include_adult", "false")
	if year != nil {
		if mediaKind == "tv" {
			q.Set("first_air_date_year", strconv.Itoa(*year))
		} else {
			q.Set("year", strconv.Itoa(*year))
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func buildTMDBDetailsURL(endpoint, apiKey string, tmdbID int) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse tmdb endpoint: %w", err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/tv/" + strconv.Itoa(tmdbID)
	q := u.Query()
	if !isBearerToken(apiKey) {
		q.Set("api_key", apiKey)
	}
	q.Set("language", "zh-CN")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// isBearerToken detects TMDB v4 read-access tokens (JWT format starting with
// "eyJ"). v3 API keys are 32-char hex strings and use ?api_key= instead.
func isBearerToken(apiKey string) bool {
	return strings.HasPrefix(apiKey, "eyJ")
}

// stripConfigurationSuffix removes a trailing /configuration from the endpoint
// so that /search/tv etc. can be appended cleanly.
func stripConfigurationSuffix(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	endpoint = strings.TrimSuffix(endpoint, "/configuration")
	return endpoint
}
