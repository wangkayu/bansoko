package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"bansoko/internal/acquisition/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	providerdomain "bansoko/internal/provider/domain"
)

type ProviderFeedConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

type ProviderFeedHTTPClientResolver interface {
	ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error)
}

type ConfiguredProviderFeedFetcher struct {
	Connections    ProviderFeedConnectionSource
	ClientResolver ProviderFeedHTTPClientResolver
	Resolver       func(context.Context, string) ([]net.IP, error)
	// ParseTorrentMetadata is enabled by the production composition root. It
	// remains opt-in for fixture/unit adapters so a feed test does not perform
	// an undeclared second network request.
	ParseTorrentMetadata bool
	// AllowPrivateHosts is test-fixture-only dependency injection. Production
	// composition leaves it empty so provider feeds retain SSRF protection.
	AllowPrivateHosts []string
}

// FetchProviderFeed returns the usable items, failing when every item was
// skipped, exactly like a custom source.
func (f ConfiguredProviderFeedFetcher) FetchProviderFeed(ctx context.Context, locator providerdomain.FeedLocator) ([]domain.FeedItem, error) {
	return usableItems(f.FetchProviderFeedDetailed(ctx, locator))
}

// FetchProviderFeedDetailed keeps the provider's URL construction and
// connection selection; parsing is the declared adapter of that provider's
// site, with the same skip reasons a custom source reports.
func (f ConfiguredProviderFeedFetcher) FetchProviderFeedDetailed(ctx context.Context, locator providerdomain.FeedLocator) (RSSFetchResult, error) {
	if err := locator.Validate(); err != nil {
		return RSSFetchResult{}, err
	}
	connection, client, err := f.resolveConnection(ctx, locator.Provider)
	if err != nil {
		return RSSFetchResult{}, err
	}
	switch locator.Provider {
	case providerdomain.ProviderAniBT:
		var reader TorrentMetadataReader
		if f.ParseTorrentMetadata {
			reader = HTTPTorrentMetadataReader{Client: client, AllowPrivateHosts: f.AllowPrivateHosts, Resolver: f.Resolver}
		}
		return (HTTPAniBTRSSFetcher{Endpoint: connection.Endpoint, Client: client, AllowPrivateHosts: f.AllowPrivateHosts, Resolver: f.Resolver, TorrentReader: reader}).FetchAnimeDetailed(ctx, locator.CatalogEntryRef, locator.Group.ExternalID)
	case providerdomain.ProviderMikan:
		var reader TorrentMetadataReader
		if f.ParseTorrentMetadata {
			reader = HTTPTorrentMetadataReader{Client: client, AllowPrivateHosts: f.AllowPrivateHosts, Resolver: f.Resolver}
		}
		return (HTTPMikanRSSFetcher{Endpoint: connection.Endpoint, Client: client, AllowPrivateHosts: f.AllowPrivateHosts, Resolver: f.Resolver, TorrentReader: reader}).FetchAnimeDetailed(ctx, locator.CatalogEntryRef, locator.Group.ExternalID)
	default:
		return RSSFetchResult{}, fmt.Errorf("unsupported provider feed %q", locator.Provider)
	}
}

// FetchTorrentPayload fetches one .torrent of the provider's feed through the
// same connection and network route the feed itself uses. The explicit
// Release retry uses it for a Release whose torrent was never fetched.
func (f ConfiguredProviderFeedFetcher) FetchTorrentPayload(ctx context.Context, locator providerdomain.FeedLocator, torrentURL string) (TorrentPayload, error) {
	if err := locator.Validate(); err != nil {
		return TorrentPayload{}, err
	}
	_, client, err := f.resolveConnection(ctx, locator.Provider)
	if err != nil {
		return TorrentPayload{}, err
	}
	return (HTTPTorrentMetadataReader{Client: client, AllowPrivateHosts: f.AllowPrivateHosts, Resolver: f.Resolver}).FetchPayload(ctx, torrentURL)
}

func (f ConfiguredProviderFeedFetcher) resolveConnection(ctx context.Context, provider providerdomain.Kind) (configurationdomain.Connection, *http.Client, error) {
	if f.Connections == nil {
		return configurationdomain.Connection{}, nil, errors.New("provider feed connection source is not configured")
	}
	connections, err := f.Connections.ListConnections(ctx)
	if err != nil {
		return configurationdomain.Connection{}, nil, fmt.Errorf("list provider feed connections: %w", err)
	}
	connection, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionKind(provider))
	if err != nil {
		return configurationdomain.Connection{}, nil, err
	}
	var client *http.Client
	if connection.NetworkRouteID != "" {
		if f.ClientResolver == nil {
			return configurationdomain.Connection{}, nil, errors.New("provider feed network route client is not configured")
		}
		client, err = f.ClientResolver.ClientForConnection(ctx, connection)
		if err != nil {
			return configurationdomain.Connection{}, nil, fmt.Errorf("resolve provider feed network route: %w", err)
		}
	}
	return connection, client, nil
}
