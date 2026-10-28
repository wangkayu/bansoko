package worker

import (
	"bansoko/internal/workitems"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type quarantineStore struct {
	*fakeRuntimeStore
	blocks atomic.Int64
}

func (s *quarantineStore) BlockRecovery(context.Context, workitems.Claim, string) error {
	s.blocks.Add(1)
	return nil
}

func TestRuntimeCancelledClaimNeverStartsHandler(t *testing.T) {
	s := &quarantineStore{fakeRuntimeStore: newFakeRuntimeStore()}
	var calls atomic.Int64
	reg := testRegistry()
	rt, err := NewRuntime(s, reg, map[string]Handler{"rss_refresh": HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) { calls.Add(1); return Outcome{}, nil })}, "cancel", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt.execute(context.Background(), workitems.Claim{ID: "cancel", Kind: "rss_refresh", CancelRequested: true, FencingToken: 1}, reg.MustGet("rss_refresh"))
	if calls.Load() != 0 || s.blocks.Load() != 1 {
		t.Fatalf("cancel started business: calls=%d blocked=%d", calls.Load(), s.blocks.Load())
	}
}

type observedErrorHandler struct{ panicOnHandle bool }

func (h observedErrorHandler) Handle(context.Context, workitems.Claim) (Outcome, error) {
	if h.panicOnHandle {
		panic("stage handler panic")
	}
	return Outcome{}, errors.New("stage handler error")
}

func (observedErrorHandler) ObserveRecovery(context.Context, workitems.Claim) (Outcome, error) {
	return AwaitRecovery("observed"), nil
}

// A handler with a recovery observer never fails its last attempt through an
// owner-less retry: the claim is held for the observer to write the owner.
func TestRuntimeFinalHandlerFailureHeldForRecovery(t *testing.T) {
	for _, test := range []struct {
		name    string
		panic   bool
		attempt int
		blocks  int64
		retries int64
	}{
		{name: "error before budget", attempt: 1, retries: 1},
		{name: "error at budget", attempt: 3, blocks: 1},
		{name: "panic at budget", panic: true, attempt: 3, blocks: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &quarantineStore{fakeRuntimeStore: newFakeRuntimeStore()}
			s.items["observed"] = &fakeItem{state: "leased", fencingToken: 1}
			rt, err := NewRuntime(s, testRegistry(), map[string]Handler{"rss_refresh": observedErrorHandler{panicOnHandle: test.panic}}, "observed", testRuntimeLogger())
			if err != nil {
				t.Fatal(err)
			}
			rt.handleWithRecovery(context.Background(), workitems.Claim{ID: "observed", Kind: "rss_refresh", FencingToken: 1, Attempt: test.attempt, MaxAttempts: 3}, nil)
			if s.blocks.Load() != test.blocks || s.retryCount.Load() != test.retries || s.finishCount.Load() != 0 {
				t.Fatalf("blocks=%d retries=%d finish=%d; want blocks=%d retries=%d", s.blocks.Load(), s.retryCount.Load(), s.finishCount.Load(), test.blocks, test.retries)
			}
		})
	}
}

func TestRuntimeFinalDeadlineQuarantinesWithoutFakeOutcome(t *testing.T) {
	s := &quarantineStore{fakeRuntimeStore: newFakeRuntimeStore()}
	reg, err := NewRegistry(KindPolicy{Kind: "rss_refresh", Lane: LaneLight, Lease: time.Second, Heartbeat: 100 * time.Millisecond, Deadline: 10 * time.Millisecond, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(s, reg, map[string]Handler{"rss_refresh": HandlerFunc(func(ctx context.Context, _ workitems.Claim) (Outcome, error) {
		<-ctx.Done()
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
	})}, "last-attempt", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt.execute(context.Background(), workitems.Claim{ID: "last", Kind: "rss_refresh", FencingToken: 1, Attempt: 1, MaxAttempts: 1}, reg.MustGet("rss_refresh"))
	if s.blocks.Load() != 1 || s.finishCount.Load() != 0 || s.retryCount.Load() != 0 {
		t.Fatalf("deadline disposition invented: blocks=%d finish=%d retry=%d", s.blocks.Load(), s.finishCount.Load(), s.retryCount.Load())
	}
}
