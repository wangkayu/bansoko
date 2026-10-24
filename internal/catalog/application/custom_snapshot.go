package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/apperrors"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// FrozenCustomSnapshotSource is resolved by the candidate owner under revision
// and expected-version checks. HTTP callers never supply a free-standing URL.
type FrozenCustomSnapshotSource struct{ Identity, GroupRef, GroupName, Locator string }
type CustomSnapshotRevisionReader interface {
	ResolveCustomSnapshotSource(context.Context, string, int64, int64) (FrozenCustomSnapshotSource, error)
}

// CustomSnapshotOwnerReader resolves the single custom RSS source of a pending
// custom subscription (scope = OwnerCustomSnapshotIdentity) and re-checks it
// inside the persisting transaction: the subscription must still be pending
// and its source unchanged.
type CustomSnapshotOwnerReader interface {
	ResolveOwnerCustomSnapshotSource(context.Context, string) (FrozenCustomSnapshotSource, error)
	ValidateOwnerCustomSnapshotSourceTx(context.Context, txscope.Tx, string, FrozenCustomSnapshotSource) error
}
type CustomSnapshotFetcher interface {
	FetchCustomSnapshot(context.Context, string) ([]acquisitiondomain.FeedItem, error)
}

// CustomSnapshotApplication uses the same append-only feed snapshot store and
// seed hash/manifest mapper as providers. It owns no background runtime.
type CustomSnapshotApplication struct {
	Revisions CustomSnapshotRevisionReader
	// Owners resolves the custom source of a pending (not started) custom
	// subscription for the owner test bench; nil disables FetchOwner.
	Owners    CustomSnapshotOwnerReader
	Fetcher   CustomSnapshotFetcher
	Snapshots FeedSnapshotStore
	// FreshWindow is the preview fresh window; the catalog service wires its
	// RSS-refresh-interval window, nil falls back to the shipped default.
	FreshWindow func(context.Context) time.Duration
}

func (s CustomSnapshotApplication) Fetch(ctx context.Context, candidateID string, revision, expectedVersion int64) (catalogdomain.ProviderRSSPreviewResult, error) {
	if s.Revisions == nil || s.Fetcher == nil || s.Snapshots == nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: 503, Code: "CUSTOM_FEED_SNAPSHOT_UNAVAILABLE", Message: "Custom feed snapshots are not configured."}
	}
	if strings.TrimSpace(candidateID) == "" || revision < 1 || expectedVersion < 1 {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: 422, Code: "CUSTOM_CANDIDATE_REVISION_REQUIRED", Message: "An exact candidate revision and expected version are required."}
	}
	source, err := s.Revisions.ResolveCustomSnapshotSource(ctx, candidateID, revision, expectedVersion)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	validator, valid := s.Revisions.(CustomSnapshotAtomicRevisionReader)
	return s.fetchAndPersist(ctx, source, valid, func(ctx context.Context, scope txscope.Tx) error {
		return validator.ValidateCustomSnapshotSourceTx(ctx, scope, candidateID, revision, expectedVersion, source)
	})
}

// FetchOwner is the test-bench fetch of a pending custom subscription: the
// owner reader resolves the subscription's single saved custom source (only
// while the subscription has not been started) and re-checks it in the
// persisting transaction. The fetch writes one feed snapshot and nothing
// else: no runtime, no refresh work, no download admission.
func (s CustomSnapshotApplication) FetchOwner(ctx context.Context, managedAnimeID string) (catalogdomain.ProviderRSSPreviewResult, error) {
	if s.Owners == nil || s.Fetcher == nil || s.Snapshots == nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: 503, Code: "CUSTOM_FEED_SNAPSHOT_UNAVAILABLE", Message: "Custom feed snapshots are not configured."}
	}
	managedAnimeID = strings.TrimSpace(managedAnimeID)
	if managedAnimeID == "" {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	source, err := s.Owners.ResolveOwnerCustomSnapshotSource(ctx, managedAnimeID)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	return s.fetchAndPersist(ctx, source, true, func(ctx context.Context, scope txscope.Tx) error {
		return s.Owners.ValidateOwnerCustomSnapshotSourceTx(ctx, scope, managedAnimeID, source)
	})
}

// fetchAndPersist fetches the resolved source once and persists its snapshot
// with validate run inside the persisting transaction (no read-then-write
// fallback: the scope check and the INSERT share a transaction).
func (s CustomSnapshotApplication) fetchAndPersist(ctx context.Context, source FrozenCustomSnapshotSource, validatorAvailable bool, validate func(context.Context, txscope.Tx) error) (catalogdomain.ProviderRSSPreviewResult, error) {
	u, err := url.Parse(source.Locator)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || strings.TrimSpace(source.Identity) == "" || strings.TrimSpace(source.GroupName) == "" {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: 422, Code: "CUSTOM_FEED_SOURCE_INVALID", Message: "The frozen custom source is invalid."}
	}
	items, err := s.Fetcher.FetchCustomSnapshot(ctx, source.Locator)
	if err != nil {
		if business := feedFailureBusinessError(err); business != nil {
			return catalogdomain.ProviderRSSPreviewResult{}, business
		}
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: 503, Code: "CUSTOM_FEED_FETCH_FAILED", Message: safeProviderReason(err.Error())}
	}
	seeds, err := mapFeedItemsToSnapshotSeeds(items)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: 422, Code: "CUSTOM_FEED_SNAPSHOT_INVALID", Message: safeProviderReason(err.Error())}
	}
	hash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	atomicStore, ok := s.Snapshots.(CustomSnapshotAtomicStore)
	if !ok || !validatorAvailable {
		return catalogdomain.ProviderRSSPreviewResult{}, errors.New("atomic custom snapshot persistence unavailable")
	}
	now := time.Now().UTC()
	snapshot, err := atomicStore.PersistCustomFeedSnapshot(ctx, catalogdomain.ProviderFeedSnapshotWrite{Provider: "custom", ProviderEntryRef: source.Identity, ProviderGroupRef: source.GroupRef, FetchedAt: now, FreshUntil: now.Add(s.freshWindow(ctx)), ContentHash: hash, HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema, ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema, Seeds: seeds}, validate)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	return mapProviderFeedSnapshot(snapshot), nil
}

func (s CustomSnapshotApplication) freshWindow(ctx context.Context) time.Duration {
	if s.FreshWindow == nil {
		return defaultPreviewFreshWindow
	}
	return s.FreshWindow(ctx)
}
