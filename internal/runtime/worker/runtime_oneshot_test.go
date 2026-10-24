package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

// oneShotStore extends fakeRuntimeStore with the one-shot admission and
// marker-first transition ports so the central gate is testable without a
// database.
type oneShotStore struct {
	fakeRuntimeStore
	admitCount    atomic.Int64
	gateErr       error
	finish1Count  atomic.Int64
	retry1Count   atomic.Int64
	lastLease     workitems.AdmissionLease
	lastDisp      workitems.EffectDisposition
	leaseToReturn workitems.AdmissionLease
}

func (s *oneShotStore) AdmitOneShotEffect(ctx context.Context, claim workitems.Claim) (workitems.AdmissionLease, error) {
	s.admitCount.Add(1)
	if s.gateErr != nil {
		return workitems.AdmissionLease{}, s.gateErr
	}
	if s.leaseToReturn.AdmissionID == "" {
		s.leaseToReturn = workitems.AdmissionLease{
			MarkerID: claim.OneShot.MarkerID, AdmissionID: "admission-1",
			WorkItemID: claim.ID, OwnerType: claim.Owner.Type, OwnerID: claim.Owner.ID,
			ClaimedEpoch: claim.OneShot.AdmissionEpoch, AdmittedAt: time.Now().UTC(),
		}
	}
	return s.leaseToReturn, nil
}

func (s *oneShotStore) FinishOneShotWithResult(ctx context.Context, claim workitems.Claim, lease workitems.AdmissionLease, disposition workitems.EffectDisposition, terminalState string, ownerResult workitems.OwnerResult, writeOwner workitems.OwnerWriter) error {
	s.finish1Count.Add(1)
	s.lastLease = lease
	s.lastDisp = disposition
	return s.Finish(ctx, claim, terminalState, writeOwner)
}

func (s *oneShotStore) RetryOneShot(ctx context.Context, claim workitems.Claim, lease workitems.AdmissionLease, disposition workitems.EffectDisposition, reasonCode, reason string, delay time.Duration, writeOwner workitems.OwnerWriter) (workitems.RetryOutcome, error) {
	s.retry1Count.Add(1)
	s.lastLease = lease
	s.lastDisp = disposition
	return s.Retry(ctx, claim, reasonCode, reason, delay, writeOwner)
}

func oneShotClaim() workitems.Claim {
	return workitems.Claim{
		ID: "item-oneshot", Kind: "qb_submit", FencingToken: 1, Attempt: 1, MaxAttempts: 5,
		Owner:         workitems.OwnerRef{Type: workitems.OwnerDownloadTask, ID: "task-1"},
		InputSnapshot: json.RawMessage(`{"trigger":"one_shot_acquire","one_shot":{"marker_id":"marker-1","correlation_id":"corr-1","admission_epoch":1}}`),
		OneShot:       &workitems.OneShotRef{MarkerID: "marker-1", CorrelationID: "corr-1", AdmissionEpoch: 1},
	}
}

func newOneShotRuntime(t *testing.T, store *oneShotStore, handler Handler) (*Runtime, *runtimeEventCapture) {
	t.Helper()
	registry, err := NewRegistry(KindPolicy{Kind: "qb_submit", Lane: LaneHeavy, Lease: 5 * time.Second, Heartbeat: 2 * time.Second, Deadline: 5 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff})
	if err != nil {
		t.Fatal(err)
	}
	capture := &runtimeEventCapture{}
	rt, err := NewRuntime(store, registry, map[string]Handler{"qb_submit": handler}, "worker-oneshot", capture)
	if err != nil {
		t.Fatal(err)
	}
	return rt, capture
}

// seedLeasedItem seeds the leased WorkItem row so fenced transitions commit.
func seedLeasedItem(store *oneShotStore, id string, fencingToken int64) {
	store.mu.Lock()
	store.items[id] = &fakeItem{state: "leased", fencingToken: fencingToken, leaseUntil: time.Now().Add(time.Minute)}
	store.mu.Unlock()
}

func TestOneShotGateRunsBeforeHandlerAndRoutesFinish(t *testing.T) {
	store := &oneShotStore{fakeRuntimeStore: *newFakeRuntimeStore()}
	var handlerRanAfterGate atomic.Bool
	handler := HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
		handlerRanAfterGate.Store(store.admitCount.Load() == 1)
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "downloading"}, nil), nil
	})
	rt, _ := newOneShotRuntime(t, store, handler)
	claim := oneShotClaim()
	seedLeasedItem(store, claim.ID, claim.FencingToken)

	rt.execute(context.Background(), claim, rt.registry.MustGet("qb_submit"))
	if store.admitCount.Load() != 1 {
		t.Fatalf("gate admissions = %d, want 1", store.admitCount.Load())
	}
	if !handlerRanAfterGate.Load() {
		t.Fatal("handler ran before the admission gate committed")
	}
	if store.finish1Count.Load() != 1 || store.lastDisp != workitems.DispositionAmbiguous {
		t.Fatalf("finish routing failed: count=%d disposition=%q", store.finish1Count.Load(), store.lastDisp)
	}
	if store.lastLease.WorkItemID != "item-oneshot" || store.lastLease.MarkerID != "marker-1" {
		t.Fatalf("finish did not receive the admission lease: %+v", store.lastLease)
	}
}

func TestOneShotExplicitDispositionIsRouted(t *testing.T) {
	store := &oneShotStore{fakeRuntimeStore: *newFakeRuntimeStore()}
	handler := HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
		outcome := Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "EFFECT_UNCERTAIN"}, nil)
		outcome.Disposition = workitems.DispositionAmbiguous
		return outcome, nil
	})
	rt, _ := newOneShotRuntime(t, store, handler)
	claim := oneShotClaim()
	seedLeasedItem(store, claim.ID, claim.FencingToken)
	rt.execute(context.Background(), claim, rt.registry.MustGet("qb_submit"))
	if store.lastDisp != workitems.DispositionAmbiguous {
		t.Fatalf("explicit ambiguous disposition lost: %q", store.lastDisp)
	}

	store2 := &oneShotStore{fakeRuntimeStore: *newFakeRuntimeStore()}
	handler2 := HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
		outcome := Retry("qb_timeout", "timed out", time.Second, nil)
		outcome.Disposition = workitems.DispositionConfirmed
		return outcome, nil
	})
	rt2, _ := newOneShotRuntime(t, store2, handler2)
	seedLeasedItem(store2, claim.ID, claim.FencingToken)
	rt2.execute(context.Background(), claim, rt2.registry.MustGet("qb_submit"))
	if store2.retry1Count.Load() != 1 || store2.lastDisp != workitems.DispositionConfirmed {
		t.Fatalf("retry routing failed: count=%d disposition=%q", store2.retry1Count.Load(), store2.lastDisp)
	}
}

func TestOneShotReclaimingAbandonsClaimWithoutMutation(t *testing.T) {
	store := &oneShotStore{fakeRuntimeStore: *newFakeRuntimeStore(), gateErr: workitems.ErrOneShotReclaiming}
	handlerCalled := atomic.Bool{}
	handler := HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
		handlerCalled.Store(true)
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
	})
	rt, capture := newOneShotRuntime(t, store, handler)
	rt.execute(context.Background(), oneShotClaim(), rt.registry.MustGet("qb_submit"))

	if handlerCalled.Load() {
		t.Fatal("handler dispatched after ONE_SHOT_RECLAIMING; external effect possible")
	}
	if store.finish1Count.Load() != 0 || store.retry1Count.Load() != 0 ||
		store.fakeRuntimeStore.finishCount.Load() != 0 || store.fakeRuntimeStore.retryCount.Load() != 0 {
		t.Fatal("reclaiming abort mutated the WorkItem or owner")
	}
	if _, _, ok := capture.event("one_shot_reclaiming"); !ok {
		t.Fatal("structured ONE_SHOT_RECLAIMING record was not emitted")
	}
}

func TestOneShotGateFailureFailsClosed(t *testing.T) {
	store := &oneShotStore{fakeRuntimeStore: *newFakeRuntimeStore(), gateErr: errors.New("database unavailable")}
	handlerCalled := atomic.Bool{}
	handler := HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
		handlerCalled.Store(true)
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
	})
	rt, _ := newOneShotRuntime(t, store, handler)
	rt.execute(context.Background(), oneShotClaim(), rt.registry.MustGet("qb_submit"))
	if handlerCalled.Load() {
		t.Fatal("handler dispatched despite a gate failure")
	}
	if store.finish1Count.Load() != 0 || store.retry1Count.Load() != 0 {
		t.Fatal("gate failure mutated the WorkItem")
	}
}

func TestOrdinaryClaimsBypassTheGate(t *testing.T) {
	store := &oneShotStore{fakeRuntimeStore: *newFakeRuntimeStore()}
	handlerCalls := atomic.Int64{}
	handler := HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
		handlerCalls.Add(1)
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
	})
	rt, _ := newOneShotRuntime(t, store, handler)
	ordinary := oneShotClaim()
	ordinary.OneShot = nil
	ordinary.InputSnapshot = json.RawMessage(`{"trigger":"release_evaluate"}`)
	seedLeasedItem(store, ordinary.ID, ordinary.FencingToken)
	rt.execute(context.Background(), ordinary, rt.registry.MustGet("qb_submit"))
	if store.admitCount.Load() != 0 {
		t.Fatalf("ordinary claim passed the one-shot gate %d times", store.admitCount.Load())
	}
	if handlerCalls.Load() != 1 || store.finish1Count.Load() != 0 {
		t.Fatal("ordinary claim did not take the ordinary finish path")
	}
	if store.fakeRuntimeStore.finishCount.Load() != 1 {
		t.Fatal("ordinary finish bypassed the plain store")
	}
}
