package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	observabilityport "bansoko/internal/observability/port"
	maintenancepkg "bansoko/internal/runtime/maintenance"
	// runtimeworker "bansoko/internal/runtime/worker"
)

// RuntimeLike is the worker-runtime surface the controller needs;
// *runtimeworker.Runtime implements it.
type RuntimeLike interface {
	Start()
	Drain(timeout time.Duration) error
}

// ErrBackgroundRuntimeInvalid indicates that a required lifecycle dependency
// was omitted from the composition root.
var ErrBackgroundRuntimeInvalid = errors.New("invalid background runtime wiring")

// BackgroundRuntime is the composition-root lifecycle controller for the
// worker runtime and every background producer goroutine (qB dispatcher,
// retention, automation dispatcher, plugin admission resolver, file
// maintenance scheduler). Boot state and maintenance entry/exit drive it:
//
//   - Start is called when the instance is serving (boot in serving state, or
//     maintenance exit on the switched instance). It constructs a fresh worker
//     runtime and launches every producer under one cancellable context.
//   - Stop is called when maintenance is entered: it drains the worker
//     runtime (leases expire, items become reclaimable) and cancels the
//     producer context so no new external effects are produced.
//   - Drain forwards to the current worker runtime for the orchestrator's
//     drain step (idempotent when already stopped).
//
// The worker runtime is deliberately rebuilt on every Start: a drained
// runtime cannot be restarted in-process, and after a container switch the
// new process boots in maintenance with no runtime at all.
type BackgroundRuntime struct {
	gate        *maintenancepkg.Gate
	buildWorker func() (RuntimeLike, error)
	producers   []func(ctx context.Context)
	logger      observabilityport.RuntimeLogger

	mu           sync.Mutex
	worker       RuntimeLike
	cancel       context.CancelFunc
	active       bool
	producerDone <-chan struct{}
}

// NewBackgroundRuntime wires the controller. buildWorker constructs a fresh
// worker runtime per Start (never reused after a drain).
func NewBackgroundRuntime(gate *maintenancepkg.Gate, buildWorker func() (RuntimeLike, error), producers []func(ctx context.Context), logger observabilityport.RuntimeLogger) *BackgroundRuntime {
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	return &BackgroundRuntime{gate: gate, buildWorker: buildWorker, producers: producers, logger: logger}
}

// Active reports whether the background runtime is currently running.
func (b *BackgroundRuntime) Active() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// Start boots the worker runtime and every producer. It is idempotent per
// serving period; a maintenance-exit on the same process starts a fresh
// worker (a drained runtime cannot be restarted).
func (b *BackgroundRuntime) Start() error {
	if b == nil {
		return ErrBackgroundRuntimeInvalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active {
		return nil
	}
	if b.gate == nil {
		return fmt.Errorf("%w: maintenance gate is nil", ErrBackgroundRuntimeInvalid)
	}
	if b.buildWorker == nil {
		err := fmt.Errorf("%w: worker builder is nil", ErrBackgroundRuntimeInvalid)
		b.failClosed()
		return err
	}
	for index, producer := range b.producers {
		if producer == nil {
			err := fmt.Errorf("%w: producer %d is nil", ErrBackgroundRuntimeInvalid, index)
			b.failClosed()
			return err
		}
	}
	if b.gate.RejectWrites() {
		return maintenancepkg.ErrResumeGateNotPassed
	}
	worker, err := b.buildWorker()
	if err != nil {
		b.failClosed()
		return err
	}
	if worker == nil {
		err := fmt.Errorf("%w: worker builder returned nil", ErrBackgroundRuntimeInvalid)
		b.failClosed()
		return err
	}
	// The gate may have changed while the worker was being constructed. Do
	// not open a worker/producer window after a concurrent maintenance entry.
	if b.gate.RejectWrites() {
		return maintenancepkg.ErrResumeGateNotPassed
	}
	ctx, cancel := context.WithCancel(context.Background())
	worker.Start()
	var producerWG sync.WaitGroup
	for _, producer := range b.producers {
		producerWG.Add(1)
		go func(run func(context.Context)) {
			defer producerWG.Done()
			run(ctx)
		}(producer)
	}
	done := make(chan struct{})
	go func() { producerWG.Wait(); close(done) }()
	b.producerDone = done
	b.worker = worker
	b.cancel = cancel
	b.active = true
	if b.logger != nil {
		b.logger.Info(context.Background(), observabilityport.RuntimeEvent{
			Message:    "background runtime started",
			Component:  "runtime",
			Operation:  "background_start",
			Outcome:    "info",
			ReasonCode: "background_runtime_started",
		})
	}
	return nil
}

// failClosed parks a failed restart attempt in maintenance. This is required
// for maintenance exit: a worker-construction failure must not leave the HTTP
// write gate serving while the asynchronous runtime is absent.
func (b *BackgroundRuntime) failClosed() {
	if b.gate == nil {
		return
	}
	b.gate.EnterMaintenance()
	b.gate.SetAutomationResumeGate(false)
}

// Stop drains the worker runtime and cancels the producer context. It is
// idempotent. Producers are cancelled before the worker drain begins, so a
// maintenance window cannot admit another producer tick while active handlers
// are winding down. Producers already inside one iteration remain responsible
// for honoring context cancellation at their next effect boundary.
func (b *BackgroundRuntime) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return nil
	}
	// Cancel producers first. Worker drain may wait for active handlers, and
	// producers must not continue dispatching during that maintenance window.
	if b.cancel != nil {
		b.cancel()
	}
	deadline := time.Now().Add(30 * time.Second)
	var drainErr error
	if b.worker != nil {
		drainErr = b.worker.Drain(time.Until(deadline))
	}
	if drainErr == nil && b.producerDone != nil {
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-b.producerDone:
		case <-timer.C:
			drainErr = errors.New("background producers have not stopped before drain deadline")
		}
		timer.Stop()
	}
	if drainErr == nil {
		b.worker = nil
		b.cancel = nil
		b.active = false
	} else {
		// Preserve the worker for subsequent drain; never replace live handlers.
		b.failClosed()
	}
	if b.logger != nil {
		event := observabilityport.RuntimeEvent{
			Message:    "background runtime stopped",
			Component:  "runtime",
			Operation:  "background_stop",
			Outcome:    "info",
			ReasonCode: "background_runtime_stopped",
		}
		if drainErr != nil {
			event.Outcome = "failure"
			event.ReasonCode = "background_runtime_drain_failed"
			event.ErrorClass = "worker_drain"
			b.logger.Error(context.Background(), event)
		} else {
			b.logger.Info(context.Background(), event)
		}
	}
	return drainErr
}

// Drain forwards to the current worker runtime within the deadline. A
// runtime that is already stopped counts as drained. The worker error is
// preserved for the update caller instead of being reduced to a
// boolean.
func (b *BackgroundRuntime) Drain(deadline time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.worker == nil {
		return true, nil
	}
	until := time.Now().Add(deadline)
	if b.cancel != nil {
		b.cancel()
	}
	err := b.worker.Drain(time.Until(until))
	if err == nil && b.producerDone != nil {
		timer := time.NewTimer(time.Until(until))
		select {
		case <-b.producerDone:
		case <-timer.C:
			err = errors.New("background producers have not stopped before drain deadline")
		}
		timer.Stop()
	}
	return err == nil, err
}
