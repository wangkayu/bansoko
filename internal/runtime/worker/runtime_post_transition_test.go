package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// runtimePostStore adds the post-transition finisher to the runtime fake, so
// the lane-based Runtime can be exercised with a completion that must commit a
// durable follow-up row with the terminal transition.
type runtimePostStore struct {
	fakeRuntimeStore
	hookCalls     int
	hookAfterTerm bool
	hookErr       error
}

func (s *runtimePostStore) FinishWithResultPostTransition(ctx context.Context, claim workitems.Claim, terminalState string, result workitems.OwnerResult, owner workitems.OwnerWriter, hook func(context.Context, txscope.Tx) error) error {
	if err := s.Finish(ctx, claim, terminalState, owner); err != nil {
		return err
	}
	if hook == nil {
		return nil
	}
	s.hookCalls++
	s.hookAfterTerm = s.items[claim.ID].state == terminalState
	return hook(ctx, txscope.Tx{})
}

func newPostRuntime(t *testing.T, store RuntimeStore, handler Handler) *Runtime {
	t.Helper()
	registry, err := NewRegistry(KindPolicy{Kind: "file_execute", Lane: LaneHeavy, Lease: 5 * time.Second, Heartbeat: 2 * time.Second, Deadline: 5 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(store, registry, map[string]Handler{"file_execute": handler}, "worker-post-transition", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// seedRuntimeClaim arms the fake item exactly as a real claim would, so the
// fenced finish transition accepts the synthetic claim.
func seedRuntimeClaim(store *fakeRuntimeStore, id string, fencingToken int64) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.items[id] = &fakeItem{state: "leased", fencingToken: fencingToken, leaseUntil: time.Now().Add(time.Minute)}
}

// TestRuntimeRunsPostTransitionInsideTheFinishTransition proves the production
// lane runtime — not only the single-shot Worker — honors Outcome.PostTransition.
// The durable placement obligation is created by exactly this hook, so a
// runtime that dispatched only the plain finish would silently reopen the crash
// window between the file effect commit and the placement handoff.
func TestRuntimeRunsPostTransitionInsideTheFinishTransition(t *testing.T) {
	store := &runtimePostStore{fakeRuntimeStore: *newFakeRuntimeStore()}
	seedRuntimeClaim(&store.fakeRuntimeStore, "item-1", 1)
	hookRan := false
	rt := newPostRuntime(t, store, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		outcome := Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed"}, nil)
		outcome.PostTransition = func(context.Context, txscope.Tx) error { hookRan = true; return nil }
		return outcome, nil
	}))
	rt.execute(context.Background(), workitems.Claim{
		ID: "item-1", Kind: "file_execute", FencingToken: 1, Attempt: 1, MaxAttempts: 5,
		Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "op-1"},
	}, rt.registry.MustGet("file_execute"))

	if store.hookCalls != 1 || !hookRan {
		t.Fatalf("runtime must run the post-transition hook exactly once, calls=%d ran=%v", store.hookCalls, hookRan)
	}
	if !store.hookAfterTerm {
		t.Fatal("the post-transition hook must observe the already-terminal WorkItem")
	}
	if store.finishCount.Load() != 1 {
		t.Fatalf("finish count = %d, want 1", store.finishCount.Load())
	}
}

// TestRuntimeFailsClosedWhenStoreLacksPostTransitionSupport proves a runtime
// whose store cannot run the hook leaves the claim untouched instead of
// committing the completion without its durable follow-up row.
func TestRuntimeFailsClosedWhenStoreLacksPostTransitionSupport(t *testing.T) {
	store := &fakeRuntimeStore{items: map[string]*fakeItem{}}
	seedRuntimeClaim(store, "item-2", 1)
	hookRan := false
	rt := newPostRuntime(t, store, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		outcome := Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed"}, nil)
		outcome.PostTransition = func(context.Context, txscope.Tx) error { hookRan = true; return nil }
		return outcome, nil
	}))
	rt.execute(context.Background(), workitems.Claim{
		ID: "item-2", Kind: "file_execute", FencingToken: 1, Attempt: 1, MaxAttempts: 5,
		Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "op-2"},
	}, rt.registry.MustGet("file_execute"))

	if hookRan {
		t.Fatal("unsupported post-transition must never run the hook")
	}
	if store.finishCount.Load() != 0 {
		t.Fatalf("unsupported post-transition must not finish the item, finish count = %d", store.finishCount.Load())
	}
	if store.items["item-2"].state != "leased" {
		t.Fatalf("the claim must stay leased for reclaim, got %q", store.items["item-2"].state)
	}
}

// runtimeOneShotPostStore is the marker-first variant: it records that the
// hook ran through the one-shot settlement path rather than the ordinary one.
type runtimeOneShotPostStore struct {
	oneShotStore
	postCount atomic.Int64
	postErr   error
}

func (s *runtimeOneShotPostStore) FinishOneShotWithResultPostTransition(ctx context.Context, claim workitems.Claim, lease workitems.AdmissionLease, disposition workitems.EffectDisposition, terminalState string, ownerResult workitems.OwnerResult, owner workitems.OwnerWriter, hook func(context.Context, txscope.Tx) error) error {
	s.postCount.Add(1)
	s.lastLease = lease
	s.lastDisp = disposition
	if s.postErr != nil {
		return s.postErr
	}
	if err := s.Finish(ctx, claim, terminalState, owner); err != nil {
		return err
	}
	if hook != nil {
		return hook(ctx, txscope.Tx{})
	}
	return nil
}

// TestRuntimeRoutesOneShotPostTransitionToMarkerFirstFinish proves a one-shot
// completion with a follow-up hook settles through the marker-first variant:
// the ordinary path would drop the hook inside FinishOneShotWithResult.
func TestRuntimeRoutesOneShotPostTransitionToMarkerFirstFinish(t *testing.T) {
	store := &runtimeOneShotPostStore{oneShotStore: oneShotStore{fakeRuntimeStore: *newFakeRuntimeStore()}}
	claim := oneShotClaim()
	// The lane runtime dispatches by the exact claim kind, so exercise the
	// one-shot finish with the kind this runtime registers.
	claim.Kind = "file_execute"
	seedLeasedItem(&store.oneShotStore, claim.ID, claim.FencingToken)
	hookRan := false
	rt := newPostRuntime(t, store, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		outcome := Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed"}, nil)
		outcome.PostTransition = func(context.Context, txscope.Tx) error { hookRan = true; return nil }
		return outcome, nil
	}))
	rt.execute(context.Background(), claim, rt.registry.MustGet("file_execute"))

	if store.postCount.Load() != 1 || !hookRan {
		t.Fatalf("one-shot post-transition must route to the marker-first variant once, count=%d ran=%v", store.postCount.Load(), hookRan)
	}
	if store.finish1Count.Load() != 0 {
		t.Fatalf("the ordinary one-shot finish must not be used when a hook is present, count=%d", store.finish1Count.Load())
	}
	if store.lastLease.WorkItemID != claim.ID {
		t.Fatalf("marker-first variant did not receive the admission lease: %+v", store.lastLease)
	}
}

// TestRuntimeOneShotPostTransitionFailsClosedWithoutSupport proves a one-shot
// store that cannot run the marker-first hook never settles the claim, so the
// durable follow-up can be retried instead of being silently lost.
func TestRuntimeOneShotPostTransitionFailsClosedWithoutSupport(t *testing.T) {
	store := &oneShotStore{fakeRuntimeStore: *newFakeRuntimeStore()}
	claim := oneShotClaim()
	claim.Kind = "file_execute"
	seedLeasedItem(store, claim.ID, claim.FencingToken)
	hookRan := false
	rt := newPostRuntime(t, store, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		outcome := Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed"}, nil)
		outcome.PostTransition = func(context.Context, txscope.Tx) error { hookRan = true; return nil }
		return outcome, nil
	}))
	rt.execute(context.Background(), claim, rt.registry.MustGet("file_execute"))

	if hookRan {
		t.Fatal("unsupported one-shot post-transition must never run the hook")
	}
	if store.finish1Count.Load() != 0 || store.retry1Count.Load() != 0 || store.finishCount.Load() != 0 {
		t.Fatalf("unsupported one-shot post-transition must not settle the claim: finish1=%d retry1=%d finish=%d",
			store.finish1Count.Load(), store.retry1Count.Load(), store.finishCount.Load())
	}
}

// TestRuntimePostTransitionHookFailureRollsBackTheSettlement proves a failing
// hook aborts the whole transition: the store reports the error, the runtime
// logs it, and no after-commit work runs for a completion that never committed.
func TestRuntimePostTransitionHookFailureRollsBackTheSettlement(t *testing.T) {
	store := &runtimePostStore{fakeRuntimeStore: *newFakeRuntimeStore(), hookErr: errors.New("obligation insert failed")}
	seedRuntimeClaim(&store.fakeRuntimeStore, "item-3", 1)
	afterCommitRan := false
	rt := newPostRuntime(t, store, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		outcome := Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed"}, nil)
		outcome.PostTransition = func(context.Context, txscope.Tx) error { return store.hookErr }
		return WithAfterCommit(outcome, func(context.Context) { afterCommitRan = true }), nil
	}))
	rt.execute(context.Background(), workitems.Claim{
		ID: "item-3", Kind: "file_execute", FencingToken: 1, Attempt: 1, MaxAttempts: 5,
		Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "op-3"},
	}, rt.registry.MustGet("file_execute"))

	if afterCommitRan {
		t.Fatal("after-commit work must not run when the transition failed")
	}
}
