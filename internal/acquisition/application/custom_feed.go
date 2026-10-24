package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"bansoko/internal/acquisition/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// CustomRSSConnectionSource is the narrow configuration read port used by
// custom RSS. A custom locator may name one HTTP feed connection, but the
// fetcher never selects a connection implicitly from the available list.
type CustomRSSConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

// CustomRSSHTTPClientResolver resolves the saved network route selected by a
// custom feed connection. The acquisition adapter never reads route tables.
type CustomRSSHTTPClientResolver interface {
	ClientForConnection(context.Context, configurationdomain.Connection) (*http.Client, error)
}

// ConfiguredCustomRSSFetcher is the production custom-RSS adapter. It owns
// only connection/egress selection and delegates XML normalization, SSRF
// protection and torrent metadata enrichment to the shared HTTPRSSFetcher.
type ConfiguredCustomRSSFetcher struct {
	Connections          CustomRSSConnectionSource
	ClientResolver       CustomRSSHTTPClientResolver
	AllowPrivateHosts    []string
	Resolver             func(context.Context, string) ([]net.IP, error)
	MaxSize              int64
	ParseTorrentMetadata bool
}

func (f ConfiguredCustomRSSFetcher) Fetch(ctx context.Context, locator subscriptiondomain.RSSLocator) ([]domain.FeedItem, error) {
	return usableItems(f.FetchDetailed(ctx, locator))
}

func (f ConfiguredCustomRSSFetcher) FetchDetailed(ctx context.Context, locator subscriptiondomain.RSSLocator) (RSSFetchResult, error) {
	normalized, err := locator.Normalize()
	if err != nil {
		return RSSFetchResult{}, err
	}
	client, err := f.resolveClient(ctx, normalized)
	if err != nil {
		return RSSFetchResult{}, err
	}
	var torrentReader TorrentMetadataReader
	if f.ParseTorrentMetadata {
		torrentReader = HTTPTorrentMetadataReader{Client: client, AllowPrivateHosts: f.AllowPrivateHosts, Resolver: f.Resolver}
	}
	return (HTTPRSSFetcher{
		Client:            client,
		MaxSize:           f.MaxSize,
		Resolver:          f.Resolver,
		AllowPrivateHosts: f.AllowPrivateHosts,
		TorrentReader:     torrentReader,
	}).FetchDetailed(ctx, normalized)
}

// FetchTorrentPayload fetches one .torrent of the custom feed through the
// same connection and network route the feed itself uses. The explicit
// Release retry uses it for a Release whose torrent was never fetched.
func (f ConfiguredCustomRSSFetcher) FetchTorrentPayload(ctx context.Context, locator subscriptiondomain.RSSLocator, torrentURL string) (TorrentPayload, error) {
	normalized, err := locator.Normalize()
	if err != nil {
		return TorrentPayload{}, err
	}
	client, err := f.resolveClient(ctx, normalized)
	if err != nil {
		return TorrentPayload{}, err
	}
	return (HTTPTorrentMetadataReader{Client: client, AllowPrivateHosts: f.AllowPrivateHosts, Resolver: f.Resolver}).FetchPayload(ctx, torrentURL)
}

func (f ConfiguredCustomRSSFetcher) resolveClient(ctx context.Context, locator subscriptiondomain.RSSLocator) (*http.Client, error) {
	connectionRef := strings.TrimSpace(locator.HTTPFeedConnection)
	routeRef := strings.TrimSpace(locator.NetworkRoute)
	if connectionRef == "" && routeRef == "" {
		return nil, nil
	}

	var connection configurationdomain.Connection
	if connectionRef != "" {
		if f.Connections == nil {
			return nil, errors.New("custom RSS connection source is not configured")
		}
		connections, err := f.Connections.ListConnections(ctx)
		if err != nil {
			return nil, fmt.Errorf("list custom RSS connections: %w", err)
		}
		found := false
		for _, candidate := range connections {
			if strings.TrimSpace(candidate.ID) != connectionRef {
				continue
			}
			if candidate.Kind != configurationdomain.ConnectionHTTPFeed {
				return nil, fmt.Errorf("custom RSS connection %q is not an rss_http connection", connectionRef)
			}
			if candidate.State == configurationdomain.ConnectionDisabled {
				return nil, fmt.Errorf("custom RSS connection %q is disabled", connectionRef)
			}
			if candidate.State != configurationdomain.ConnectionConfigured {
				return nil, fmt.Errorf("custom RSS connection %q is not configured", connectionRef)
			}
			connection = candidate
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("custom RSS connection %q is not configured", connectionRef)
		}
		if configuredRoute := strings.TrimSpace(connection.NetworkRouteID); routeRef != "" && configuredRoute != "" && configuredRoute != routeRef {
			return nil, fmt.Errorf("custom RSS locator route %q conflicts with connection route %q", routeRef, configuredRoute)
		}
		if routeRef == "" {
			routeRef = strings.TrimSpace(connection.NetworkRouteID)
		}
	}

	if routeRef == "" && !requiresHTTPFeedClient(connection) {
		return nil, nil
	}
	if f.ClientResolver == nil {
		return nil, errors.New("custom RSS network route resolver is not configured")
	}
	if connection.ID == "" {
		connection = configurationdomain.Connection{ID: "custom-rss-route", Kind: configurationdomain.ConnectionHTTPFeed, State: configurationdomain.ConnectionConfigured}
	}
	connection.NetworkRouteID = routeRef
	client, err := f.ClientResolver.ClientForConnection(ctx, connection)
	if err != nil {
		return nil, fmt.Errorf("resolve custom RSS network route: %w", err)
	}
	return client, nil
}

func requiresHTTPFeedClient(connection configurationdomain.Connection) bool {
	if connection.SecretRef != "" {
		return true
	}
	return connection.HTTPFeedConfig != nil && connection.HTTPFeedConfig.AuthMode != configurationdomain.HTTPFeedAuthNone
}
