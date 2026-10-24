package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
	maintenancepkg "bansoko/internal/runtime/maintenance"
)

type fakeRuntime struct {
	startCalls atomic.Int64
	drainCalls atomic.Int64
	drainErr   error
	drainHook  func()
}

func (f *fakeRuntime) Start() { f.startCalls.Add(1) }
func (f *fakeRuntime) Drain(time.Duration) error {
	f.drainCalls.Add(1)
	if f.drainHook != nil {
		f.drainHook()
	}
	return f.drainErr
}

func TestBackgroundRuntimeRejectsIncompleteWiring(t *testing.T) {
	tests := []struct {
		name    string
		runtime *BackgroundRuntime
	}{
		{
			name:    "nil maintenance gate",
			runtime: NewBackgroundRuntime(nil, func() (RuntimeLike, error) { return &fakeRuntime{}, nil }, nil, nil),
		},
		{
			name:    "nil worker builder",
			runtime: NewBackgroundRuntime(maintenancepkg.NewGate(), nil, nil, nil),
		},
		{
			name: "nil producer",
			runtime: NewBackgroundRuntime(
				maintenancepkg.NewGate(),
				func() (RuntimeLike, error) { return &fakeRuntime{}, nil },
				[]func(context.Context){nil},
				nil,
			),
		},
		{
			name: "nil built worker",
			runtime: NewBackgroundRuntime(
				maintenancepkg.NewGate(),
				func() (RuntimeLike, error) { return nil, nil },
				nil,
				nil,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.runtime.Start(); !errors.Is(err, ErrBackgroundRuntimeInvalid) {
				t.Fatalf("Start error = %v, want ErrBackgroundRuntimeInvalid", err)
			}
			if test.runtime.Active() {
				t.Fatal("invalid runtime must remain inactive")
			}
		})
	}
}

func TestBackgroundRuntimeNilReceiverFailsClosed(t *testing.T) {
	var runtime *BackgroundRuntime
	if err := runtime.Start(); !errors.Is(err, ErrBackgroundRuntimeInvalid) {
		t.Fatalf("nil receiver Start error = %v, want ErrBackgroundRuntimeInvalid", err)
	}
}

func TestBackgroundRuntimeBuilderFailureClosesMaintenanceGate(t *testing.T) {
	gate := maintenancepkg.NewGate()
	want := errors.New("worker construction failed")
	runtime := NewBackgroundRuntime(gate, func() (RuntimeLike, error) { return nil, want }, nil, nil)
	if err := runtime.Start(); !errors.Is(err, want) {
		t.Fatalf("Start error = %v, want %v", err, want)
	}
	if !gate.RejectWrites() {
		t.Fatal("failed restart must leave the write gate closed")
	}
	if runtime.Active() {
		t.Fatal("failed restart must remain inactive")
	}
}

// TestBackgroundRuntimeStartsWorkersAndProducersWhenServing proves a serving
// instance boots the worker and producers exactly once.
func TestBackgroundRuntimeStartsWorkersAndProducersWhenServing(t *testing.T) {
	gate := maintenancepkg.NewGate()
	worker := &fakeRuntime{}
	producerStarted := make(chan struct{})
	var producerStarts atomic.Int64
	var producerStartOnce sync.Once
	var builds atomic.Int64
	runtime := NewBackgroundRuntime(gate,
		func() (RuntimeLike, error) { builds.Add(1); return worker, nil },
		[]func(context.Context){func(context.Context) {
			producerStarts.Add(1)
			producerStartOnce.Do(func() { close(producerStarted) })
		}}, observabilityport.NopRuntimeLogger{})
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if !runtime.Active() {
		t.Fatal("runtime must be active after start")
	}
	if builds.Load() != 1 || worker.startCalls.Load() != 1 {
		t.Fatalf("expected one build/start, got build=%d workerStart=%d", builds.Load(), worker.startCalls.Load())
	}
	select {
	case <-producerStarted:
	case <-time.After(time.Second):
		t.Fatal("producer was not launched")
	}
	if producerStarts.Load() != 1 {
		t.Fatalf("expected one producer launch, got %d", producerStarts.Load())
	}
	// Idempotent second start.
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 1 || worker.startCalls.Load() != 1 || producerStarts.Load() != 1 {
		t.Fatalf("second start must be a no-op: build=%d workerStart=%d producer=%d", builds.Load(), worker.startCalls.Load(), producerStarts.Load())
	}
	runtime.Stop()
}

func TestBackgroundRuntimeCancelsProducersBeforeWorkerDrain(t *testing.T) {
	gate := maintenancepkg.NewGate()
	producerStarted := make(chan struct{})
	producerCancelled := make(chan struct{})
	var startOnce sync.Once
	var cancelOnce sync.Once
	worker := &fakeRuntime{}
	worker.drainHook = func() {
		select {
		case <-producerCancelled:
		case <-time.After(time.Second):
			t.Error("worker drain began before producer cancellation")
		}
	}
	runtime := NewBackgroundRuntime(gate, func() (RuntimeLike, error) { return worker, nil }, []func(context.Context){func(ctx context.Context) {
		startOnce.Do(func() { close(producerStarted) })
		<-ctx.Done()
		cancelOnce.Do(func() { close(producerCancelled) })
	}}, nil)
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-producerStarted:
	case <-time.After(time.Second):
		t.Fatal("producer was not launched")
	}
	runtime.Stop()
	select {
	case <-producerCancelled:
	default:
		t.Fatal("producer did not observe cancellation")
	}
}

func TestBackgroundRuntimeRefusesStartInMaintenance(t *testing.T) {
	gate := maintenancepkg.NewGate()
	gate.EnterMaintenance()
	gate.SetAutomationResumeGate(false)
	worker := &fakeRuntime{}
	var builds, producers atomic.Int64
	runtime := NewBackgroundRuntime(gate,
		func() (RuntimeLike, error) { builds.Add(1); return worker, nil },
		[]func(context.Context){func(context.Context) { producers.Add(1) }}, observabilityport.NopRuntimeLogger{})
	if err := runtime.Start(); err == nil {
		t.Fatal("start in maintenance must be refused")
	}
	if runtime.Active() || builds.Load() != 0 || producers.Load() != 0 {
		t.Fatalf("maintenance boot must not build/start anything: active=%v build=%d producer=%d", runtime.Active(), builds.Load(), producers.Load())
	}
}

// TestBackgroundRuntimeRestartsFreshWorkerAfterStop proves a maintenance
// cycle builds a NEW worker on the next serving period (a drained runtime
// cannot be restarted in-process).
func TestBackgroundRuntimeRestartsFreshWorkerAfterStop(t *testing.T) {
	gate := maintenancepkg.NewGate()
	var producers atomic.Int64
	var workers []*fakeRuntime
	runtime := NewBackgroundRuntime(gate,
		func() (RuntimeLike, error) {
			w := &fakeRuntime{}
			workers = append(workers, w)
			return w, nil
		},
		[]func(context.Context){func(context.Context) { producers.Add(1) }}, observabilityport.NopRuntimeLogger{})
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	runtime.Stop()
	if runtime.Active() {
		t.Fatal("runtime must be inactive after stop")
	}
	if len(workers) != 1 || workers[0].drainCalls.Load() != 1 {
		t.Fatalf("stop must drain the worker exactly once: workers=%d drain=%d", len(workers), workers[0].drainCalls.Load())
	}
	// Serving again: a fresh worker is built.
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 {
		t.Fatalf("second serving period must build a fresh worker, got %d", len(workers))
	}
	runtime.Stop()
}

// TestBackgroundRuntimeDrainForwardsToCurrentWorker proves the claim drain
// step reaches the current worker.
func TestBackgroundRuntimeDrainForwardsToCurrentWorker(t *testing.T) {
	gate := maintenancepkg.NewGate()
	worker := &fakeRuntime{drainErr: errors.New("deadline exceeded")}
	runtime := NewBackgroundRuntime(gate, func() (RuntimeLike, error) { return worker, nil }, nil, observabilityport.NopRuntimeLogger{})
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	drained, err := runtime.Drain(10 * time.Second)
	if drained || !errors.Is(err, worker.drainErr) {
		t.Fatalf("drain result = drained=%v err=%v, want false and %v", drained, err, worker.drainErr)
	}
	if worker.drainCalls.Load() != 1 {
		t.Fatalf("drain must reach the worker, got %d calls", worker.drainCalls.Load())
	}
	runtime.Stop()
}

// TestBackgroundRuntimeDrainWithoutWorkerIsDrained proves drain is idempotent
// when the runtime is already stopped (no worker).
func TestBackgroundRuntimeDrainWithoutWorkerIsDrained(t *testing.T) {
	gate := maintenancepkg.NewGate()
	runtime := NewBackgroundRuntime(gate, func() (RuntimeLike, error) { return &fakeRuntime{}, nil }, nil, observabilityport.NopRuntimeLogger{})
	drained, err := runtime.Drain(5 * time.Second)
	if !drained || err != nil {
		t.Fatalf("drain without worker must report drained, got drained=%v err=%v", drained, err)
	}
}
