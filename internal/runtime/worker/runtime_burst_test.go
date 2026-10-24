package worker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

// burstStore records every claim attempt (kind, in order) and every dispatched
// claim, and hands out work from per-kind counters that a test can top up.
type burstStore struct {
	*fakeRuntimeStore
	claimMu   sync.Mutex
	remaining map[string]int
	attempts  []string
	claimed   []string
}

func newBurstStore(remaining map[string]int) *burstStore {
	return &burstStore{fakeRuntimeStore: newFakeRuntimeStore(), remaining: remaining}
}

func (s *burstStore) ClaimDue(ctx context.Context, _ string, kinds []string, _ time.Duration) (workitems.Claim, error) {
	if err := ctx.Err(); err != nil {
		return workitems.Claim{}, err
	}
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	kind := kinds[0]
	s.attempts = append(s.attempts, kind)
	if s.remaining[kind] == 0 {
		return workitems.Claim{}, workitems.ErrNoDueWork
	}
	s.remaining[kind]--
	s.claimed = append(s.claimed, kind)
	id := fmt.Sprintf("burst-%d", len(s.claimed))
	s.mu.Lock()
	s.items[id] = &fakeItem{state: "leased", fencingToken: 1, leaseUntil: time.Now().Add(time.Minute)}
	s.mu.Unlock()
	return workitems.Claim{ID: id, Kind: kind, FencingToken: 1, Attempt: 1, MaxAttempts: 3}, nil
}

func (s *burstStore) give(kind string, n int) (claimedBefore int) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	s.remaining[kind] += n
	return len(s.claimed)
}

func (s *burstStore) snapshot() (attempts, claimed []string) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	return append([]string(nil), s.attempts...), append([]string(nil), s.claimed...)
}

func burstKinds(n int) []string {
	kinds := make([]string, n)
	for i := range kinds {
		kinds[i] = fmt.Sprintf("kind_%02d", i)
	}
	return kinds
}

func instantFinish() Handler {
	return HandlerFunc(func(context.Context, workitems.Claim) (Outcome, error) {
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
	})
}

func waitClaimed(t *testing.T, s *burstStore, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, claimed := s.snapshot(); len(claimed) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, claimed := s.snapshot()
	t.Fatalf("claimed %d/%d within %v", len(claimed), n, within)
}

// A single backlogged kind among nine is drained at handler speed, not at one
// claim per full kind rotation of ticks (the old ceiling: 9 x 250 ms per claim,
// so 200 items took 7.5 minutes).
func TestDispatcherBurstDrainsSingleKindBacklog(t *testing.T) {
	kinds := burstKinds(9)
	s := newBurstStore(map[string]int{kinds[4]: 200})
	rt := dispatchRuntime(t, LaneLight, kinds, s, instantFinish())
	rt.Start()
	defer func() { _ = rt.Drain(time.Second) }()
	waitClaimed(t, s, 200, 5*time.Second)
}

// Every attempt advances the rotation by exactly one kind, bursting or not, so
// with every kind backlogged the claims are shared evenly and no kind waits for
// more than one attempt of each other kind.
func TestDispatcherBurstRotatesKindsStrictly(t *testing.T) {
	kinds := burstKinds(5)
	remaining := map[string]int{}
	for _, k := range kinds {
		remaining[k] = 1000
	}
	s := newBurstStore(remaining)
	rt := dispatchRuntime(t, LaneLight, kinds, s, instantFinish())
	rt.Start()
	waitClaimed(t, s, 400, 5*time.Second)
	if err := rt.Drain(time.Second); err != nil {
		t.Fatal(err)
	}
	attempts, claimed := s.snapshot()
	index := map[string]int{}
	for i, k := range kinds {
		index[k] = i
	}
	for i := 1; i < len(attempts); i++ {
		if want := kinds[(index[attempts[i-1]]+1)%len(kinds)]; attempts[i] != want {
			t.Fatalf("attempt %d is %s after %s, want %s (rotation broken)", i, attempts[i], attempts[i-1], want)
		}
	}
	counts := map[string]int{}
	for _, k := range claimed {
		counts[k]++
	}
	lo, hi := len(claimed), 0
	for _, k := range kinds {
		lo, hi = min(lo, counts[k]), max(hi, counts[k])
	}
	if hi-lo > 1 {
		t.Fatalf("uneven claims across backlogged kinds: %v", counts)
	}
}

// A kind that becomes due while another kind's backlog keeps the lane bursting
// is claimed within one rotation (len(kinds) claims) of becoming due.
func TestDispatcherBurstBacklogDoesNotStarveOtherKind(t *testing.T) {
	kinds := burstKinds(9)
	s := newBurstStore(map[string]int{kinds[0]: 100000})
	rt := dispatchRuntime(t, LaneLight, kinds, s, instantFinish())
	rt.Start()
	defer func() { _ = rt.Drain(time.Second) }()
	waitClaimed(t, s, 50, 5*time.Second)
	for round := 0; round < 3; round++ {
		late := kinds[1+round*3]
		before := s.give(late, 1)
		deadline := time.Now().Add(2 * time.Second)
		for {
			_, claimed := s.snapshot()
			found := -1
			for i := before; i < len(claimed); i++ {
				if claimed[i] == late {
					found = i
					break
				}
			}
			if found >= 0 {
				if waited := found - before; waited > len(kinds) {
					t.Fatalf("%s waited %d claims behind the backlog, want <= %d", late, waited, len(kinds))
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s starved behind %s's backlog", late, kinds[0])
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// An idle lane keeps the one-attempt-per-tick cost, also after a burst ended.
func TestDispatcherIdleLaneMakesOneAttemptPerTick(t *testing.T) {
	kinds := burstKinds(9)
	s := newBurstStore(map[string]int{kinds[2]: 30})
	rt := dispatchRuntime(t, LaneLight, kinds, s, instantFinish())
	rt.Start()
	defer func() { _ = rt.Drain(time.Second) }()
	waitClaimed(t, s, 30, 5*time.Second)
	// Let the burst end (one empty rotation), then count idle attempts.
	time.Sleep(2 * claimTick)
	before, _ := s.snapshot()
	window := 8 * claimTick
	time.Sleep(window)
	after, _ := s.snapshot()
	idle := len(after) - len(before)
	if maxAttempts := int(window/claimTick) + 1; idle > maxAttempts {
		t.Fatalf("idle lane made %d claim attempts in %v, want <= %d (one per tick)", idle, window, maxAttempts)
	}
	if idle == 0 {
		t.Fatal("idle lane stopped polling")
	}
}

// A burst never claims without a free slot: with every slot held by a blocked
// handler the lane waits instead of claiming more.
func TestDispatcherBurstRespectsSlots(t *testing.T) {
	kinds := burstKinds(3)
	s := newBurstStore(map[string]int{kinds[0]: 50, kinds[1]: 50})
	release := make(chan struct{})
	started := make(chan string, 100)
	rt := dispatchRuntime(t, LaneLight, kinds, s, HandlerFunc(func(ctx context.Context, c workitems.Claim) (Outcome, error) {
		started <- c.Kind
		<-release
		return Finish(workitems.StateSucceeded, workitems.OwnerResult{}, nil), nil
	}))
	rt.Start()
	awaitDispatch(t, started, LaneLightConcurrency)
	time.Sleep(3 * claimTick)
	if _, claimed := s.snapshot(); len(claimed) != LaneLightConcurrency {
		t.Fatalf("claimed %d with %d slots held", len(claimed), LaneLightConcurrency)
	}
	close(release)
	waitClaimed(t, s, 100, 5*time.Second)
	if err := rt.Drain(time.Second); err != nil {
		t.Fatal(err)
	}
}
