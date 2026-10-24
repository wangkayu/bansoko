package worker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

// Unlike fakeRuntimeStore, this fake honors the requested kind and never
// reclaims. Expected concurrency comes from barriers, not runtime internals.
type dispatchStore struct {
	*fakeRuntimeStore
	claimMu   sync.Mutex
	remaining map[string]int
	claims    int
	block     <-chan struct{}
	entered   chan struct{}
}

func (s *dispatchStore) ClaimDue(ctx context.Context, _ string, kinds []string, _ time.Duration) (workitems.Claim, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	if s.block != nil {
		<-s.block
	}
	if err := ctx.Err(); err != nil {
		return workitems.Claim{}, err
	}
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	kind := kinds[0]
	if s.remaining[kind] == 0 {
		return workitems.Claim{}, workitems.ErrNoDueWork
	}
	s.remaining[kind]--
	s.claims++
	id := fmt.Sprintf("dispatch-%d", s.claims)
	s.mu.Lock()
	s.items[id] = &fakeItem{state: "leased", fencingToken: 1, leaseUntil: time.Now().Add(time.Minute)}
	s.mu.Unlock()
	return workitems.Claim{ID: id, Kind: kind, FencingToken: 1, Attempt: 1, MaxAttempts: 3}, nil
}
func dispatchRuntime(t *testing.T, lane Lane, kinds []string, s RuntimeStore, h Handler) *Runtime {
	t.Helper()
	var policies []KindPolicy
	handlers := map[string]Handler{}
	for _, kind := range kinds {
		policies = append(policies, KindPolicy{Kind: kind, Lane: lane, Lease: time.Minute, Heartbeat: 15 * time.Second, Deadline: time.Minute, MaxAttempts: 3})
		handlers[kind] = h
	}
	reg, err := NewRegistry(policies...)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(s, reg, handlers, "barrier", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	return rt
}
func awaitDispatch(t *testing.T, started <-chan string, n int) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-timer.C:
			t.Fatalf("only %d/%d handlers reached barrier", i, n)
		}
	}
}
func TestDispatcherBarrierCapacityAndNoClaimHoarding(t *testing.T) {
	for _, lane := range []Lane{LaneLight, LaneHeavy} {
		t.Run(string(lane), func(t *testing.T) {
			capacity := LaneLightConcurrency
			if lane == LaneHeavy {
				capacity = LaneHeavyConcurrency
			}
			s := &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{"rss_refresh": capacity + 1}}
			started := make(chan string, capacity+1)
			release := make(chan struct{})
			rt := dispatchRuntime(t, lane, []string{"rss_refresh"}, s, HandlerFunc(func(ctx context.Context, c workitems.Claim) (Outcome, error) {
				started <- c.Kind
				<-release
				return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
			}))
			rt.Start()
			defer func() { close(release); _ = rt.Drain(time.Second) }()
			awaitDispatch(t, started, capacity)
			time.Sleep(350 * time.Millisecond)
			s.claimMu.Lock()
			claims := s.claims
			s.claimMu.Unlock()
			if claims != capacity {
				t.Fatalf("claimed %d with %d slots", claims, capacity)
			}
			select {
			case <-started:
				t.Fatal("capacity exceeded")
			default:
			}
		})
	}
}
func TestDispatcherKindFairnessBehindSlowKind(t *testing.T) {
	s := &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{"rss_refresh": 10, "qb_sync": 1}}
	started := make(chan string, 10)
	release := make(chan struct{})
	rt := dispatchRuntime(t, LaneLight, []string{"rss_refresh", "qb_sync"}, s, HandlerFunc(func(ctx context.Context, c workitems.Claim) (Outcome, error) {
		started <- c.Kind
		<-release
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
	}))
	rt.Start()
	defer func() { close(release); _ = rt.Drain(time.Second) }()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case kind := <-started:
			if kind == "qb_sync" {
				return
			}
		case <-timer.C:
			t.Fatal("slow kind starved next kind")
		}
	}
}
func TestDrainDeadlineIncludesBlockedClaimAndDoesNotLie(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	s := &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{"rss_refresh": 1}, block: release, entered: entered}
	rt := dispatchRuntime(t, LaneLight, []string{"rss_refresh"}, s, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		t.Error("handler started after drain")
		return Outcome{}, nil
	}))
	rt.Start()
	<-entered
	result := make(chan error, 1)
	go func() { result <- rt.Drain(40 * time.Millisecond) }()
	select {
	case err := <-result:
		if err == nil {
			t.Error("blocked loop reported stopped")
		}
	case <-time.After(400 * time.Millisecond):
		close(release)
		<-result
		t.Fatal("drain deadline excludes claim loop")
	}
	if rt.Status().State != StateDraining {
		t.Error("unfinished loop must remain draining")
	}
	close(release)
	if err := rt.Drain(time.Second); err != nil {
		t.Fatal(err)
	}
}
func TestDrainUncooperativeHandlerRemainsActive(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 1)
	s := &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{"rss_refresh": 1}}
	rt := dispatchRuntime(t, LaneLight, []string{"rss_refresh"}, s, HandlerFunc(func(ctx context.Context, c workitems.Claim) (Outcome, error) {
		started <- c.Kind
		<-release
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
	}))
	rt.Start()
	awaitDispatch(t, started, 1)
	result := make(chan error, 1)
	go func() { result <- rt.Drain(40 * time.Millisecond) }()
	select {
	case err := <-result:
		if err == nil {
			t.Error("uncooperative handler reported stopped")
		}
	case <-time.After(400 * time.Millisecond):
		close(release)
		<-result
		t.Fatal("unbounded wait after cancellation")
	}
	if status := rt.Status(); status.State != StateDraining || status.Active != 1 {
		t.Errorf("false quiescence: %+v", status)
	}
	close(release)
	if err := rt.Drain(time.Second); err != nil {
		t.Fatal(err)
	}
	if s.finishCount.Load() != 0 {
		t.Fatal("cancelled handler committed success")
	}
}

func TestDispatcherFileEffectGateReportsSerialCapacity(t *testing.T) {
	s := &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{}}
	rt := dispatchRuntime(t, LaneHeavy, []string{workitems.KindFileExecute}, s, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) { return Outcome{}, nil }))
	rt.Start()
	if got := rt.Status().Lanes[LaneHeavy].Concurrency; got != 1 {
		t.Fatalf("unaccepted file-effect concurrency = %d, want 1", got)
	}
	if err := rt.Drain(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherStartDrainRegistrationRace(t *testing.T) {
	for i := 0; i < 50; i++ {
		s := &dispatchStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: map[string]int{}}
		rt := dispatchRuntime(t, LaneLight, []string{"rss_refresh"}, s, HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) { return Outcome{}, nil }))
		gate := make(chan struct{})
		var callers sync.WaitGroup
		callers.Add(3)
		go func() { defer callers.Done(); <-gate; rt.Start() }()
		go func() { defer callers.Done(); <-gate; _ = rt.Drain(time.Second) }()
		go func() {
			defer callers.Done()
			<-gate
			rt.execute(context.Background(), workitems.Claim{ID: "racing", Kind: "rss_refresh", FencingToken: 1}, rt.registry.MustGet("rss_refresh"))
		}()
		close(gate)
		callers.Wait()
		if err := rt.Drain(time.Second); err != nil {
			t.Fatal(err)
		}
		rt.Start()
		if status := rt.Status(); status.State != StateStopped || status.Active != 0 {
			t.Fatalf("restart after drain: %+v", status)
		}
	}
}

type panicAdmissionStore struct{ *dispatchStore }

func (s *panicAdmissionStore) AdmitManagedAnimeEffect(context.Context, workitems.Claim) error {
	panic("admission panic")
}

func TestRuntimeAdmissionPanicReleasesRegistration(t *testing.T) {
	s := &panicAdmissionStore{&dispatchStore{fakeRuntimeStore: newFakeRuntimeStore()}}
	reg := testRegistry()
	rt, err := NewRuntime(s, reg, map[string]Handler{"rss_refresh": HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		t.Error("effect after gate panic")
		return Outcome{}, nil
	})}, "panic", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt.execute(context.Background(), workitems.Claim{ID: "panic", Kind: "rss_refresh", ManagedAnimeID: "anime", FencingToken: 1}, reg.MustGet("rss_refresh"))
	if err := rt.Drain(time.Second); err != nil {
		t.Fatal(err)
	}
	if rt.Status().Active != 0 {
		t.Fatal("panic leaked active registration")
	}
}

type blockedRenewStore struct {
	*fakeRuntimeStore
	entered chan struct{}
	release <-chan struct{}
}

func (s *blockedRenewStore) RenewLease(context.Context, workitems.Claim, time.Time) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return workitems.ErrStaleClaim
}

func TestDrainDoesNotHideBlockedHeartbeatGoroutine(t *testing.T) {
	release := make(chan struct{})
	s := &blockedRenewStore{fakeRuntimeStore: newFakeRuntimeStore(), entered: make(chan struct{}, 1), release: release}
	reg, err := NewRegistry(KindPolicy{Kind: "rss_refresh", Lane: LaneLight, Lease: time.Second, Heartbeat: 10 * time.Millisecond, Deadline: time.Second, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(s, reg, map[string]Handler{"rss_refresh": HandlerFunc(func(ctx context.Context, _ workitems.Claim) (Outcome, error) {
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	})}, "blocked-heartbeat", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	go rt.execute(context.Background(), workitems.Claim{ID: "heartbeat", Kind: "rss_refresh", FencingToken: 1}, reg.MustGet("rss_refresh"))
	<-s.entered
	if err := rt.Drain(40 * time.Millisecond); err == nil {
		t.Error("blocked heartbeat reported quiescent")
	}
	if rt.Status().Active != 1 {
		t.Error("heartbeat still running but active registration removed")
	}
	close(release)
	if err := rt.Drain(time.Second); err != nil {
		t.Fatal(err)
	}
}
