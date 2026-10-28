package application

import (
	"context"
	"errors"
	"testing"

	draftapplication "bansoko/internal/anime/draft/application"
	"bansoko/internal/apperrors"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

func requireProviderEntrySubscribed(t *testing.T, err error, wantIDs ...string) {
	t.Helper()
	var subscribed *apperrors.ProviderEntryAlreadySubscribed
	if !errors.As(err, &subscribed) {
		t.Fatalf("err = %v, want ProviderEntryAlreadySubscribed", err)
	}
	if len(subscribed.ManagedAnimeIDs) != len(wantIDs) {
		t.Fatalf("existing ids = %v, want %v", subscribed.ManagedAnimeIDs, wantIDs)
	}
	for index, id := range wantIDs {
		if subscribed.ManagedAnimeIDs[index] != id {
			t.Fatalf("existing ids = %v, want %v", subscribed.ManagedAnimeIDs, wantIDs)
		}
	}
}

func activateProviderEntry(t *testing.T, service *ownershipTestServices, entryRef, key string, start bool) (draftapplication.SubscribeResult, error) {
	t.Helper()
	candidate := ownershipSaveCandidate(t, service, ownershipProviderDraft(t, entryRef, "Entry "+entryRef))
	return service.ActivateAnimeConfigCandidate(context.Background(), candidate.Id, candidate.Version, key, draftapplication.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: start})
}

func TestProviderEntryAllowsOnlyOneLiveSubscription(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	first, err := activateProviderEntry(t, service, "mikan-single-live", "single-live-first", true)
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	// A second candidate of the same entry is refused and carries the first id.
	_, err = activateProviderEntry(t, service, "mikan-single-live", "single-live-second", true)
	requireProviderEntrySubscribed(t, err, first.ManagedAnime.ID)
	// Direct subscribe is guarded the same way.
	_, err = service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: ownershipProviderDraft(t, "mikan-single-live", "Entry direct")}, "single-live-direct")
	requireProviderEntrySubscribed(t, err, first.ManagedAnime.ID)
	var subscriptions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM subscriptions WHERE origin_entry_ref = 'mikan-single-live'`).Scan(&subscriptions); err != nil || subscriptions != 1 {
		t.Fatalf("refused subscribes left %d subscriptions, err %v; want 1", subscriptions, err)
	}

	// Another entry is unaffected.
	if _, err := activateProviderEntry(t, service, "mikan-other-entry", "single-live-other", true); err != nil {
		t.Fatalf("other entry: %v", err)
	}

	// The first instance's key still replays its original result.
	candidate := ownershipSaveCandidate(t, service, ownershipProviderDraft(t, "mikan-replay-entry", "Replay"))
	input := draftapplication.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true}
	original, err := service.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "single-live-replay", input)
	if err != nil {
		t.Fatalf("replay original: %v", err)
	}
	replayed, err := service.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "single-live-replay", input)
	if err != nil || replayed.ManagedAnime.ID != original.ManagedAnime.ID {
		t.Fatalf("replay = %+v, %v; want the original %s", replayed.ManagedAnime, err, original.ManagedAnime.ID)
	}
}

func TestProviderEntryPendingAndStoppedInstancesStillBlock(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	pending, err := activateProviderEntry(t, service, "mikan-pending-blocks", "pending-blocks-first", false)
	if err != nil {
		t.Fatalf("pending subscribe: %v", err)
	}
	_, err = activateProviderEntry(t, service, "mikan-pending-blocks", "pending-blocks-second", true)
	requireProviderEntrySubscribed(t, err, pending.ManagedAnime.ID)

	started, err := activateProviderEntry(t, service, "mikan-stopped-blocks", "stopped-blocks-first", true)
	if err != nil {
		t.Fatalf("started subscribe: %v", err)
	}
	if _, err := service.PatchAnimeSubscription(ctx, started.ManagedAnime.ID, SubscriptionPatchInput{ExpectedVersion: started.Subscription.Version, State: subscriptiondomain.SubscriptionStopped}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	_, err = activateProviderEntry(t, service, "mikan-stopped-blocks", "stopped-blocks-second", true)
	requireProviderEntrySubscribed(t, err, started.ManagedAnime.ID)
}

func TestProviderEntryCanBeSubscribedAgainAfterRetirement(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	first, err := activateProviderEntry(t, service, "mikan-resubscribe", "resubscribe-first", true)
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	if err := service.DeleteAnimeSubscription(ctx, first.ManagedAnime.ID, SubscriptionDeleteInput{ExpectedVersion: first.Subscription.Version}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	second, err := activateProviderEntry(t, service, "mikan-resubscribe", "resubscribe-second", true)
	if err != nil {
		t.Fatalf("subscribe after retirement: %v", err)
	}
	if second.ManagedAnime.ID == first.ManagedAnime.ID {
		t.Fatal("re-subscribe must create a new instance")
	}
}

func TestProviderEntryDuplicateAndCustomOriginAreNotGuarded(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	first, err := activateProviderEntry(t, service, "mikan-duplicate-allowed", "duplicate-allowed-first", true)
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	copied, err := service.Draft.DuplicateAnimeSubscription(ctx, first.ManagedAnime.ID, draftapplication.DuplicateSubscriptionInput{InstanceLabel: "Second"}, "duplicate-allowed-copy")
	if err != nil || copied.ManagedAnime.ID == first.ManagedAnime.ID {
		t.Fatalf("duplicate of a live provider instance = %+v, %v", copied.ManagedAnime, err)
	}
	// With two live instances the refusal names both.
	_, err = activateProviderEntry(t, service, "mikan-duplicate-allowed", "duplicate-allowed-third", true)
	requireProviderEntrySubscribed(t, err, first.ManagedAnime.ID, copied.ManagedAnime.ID)

	// Custom origin carries no entry identity: the same preset subscribes twice.
	ownershipSubscribeTestDraft(t, service, "custom-origin-first")
	ownershipSubscribeTestDraft(t, service, "custom-origin-second")
}
