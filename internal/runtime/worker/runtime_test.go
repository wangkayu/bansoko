package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/workitems"
)

func TestRegistryRejectsDuplicateAndMissing(t *testing.T) {
	if _, err := NewRegistry(DefaultPolicies()...); err != nil {
		t.Fatalf("default policies must be valid: %v", err)
	}
	if _, err := NewRegistry(DefaultPolicies()...); err != nil {
		t.Fatal(err)
	}
	dupes := append(DefaultPolicies(), DefaultPolicies()[0])
	if _, err := NewRegistry(dupes...); err == nil {
		t.Fatal("duplicate kind must be rejected")
	}
	if _, err := NewRegistry(KindPolicy{Kind: "", Lane: LaneLight, Lease: time.Minute, Heartbeat: 15 * time.Second, Deadline: time.Minute, MaxAttempts: 5}); err == nil {
		t.Fatal("empty kind must be rejected")
	}
	if _, err := NewRegistry(KindPolicy{Kind: "x", Lane: "bogus", Lease: time.Minute, Heartbeat: 15 * time.Second, Deadline: time.Minute, MaxAttempts: 5}); err == nil {
		t.Fatal("invalid lane must be rejected")
	}
}

func TestRegistryCompleteFailsClosed(t *testing.T) {
	reg, err := NewRegistry(DefaultPolicies()...)
	if err != nil {
		t.Fatal(err)
	}
	// Missing handler for a registered kind.
	handlers := map[string]Handler{}
	if err := reg.Complete(handlers); err == nil {
		t.Fatal("incomplete wiring must fail")
	}
	// Extra handler without policy.
	all := map[string]Handler{}
	for _, kind := range reg.Kinds() {
		all[kind] = HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
			return Finish("succeeded", workitems.OwnerResult{}, nil), nil
		})
	}
	all["bogus-kind"] = HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) { return Outcome{}, nil })
	if err := reg.Complete(all); err == nil {
		t.Fatal("unregistered handler kind must fail wiring")
	}
}

func TestRegistryLaneGrouping(t *testing.T) {
	reg, err := NewRegistry(DefaultPolicies()...)
	if err != nil {
		t.Fatal(err)
	}
	light := reg.KindsForLane(LaneLight)
	heavy := reg.KindsForLane(LaneHeavy)
	// 4 original light kinds + calibration and the two generation consumers
	// (DB-bound, no external effect) + the post-processing plan stage (EXEC-001,
	// plans naming without any external effect) + the MF3 download-completion
	// action (idempotent qBittorrent calls) + the replacement fallback query
	// (DB-bound, enqueues evaluations only) = 10; heavy = 9 (external-effect
	// work, plus the durable offset migration execute lane and the file
	// manager's user operations).
	if len(light) != 10 || len(heavy) != 9 {
		t.Fatalf("light=%d heavy=%d, want 10/9", len(light), len(heavy))
	}
}

// fakeRuntimeStore implements RuntimeStore in memory for unit tests.
type fakeRuntimeStore struct {
	mu                   sync.Mutex
	items                map[string]*fakeItem
	renewCount           atomic.Int64
	finishCount          atomic.Int64
	retryCount           atomic.Int64
	budgetlessRetryCount atomic.Int64
	reclaimCount         atomic.Int64
	renewErr             error
	retryErr             error
	retryOutcome         workitems.RetryOutcome
	requests             []workitems.Claim
	admissionErr         error
	admissionCount       atomic.Int64
}
type fakeItem struct {
	state        string
	leaseUntil   time.Time
	fencingToken int64
	attempt      int
	cancelled    bool
	effectKey    string
}

type fallbackTestHandler struct {
	panicOnHandle   bool
	panicOnFallback bool
	afterFallback   func()
}

func (h fallbackTestHandler) Handle(context.Context, workitems.Claim) (Outcome, error) {
	if h.panicOnHandle {
		panic("ambiguous file effect")
	}
	return Outcome{}, errors.New("ambiguous file effect")
}

func (h fallbackTestHandler) RecoveryFallback(context.Context, workitems.Claim, error) (Outcome, bool) {
	if h.panicOnFallback {
		panic("fallback cannot inspect recovery")
	}
	if h.afterFallback != nil {
		h.afterFallback()
	}
	return RetryUntilResolved("fallback_recovery", "reconcile ambiguous effect", time.Second, nil), true
}

func newFakeRuntimeStore() *fakeRuntimeStore {
	return &fakeRuntimeStore{items: map[string]*fakeItem{}}
}

func (f *fakeRuntimeStore) add(kind, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[id] = &fakeItem{state: "available", leaseUntil: time.Time{}, fencingToken: 0}
}

func (f *fakeRuntimeStore) ClaimDue(ctx context.Context, workerID string, kinds []string, lease time.Duration) (workitems.Claim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for id, item := range f.items {
		due := (item.state == "available") || (item.state == "leased" && item.leaseUntil.Before(now))
		if !due {
			continue
		}
		item.state = "leased"
		item.leaseUntil = now.Add(lease)
		item.fencingToken++
		item.attempt++
		return workitems.Claim{ID: id, Kind: "rss_refresh", FencingToken: item.fencingToken, Attempt: item.attempt, MaxAttempts: 5}, nil
	}
	return workitems.Claim{}, workitems.ErrNoDueWork
}

func (f *fakeRuntimeStore) Finish(ctx context.Context, claim workitems.Claim, terminalState string, writeOwner workitems.OwnerWriter) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.items[claim.ID]
	if !ok || item.fencingToken != claim.FencingToken || item.state != "leased" {
		return workitems.ErrStaleClaim
	}
	item.state = terminalState
	f.finishCount.Add(1)
	return nil
}

func (f *fakeRuntimeStore) FinishWithResult(ctx context.Context, claim workitems.Claim, terminalState string, ownerResult workitems.OwnerResult, writeOwner workitems.OwnerWriter) error {
	return f.Finish(ctx, claim, terminalState, writeOwner)
}

func (f *fakeRuntimeStore) Retry(ctx context.Context, claim workitems.Claim, reasonCode, reason string, delay time.Duration, writeOwner workitems.OwnerWriter) (workitems.RetryOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.retryErr != nil {
		return workitems.RetryOutcome{}, f.retryErr
	}
	item, ok := f.items[claim.ID]
	if !ok || item.fencingToken != claim.FencingToken || item.state != "leased" {
		return workitems.RetryOutcome{}, workitems.ErrStaleClaim
	}
	item.state = "available"
	f.retryCount.Add(1)
	return f.retryOutcome, nil
}

func (f *fakeRuntimeStore) RetryWithoutAttemptBudget(ctx context.Context, claim workitems.Claim, reasonCode, reason string, delay time.Duration, writeOwner workitems.OwnerWriter) (workitems.RetryOutcome, error) {
	f.budgetlessRetryCount.Add(1)
	return f.Retry(ctx, claim, reasonCode, reason, delay, writeOwner)
}
func (f *fakeRuntimeStore) RenewLease(ctx context.Context, claim workitems.Claim, extendTo time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return f.renewErr
	}
	item, ok := f.items[claim.ID]
	if !ok || item.fencingToken != claim.FencingToken || item.state != "leased" || item.cancelled || item.leaseUntil.Before(time.Now()) {
		return workitems.ErrStaleClaim
	}
	item.leaseUntil = extendTo
	f.renewCount.Add(1)
	return nil
}

func (f *fakeRuntimeStore) RequestCancel(ctx context.Context, claim workitems.Claim, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, claim)
	if item, ok := f.items[claim.ID]; ok && item.fencingToken == claim.FencingToken {
		item.cancelled = true
	}
	return nil
}

func (f *fakeRuntimeStore) CancelRequests(ctx context.Context, kinds []string, reason string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, item := range f.items {
		if !item.cancelled {
			item.cancelled = true
			n++
		}
	}
	return n, nil
}

func (f *fakeRuntimeStore) AdmitManagedAnimeEffect(context.Context, workitems.Claim) error {
	f.admissionCount.Add(1)
	return f.admissionErr
}

type runtimeEventCapture struct {
	mu     sync.Mutex
	events []observabilityport.RuntimeEvent
	levels []runtimeSeverity
}

func (c *runtimeEventCapture) Debug(_ context.Context, event observabilityport.RuntimeEvent) {
	c.append(runtimeDebug, event)
}

func (c *runtimeEventCapture) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	c.append(runtimeInfo, event)
}

func (c *runtimeEventCapture) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	c.append(runtimeWarn, event)
}

func (c *runtimeEventCapture) Error(_ context.Context, event observabilityport.RuntimeEvent) {
	c.append(runtimeError, event)
}

func (c *runtimeEventCapture) append(level runtimeSeverity, event observabilityport.RuntimeEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	c.levels = append(c.levels, level)
}

func (c *runtimeEventCapture) event(reasonCode string) (observabilityport.RuntimeEvent, runtimeSeverity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, event := range c.events {
		if event.ReasonCode == reasonCode {
			return event, c.levels[i], true
		}
	}
	return observabilityport.RuntimeEvent{}, 0, false
}

func (c *runtimeEventCapture) hasReason(reasonCode string) (observabilityport.RuntimeEvent, bool) {
	event, _, ok := c.event(reasonCode)
	return event, ok
}

func testRuntimeLogger() observabilityport.RuntimeLogger {
	return observabilityport.NopRuntimeLogger{}
}

func testRegistry() *Registry {
	reg, err := NewRegistry(
		KindPolicy{Kind: "rss_refresh", Lane: LaneLight, Lease: 2 * time.Second, Heartbeat: 300 * time.Millisecond, Deadline: time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
	)
	if err != nil {
		panic(err)
	}
	return reg
}

func TestRuntimeActiveGenerationsDoNotDeleteEachOther(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["overlap"] = &fakeItem{state: "leased", fencingToken: 2}
	registry, err := NewRegistry(KindPolicy{Kind: "rss_refresh", Lane: LaneLight, Lease: time.Second, Heartbeat: 100 * time.Millisecond, Deadline: time.Second, MaxAttempts: 1, Backoff: defaultBackoff})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan int64, 2)
	// One release channel per fencing token. A single shared channel made this test
	// nondeterministic: whichever generation happened to be inside its `select` first
	// consumed the value, and a generation that received the other token returned an
	// error and tore itself down — leaving the generation the test meant to release as
	// the survivor, so the final assertion failed about one run in ten with 0.00s
	// elapsed (no timer involved). Addressing the release by token removes the race
	// without changing what the test asserts.
	releases := map[int64]chan struct{}{
		1: make(chan struct{}, 1),
		2: make(chan struct{}, 1),
	}
	rt, err := NewRuntime(store, registry, map[string]Handler{
		"rss_refresh": HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
			started <- claim.FencingToken
			select {
			case <-releases[claim.FencingToken]:
				return Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ok"}, nil), nil
			case <-ctx.Done():
				return Outcome{}, ctx.Err()
			}
		}),
	}, "worker-overlap", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	policy := registry.MustGet("rss_refresh")
	go rt.execute(context.Background(), workitems.Claim{ID: "overlap", Kind: "rss_refresh", FencingToken: 1, Attempt: 1, MaxAttempts: 1}, policy)
	go rt.execute(context.Background(), workitems.Claim{ID: "overlap", Kind: "rss_refresh", FencingToken: 2, Attempt: 1, MaxAttempts: 1}, policy)
	tokenA := <-started
	_ = <-started
	if got := rt.Status().Active; got != 2 {
		t.Fatalf("active generations = %d, want 2", got)
	}
	releases[tokenA] <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for rt.Status().Active != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := rt.Status().Active; got != 1 {
		t.Fatalf("old generation cleanup removed new generation: active=%d", got)
	}
	rt.cancelActive("drain-timeout")
	rt.wg.Wait()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.requests) != 1 || store.requests[0].FencingToken == tokenA || store.requests[0].FencingToken == 0 {
		t.Fatalf("cancel request lost live generation fencing token: released=%d requests=%+v", tokenA, store.requests)
	}
}

func TestRuntimeRetiredOwnerRunsNoHandlerOrExternalEffect(t *testing.T) {
	store := newFakeRuntimeStore()
	store.admissionErr = workitems.ErrOwnerRetired
	registry, err := NewRegistry(KindPolicy{
		Kind: workitems.KindQBSubmit, Lane: LaneHeavy, Lease: time.Minute,
		Heartbeat: 15 * time.Second, Deadline: time.Minute, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	var handlerCalls atomic.Int64
	handlers := map[string]Handler{
		workitems.KindQBSubmit: HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
			handlerCalls.Add(1)
			return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
		}),
	}
	runtime, err := NewRuntime(store, registry, handlers, "worker-retirement", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	claim := workitems.Claim{ID: "retired-item", Kind: workitems.KindQBSubmit, ManagedAnimeID: "anime-retired", OwnerLifecycleEpoch: 1, FencingToken: 1, Attempt: 1, MaxAttempts: 3}
	runtime.execute(context.Background(), claim, registry.MustGet(workitems.KindQBSubmit))
	if store.admissionCount.Load() != 1 || handlerCalls.Load() != 0 || store.finishCount.Load() != 0 || store.retryCount.Load() != 0 {
		t.Fatalf("retired gate calls=%d handler=%d finish=%d retry=%d", store.admissionCount.Load(), handlerCalls.Load(), store.finishCount.Load(), store.retryCount.Load())
	}
}

func TestRuntimeExecutesAndFinishes(t *testing.T) {
	store := newFakeRuntimeStore()
	store.add("rss_refresh", "item-1")
	handlers := map[string]Handler{
		"rss_refresh": HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
			time.Sleep(600 * time.Millisecond) // long enough for a heartbeat tick
			return Finish("succeeded", workitems.OwnerResult{State: "ok"}, nil), nil
		}),
	}
	rt, err := NewRuntime(store, testRegistry(), handlers, "worker-test", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt.Start()
	time.Sleep(1200 * time.Millisecond)
	rt.Stop()
	if store.finishCount.Load() != 1 {
		t.Fatalf("finish count = %d, want 1", store.finishCount.Load())
	}
	if store.renewCount.Load() == 0 {
		t.Fatal("heartbeat must have renewed the lease")
	}
}

func TestRuntimeCancelsHandlerOnDeadline(t *testing.T) {
	store := newFakeRuntimeStore()
	store.add("rss_refresh", "item-deadline")
	capture := &runtimeEventCapture{}
	var sawCancel atomic.Bool
	handlers := map[string]Handler{
		"rss_refresh": HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
			select {
			case <-ctx.Done():
				sawCancel.Store(true)
				return Outcome{}, ctx.Err()
			case <-time.After(5 * time.Second):
				return Finish("succeeded", workitems.OwnerResult{}, nil), nil
			}
		}),
	}
	rt, err := NewRuntime(store, testRegistry(), handlers, "worker-deadline", capture)
	if err != nil {
		t.Fatal(err)
	}
	rt.Start()
	time.Sleep(2 * time.Second)
	rt.Stop()
	if !sawCancel.Load() {
		t.Fatal("handler must observe deadline cancellation")
	}
	if store.finishCount.Load() != 0 {
		t.Fatal("cancelled handler must not finish")
	}
	if event, level, ok := capture.event("deadline_exceeded"); !ok {
		t.Fatal("deadline event was not emitted")
	} else if level != runtimeWarn || event.WorkItemID != "item-deadline" || event.Attempt != 1 || event.Retryable == nil || !*event.Retryable {
		t.Fatalf("unexpected deadline event: level=%d event=%+v", level, event)
	}
}

func TestRuntimeRecoversPanic(t *testing.T) {
	store := newFakeRuntimeStore()
	store.add("rss_refresh", "item-panic")
	capture := &runtimeEventCapture{}
	handlers := map[string]Handler{
		"rss_refresh": HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
			panic("boom")
		}),
	}
	rt, err := NewRuntime(store, testRegistry(), handlers, "worker-panic", capture)
	if err != nil {
		t.Fatal(err)
	}
	rt.Start()
	time.Sleep(800 * time.Millisecond)
	rt.Stop()
	if store.retryCount.Load() == 0 {
		t.Fatal("panic must be converted into a retry")
	}
	if event, level, ok := capture.event("handler_panic"); !ok {
		t.Fatal("panic event was not emitted")
	} else if level != runtimeWarn || event.WorkItemID != "item-panic" || event.ErrorClass != "handler_panic" || event.Retryable == nil || !*event.Retryable {
		t.Fatalf("unexpected panic event: level=%d event=%+v", level, event)
	}
}

func TestRuntimeLogsHandlerErrorAtAttemptBudget(t *testing.T) {
	for _, test := range []struct {
		name      string
		attempt   int
		wantLevel runtimeSeverity
		wantRetry bool
	}{
		{name: "retryable", attempt: 1, wantLevel: runtimeWarn, wantRetry: true},
		{name: "terminal", attempt: 3, wantLevel: runtimeError, wantRetry: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeRuntimeStore()
			store.items["item-handler-error"] = &fakeItem{state: "leased", fencingToken: 1}
			capture := &runtimeEventCapture{}
			handlers := map[string]Handler{
				"rss_refresh": HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
					return Outcome{}, errors.New("handler error must not be logged")
				}),
			}
			rt, err := NewRuntime(store, testRegistry(), handlers, "worker-handler-error", capture)
			if err != nil {
				t.Fatal(err)
			}
			rt.handleWithRecovery(context.Background(), workitems.Claim{
				ID: "item-handler-error", Kind: "rss_refresh", FencingToken: 1,
				Attempt: test.attempt, MaxAttempts: 3,
			}, nil)
			event, level, ok := capture.event("handler_error")
			if !ok {
				t.Fatal("handler error event was not emitted")
			}
			if level != test.wantLevel || event.Retryable == nil || *event.Retryable != test.wantRetry {
				t.Fatalf("handler error = level %d event %+v, want level %d retryable %t", level, event, test.wantLevel, test.wantRetry)
			}
		})
	}
}

func TestRuntimeRecoveryFallbackBypassesAttemptBudget(t *testing.T) {
	for _, panicOnHandle := range []bool{false, true} {
		name := "error"
		if panicOnHandle {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			store := newFakeRuntimeStore()
			store.items["item-file-recovery"] = &fakeItem{state: "leased", fencingToken: 1}
			capture := &runtimeEventCapture{}
			registry, err := NewRegistry(KindPolicy{
				Kind: "file_execute", Lane: LaneHeavy, Lease: 2 * time.Second, Heartbeat: time.Second,
				Deadline: time.Second, MaxAttempts: 1, Backoff: defaultBackoff,
			})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := NewRuntime(store, registry, map[string]Handler{
				"file_execute": fallbackTestHandler{panicOnHandle: panicOnHandle},
			}, "worker-file-recovery", capture)
			if err != nil {
				t.Fatal(err)
			}
			rt.handleWithRecovery(context.Background(), workitems.Claim{
				ID: "item-file-recovery", Kind: "file_execute", FencingToken: 1,
				Owner:         workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "operation-1"},
				InputSnapshot: json.RawMessage(`{"operation_id":"operation-1","recovery_mode":"overwrite"}`),
				Attempt:       1, MaxAttempts: 1,
			}, nil)
			if store.budgetlessRetryCount.Load() != 1 || store.retryCount.Load() != 1 {
				t.Fatalf("recovery fallback did not use only budgetless retry: budgetless=%d ordinary=%d", store.budgetlessRetryCount.Load(), store.retryCount.Load())
			}
			if _, ok := capture.hasReason(map[bool]string{false: "handler_error", true: "handler_panic"}[panicOnHandle]); !ok {
				t.Fatal("handler failure event was not emitted")
			}
		})
	}
}

func TestRuntimeRecoveryFallbackCancellationAfterFallbackDoesNotTransition(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["item-file-recovery-cancel"] = &fakeItem{state: "leased", fencingToken: 1}
	capture := &runtimeEventCapture{}
	registry, err := NewRegistry(KindPolicy{Kind: "file_execute", Lane: LaneHeavy, Lease: 2 * time.Second, Heartbeat: time.Second, Deadline: time.Second, MaxAttempts: 1, Backoff: defaultBackoff})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := NewRuntime(store, registry, map[string]Handler{
		"file_execute": fallbackTestHandler{afterFallback: cancel},
	}, "worker-file-recovery-cancel", capture)
	if err != nil {
		t.Fatal(err)
	}
	rt.handleWithRecovery(ctx, workitems.Claim{ID: "item-file-recovery-cancel", Kind: "file_execute", FencingToken: 1, Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "operation-1"}, InputSnapshot: json.RawMessage(`{"operation_id":"operation-1","recovery_mode":"overwrite"}`), Attempt: 1, MaxAttempts: 1}, nil)
	if store.finishCount.Load() != 0 || store.retryCount.Load() != 0 || store.budgetlessRetryCount.Load() != 0 {
		t.Fatalf("canceled recovery fallback transitioned work: finish=%d retry=%d budgetless=%d", store.finishCount.Load(), store.retryCount.Load(), store.budgetlessRetryCount.Load())
	}
}

func TestRuntimeRecoveryFallbackPanicLeavesClaimForReclaim(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["item-file-recovery-fallback-panic"] = &fakeItem{state: "leased", fencingToken: 1}
	capture := &runtimeEventCapture{}
	registry, err := NewRegistry(KindPolicy{Kind: "file_execute", Lane: LaneHeavy, Lease: 2 * time.Second, Heartbeat: time.Second, Deadline: time.Second, MaxAttempts: 1, Backoff: defaultBackoff})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(store, registry, map[string]Handler{
		"file_execute": fallbackTestHandler{panicOnFallback: true},
	}, "worker-file-recovery-fallback-panic", capture)
	if err != nil {
		t.Fatal(err)
	}
	claim := workitems.Claim{ID: "item-file-recovery-fallback-panic", Kind: "file_execute", FencingToken: 1, Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "operation-1"}, InputSnapshot: json.RawMessage(`{"operation_id":"operation-1","recovery_mode":"overwrite"}`), Attempt: 1, MaxAttempts: 1}
	rt.handleWithRecovery(context.Background(), claim, nil)
	if store.finishCount.Load() != 0 || store.retryCount.Load() != 0 || store.budgetlessRetryCount.Load() != 0 {
		t.Fatalf("fallback panic transitioned work: finish=%d retry=%d budgetless=%d", store.finishCount.Load(), store.retryCount.Load(), store.budgetlessRetryCount.Load())
	}
	if _, ok := capture.hasReason("recovery_fallback_panic"); !ok {
		t.Fatal("fallback panic event was not emitted")
	}
}

func TestRuntimeExecuteCleansActiveClaimAfterFallbackPanic(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["item-execute-cleanup"] = &fakeItem{state: "leased", fencingToken: 1}
	registry, err := NewRegistry(KindPolicy{Kind: "file_execute", Lane: LaneHeavy, Lease: 2 * time.Second, Heartbeat: time.Second, Deadline: time.Second, MaxAttempts: 1, Backoff: defaultBackoff})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(store, registry, map[string]Handler{
		"file_execute": fallbackTestHandler{panicOnFallback: true},
	}, "worker-execute-cleanup", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt.execute(context.Background(), workitems.Claim{ID: "item-execute-cleanup", Kind: "file_execute", FencingToken: 1, Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "operation-1"}, InputSnapshot: json.RawMessage(`{"operation_id":"operation-1","recovery_mode":"overwrite"}`), Attempt: 1, MaxAttempts: 1}, registry.MustGet("file_execute"))
	if status := rt.Status(); status.Active != 0 {
		t.Fatalf("active claim leaked after fallback panic: %+v", status)
	}
}

func TestRuntimeOrdinaryHandlerFallbackUsesAttemptBudget(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["item-ordinary-recovery"] = &fakeItem{state: "leased", fencingToken: 1}
	capture := &runtimeEventCapture{}
	registry, err := NewRegistry(KindPolicy{
		Kind: "rss_refresh", Lane: LaneLight, Lease: 2 * time.Second, Heartbeat: time.Second,
		Deadline: time.Second, MaxAttempts: 1, Backoff: defaultBackoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(store, registry, map[string]Handler{
		"rss_refresh": HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
			return Outcome{}, errors.New("ordinary handler effect is uncertain")
		}),
	}, "worker-ordinary-recovery", capture)
	if err != nil {
		t.Fatal(err)
	}
	rt.handleWithRecovery(context.Background(), workitems.Claim{
		ID: "item-ordinary-recovery", Kind: "rss_refresh", FencingToken: 1,
		Attempt: 1, MaxAttempts: 1,
	}, nil)
	if store.budgetlessRetryCount.Load() != 0 || store.retryCount.Load() != 1 {
		t.Fatalf("ordinary handler fallback used budgetless retry: budgetless=%d ordinary=%d", store.budgetlessRetryCount.Load(), store.retryCount.Load())
	}
}

func TestRuntimeLogsHeartbeatFailureAsWarnWhileRetryable(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["item-heartbeat"] = &fakeItem{state: "leased", fencingToken: 1, leaseUntil: time.Now().Add(time.Second)}
	store.renewErr = errors.New("lease persistence failed")
	capture := &runtimeEventCapture{}
	policy, err := NewRegistry(KindPolicy{
		Kind:        "rss_refresh",
		Lane:        LaneLight,
		Lease:       100 * time.Millisecond,
		Heartbeat:   10 * time.Millisecond,
		Deadline:    time.Second,
		MaxAttempts: 3,
		Backoff:     defaultBackoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(store, policy, map[string]Handler{
		"rss_refresh": HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
			<-ctx.Done()
			return Outcome{}, ctx.Err()
		}),
	}, "worker-heartbeat", capture)
	if err != nil {
		t.Fatal(err)
	}
	rt.execute(context.Background(), workitems.Claim{
		ID: "item-heartbeat", Kind: "rss_refresh", FencingToken: 1, Attempt: 1, MaxAttempts: 3,
	}, policy.MustGet("rss_refresh"))
	event, level, ok := capture.event("lease_heartbeat_failed")
	if !ok {
		t.Fatal("heartbeat failure event was not emitted")
	}
	if level != runtimeWarn || event.Retryable == nil || !*event.Retryable {
		t.Fatalf("heartbeat event = level %d %+v, want warn/retryable", level, event)
	}
}

func TestRuntimeDoesNotTransitionCanceledHandlerErrorAtMaxAttempts(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["item-canceled"] = &fakeItem{state: "leased", fencingToken: 1}
	capture := &runtimeEventCapture{}
	registry, err := NewRegistry(KindPolicy{Kind: "rss_refresh", Lane: LaneLight, Lease: 2 * time.Second, Heartbeat: time.Second, Deadline: time.Second, MaxAttempts: 1, Backoff: defaultBackoff})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt, err := NewRuntime(store, registry, map[string]Handler{
		"rss_refresh": HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
			return Outcome{}, context.Canceled
		}),
	}, "worker-canceled", capture)
	if err != nil {
		t.Fatal(err)
	}
	rt.handleWithRecovery(ctx, workitems.Claim{ID: "item-canceled", Kind: "rss_refresh", FencingToken: 1, Attempt: 1, MaxAttempts: 1}, nil)
	if store.finishCount.Load() != 0 || store.retryCount.Load() != 0 || store.budgetlessRetryCount.Load() != 0 {
		t.Fatalf("canceled handler transitioned work: finish=%d retry=%d budgetless=%d", store.finishCount.Load(), store.retryCount.Load(), store.budgetlessRetryCount.Load())
	}
}

func TestRuntimeDoesNotTransitionCanceledHandlerPanic(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["item-canceled-panic"] = &fakeItem{state: "leased", fencingToken: 1}
	capture := &runtimeEventCapture{}
	registry, err := NewRegistry(KindPolicy{Kind: "rss_refresh", Lane: LaneLight, Lease: 2 * time.Second, Heartbeat: time.Second, Deadline: time.Second, MaxAttempts: 1, Backoff: defaultBackoff})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	handlers := map[string]Handler{"rss_refresh": HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		cancel()
		panic("canceled effect")
	})}
	rt, err := NewRuntime(store, registry, handlers, "worker-canceled-panic", capture)
	if err != nil {
		t.Fatal(err)
	}
	rt.handleWithRecovery(ctx, workitems.Claim{ID: "item-canceled-panic", Kind: "rss_refresh", FencingToken: 1, Attempt: 1, MaxAttempts: 1}, nil)
	if store.finishCount.Load() != 0 || store.retryCount.Load() != 0 || store.budgetlessRetryCount.Load() != 0 {
		t.Fatalf("canceled panic transitioned work: finish=%d retry=%d budgetless=%d", store.finishCount.Load(), store.retryCount.Load(), store.budgetlessRetryCount.Load())
	}
}

func TestRuntimeApplyOutcomeUsesRetryOutcome(t *testing.T) {
	store := newFakeRuntimeStore()
	store.items["item-retry"] = &fakeItem{state: "leased", fencingToken: 1}
	capture := &runtimeEventCapture{}
	rt, err := NewRuntime(store, testRegistry(), map[string]Handler{
		"rss_refresh": HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
			return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
		}),
	}, "worker-retry", capture)
	if err != nil {
		t.Fatal(err)
	}
	claim := workitems.Claim{ID: "item-retry", Kind: "rss_refresh", FencingToken: 1, Attempt: 1, MaxAttempts: 3}
	rt.applyOutcome(context.Background(), claim, Retry("handler_error", "private handler reason", time.Second, nil), nil)
	if event, level, ok := capture.event("retry_scheduled"); !ok {
		t.Fatal("retry_scheduled event was not emitted")
	} else if level != runtimeWarn || event.Retryable == nil || !*event.Retryable {
		t.Fatalf("scheduled retry = level %d %+v, want warn/retryable", level, event)
	}

	store.mu.Lock()
	store.items["item-retry"].state = "leased"
	store.retryOutcome = workitems.RetryOutcome{Terminal: true}
	store.mu.Unlock()
	claim.Attempt = claim.MaxAttempts
	rt.applyOutcome(context.Background(), claim, Retry("handler_error", "private handler reason", time.Second, nil), nil)
	if event, level, ok := capture.event("retry_exhausted"); !ok {
		t.Fatal("retry_exhausted event was not emitted")
	} else if level != runtimeError || event.Retryable == nil || *event.Retryable {
		t.Fatalf("exhausted retry = level %d %+v, want error/non-retryable", level, event)
	}

	store.mu.Lock()
	store.items["item-retry"].state = "leased"
	store.retryOutcome = workitems.RetryOutcome{}
	store.mu.Unlock()
	rt.applyOutcome(context.Background(), claim, RetryUntilResolved("recovery_pending", "recovery is still required", time.Second, nil), nil)
	if store.items["item-retry"].state != "available" {
		t.Fatalf("recovery retry state = %q, want available", store.items["item-retry"].state)
	}
}

func TestRuntimeDrainWaitsForActiveHandlers(t *testing.T) {
	store := newFakeRuntimeStore()
	store.add("rss_refresh", "item-drain")
	var finished atomic.Bool
	handlers := map[string]Handler{
		"rss_refresh": HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
			time.Sleep(500 * time.Millisecond) // under 1s deadline, over heartbeat
			finished.Store(true)
			return Finish("succeeded", workitems.OwnerResult{}, nil), nil
		}),
	}
	rt, err := NewRuntime(store, testRegistry(), handlers, "worker-drain", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt.Start()
	time.Sleep(300 * time.Millisecond) // claim dispatched; handler still running
	start := time.Now()
	if err := rt.Drain(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("drain must wait for the active handler to finish")
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("drain returned too early")
	}
	if store.finishCount.Load() != 1 {
		t.Fatalf("finish count = %d, want 1", store.finishCount.Load())
	}
	if rt.Status().State != StateStopped {
		t.Fatalf("state = %s, want stopped", rt.Status().State)
	}
}

func TestRuntimeStatusReportsLanes(t *testing.T) {
	store := newFakeRuntimeStore()
	handlers := map[string]Handler{
		"rss_refresh": HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
			return Finish("succeeded", workitems.OwnerResult{}, nil), nil
		}),
	}
	rt, err := NewRuntime(store, testRegistry(), handlers, "worker-status", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt.Start()
	status := rt.Status()
	if status.State != StateServing {
		t.Fatalf("state = %s, want serving", status.State)
	}
	if _, ok := status.Lanes[LaneLight]; !ok {
		t.Fatal("light lane missing from status")
	}
	rt.Stop()
}

func TestRuntimeRejectsIncompleteWiring(t *testing.T) {
	store := newFakeRuntimeStore()
	if _, err := NewRuntime(store, testRegistry(), map[string]Handler{}, "worker-incomplete", testRuntimeLogger()); err == nil {
		t.Fatal("incomplete wiring must fail at construction")
	}
	// Unregistered handler kind must also fail.
	if _, err := NewRuntime(store, testRegistry(), map[string]Handler{"unregistered": HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) { return Outcome{}, nil })}, "worker-x", testRuntimeLogger()); err == nil {
		t.Fatal("unregistered kind wiring must fail at construction")
	}
}

var _ = errors.Is
