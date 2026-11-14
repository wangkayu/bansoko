package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	configurationdomain "bansoko/internal/configuration/domain"
)

// TMDBPosterURL resolves a bound work directly by its provider identity. It
// never performs a title search and returns only the fixed TMDB image CDN.
func (s ConfiguredCandidateSearcher) TMDBPosterURL(ctx context.Context, kind, workID string) (string, error) {
	kind = strings.TrimSpace(kind)
	workID = strings.TrimSpace(workID)
	id, err := strconv.Atoi(workID)
	if (kind != "tv" && kind != "movie") || id <= 0 {
		return "", errors.New("TMDB work identity is invalid")
	}
	if s.Connections == nil {
		return "", errors.New("TMDB connection source is not configured")
	}
	connections, err := s.Connections.ListConnections(ctx)
	if err != nil {
		return "", fmt.Errorf("list TMDB connections: %w", err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionTMDB)
	if err != nil {
		return "", err
	}
	if s.Secrets == nil || strings.TrimSpace(connection.SecretRef) == "" {
		return "", errors.New("TMDB credential is not configured")
	}
	apiKey, ok := s.Secrets.Resolve(connection.SecretRef)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return "", errors.New("TMDB credential could not be resolved")
	}
	client := s.Client
	if s.ClientResolver != nil {
		resolved, resolveErr := s.ClientResolver.ClientForConnection(ctx, connection)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve TMDB network route: %w", resolveErr)
		}
		if resolved != nil {
			client = resolved
		}
	}
	endpoint, err := appendEndpointPath(connection.Endpoint, "/"+kind+"/"+workID)
	if err != nil {
		return "", err
	}
	params := endpoint.Query()
	if !isBearerToken(apiKey) {
		params.Set("api_key", apiKey)
	}
	endpoint.RawQuery = params.Encode()
	var payload tmdbDetailsResponse
	if err := s.getJSON(ctx, client, endpoint, apiKey, &payload); err != nil {
		return "", fmt.Errorf("request TMDB work poster: %w", err)
	}
	if payload.ID != id {
		return "", errors.New("TMDB work response identity does not match")
	}
	posterPath := strings.TrimSpace(payload.PosterPath)
	if len(posterPath) < 2 || len(posterPath) > 512 || !strings.HasPrefix(posterPath, "/") {
		return "", errors.New("TMDB work has no usable poster")
	}
	return (&url.URL{Scheme: "https", Host: "image.tmdb.org", Path: "/t/p/w342" + posterPath}).String(), nil
}
