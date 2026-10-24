package deployment

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func fakeRegistration(key string, handler ReconcileHandler) Registration {
	return Registration{
		Key:     key,
		Effect:  EffectIdentity{Owner: "test", Effect: key + "_effect"},
		Version: 1,
		Handler: handler,
	}
}

func handlerReturning(state ReconcileState) ReconcileHandler {
	return handlerFunc(func(ctx context.Context, req ReconcileRequest) (ReconcileResult, error) {
		return ReconcileResult{State: state, Effect: EffectNone, Reason: "fake"}, nil
	})
}

type handlerFunc func(ctx context.Context, req ReconcileRequest) (ReconcileResult, error)

func (f handlerFunc) ReconcileUnknownEffect(ctx context.Context, req ReconcileRequest) (ReconcileResult, error) {
	return f(ctx, req)
}

func TestRegistryRejectsInvalidWiring(t *testing.T) {
	ok := handlerReturning(StateCompleted)
	cases := []struct {
		name     string
		required []string
		regs     []Registration
		wantErr  string
	}{
		{name: "duplicate key", required: nil, regs: []Registration{fakeRegistration("a", ok), fakeRegistration("a", ok)}, wantErr: "duplicated"},
		{name: "missing required", required: []string{"qb_download_task"}, regs: nil, wantErr: "missing required"},
		{name: "nil handler", required: nil, regs: []Registration{{Key: "a", Effect: EffectIdentity{Owner: "t", Effect: "e"}, Version: 1}}, wantErr: "no handler"},
		{name: "bad version", required: nil, regs: []Registration{{Key: "a", Effect: EffectIdentity{Owner: "t", Effect: "e"}, Version: 0, Handler: ok}}, wantErr: "supported version"},
		{name: "empty identity", required: nil, regs: []Registration{{Key: "a", Version: 1, Handler: ok}}, wantErr: "owner and effect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRegistry(tc.required, tc.regs...)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestRegistryRequiredKeyMustBeEnabled(t *testing.T) {
	// Register all required keys so the only failure can be the disabled
	// predicate (the required-set check iterates a map, so a partial
	// registration would nondeterministically report "missing" instead).
	disabled := fakeRegistration("qb_download_task", handlerReturning(StateCompleted))
	disabled.Enabled = func() bool { return false }
	_, err := NewRegistry(DefaultRequiredKeys,
		disabled,
		fakeRegistration("artifact", handlerReturning(StateCompleted)),
		fakeRegistration("file_operation", handlerReturning(StateCompleted)))
	if err == nil || !strings.Contains(err.Error(), "must be enabled") {
		t.Fatalf("expected enabled failure, got %v", err)
	}
}

func TestRegistryEnabledEntriesIncludeRequiredAndEnabled(t *testing.T) {
	regs := []Registration{
		fakeRegistration("qb_download_task", handlerReturning(StateCompleted)),
		fakeRegistration("artifact", handlerReturning(StateCompleted)),
		fakeRegistration("file_operation", handlerReturning(StateCompleted)),
	}
	optional := fakeRegistration("cleanup_purge", handlerReturning(StateCompleted))
	optional.Enabled = func() bool { return true }
	regs = append(regs, optional)
	registry, err := NewRegistry(DefaultRequiredKeys, regs...)
	if err != nil {
		t.Fatal(err)
	}
	entries := registry.EnabledEntries()
	if len(entries) != 4 {
		t.Fatalf("expected 4 enabled entries, got %d", len(entries))
	}
	inventory := registry.Inventory()
	if len(inventory) != 4 {
		t.Fatalf("expected 4 inventory entries, got %d", len(inventory))
	}
}

func TestCheckpointSettleAndCallFor(t *testing.T) {
	checkpoint := NewCheckpoint()
	checkpoint.Settle("a", StateCompleted, ReconcileResult{State: StateCompleted, Effect: EffectNone}, "", false)
	checkpoint.Settle("b", StateBlocked, ReconcileResult{State: StateBlocked, Effect: EffectPresent, Reason: "ambiguous"}, "ambiguous", false)
	call, ok := checkpoint.CallFor("a")
	if !ok || call.State != StateCompleted || call.Sequence != 1 {
		t.Fatalf("unexpected call record: %+v ok=%v", call, ok)
	}
	call, ok = checkpoint.CallFor("b")
	if !ok || call.State != StateBlocked || call.Sequence != 2 {
		t.Fatalf("unexpected call record: %+v ok=%v", call, ok)
	}
	if checkpoint.Retries != 0 {
		t.Fatalf("unexpected retries: %d", checkpoint.Retries)
	}
	checkpoint.Settle("b", StateCompleted, ReconcileResult{State: StateCompleted, Effect: EffectNone}, "", true)
	if checkpoint.Retries != 1 {
		t.Fatalf("expected retries to increment, got %d", checkpoint.Retries)
	}
	call, _ = checkpoint.CallFor("b")
	if call.State != StateCompleted || call.Sequence != 3 {
		t.Fatalf("retry should append a new call record: %+v", call)
	}
}

func TestCheckpointRejectsUnknownProtocol(t *testing.T) {
	if _, err := UnmarshalCheckpoint([]byte(`{"protocol_version":99}`)); err == nil {
		t.Fatal("expected protocol version rejection")
	}
}

// memStore is a minimal durable in-memory store for orchestrator tests.
type memStore struct {
	runs map[string]*Run
}

func (m *memStore) Load(_ context.Context, id string) (*Run, error) {
	run, ok := m.runs[id]
	if !ok {
		return nil, ErrRunNotFound
	}
	copy := *run
	return &copy, nil
}

func (m *memStore) Create(_ context.Context, run *Run) error {
	if m.runs == nil {
		m.runs = map[string]*Run{}
	}
	copy := *run
	m.runs[run.ID] = &copy
	return nil
}

func (m *memStore) Update(_ context.Context, id string, expectedVersion int64, mutate func(*Run)) (int64, error) {
	run, ok := m.runs[id]
	if !ok {
		return 0, ErrRunNotFound
	}
	if run.Version != expectedVersion {
		return 0, errors.New("version conflict")
	}
	mutate(run)
	run.Version++
	return run.Version, nil
}

func (m *memStore) Latest(_ context.Context) (*Run, error) {
	var latest *Run
	for _, run := range m.runs {
		if latest == nil || run.UpdatedAt.After(latest.UpdatedAt) {
			copy := *run
			latest = &copy
		}
	}
	return latest, nil
}

func testPlan() Plan {
	return Plan{
		OperationID:          "op-1",
		WindowID:             "win-1",
		OldBundleIdentity:    "old-1",
		NewBundleIdentity:    "new-1",
		BackupIdentity:       "backup-1",
		SchemaIdentity:       "schema-1",
		BuildIdentity:        "build-1",
		OldBundleSmokePassed: true,
	}
}

func TestOrchestratorCallsEachHandlerOnceAndReplaySkipsCompleted(t *testing.T) {
	var calls atomic.Int32
	handler := handlerFunc(func(ctx context.Context, req ReconcileRequest) (ReconcileResult, error) {
		calls.Add(1)
		return ReconcileResult{State: StateCompleted, Effect: EffectNone}, nil
	})
	registry, err := NewRegistry(DefaultRequiredKeys,
		fakeRegistration("qb_download_task", handler),
		fakeRegistration("artifact", handler),
		fakeRegistration("file_operation", handler))
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{}
	orchestrator := NewOrchestrator(registry, store)

	first, err := orchestrator.Run(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Gate.Passed {
		t.Fatalf("gate must pass on clean run: %+v", first.Gate)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 handler calls, got %d", calls.Load())
	}

	// Replay with the same operation id must skip the already-completed calls.
	replay, err := orchestrator.Run(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Gate.Passed {
		t.Fatalf("replay gate must pass: %+v", replay.Gate)
	}
	if calls.Load() != 3 {
		t.Fatalf("replay must not re-invoke completed handlers, got %d calls", calls.Load())
	}
}

func TestOrchestratorFailClosedOnAnyNonCompletedState(t *testing.T) {
	for _, state := range []ReconcileState{StateBlocked, StateConflict, StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			bad := handlerReturning(state)
			registry, err := NewRegistry(DefaultRequiredKeys,
				fakeRegistration("qb_download_task", bad),
				fakeRegistration("artifact", handlerReturning(StateCompleted)),
				fakeRegistration("file_operation", handlerReturning(StateCompleted)))
			if err != nil {
				t.Fatal(err)
			}
			result, err := NewOrchestrator(registry, &memStore{}).Run(context.Background(), testPlan())
			if err != nil {
				t.Fatal(err)
			}
			if result.Gate.Passed {
				t.Fatalf("gate must fail closed on %s", state)
			}
		})
	}
}

func TestOrchestratorPanicFailsClosed(t *testing.T) {
	panicHandler := handlerFunc(func(ctx context.Context, req ReconcileRequest) (ReconcileResult, error) {
		panic("boom")
	})
	registry, err := NewRegistry(DefaultRequiredKeys,
		fakeRegistration("qb_download_task", panicHandler),
		fakeRegistration("artifact", handlerReturning(StateCompleted)),
		fakeRegistration("file_operation", handlerReturning(StateCompleted)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewOrchestrator(registry, &memStore{}).Run(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Passed || !result.Failed {
		t.Fatalf("panic must fail the run closed: %+v", result)
	}
}

func TestOrchestratorSmokeNotAssertedFailsClosed(t *testing.T) {
	registry, err := NewRegistry(DefaultRequiredKeys,
		fakeRegistration("qb_download_task", handlerReturning(StateCompleted)),
		fakeRegistration("artifact", handlerReturning(StateCompleted)),
		fakeRegistration("file_operation", handlerReturning(StateCompleted)))
	if err != nil {
		t.Fatal(err)
	}
	plan := testPlan()
	plan.OldBundleSmokePassed = false
	result, err := NewOrchestrator(registry, &memStore{}).Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Passed {
		t.Fatalf("gate must fail without the old-bundle smoke assertion: %+v", result.Gate)
	}
}

func TestOrchestratorRetryableThenCompletedOnReplay(t *testing.T) {
	var calls atomic.Int32
	flaky := handlerFunc(func(ctx context.Context, req ReconcileRequest) (ReconcileResult, error) {
		if calls.Add(1) == 1 {
			return ReconcileResult{State: StateRetryable, Effect: EffectUnknown, Reason: "provider unreachable"}, nil
		}
		return ReconcileResult{State: StateCompleted, Effect: EffectNone}, nil
	})
	registry, err := NewRegistry(DefaultRequiredKeys,
		fakeRegistration("qb_download_task", flaky),
		fakeRegistration("artifact", handlerReturning(StateCompleted)),
		fakeRegistration("file_operation", handlerReturning(StateCompleted)))
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{}
	orchestrator := NewOrchestrator(registry, store)
	first, err := orchestrator.Run(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if first.Gate.Passed {
		t.Fatalf("gate must fail while a handler is retryable: %+v", first.Gate)
	}
	second, err := orchestrator.Run(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Gate.Passed {
		t.Fatalf("gate must pass once the retryable handler settles: %+v", second.Gate)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected exactly 2 calls (1 retryable + 1 completed), got %d", calls.Load())
	}
}
