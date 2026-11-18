package worker

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"errors"
	"strings"
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
	deferErr      error
	settleErr     error
}

func (s *automaticRecoveryStore) ClaimRecovery(context.Context, []string, time.Duration) (workitems.Claim, error) {
	if s.recoveryCalls.Add(1) == 1 {
		return s.recoveryClaim, nil
	}
	return workitems.Claim{}, workitems.ErrNoDueWork
}
func (s *automaticRecoveryStore) DeferRecovery(context.Context, workitems.Claim, string, time.Duration) error {
	s.defers.Add(1)
	return s.deferErr
}
func (s *automaticRecoveryStore) SettleRecovery(context.Context, workitems.Claim, string, workitems.OwnerResult, workitems.EffectDisposition, workitems.OwnerWriter, func(context.Context, txscope.Tx) error) error {
	s.settles.Add(1)
	return s.settleErr
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

// scriptedRecoveryObserver returns one fixed observation outcome.
type scriptedRecoveryObserver struct {
	outcome Outcome
}

func (o scriptedRecoveryObserver) Handle(context.Context, workitems.Claim) (Outcome, error) {
	return Outcome{}, nil
}

func (o scriptedRecoveryObserver) ObserveRecovery(context.Context, workitems.Claim) (Outcome, error) {
	return o.outcome, nil
}

func recoveryTestRuntime(t *testing.T, store RuntimeStore, observer Handler) (*Runtime, *runtimeEventCapture) {
	t.Helper()
	capture := &runtimeEventCapture{}
	rt, err := NewRuntime(store, testRegistry(), map[string]Handler{"rss_refresh": observer}, "recovery-test", capture)
	if err != nil {
		t.Fatal(err)
	}
	return rt, capture
}

func recoveryTestClaim() workitems.Claim {
	return workitems.Claim{ID: "blocked", Kind: "rss_refresh", Owner: workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: "source-1"}, FencingToken: 2, Attempt: 1, MaxAttempts: 1}
}

func TestRecoverySettledLogsOnlyAfterSettlement(t *testing.T) {
	claim := recoveryTestClaim()
	settled := Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed"}, func(context.Context, txscope.Tx, workitems.OwnerResult) (int64, error) { return 2, nil })
	settled.Disposition = workitems.DispositionConfirmed
	store := &automaticRecoveryStore{dispatchStore: &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{}}}
	rt, capture := recoveryTestRuntime(t, store, scriptedRecoveryObserver{outcome: settled})

	rt.observeRecovery(context.Background(), claim, KindPolicy{Deadline: time.Second})

	if store.settles.Load() != 1 {
		t.Fatalf("settle calls = %d", store.settles.Load())
	}
	event, level, ok := capture.event("recovery_settled")
	if !ok {
		t.Fatal("settled recovery emitted no event")
	}
	if level != runtimeInfo || event.Outcome != "success" {
		t.Fatalf("settled event level/outcome = %d/%s", level, event.Outcome)
	}
	if event.StateAfter != workitems.StateFailed || event.Disposition != string(workitems.DispositionConfirmed) {
		t.Fatalf("settled event does not carry the observed disposition: %+v", event)
	}
	if event.OwnerID != "source-1" || event.WorkItemID != "blocked" || event.Kind != "rss_refresh" {
		t.Fatalf("settled event identity = %+v", event)
	}
	if strings.Contains(event.Message, "delivered") || strings.Contains(event.Message, "sent successfully") {
		t.Fatalf("settled event claims a new external effect: %q", event.Message)
	}
}

func TestRecoverySettlementFailureLogsNoSettledEvent(t *testing.T) {
	claim := recoveryTestClaim()
	settled := Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed"}, func(context.Context, txscope.Tx, workitems.OwnerResult) (int64, error) { return 2, nil })
	settled.Disposition = workitems.DispositionNoExternalEffect
	store := &automaticRecoveryStore{dispatchStore: &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{}}, settleErr: errors.New("database unavailable")}
	rt, capture := recoveryTestRuntime(t, store, scriptedRecoveryObserver{outcome: settled})

	rt.observeRecovery(context.Background(), claim, KindPolicy{Deadline: time.Second})

	if _, ok := capture.hasReason("recovery_settled"); ok {
		t.Fatal("a failed settlement reported success")
	}
	if store.defers.Load() != 1 {
		t.Fatalf("failed settlement did not defer: %d", store.defers.Load())
	}
}

func TestRecoveryCommittedLogsNoNewEffect(t *testing.T) {
	claim := recoveryTestClaim()
	store := &automaticRecoveryStore{dispatchStore: &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{}}}
	rt, capture := recoveryTestRuntime(t, store, scriptedRecoveryObserver{outcome: Committed(claim, workitems.OwnerResult{State: "delivered"})})

	rt.observeRecovery(context.Background(), claim, KindPolicy{Deadline: time.Second})

	if store.settles.Load() != 0 || store.defers.Load() != 0 {
		t.Fatalf("a committed receipt was settled or deferred: settles=%d defers=%d", store.settles.Load(), store.defers.Load())
	}
	event, level, ok := capture.event("recovery_committed")
	if !ok {
		t.Fatal("committed recovery emitted no event")
	}
	if level != runtimeInfo || event.Outcome != "success" {
		t.Fatalf("committed event level/outcome = %d/%s", level, event.Outcome)
	}
	if !strings.Contains(event.Message, "no new effect") && !strings.Contains(event.Message, "sent no new effect") {
		t.Fatalf("committed event message = %q", event.Message)
	}
}

func TestRecoveryDeferWriteFailureDoesNotPromiseRetry(t *testing.T) {
	claim := recoveryTestClaim()
	store := &automaticRecoveryStore{
		dispatchStore: &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{}},
		deferErr:      errors.New("database unavailable"),
	}
	rt, capture := recoveryTestRuntime(t, store, scriptedRecoveryObserver{outcome: AwaitRecovery("")})

	rt.observeRecovery(context.Background(), claim, KindPolicy{Deadline: time.Second})

	event, level, ok := capture.event("recovery_defer_failed")
	if !ok {
		t.Fatal("failed deferral emitted no event")
	}
	if level != runtimeWarn || event.Outcome != "unconfirmed" {
		t.Fatalf("defer failure event level/outcome = %d/%s", level, event.Outcome)
	}
	if !strings.Contains(event.Message, "whether a retry was scheduled is unknown") {
		t.Fatalf("defer failure message = %q", event.Message)
	}
	for _, forbidden := range []string{"will retry", "retries automatically", "is scheduled"} {
		if strings.Contains(event.Message, forbidden) {
			t.Fatalf("defer failure promised a retry: %q", event.Message)
		}
	}
	if strings.Contains(event.Message, "database unavailable") {
		t.Fatalf("defer failure carried the raw error: %q", event.Message)
	}
}
