package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
)

// TestRetiredManagedAnimeRejectsAdmissionCommands proves a retired managed
// anime fails every owner admission command with the typed
// MANAGED_ANIME_RETIRED conflict rather than reviving the owner graph.
func TestRetiredManagedAnimeRejectsAdmissionCommands(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := ownershipSubscribeTestDraft(t, service, "subscribe-key-retired-admission")
	animeID := subscribed.ManagedAnime.Id
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatal(err)
	}
	assertRetired := func(name string, err error) {
		t.Helper()
		var business *apperrors.Business
		if !errors.As(err, &business) || business.Status != 409 || business.Code != "MANAGED_ANIME_RETIRED" {
			t.Fatalf("%s error = %v", name, err)
		}
	}
	_, err := service.RefreshRSSSource(ctx, subscribed.RssSources[0].Id, "retired-refresh")
	assertRetired("refresh", err)
	_, err = service.ReevaluateAnimeReleasesApplication(ctx, animeID)
	assertRetired("re-evaluate", err)
	_, err = service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{ExpectedVersion: subscribed.Subscription.Version, State: "active"})
	assertRetired("resume", err)
}
