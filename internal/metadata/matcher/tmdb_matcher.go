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
	"unicode"

	"golang.org/x/text/unicode/norm"

	configurationdomain "bansoko/internal/configuration/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	"bansoko/internal/parser/reltitletags"
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
// correct season from explicit references and airing evidence. It reads the
// TMDB connection endpoint and API key on every match.
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

// Match searches TMDB without a separate target air-date clue.
func (m *TMDBMatcher) Match(ctx context.Context, title string) (*TMDBMatchResult, error) {
	return m.MatchWithTargetDate(ctx, title, "")
}

// MatchWithTargetDate searches for a work and then resolves its season using
// the original title and the BGM air date. Trimming never erases those clues.
func (m *TMDBMatcher) MatchWithTargetDate(ctx context.Context, title, targetAirDate string) (*TMDBMatchResult, error) {
	return m.matchTitles(ctx, title, []string{title}, parseProviderClue(targetAirDate, "target"))
}

const tmdbMatchBudget = 15 * time.Second

var (
	errAmbiguousTMDB        = errors.New("ambiguous TMDB work")
	errUnresolvedTMDBSeason = errors.New("unresolved TMDB season")
)

func (m *TMDBMatcher) matchTitles(ctx context.Context, original string, titles []string, target dateClue) (*TMDBMatchResult, error) {
	return m.matchTitlesWithPreference(ctx, original, titles, target, mediaPreferenceForTitle(original, m.trimmer()))
}

func (m *TMDBMatcher) matchTitlesWithPreference(ctx context.Context, original string, titles []string, target dateClue, preference mediaPreference) (*TMDBMatchResult, error) {
	if strings.TrimSpace(firstNonEmpty(titles...)) == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, tmdbMatchBudget)
	defer cancel()
	client, endpoint, apiKey, err := m.resolveAccess(ctx)
	if err != nil {
		return nil, err
	}
	clues := tmdbSeasonClues{requested: m.trimmer().ExtractSeasonNumber(original), date: target}
	if target.precision == dateUnknown {
		clues.weakYear = reltitletags.WeakYear(original)
	}
	failedKinds := make(map[string]bool, 2)
	for _, terms := range m.searchTitleGroups(titles) {
		for _, kind := range mediaKinds(preference) {
			if failedKinds[kind] {
				continue
			}
			kindTerms := terms
			if kind == "movie" && preference == mediaPrefMovieOnly {
				kindTerms = appendMoviePrefixTerms(kindTerms)
			}
			for _, term := range kindTerms {
				result, err := m.trySearch(ctx, client, endpoint, apiKey, term, kind, clues)
				switch {
				case errors.Is(err, errAmbiguousTMDB), errors.Is(err, errUnresolvedTMDBSeason):
					// Stop this media kind's query terms, but keep other kinds alive.
					failedKinds[kind] = true
				case err != nil:
					return nil, &MatchError{Kind: ErrorProvider, Layer: "tmdb", Err: fmt.Errorf("tmdb %s search: %w", kind, err)}
				case result != nil:
					result.Method = kind
					return result, nil
				}
				if failedKinds[kind] {
					break
				}
			}
		}
	}
	return nil, nil
}

func mediaKinds(preference mediaPreference) []string {
	switch preference {
	case mediaPrefMovieOnly:
		return []string{"movie"}
	case mediaPrefTVOnly:
		return []string{"tv"}
	case mediaPrefMovieFirst:
		return []string{"movie", "tv"}
	case mediaPrefTVFirst:
		return []string{"tv", "movie"}
	default:
		return []string{"tv", "movie"}
	}
}

func appendMoviePrefixTerms(terms []string) []string {
	result := append([]string(nil), terms...)
	seen := make(map[string]bool, len(result))
	for _, term := range result {
		seen[bgmTitleKey(term)] = true
	}
	for _, term := range terms {
		stripped := stripMoviePrefix(term)
		if stripped != "" && !seen[bgmTitleKey(stripped)] {
			result = append(result, stripped)
			seen[bgmTitleKey(stripped)] = true
		}
	}
	return result
}

// MatchReference resolves the exact work supplied by bangumi-data or an
// existing binding. It never searches for and combines a different work.
func (m *TMDBMatcher) MatchReference(ctx context.Context, title, mediaKind, workID string, mappedSeason *int, targetAirDate string) (*TMDBMatchResult, error) {
	return m.matchReference(ctx, title, mediaKind, workID, mappedSeason, parseProviderClue(targetAirDate, "target"))
}

func (m *TMDBMatcher) matchReference(ctx context.Context, title, mediaKind, workID string, mappedSeason *int, targetAirDate dateClue) (*TMDBMatchResult, error) {
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
		clues := tmdbSeasonClues{requested: m.trimmer().ExtractSeasonNumber(title), mapped: mappedSeason, date: targetAirDate}
		if targetAirDate.precision == dateUnknown && mappedSeason == nil {
			clues.weakYear = reltitletags.WeakYear(title)
		}
		identified, ok, err := m.selectSeason(ctx, client, endpoint, apiKey, id, details.Seasons, clues)
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

// searchTitleGroups reserves the budget for the original relay terms first.
// A trimmed variant, when it fits, is tried within the same media kind.
func (m *TMDBMatcher) searchTitleGroups(titles []string) [][]string {
	const maxTerms = 3
	groups := make([][]string, 0, maxTerms)
	seen := make(map[string]bool, maxTerms)
	reserve := func(title string) bool {
		key := bgmTitleKey(title)
		if key == "" || seen[key] || len(seen) == maxTerms {
			return false
		}
		seen[key] = true
		return true
	}
	for _, title := range titles {
		if reserve(title) {
			groups = append(groups, []string{strings.TrimSpace(title)})
		}
	}
	for i, group := range groups {
		if len(seen) == maxTerms {
			break
		}
		if trimmed := m.trimmer().TrimTitle(group[0]); reserve(trimmed) {
			groups[i] = append(group, trimmed)
		}
	}
	return groups
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

func (m *TMDBMatcher) trySearch(ctx context.Context, client *http.Client, endpoint, apiKey, query, mediaKind string, clues tmdbSeasonClues) (*TMDBMatchResult, error) {
	searchURL, err := buildTMDBSearchURL(endpoint, apiKey, query, mediaKind)
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

	var best *tmdbSearchResult
	var bestConf float64
	type scoredCandidate struct {
		item            *tmdbSearchResult
		confidence      float64
		titleSimilarity float64
	}
	var candidates []scoredCandidate
	for i := range resp.Results {
		item := &resp.Results[i]
		if item.ID <= 0 || !hasAnimationGenre(item.GenreIDs) {
			continue
		}
		similarity := m.titleSimilarityWithClues(query, tmdbResultTitle(item), tmdbResultOriginalTitle(item), clues)
		conf := m.calculateConfidenceWithClues(query, item, clues)
		if conf < tmdbMinConfidence {
			continue
		}
		candidates = append(candidates, scoredCandidate{item: item, confidence: conf, titleSimilarity: similarity})
	}
	var highestSimilarity float64 = -1
	sameTitleIDs := make(map[int]bool)
	for _, candidate := range candidates {
		if candidate.titleSimilarity > highestSimilarity {
			highestSimilarity = candidate.titleSimilarity
			clear(sameTitleIDs)
		}
		if candidate.titleSimilarity == highestSimilarity {
			sameTitleIDs[candidate.item.ID] = true
		}
	}
	if len(sameTitleIDs) > 1 {
		return nil, errAmbiguousTMDB
	}
	ambiguous := false
	for _, candidate := range candidates {
		if candidate.confidence > bestConf {
			best, bestConf, ambiguous = candidate.item, candidate.confidence, false
		} else if candidate.confidence == bestConf && best != nil && best.ID != candidate.item.ID {
			ambiguous = true
		}
	}
	if ambiguous {
		return nil, errAmbiguousTMDB
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
		identified, ok, err := m.selectSeason(ctx, client, endpoint, apiKey, best.ID, details.Seasons, clues)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errUnresolvedTMDBSeason
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

func (m *TMDBMatcher) calculateConfidence(query string, result *tmdbSearchResult) float64 {
	return m.calculateConfidenceWithClues(query, result, tmdbSeasonClues{})
}

func (m *TMDBMatcher) calculateConfidenceWithClues(query string, result *tmdbSearchResult, clues tmdbSeasonClues) float64 {
	// Work confidence remains title-led; airing clues only select a TV season.
	conf := 0.1 + m.titleSimilarityWithClues(query, tmdbResultTitle(result), tmdbResultOriginalTitle(result), clues)*0.6
	conf += popularityWeight(result.Popularity) * 0.1
	conf += languageWeight(result.OriginalLanguage) * 0.1
	return math.Min(conf, 1.0)
}

func (m *TMDBMatcher) titleSimilarity(query, title, originalTitle string) float64 {
	return m.titleSimilarityWithClues(query, title, originalTitle, tmdbSeasonClues{})
}

func (m *TMDBMatcher) titleSimilarityWithClues(query, title, originalTitle string, clues tmdbSeasonClues) float64 {
	query = normalizeTMDBTitle(query)
	title = normalizeTMDBTitle(title)
	originalTitle = normalizeTMDBTitle(originalTitle)
	if query == "" {
		return 0
	}
	if query == title || query == originalTitle {
		return 1.0
	}
	compactQuery := compactTMDBTitle(query)
	compactTitle := compactTMDBTitle(title)
	compactOriginal := compactTMDBTitle(originalTitle)
	if containsScoreAllowed(compactQuery, compactTitle, compactOriginal, clues) {
		if compactTitle != "" && strings.Contains(compactTitle, compactQuery) || compactOriginal != "" && strings.Contains(compactOriginal, compactQuery) {
			return 0.9
		}
		if compactTitle != "" && strings.Contains(compactQuery, compactTitle) || compactOriginal != "" && strings.Contains(compactQuery, compactOriginal) {
			return 0.8
		}
	}
	titleSim := levenshteinSimilarity(query, title)
	originalSim := levenshteinSimilarity(query, originalTitle)
	return math.Max(titleSim, originalSim)
}

func normalizeTMDBTitle(value string) string {
	value = strings.NewReplacer("½", "1/2", "⅓", "1/3", "⅔", "2/3", "¼", "1/4", "¾", "3/4", "⅕", "1/5", "⅖", "2/5", "⅗", "3/5", "⅘", "4/5", "⅙", "1/6", "⅚", "5/6", "⅛", "1/8", "⅜", "3/8", "⅝", "5/8", "⅞", "7/8").Replace(value)
	value = norm.NFKC.String(value)
	value = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	return strings.TrimFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

func compactTMDBTitle(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return -1
		}
		return r
	}, value)
}

func containsScoreAllowed(compactQuery, compactTitle, compactOriginal string, clues tmdbSeasonClues) bool {
	if compactQuery == "" {
		return false
	}
	if clues.date.precision == dateUnknown && clues.weakYear == nil {
		if isShortTMDBQuery(compactQuery) {
			return false
		}
		for _, title := range []string{compactTitle, compactOriginal} {
			if title == "" {
				continue
			}
			short, long := len([]rune(compactQuery)), len([]rune(title))
			if short > long {
				short, long = long, short
			}
			if long > 0 && float64(short)/float64(long) < 0.5 {
				return false
			}
		}
	}
	return true
}

func isShortTMDBQuery(value string) bool {
	runes := []rune(value)
	containsCJK := false
	lettersOrDigits := 0
	cjkCount := 0
	for _, r := range runes {
		if isCJK(r) {
			containsCJK = true
			cjkCount++
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			lettersOrDigits++
		}
	}
	if containsCJK {
		return cjkCount <= 4
	}
	return lettersOrDigits <= 5
}

func isCJK(r rune) bool {
	return r >= 0x3040 && r <= 0x30ff || r >= 0x3400 && r <= 0x9fff || r >= 0xac00 && r <= 0xd7af
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

type tmdbSeasonClues struct {
	requested, mapped *int
	date              dateClue
	weakYear          *int
}

// selectSeason gives explicit references priority. A day clue can also match
// an episode of a merged season; less precise dates cannot.
func (m *TMDBMatcher) selectSeason(ctx context.Context, client *http.Client, endpoint, apiKey string, workID int, seasons []tmdbSeason, clues tmdbSeasonClues) (int, bool, error) {
	for _, explicit := range []*int{clues.mapped, clues.requested} {
		if explicit == nil {
			continue
		}
		for _, season := range seasons {
			if season.SeasonNumber == *explicit {
				return season.SeasonNumber, true, nil
			}
		}
	}
	targetAirDate := clues.date
	regular := regularSeasons(seasons)
	if len(regular) == 0 {
		return 0, false, nil
	}

	if targetAirDate.conflict {
		return 0, false, nil
	}
	if targetAirDate.precision == dateDay {
		target := targetAirDate.start
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
	if clues.requested != nil || clues.mapped != nil {
		return 0, false, nil
	}
	if targetAirDate.precision != dateUnknown {
		season, ok := seasonInWindow(regular, targetAirDate)
		return season, ok, nil
	}
	if clues.weakYear != nil {
		// A release-title year can confirm a sole season but cannot choose
		// between multiple seasons or compensate for a missing provider date.
		if len(regular) != 1 {
			return 0, false, nil
		}
		season := regular[0]
		if day, ok := providerDate(season.AirDate); ok && day.Year() == *clues.weakYear {
			return season.SeasonNumber, true, nil
		}
		return 0, false, nil
	}
	season, ok := identifySeason(regular, nil)
	return season, ok, nil
}

func seasonInWindow(regular []tmdbSeason, window dateClue) (int, bool) {
	best := 0
	for _, season := range regular {
		date, ok := providerDate(season.AirDate)
		if !ok {
			return 0, false
		}
		if !window.contains(date) {
			continue
		}
		if best != 0 && best != season.SeasonNumber {
			return 0, false
		}
		best = season.SeasonNumber
	}
	return best, best != 0
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
	if parsed, err := time.Parse(time.DateOnly, value); err == nil {
		return parsed, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	year, month, day := parsed.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), true
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

	// A work with multiple regular seasons needs explicit or date evidence;
	// never infer the latest aired season from the current clock.
	return 0, false
}

// --- URL builders ---

func buildTMDBSearchURL(endpoint, apiKey, query, mediaKind string) (string, error) {
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
