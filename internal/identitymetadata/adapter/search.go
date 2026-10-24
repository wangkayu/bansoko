package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	hardeningtransport "bansoko/internal/hardening/transport"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	identitymetadataport "bansoko/internal/identitymetadata/port"
	"bansoko/internal/runtime/buildinfo"
)

type ConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

type SecretResolver interface {
	Resolve(string) (string, bool)
}

type HTTPClientResolver interface {
	ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error)
}

const (
	tmdbDetailsTimeout     = 3 * time.Second
	tmdbDetailsConcurrency = 4
)

type tmdbDetailsResponse struct {
	Seasons []struct {
		SeasonNumber int    `json:"season_number"`
		Name         string `json:"name"`
		EpisodeCount int    `json:"episode_count"`
		AirDate      string `json:"air_date"`
	} `json:"seasons"`
}

// ConfiguredCandidateSearcher keeps external metadata I/O behind a narrow
// provider port. TMDB uses the configured connection; Bangumi uses its public
// title-search endpoint because no BGM credential is required for this lookup.
// Search results are normalized into the identity-metadata candidate type.
type ConfiguredCandidateSearcher struct {
	Connections    ConnectionSource
	Secrets        SecretResolver
	ClientResolver HTTPClientResolver
	Client         *http.Client
	BGMEndpoint    string
}

var _ identitymetadataport.CandidateSearcher = ConfiguredCandidateSearcher{}

func (s ConfiguredCandidateSearcher) Search(ctx context.Context, provider, query string, limit int) ([]identitymetadatadomain.SearchCandidate, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("metadata search query is required")
	}
	if limit < 1 || limit > 50 {
		return nil, errors.New("metadata search limit must be between 1 and 50")
	}
	switch provider {
	case "tmdb":
		return s.searchTMDB(ctx, query, limit)
	case "bgm":
		return s.searchBGM(ctx, query, limit)
	default:
		return nil, fmt.Errorf("unsupported metadata search provider %q", provider)
	}
}

func (s ConfiguredCandidateSearcher) searchTMDB(ctx context.Context, query string, limit int) ([]identitymetadatadomain.SearchCandidate, error) {
	if s.Connections == nil {
		return nil, errors.New("TMDB connection source is not configured")
	}
	connections, err := s.Connections.ListConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("list TMDB connections: %w", err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionTMDB)
	if err != nil {
		return nil, err
	}
	if s.Secrets == nil || strings.TrimSpace(connection.SecretRef) == "" {
		return nil, &apperrors.Business{Status: 409, Code: "TMDB_CREDENTIAL_NOT_CONFIGURED", Message: "TMDB API key is not configured; set it in 设置 → 连接 before searching TMDB metadata."}
	}
	apiKey, ok := s.Secrets.Resolve(connection.SecretRef)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return nil, &apperrors.Business{Status: 409, Code: "TMDB_CREDENTIAL_NOT_CONFIGURED", Message: "TMDB API key is not configured; set it in 设置 → 连接 before searching TMDB metadata."}
	}
	client := s.Client
	if s.ClientResolver != nil {
		resolved, resolveErr := s.ClientResolver.ClientForConnection(ctx, connection)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve TMDB network route: %w", resolveErr)
		}
		if resolved != nil {
			client = resolved
		}
	}
	endpoint, err := appendEndpointPath(connection.Endpoint, "/search/multi")
	if err != nil {
		return nil, fmt.Errorf("resolve TMDB search endpoint: %w", err)
	}
	params := endpoint.Query()
	if !isBearerToken(apiKey) {
		params.Set("api_key", apiKey)
	}
	params.Set("query", query)
	params.Set("include_adult", "false")
	params.Set("language", "zh-CN")
	params.Set("page", "1")
	endpoint.RawQuery = params.Encode()
	var payload struct {
		Results []struct {
			ID            int    `json:"id"`
			MediaType     string `json:"media_type"`
			Name          string `json:"name"`
			OriginalName  string `json:"original_name"`
			Title         string `json:"title"`
			OriginalTitle string `json:"original_title"`
			Overview      string `json:"overview"`
			FirstAirDate  string `json:"first_air_date"`
			ReleaseDate   string `json:"release_date"`
			GenreIDs      []int  `json:"genre_ids"`
			PosterPath    string `json:"poster_path"`
		} `json:"results"`
	}
	if err := s.getJSON(ctx, client, endpoint, apiKey, &payload); err != nil {
		return nil, fmt.Errorf("request TMDB candidates: %w", err)
	}
	result := make([]identitymetadatadomain.SearchCandidate, 0, minInt(limit, len(payload.Results)))
	type detailsJob struct {
		resultIndex int
		tmdbID      int
	}
	jobs := make([]detailsJob, 0, minInt(limit, len(payload.Results)))
	for index, item := range payload.Results {
		if len(result) >= limit || item.ID <= 0 {
			break
		}
		kind := strings.ToLower(strings.TrimSpace(item.MediaType))
		if kind != "tv" && kind != "movie" {
			continue
		}
		title := strings.TrimSpace(item.Name)
		originalTitle := strings.TrimSpace(item.OriginalName)
		if kind == "movie" {
			title = strings.TrimSpace(item.Title)
			originalTitle = strings.TrimSpace(item.OriginalTitle)
		}
		// titleCN is TMDB's title for the requested language (language=zh-CN);
		// it stays the recorded fact even when the client-facing display title
		// falls back to the original title below.
		titleCN := title
		if title == "" {
			title = originalTitle
		}
		year := parseYear(item.FirstAirDate)
		if year == 0 {
			year = parseYear(item.ReleaseDate)
		}
		detailsStatus := identitymetadatadomain.DetailsStatusNotApplicable
		poster := ""
		if strings.TrimSpace(item.PosterPath) != "" {
			poster = "https://image.tmdb.org/t/p/w342" + strings.TrimSpace(item.PosterPath)
		}
		result = append(result, identitymetadatadomain.SearchCandidate{
			Provider: "tmdb", ProviderWorkID: strconv.Itoa(item.ID), Title: title,
			OriginalTitle: originalTitle, Overview: strings.TrimSpace(item.Overview),
			PosterURL: poster, MediaKind: kind, DetailsStatus: detailsStatus,
			GenreIDs: append([]int(nil), item.GenreIDs...), Year: candidateYear(year), Score: rankScore(index, len(payload.Results)),
			Native: identitymetadatadomain.WorkTitles{Provider: "tmdb", MediaKind: kind, WorkID: strconv.Itoa(item.ID), TitleCN: titleCN, OriginalTitle: originalTitle, Overview: strings.TrimSpace(item.Overview), Year: recordedYear(year)},
		})
		if kind == "tv" {
			jobs = append(jobs, detailsJob{resultIndex: len(result) - 1, tmdbID: item.ID})
		}
	}
	if len(jobs) > 0 {
		workerCount := minInt(tmdbDetailsConcurrency, len(jobs))
		jobQueue := make(chan detailsJob)
		var workers sync.WaitGroup
		workers.Add(workerCount)
		for i := 0; i < workerCount; i++ {
			go func() {
				defer workers.Done()
				for job := range jobQueue {
					seasons, suggestedSeason, detailsStatus := s.loadTMDBSeasons(ctx, client, connection.Endpoint, apiKey, job.tmdbID)
					result[job.resultIndex].Seasons = seasons
					result[job.resultIndex].SuggestedSeason = suggestedSeason
					result[job.resultIndex].DetailsStatus = detailsStatus
				}
			}()
		}
		for _, job := range jobs {
			jobQueue <- job
		}
		close(jobQueue)
		workers.Wait()
	}
	return result, nil
}

func (s ConfiguredCandidateSearcher) loadTMDBSeasons(parent context.Context, client *http.Client, base, apiKey string, tmdbID int) ([]identitymetadatadomain.SearchSeason, *int, identitymetadatadomain.DetailsStatus) {
	endpoint, err := appendEndpointPath(base, "/tv/"+strconv.Itoa(tmdbID))
	if err != nil {
		return nil, nil, identitymetadatadomain.DetailsStatusError
	}
	params := endpoint.Query()
	if !isBearerToken(apiKey) {
		params.Set("api_key", apiKey)
	}
	params.Set("language", "zh-CN")
	endpoint.RawQuery = params.Encode()

	ctx, cancel := context.WithTimeout(parent, tmdbDetailsTimeout)
	defer cancel()
	var payload tmdbDetailsResponse
	if err := s.getJSON(ctx, client, endpoint, apiKey, &payload); err != nil {
		return nil, nil, identitymetadatadomain.DetailsStatusError
	}
	seasons := make([]identitymetadatadomain.SearchSeason, 0, len(payload.Seasons))
	var suggested *int
	for _, season := range payload.Seasons {
		isSpecial := season.SeasonNumber == 0
		seasons = append(seasons, identitymetadatadomain.SearchSeason{
			SeasonNumber: season.SeasonNumber,
			Name:         strings.TrimSpace(season.Name),
			EpisodeCount: season.EpisodeCount,
			AirDate:      strings.TrimSpace(season.AirDate),
			IsSpecial:    isSpecial,
		})
		if suggested == nil && !isSpecial {
			value := season.SeasonNumber
			suggested = &value
		}
	}
	if len(seasons) == 0 {
		return seasons, nil, identitymetadatadomain.DetailsStatusEmpty
	}
	return seasons, suggested, identitymetadatadomain.DetailsStatusAvailable
}

func (s ConfiguredCandidateSearcher) searchBGM(ctx context.Context, query string, limit int) ([]identitymetadatadomain.SearchCandidate, error) {
	// The optional bgm connection owns the endpoint and egress route, the same
	// client path the metadata matcher's BGM layer uses. Without a configured
	// connection the search keeps the public endpoint and the default client.
	base := strings.TrimSpace(s.BGMEndpoint)
	if base == "" {
		base = "https://api.bgm.tv"
	}
	client := s.Client
	if s.Connections != nil {
		connections, err := s.Connections.ListConnections(ctx)
		if err != nil {
			return nil, fmt.Errorf("list BGM connections: %w", err)
		}
		connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionBGM)
		if err == nil {
			if strings.TrimSpace(connection.Endpoint) != "" {
				base = strings.TrimSpace(connection.Endpoint)
			}
			if s.ClientResolver != nil {
				resolved, resolveErr := s.ClientResolver.ClientForConnection(ctx, connection)
				if resolveErr != nil {
					return nil, fmt.Errorf("resolve BGM network route: %w", resolveErr)
				}
				if resolved != nil {
					client = resolved
				}
			}
		}
	}
	// The v0 search answers with each subject's air date (`date`); the legacy
	// `/search/subject/{q}` endpoint returns an empty `air_date` for every
	// result, which recorded every searched work with an unknown year.
	endpoint, err := appendEndpointPath(base, "/v0/search/subjects")
	if err != nil {
		return nil, fmt.Errorf("resolve Bangumi search endpoint: %w", err)
	}
	params := endpoint.Query()
	params.Set("limit", strconv.Itoa(limit))
	endpoint.RawQuery = params.Encode()
	body, err := json.Marshal(map[string]any{"keyword": query, "filter": map[string]any{"type": []int{2}}})
	if err != nil {
		return nil, fmt.Errorf("encode Bangumi search request: %w", err)
	}
	var payload struct {
		List []struct {
			ID       int    `json:"id"`
			Name     string `json:"name"`
			NameCN   string `json:"name_cn"`
			AirDate  string `json:"date"`
			Platform string `json:"platform"`
			Images   struct {
				Common string `json:"common"`
			} `json:"images"`
		} `json:"data"`
	}
	if err := s.doJSON(ctx, client, http.MethodPost, endpoint, "", body, &payload); err != nil {
		return nil, fmt.Errorf("request Bangumi candidates: %w", err)
	}
	result := make([]identitymetadatadomain.SearchCandidate, 0, minInt(limit, len(payload.List)))
	for index, item := range payload.List {
		if len(result) >= limit || item.ID <= 0 {
			break
		}
		title := strings.TrimSpace(item.NameCN)
		if title == "" {
			title = strings.TrimSpace(item.Name)
		}
		result = append(result, identitymetadatadomain.SearchCandidate{
			Provider: "bgm", ProviderWorkID: strconv.Itoa(item.ID), Title: title,
			OriginalTitle: strings.TrimSpace(item.Name), PosterURL: strings.TrimSpace(item.Images.Common),
			MediaKind: "tv", DetailsStatus: identitymetadatadomain.DetailsStatusNotApplicable,
			Year: candidateYear(parseYear(item.AirDate)), Score: rankScore(index, len(payload.List)),
			ReleaseFormat: releaseFormatText(identitymetadatadomain.ReleaseFormatFromBangumiPlatform(item.Platform)),
			Native:        identitymetadatadomain.WorkTitles{Provider: "bgm", MediaKind: "subject", WorkID: strconv.Itoa(item.ID), Title: strings.TrimSpace(item.Name), TitleCN: strings.TrimSpace(item.NameCN), Year: recordedYear(parseYear(item.AirDate))},
		})
	}
	return result, nil
}

func (s ConfiguredCandidateSearcher) getJSON(ctx context.Context, client *http.Client, endpoint *url.URL, apiKey string, target any) error {
	return s.doJSON(ctx, client, http.MethodGet, endpoint, apiKey, nil, target)
}

func (s ConfiguredCandidateSearcher) doJSON(ctx context.Context, client *http.Client, method string, endpoint *url.URL, apiKey string, body []byte, target any) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", buildinfo.UserAgent("metadata-search"))
	if isBearerToken(apiKey) {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("provider returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}

func appendEndpointPath(raw, suffix string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("metadata provider endpoint is invalid")
	}
	base := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(base, "/configuration") {
		base = strings.TrimSuffix(base, "/configuration")
	}
	parsed.Path = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(suffix, "/")
	return parsed, nil
}

// isBearerToken detects TMDB v4 read-access tokens (JWT format starting with
// "eyJ"). v3 API keys are 32-char hex strings and use ?api_key= instead.
func isBearerToken(apiKey string) bool {
	return strings.HasPrefix(apiKey, "eyJ")
}

func parseYear(value string) int {
	value = strings.TrimSpace(value)
	if len(value) < 4 {
		return 0
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil || year < 0 {
		return 0
	}
	return year
}

// recordedYear is the provider year a candidate's Native titles carry: the
// parsed value when it is a plausible release year, nil otherwise. A missing
// year is recorded as unknown, never defaulted.
func recordedYear(value int) *int {
	year := value
	return identitymetadatadomain.NormalizeYear(&year)
}

// candidateYear is the year a search result shows and a match fills in: the
// same plausible provider year Native records, 0 (omitted on the wire) when
// the provider gave no usable date.
func candidateYear(value int) int {
	if year := recordedYear(value); year != nil {
		return *year
	}
	return 0
}

func rankScore(index, total int) float64 {
	if total <= 1 {
		return 1
	}
	score := 1 - float64(index)/float64(total)
	if score < 0 {
		return 0
	}
	return score
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// releaseFormatText is the candidate's release format, empty when unknown.
func releaseFormatText(format *string) string {
	if format == nil {
		return ""
	}
	return *format
}
