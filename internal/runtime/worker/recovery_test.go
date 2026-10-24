package worker

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type automaticRecoveryStore struct {
	*dispatchStore
	recoveryClaim workitems.Claim
	recoveryCalls atomic.Int64
	settles       atomic.Int64
	defers        atomic.Int64
}

func (s *automaticRecoveryStore) ClaimRecovery(context.Context, []string, time.Duration) (workitems.Claim, error) {
	if s.recoveryCalls.Add(1) == 1 {
		return s.recoveryClaim, nil
	}
	return workitems.Claim{}, workitems.ErrNoDueWork
}
func (s *automaticRecoveryStore) DeferRecovery(context.Context, workitems.Claim, string, time.Duration) error {
	s.defers.Add(1)
	return nil
}
func (s *automaticRecoveryStore) SettleRecovery(context.Context, workitems.Claim, string, workitems.OwnerResult, workitems.EffectDisposition, workitems.OwnerWriter, func(context.Context, txscope.Tx) error) error {
	s.settles.Add(1)
	return nil
}

type automaticObserver struct {
	business atomic.Int64
	observed chan struct{}
	unknown  bool
}

func (h *automaticObserver) Handle(context.Context, workitems.Claim) (Outcome, error) {
	h.business.Add(1)
	return Outcome{}, nil
}
func (h *automaticObserver) ObserveRecovery(context.Context, workitems.Claim) (Outcome, error) {
	select {
	case h.observed <- struct{}{}:
	default:
	}
	if h.unknown {
		return AwaitRecovery(""), nil
	}
	o := Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed"}, func(context.Context, txscope.Tx, workitems.OwnerResult) (int64, error) { return 2, nil })
	o.Disposition = workitems.DispositionNoExternalEffect
	return o, nil
}
func TestDispatcherAutomaticallyObservesBlockedWorkWithoutBusinessHandle(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "unknown"}[unknown], func(t *testing.T) {
			s := &automaticRecoveryStore{dispatchStore: &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{}}, recoveryClaim: workitems.Claim{ID: "blocked", Kind: "rss_refresh", FencingToken: 2, Attempt: 1, MaxAttempts: 1}}
			h := &automaticObserver{observed: make(chan struct{}, 1), unknown: unknown}
			rt, err := NewRuntime(s, testRegistry(), map[string]Handler{"rss_refresh": h}, "auto-recovery", testRuntimeLogger())
			if err != nil {
				t.Fatal(err)
			}
			rt.Start()
			select {
			case <-h.observed:
			case <-time.After(time.Second):
				_ = rt.Drain(time.Second)
				t.Fatal("blocked item has no automatic production observation path")
			}
			if err := rt.Drain(time.Second); err != nil {
				t.Fatal(err)
			}
			if h.business.Load() != 0 {
				t.Fatal("recovery called normal business handler")
			}
			if unknown && (s.defers.Load() != 1 || s.settles.Load() != 0) {
				t.Fatal("unknown evidence incorrectly settled")
			}
			if !unknown && s.settles.Load() != 1 {
				t.Fatal("no-effect recovery never settled owner")
			}
		})
	}
}
