package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	downloadapplication "bansoko/internal/download/application"
	observabilityport "bansoko/internal/observability/port"
)

type qbSyncDispatchStub struct {
	result downloadapplication.QBSyncDispatchResult
	err    error
}

func (s qbSyncDispatchStub) DispatchQBSync(context.Context) (downloadapplication.QBSyncDispatchResult, error) {
	return s.result, s.err
}

type recordingRuntimeLogger struct {
	debug []observabilityport.RuntimeEvent
	info  []observabilityport.RuntimeEvent
	warn  []observabilityport.RuntimeEvent
	errs  []observabilityport.RuntimeEvent
}

func (l *recordingRuntimeLogger) Debug(_ context.Context, event observabilityport.RuntimeEvent) {
	l.debug = append(l.debug, event)
}
func (l *recordingRuntimeLogger) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	l.info = append(l.info, event)
}
func (l *recordingRuntimeLogger) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	l.warn = append(l.warn, event)
}
func (l *recordingRuntimeLogger) Error(_ context.Context, event observabilityport.RuntimeEvent) {
	l.errs = append(l.errs, event)
}

func TestRuntimeCommand(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		mode string
		rest []string
	}{
		{name: "default serve", mode: "serve"},
		{name: "explicit serve", args: []string{"serve", "--listen", "127.0.0.1:0"}, mode: "serve", rest: []string{"--listen", "127.0.0.1:0"}},
		{name: "serve flags stay unchanged", args: []string{"--listen", "127.0.0.1:0"}, mode: "serve", rest: []string{"--listen", "127.0.0.1:0"}},
		{name: "bootstrap", args: []string{"bootstrap", "recover", "status"}, mode: "bootstrap", rest: []string{"recover", "status"}},
		{name: "migrate", args: []string{"migrate", "rotate-secret-key"}, mode: "migrate", rest: []string{"rotate-secret-key"}},
		{name: "migrate check-vars", args: []string{"migrate", "check-vars", "--json"}, mode: "migrate", rest: []string{"check-vars", "--json"}},
		{name: "local update", args: []string{"update", "check", "--json"}, mode: "update", rest: []string{"check", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode, rest := runtimeCommand(test.args)
			if mode != test.mode || !reflect.DeepEqual(rest, test.rest) {
				t.Fatalf("runtimeCommand(%v) = %q, %v; want %q, %v", test.args, mode, rest, test.mode, test.rest)
			}
		})
	}
}

func TestQBSyncDispatchEvent(t *testing.T) {
	empty := qbSyncDispatchEvent(downloadapplication.QBSyncDispatchResult{})
	if empty.Outcome != "success" || empty.ReasonCode != "qb_sync_idle" {
		t.Fatalf("empty dispatch event = %+v", empty)
	}
	if !strings.Contains(empty.Message, "no qBittorrent check was scheduled") || !strings.Contains(empty.Message, "no connection was made") {
		t.Fatalf("empty dispatch message does not separate scheduling from connecting: %q", empty.Message)
	}

	scheduled := qbSyncDispatchEvent(downloadapplication.QBSyncDispatchResult{Scanned: 6, Queued: 2, Merged: 3, Skipped: 1})
	if scheduled.Outcome != "success" || scheduled.ReasonCode != "qb_sync_completed" {
		t.Fatalf("scheduled dispatch event = %+v", scheduled)
	}
	for _, want := range []string{"scanned=6", "queued=2", "merged=3", "skipped=1", "qBittorrent was not contacted"} {
		if !strings.Contains(scheduled.Message, want) {
			t.Fatalf("scheduled dispatch message %q lacks %q", scheduled.Message, want)
		}
	}
}

func TestQBSyncDispatchEventDistinguishesSkipAndMergePasses(t *testing.T) {
	for _, test := range []struct {
		name   string
		result downloadapplication.QBSyncDispatchResult
		wants  []string
	}{
		{
			name:   "all skipped",
			result: downloadapplication.QBSyncDispatchResult{Scanned: 5, Skipped: 5},
			wants:  []string{"scanned=5", "queued=0", "merged=0", "skipped=5", "qBittorrent was not contacted"},
		},
		{
			name:   "all merged",
			result: downloadapplication.QBSyncDispatchResult{Scanned: 4, Merged: 4},
			wants:  []string{"scanned=4", "queued=0", "merged=4", "skipped=0", "qBittorrent was not contacted"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := qbSyncDispatchEvent(test.result)
			if event.Outcome != "success" || event.ReasonCode != "qb_sync_completed" {
				t.Fatalf("event = %+v", event)
			}
			if strings.Contains(event.Message, "scheduled checks") {
				t.Fatalf("event claims scheduled checks without scheduling any: %q", event.Message)
			}
			for _, want := range test.wants {
				if !strings.Contains(event.Message, want) {
					t.Fatalf("message %q lacks %q", event.Message, want)
				}
			}
		})
	}
}

func TestQBSyncDispatchErrorReportsAnIncompleteRoundWithoutTheRawError(t *testing.T) {
	logger := &recordingRuntimeLogger{}
	rawError := errors.New("begin qB sync dispatch: /config/private/dsn token=secret")
	dispatchQBSyncOnce(context.Background(), qbSyncDispatchStub{err: rawError}, logger)
	if len(logger.info) != 0 || len(logger.warn) != 1 {
		t.Fatalf("events = info:%+v warn:%+v", logger.info, logger.warn)
	}
	event := logger.warn[0]
	if event.Outcome != "failure" || event.ReasonCode != "qb_sync_failed" {
		t.Fatalf("failure event = %+v", event)
	}
	for _, want := range []string{"did not complete this round", "no qBittorrent check was made"} {
		if !strings.Contains(event.Message, want) {
			t.Fatalf("failure message %q lacks %q", event.Message, want)
		}
	}
	for _, leak := range []string{"private", "token", "secret", "dsn"} {
		if strings.Contains(event.Message, leak) {
			t.Fatalf("failure message leaked the raw error %q: %q", leak, event.Message)
		}
	}

	// Cancellation is shutdown, not a dispatcher failure.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	dispatchQBSyncOnce(cancelled, qbSyncDispatchStub{err: rawError}, logger)
	if len(logger.warn) != 1 {
		t.Fatalf("cancellation logged a failure: %+v", logger.warn)
	}
}

func TestQBSyncDispatchOnceLogsTheSuccessBody(t *testing.T) {
	logger := &recordingRuntimeLogger{}
	dispatchQBSyncOnce(context.Background(), qbSyncDispatchStub{result: downloadapplication.QBSyncDispatchResult{Scanned: 3, Merged: 2, Skipped: 1}}, logger)
	if len(logger.warn) != 0 || len(logger.info) != 1 {
		t.Fatalf("events = info:%+v warn:%+v", logger.info, logger.warn)
	}
	if event := logger.info[0]; event.ReasonCode != "qb_sync_completed" || !strings.Contains(event.Message, "scanned=3") {
		t.Fatalf("success event = %+v", event)
	}
}

func TestConfigWaitSweepEvent(t *testing.T) {
	event := configWaitSweepEvent(4)
	if event.Outcome != "success" || event.ReasonCode != "config_wait_sweep_woken" {
		t.Fatalf("config wait sweep event = %+v", event)
	}
	for _, want := range []string{"woke 4 import(s)", "waiting for configuration", "performed no import"} {
		if !strings.Contains(event.Message, want) {
			t.Fatalf("config wait sweep message %q lacks %q", event.Message, want)
		}
	}
}

func TestDownloadCompletionEvents(t *testing.T) {
	partial := downloadCompletionFailureEvent(3)
	if partial.Outcome != "failure" || partial.ReasonCode != "download_completion_pass_failed" {
		t.Fatalf("partial completion event = %+v", partial)
	}
	for _, want := range []string{"recorded 3 completion row(s)", "does not confirm an action result"} {
		if !strings.Contains(partial.Message, want) {
			t.Fatalf("partial completion message %q lacks %q", partial.Message, want)
		}
	}

	none := downloadCompletionFailureEvent(0)
	if strings.Contains(none.Message, "action was") || strings.Contains(none.Message, "action ran") {
		t.Fatalf("empty completion failure claims an action result: %q", none.Message)
	}
	if !strings.Contains(none.Message, "before recording a completion row") {
		t.Fatalf("empty completion failure message = %q", none.Message)
	}

	success := downloadCompletionSuccessEvent(2)
	if success.Outcome != "success" || success.ReasonCode != "download_completion_completed" || !strings.Contains(success.Message, "recorded 2 completion row(s)") {
		t.Fatalf("completion success event = %+v", success)
	}
}
