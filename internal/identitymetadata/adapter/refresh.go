package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/episodetitle"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
)

// ConfiguredWorkRefresher re-pulls one bound work from its provider, bypassing
// every cache. It is the engine of the 刷新元数据 action (owner decree
// 2026-10-22): the only positive-cache bypass in the product. The alignment
// and rename renders stay cache-first and never construct one.
type ConfiguredWorkRefresher struct {
	Connections    ConnectionSource
	Secrets        SecretResolver
	ClientResolver HTTPClientResolver
	Client         *http.Client
	BGMEndpoint    string
	// EpisodeTitles is the persistent provider episode title store; a bound
	// work's episode titles are re-pulled through it with the overwrite store.
	EpisodeTitles EpisodeTitleCacheRefresher
	// Titles records the re-pulled native titles (metadata_work_titles).
	Titles TitleRecorder
}

// EpisodeTitleCacheRefresher is the slice of the persistent episode title
// cache the refresher drives (implemented by *episodetitle.PersistentCache).
type EpisodeTitleCacheRefresher interface {
	RefreshWork(ctx context.Context, source episodetitle.Source, workID string, season int) error
}

// RefreshWork re-pulls the bound work's native titles and its episode title
// list straight from the provider, overwriting the cached rows. provider is
// "bgm" (mediaKind "subject") or "tmdb" (mediaKind "tv"|"movie"); season is
// the TMDB season whose episode list is re-pulled (BGM has one implicit
// season and ignores it). Failures are typed Business errors: the refresh
// fails closed, never with a partial silent answer.
func (r ConfiguredWorkRefresher) RefreshWork(ctx context.Context, provider, mediaKind, workID string, season int) error {
	provider, mediaKind, workID = strings.ToLower(strings.TrimSpace(provider)), strings.ToLower(strings.TrimSpace(mediaKind)), strings.TrimSpace(workID)
	if workID == "" {
		return &apperrors.Business{Status: 409, Code: "METADATA_NOT_BOUND", Message: "The anime has no metadata binding to refresh."}
	}
	switch provider {
	case "bgm":
		if mediaKind != "subject" {
			return fmt.Errorf("BGM refresh requires the subject media kind, got %q", mediaKind)
		}
		return r.refreshBGM(ctx, workID)
	case "tmdb":
		if mediaKind != "tv" && mediaKind != "movie" {
			return fmt.Errorf("TMDB refresh requires the tv or movie media kind, got %q", mediaKind)
		}
		return r.refreshTMDB(ctx, mediaKind, workID, season)
	default:
		return fmt.Errorf("unsupported metadata refresh provider %q", provider)
	}
}

// refreshBGM re-pulls the subject (GET /v0/subjects/{id}) and its episode
// list (season 0: one subject is the whole scope).
func (r ConfiguredWorkRefresher) refreshBGM(ctx context.Context, workID string) error {
	endpoint, client, err := r.bgmRequest(ctx)
	if err != nil {
		return err
	}
	native, err := fetchBGMSubject(ctx, client, endpoint, workID)
	if err != nil {
		return err
	}
	if r.Titles != nil {
		if err := r.Titles.RecordWorkTitles(ctx, []identitymetadatadomain.WorkTitles{native}); err != nil {
			return &apperrors.Business{Status: 409, Code: "METADATA_REFRESH_FAILED", Message: "The re-pulled Bangumi titles could not be stored; nothing was changed."}
		}
	}
	if r.EpisodeTitles != nil {
		if err := r.EpisodeTitles.RefreshWork(ctx, episodetitle.SourceBGM, workID, 0); err != nil {
			return refreshFailed("Bangumi", err)
		}
	}
	return nil
}

// refreshTMDB re-pulls the tv/movie work and, for a tv binding, the episode
// list of its bound season (season <= 0 with no bound season reads season 1,
// the same default the matcher's season resolution starts from).
func (r ConfiguredWorkRefresher) refreshTMDB(ctx context.Context, mediaKind, workID string, season int) error {
	endpoint, client, apiKey, err := r.tmdbRequest(ctx)
	if err != nil {
		return err
	}
	native, err := fetchTMDBWork(ctx, client, endpoint, apiKey, mediaKind, workID)
	if err != nil {
		return err
	}
	if r.Titles != nil {
		if err := r.Titles.RecordWorkTitles(ctx, []identitymetadatadomain.WorkTitles{native}); err != nil {
			return &apperrors.Business{Status: 409, Code: "METADATA_REFRESH_FAILED", Message: "The re-pulled TMDB titles could not be stored; nothing was changed."}
		}
	}
	if mediaKind == "tv" && r.EpisodeTitles != nil {
		if season < 1 {
			season = 1
		}
		if err := r.EpisodeTitles.RefreshWork(ctx, episodetitle.SourceTMDB, workID, season); err != nil {
			return refreshFailed("TMDB", err)
		}
	}
	return nil
}

// refreshFailed is the typed offline answer of a failed re-pull.
func refreshFailed(provider string, err error) error {
	message := provider + " could not be reached for the re-pull; the cached rows are unchanged (" + err.Error() + ")."
	return &apperrors.Business{Status: 409, Code: "METADATA_REFRESH_FAILED", Message: message}
}

func (r ConfiguredWorkRefresher) bgmRequest(ctx context.Context) (*url.URL, *http.Client, error) {
	if r.Connections == nil {
		return nil, nil, errors.New("BGM connection source is not configured")
	}
	connections, err := r.Connections.ListConnections(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list BGM connections: %w", err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionBGM)
	if err != nil {
		return nil, nil, refreshFailed("Bangumi", err)
	}
	endpoint := strings.TrimSpace(connection.Endpoint)
	if endpoint == "" {
		endpoint = "https://api.bgm.tv"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, nil, refreshFailed("Bangumi", errors.New("the BGM endpoint is invalid"))
	}
	client := r.Client
	if r.ClientResolver != nil {
		resolved, resolveErr := r.ClientResolver.ClientForConnection(ctx, connection)
		if resolveErr != nil {
			return nil, nil, refreshFailed("Bangumi", resolveErr)
		}
		if resolved != nil {
			client = resolved
		}
	}
	return parsed, client, nil
}

func (r ConfiguredWorkRefresher) tmdbRequest(ctx context.Context) (*url.URL, *http.Client, string, error) {
	if r.Connections == nil {
		return nil, nil, "", errors.New("TMDB connection source is not configured")
	}
	connections, err := r.Connections.ListConnections(ctx)
	if err != nil {
		return nil, nil, "", fmt.Errorf("list TMDB connections: %w", err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionTMDB)
	if err != nil {
		return nil, nil, "", refreshFailed("TMDB", err)
	}
	apiKey := ""
	if r.Secrets != nil && strings.TrimSpace(connection.SecretRef) != "" {
		apiKey, _ = r.Secrets.Resolve(connection.SecretRef)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, nil, "", &apperrors.Business{Status: 409, Code: "TMDB_CREDENTIAL_NOT_CONFIGURED", Message: "TMDB API key is not configured; set it in 设置 → 连接 before refreshing TMDB metadata."}
	}
	parsed, err := url.Parse(strings.TrimSpace(connection.Endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, nil, "", refreshFailed("TMDB", errors.New("the TMDB endpoint is invalid"))
	}
	client := r.Client
	if r.ClientResolver != nil {
		resolved, resolveErr := r.ClientResolver.ClientForConnection(ctx, connection)
		if resolveErr != nil {
			return nil, nil, "", refreshFailed("TMDB", resolveErr)
		}
		if resolved != nil {
			client = resolved
		}
	}
	return parsed, client, apiKey, nil
}

// fetchBGMSubject reads one subject's native titles.
func fetchBGMSubject(ctx context.Context, client *http.Client, endpoint *url.URL, workID string) (identitymetadatadomain.WorkTitles, error) {
	if !isDigits(workID) {
		return identitymetadatadomain.WorkTitles{}, refreshFailed("Bangumi", errors.New("the BGM binding is not a numeric subject id"))
	}
	subject := *endpoint
	subject.Path = strings.TrimRight(subject.Path, "/") + "/v0/subjects/" + workID
	var payload struct {
		Name   string `json:"name"`
		NameCN string `json:"name_cn"`
		Date   string `json:"date"`
	}
	if err := (ConfiguredCandidateSearcher{}).getJSON(ctx, client, &subject, "", &payload); err != nil {
		return identitymetadatadomain.WorkTitles{}, refreshFailed("Bangumi", err)
	}
	return identitymetadatadomain.WorkTitles{
		Provider: "bgm", MediaKind: "subject", WorkID: workID,
		Title: strings.TrimSpace(payload.Name), TitleCN: strings.TrimSpace(payload.NameCN),
		Year: recordedYear(parseYear(payload.Date)),
	}, nil
}

// fetchTMDBWork reads one tv/movie work's zh-CN and original titles, plus the
// overview the work summary fact freezes from.
func fetchTMDBWork(ctx context.Context, client *http.Client, endpoint *url.URL, apiKey, mediaKind, workID string) (identitymetadatadomain.WorkTitles, error) {
	if !isDigits(workID) {
		return identitymetadatadomain.WorkTitles{}, refreshFailed("TMDB", errors.New("the TMDB binding is not a numeric work id"))
	}
	work := *endpoint
	work.Path = strings.TrimRight(work.Path, "/") + "/" + mediaKind + "/" + workID
	params := work.Query()
	if !isBearerToken(apiKey) {
		params.Set("api_key", apiKey)
	}
	params.Set("language", "zh-CN")
	work.RawQuery = params.Encode()
	var payload struct {
		Name          string `json:"name"`
		Title         string `json:"title"`
		OriginalName  string `json:"original_name"`
		OriginalTitle string `json:"original_title"`
		Overview      string `json:"overview"`
		FirstAirDate  string `json:"first_air_date"`
		ReleaseDate   string `json:"release_date"`
	}
	if err := (ConfiguredCandidateSearcher{}).getJSON(ctx, client, &work, apiKey, &payload); err != nil {
		return identitymetadatadomain.WorkTitles{}, refreshFailed("TMDB", err)
	}
	titleCN, original, date := payload.Name, payload.OriginalName, payload.FirstAirDate
	if mediaKind == "movie" {
		titleCN, original, date = payload.Title, payload.OriginalTitle, payload.ReleaseDate
	}
	year := recordedYear(parseYear(date))
	return identitymetadatadomain.WorkTitles{
		Provider: "tmdb", MediaKind: mediaKind, WorkID: workID,
		TitleCN: strings.TrimSpace(titleCN), OriginalTitle: strings.TrimSpace(original),
		Overview: strings.TrimSpace(payload.Overview), Year: year,
	}, nil
}

// TitleRecorder stores work titles (implemented by the persistence Repository).
type TitleRecorder interface {
	RecordWorkTitles(ctx context.Context, titles []identitymetadatadomain.WorkTitles) error
}

// isDigits reports a non-empty all-digit string (a provider numeric id).
func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
