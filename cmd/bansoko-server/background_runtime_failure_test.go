package main

import (
	"context"
	"errors"
	"testing"
	"time"

	maintenancepkg "bansoko/internal/runtime/maintenance"
)

func TestBackgroundRuntimeFailedStopRetainsWorker(t *testing.T) {
	want := errors.New("handler still running")
	worker := &fakeRuntime{drainErr: want}
	builds := 0
	runtime := NewBackgroundRuntime(maintenancepkg.NewGate(), func() (RuntimeLike, error) { builds++; return worker, nil }, nil, nil)
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(); !errors.Is(err, want) {
		t.Fatalf("Stop lost drain error: %v", err)
	}
	if !runtime.Active() {
		t.Error("failed drain falsely reported inactive")
	}
	drained, err := runtime.Drain(time.Second)
	if drained || !errors.Is(err, want) {
		t.Errorf("lost failed worker: drained=%v err=%v", drained, err)
	}
	_ = runtime.Start()
	if builds != 1 {
		t.Error("new worker started while old handler still alive")
	}
	worker.drainErr = nil
	runtime.Stop()
	if runtime.Active() {
		t.Error("successful stop retained active worker")
	}
}

func TestBackgroundRuntimeDrainDoesNotHideUncooperativeProducer(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	runtime := NewBackgroundRuntime(maintenancepkg.NewGate(), func() (RuntimeLike, error) { return &fakeRuntime{}, nil }, []func(context.Context){func(context.Context) { close(started); <-release }}, nil)
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	<-started
	drained, err := runtime.Drain(10 * time.Millisecond)
	if drained || err == nil || !runtime.Active() {
		t.Fatalf("producer false quiescence: %v %v", drained, err)
	}
	close(release)
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
}
