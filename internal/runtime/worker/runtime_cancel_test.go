package worker

import (
	"bansoko/internal/workitems"
	"context"
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
