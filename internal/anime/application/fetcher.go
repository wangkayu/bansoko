package application

import (
	"context"
	"net"
	"net/http"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// FeedResult is the normalized feed read used by preview.
type FeedResult struct {
	Items []acquisitiondomain.FeedItem
	Parse acquisitiondomain.FeedParseResult
}

// FeedFetcher is the preview feed read port. Preview may perform external
// reads through this port but never writes product state.
type FeedFetcher interface {
	Fetch(ctx context.Context, locatorURL string) (FeedResult, error)
}

// TorrentPayload is one bounded, bencode-validated torrent input retained for
// release-owned persistence. It mirrors acquisitionapplication.TorrentPayload
// without leaking the acquisition package type through the anime application
// boundary.
type TorrentPayload struct {
	Bytes    []byte
	Metadata torrent.Metadata
}

// TorrentPayloadFetcher is the one-shot port for fetching and
// bencode-validating one torrent URL. It reuses the shared SSRF-safe transport
// and size limits; no torrent input row or Release is created.
type TorrentPayloadFetcher interface {
	FetchPayload(ctx context.Context, torrentURL string) (TorrentPayload, error)
}

// torrentPayloadAdapter wraps the acquisition reader so the anime application
// depends on its own port type, not the acquisition package.
type torrentPayloadAdapter struct {
	reader acquisitionapplication.TorrentPayloadReader
}

func (a torrentPayloadAdapter) FetchPayload(ctx context.Context, torrentURL string) (TorrentPayload, error) {
	result, err := a.reader.FetchPayload(ctx, torrentURL)
	if err != nil {
		return TorrentPayload{}, err
	}
	return TorrentPayload{Bytes: result.Bytes, Metadata: result.Metadata}, nil
}

// NewTorrentPayloadFetcher builds the production fetcher backed by the shared
// SSRF-safe HTTP torrent reader. The optional resolver is only for explicit
// development/test fixture host mappings.
func NewTorrentPayloadFetcher(allowPrivateHosts []string, resolvers ...func(context.Context, string) ([]net.IP, error)) TorrentPayloadFetcher {
	var resolver func(context.Context, string) ([]net.IP, error)
	if len(resolvers) > 0 {
		resolver = resolvers[0]
	}
	return torrentPayloadAdapter{reader: acquisitionapplication.HTTPTorrentMetadataReader{AllowPrivateHosts: allowPrivateHosts, Resolver: resolver}}
}

// HTTPFeedFetcher is the production preview fetcher. It reuses the shared
// SSRF-safe RSS fetcher (scheme/credential/redirect/DNS/private-address and
// size validation at every hop). Test-only private host allowances must be
// injected explicitly; production wiring passes none.
type HTTPFeedFetcher struct {
	Client            *http.Client
	MaxSize           int64
	AllowPrivateHosts []string
	Resolver          func(context.Context, string) ([]net.IP, error)
}

func (f HTTPFeedFetcher) Fetch(ctx context.Context, locatorURL string) (FeedResult, error) {
	result, err := (acquisitionapplication.HTTPRSSFetcher{
		Client:            f.Client,
		MaxSize:           f.MaxSize,
		Resolver:          f.Resolver,
		AllowPrivateHosts: f.AllowPrivateHosts,
	}).FetchDetailed(ctx, subscriptiondomain.RSSLocator{URL: locatorURL})
	if err != nil {
		return FeedResult{}, err
	}
	return FeedResult{Items: result.Items, Parse: result.Parse}, nil
}
