package application

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"bansoko/internal/acquisition/domain"
)

// ErrFeedNotModified is returned by the feed fetchers when the request carried
// validators and the upstream answered 304 Not Modified. No items are read.
var ErrFeedNotModified = errors.New("RSS feed not modified")

// FeedExchange is the optional per-refresh exchange between a feed consumer and
// the shared feed fetchers (custom RSS, AniBT, Mikan). The consumer supplies
// the stored validators and a torrent-metadata reuse hook; the fetcher records
// the validators of the response. A fetch without an exchange behaves exactly
// as before: unconditional request, metadata fetched for every item.
type FeedExchange struct {
	// ETag / LastModified are the stored validators sent as If-None-Match /
	// If-Modified-Since. Empty means unconditional.
	ETag         string
	LastModified string
	// ReuseTorrentMetadata is consulted before an item's .torrent is fetched.
	// Returning true means the item already carries trusted metadata (or does
	// not need it) and must not be fetched again.
	ReuseTorrentMetadata func(domain.FeedItem) (domain.FeedItem, bool)

	// ResponseETag / ResponseLastModified are the validators of the answer.
	ResponseETag         string
	ResponseLastModified string
}

type feedExchangeKey struct{}

// WithFeedExchange attaches exchange to ctx for the next feed fetch.
func WithFeedExchange(ctx context.Context, exchange *FeedExchange) context.Context {
	return context.WithValue(ctx, feedExchangeKey{}, exchange)
}

func feedExchangeFrom(ctx context.Context) *FeedExchange {
	exchange, _ := ctx.Value(feedExchangeKey{}).(*FeedExchange)
	return exchange
}

func applyFeedValidators(ctx context.Context, request *http.Request) {
	exchange := feedExchangeFrom(ctx)
	if exchange == nil {
		return
	}
	if value := strings.TrimSpace(exchange.ETag); value != "" {
		request.Header.Set("If-None-Match", value)
	}
	if value := strings.TrimSpace(exchange.LastModified); value != "" {
		request.Header.Set("If-Modified-Since", value)
	}
}

// recordFeedResponse stores the response validators and reports whether the
// answer is a 304 to a conditional request.
func recordFeedResponse(ctx context.Context, response *http.Response) bool {
	exchange := feedExchangeFrom(ctx)
	if exchange == nil {
		return false
	}
	exchange.ResponseETag = boundedValidator(response.Header.Get("ETag"), 512)
	exchange.ResponseLastModified = boundedValidator(response.Header.Get("Last-Modified"), 128)
	conditional := strings.TrimSpace(exchange.ETag) != "" || strings.TrimSpace(exchange.LastModified) != ""
	return conditional && response.StatusCode == http.StatusNotModified
}

func boundedValidator(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return ""
	}
	return value
}

func reuseTorrentMetadata(ctx context.Context, item *domain.FeedItem) bool {
	exchange := feedExchangeFrom(ctx)
	if exchange == nil || exchange.ReuseTorrentMetadata == nil {
		return false
	}
	reused, ok := exchange.ReuseTorrentMetadata(*item)
	if ok {
		*item = reused
	}
	return ok
}
