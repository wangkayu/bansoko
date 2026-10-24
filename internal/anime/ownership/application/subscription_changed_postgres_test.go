package application

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
)

// recordingNotices records the business notices a producer admits inside its
// owner transaction.
type recordingNotices struct {
	mu       sync.Mutex
	requests []plugintargetport.BusinessDeliveryRequest
}

func (r *recordingNotices) EnqueueBusinessDelivery(_ context.Context, scope txscope.Tx, request plugintargetport.BusinessDeliveryRequest) error {
	if scope.SQLTx() == nil {
		panic("notice admitted outside the owner transaction")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return nil
}

func (r *recordingNotices) list() []plugintargetport.BusinessDeliveryRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]plugintargetport.BusinessDeliveryRequest(nil), r.requests...)
}

// subscription_changed: subscribe, stop, resume and retire each admit one
// notice keyed by the subscription version the transition commits; a
// same-state save is not a transition. The retirement notice names no
// managed anime (the retirement fence), and carries it in the payload.
func TestSubscriptionChangedNoticePerTransition(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	notices := &recordingNotices{}
	service.SetBusinessDeliveryEnqueuer(notices)
	service.Draft.SetBusinessDeliveryEnqueuer(notices)
	ctx := context.Background()

	subscribed := ownershipSubscribeTestDraft(t, service, "subscribe-key-changed-notice")
	animeID := subscribed.ManagedAnime.Id
	subscriptionID := subscribed.Subscription.Id
	patch := func(version int64, state string) {
		t.Helper()
		if _, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
			ExpectedVersion: version, State: subscriptiondomain.SubscriptionState(state),
		}); err != nil {
			t.Fatalf("patch %s: %v", state, err)
		}
	}
	patch(1, "stopped")
	patch(2, "stopped") // same state: no transition
	patch(3, "active")
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: 4}); err != nil {
		t.Fatalf("retire: %v", err)
	}

	want := []struct {
		action  string
		version int64
		anime   string
	}{
		{plugintargetport.SubscriptionStarted, 1, animeID},
		{plugintargetport.SubscriptionStopped, 2, animeID},
		{plugintargetport.SubscriptionResumed, 4, animeID},
		{plugintargetport.SubscriptionRetired, 4, ""},
	}
	got := notices.list()
	if len(got) != len(want) {
		t.Fatalf("notices=%+v, want %d", got, len(want))
	}
	for i, request := range got {
		if request.TriggerKind != plugintargetport.TriggerSubscriptionChanged || request.SourceOwnerType != plugintargetport.OwnerSubscription ||
			request.SourceOwnerID != subscriptionID || request.SourceOwnerVersion != want[i].version ||
			request.ResultKey != want[i].action || request.ManagedAnimeID != want[i].anime {
			t.Fatalf("notice %d=%+v, want %s at version %d", i, request, want[i].action, want[i].version)
		}
		var facts map[string]any
		if err := json.Unmarshal(request.Payload, &facts); err != nil {
			t.Fatal(err)
		}
		if facts["event.action"] != want[i].action || facts["anime.id"] != animeID || facts["anime.title"] != "Test Show" {
			t.Fatalf("notice %d facts=%v", i, facts)
		}
	}

	// Retiring again is a no-op and admits nothing.
	var current int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM subscriptions WHERE id = $1`, subscriptionID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: current}); err != nil {
		t.Fatalf("retire again: %v", err)
	}
	if again := notices.list(); len(again) != len(want) {
		t.Fatalf("a repeated retirement admitted another notice: %+v", again[len(want):])
	}
}
