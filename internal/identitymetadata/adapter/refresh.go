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
// season and ignores it). The boolean reports a confirmed cache write even
// when a later step fails.
func (r ConfiguredWorkRefresher) RefreshWork(ctx context.Context, provider, mediaKind, workID string, season int) (bool, error) {
	provider, mediaKind, workID = strings.ToLower(strings.TrimSpace(provider)), strings.ToLower(strings.TrimSpace(mediaKind)), strings.TrimSpace(workID)
	if workID == "" {
		return false, &apperrors.Business{Status: 409, Code: "METADATA_NOT_BOUND", Message: "The anime has no metadata binding to refresh."}
	}
	switch provider {
	case "bgm":
		if mediaKind != "subject" {
			return false, fmt.Errorf("BGM refresh requires the subject media kind, got %q", mediaKind)
		}
		endpoint, client, err := r.bgmRequest(ctx)
		if err != nil {
			return false, err
		}
		native, err := fetchBGMSubject(ctx, client, endpoint, workID)
		if err != nil {
			return false, err
		}
		return r.storeRefreshedTitles(ctx, "Bangumi", native, episodetitle.SourceBGM, 0)
	case "tmdb":
		if mediaKind != "tv" && mediaKind != "movie" {
			return false, fmt.Errorf("TMDB refresh requires the tv or movie media kind, got %q", mediaKind)
		}
		endpoint, client, apiKey, err := r.tmdbRequest(ctx)
		if err != nil {
			return false, err
		}
		native, err := fetchTMDBWork(ctx, client, endpoint, apiKey, mediaKind, workID)
		if err != nil {
			return false, err
		}
		if mediaKind == "movie" {
			return r.storeRefreshedTitles(ctx, "TMDB", native, "", 0)
		}
		return r.storeRefreshedTitles(ctx, "TMDB", native, episodetitle.SourceTMDB, max(1, season))
	default:
		return false, fmt.Errorf("unsupported metadata refresh provider %q", provider)
	}
}

func (r ConfiguredWorkRefresher) storeRefreshedTitles(ctx context.Context, provider string, native identitymetadatadomain.WorkTitles, source episodetitle.Source, season int) (bool, error) {
	written := false
	if r.Titles != nil {
		if err := r.Titles.RecordWorkTitles(ctx, []identitymetadatadomain.WorkTitles{native}); err != nil {
			return false, refreshFailed("Could not confirm that the " + provider + " work titles were saved.")
		}
		written = true
	}
	if source != "" && r.EpisodeTitles != nil {
		if err := r.EpisodeTitles.RefreshWork(ctx, source, native.WorkID, season); err != nil {
			message := "Could not refresh the " + provider + " episode titles."
			if written {
				message = "The " + provider + " work titles were saved, but the episode titles could not be refreshed."
			}
			return written, refreshFailed(message)
		}
		written = true
	}
	return written, nil
}

func refreshFailed(message string) error {
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
		return nil, nil, refreshFailed("Could not read the Bangumi work metadata.")
	}
	endpoint := strings.TrimSpace(connection.Endpoint)
	if endpoint == "" {
		endpoint = "https://api.bgm.tv"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, nil, refreshFailed("Could not read the Bangumi work metadata.")
	}
	client := r.Client
	if r.ClientResolver != nil {
		resolved, resolveErr := r.ClientResolver.ClientForConnection(ctx, connection)
		if resolveErr != nil {
			return nil, nil, refreshFailed("Could not read the Bangumi work metadata.")
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
		return nil, nil, "", refreshFailed("Could not read the TMDB work metadata.")
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
		return nil, nil, "", refreshFailed("Could not read the TMDB work metadata.")
	}
	client := r.Client
	if r.ClientResolver != nil {
		resolved, resolveErr := r.ClientResolver.ClientForConnection(ctx, connection)
		if resolveErr != nil {
			return nil, nil, "", refreshFailed("Could not read the TMDB work metadata.")
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
		return identitymetadatadomain.WorkTitles{}, refreshFailed("Could not read the Bangumi work metadata.")
	}
	subject := *endpoint
	subject.Path = strings.TrimRight(subject.Path, "/") + "/v0/subjects/" + workID
	var payload struct {
		Name   string `json:"name"`
		NameCN string `json:"name_cn"`
		Date   string `json:"date"`
	}
	if err := (ConfiguredCandidateSearcher{}).getJSON(ctx, client, &subject, "", &payload); err != nil {
		return identitymetadatadomain.WorkTitles{}, refreshFailed("Could not read the Bangumi work metadata.")
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
		return identitymetadatadomain.WorkTitles{}, refreshFailed("Could not read the TMDB work metadata.")
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
		return identitymetadatadomain.WorkTitles{}, refreshFailed("Could not read the TMDB work metadata.")
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
