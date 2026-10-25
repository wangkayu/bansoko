package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"bansoko/internal/catalog/application"
	"bansoko/internal/catalog/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	observabilityport "bansoko/internal/observability/port"
)

type ConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

type ConnectionHTTPClientResolver interface {
	ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error)
}

// ConfiguredReader resolves the current saved provider connection for every
// catalog refresh. Mikan/AniBT mirror the BGM/TMDB model: a missing or
// ambiguous connection is not fatal, the official public endpoint and a
// direct client apply. A saved enabled connection still overrides the
// default, including its base URL and egress route.
type ConfiguredReader struct {
	Provider       string
	Connections    ConnectionSource
	ClientResolver ConnectionHTTPClientResolver
	Recorder       observabilityport.Recorder
}

func (r ConfiguredReader) ReadSeason(ctx context.Context, season string, hints domain.ReadHints) (domain.ReadResult, error) {
	if r.Connections == nil {
		return domain.ReadResult{}, errors.New("catalog connection source is not configured")
	}
	provider := r.Provider
	var kind configurationdomain.ConnectionKind
	switch provider {
	case application.ProviderAniBT:
		kind = configurationdomain.ConnectionAniBT
	case application.ProviderMikan:
		kind = configurationdomain.ConnectionMikan
	default:
		return domain.ReadResult{}, fmt.Errorf("unsupported catalog provider %q", provider)
	}
	connections, err := r.Connections.ListConnections(ctx)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("list %s catalog connections: %w", provider, err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, kind)
	if err != nil {
		// No usable saved connection: fall back to the official public
		// endpoint with a direct route, the same contract BGM/TMDB use. A
		// "multiple enabled connections" conflict is a real configuration
		// error and is never swallowed here.
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return domain.ReadResult{}, err
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(kind)
		if !ok {
			return domain.ReadResult{}, err
		}
		connection = fallback
	}
	var client *http.Client
	if connection.NetworkRouteID != "" {
		if r.ClientResolver == nil {
			return domain.ReadResult{}, errors.New("catalog network route client is not configured")
		}
		client, err = r.ClientResolver.ClientForConnection(ctx, connection)
		if err != nil {
			return domain.ReadResult{}, fmt.Errorf("resolve catalog network route: %w", err)
		}
	}
	switch provider {
	case application.ProviderAniBT:
		return (application.HTTPReader{Endpoint: connection.Endpoint, Client: client}).ReadSeason(ctx, season, hints)
	case application.ProviderMikan:
		return (application.MikanHTTPReader{Endpoint: connection.Endpoint, Client: client, Recorder: r.Recorder}).ReadSeason(ctx, season, hints)
	default:
		return domain.ReadResult{}, fmt.Errorf("unsupported catalog provider %q", provider)
	}
}

// Search performs a side-effect-free provider keyword search. Only AniBT
// exposes a public search endpoint; Mikan has no public search surface, so it
// returns a typed not-supported error that the transport maps to 422.
func (r ConfiguredReader) Search(ctx context.Context, query string, hints domain.ReadHints) (domain.ReadResult, error) {
	if r.Connections == nil {
		return domain.ReadResult{}, errors.New("catalog connection source is not configured")
	}
	provider := r.Provider
	var kind configurationdomain.ConnectionKind
	switch provider {
	case application.ProviderAniBT:
		kind = configurationdomain.ConnectionAniBT
	case application.ProviderMikan:
		return domain.ReadResult{}, application.ErrSearchNotSupported
	default:
		return domain.ReadResult{}, fmt.Errorf("unsupported catalog provider %q", provider)
	}
	connections, err := r.Connections.ListConnections(ctx)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("list %s catalog connections: %w", provider, err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, kind)
	if err != nil {
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return domain.ReadResult{}, err
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(kind)
		if !ok {
			return domain.ReadResult{}, err
		}
		connection = fallback
	}
	var client *http.Client
	if connection.NetworkRouteID != "" {
		if r.ClientResolver == nil {
			return domain.ReadResult{}, errors.New("catalog network route client is not configured")
		}
		client, err = r.ClientResolver.ClientForConnection(ctx, connection)
		if err != nil {
			return domain.ReadResult{}, fmt.Errorf("resolve catalog network route: %w", err)
		}
	}
	return (application.HTTPReader{Endpoint: connection.Endpoint, Client: client}).Search(ctx, query, hints)
}
