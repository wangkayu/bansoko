package adapter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	acquisitionapplication "bansoko/internal/acquisition/application"
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
	Provider          string
	Connections       ConnectionSource
	ClientResolver    ConnectionHTTPClientResolver
	Recorder          observabilityport.Recorder
	AllowPrivateHosts []string
	Resolver          func(context.Context, string) ([]net.IP, error)
}

// configure resolves the saved connection and its optional network route once
// for every provider surface (season read, keyword search, entry synopsis).
func (r ConfiguredReader) configure(ctx context.Context) (configurationdomain.Connection, *http.Client, error) {
	if r.Connections == nil {
		return configurationdomain.Connection{}, nil, errors.New("catalog connection source is not configured")
	}
	provider := r.Provider
	var kind configurationdomain.ConnectionKind
	switch provider {
	case application.ProviderAniBT:
		kind = configurationdomain.ConnectionAniBT
	case application.ProviderMikan:
		kind = configurationdomain.ConnectionMikan
	default:
		return configurationdomain.Connection{}, nil, fmt.Errorf("unsupported catalog provider %q", provider)
	}
	connections, err := r.Connections.ListConnections(ctx)
	if err != nil {
		return configurationdomain.Connection{}, nil, fmt.Errorf("list %s catalog connections: %w", provider, err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, kind)
	if err != nil {
		// No usable saved connection: fall back to the official public
		// endpoint with a direct route, the same contract BGM/TMDB use. A
		// "multiple enabled connections" conflict is a real configuration
		// error and is never swallowed here.
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return configurationdomain.Connection{}, nil, err
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(kind)
		if !ok {
			return configurationdomain.Connection{}, nil, err
		}
		connection = fallback
	}
	var client *http.Client
	if connection.NetworkRouteID != "" {
		if r.ClientResolver == nil {
			return configurationdomain.Connection{}, nil, errors.New("catalog network route client is not configured")
		}
		client, err = r.ClientResolver.ClientForConnection(ctx, connection)
		if err != nil {
			return configurationdomain.Connection{}, nil, fmt.Errorf("resolve catalog network route: %w", err)
		}
	}
	return connection, client, nil
}

func (r ConfiguredReader) ReadSeason(ctx context.Context, season string, hints domain.ReadHints) (domain.ReadResult, error) {
	connection, client, err := r.configure(ctx)
	if err != nil {
		return domain.ReadResult{}, err
	}
	switch r.Provider {
	case application.ProviderAniBT:
		return (application.HTTPReader{Endpoint: connection.Endpoint, Client: client}).ReadSeason(ctx, season, hints)
	case application.ProviderMikan:
		return (application.MikanHTTPReader{Endpoint: connection.Endpoint, Client: client, Recorder: r.Recorder}).ReadSeason(ctx, season, hints)
	default:
		return domain.ReadResult{}, fmt.Errorf("unsupported catalog provider %q", r.Provider)
	}
}

// Search performs a side-effect-free provider keyword search. Both discovery
// providers expose one; a future provider without one still returns the typed
// not-supported error. The shared SSRF-safe transport is applied here as well as
// on seed search, so a saved endpoint or redirect cannot reach a private host.
func (r ConfiguredReader) Search(ctx context.Context, query string, hints domain.ReadHints) (domain.ReadResult, error) {
	connection, client, err := r.configure(ctx)
	if err != nil {
		return domain.ReadResult{}, err
	}
	client, err = acquisitionapplication.SSRFClientForProvider(client, r.Resolver, r.AllowPrivateHosts, nil)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("%w: configure catalog search transport: %v", application.ErrProviderUpstream, err)
	}
	// Both sites now expose a keyword search: AniBT through its BGM search API and
	// Mikan through the Home/Search recommendation block. A provider without one
	// still answers ErrSearchNotSupported, which the transport maps to 422.
	switch r.Provider {
	case application.ProviderAniBT:
		return (application.HTTPReader{Endpoint: connection.Endpoint, Client: client}).Search(ctx, query, hints)
	case application.ProviderMikan:
		return (application.MikanHTTPReader{Endpoint: connection.Endpoint, Client: client, Recorder: r.Recorder}).Search(ctx, query, hints)
	default:
		return domain.ReadResult{}, fmt.Errorf("unsupported catalog provider %q", r.Provider)
	}
}

// ReadSynopsis reads the synopsis and source-stated facts from the same saved
// provider connection the season and search reads use, behind the same
// SSRF-safe transport.
func (r ConfiguredReader) ReadSynopsis(ctx context.Context, providerEntryRef string) (domain.EntrySynopsis, error) {
	connection, client, err := r.configure(ctx)
	if err != nil {
		return domain.EntrySynopsis{}, err
	}
	client, err = acquisitionapplication.SSRFClientForProvider(client, r.Resolver, r.AllowPrivateHosts, nil)
	if err != nil {
		return domain.EntrySynopsis{}, fmt.Errorf("%w: configure catalog synopsis transport: %v", application.ErrProviderUpstream, err)
	}
	switch r.Provider {
	case application.ProviderAniBT:
		return (application.HTTPReader{Endpoint: connection.Endpoint, Client: client}).ReadSynopsis(ctx, providerEntryRef)
	case application.ProviderMikan:
		return (application.MikanHTTPReader{Endpoint: connection.Endpoint, Client: client, Recorder: r.Recorder}).ReadSynopsis(ctx, providerEntryRef)
	default:
		return domain.EntrySynopsis{}, fmt.Errorf("unsupported catalog provider %q", r.Provider)
	}
}
