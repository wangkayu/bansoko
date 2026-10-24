package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	rootapplication "bansoko/internal/anime/application"
	"bansoko/internal/apperrors"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
)

// A custom draft's RSS tab is the same bench a provider draft and a pending
// owner get: the saved preset's explicit fetch persists one snapshot of its
// source under the preset revision's scope, and the current_input read of the
// (possibly unsaved) draft, scoped by that revision, evaluates the draft over
// it. Opening the bench reads only; an unsaved source edit reads no snapshot.
func TestPGCustomDraftBenchReadsTheSavedPresetSnapshot(t *testing.T) {
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
	revision := *candidate.CurrentRevision
	draft := input.Draft

	repository := catalogpersistence.NewRepository(db)
	catalogService := catalogapplication.NewService(db, nil)
	catalogService.SetFeedSnapshotStore(repository)
	view.seedPreviews = rootapplication.SeedPreviewSourceFromProviderFeedSnapshot(catalogService.ReadProviderEntryRSSByIdentity, catalogService.ReadProviderEntrySeedPayload)
	fetcher := &customPipelineFetcher{}
	app := catalogapplication.CustomSnapshotApplication{Revisions: services.Draft, Fetcher: fetcher, Snapshots: repository}

	before := animetest.ProductRowCounts(t, db)
	scoped := ReadRSSListViewInput{InputSource: "current_input", Draft: &draft, CandidateID: candidate.Id, Revision: revision}
	empty, err := view.ReadAnimeRSSListView(ctx, scoped)
	if err != nil {
		t.Fatalf("custom draft bench read: %v", err)
	}
	if len(empty.Groups) != 1 || empty.Groups[0].Feed.State != RSSListFeedSnapshotRequired || !strings.Contains(empty.ConfigIdentity, "#custom:") {
		t.Fatalf("bench before fetch = %+v", empty)
	}
	if fetcher.count() != 0 {
		t.Fatal("opening the bench fetched the feed")
	}

	result, err := app.Fetch(ctx, candidate.Id, revision, candidate.Version)
	if err != nil {
		t.Fatalf("candidate fetch: %v", err)
	}
	if result.ProviderEntryRef != empty.Groups[0].Scope.ProviderEntryRef || result.GroupRef != empty.Groups[0].Scope.ProviderGroupRef {
		t.Fatalf("fetched scope %s/%s, bench group scope %+v", result.ProviderEntryRef, result.GroupRef, empty.Groups[0].Scope)
	}
	ref := PreviewFeedSnapshotRef{Provider: "custom", ProviderEntryRef: result.ProviderEntryRef, ProviderGroupRef: result.GroupRef, SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, ContentHash: result.ContentHash}
	scoped.Feed = []PreviewFeedSnapshotRef{ref}
	bench, err := view.ReadAnimeRSSListView(ctx, scoped)
	if err != nil {
		t.Fatalf("custom draft bench read with snapshot: %v", err)
	}
	if len(bench.Groups) != 1 || len(bench.Groups[0].Items) != 2 {
		t.Fatalf("bench after fetch = %+v", bench)
	}

	// An unsaved offset edit still reads the saved source's snapshot.
	edited := draft
	edited.RssSources = append(edited.RssSources[:0:0], draft.RssSources...)
	edited.RssSources[0].EpisodeOffset = 2
	if _, err := view.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "current_input", Draft: &edited, CandidateID: candidate.Id, Revision: revision, Feed: scoped.Feed}); err != nil {
		t.Fatalf("unsaved offset edit: %v", err)
	}
	// An unsaved source edit names another source: the saved snapshot is not its.
	moved := draft
	moved.RssSources = append(moved.RssSources[:0:0], draft.RssSources...)
	moved.RssSources[0].Locator = "https://elsewhere.example.test/rss"
	_, err = view.ReadAnimeRSSListView(ctx, ReadRSSListViewInput{InputSource: "current_input", Draft: &moved, CandidateID: candidate.Id, Revision: revision, Feed: scoped.Feed})
	var business422 *apperrors.Business
	if !errors.As(err, &business422) || business422.Code != "FEED_SNAPSHOT_IDENTITY_INVALID" {
		t.Fatalf("unsaved source edit read = %v, want FEED_SNAPSHOT_IDENTITY_INVALID", err)
	}
	if fetcher.count() != 1 {
		t.Fatal("bench reads fetched the feed")
	}
	if after := animetest.ProductRowCounts(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("the bench fetch/read wrote product rows: before=%v after=%v", before, after)
	}
}
