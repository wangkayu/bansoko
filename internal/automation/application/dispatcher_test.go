package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
)

type dispatcherEndpointConnectionSource struct {
	connections []configurationdomain.Connection
	err         error
}

func (s dispatcherEndpointConnectionSource) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]configurationdomain.Connection(nil), s.connections...), nil
}

func TestReaderEndpointUsesOfficialFallbackWhenNoConnectionIsEnabled(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name        string
		connections []configurationdomain.Connection
		want        string
	}{
		{name: "empty list", want: configurationdomain.DefaultEndpointForKind(configurationdomain.ConnectionAniBT)},
		{name: "disabled built-in", connections: []configurationdomain.Connection{{Kind: configurationdomain.ConnectionAniBT, State: configurationdomain.ConnectionDisabled}}, want: configurationdomain.DefaultEndpointForKind(configurationdomain.ConnectionAniBT)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := readerEndpoint(ctx, &Dispatcher{Connections: dispatcherEndpointConnectionSource{connections: testCase.connections}}, providerdomain.ProviderAniBT)
			if got != testCase.want {
				t.Fatalf("endpoint=%q, want %q", got, testCase.want)
			}
		})
	}
}

func TestReaderEndpointDoesNotFallbackOnConfigurationOrListErrors(t *testing.T) {
	ctx := context.Background()
	multiple := dispatcherEndpointConnectionSource{connections: []configurationdomain.Connection{
		{Kind: configurationdomain.ConnectionAniBT, State: configurationdomain.ConnectionConfigured, Endpoint: "https://one.example"},
		{Kind: configurationdomain.ConnectionAniBT, State: configurationdomain.ConnectionConfigured, Endpoint: "https://two.example"},
	}}
	if got := readerEndpoint(ctx, &Dispatcher{Connections: multiple}, providerdomain.ProviderAniBT); got != "" {
		t.Fatalf("multiple enabled connections must fail closed, got %q", got)
	}
	if got := readerEndpoint(ctx, &Dispatcher{Connections: dispatcherEndpointConnectionSource{err: errors.New("list failed")}}, providerdomain.ProviderAniBT); got != "" {
		t.Fatalf("connection list failure must fail closed, got %q", got)
	}
}

func TestProviderSelectionStoragePersistsDerivedModesAsAuto(t *testing.T) {
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderMikan, "42")
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name       string
		mode       selectiondomain.SelectionMode
		wantMode   string
		wantManual *bool
	}{
		{name: "ranked", mode: selectiondomain.Ranked, wantMode: "auto"},
		{name: "always", mode: selectiondomain.Always, wantMode: "auto"},
		{name: "never", mode: selectiondomain.Never, wantMode: "auto"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mode, manual := providerSelectionStorage(selectiondomain.GroupSelectionDecision{
				Group: selectiondomain.EffectiveProviderGroup{Ref: ref, Mode: testCase.mode},
			})
			if mode != testCase.wantMode {
				t.Fatalf("mode=%q, want %q", mode, testCase.wantMode)
			}
			if (manual == nil) != (testCase.wantManual == nil) || (manual != nil && *manual != *testCase.wantManual) {
				t.Fatalf("manual=%v, want %v", manual, testCase.wantManual)
			}
		})
	}
}

func TestDispatchRoutineOutcome(t *testing.T) {
	cases := []struct {
		name        string
		counts      dispatchCounts
		err         error
		wantOutcome string
		wantCode    string
	}{
		{name: "success", wantOutcome: "success", wantCode: "dispatch_completed"},
		{name: "partial", counts: dispatchCounts{enqueued: 1, errors: 1}, wantOutcome: "warning", wantCode: "dispatch_partial"},
		{name: "failed counts", counts: dispatchCounts{errors: 1}, wantOutcome: "failure", wantCode: "dispatch_failed"},
		{name: "run error", err: errors.New("failed"), wantOutcome: "failure", wantCode: "dispatch_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, code := dispatchRoutineOutcome("completed", tc.counts, tc.err)
			if outcome != tc.wantOutcome || code != tc.wantCode {
				t.Fatalf("got outcome=%q code=%q, want outcome=%q code=%q", outcome, code, tc.wantOutcome, tc.wantCode)
			}
		})
	}
}

func TestSafeDispatchErrorRedactsSensitiveValues(t *testing.T) {
	if got := safeDispatchError(errors.New("request https://example.test/?token=secret")); got == "request https://example.test/?token=secret" {
		t.Fatal("expected sensitive dispatch error to be redacted")
	}
	if got := safeDispatchError(errors.New(strings.Repeat("x", 5000))); len(got) != 4096 {
		t.Fatalf("expected bounded dispatch error, got %d bytes", len(got))
	}
}

type captureRoutineRecorder struct {
	routine   observabilityport.RoutineRequest
	failures  []observabilityport.DiagnosticFailureRequest
	successes []observabilityport.DiagnosticSuccessRequest
}

func (r *captureRoutineRecorder) UpsertRoutine(_ context.Context, req observabilityport.RoutineRequest) error {
	r.routine = req
	return nil
}
func (r *captureRoutineRecorder) RecordDiagnosticFailure(_ context.Context, req observabilityport.DiagnosticFailureRequest) error {
	r.failures = append(r.failures, req)
	return nil
}
func (r *captureRoutineRecorder) RecordDiagnosticSuccess(_ context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	r.successes = append(r.successes, req)
	return nil
}
func (r *captureRoutineRecorder) UpsertRoutineTx(context.Context, txscope.Tx, observabilityport.RoutineRequest) error {
	return nil
}
func (r *captureRoutineRecorder) RecordDiagnosticFailureTx(context.Context, txscope.Tx, observabilityport.DiagnosticFailureRequest) error {
	return nil
}
func (r *captureRoutineRecorder) RecordDiagnosticSuccessTx(context.Context, txscope.Tx, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func TestRecordObservabilitySuccessfulDispatchClosesAllDispatchEpisodes(t *testing.T) {
	recorder := &captureRoutineRecorder{}
	d := &Dispatcher{Recorder: recorder}
	d.recordObservability(context.Background(), "rss_refresh", "completed", dispatchCounts{}, nil)

	want := []observabilityport.DiagnosticIdentity{
		observabilityport.AutomationDispatchFailure(observabilityport.ScopeWorker),
		observabilityport.AutomationDispatchPartial(observabilityport.ScopeWorker),
	}
	if len(recorder.successes) != len(want) {
		t.Fatalf("got %d recovery writes, want %d: %+v", len(recorder.successes), len(want), recorder.successes)
	}
	for i, identity := range want {
		if got := recorder.successes[i]; got.Identity != identity || got.ScopeID != "rss_refresh" {
			t.Errorf("recovery[%d]=%+v, want identity=%+v scope_id=%q", i, got, identity, "rss_refresh")
		}
	}
}

func TestRecordObservabilityWritesStructuredDispatchCounts(t *testing.T) {
	recorder := &captureRoutineRecorder{}
	d := &Dispatcher{Recorder: recorder}
	d.recordObservability(context.Background(), "rss_refresh", "completed", dispatchCounts{targetCount: 5, enqueued: 4, merged: 1, skipped: 2, errors: 3}, nil)

	var details map[string]int
	if err := json.Unmarshal(recorder.routine.Details, &details); err != nil {
		t.Fatalf("details must decode: %v", err)
	}
	want := map[string]int{"target_count": 5, "enqueued_count": 4, "merged_count": 1, "skipped_count": 2, "error_count": 3}
	for key, value := range want {
		if details[key] != value {
			t.Fatalf("details[%q]=%d, want %d (details=%v)", key, details[key], value, details)
		}
	}
}

func boolPointer(value bool) *bool { return &value }
