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
	"sort"
	"strconv"
	"strings"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
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
	// Seasons is the real TV season list returned by the work details request.
	Seasons []identitymetadatadomain.SearchSeason
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

// Match searches TMDB without a separate target air-date clue.
func (m *TMDBMatcher) Match(ctx context.Context, title string) (*TMDBMatchResult, error) {
	return m.MatchWithTargetDate(ctx, title, "")
}

// MatchWithTargetDate searches for a work and then resolves its season using
// the original title and the BGM air date. Trimming never erases those clues.
func (m *TMDBMatcher) MatchWithTargetDate(ctx context.Context, title, targetAirDate string) (*TMDBMatchResult, error) {
	if strings.TrimSpace(title) == "" {
		return nil, nil
	}
	client, endpoint, apiKey, err := m.resolveAccess(ctx)
	if err != nil {
		return nil, err
	}
	result, err := m.searchAndScore(ctx, client, endpoint, apiKey, title, "tv", targetAirDate)
	if err != nil {
		return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("tmdb tv search: %w", err)}
	}
	if result != nil && result.Confidence >= tmdbMinConfidence {
		result.Method = "tv"
		return result, nil
	}
	movieResult, err := m.searchAndScore(ctx, client, endpoint, apiKey, title, "movie", targetAirDate)
	if err != nil {
		return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("tmdb movie search: %w", err)}
	}
	if movieResult != nil && movieResult.Confidence >= tmdbMinConfidence {
		movieResult.Method = "movie"
		return movieResult, nil
	}
	return nil, nil
}

// MatchReference resolves the exact work supplied by bangumi-data or an
// existing binding. It never searches for and combines a different work.
func (m *TMDBMatcher) MatchReference(ctx context.Context, title, mediaKind, workID string, mappedSeason *int, targetAirDate string) (*TMDBMatchResult, error) {
	if mediaKind != "tv" && mediaKind != "movie" || !numericID(workID) {
		return nil, nil
	}
	id, err := strconv.Atoi(workID)
	if err != nil || id <= 0 {
		return nil, nil
	}
	client, endpoint, apiKey, err := m.resolveAccess(ctx)
	if err != nil {
		return nil, err
	}
	details, err := m.getDetails(ctx, client, endpoint, apiKey, mediaKind, id)
	if err != nil {
		return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("tmdb work details: %w", err)}
	}
	season := 0
	if mediaKind == "tv" {
		requested := m.trimmer().ExtractSeasonNumber(title)
		identified, ok, err := m.selectSeason(ctx, client, endpoint, apiKey, id, details.Seasons, requested, mappedSeason, targetAirDate)
		if err != nil {
			return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: err}
		}
		if !ok {
			return nil, nil
		}
		season = identified
	}
	return &TMDBMatchResult{
		WorkID: workID, MediaKind: mediaKind, Season: season, Confidence: 1, Method: "reference",
		TitleCN:       strings.TrimSpace(firstNonEmpty(details.Name, details.Title)),
		OriginalTitle: strings.TrimSpace(firstNonEmpty(details.OriginalName, details.OriginalTitle)),
		Overview:      strings.TrimSpace(details.Overview), Year: providerYearFromDate(firstNonEmpty(details.FirstAirDate, details.ReleaseDate)),
		Seasons: tmdbSearchSeasons(details.Seasons),
	}, nil
}

func (m *TMDBMatcher) resolveAccess(ctx context.Context) (*http.Client, string, string, error) {
	if m.Connections == nil {
		return nil, "", "", &MatchError{Kind: ErrorConfig, Layer: "tmdb", Err: errors.New("TMDB connection source is not configured")}
	}
	connections, err := m.Connections.ListConnections(ctx)
	if err != nil {
		return nil, "", "", &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("list tmdb connections: %w", err)}
	}
	conn, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionTMDB)
	if err != nil {
		return nil, "", "", &MatchError{Kind: ErrorConfig, Layer: "tmdb", Err: err}
	}
	if conn.SecretRef == "" || m.Secrets == nil {
		return nil, "", "", &MatchError{Kind: ErrorConfig, Layer: "tmdb", Err: errors.New("TMDB API key is not configured")}
	}
	apiKey, ok := m.Secrets.Resolve(conn.SecretRef)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return nil, "", "", &MatchError{Kind: ErrorConfig, Layer: "tmdb", Err: errors.New("TMDB API key is unavailable")}
	}
	client := m.Client
	if m.ClientResolver != nil {
		resolved, resolveErr := m.ClientResolver.ClientForConnection(ctx, conn)
		if resolveErr != nil {
			return nil, "", "", &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("resolve tmdb network route: %w", resolveErr)}
		}
		if resolved != nil {
			client = resolved
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	return client, stripConfigurationSuffix(conn.Endpoint), apiKey, nil
}

func (m *TMDBMatcher) searchAndScore(ctx context.Context, client *http.Client, endpoint, apiKey, query, mediaKind, targetAirDate string) (*TMDBMatchResult, error) {
	requestedSeason := m.trimmer().ExtractSeasonNumber(query)
	// Try original title first.
	result, err := m.trySearch(ctx, client, endpoint, apiKey, query, mediaKind, nil, requestedSeason, targetAirDate)
	if err != nil {
		return nil, err
	}
	if result != nil && result.Confidence >= tmdbMinConfidence {
		return result, nil
	}

	// Try trimmed title.
	trimmed := m.trimmer().TrimTitle(query)
	if trimmed != query {
		r2, err := m.trySearch(ctx, client, endpoint, apiKey, trimmed, mediaKind, nil, requestedSeason, targetAirDate)
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
		r3, err := m.trySearch(ctx, client, endpoint, apiKey, trimmed, mediaKind, year, requestedSeason, targetAirDate)
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
	Name         string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date"`
}

type tmdbDetailsResponse struct {
	Name          string       `json:"name"`
	OriginalName  string       `json:"original_name"`
	Title         string       `json:"title"`
	OriginalTitle string       `json:"original_title"`
	Overview      string       `json:"overview"`
	FirstAirDate  string       `json:"first_air_date"`
	ReleaseDate   string       `json:"release_date"`
	Seasons       []tmdbSeason `json:"seasons"`
}

func (m *TMDBMatcher) trySearch(ctx context.Context, client *http.Client, endpoint, apiKey, query, mediaKind string, year *int, requestedSeason *int, targetAirDate string) (*TMDBMatchResult, error) {
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
	var seasons []identitymetadatadomain.SearchSeason
	if mediaKind == "tv" {
		details, err := m.getDetails(ctx, client, endpoint, apiKey, mediaKind, best.ID)
		if err != nil {
			return nil, fmt.Errorf("get tv details for %d: %w", best.ID, err)
		}
		if details == nil {
			return nil, nil
		}
		identified, ok, err := m.selectSeason(ctx, client, endpoint, apiKey, best.ID, details.Seasons, requestedSeason, nil, targetAirDate)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		season = identified
		seasons = tmdbSearchSeasons(details.Seasons)
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
		Seasons:       seasons,
	}, nil
}

func tmdbSearchSeasons(seasons []tmdbSeason) []identitymetadatadomain.SearchSeason {
	if seasons == nil {
		return nil
	}
	result := make([]identitymetadatadomain.SearchSeason, 0, len(seasons))
	for _, season := range seasons {
		result = append(result, identitymetadatadomain.SearchSeason{
			SeasonNumber: season.SeasonNumber,
			Name:         strings.TrimSpace(season.Name),
			EpisodeCount: season.EpisodeCount,
			AirDate:      strings.TrimSpace(season.AirDate),
			IsSpecial:    season.SeasonNumber == 0,
		})
	}
	return result
}

func (m *TMDBMatcher) getDetails(ctx context.Context, client *http.Client, endpoint, apiKey, mediaKind string, tmdbID int) (*tmdbDetailsResponse, error) {
	detailsURL, err := buildTMDBDetailsURL(endpoint, apiKey, mediaKind, tmdbID)
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

// selectSeason uses explicit references and the original title first, then
// season and episode air dates. A known conflict never falls through to the
// unique/recent-season heuristic. Episode matches never produce an EP offset.
func (m *TMDBMatcher) selectSeason(ctx context.Context, client *http.Client, endpoint, apiKey string, workID int, seasons []tmdbSeason, requested, mapped *int, targetAirDate string) (int, bool, error) {
	for _, explicit := range []*int{mapped, requested} {
		if explicit == nil {
			continue
		}
		for _, season := range seasons {
			if season.SeasonNumber == *explicit {
				return season.SeasonNumber, true, nil
			}
		}
	}
	regular := regularSeasons(seasons)
	if len(regular) == 0 {
		return 0, false, nil
	}

	target, hasTarget := providerDate(targetAirDate)
	if hasTarget {
		best, ok := closestSeasonByAirDate(regular, target)
		if ok {
			return best, true, nil
		}
		ordered := append([]tmdbSeason(nil), regular...)
		sort.SliceStable(ordered, func(i, j int) bool {
			left, lok := providerDate(ordered[i].AirDate)
			right, rok := providerDate(ordered[j].AirDate)
			if lok != rok {
				return lok
			}
			if lok {
				leftBefore, rightBefore := !left.After(target), !right.After(target)
				if leftBefore != rightBefore {
					return leftBefore
				}
				return dateDistance(left, target) < dateDistance(right, target)
			}
			return ordered[i].SeasonNumber < ordered[j].SeasonNumber
		})
		for _, season := range ordered {
			matched, err := m.seasonHasEpisodeNear(ctx, client, endpoint, apiKey, workID, season.SeasonNumber, target)
			if err != nil {
				return 0, false, err
			}
			if matched {
				return season.SeasonNumber, true, nil
			}
		}
		return 0, false, nil
	}
	if requested != nil || mapped != nil {
		return 0, false, nil
	}
	season, ok := identifySeason(regular, nil)
	return season, ok, nil
}

func regularSeasons(seasons []tmdbSeason) []tmdbSeason {
	regular := make([]tmdbSeason, 0, len(seasons))
	for _, season := range seasons {
		if season.SeasonNumber > 0 {
			regular = append(regular, season)
		}
	}
	return regular
}

func providerDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

func dateDistance(a, b time.Time) time.Duration {
	delta := a.Sub(b)
	if delta < 0 {
		return -delta
	}
	return delta
}

func closestSeasonByAirDate(seasons []tmdbSeason, target time.Time) (int, bool) {
	best := 0
	var bestDistance time.Duration
	for _, season := range seasons {
		date, ok := providerDate(season.AirDate)
		if !ok {
			continue
		}
		distance := dateDistance(date, target)
		if distance <= 72*time.Hour && (best == 0 || distance < bestDistance) {
			best, bestDistance = season.SeasonNumber, distance
		}
	}
	return best, best != 0
}

type tmdbSeasonDetailsResponse struct {
	Episodes []struct {
		AirDate string `json:"air_date"`
	} `json:"episodes"`
}

func (m *TMDBMatcher) seasonHasEpisodeNear(ctx context.Context, client *http.Client, endpoint, apiKey string, workID, season int, target time.Time) (bool, error) {
	rawURL, err := buildTMDBSeasonURL(endpoint, apiKey, workID, season)
	if err != nil {
		return false, err
	}
	var response tmdbSeasonDetailsResponse
	if err := m.getJSON(ctx, client, rawURL, apiKey, &response); err != nil {
		return false, fmt.Errorf("get tv season %d episodes: %w", season, err)
	}
	for _, episode := range response.Episodes {
		date, ok := providerDate(episode.AirDate)
		if ok && dateDistance(date, target) <= 72*time.Hour {
			return true, nil
		}
	}
	return false, nil
}

// identifySeason is the lightweight no-clue fallback used by direct callers.
func identifySeason(seasons []tmdbSeason, requested *int) (int, bool) {
	regular := regularSeasons(seasons)
	if len(regular) == 0 {
		return 0, false
	}
	if requested != nil {
		for _, season := range regular {
			if season.SeasonNumber == *requested {
				return season.SeasonNumber, true
			}
		}
		return 0, false
	}
	if len(regular) == 1 {
		return regular[0].SeasonNumber, true
	}

	now := time.Now()
	var bestSeason *tmdbSeason
	var minTimeDiff time.Duration
	for i := range regular {
		season := &regular[i]
		seasonDate, ok := providerDate(season.AirDate)
		if !ok || seasonDate.After(now) {
			continue
		}
		if diff := now.Sub(seasonDate); bestSeason == nil || diff < minTimeDiff {
			minTimeDiff, bestSeason = diff, season
		}
	}
	if bestSeason != nil {
		return bestSeason.SeasonNumber, true
	}
	earliest := regular[0]
	for _, season := range regular {
		if season.SeasonNumber < earliest.SeasonNumber {
			earliest = season
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

func buildTMDBDetailsURL(endpoint, apiKey, mediaKind string, tmdbID int) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse tmdb endpoint: %w", err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + mediaKind + "/" + strconv.Itoa(tmdbID)
	q := u.Query()
	if !isBearerToken(apiKey) {
		q.Set("api_key", apiKey)
	}
	q.Set("language", "zh-CN")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func buildTMDBSeasonURL(endpoint, apiKey string, tmdbID, season int) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse tmdb endpoint: %w", err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/tv/" + strconv.Itoa(tmdbID) + "/season/" + strconv.Itoa(season)
	q := u.Query()
	if !isBearerToken(apiKey) {
		q.Set("api_key", apiKey)
	}
	q.Set("language", "zh-CN")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
