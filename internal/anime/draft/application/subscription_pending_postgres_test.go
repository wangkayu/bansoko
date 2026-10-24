package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// Every entry creates a pending subscription unless the caller starts it:
// activation and direct subscribe without start schedule no refresh, and a
// provider activation still runs its group discovery (a configuration read,
// it schedules no refresh for a pending owner). start conflicts with any
// other explicit initial state.
func TestSubscribeDefaultsToPending(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	provider := &recordingGroupDiscoveryProvider{db: db, result: DiscoverResult{Outcome: "no_groups"}}
	service.SetGroupDiscoveryProvider(provider)
	ctx := context.Background()

	candidate := saveCandidate(t, service, providerDraft(t, "mikan-entry-pending", "Show Pending"))
	stopped := generated.SubscriptionStateStopped
	_, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-pending-conflict", generated.CandidateSubscribeInput{InitialState: &stopped, Start: new(true)})
	var bad *apperrors.BadRequest
	if !errors.As(err, &bad) {
		t.Fatalf("start with initial_state stopped: err = %v, want a bad request", err)
	}

	activated, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "candidate-pending-key", generated.CandidateSubscribeInput{})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if activated.Subscription.State != generated.SubscriptionStatePending {
		t.Fatalf("activation without start: state = %s, want pending", activated.Subscription.State)
	}
	if provider.count() != 1 || !provider.committed(0) {
		t.Fatalf("pending activation discovery = %d calls, committed = %t; want one post-commit call", provider.count(), provider.committed(0))
	}

	direct, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftTestDraft(t))}, "direct-pending-key")
	if err != nil {
		t.Fatalf("direct subscribe: %v", err)
	}
	if direct.Subscription.State != generated.SubscriptionStatePending || len(direct.RssSources) != 1 {
		t.Fatalf("direct subscribe without start: %+v", direct.Subscription)
	}
	var refreshes int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE kind = 'rss_refresh'`).Scan(&refreshes); err != nil {
		t.Fatal(err)
	}
	if refreshes != 0 {
		t.Fatalf("pending subscribes scheduled %d refreshes", refreshes)
	}
	// start is part of the request identity.
	_, err = service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftTestDraft(t)), Start: new(true)}, "direct-pending-key")
	assertCandidateBusinessCode(t, err, "IDEMPOTENCY_KEY_REUSED")
}
