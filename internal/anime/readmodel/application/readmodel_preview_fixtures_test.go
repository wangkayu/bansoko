package application

import (
	"context"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
	providerdomain "bansoko/internal/provider/domain"
)

// This file holds the read model's own preview fixtures. A read-model test may
// not use another bucket's test support, so these are copies of the oneshot
// doubles (fakeSeedPreviewSource, fixedCatalogFeedFetcher); only the shared
// vocabulary they speak comes from the kernel's preview port.

// readmodelPreviewSource is a test double for the kernel's SeedPreviewSource
// port. It returns a configured snapshot or error for every call and counts the
// reads so a test can assert that a saved preview never fetches more identities
// than the caller asked for.
type readmodelPreviewSource struct {
	snapshot     SeedPreviewSnapshot
	err          error
	calls        int
	payload      SeedPayload
	payloadErr   error
	payloadCalls int
	lastIdentity animecatalogdomain.ProviderRSSPreviewIdentity
	lastProvider string
	lastEntryRef string
	lastGroupRef string
	lastSeedKey  string
}

func (f *readmodelPreviewSource) FetchEntryGroupSnapshotByID(_ context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (SeedPreviewSnapshot, error) {
	f.calls++
	f.lastIdentity = identity
	f.lastProvider, f.lastEntryRef, f.lastGroupRef = provider, entryRef, groupRef
	return f.snapshot, f.err
}

func (f *readmodelPreviewSource) ReadSeedPayload(_ context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (SeedPayload, error) {
	f.payloadCalls++
	f.lastIdentity = identity
	f.lastProvider, f.lastEntryRef, f.lastGroupRef = provider, entryRef, groupRef
	f.lastSeedKey = stableSeedKey
	return f.payload, f.payloadErr
}

// readmodelCatalogFeedFetcher serves the fixed provider items one retained
// catalog snapshot was ingested from.
type readmodelCatalogFeedFetcher struct {
	items []acquisitiondomain.FeedItem
}

func (f readmodelCatalogFeedFetcher) FetchProviderFeed(context.Context, providerdomain.FeedLocator) ([]acquisitiondomain.FeedItem, error) {
	return f.items, nil
}
