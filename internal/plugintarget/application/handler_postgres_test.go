package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	deliverypayload "bansoko/internal/plugintarget/payload"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type countingHTTPClient struct {
	calls  int
	status int
	err    error
}

func (c *countingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return (&fakeHTTPClient{statusCode: c.status, body: `{"ok":true,"result":{"message_id":123},"status":"free provider text","token":"must-not-persist"}`}).Do(req)
}

type countingTemplateRenderer struct {
	calls int
	title string
	body  string
	err   error
}

func (r *countingTemplateRenderer) RenderNotification(_, _, _, _ string) (string, string, error) {
	r.calls++
	return r.title, r.body, r.err
}

type countingSecretResolver struct {
	calls     int
	delegate  SecretResolver
	value     string
	available bool
}

func (r *countingSecretResolver) Resolve(ref string) (string, bool) {
	r.calls++
	if !r.available {
		return "", false
	}
	if r.delegate != nil {
		return r.delegate.Resolve(ref)
	}
	return r.value, true
}

type responseReadErrorClient struct{}

func (responseReadErrorClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusAccepted,
		Body: io.NopCloser(&errorAfterReader{
			data: []byte("raw-secret-response"),
		}),
	}, nil
}

type failingDiagnosticRecorder struct {
	failureCalls int
	successCalls []observabilityport.DiagnosticSuccessRequest
}

func (r *failingDiagnosticRecorder) UpsertRoutine(context.Context, observabilityport.RoutineRequest) error {
	return nil
}
func (r *failingDiagnosticRecorder) RecordDiagnosticFailure(context.Context, observabilityport.DiagnosticFailureRequest) error {
	r.failureCalls++
	return errors.New("diagnostic unavailable")
}
func (r *failingDiagnosticRecorder) RecordDiagnosticSuccess(_ context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	r.successCalls = append(r.successCalls, req)
	return nil
}
func (r *failingDiagnosticRecorder) UpsertRoutineTx(context.Context, txscope.Tx, observabilityport.RoutineRequest) error {
	return nil
}
func (r *failingDiagnosticRecorder) RecordDiagnosticFailureTx(context.Context, txscope.Tx, observabilityport.DiagnosticFailureRequest) error {
	return errors.New("transactional diagnostic must not be called")
}
func (r *failingDiagnosticRecorder) RecordDiagnosticSuccessTx(context.Context, txscope.Tx, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func seedHandlerDelivery(t *testing.T) (*Service, string, workitems.Claim) {
	t.Helper()
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	input := validCreateInput(t)
	input.Enabled = true
	input.Secret = secretSetInput(t)
	target, err := service.CreateTarget(ctx, input, "handler-target-create-key")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.TestTarget(ctx, target.ID, TargetTestInput{ExpectedVersion: 1, TriggerKind: TriggerDownloadSubmitted}, "handler-test-request-key")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := service.workItems.ClaimDue(ctx, "handler-worker", []string{workitems.KindPluginDeliver}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return service, accepted.DeliveryID, claim
}

func expireAndReclaimDelivery(t *testing.T, service *Service, id string) workitems.Claim {
	t.Helper()
	fixtureDB := disposabledb.Owner(t)
	if _, err := fixtureDB.ExecContext(context.Background(), `UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE owner_type='plugin_delivery' AND owner_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	claim, err := service.workItems.ClaimDue(context.Background(), "reclaim-worker", []string{workitems.KindPluginDeliver}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func finishHandlerOutcome(t *testing.T, service *Service, claim workitems.Claim, out runtimeworker.Outcome) {
	t.Helper()
	var err error
	if out.Kind == runtimeworker.OutcomeRetry {
		_, err = service.workItems.Retry(context.Background(), claim, out.ReasonCode, out.Reason, out.RetryAfter, out.WriteOwner)
	} else {
		err = service.workItems.FinishWithResult(context.Background(), claim, out.TerminalState, out.OwnerResult, out.WriteOwner)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func readOpenAttemptID(t *testing.T, service *Service, deliveryID string) string {
	t.Helper()
	var attemptID string
	if err := service.db.QueryRowContext(context.Background(), `SELECT id FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, deliveryID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	return attemptID
}

func TestHandlerRequestStartedSkipsPrepareDependenciesAndFinalizesUnknown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		renderer *countingTemplateRenderer
		resolver func(SecretResolver) *countingSecretResolver
	}{
		{
			name:     "renderer failure",
			renderer: &countingTemplateRenderer{err: errors.New("renderer unavailable")},
			resolver: func(delegate SecretResolver) *countingSecretResolver {
				return &countingSecretResolver{delegate: delegate, available: true}
			},
		},
		{
			name:     "secret unavailable",
			renderer: &countingTemplateRenderer{title: "title", body: "body"},
			resolver: func(SecretResolver) *countingSecretResolver { return &countingSecretResolver{} },
		},
		{
			name:     "request build failure",
			renderer: &countingTemplateRenderer{title: "title", body: "body"},
			resolver: func(SecretResolver) *countingSecretResolver {
				return &countingSecretResolver{value: `{}`, available: true}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, id, claim := seedHandlerDelivery(t)
			initialClient := &countingHTTPClient{status: http.StatusNoContent}
			initial := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, initialClient, &fakeTemplateRenderer{title: "title", body: "body"})
			initial.hooks.afterRequestStartCommit = func() error { return errors.New("crash after request start") }
			if _, err := initial.Handle(context.Background(), claim); err == nil {
				t.Fatal("expected injected crash")
			}
			attemptID := readOpenAttemptID(t, service, id)
			reclaim := expireAndReclaimDelivery(t, service, id)

			client := &countingHTTPClient{status: http.StatusNoContent}
			resolver := tc.resolver(service.secrets)
			handler := NewPluginDeliverHandler(service.db, resolver, service.workItems, client, tc.renderer)
			out, err := handler.Handle(context.Background(), reclaim)
			if err != nil {
				t.Fatal(err)
			}
			if tc.renderer.calls != 0 || resolver.calls != 0 || client.calls != 0 {
				t.Fatalf("request-start recovery called dependencies: renderer=%d secret=%d dial=%d", tc.renderer.calls, resolver.calls, client.calls)
			}
			finishHandlerOutcome(t, service, reclaim, out)

			var finalAttemptID, deliveryState, deliveryCode, itemState, attemptCode string
			var attempts int
			if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*),MIN(a.id::text),MIN(d.state),MIN(d.last_error_code),MIN(w.state),MIN(a.error_code) FROM plugin_delivery_attempts a JOIN plugin_deliveries d ON d.id=a.plugin_delivery_id JOIN work_item_history w ON w.owner_type='plugin_delivery' AND w.owner_id=d.id WHERE d.id=$1 GROUP BY d.id`, id).Scan(&attempts, &finalAttemptID, &deliveryState, &deliveryCode, &itemState, &attemptCode); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || finalAttemptID != attemptID || deliveryState != "unknown" || deliveryCode != "request_outcome_unknown" || itemState != workitems.StateFailed || attemptCode != "request_outcome_unknown" {
				t.Fatalf("unexpected recovery evidence: attempts=%d attempt=%q delivery=%s/%s item=%s attempt_code=%s", attempts, finalAttemptID, deliveryState, deliveryCode, itemState, attemptCode)
			}
		})
	}
}

func TestHandlerNotStartedPrepareFailureFinalizesSameAttempt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wantCode string
		renderer func() *countingTemplateRenderer
		resolver func(SecretResolver) *countingSecretResolver
	}{
		{
			name:     "renderer failure",
			wantCode: "notification_template_invalid",
			renderer: func() *countingTemplateRenderer {
				return &countingTemplateRenderer{err: errors.New("renderer unavailable")}
			},
			resolver: func(delegate SecretResolver) *countingSecretResolver {
				return &countingSecretResolver{delegate: delegate, available: true}
			},
		},
		{
			name:     "secret unavailable",
			wantCode: "secret_resolve_failed",
			renderer: func() *countingTemplateRenderer { return &countingTemplateRenderer{title: "title", body: "body"} },
			resolver: func(SecretResolver) *countingSecretResolver { return &countingSecretResolver{} },
		},
		{
			name:     "request build failure",
			wantCode: "request_build_failed",
			renderer: func() *countingTemplateRenderer { return &countingTemplateRenderer{title: "title", body: "body"} },
			resolver: func(SecretResolver) *countingSecretResolver {
				return &countingSecretResolver{value: `{}`, available: true}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, id, claim := seedHandlerDelivery(t)
			initial := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, &countingHTTPClient{status: http.StatusNoContent}, &fakeTemplateRenderer{title: "title", body: "body"})
			initial.hooks.afterAttemptCommit = func() error { return errors.New("crash after attempt") }
			if _, err := initial.Handle(context.Background(), claim); err == nil {
				t.Fatal("expected injected crash")
			}
			attemptID := readOpenAttemptID(t, service, id)
			reclaim := expireAndReclaimDelivery(t, service, id)

			client := &countingHTTPClient{status: http.StatusNoContent}
			renderer := tc.renderer()
			resolver := tc.resolver(service.secrets)
			handler := NewPluginDeliverHandler(service.db, resolver, service.workItems, client, renderer)
			out, err := handler.Handle(context.Background(), reclaim)
			if err != nil {
				t.Fatal(err)
			}
			if client.calls != 0 {
				t.Fatalf("prepare failure dial count=%d", client.calls)
			}
			finishHandlerOutcome(t, service, reclaim, out)

			var finalAttemptID, deliveryState, deliveryCode, itemState, attemptCode string
			var attempts int
			var requestStarted bool
			if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*),MIN(a.id::text),bool_or(a.request_started_at IS NOT NULL),MIN(d.state),MIN(d.last_error_code),MIN(w.state),MIN(a.error_code) FROM plugin_delivery_attempts a JOIN plugin_deliveries d ON d.id=a.plugin_delivery_id JOIN work_item_history w ON w.owner_type='plugin_delivery' AND w.owner_id=d.id WHERE d.id=$1 GROUP BY d.id`, id).Scan(&attempts, &finalAttemptID, &requestStarted, &deliveryState, &deliveryCode, &itemState, &attemptCode); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || finalAttemptID != attemptID || requestStarted || deliveryState != "failed" || deliveryCode != tc.wantCode || itemState != workitems.StateFailed || attemptCode != tc.wantCode {
				t.Fatalf("unexpected prepare evidence: attempts=%d attempt=%q started=%v delivery=%s/%s item=%s attempt_code=%s", attempts, finalAttemptID, requestStarted, deliveryState, deliveryCode, itemState, attemptCode)
			}
		})
	}
}
func TestHandlerDiagnosticFailureRunsAfterCommittedBusinessEvidence(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := &countingHTTPClient{status: http.StatusNoContent}
	recorder := &failingDiagnosticRecorder{}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{err: errors.New("invalid template")})
	handler.SetDiagnosticRecorder(recorder, nil)

	out, err := handler.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 || recorder.failureCalls != 0 || out.AfterCommit == nil {
		t.Fatalf("pre-commit side effect: dials=%d diagnostics=%d after_commit=%v", client.calls, recorder.failureCalls, out.AfterCommit != nil)
	}
	finishHandlerOutcome(t, service, claim, out)
	out.AfterCommit(context.Background())
	if recorder.failureCalls != 1 {
		t.Fatalf("diagnostic calls=%d", recorder.failureCalls)
	}
	if recorder.successCalls != nil {
		t.Fatalf("unexpected recovery closes on failure: %+v", recorder.successCalls)
	}

	// seedHandlerDelivery resets the shared disposable schema, so assert this
	// delivery's committed evidence before creating the success-path fixture.
	var deliveryState, itemState, reasonCode string
	if err := service.db.QueryRowContext(context.Background(), `SELECT d.state,w.state,d.last_error_code FROM plugin_deliveries d JOIN work_item_history w ON w.owner_type='plugin_delivery' AND w.owner_id=d.id WHERE d.id=$1`, id).Scan(&deliveryState, &itemState, &reasonCode); err != nil {
		t.Fatal(err)
	}
	if deliveryState != "failed" || itemState != workitems.StateFailed || reasonCode != "notification_template_invalid" {
		t.Fatalf("business evidence rolled back: delivery=%s item=%s reason=%s", deliveryState, itemState, reasonCode)
	}

	successService, successID, successClaim := seedHandlerDelivery(t)
	successClient := &countingHTTPClient{status: http.StatusNoContent}
	successHandler := NewPluginDeliverHandler(successService.db, successService.secrets, successService.workItems, successClient, &fakeTemplateRenderer{title: "title", body: "body"})
	successHandler.SetDiagnosticRecorder(recorder, nil)
	successOut, err := successHandler.Handle(context.Background(), successClaim)
	if err != nil {
		t.Fatal(err)
	}
	if successOut.AfterCommit == nil || successOut.OwnerResult.State != "delivered" {
		t.Fatalf("successful delivery lacks diagnostic recovery hook: state=%q after_commit=%v", successOut.OwnerResult.State, successOut.AfterCommit != nil)
	}
	finishHandlerOutcome(t, successService, successClaim, successOut)
	successOut.AfterCommit(context.Background())
	wantCodes := []observabilityport.DiagnosticErrorCode{
		observabilityport.ErrorNotificationTemplateInvalid,
		observabilityport.ErrorSecretResolveFailed,
		observabilityport.ErrorRequestBuildFailed,
	}
	if len(recorder.successCalls) != len(wantCodes) {
		t.Fatalf("diagnostic close calls=%d, want %d", len(recorder.successCalls), len(wantCodes))
	}
	for i, req := range recorder.successCalls {
		want := observabilityport.PluginPrepareFailure(wantCodes[i])
		if req.Identity != want || req.ScopeID != successID {
			t.Fatalf("diagnostic close[%d]=%+v/%q, want %+v/%q", i, req.Identity, req.ScopeID, want, successID)
		}
	}
}

func TestHandlerAttemptWriteRollsBackBeforeCommit(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := &countingHTTPClient{status: http.StatusNoContent}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	handler.hooks.beforeAttemptCommit = func() error { return errors.New("rollback attempt transaction") }
	if _, err := handler.Handle(context.Background(), claim); err == nil {
		t.Fatal("expected injected rollback")
	}
	if client.calls != 0 {
		t.Fatal("attempt rollback must occur before dial")
	}
	var attempts, count int
	var state string
	if err := service.db.QueryRowContext(context.Background(), `SELECT state,attempt_count FROM plugin_deliveries WHERE id=$1`, id).Scan(&state, &count); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || count != 0 || attempts != 0 {
		t.Fatalf("attempt transaction leaked state=%s count=%d attempts=%d", state, count, attempts)
	}
}

func TestHandlerRequestStartWriteFailureRollsBackAndReusesAttempt(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := &countingHTTPClient{status: http.StatusNoContent}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	handler.hooks.beforeRequestStartWrite = func() error { return errors.New("rollback request-start transaction") }
	if _, err := handler.Handle(context.Background(), claim); err == nil {
		t.Fatal("expected injected request-start rollback")
	}
	if client.calls != 0 {
		t.Fatal("request-start rollback must occur before dial")
	}
	var attempts int
	var requestStarted bool
	var firstAttemptID string
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*),COALESCE(bool_or(request_started_at IS NOT NULL),false),COALESCE(MIN(id::text),'') FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&attempts, &requestStarted, &firstAttemptID); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || requestStarted || firstAttemptID == "" {
		t.Fatalf("durable attempt/request-start mismatch: attempts=%d request_started=%v attempt_id=%q", attempts, requestStarted, firstAttemptID)
	}

	reclaim := expireAndReclaimDelivery(t, service, id)
	handler.hooks = deliveryHooks{}
	out, err := handler.Handle(context.Background(), reclaim)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("reclaimed attempt dial count=%d", client.calls)
	}
	finishHandlerOutcome(t, service, reclaim, out)
	var finalAttemptID string
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*),COALESCE(MIN(id::text),'') FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&attempts, &finalAttemptID); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || finalAttemptID != firstAttemptID {
		t.Fatalf("reclaim did not reuse durable attempt: attempts=%d first_id=%q final_id=%q", attempts, firstAttemptID, finalAttemptID)
	}
}

func TestHandlerAttemptCommittedBeforeDialAndReusableBeforeRequestStart(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := &countingHTTPClient{status: http.StatusNoContent}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	handler.hooks.afterAttemptCommit = func() error { return errors.New("crash after attempt") }
	if _, err := handler.Handle(context.Background(), claim); err == nil {
		t.Fatal("expected injected crash")
	}
	if client.calls != 0 {
		t.Fatal("dial occurred before request-start")
	}
	var attempts int
	var requestStarted bool
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*),COALESCE(bool_or(request_started_at IS NOT NULL),false) FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&attempts, &requestStarted); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || requestStarted {
		t.Fatalf("attempts=%d request_started=%v", attempts, requestStarted)
	}
	reclaim := expireAndReclaimDelivery(t, service, id)
	handler.hooks = deliveryHooks{}
	out, err := handler.Handle(context.Background(), reclaim)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("reusable attempt dial count=%d", client.calls)
	}
	finishHandlerOutcome(t, service, reclaim, out)
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempt finalization inserted duplicate: %d", attempts)
	}
}

func TestHandlerRequestStartCrashReclaimsUnknownWithoutResend(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := &countingHTTPClient{status: http.StatusNoContent}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	handler.hooks.afterRequestStartCommit = func() error { return errors.New("crash before dial") }
	if _, err := handler.Handle(context.Background(), claim); err == nil {
		t.Fatal("expected injected crash")
	}
	if client.calls != 0 {
		t.Fatal("dial occurred despite injected pre-dial crash")
	}
	reclaim := expireAndReclaimDelivery(t, service, id)
	handler.hooks = deliveryHooks{}
	out, err := handler.Handle(context.Background(), reclaim)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatal("request-start evidence must suppress resend")
	}
	finishHandlerOutcome(t, service, reclaim, out)
	var state, errorCode string
	var attempts int
	var responseReceived bool
	if err := service.db.QueryRowContext(context.Background(), `SELECT d.state,d.attempt_count,a.error_code,a.response_received_at IS NOT NULL FROM plugin_deliveries d JOIN plugin_delivery_attempts a ON a.plugin_delivery_id=d.id WHERE d.id=$1`, id).Scan(&state, &attempts, &errorCode, &responseReceived); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" || attempts != 1 || errorCode != "request_outcome_unknown" || responseReceived {
		t.Fatalf("state=%s attempts=%d error_code=%s response_received=%v", state, attempts, errorCode, responseReceived)
	}
}

func TestHandlerResponseCrashReclaimsUnknownAndExpectedStatusCreatesNoDiagnostic(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := &countingHTTPClient{status: http.StatusOK}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	handler.hooks.afterResponseBeforeFinal = func() error { return errors.New("crash before final") }
	if _, err := handler.Handle(context.Background(), claim); err == nil {
		t.Fatal("expected injected crash")
	}
	if client.calls != 1 {
		t.Fatalf("first dial count=%d", client.calls)
	}
	reclaim := expireAndReclaimDelivery(t, service, id)
	handler.hooks = deliveryHooks{}
	out, err := handler.Handle(context.Background(), reclaim)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatal("response crash reclaim resent request")
	}
	finishHandlerOutcome(t, service, reclaim, out)
	var diagnostics int
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM diagnostic_episodes`).Scan(&diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics != 0 {
		t.Fatalf("plugin status created diagnostics: %d", diagnostics)
	}
}

func TestHandlerTimeoutFinalizesExistingAttemptUnknownWithoutDiagnostic(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := writtenThenFailingClient{err: fmt.Errorf("Authorization: Bearer secret https://private.example: %w", context.DeadlineExceeded)}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	out, err := handler.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	finishHandlerOutcome(t, service, claim, out)
	var state, attemptError, errorCode string
	var attempts int
	var responseReceived bool
	if err := service.db.QueryRowContext(context.Background(), `SELECT d.state,d.attempt_count,a.error,a.error_code,a.response_received_at IS NOT NULL FROM plugin_deliveries d JOIN plugin_delivery_attempts a ON a.plugin_delivery_id=d.id WHERE d.id=$1`, id).Scan(&state, &attempts, &attemptError, &errorCode, &responseReceived); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" || attempts != 1 || errorCode != "response_timeout" || responseReceived {
		t.Fatalf("state=%s attempts=%d error_code=%s response_received=%v", state, attempts, errorCode, responseReceived)
	}
	if strings.Contains(attemptError, "secret") || strings.Contains(attemptError, "https://") {
		t.Fatalf("unsafe error persisted: %q", attemptError)
	}
	var diagnostics int
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM diagnostic_episodes`).Scan(&diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics != 0 {
		t.Fatalf("timeout created diagnostics: %d", diagnostics)
	}
}

func TestHandlerResponseReadErrorPersistsStatusAndSafePreview(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, responseReadErrorClient{}, &fakeTemplateRenderer{title: "title", body: "body"})
	out, err := handler.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	finishHandlerOutcome(t, service, claim, out)

	var status int
	var preview, attemptError string
	var responseReceived bool
	if err := service.db.QueryRowContext(context.Background(), `SELECT response_received_at IS NOT NULL,response_status,response_preview,error FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&responseReceived, &status, &preview, &attemptError); err != nil {
		t.Fatal(err)
	}
	if !responseReceived || status != http.StatusAccepted || preview != "status=202 body_bytes=19" {
		t.Fatalf("response evidence received=%v status=%d preview=%q", responseReceived, status, preview)
	}
	if strings.Contains(preview, "raw-secret-response") || strings.Contains(attemptError, "raw-secret-response") {
		t.Fatalf("raw response persisted: preview=%q error=%q", preview, attemptError)
	}
}

func TestHandlerHTTPOutcomesPersistAttemptWithoutDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		status             int
		wantDeliveryState  string
		wantWorkItemState  string
		wantAttemptError   string
		wantDeliveryReason string
	}{
		{http.StatusUnauthorized, "failed", workitems.StateFailed, "http_401", "provider returned a permanent HTTP status"},
		{http.StatusTooManyRequests, "waiting_retry", workitems.StateAvailable, "http_429", "provider returned a retryable HTTP status"},
		{http.StatusInternalServerError, "waiting_retry", workitems.StateAvailable, "http_500", "provider returned a retryable HTTP status"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			service, id, claim := seedHandlerDelivery(t)
			body := `{"ok":false,"result":{"message_id":456},"status":"provider free text","token":"raw-body-secret"}`
			client := &fakeHTTPClient{statusCode: tc.status, body: body}
			handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
			out, err := handler.Handle(context.Background(), claim)
			if err != nil {
				t.Fatal(err)
			}
			finishHandlerOutcome(t, service, claim, out)

			var deliveryState, deliveryCode, deliveryError, itemState string
			if err := service.db.QueryRowContext(context.Background(), `SELECT d.state,d.last_error_code,d.last_error,w.state FROM plugin_deliveries d JOIN work_item_history w ON w.owner_type='plugin_delivery' AND w.owner_id=d.id WHERE d.id=$1`, id).Scan(&deliveryState, &deliveryCode, &deliveryError, &itemState); err != nil {
				t.Fatal(err)
			}
			if deliveryState != tc.wantDeliveryState || deliveryCode != tc.wantAttemptError || deliveryError != tc.wantDeliveryReason || itemState != tc.wantWorkItemState {
				t.Fatalf("delivery/item mismatch: state=%s code=%s error=%q item=%s", deliveryState, deliveryCode, deliveryError, itemState)
			}

			var attemptCode, attemptError, preview, providerAck string
			var responseStatus, attemptNumber int
			var requestStarted, responseReceived bool
			if err := service.db.QueryRowContext(context.Background(), `SELECT attempt_number,request_started_at IS NOT NULL,response_received_at IS NOT NULL,response_status,response_preview,COALESCE(provider_ack::text,''),error_code,error FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&attemptNumber, &requestStarted, &responseReceived, &responseStatus, &preview, &providerAck, &attemptCode, &attemptError); err != nil {
				t.Fatal(err)
			}
			if attemptNumber != 1 || !requestStarted || !responseReceived || responseStatus != tc.status || attemptCode != tc.wantAttemptError || attemptError != tc.wantDeliveryReason {
				t.Fatalf("attempt mismatch: number=%d started=%v received=%v status=%d code=%s error=%q", attemptNumber, requestStarted, responseReceived, responseStatus, attemptCode, attemptError)
			}
			if providerAck != `{"ok": false, "message_id": 456}` {
				t.Fatalf("provider ack was not typed: %s", providerAck)
			}
			for _, persisted := range []string{deliveryError, attemptError, preview, providerAck} {
				if strings.Contains(persisted, "raw-body-secret") || strings.Contains(persisted, "provider free text") {
					t.Fatalf("raw provider body persisted: %q", persisted)
				}
			}
			var diagnostics int
			if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM diagnostic_episodes`).Scan(&diagnostics); err != nil {
				t.Fatal(err)
			}
			if diagnostics != 0 {
				t.Fatalf("expected external outcome created diagnostics: %d", diagnostics)
			}
		})
	}
}

func TestHandlerOversizeResponsePersistsResponseTimestamp(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := &fakeHTTPClient{statusCode: http.StatusOK, body: strings.Repeat("x", maxResponseBody+1)}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	out, err := handler.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	finishHandlerOutcome(t, service, claim, out)

	var responseReceived bool
	var status int
	var errorCode string
	if err := service.db.QueryRowContext(context.Background(), `SELECT response_received_at IS NOT NULL,response_status,error_code FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&responseReceived, &status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if !responseReceived || status != http.StatusOK || errorCode != "response_too_large" {
		t.Fatalf("oversize response evidence received=%v status=%d error_code=%s", responseReceived, status, errorCode)
	}
}

// recordingHTTPClient captures the outbound request shape and returns a canned
// Telegram success body, so the full TestTarget -> WorkItem -> Handle -> HTTP
// chain can be asserted end-to-end without a real bot.
type recordingHTTPClient struct {
	lastReq  *http.Request
	lastBody []byte
}

func (r *recordingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	r.lastReq = req
	if req.Body != nil {
		r.lastBody, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":777}}`)),
		Header:     make(http.Header),
	}, nil
}

// TestHandlerTelegramDeliveryShape proves the telegram adapter end-to-end path:
// create a telegram target (chat_id + bot_token) -> TestTarget -> claim the
// WorkItem -> Handle dials api.telegram.org/bot{token}/sendMessage with the
// expected JSON body (chat_id/text/parse_mode) -> the delivery row is marked
// delivered. This is the integration-level evidence the handler unit tests
// (which only exercise buildRequest) do not cover.
func TestHandlerTelegramDeliveryShape(t *testing.T) {
	service, id, claim := seedHandlerDelivery(t)
	client := &recordingHTTPClient{}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "Title", body: "Body"})
	out, err := handler.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != runtimeworker.OutcomeFinish {
		t.Fatalf("expected finish outcome, got %s", out.Kind)
	}
	finishHandlerOutcome(t, service, claim, out)

	if client.lastReq == nil {
		t.Fatal("handler did not dial the telegram endpoint")
	}
	if client.lastReq.Method != http.MethodPost {
		t.Errorf("telegram method = %q, want POST", client.lastReq.Method)
	}
	// seedHandlerDelivery uses bot_token "123:abc" (see secretSetInput).
	wantURL := "https://api.telegram.org/bot123:abc/sendMessage"
	if client.lastReq.URL.String() != wantURL {
		t.Errorf("telegram URL = %q, want %q", client.lastReq.URL.String(), wantURL)
	}
	if got := client.lastReq.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("telegram content-type = %q, want application/json", got)
	}
	var payload map[string]string
	if err := json.Unmarshal(client.lastBody, &payload); err != nil {
		t.Fatalf("decode telegram body: %v", err)
	}
	// seedHandlerDelivery uses chat_id "12345" (see validCreateInput).
	if payload["chat_id"] != "12345" {
		t.Errorf("telegram chat_id = %q, want 12345", payload["chat_id"])
	}
	if payload["text"] != "Title\n\nBody" {
		t.Errorf("telegram text = %q, want combined title/body", payload["text"])
	}
	if _, ok := payload["parse_mode"]; !ok {
		t.Errorf("telegram body missing parse_mode: %v", payload)
	}

	var deliveryState, itemState, reasonCode, providerAck string
	var responseStatus int
	if err := service.db.QueryRowContext(context.Background(), `
SELECT d.state, w.state, COALESCE(d.last_error_code,''), a.response_status, COALESCE(a.provider_ack::text,'')
FROM plugin_deliveries d
JOIN work_item_history w ON w.owner_type='plugin_delivery' AND w.owner_id=d.id
JOIN plugin_delivery_attempts a ON a.plugin_delivery_id = d.id
WHERE d.id = $1`, id).Scan(&deliveryState, &itemState, &reasonCode, &responseStatus, &providerAck); err != nil {
		t.Fatal(err)
	}
	if deliveryState != "delivered" || itemState != workitems.StateSucceeded || reasonCode != "" {
		t.Fatalf("delivery not delivered: state=%s item=%s reason=%q", deliveryState, itemState, reasonCode)
	}
	if responseStatus != http.StatusOK {
		t.Fatalf("attempt response status = %d, want 200", responseStatus)
	}
	// allowlistedProviderAck keeps only ok + integer message_id.
	if providerAck != `{"ok": true, "message_id": 777}` {
		t.Errorf("telegram provider ack = %s, want typed {ok,message_id}", providerAck)
	}
}

// TestHandlerSendsTheFrozenPayloadAndFailsClosedOnMalformed proves the dispatch
// path reads the payload the delivery row persisted: a canonical payload is
// submitted verbatim inside the webhook document, and a legacy payload that is
// not canonical fails the delivery closed without creating an attempt or
// sending anything.
// seedWebhookHandlerDelivery creates a webhook target, accepts one test
// delivery and claims it, so the submitted document can be inspected.
func seedWebhookHandlerDelivery(t *testing.T) (*Service, string, workitems.Claim) {
	t.Helper()
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	input := validCreateInput(t)
	input.Name = "handler-webhook-target"
	input.AdapterKind = AdapterWebhook
	input.Config = webhookConfig(t, "none")
	input.Enabled = true
	input.Secret = SecretInput{Mode: "set", Value: json.RawMessage(`{"adapter_kind":"webhook","endpoint_url":"https://example.test/notify"}`)}
	target, err := service.CreateTarget(ctx, input, "handler-webhook-create-key")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.TestTarget(ctx, target.ID, TargetTestInput{ExpectedVersion: 1, TriggerKind: TriggerDownloadSubmitted}, "handler-webhook-test-key")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := service.workItems.ClaimDue(ctx, "handler-webhook-worker", []string{workitems.KindPluginDeliver}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return service, accepted.DeliveryID, claim
}

func TestHandlerSendsTheFrozenPayloadAndFailsClosedOnMalformed(t *testing.T) {
	service, id, claim := seedWebhookHandlerDelivery(t)
	ctx := context.Background()

	// The writer persisted the canonical payload, so a plain dispatch succeeds
	// and the submitted document carries exactly those frozen bytes.
	var storedPayload []byte
	if err := service.db.QueryRowContext(ctx, `SELECT payload FROM plugin_deliveries WHERE id=$1`, id).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	canonical, status := deliverypayload.Normalize(storedPayload)
	if status != deliverypayload.StatusAvailable {
		t.Fatalf("the writer must persist a canonical payload, got %q for %s", status, storedPayload)
	}
	client := &webhookRecordingClient{}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	outcome, err := handler.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	finishHandlerOutcome(t, service, claim, outcome)
	if client.calls != 1 {
		t.Fatalf("http calls = %d, want 1", client.calls)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(client.lastBody, &body); err != nil {
		t.Fatalf("webhook body is not a JSON object: %v (%s)", err, client.lastBody)
	}
	frozenInBody, ok := body["payload"]
	if !ok {
		t.Fatalf("the submitted document must carry the frozen payload: %s", client.lastBody)
	}
	// The envelope carries the frozen facts under their registry paths.
	var frozen struct {
		Facts map[string]any `json:"facts"`
	}
	if err := json.Unmarshal(canonical, &frozen); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(nestUnifiedFacts(frozen.Facts))
	if string(frozenInBody) != string(want) {
		t.Fatalf("submitted payload = %s, want the frozen facts %s", frozenInBody, want)
	}
	if string(body["version"]) != `"`+WebhookEnvelopeVersion+`"` || string(body["delivery_id"]) != `"`+id+`"` {
		t.Fatalf("envelope version/delivery_id = %s %s", body["version"], body["delivery_id"])
	}

	// A legacy row keeps its old bytes and fails closed: no attempt, no send.
	service2, legacyID, legacyClaim := seedWebhookHandlerDelivery(t)
	if _, err := service2.db.ExecContext(ctx, `UPDATE plugin_deliveries SET payload='{"expected_version":1,"trigger_kind":"download_submitted"}'::jsonb WHERE id=$1`, legacyID); err != nil {
		t.Fatal(err)
	}
	legacyClient := &webhookRecordingClient{}
	legacyHandler := NewPluginDeliverHandler(service2.db, service2.secrets, service2.workItems, legacyClient, &fakeTemplateRenderer{title: "title", body: "body"})
	legacyOutcome, err := legacyHandler.Handle(ctx, legacyClaim)
	if err != nil {
		t.Fatal(err)
	}
	finishHandlerOutcome(t, service2, legacyClaim, legacyOutcome)
	if legacyClient.calls != 0 {
		t.Fatalf("a malformed frozen payload must not be sent, calls = %d", legacyClient.calls)
	}
	if legacyOutcome.OwnerResult.ReasonCode != "delivery_payload_invalid" {
		t.Fatalf("reason code = %q, want delivery_payload_invalid", legacyOutcome.OwnerResult.ReasonCode)
	}
	var attempts int
	var state string
	var lastErrorCode sql.NullString
	if err := service2.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, legacyID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("a malformed payload must not create an attempt, got %d", attempts)
	}
	if err := service2.db.QueryRowContext(ctx, `SELECT state, last_error_code FROM plugin_deliveries WHERE id=$1`, legacyID).Scan(&state, &lastErrorCode); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !lastErrorCode.Valid || lastErrorCode.String != "delivery_payload_invalid" {
		t.Fatalf("delivery state = %q/%v, want failed/delivery_payload_invalid", state, lastErrorCode)
	}
}

// webhookRecordingClient records the last request body for assertion.
type webhookRecordingClient struct {
	calls    int
	lastBody []byte
}

func (c *webhookRecordingClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	if req.Body != nil {
		c.lastBody, _ = io.ReadAll(req.Body)
	}
	return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
}

// TestHandlerFailsClosedOnLegacyAdapterConfig proves a delivery whose frozen
// adapter config is not a canonical allowlisted document never reaches the
// transport: the dispatch fails the delivery closed instead of sending a
// request built from unprovable bytes.
func TestHandlerFailsClosedOnLegacyAdapterConfig(t *testing.T) {
	service, id, claim := seedWebhookHandlerDelivery(t)
	ctx := context.Background()

	if _, err := service.db.ExecContext(ctx, `
UPDATE plugin_deliveries
SET adapter_config_snapshot = '{"adapter_kind":"webhook","auth_kind":"none","auth_value":"legacy-secret"}'::jsonb,
    config_status = 'invalid'
WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	client := &webhookRecordingClient{}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, &fakeTemplateRenderer{title: "title", body: "body"})
	outcome, err := handler.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	finishHandlerOutcome(t, service, claim, outcome)

	if client.calls != 0 {
		t.Fatalf("a legacy adapter config must not be sent, calls = %d", client.calls)
	}
	if outcome.OwnerResult.ReasonCode != "delivery_config_invalid" {
		t.Fatalf("reason code = %q, want delivery_config_invalid", outcome.OwnerResult.ReasonCode)
	}
	var attempts int
	if err := service.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("a legacy adapter config must not create an attempt, got %d", attempts)
	}
	var state string
	var lastErrorCode sql.NullString
	if err := service.db.QueryRowContext(ctx, `SELECT state, last_error_code FROM plugin_deliveries WHERE id=$1`, id).Scan(&state, &lastErrorCode); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !lastErrorCode.Valid || lastErrorCode.String != "delivery_config_invalid" {
		t.Fatalf("delivery state = %q/%v, want failed/delivery_config_invalid", state, lastErrorCode)
	}
}
func TestPluginPrepareDiagnosticRealWriterClosesSameEpisode(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	writer, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID := "plugin-prepare-recovery-delivery"
	identity := observabilityport.PluginPrepareFailure(observabilityport.ErrorNotificationTemplateInvalid)
	if err := writer.RecordDiagnosticFailure(ctx, observabilityport.DiagnosticFailureRequest{
		Identity: identity, Severity: observabilityport.SeverityError, ScopeID: deliveryID, LastError: "invalid notification template",
	}); err != nil {
		t.Fatalf("record prepare diagnostic failure: %v", err)
	}
	var id string
	var succeededAfter sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT id, succeeded_after_at FROM diagnostic_episodes WHERE component=$1 AND operation=$2 AND error_code=$3 AND scope_kind=$4 AND scope_id=$5`, identity.Component, identity.Operation, identity.ErrorCode, identity.ScopeKind, deliveryID).Scan(&id, &succeededAfter); err != nil {
		t.Fatal(err)
	}
	if id == "" || succeededAfter.Valid {
		t.Fatalf("failure episode before recovery: id=%q succeeded_after=%v", id, succeededAfter)
	}
	if err := writer.RecordDiagnosticSuccess(ctx, observabilityport.DiagnosticSuccessRequest{Identity: identity, ScopeID: deliveryID}); err != nil {
		t.Fatalf("close prepare diagnostic episode: %v", err)
	}
	var closedID string
	if err := db.QueryRowContext(ctx, `SELECT id, succeeded_after_at FROM diagnostic_episodes WHERE component=$1 AND operation=$2 AND error_code=$3 AND scope_kind=$4 AND scope_id=$5`, identity.Component, identity.Operation, identity.ErrorCode, identity.ScopeKind, deliveryID).Scan(&closedID, &succeededAfter); err != nil {
		t.Fatal(err)
	}
	if closedID != id || !succeededAfter.Valid {
		t.Fatalf("recovery did not close same episode: id=%q/%q succeeded_after=%v", id, closedID, succeededAfter)
	}
}
