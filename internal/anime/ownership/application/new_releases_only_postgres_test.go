package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

func requireNewReleasesOnlyLocked(t *testing.T, err error, what string) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 409 || business.Code != "NEW_RELEASES_ONLY_LOCKED" {
		t.Fatalf("%s: err=%v, want 409 NEW_RELEASES_ONLY_LOCKED", what, err)
	}
}

// New releases only: the draft option is frozen at
// subscribe with the creation time as baseline; while pending it can be
// turned off and on again (the baseline stays the creation time); once
// started it is immutable at the API and in the database.
func TestNewReleasesOnlyFrozenAtCreationAndLockedAfterStart(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	draft := ownershipTestDraft(t)
	on := true
	draft.NewReleasesOnly = &on
	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: draft}, "subscribe-key-new-only-0001")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID, subscriptionID := subscribed.ManagedAnime.Id, subscribed.Subscription.Id
	if !subscribed.Subscription.NewReleasesOnly || subscribed.Subscription.StartBaselineAt == nil {
		t.Fatalf("subscribe result must carry the frozen option: %+v", subscribed.Subscription)
	}
	baselineIsCreation := func() bool {
		t.Helper()
		var ok bool
		if err := db.QueryRowContext(ctx, `SELECT new_releases_only AND start_baseline_at = created_at FROM subscriptions WHERE id = $1`, subscriptionID).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !baselineIsCreation() {
		t.Fatal("the baseline must be the instance creation time")
	}

	off, err := service.SetNewReleasesOnly(ctx, animeID, NewReleasesOnlyInput{ExpectedVersion: 1, Enabled: false})
	if err != nil {
		t.Fatalf("turn off while pending: %v", err)
	}
	if off.NewReleasesOnly || off.StartBaselineAt != nil || off.Version != 2 {
		t.Fatalf("off result = %+v", off)
	}
	// Turned on later while pending: the baseline is still the creation time.
	if _, err := db.ExecContext(ctx, `SELECT pg_sleep(0.01)`); err != nil {
		t.Fatal(err)
	}
	again, err := service.SetNewReleasesOnly(ctx, animeID, NewReleasesOnlyInput{ExpectedVersion: 2, Enabled: true})
	if err != nil {
		t.Fatalf("turn on again while pending: %v", err)
	}
	if !again.NewReleasesOnly || again.StartBaselineAt == nil || !again.StartBaselineAt.Equal(*subscribed.Subscription.StartBaselineAt) {
		t.Fatalf("turning the option on later must keep the creation baseline: %+v", again)
	}
	if !baselineIsCreation() {
		t.Fatal("the baseline moved away from the creation time")
	}
	_, err = service.SetNewReleasesOnly(ctx, animeID, NewReleasesOnlyInput{ExpectedVersion: 2, Enabled: false})
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("stale version: err=%v, want a version conflict", err)
	}

	started, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{ExpectedVersion: 3, State: subscriptiondomain.SubscriptionActive})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !started.NewReleasesOnly || started.StartBaselineAt == nil {
		t.Fatalf("start result must keep the option: %+v", started)
	}
	_, err = service.SetNewReleasesOnly(ctx, animeID, NewReleasesOnlyInput{ExpectedVersion: started.Version, Enabled: false})
	requireNewReleasesOnlyLocked(t, err, "change after start")
	if _, err := db.ExecContext(ctx, `UPDATE subscriptions SET new_releases_only = false, start_baseline_at = NULL WHERE id = $1`, subscriptionID); err == nil {
		t.Fatal("the database must refuse a change after start")
	}
	stopped, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{ExpectedVersion: started.Version, State: subscriptiondomain.SubscriptionStopped})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	_, err = service.SetNewReleasesOnly(ctx, animeID, NewReleasesOnlyInput{ExpectedVersion: stopped.Version, Enabled: false})
	requireNewReleasesOnlyLocked(t, err, "change while stopped")
	if !baselineIsCreation() {
		t.Fatal("start/stop changed the frozen option")
	}
}

// Default off: a draft without the option creates an instance without a
// baseline, and the option can be turned on while pending.
func TestNewReleasesOnlyDefaultsOff(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: ownershipTestDraft(t)}, "subscribe-key-new-only-0002")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if subscribed.Subscription.NewReleasesOnly || subscribed.Subscription.StartBaselineAt != nil {
		t.Fatalf("the option must default off: %+v", subscribed.Subscription)
	}
	on, err := service.SetNewReleasesOnly(ctx, subscribed.ManagedAnime.Id, NewReleasesOnlyInput{ExpectedVersion: 1, Enabled: true})
	if err != nil {
		t.Fatalf("turn on while pending: %v", err)
	}
	var created bool
	if err := db.QueryRowContext(ctx, `SELECT start_baseline_at = created_at FROM subscriptions WHERE id = $1`, subscribed.Subscription.Id).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if !on.NewReleasesOnly || !created {
		t.Fatalf("turning the option on while pending must use the creation time: %+v", on)
	}
}
