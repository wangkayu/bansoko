package application

import (
	"context"
	"errors"
	"sync"
	"testing"

	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
)

// recordingReplacementTrigger is the fake after-commit replacement trigger
// port: it records every resume dispatch and never touches the database.
type recordingReplacementTrigger struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingReplacementTrigger) TriggerSubscriptionResumed(_ context.Context, animeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, animeID)
	return nil
}

func (r *recordingReplacementTrigger) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// failingReplacementTrigger simulates an after-commit dispatch failure: the
// resume owner truth must stay committed and the PATCH stays a success.
type failingReplacementTrigger struct{}

func (failingReplacementTrigger) TriggerSubscriptionResumed(context.Context, string) error {
	return errors.New("trigger store unavailable")
}

// TestPatchSubscriptionResumeDispatchesFreshReplacementEpoch verifies the
// stop/resume lifecycle dispatch discipline: stop dispatches nothing (the
// version bump invalidates old admitted decisions at settle), resume dispatches
// exactly one fresh epoch per actual stopped -> active transition, and
// same-state saves never dispatch. Repeated resume transitions are replayed
// safely (the trigger merges into one live item at the DB level).
func TestPatchSubscriptionResumeDispatchesFreshReplacementEpoch(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	trigger := &recordingReplacementTrigger{}
	service.SetReplacementTrigger(trigger)
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-replacement-0001")
	animeID := subscribe.ManagedAnime.Id
	ctx := context.Background()

	stopped, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 1, State: subscriptiondomain.SubscriptionState("stopped"),
	})
	if err != nil {
		t.Fatalf("stop subscription: %v", err)
	}
	if stopped.State != "stopped" || stopped.Version != 2 {
		t.Fatalf("stop must bump the subscription version: %+v", stopped)
	}
	if trigger.count() != 0 {
		t.Fatalf("stop must not dispatch a replacement epoch: %d", trigger.count())
	}

	resumed, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 2, State: subscriptiondomain.SubscriptionState("active"),
	})
	if err != nil {
		t.Fatalf("resume subscription: %v", err)
	}
	if resumed.State != "active" || resumed.Version != 3 {
		t.Fatalf("resume must bump the subscription version: %+v", resumed)
	}
	if trigger.count() != 1 || trigger.calls[0] != animeID {
		t.Fatalf("resume must dispatch exactly one fresh epoch for the anime: %v", trigger.calls)
	}

	// Same-state saves are not transitions: no new epoch dispatch.
	if _, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 3, State: subscriptiondomain.SubscriptionState("active"),
	}); err != nil {
		t.Fatalf("same-state save: %v", err)
	}
	if trigger.count() != 1 {
		t.Fatalf("same-state save must not dispatch: %d", trigger.count())
	}

	// A second stop/resume cycle is a new transition and dispatches again; the
	// DB-level merge keeps at most one live item per anime.
	if _, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 4, State: subscriptiondomain.SubscriptionState("stopped"),
	}); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if trigger.count() != 1 {
		t.Fatalf("second stop must not dispatch: %d", trigger.count())
	}
	if _, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 5, State: subscriptiondomain.SubscriptionState("active"),
	}); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if trigger.count() != 2 {
		t.Fatalf("second resume must dispatch a fresh epoch: %d", trigger.count())
	}
}

// TestPatchSubscriptionResumeTriggerFailureKeepsOwnerTruth verifies the
// after-commit sink contract: a failed trigger never rolls back the resumed
// subscription state or fails the PATCH response.
func TestPatchSubscriptionResumeTriggerFailureKeepsOwnerTruth(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	service.SetReplacementTrigger(failingReplacementTrigger{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-replacement-0002")
	animeID := subscribe.ManagedAnime.Id
	ctx := context.Background()

	if _, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 1, State: subscriptiondomain.SubscriptionState("stopped"),
	}); err != nil {
		t.Fatalf("stop subscription: %v", err)
	}
	resumed, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 2, State: subscriptiondomain.SubscriptionState("active"),
	})
	if err != nil {
		t.Fatalf("resume must stay a success even when the trigger fails: %v", err)
	}
	if resumed.State != "active" || resumed.Version != 3 {
		t.Fatalf("resumed owner truth must be committed: %+v", resumed)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "active" {
		t.Fatalf("subscription state must stay active: %s", state)
	}
}

func TestPatchSubscriptionProviderResumeRunsDiscoveryAfterCommit(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	provider := &ownershipRecordingDiscoveryProvider{db: db, result: DiscoverResult{Outcome: "no_groups"}}
	service.SetGroupDiscoveryProvider(provider)
	candidate := ownershipSaveCandidate(t, service, ownershipProviderDraft(t, "mikan-resume-discovery", "Resume Discovery"))
	activated := ownershipActivateCurrentCandidate(t, service, candidate, "candidate-resume-discovery")
	if provider.count() != 1 || !provider.committed(0) {
		t.Fatalf("activation discovery = %d calls, committed = %t; want one post-commit call", provider.count(), provider.committed(0))
	}
	ctx := context.Background()
	animeID := activated.ManagedAnime.ID
	if _, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 1, State: subscriptiondomain.SubscriptionStopped,
	}); err != nil {
		t.Fatalf("stop provider subscription: %v", err)
	}
	if provider.count() != 1 {
		t.Fatalf("stop must not run provider discovery, got %d calls", provider.count())
	}
	resumed, err := service.PatchAnimeSubscription(ctx, animeID, SubscriptionPatchInput{
		ExpectedVersion: 2, State: subscriptiondomain.SubscriptionActive,
	})
	if err != nil {
		t.Fatalf("resume provider subscription: %v", err)
	}
	if resumed.Version != 3 || provider.count() != 2 || !provider.committed(1) {
		t.Fatalf("resume discovery = version %d, calls %d, committed = %t; want version 3 and one new post-commit call", resumed.Version, provider.count(), provider.committed(1))
	}
	if got := animetest.CountRows(t, db, "work_items"); got != 0 {
		t.Fatalf("provider with no groups must not create refresh work, got %d", got)
	}
}
