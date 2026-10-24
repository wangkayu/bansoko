package application

import (
	"context"
	"fmt"
	"net"
	"strings"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	providerdomain "bansoko/internal/provider/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// ConfiguredSourceFetcher resolves one persisted RSS source through the typed
// provider/custom acquisition adapters. It does not infer credentials or route
// settings from the locator URL.
type ConfiguredSourceFetcher struct {
	Connections          acquisitionapplication.CustomRSSConnectionSource
	ClientResolver       acquisitionapplication.CustomRSSHTTPClientResolver
	ParseTorrentMetadata bool
	AllowPrivateHosts    []string
	Resolver             func(context.Context, string) ([]net.IP, error)
}

func (f ConfiguredSourceFetcher) FetchSource(ctx context.Context, source SourceLocator) ([]acquisitiondomain.FeedItem, error) {
	switch source.Kind {
	case animedomain.SourceKindCustomRss:
		return (acquisitionapplication.ConfiguredCustomRSSFetcher{
			Connections:          f.Connections,
			ClientResolver:       f.ClientResolver,
			ParseTorrentMetadata: f.ParseTorrentMetadata,
			AllowPrivateHosts:    f.AllowPrivateHosts,
			Resolver:             f.Resolver,
		}).Fetch(ctx, subscriptionLocator(source))
	case animedomain.SourceKindProviderGroup:
		providerKind := providerdomain.Kind(strings.TrimSpace(source.Provider))
		groupRef := strings.TrimSpace(source.ProviderGroupRef)
		if prefix := string(providerKind) + ":"; strings.HasPrefix(groupRef, prefix) {
			groupRef = strings.TrimPrefix(groupRef, prefix)
		}
		provider, err := providerdomain.NewGroupRef(providerKind, groupRef)
		if err != nil {
			return nil, fmt.Errorf("provider group source is invalid: %w", err)
		}
		return (acquisitionapplication.ConfiguredProviderFeedFetcher{
			Connections:          f.Connections,
			ClientResolver:       f.ClientResolver,
			ParseTorrentMetadata: f.ParseTorrentMetadata,
			AllowPrivateHosts:    f.AllowPrivateHosts,
			Resolver:             f.Resolver,
		}).FetchProviderFeed(ctx, providerdomain.FeedLocator{
			Provider:        provider.Provider,
			CatalogEntryRef: source.ProviderEntryRef,
			Group:           provider,
		})
	default:
		return nil, fmt.Errorf("unsupported RSS source kind %q", source.Kind)
	}
}

// FetchSourceTorrent fetches one .torrent of a source through the source's own
// connection and network route (custom connection, or the provider's).
func (f ConfiguredSourceFetcher) FetchSourceTorrent(ctx context.Context, source SourceLocator, torrentURL string) (acquisitionapplication.TorrentPayload, error) {
	switch source.Kind {
	case animedomain.SourceKindCustomRss:
		return (acquisitionapplication.ConfiguredCustomRSSFetcher{
			Connections:       f.Connections,
			ClientResolver:    f.ClientResolver,
			AllowPrivateHosts: f.AllowPrivateHosts,
			Resolver:          f.Resolver,
		}).FetchTorrentPayload(ctx, subscriptionLocator(source), torrentURL)
	case animedomain.SourceKindProviderGroup:
		providerKind := providerdomain.Kind(strings.TrimSpace(source.Provider))
		groupRef := strings.TrimSpace(source.ProviderGroupRef)
		if prefix := string(providerKind) + ":"; strings.HasPrefix(groupRef, prefix) {
			groupRef = strings.TrimPrefix(groupRef, prefix)
		}
		provider, err := providerdomain.NewGroupRef(providerKind, groupRef)
		if err != nil {
			return acquisitionapplication.TorrentPayload{}, fmt.Errorf("provider group source is invalid: %w", err)
		}
		return (acquisitionapplication.ConfiguredProviderFeedFetcher{
			Connections:       f.Connections,
			ClientResolver:    f.ClientResolver,
			AllowPrivateHosts: f.AllowPrivateHosts,
			Resolver:          f.Resolver,
		}).FetchTorrentPayload(ctx, providerdomain.FeedLocator{
			Provider:        provider.Provider,
			CatalogEntryRef: source.ProviderEntryRef,
			Group:           provider,
		}, torrentURL)
	default:
		return acquisitionapplication.TorrentPayload{}, fmt.Errorf("unsupported RSS source kind %q", source.Kind)
	}
}

func subscriptionLocator(source SourceLocator) subscriptiondomain.RSSLocator {
	return subscriptiondomain.RSSLocator{
		URL:                source.URL,
		HTTPFeedConnection: source.HTTPFeedConnection,
		NetworkRoute:       source.NetworkRoute,
	}
}

var (
	_ SourceFetcher        = ConfiguredSourceFetcher{}
	_ SourceTorrentFetcher = ConfiguredSourceFetcher{}
)
