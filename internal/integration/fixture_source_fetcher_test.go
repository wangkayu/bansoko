package integration

import (
	"context"
	"testing"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	configurationapplication "bansoko/internal/configuration/application"
	configurationtransport "bansoko/internal/configuration/transport"
	rssapplication "bansoko/internal/rss/application"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// fixtureSourceFetcher is a test-only SourceFetcher that routes one custom RSS
// locator through the real SSRF-safe HTTP fetcher. It preserves the fixture
// semantics of the removed URL-only Fetcher path without requiring seeded
// connection configuration. It is not a production implementation path.
type fixtureSourceFetcher struct {
	fetcher acquisitionapplication.RSSFetcher
}

func (f fixtureSourceFetcher) FetchSource(ctx context.Context, source rssapplication.SourceLocator) ([]acquisitiondomain.FeedItem, error) {
	return f.fetcher.Fetch(ctx, subscriptiondomain.RSSLocator{URL: source.URL})
}

func putGeneratedConnection(t *testing.T, service *configurationapplication.HTTPService, ctx context.Context, id string, expected int64, input generated.ConnectionInput) (generated.Connection, error) {
	t.Helper()
	mapped, err := configurationtransport.ConnectionInput(input, id)
	if err != nil {
		return generated.Connection{}, err
	}
	stored, err := service.PutConnection(ctx, id, expected, mapped)
	if err != nil {
		return generated.Connection{}, err
	}
	return configurationtransport.ConnectionView(stored)
}
