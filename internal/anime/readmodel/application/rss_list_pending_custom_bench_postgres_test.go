package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	rootapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	"bansoko/internal/apperrors"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
)

// A pending (not started) custom subscription's RSS tab is the same test bench
// a provider origin gets: an explicit owner fetch persists one snapshot of its
// saved source under the instance's own scope, and the owner read evaluates the
// owner's configuration over it. The fetch and the reads start nothing: no RSS
// runtime, no work item, no Release or download task. Once started, the owner
// fetch is refused and the read is the ordinary owner view again.
func TestPGPendingCustomOwnerBenchFetchesOnDemand(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	view, services := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	input := readmodelCandidateInput(t, db, readmodelTestDraft(t))
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	input.Draft.Business = &business
	digest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	input.Draft.BusinessDigest = &digest
	candidate, err := services.Draft.SaveAnimeConfigCandidate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := services.Draft.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "custom-bench-activate", draftapplication.CandidateSubscribeInput{Revision: *candidate.CurrentRevision})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if activated.Subscription.State != "pending" {
		t.Fatalf("state = %s, want pending", activated.Subscription.State)
	}
	animeID := activated.ManagedAnime.ID

	repository := catalogpersistence.NewRepository(db)
	catalogService := catalogapplication.NewService(db, nil)
	catalogService.SetFeedSnapshotStore(repository)
	view.seedPreviews = rootapplication.SeedPreviewSourceFromProviderFeedSnapshot(catalogService.ReadProviderEntryRSSByIdentity, catalogService.ReadProviderEntrySeedPayload)
	fetcher := &customPipelineFetcher{}
	var _ catalogapplication.CustomSnapshotOwnerReader = services.Draft
	app := catalogapplication.CustomSnapshotApplication{Revisions: services.Draft, Owners: services.Draft, Fetcher: fetcher, Snapshots: repository}

	before := animetest.ProductRowCounts(t, db)
	// Before any fetch the bench lists the single custom group as needing a
	// snapshot; opening it reads only.
	empty, err := view.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID})
	if err != nil {
		t.Fatalf("pending custom owner read: %v", err)
	}
	if !strings.HasPrefix(empty.ConfigIdentity, "owner_bench:") || len(empty.Groups) != 1 || empty.Groups[0].Feed.State != RSSListFeedSnapshotRequired || len(empty.Groups[0].Items) != 0 {
		t.Fatalf("bench before fetch = %+v", empty)
	}
	if fetcher.count() != 0 {
		t.Fatal("opening the bench fetched the feed")
	}

	result, err := app.FetchOwner(ctx, animeID)
	if err != nil {
		t.Fatalf("owner fetch: %v", err)
	}
	if fetcher.count() != 1 || result.Provider != "custom" || result.ProviderEntryRef != empty.Groups[0].Scope.ProviderEntryRef || result.GroupRef != empty.Groups[0].Scope.ProviderGroupRef {
		t.Fatalf("owner fetch result = %+v (calls %d), group scope = %+v", result, fetcher.count(), empty.Groups[0].Scope)
	}
	ref := PreviewFeedSnapshotRef{Provider: "custom", ProviderEntryRef: result.ProviderEntryRef, ProviderGroupRef: result.GroupRef, SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, ContentHash: result.ContentHash}
	bench, err := view.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID, Feed: []PreviewFeedSnapshotRef{ref}})
	if err != nil {
		t.Fatalf("pending custom bench read: %v", err)
	}
	if len(bench.Groups) != 1 || len(bench.Groups[0].Items) != 2 || bench.ConfigIdentity != empty.ConfigIdentity {
		t.Fatalf("bench after fetch = %+v", bench)
	}
	for _, item := range bench.Groups[0].Items {
		if item.DownloadAction.Allowed || item.DownloadAction.Kind != RSSListDownloadNone || item.DownloadAction.ReasonCode != rssListReasonNotStarted {
			t.Fatalf("bench row admits a download: %+v", item.DownloadAction)
		}
	}
	// An unsaved rules patch previews over the same snapshot.
	patch := rulesPreviewWithExclude(t, "Test")
	if _, err := view.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner_draft", ManagedAnimeID: animeID, RulesPreview: &patch, Feed: []PreviewFeedSnapshotRef{ref}}); err != nil {
		t.Fatalf("pending custom owner_draft read: %v", err)
	}
	if after := animetest.ProductRowCounts(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("the bench fetch/read wrote product rows: before=%v after=%v", before, after)
	}

	// Started: the bench scope is gone and the owner fetch persists nothing.
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET state = 'active', version = version + 1 WHERE id = $1`, activated.Subscription.ID); err != nil {
		t.Fatal(err)
	}
	_, err = app.FetchOwner(ctx, animeID)
	var business409 *apperrors.Business
	if !errors.As(err, &business409) || business409.Code != "CUSTOM_SNAPSHOT_SCOPE_INVALID" {
		t.Fatalf("started owner fetch = %v, want CUSTOM_SNAPSHOT_SCOPE_INVALID", err)
	}
	if fetcher.count() != 1 {
		t.Fatal("a started owner fetched the feed")
	}
	owner, err := view.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID})
	if err != nil {
		t.Fatalf("active owner read: %v", err)
	}
	if strings.HasPrefix(owner.ConfigIdentity, "owner_bench:") {
		t.Fatalf("an active custom owner was read as the bench: %s", owner.ConfigIdentity)
	}
}
