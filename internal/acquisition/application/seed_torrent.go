package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	configurationdomain "bansoko/internal/configuration/domain"
)

// ConfiguredSeedTorrentFetcher is the production lazy-expansion adapter. Lazy
// expansion (browsing a persisted feed/search snapshot and opening one seed's
// file list) is a request about one source's upstream, so it must leave through
// the same saved connection and network route that source's catalog, feed and
// search reads use: a direct dial would bypass an operator's proxy.
//
// It owns only connection/egress selection. The request itself stays with
// HTTPTorrentMetadataReader, so the SSRF-safe transport, the redirect limit and
// the size limit are unchanged from the one-shot acquisition path.
type ConfiguredSeedTorrentFetcher struct {
	Connections       ProviderFeedConnectionSource
	ClientResolver    ProviderFeedHTTPClientResolver
	Resolver          func(context.Context, string) ([]net.IP, error)
	AllowPrivateHosts []string
}

// FetchPayload fetches one seed's torrent of one snapshot source through that
// source's connection. A source whose connection selects no network route is
// fetched directly, exactly like before routing existed, and so is any source
// this adapter cannot select a connection for.
func (f ConfiguredSeedTorrentFetcher) FetchPayload(ctx context.Context, provider, locator string) (TorrentPayload, error) {
	client, err := f.routeClient(ctx, provider)
	if err != nil {
		return TorrentPayload{}, err
	}
	reader := HTTPTorrentMetadataReader{Client: client, AllowPrivateHosts: f.AllowPrivateHosts, Resolver: f.Resolver}
	if strings.EqualFold(strings.TrimSpace(provider), string(configurationdomain.ConnectionJackett)) {
		reader.AllowPrivateNetwork = true
	}
	return reader.FetchPayload(ctx, locator)
}

// routeClient resolves the http.Client that must carry one provider's request,
// or nil for a direct fetch. The rules are the provider adapters' rules: one
// enabled connection of the provider's kind wins, no enabled connection falls
// back to the official code default (which carries no route), and an ambiguous
// configuration is a real error instead of a silent choice.
//
// A custom snapshot is not routed here: its connection is part of the frozen RSS
// source locator, which a persisted custom snapshot identity does not carry (it
// stores the hashed source/group scope and the feed facts only), so no
// connection can be selected from the identity alone. Such a fetch stays direct,
// which is exactly how it behaved before seed expansion resolved routes at all.
func (f ConfiguredSeedTorrentFetcher) routeClient(ctx context.Context, provider string) (*http.Client, error) {
	kind, routable := seedTorrentConnectionKind(provider)
	if !routable {
		return nil, nil
	}
	if f.Connections == nil {
		return nil, errors.New("seed torrent connection source is not configured")
	}
	connections, err := f.Connections.ListConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("list %s connections: %w", strings.ToLower(strings.TrimSpace(provider)), err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, kind)
	if err != nil {
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return nil, err
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(kind)
		if !ok {
			return nil, err
		}
		connection = fallback
	}
	if connection.NetworkRouteID == "" {
		return nil, nil
	}
	if f.ClientResolver == nil {
		return nil, errors.New("seed torrent network route client is not configured")
	}
	client, err := f.ClientResolver.ClientForConnection(ctx, connection)
	if err != nil {
		return nil, fmt.Errorf("resolve seed torrent network route: %w", err)
	}
	return client, nil
}

// seedTorrentConnectionKind maps a snapshot provider onto the saved connection
// kind whose egress must carry its requests. A search snapshot names the search
// provider that produced it, and that provider's connection is the same one its
// search request used.
func seedTorrentConnectionKind(provider string) (configurationdomain.ConnectionKind, bool) {
	kind := configurationdomain.ConnectionKind(strings.ToLower(strings.TrimSpace(provider)))
	switch kind {
	case configurationdomain.ConnectionAniBT, configurationdomain.ConnectionMikan, configurationdomain.ConnectionNyaa, configurationdomain.ConnectionJackett:
		return kind, true
	default:
		return "", false
	}
}
