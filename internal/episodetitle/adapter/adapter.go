// Package adapter implements episodetitle.Client and SeasonClient over the
// TMDB HTTP API.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/episodetitle"
	hardeningtransport "bansoko/internal/hardening/transport"
	"bansoko/internal/provider/outbound"
	"bansoko/internal/runtime/buildinfo"
)

// ConnectionSource lists configured provider connections.
type ConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

// SecretResolver resolves a connection secret reference.
type SecretResolver interface {
	Resolve(string) (string, bool)
}

// HTTPClientResolver returns the egress client for a connection's route.
type HTTPClientResolver interface {
	ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error)
}

// TMDBClient fetches TMDB seasons and season episode lists through the single
// enabled TMDB connection.
type TMDBClient struct {
	Connections    ConnectionSource
	Secrets        SecretResolver
	ClientResolver HTTPClientResolver
	Client         *http.Client
}

var _ episodetitle.Client = TMDBClient{}

// tmdbRequest is a prepared TMDB request base: the egress client, the
// resolved key and the connection endpoint.
type tmdbRequest struct {
	client   *http.Client
	apiKey   string
	endpoint string
}

func (c TMDBClient) prepare(ctx context.Context) (tmdbRequest, error) {
	if c.Connections == nil {
		return tmdbRequest{}, providerError(episodetitle.SourceTMDB, episodetitle.LookupUnconfigured, errors.New("TMDB connection source is not configured"))
	}
	connections, err := c.Connections.ListConnections(ctx)
	if err != nil {
		return tmdbRequest{}, configurationError(ctx, fmt.Errorf("list TMDB connections: %w", err))
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionTMDB)
	if err != nil {
		return tmdbRequest{}, connectionError(ctx, episodetitle.SourceTMDB, err)
	}
	apiKey := ""
	if c.Secrets != nil && strings.TrimSpace(connection.SecretRef) != "" {
		apiKey, _ = c.Secrets.Resolve(connection.SecretRef)
	}
	if strings.TrimSpace(apiKey) == "" {
		return tmdbRequest{}, providerError(episodetitle.SourceTMDB, episodetitle.LookupUnconfigured, errors.New("TMDB API key is not configured"))
	}
	client, err := resolveClient(ctx, c.Client, c.ClientResolver, connection)
	if err != nil {
		return tmdbRequest{}, configurationError(ctx, fmt.Errorf("resolve TMDB network route: %w", err))
	}
	return tmdbRequest{client: client, apiKey: apiKey, endpoint: connection.Endpoint}, nil
}

func (r tmdbRequest) get(ctx context.Context, suffix, language string, target any) error {
	endpoint, err := appendEndpointPath(r.endpoint, suffix)
	if err != nil {
		return fmt.Errorf("resolve TMDB endpoint: %w", err)
	}
	params := endpoint.Query()
	if !isBearerToken(r.apiKey) {
		params.Set("api_key", r.apiKey)
	}
	if language == "" {
		language = episodetitle.DefaultTMDBLanguage
	}
	params.Set("language", language)
	endpoint.RawQuery = params.Encode()
	return getJSON(ctx, episodetitle.SourceTMDB, r.client, endpoint, r.apiKey, target)
}

// Episodes implements episodetitle.Client.
func (c TMDBClient) Episodes(ctx context.Context, key episodetitle.Key) (episodetitle.List, error) {
	workID := strings.TrimSpace(key.WorkID)
	if !isDigits(workID) || key.Season < 0 {
		return episodetitle.List{}, errors.New("TMDB episode lookup requires a numeric work id and a non-negative season")
	}
	request, err := c.prepare(ctx)
	if err != nil {
		return episodetitle.List{}, err
	}
	var payload struct {
		Episodes []struct {
			EpisodeNumber int    `json:"episode_number"`
			Name          string `json:"name"`
			AirDate       string `json:"air_date"`
			Runtime       *int   `json:"runtime"`
			StillPath     string `json:"still_path"`
			Overview      string `json:"overview"`
		} `json:"episodes"`
	}
	if err := request.get(ctx, "/tv/"+workID+"/season/"+strconv.Itoa(key.Season), key.Language, &payload); err != nil {
		return episodetitle.List{}, fmt.Errorf("request TMDB season episodes: %w", err)
	}
	episodes := make([]episodetitle.Episode, 0, len(payload.Episodes))
	for _, episode := range payload.Episodes {
		if episode.EpisodeNumber < 0 {
			continue
		}
		runtime := episode.Runtime
		if runtime != nil && *runtime < 0 {
			runtime = nil
		}
		episodes = append(episodes, episodetitle.Episode{
			Number: episode.EpisodeNumber, Title: strings.TrimSpace(episode.Name),
			AirDate: strings.TrimSpace(episode.AirDate), RuntimeMinutes: runtime,
			StillURL: tmdbStillURL(episode.StillPath), Overview: strings.TrimSpace(episode.Overview),
		})
	}
	return episodetitle.List{Episodes: episodes}, nil
}

var _ episodetitle.SeasonClient = TMDBClient{}

// Seasons implements episodetitle.SeasonClient: the seasons of a TMDB TV
// show, season 0 (specials) included.
func (c TMDBClient) Seasons(ctx context.Context, workID string) ([]episodetitle.Season, error) {
	workID = strings.TrimSpace(workID)
	if !isDigits(workID) {
		return nil, errors.New("TMDB season lookup requires a numeric work id")
	}
	request, err := c.prepare(ctx)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Seasons []struct {
			SeasonNumber int    `json:"season_number"`
			Name         string `json:"name"`
			EpisodeCount int    `json:"episode_count"`
			AirDate      string `json:"air_date"`
		} `json:"seasons"`
	}
	if err := request.get(ctx, "/tv/"+workID, "", &payload); err != nil {
		return nil, fmt.Errorf("request TMDB seasons: %w", err)
	}
	seasons := make([]episodetitle.Season, 0, len(payload.Seasons))
	for _, season := range payload.Seasons {
		if season.SeasonNumber < 0 {
			continue
		}
		seasons = append(seasons, episodetitle.Season{
			Number: season.SeasonNumber, Name: strings.TrimSpace(season.Name), EpisodeCount: season.EpisodeCount, AirDate: strings.TrimSpace(season.AirDate),
		})
	}
	return seasons, nil
}

func resolveClient(ctx context.Context, fallback *http.Client, resolver HTTPClientResolver, connection configurationdomain.Connection) (*http.Client, error) {
	if resolver == nil {
		return fallback, nil
	}
	resolved, err := resolver.ClientForConnection(ctx, connection)
	if err != nil {
		return nil, err
	}
	if resolved == nil {
		return fallback, nil
	}
	return resolved, nil
}

// getJSON GETs endpoint and decodes a 2xx JSON body into target. HTTP 404
// maps to episodetitle.ErrNotFound. Errors never carry the request URL (which
// may hold an api_key) or the Authorization header.
func getJSON(ctx context.Context, source episodetitle.Source, client *http.Client, endpoint *url.URL, apiKey string, target any) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, Transport: hardeningtransport.DirectTransport()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errors.New("build provider request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", buildinfo.UserAgent("episode-title"))
	if isBearerToken(apiKey) {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return providerError(source, episodetitle.LookupUnreachable, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("provider returned HTTP 404: %w", episodetitle.ErrNotFound)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &episodetitle.LookupError{Source: source, Kind: episodetitle.LookupUnreachable, Cause: fmt.Errorf("provider returned HTTP %d", response.StatusCode)}
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			failure.Kind = episodetitle.LookupCredentialsRejected
		case http.StatusTooManyRequests:
			failure.Kind = episodetitle.LookupRateLimited
			failure.RetryAfter, _ = outbound.ParseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		}
		return failure
	}
	const maxResponse = 4 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return providerError(source, episodetitle.LookupUnreachable, err)
	}
	if len(body) > maxResponse {
		return providerError(source, episodetitle.LookupInvalidResponse, errors.New("provider response is too large"))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return providerError(source, episodetitle.LookupInvalidResponse, fmt.Errorf("decode provider response: %w", err))
	}
	return nil
}

func providerError(source episodetitle.Source, kind episodetitle.LookupErrorKind, cause error) error {
	return &episodetitle.LookupError{Source: source, Kind: kind, Cause: cause}
}

func configurationError(ctx context.Context, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &episodetitle.ConfigurationError{Cause: cause}
}

func connectionError(ctx context.Context, source episodetitle.Source, cause error) error {
	if errors.Is(cause, configurationdomain.ErrNoEnabledConnection) {
		return providerError(source, episodetitle.LookupUnconfigured, cause)
	}
	return configurationError(ctx, cause)
}

func appendEndpointPath(raw, suffix string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("metadata provider endpoint is invalid")
	}
	base := strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/configuration")
	parsed.Path = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(suffix, "/")
	return parsed, nil
}

// tmdbStillBaseURL is the CDN rendition of one episode still. The cover proxy
// registers the resulting URL and serves the bytes from the API origin.
const tmdbStillBaseURL = "https://image.tmdb.org/t/p/w780"

// tmdbStillURL expands a provider still path into the address the cover proxy
// registers. An empty path is no still at all.
func tmdbStillURL(path string) string {
	path = strings.TrimLeft(strings.TrimSpace(path), "/")
	if path == "" {
		return ""
	}
	return tmdbStillBaseURL + "/" + path
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isBearerToken detects TMDB v4 read-access tokens (JWT, prefix "eyJ"); v3 API
// keys use ?api_key= instead.
func isBearerToken(apiKey string) bool {
	return strings.HasPrefix(apiKey, "eyJ")
}
