package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	configurationdomain "bansoko/internal/configuration/domain"
	observabilityport "bansoko/internal/observability/port"
	providerdomain "bansoko/internal/provider/domain"
)

type ConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

type ConnectionHTTPClientResolver interface {
	ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error)
}

// ConfiguredGroupReader resolves the current saved connection on every call,
// so a successful connection Save affects future refreshes without restart.
type ConfiguredGroupReader struct {
	Provider       providerdomain.Kind
	Connections    ConnectionSource
	ClientResolver ConnectionHTTPClientResolver
	Recorder       observabilityport.Recorder
}

func (r ConfiguredGroupReader) ReadGroups(ctx context.Context, catalogEntryRef string) (providerdomain.GroupReadResult, error) {
	selected, err := resolveProviderConnection(ctx, r.Connections, r.Provider)
	if err != nil {
		return providerdomain.GroupReadResult{}, err
	}
	client, err := resolveConnectionClient(ctx, r.ClientResolver, selected)
	if err != nil {
		return providerdomain.GroupReadResult{}, err
	}
	switch r.Provider {
	case providerdomain.ProviderAniBT:
		return (AniBTGroupReader{Endpoint: selected.Endpoint, Client: client}).ReadGroups(ctx, catalogEntryRef)
	case providerdomain.ProviderMikan:
		return (MikanGroupReader{Endpoint: selected.Endpoint, Client: client, Recorder: r.Recorder}).ReadGroups(ctx, catalogEntryRef)
	default:
		return providerdomain.GroupReadResult{}, fmt.Errorf("unsupported provider %q", r.Provider)
	}
}

// resolveProviderConnection returns the single enabled saved connection for one
// provider kind. With no usable saved connection it falls back to the official
// public endpoint with a direct route, the same contract BGM/TMDB and the
// catalog ConfiguredReader use: AniBT/Mikan group reads stay available on a
// fresh instance without a manually created row. A "multiple enabled
// connections" conflict is a real configuration error and is never swallowed.
func resolveProviderConnection(ctx context.Context, connections ConnectionSource, provider providerdomain.Kind) (configurationdomain.Connection, error) {
	if connections == nil {
		return configurationdomain.Connection{}, errors.New("provider connection source is not configured")
	}
	wantKind := configurationdomain.ConnectionKind(provider)
	saved, err := connections.ListConnections(ctx)
	if err != nil {
		return configurationdomain.Connection{}, fmt.Errorf("list provider connections: %w", err)
	}
	selected, err := configurationdomain.SelectSingleEnabledConnection(saved, wantKind)
	if err != nil {
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return configurationdomain.Connection{}, err
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(wantKind)
		if !ok {
			return configurationdomain.Connection{}, err
		}
		selected = fallback
	}
	return selected, nil
}

// resolveConnectionClient resolves the network-route client of one connection.
// A connection without a saved route keeps the direct client (nil), which the
// readers turn into the direct transport.
func resolveConnectionClient(ctx context.Context, resolver ConnectionHTTPClientResolver, connection configurationdomain.Connection) (*http.Client, error) {
	if connection.NetworkRouteID == "" {
		return nil, nil
	}
	if resolver == nil {
		return nil, errors.New("provider network route client is not configured")
	}
	client, err := resolver.ClientForConnection(ctx, connection)
	if err != nil {
		return nil, fmt.Errorf("resolve provider network route: %w", err)
	}
	return client, nil
}
