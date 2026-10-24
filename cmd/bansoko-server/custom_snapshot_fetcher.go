package main

import (
	acquisition "bansoko/internal/acquisition/application"
	domain "bansoko/internal/acquisition/domain"
	subscription "bansoko/internal/subscription/domain"
	"context"
)

type customSnapshotNetworkFetcher struct {
	fetcher acquisition.ConfiguredCustomRSSFetcher
}

func (f customSnapshotNetworkFetcher) FetchCustomSnapshot(ctx context.Context, locator string) ([]domain.FeedItem, error) {
	return f.fetcher.Fetch(ctx, subscription.RSSLocator{URL: locator})
}
