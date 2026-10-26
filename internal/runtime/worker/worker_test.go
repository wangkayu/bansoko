package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

type fakeStore struct {
	claim      workitems.Claim
	claimErr   error
	claimKinds []string
	claimCalls int

	finishCalls          []finishCall
	retryCalls           []retryCall
	budgetlessRetryCalls []retryCall
	finishErr            error
	retryErr             error

	successorCreates int
}

type finishCall struct {
	claim       workitems.Claim
	state       string
	owner       workitems.OwnerWriter
	typed       bool
	ownerResult workitems.OwnerResult
}

type retryCall struct {
	claim      workitems.Claim
	reasonCode string
	reason     string
	delay      time.Duration
	owner      workitems.OwnerWriter
}

func (s *fakeStore) ClaimDue(_ context.Context, _ string, kinds []string, _ time.Duration) (workitems.Claim, error) {
	s.claimCalls++
	s.claimKinds = append([]string(nil), kinds...)
	if s.claimErr != nil {
		return workitems.Claim{}, s.claimErr
	}
	return s.claim, nil
}

func (s *fakeStore) Finish(_ context.Context, claim workitems.Claim, state string, owner workitems.OwnerWriter) error {
	s.finishCalls = append(s.finishCalls, finishCall{claim: claim, state: state, owner: owner})
	return s.finishErr
}

func (s *fakeStore) FinishWithResult(ctx context.Context, claim workitems.Claim, terminalState string, result workitems.OwnerResult, owner workitems.OwnerWriter) error {
	s.finishCalls = append(s.finishCalls, finishCall{
		claim:       claim,
		state:       terminalState,
		owner:       owner,
		typed:       true,
		ownerResult: result,
	})
	if s.finishErr != nil {
		return s.finishErr
	}
	if owner == nil {
		return nil
	}
	_, err := owner(ctx, txscope.Tx{}, result)
	return err
}

func (s *fakeStore) Retry(ctx context.Context, claim workitems.Claim, reasonCode, reason string, delay time.Duration, owner workitems.OwnerWriter) (workitems.RetryOutcome, error) {
	s.retryCalls = append(s.retryCalls, retryCall{
		claim:      claim,
		reasonCode: reasonCode,
		reason:     reason,
		delay:      delay,
		owner:      owner,
	})
	if s.retryErr != nil {
		return workitems.RetryOutcome{}, s.retryErr
	}
	if owner == nil {
		return workitems.RetryOutcome{}, nil
	}
	_, err := owner(ctx, txscope.Tx{}, workitems.OwnerResult{
		State:      "retrying",
		ReasonCode: reasonCode,
		Reason:     reason,
	})
	return workitems.RetryOutcome{}, err
}

func (s *fakeStore) RetryWithoutAttemptBudget(ctx context.Context, claim workitems.Claim, reasonCode, reason string, delay time.Duration, owner workitems.OwnerWriter) (workitems.RetryOutcome, error) {
	s.budgetlessRetryCalls = append(s.budgetlessRetryCalls, retryCall{
		claim: claim, reasonCode: reasonCode, reason: reason, delay: delay, owner: owner,
	})
	if s.retryErr != nil {
		return workitems.RetryOutcome{}, s.retryErr
	}
	if owner == nil {
		return workitems.RetryOutcome{}, nil
	}
	_, err := owner(ctx, txscope.Tx{}, workitems.OwnerResult{
		State:      "retrying",
		ReasonCode: reasonCode,
		Reason:     reason,
	})
	return workitems.RetryOutcome{}, err
}
func testClaim(kind string) workitems.Claim {
	return workitems.Claim{
		ID:   "work-item-1",
		Kind: kind,
		Owner: workitems.OwnerRef{
			Type: workitems.OwnerRSSSourceRuntime,
			ID:   "owner-1",
		},
		FencingToken: 3,
		Attempt:      1,
		MaxAttempts:  3,
	}
}

func TestRegisterUsesExplicitAllowlist(t *testing.T) {
	store := &fakeStore{}
	worker := NewWorker(store, "worker-1", time.Minute)
	handler := HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ready"}, nil), nil
	})

	if err := worker.Register("generic_command", handler); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("unknown kind registration error = %v", err)
	}
	if err := worker.Register(workitems.KindRSSRefresh, nil); !errors.Is(err, ErrWorkerInvalid) {
		t.Fatalf("nil handler registration error = %v", err)
	}
	if err := worker.Register(workitems.KindRSSRefresh, handler); err != nil {
		t.Fatalf("register supported kind: %v", err)
	}
	if err := worker.Register(workitems.KindRSSRefresh, handler); !errors.Is(err, ErrDuplicateHandler) {
		t.Fatalf("duplicate registration error = %v", err)
	}

	if got, want := AllowedKinds(), []string{
		workitems.KindRSSRefresh,
		workitems.KindReleaseEvaluate,
		workitems.KindQBSubmit,
		workitems.KindQBSync,
		workitems.KindArtifactInspect,
		workitems.KindFileExecute,
		workitems.KindPluginDeliver,
		workitems.KindReconciliationScan,
		workitems.KindAlignmentExecute,
		workitems.KindReplacementEvaluate,
		workitems.KindReplacementFallback,
		workitems.KindReplacementDelete,
		workitems.KindReconciliationCalibrate,
		workitems.KindReconciliationConsumeAlignment,
		workitems.KindReconciliationConsumeReplacement,
		workitems.KindOffsetMigrationExecute,
		workitems.KindPostProcessPlan,
		workitems.KindFileManagerExecute,
		workitems.KindCompletionExecute,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed kinds = %v, want %v", got, want)
	}
}

func TestRunOnceClaimsOnlyRegisteredKindsAndFinishesTypedResult(t *testing.T) {
	store := &fakeStore{claim: testClaim(workitems.KindFileExecute)}
	worker := NewWorker(store, "worker-1", time.Minute)
	var gotClaim workitems.Claim
	var gotContext context.Context
	var ownerWriter workitems.OwnerWriter
	handler := HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
		gotClaim = claim
		gotContext = ctx
		ownerWriter = func(context.Context, txscope.Tx, workitems.OwnerResult) (int64, error) { return 0, nil }
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{
			State:      "completed",
			ReasonCode: "DONE",
			Reason:     "finished",
		}, ownerWriter), nil
	})
	if err := worker.Register(workitems.KindFileExecute, handler); err != nil {
		t.Fatal(err)
	}

	handled, err := worker.RunOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("run once: handled=%v err=%v", handled, err)
	}
	if store.claimCalls != 1 {
		t.Fatalf("claim calls = %d, want 1", store.claimCalls)
	}
	if got, want := store.claimKinds, []string{workitems.KindFileExecute}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claimed kinds = %v, want %v", got, want)
	}
	if gotClaim.ID != store.claim.ID || gotContext == nil {
		t.Fatalf("handler did not receive the claimed item/context: claim=%+v context=%v", gotClaim, gotContext)
	}
	if len(store.finishCalls) != 1 || len(store.retryCalls) != 0 {
		t.Fatalf("finish calls = %d, retry calls = %d", len(store.finishCalls), len(store.retryCalls))
	}
	finish := store.finishCalls[0]
	if !finish.typed || finish.state != workitems.StateSucceeded || finish.ownerResult.State != "completed" || finish.ownerResult.ReasonCode != "DONE" {
		t.Fatalf("typed finish result = %+v", finish)
	}
	if store.successorCreates != 0 {
		t.Fatalf("worker created successor WorkItems: %d", store.successorCreates)
	}
}

func TestRunOnceRoutesRetryOutcomeWithoutFinish(t *testing.T) {
	store := &fakeStore{claim: testClaim(workitems.KindRSSRefresh)}
	worker := NewWorker(store, "worker-1", time.Minute)
	ownerCalled := false
	writeOwner := workitems.OwnerWriter(func(context.Context, txscope.Tx, workitems.OwnerResult) (int64, error) {
		ownerCalled = true
		return 0, nil
	})
	handler := HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		return Retry("FEED_TIMEOUT", "feed timed out", 2*time.Minute, writeOwner), nil
	})
	if err := worker.Register(workitems.KindRSSRefresh, handler); err != nil {
		t.Fatal(err)
	}

	if handled, err := worker.RunOnce(context.Background()); err != nil || !handled {
		t.Fatalf("run once: handled=%v err=%v", handled, err)
	}
	if len(store.retryCalls) != 1 || len(store.finishCalls) != 0 {
		t.Fatalf("retry calls = %d, finish calls = %d", len(store.retryCalls), len(store.finishCalls))
	}
	retry := store.retryCalls[0]
	if retry.reasonCode != "FEED_TIMEOUT" || retry.reason != "feed timed out" || retry.delay != 2*time.Minute {
		t.Fatalf("retry routing = %+v", retry)
	}
	if retry.owner == nil {
		t.Fatal("retry did not preserve the typed owner writer")
	}
	if !ownerCalled {
		t.Fatal("retry did not invoke the owner writer")
	}
}

func TestRunOnceRoutesRetryUntilResolvedToOptionalStorePath(t *testing.T) {
	store := &fakeStore{claim: testClaim(workitems.KindRSSRefresh)}
	store.claim.Attempt = store.claim.MaxAttempts
	worker := NewWorker(store, "worker-1", time.Minute)
	if err := worker.Register(workitems.KindRSSRefresh, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		return RetryUntilResolved("RECOVERY_PENDING", "waiting for external resolution", time.Minute, nil), nil
	})); err != nil {
		t.Fatal(err)
	}

	if handled, err := worker.RunOnce(context.Background()); err != nil || !handled {
		t.Fatalf("run once: handled=%v err=%v", handled, err)
	}
	if len(store.budgetlessRetryCalls) != 1 || len(store.retryCalls) != 0 {
		t.Fatalf("budgetless retries = %d, ordinary retries = %d", len(store.budgetlessRetryCalls), len(store.retryCalls))
	}
	if store.budgetlessRetryCalls[0].reasonCode != "RECOVERY_PENDING" {
		t.Fatalf("budgetless retry = %+v", store.budgetlessRetryCalls[0])
	}
}

func TestRunOnceHonorsCancellationBeforeClaim(t *testing.T) {
	store := &fakeStore{claim: testClaim(workitems.KindRSSRefresh)}
	worker := NewWorker(store, "worker-1", time.Minute)
	if err := worker.Register(workitems.KindRSSRefresh, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ready"}, nil), nil
	})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handled, err := worker.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) || handled {
		t.Fatalf("canceled run = handled=%v err=%v", handled, err)
	}
	if store.claimCalls != 0 || len(store.finishCalls) != 0 || len(store.retryCalls) != 0 {
		t.Fatalf("canceled worker touched store: claims=%d finishes=%d retries=%d", store.claimCalls, len(store.finishCalls), len(store.retryCalls))
	}
}

func TestRunOnceDoesNotWriteAfterHandlerCancellation(t *testing.T) {
	store := &fakeStore{claim: testClaim(workitems.KindRSSRefresh)}
	worker := NewWorker(store, "worker-1", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	if err := worker.Register(workitems.KindRSSRefresh, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		cancel()
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ready"}, nil), nil
	})); err != nil {
		t.Fatal(err)
	}

	handled, err := worker.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) || !handled {
		t.Fatalf("canceled handler run = handled=%v err=%v", handled, err)
	}
	if len(store.finishCalls) != 0 || len(store.retryCalls) != 0 {
		t.Fatalf("worker wrote after cancellation: finishes=%d retries=%d", len(store.finishCalls), len(store.retryCalls))
	}
}

func TestRunOnceRejectsClaimedUnknownKindWithoutSuccessor(t *testing.T) {
	store := &fakeStore{claim: testClaim("generic_command")}
	worker := NewWorker(store, "worker-1", time.Minute)
	if err := worker.Register(workitems.KindRSSRefresh, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ready"}, nil), nil
	})); err != nil {
		t.Fatal(err)
	}

	handled, err := worker.RunOnce(context.Background())
	if !errors.Is(err, ErrUnsupportedKind) || !handled {
		t.Fatalf("unknown claim = handled=%v err=%v", handled, err)
	}
	if len(store.finishCalls) != 0 || len(store.retryCalls) != 0 || store.successorCreates != 0 {
		t.Fatalf("unknown claim changed work: finishes=%d retries=%d successors=%d", len(store.finishCalls), len(store.retryCalls), store.successorCreates)
	}
}

func TestRunOnceRejectsInvalidOutcomeWithoutStoreTransition(t *testing.T) {
	store := &fakeStore{claim: testClaim(workitems.KindRSSRefresh)}
	worker := NewWorker(store, "worker-1", time.Minute)
	if err := worker.Register(workitems.KindRSSRefresh, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		return Outcome{Kind: "success"}, nil
	})); err != nil {
		t.Fatal(err)
	}

	if handled, err := worker.RunOnce(context.Background()); !handled || !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("invalid outcome = handled=%v err=%v", handled, err)
	}
	if len(store.finishCalls) != 0 || len(store.retryCalls) != 0 {
		t.Fatalf("invalid outcome changed store: finishes=%d retries=%d", len(store.finishCalls), len(store.retryCalls))
	}
}

func TestRunOnceRunsBestEffortHookOnlyAfterBusinessCommit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		finishErr  error
		wantHook   bool
		wantRunErr bool
	}{
		{name: "committed", wantHook: true},
		{name: "commit failed", finishErr: errors.New("commit failed"), wantRunErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{claim: testClaim(workitems.KindPluginDeliver), finishErr: tc.finishErr}
			worker := NewWorker(store, "worker-1", time.Minute)
			hookCalled := false
			if err := worker.Register(workitems.KindPluginDeliver, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
				outcome := Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed"}, nil)
				return WithAfterCommit(outcome, func(context.Context) { hookCalled = true }), nil
			})); err != nil {
				t.Fatal(err)
			}
			_, err := worker.RunOnce(context.Background())
			if (err != nil) != tc.wantRunErr {
				t.Fatalf("run error=%v", err)
			}
			if hookCalled != tc.wantHook {
				t.Fatalf("hook called=%v want=%v", hookCalled, tc.wantHook)
			}
		})
	}
}

// postTransitionStore extends the fake store with the post-transition finish
// path and records the invocation order relative to the owner writer.
type postTransitionStore struct {
	fakeStore
	hookErr        error
	hookCalls      int
	ownerFinished  bool
	hookAfterOwner bool
}

func (s *postTransitionStore) FinishWithResultPostTransition(ctx context.Context, claim workitems.Claim, terminalState string, result workitems.OwnerResult, owner workitems.OwnerWriter, hook func(context.Context, txscope.Tx) error) error {
	if owner != nil {
		if _, err := owner(ctx, txscope.Tx{}, result); err != nil {
			return err
		}
	}
	s.ownerFinished = true
	s.finishCalls = append(s.finishCalls, finishCall{claim: claim, state: terminalState, typed: true, ownerResult: result})
	if hook == nil {
		return nil
	}
	s.hookCalls++
	s.hookAfterOwner = s.ownerFinished
	return hook(ctx, txscope.Tx{})
}

func TestRunOnceRunsPostTransitionInsideFinishTransaction(t *testing.T) {
	store := &postTransitionStore{}
	store.claim = testClaim(workitems.KindFileExecute)
	worker := NewWorker(store, "worker-1", time.Minute)
	hookRan := false
	if err := worker.Register(workitems.KindFileExecute, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		outcome := Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed"}, nil)
		outcome.PostTransition = func(context.Context, txscope.Tx) error { hookRan = true; return nil }
		return outcome, nil
	})); err != nil {
		t.Fatal(err)
	}
	handled, err := worker.RunOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("run once: handled=%v err=%v", handled, err)
	}
	if store.hookCalls != 1 || !hookRan || !store.hookAfterOwner {
		t.Fatalf("post-transition hook must run once inside the finish path after the owner write: calls=%d ran=%v afterOwner=%v", store.hookCalls, hookRan, store.hookAfterOwner)
	}
}

func TestRunOnceFailsClosedWhenStoreLacksPostTransitionSupport(t *testing.T) {
	store := &fakeStore{claim: testClaim(workitems.KindFileExecute)}
	worker := NewWorker(store, "worker-1", time.Minute)
	if err := worker.Register(workitems.KindFileExecute, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		outcome := Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed"}, nil)
		outcome.PostTransition = func(context.Context, txscope.Tx) error { return nil }
		return outcome, nil
	})); err != nil {
		t.Fatal(err)
	}
	handled, err := worker.RunOnce(context.Background())
	if !handled || !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("a store without post-transition support must fail closed, handled=%v err=%v", handled, err)
	}
	if len(store.finishCalls) != 0 {
		t.Fatalf("unsupported post-transition must not finish: %+v", store.finishCalls)
	}
}
