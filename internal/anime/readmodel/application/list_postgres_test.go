package application

import (
	"context"
	"testing"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
)

func TestListAnimeKeepsDistinctOwnersWithSameCustomLocator(t *testing.T) {
	db := animetest.OpenTestDB(t)
	readmodel, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	ctx := context.Background()

	first := readmodelSubscribeTestDraft(t, root, "list-independent-first")
	second := readmodelSubscribeTestDraft(t, root, "list-independent-second")
	if first.RssSources[0].LocatorDisplay != second.RssSources[0].LocatorDisplay {
		t.Fatalf("test owners must share the same custom RSS locator: %q vs %q", first.RssSources[0].LocatorDisplay, second.RssSources[0].LocatorDisplay)
	}
	if first.ManagedAnime.Id == second.ManagedAnime.Id {
		t.Fatalf("distinct requests with one locator must have distinct managed owner IDs: %q", first.ManagedAnime.Id)
	}
	if first.Subscription.Id == second.Subscription.Id {
		t.Fatalf("distinct requests with one locator must have distinct subscription IDs: %q", first.Subscription.Id)
	}

	listed, err := readmodel.ListAnimeApplication(ctx)
	if err != nil {
		t.Fatalf("list independent instances: %v", err)
	}
	byID := make(map[string]AnimeListItem, len(listed))
	for _, item := range listed {
		byID[item.ManagedAnime.ID] = item
	}
	firstListed, firstOK := byID[string(first.ManagedAnime.Id)]
	secondListed, secondOK := byID[string(second.ManagedAnime.Id)]
	if !firstOK || !secondOK {
		t.Fatalf("ListAnimeApplication must return both managed IDs: got %v", byID)
	}
	if firstListed.Subscription.ID == secondListed.Subscription.ID || firstListed.Subscription.Version != first.Subscription.Version || secondListed.Subscription.Version != second.Subscription.Version {
		t.Fatalf("ListAnimeApplication lost independent subscription identity/version: first=%+v second=%+v", firstListed.Subscription, secondListed.Subscription)
	}
	if firstListed.Subscription.State != subscriptiondomain.SubscriptionActive || secondListed.Subscription.State != subscriptiondomain.SubscriptionActive {
		t.Fatalf("both freshly subscribed instances must be active: first=%s second=%s", firstListed.Subscription.State, secondListed.Subscription.State)
	}

	patched, err := root.PatchAnimeSubscription(ctx, string(second.ManagedAnime.Id), ownershipapplication.SubscriptionPatchInput{
		ExpectedVersion: secondListed.Subscription.Version,
		State:           subscriptiondomain.SubscriptionStopped,
	})
	if err != nil {
		t.Fatalf("patch second subscription state: %v", err)
	}
	if patched.ManagedAnimeID != string(second.ManagedAnime.Id) || patched.Version != secondListed.Subscription.Version+1 || patched.State != subscriptiondomain.SubscriptionStopped {
		t.Fatalf("second patch returned the wrong owner/version: %+v", patched)
	}

	listed, err = readmodel.ListAnimeApplication(ctx)
	if err != nil {
		t.Fatalf("list instances after second patch: %v", err)
	}
	byID = make(map[string]AnimeListItem, len(listed))
	for _, item := range listed {
		byID[item.ManagedAnime.ID] = item
	}
	firstListed, firstOK = byID[string(first.ManagedAnime.Id)]
	secondListed, secondOK = byID[string(second.ManagedAnime.Id)]
	if !firstOK || !secondOK {
		t.Fatalf("ListAnimeApplication must keep both instances after a state patch: got %v", byID)
	}
	if firstListed.Subscription.State != subscriptiondomain.SubscriptionActive || firstListed.Subscription.Version != first.Subscription.Version {
		t.Fatalf("patching second instance changed first: %+v", firstListed.Subscription)
	}
	if secondListed.Subscription.State != subscriptiondomain.SubscriptionStopped || secondListed.Subscription.Version != patched.Version {
		t.Fatalf("second instance did not reflect the exact patch: %+v", secondListed.Subscription)
	}
}

func TestListAnimeApplicationIncludesRetiredOwner(t *testing.T) {
	db := animetest.OpenTestDB(t)
	readmodel, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	ctx := context.Background()
	subscribed := readmodelSubscribeTestDraft(t, root, "subscribe-key-retired-read")
	animeID := subscribed.ManagedAnime.Id
	if err := root.DeleteAnimeSubscription(ctx, animeID, ownershipapplication.SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatal(err)
	}
	items, err := readmodel.ListAnimeApplication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *AnimeListItem
	for i := range items {
		if items[i].ManagedAnime.ID == animeID {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatal("retired instance disappeared from the anime list")
	}
	if found.Subscription.LifecycleState != "retired" || found.Subscription.LifecycleEpoch != 2 {
		t.Fatalf("retired row lifecycle = %s/%d", found.Subscription.LifecycleState, found.Subscription.LifecycleEpoch)
	}
	if found.Subscription.RetiredAt == nil {
		t.Fatal("retired row lost its retirement timestamp")
	}
	if found.Subscription.State == "retired" {
		t.Fatalf("lifecycle leaked into Subscription.state: %q", found.Subscription.State)
	}
}

// TestListAnimeBroadcastDayMapsProviderScheduleFacts proves the anime list
// exposes the stable BroadcastDay key mapped from the raw provider schedule
// fact the catalog snapshot keeps: Mikan data-dayofweek (0=Sunday … 6=Saturday,
// 7=tbd), the AniBT weekdayLabel (Wednesday / tbd) and a custom subscription without any
// catalog schedule reading as none.
func TestListAnimeBroadcastDayMapsProviderScheduleFacts(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()

	for _, snapshot := range []struct{ id, provider string }{
		{"snapshot-bcast-mikan", "mikan"},
		{"snapshot-bcast-anibt", "anibt"},
	} {
		if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at)
VALUES ($1, $2, '2026-summer', 'fresh', CURRENT_TIMESTAMP, 'test', CURRENT_TIMESTAMP)`, snapshot.id, snapshot.provider); err != nil {
			t.Fatal(err)
		}
	}
	entries := []struct{ id, snapshot, ref, hint string }{
		{"entry-bcast-mikan-0", "snapshot-bcast-mikan", "5001", `{"weekday":"0"}`},
		{"entry-bcast-mikan-3", "snapshot-bcast-mikan", "5003", `{"weekday":"3"}`},
		{"entry-bcast-mikan-7", "snapshot-bcast-mikan", "5007", `{"weekday":"7"}`},
		{"entry-bcast-anibt-wed", "snapshot-bcast-anibt", "6003", `{"weekdayLabel":"周三"}`},
		{"entry-bcast-anibt-tbd", "snapshot-bcast-anibt", "6007", `{"weekdayLabel":"未定"}`},
	}
	for _, entry := range entries {
		if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_entries(id, snapshot_id, provider, provider_entry_ref, title, source_hint, state, observed_at)
VALUES ($1, $2, (SELECT provider FROM provider_catalog_snapshots WHERE id=$2), $3, $3, $4, 'present', CURRENT_TIMESTAMP)`,
			entry.id, entry.snapshot, entry.ref, entry.hint); err != nil {
			t.Fatal(err)
		}
	}
	subs := []struct{ anime, sub, origin, ref string }{
		{"anime-bcast-mikan-0", "subscription-bcast-mikan-0", "mikan", "5001"},
		{"anime-bcast-mikan-3", "subscription-bcast-mikan-3", "mikan", "5003"},
		{"anime-bcast-mikan-7", "subscription-bcast-mikan-7", "mikan", "5007"},
		{"anime-bcast-anibt-wed", "subscription-bcast-anibt-wed", "anibt", "6003"},
		{"anime-bcast-anibt-tbd", "subscription-bcast-anibt-tbd", "anibt", "6007"},
		{"anime-bcast-custom", "subscription-bcast-custom", "custom", ""},
	}
	for _, sub := range subs {
		if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, $1)`, sub.anime); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state)
VALUES ($1, $2, $3, NULLIF($4, ''), $1, 'active')`, sub.sub, sub.anime, sub.origin, sub.ref); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES ($1)`, sub.anime); err != nil {
			t.Fatal(err)
		}
	}

	service := readmodelNewTestService(t, db, nil)
	items, err := service.ListAnimeApplication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AnimeListItem{}
	for _, item := range items {
		byID[item.ManagedAnime.ID] = item
	}
	want := map[string]string{
		"anime-bcast-mikan-0":   "sun",
		"anime-bcast-mikan-3":   "wed",
		"anime-bcast-mikan-7":   "none", // 7 is the movie section, not a day
		"anime-bcast-anibt-wed": "wed",
		"anime-bcast-anibt-tbd": "tbd",
		"anime-bcast-custom":    "none",
	}
	for animeID, key := range want {
		item, ok := byID[animeID]
		if !ok {
			t.Fatalf("%s missing from the anime list", animeID)
		}
		if item.BroadcastDay != key {
			t.Errorf("%s broadcast_day = %q, want %q", animeID, item.BroadcastDay, key)
		}
	}
}
