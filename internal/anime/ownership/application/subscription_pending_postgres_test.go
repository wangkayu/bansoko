package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	plugintargetport "bansoko/internal/plugintarget/port"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

func requireNotStarted(t *testing.T, err error, what string) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 409 || business.Code != "SUBSCRIPTION_NOT_STARTED" {
		t.Fatalf("%s: err=%v, want 409 SUBSCRIPTION_NOT_STARTED", what, err)
	}
}

// A subscribe without start creates a pending subscription that runs nothing:
// no initial refresh, no started notice, no manual refresh and no stop. Its
// first start is a normal first start (no resume stamp) that schedules the
// initial refresh and admits the started notice; stop -> resume afterwards
// still stamps the resume baseline.
func TestPendingSubscriptionStartsOnlyByStart(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	notices := &recordingNotices{}
	service.SetBusinessDeliveryEnqueuer(notices)
	service.Draft.SetBusinessDeliveryEnqueuer(notices)
	ctx := context.Background()

	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: ownershipTestDraft(t)}, "subscribe-key-pending-0001")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	animeID, subscriptionID := subscribed.ManagedAnime.Id, subscribed.Subscription.Id
	if subscribed.Subscription.State != generated.SubscriptionStatePending || len(subscribed.RssSources) != 1 {
		t.Fatalf("a subscribe without start must be pending with its source: %+v", subscribed)
	}
	sourceID := string(subscribed.RssSources[0].Id)
	refreshes := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE kind = 'rss_refresh' AND owner_id = $1`, sourceID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := refreshes(); n != 0 {
		t.Fatalf("a pending subscribe scheduled %d refreshes", n)
	}
	if got := notices.list(); len(got) != 0 {
		t.Fatalf("a pending subscribe admitted notices: %+v", got)
	}

	_, err = service.RefreshRSSSource(ctx, sourceID, "refresh-pending-0001")
	requireNotStarted(t, err, "manual refresh of a pending source")
	_, err = service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{ExpectedVersion: 1, State: subscriptiondomain.SubscriptionStopped})
	requireNotStarted(t, err, "stop a pending subscription")
	if n := refreshes(); n != 0 {
		t.Fatalf("refused commands scheduled %d refreshes", n)
	}

	started, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{ExpectedVersion: 1, State: subscriptiondomain.SubscriptionActive})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.State != subscriptiondomain.SubscriptionActive || started.Version != 2 {
		t.Fatalf("start result = %+v", started)
	}
	var resumedSet bool
	if err := db.QueryRowContext(ctx, `SELECT resumed_at IS NOT NULL FROM subscriptions WHERE id = $1`, subscriptionID).Scan(&resumedSet); err != nil {
		t.Fatal(err)
	}
	if resumedSet {
		t.Fatal("the first start stamped a resume baseline")
	}
	var trigger string
	if err := db.QueryRowContext(ctx, `SELECT input_snapshot->>'trigger' FROM work_items WHERE kind = 'rss_refresh' AND owner_id = $1`, sourceID).Scan(&trigger); err != nil {
		t.Fatalf("the first start must schedule the initial refresh: %v", err)
	}
	if trigger != "subscription_started" {
		t.Fatalf("initial refresh trigger = %q", trigger)
	}
	var startedLogs int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM subscription_log_entries WHERE subscription_id = $1 AND kind = 'subscription_started'`, subscriptionID).Scan(&startedLogs); err != nil {
		t.Fatal(err)
	}
	if startedLogs != 1 {
		t.Fatalf("started log entries = %d, want 1", startedLogs)
	}
	got := notices.list()
	if len(got) != 1 || got[0].ResultKey != plugintargetport.SubscriptionStarted || got[0].SourceOwnerVersion != 2 {
		t.Fatalf("notices after start = %+v, want one started at version 2", got)
	}

	// Stop -> resume is unchanged: the resume stamps the baseline.
	if _, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{ExpectedVersion: 2, State: subscriptiondomain.SubscriptionStopped}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{ExpectedVersion: 3, State: subscriptiondomain.SubscriptionActive}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT resumed_at IS NOT NULL FROM subscriptions WHERE id = $1`, subscriptionID).Scan(&resumedSet); err != nil {
		t.Fatal(err)
	}
	if !resumedSet {
		t.Fatal("a resume after the first start must stamp the resume baseline")
	}
}

// Subscribing with start is active at once, and a pending subscription can be
// retired through the ordinary unsubscribe path.
func TestSubscribeWithStartIsActiveAndPendingRetires(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	started := ownershipSubscribeTestDraft(t, service, "subscribe-key-start-0001")
	if started.Subscription.State != generated.SubscriptionStateActive {
		t.Fatalf("subscribe with start: state = %s", started.Subscription.State)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE kind = 'rss_refresh' AND owner_id = $1`, string(started.RssSources[0].Id)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("subscribe with start scheduled %d initial refreshes, want 1", n)
	}

	draft := ownershipTestDraft(t)
	draft.InstanceLabel = animetest.StringPtr("Pending instance")
	pending, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: draft}, "subscribe-key-pending-retire")
	if err != nil {
		t.Fatalf("subscribe pending: %v", err)
	}
	if err := service.DeleteAnimeSubscription(ctx, pending.ManagedAnime.Id, SubscriptionDeleteInput{ExpectedVersion: int64(pending.Subscription.Version)}); err != nil {
		t.Fatalf("retire a pending subscription: %v", err)
	}
}
