package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/apperrors"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/transport/httpapi/generated"
)

type captureRuntimeLogger struct {
	events []observabilityport.RuntimeEvent
}

func (l *captureRuntimeLogger) Debug(_ context.Context, event observabilityport.RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *captureRuntimeLogger) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *captureRuntimeLogger) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *captureRuntimeLogger) Error(_ context.Context, event observabilityport.RuntimeEvent) {
	l.events = append(l.events, event)
}

type panicRuntimeLogger struct {
	debugCalls int
	infoCalls  int
	warnCalls  int
	errorCalls int
}

func (l *panicRuntimeLogger) Debug(context.Context, observabilityport.RuntimeEvent) {
	l.debugCalls++
	panic("runtime logger failure")
}
func (l *panicRuntimeLogger) Info(context.Context, observabilityport.RuntimeEvent) {
	l.infoCalls++
	panic("runtime logger failure")
}
func (l *panicRuntimeLogger) Warn(context.Context, observabilityport.RuntimeEvent) {
	l.warnCalls++
	panic("runtime logger failure")
}
func (l *panicRuntimeLogger) Error(context.Context, observabilityport.RuntimeEvent) {
	l.errorCalls++
	panic("runtime logger failure")
}

type captureDiagnosticRecorder struct {
	failures      []observabilityport.DiagnosticFailureRequest
	contexts      []context.Context
	contextErrors []error
	deadlines     []time.Time
	hasDeadline   []bool
	err           error
	panic         bool
}

func (r *captureDiagnosticRecorder) UpsertRoutine(context.Context, observabilityport.RoutineRequest) error {
	return nil
}

func (r *captureDiagnosticRecorder) RecordDiagnosticFailure(ctx context.Context, failure observabilityport.DiagnosticFailureRequest) error {
	r.contexts = append(r.contexts, ctx)
	if ctx != nil {
		r.contextErrors = append(r.contextErrors, ctx.Err())
		deadline, ok := ctx.Deadline()
		r.deadlines = append(r.deadlines, deadline)
		r.hasDeadline = append(r.hasDeadline, ok)
	}
	r.failures = append(r.failures, failure)
	if r.panic {
		panic("diagnostic recorder panic")
	}
	return r.err
}

func (r *captureDiagnosticRecorder) RecordDiagnosticSuccess(context.Context, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func (r *captureDiagnosticRecorder) UpsertRoutineTx(context.Context, txscope.Tx, observabilityport.RoutineRequest) error {
	return nil
}

func (r *captureDiagnosticRecorder) RecordDiagnosticFailureTx(context.Context, txscope.Tx, observabilityport.DiagnosticFailureRequest) error {
	return nil
}

func (r *captureDiagnosticRecorder) RecordDiagnosticSuccessTx(context.Context, txscope.Tx, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func requestWithRuntimeLogger(requestID string, logger RuntimeLogger) *http.Request {
	return requestWithDependencies(requestID, logger, nil)
}

func requestWithDependencies(requestID string, logger RuntimeLogger, recorder observabilityport.Recorder) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	ctx := context.WithValue(r.Context(), requestIDKey, requestID)
	ctx = context.WithValue(ctx, runtimeLoggerKey, logger)
	if recorder != nil {
		ctx = context.WithValue(ctx, diagnosticRecorderKey, recorder)
	}
	return r.WithContext(ctx)
}

func TestRuntimeLoggerFromRecoversEveryLevelAndUsesNopFallback(t *testing.T) {
	logger := &panicRuntimeLogger{}
	ctx := context.WithValue(context.Background(), runtimeLoggerKey, logger)
	adapted := runtimeLoggerFrom(ctx)
	event := observabilityport.RuntimeEvent{}

	adapted.Debug(ctx, event)
	adapted.Info(ctx, event)
	adapted.Warn(ctx, event)
	adapted.Error(ctx, event)

	if logger.debugCalls != 1 || logger.infoCalls != 1 || logger.warnCalls != 1 || logger.errorCalls != 1 {
		t.Fatalf("panic logger calls = debug:%d info:%d warn:%d error:%d, want one call at every level", logger.debugCalls, logger.infoCalls, logger.warnCalls, logger.errorCalls)
	}

	fallback := runtimeLoggerFrom(nil)
	fallback.Debug(context.Background(), event)
	fallback.Info(context.Background(), event)
	fallback.Warn(context.Background(), event)
	fallback.Error(context.Background(), event)
}

func TestWriteErrorUnknownUsesRequestIDAndSafeRuntimeEvent(t *testing.T) {
	const requestID = "request-unknown-500"
	const rawError = "postgres://user:password@example.test/db secret-token"
	logger := &captureRuntimeLogger{}
	r := requestWithRuntimeLogger(requestID, logger)
	recorder := httptest.NewRecorder()

	writeError(recorder, r, errors.New(rawError))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"INTERNAL"`)) {
		t.Fatalf("response = %q, want INTERNAL", recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"request_id":"`+requestID+`"`)) {
		t.Fatalf("response = %q, want request ID %q", recorder.Body.String(), requestID)
	}
	if len(logger.events) != 1 {
		t.Fatalf("runtime events = %d, want 1", len(logger.events))
	}
	event := logger.events[0]
	// Runtime diagnostics use fixed identity plus request correlation; the raw
	// error is never included, even when it contains credentials.
	if event.Message != "request failed with unknown application error" || event.Component != "httpapi" || event.Operation != "request" || event.Outcome != "failure" || event.ReasonCode != "unknown_error" || event.RequestID != requestID || event.ErrorClass != "unknown_application_error" {
		t.Fatalf("unexpected runtime event: %+v", event)
	}
	if event.Retryable != nil || event.Attempt != 0 || event.DurationMS != 0 {
		t.Fatalf("unexpected optional runtime fields: %+v", event)
	}
	if strings.Contains(event.Message, "password") || strings.Contains(event.Message, "secret-token") || strings.Contains(event.Message, "postgres://") {
		t.Fatalf("runtime event leaks the raw error: %q", event.Message)
	}
}

// An unrouted request (no ServeMux dispatch) keeps the fixed "request"
// operation: there is no route template to name.
func TestWriteErrorUnknownWithoutRouteKeepsFixedOperation(t *testing.T) {
	logger := &captureRuntimeLogger{}
	r := requestWithRuntimeLogger("request-unknown-unrouted", logger)

	writeError(httptest.NewRecorder(), r, errors.New("boom"))

	if len(logger.events) != 1 || logger.events[0].Operation != "request" {
		t.Fatalf("operation = %+v, want the fixed \"request\"", logger.events)
	}
}

// The unknown-failure log must name the contract operation the request
// matched, so a 500 can be traced to its endpoint from the log alone. The
// operationId carries no request content and survives the runtime redaction
// that masks any value holding a "/".
func TestWriteErrorUnknownLogsTheMatchedOperation(t *testing.T) {
	logger := &captureRuntimeLogger{}
	base := requestWithRuntimeLogger("request-unknown-route", logger)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/provider-feed-snapshots/{snapshot_id}/seeds/{stable_seed_key}/expand", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, errors.New("boom"))
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/provider-feed-snapshots/snap-1/seeds/seed-1/expand", nil).WithContext(base.Context())
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if len(logger.events) != 1 {
		t.Fatalf("runtime events = %d, want 1", len(logger.events))
	}
	const want = "request expandSnapshotSeed"
	if logger.events[0].Operation != want {
		t.Fatalf("operation = %q, want %q", logger.events[0].Operation, want)
	}
	if strings.Contains(logger.events[0].Operation, "snap-1") || strings.Contains(logger.events[0].Operation, "seed-1") {
		t.Fatalf("operation leaks the concrete request path: %q", logger.events[0].Operation)
	}
}

// The written runtime line keeps the operation and a database refusal's
// SQLSTATE and constraint unmasked, and never carries the error text.
func TestWriteErrorUnknownRuntimeLineNamesOperationAndDatabaseClass(t *testing.T) {
	var line bytes.Buffer
	base := requestWithRuntimeLogger("request-unknown-pg", observabilityapplication.NewJSONRuntimeLogger(&line))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/one-shot-acquisitions", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, fmt.Errorf("insert marker: %w", &pgconn.PgError{
			Code: "23514", ConstraintName: "one_shot_acquisitions_input_snapshot_check",
			Message: `new row violates check constraint at https://tracker.example.test/announce?passkey=abc`,
		}))
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/one-shot-acquisitions", nil).WithContext(base.Context())
	mux.ServeHTTP(httptest.NewRecorder(), request)

	var event map[string]any
	if err := json.Unmarshal(line.Bytes(), &event); err != nil {
		t.Fatalf("decode runtime line %q: %v", line.String(), err)
	}
	if event["operation"] != "request acquireSeedOnce" {
		t.Fatalf("operation = %v, want request acquireSeedOnce (line %s)", event["operation"], line.String())
	}
	if event["error_class"] != "postgres_23514:one_shot_acquisitions_input_snapshot_check" {
		t.Fatalf("error_class = %v (line %s)", event["error_class"], line.String())
	}
	if strings.Contains(line.String(), "passkey") || strings.Contains(line.String(), "tracker.example.test") || strings.Contains(line.String(), "violates") {
		t.Fatalf("runtime line leaks the error text: %s", line.String())
	}
}

func TestUnknownFailureClass(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{errors.New("boom"), "unknown_application_error"},
		{nil, "unknown_application_error"},
		{&pgconn.PgError{Code: "40001"}, "postgres_40001"},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505", ConstraintName: "releases_pkey"}), "postgres_23505:releases_pkey"},
		// A constraint name that is not a plain identifier is dropped, never
		// copied into the class.
		{&pgconn.PgError{Code: "23514", ConstraintName: "bad name; DROP"}, "postgres_23514"},
		{&pgconn.PgError{Code: "not-a-state"}, "unknown_application_error"},
	} {
		if got := unknownFailureClass(test.err); got != test.want {
			t.Errorf("unknownFailureClass(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestWriteErrorUnknownRecordsFixedRedactedDiagnosticFailure(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
		rawError  string
	}{
		{name: "ordinary error", requestID: "request-diagnostic-ordinary", rawError: "ordinary backend failure"},
		{name: "sensitive database error", requestID: "request-diagnostic-sensitive", rawError: `list anime: ERROR: relation "managed_anime" does not exist SQLSTATE 42P01`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := &captureRuntimeLogger{}
			diagnostic := &captureDiagnosticRecorder{}
			r := requestWithDependencies(test.requestID, logger, diagnostic)
			response := httptest.NewRecorder()

			writeError(response, r, errors.New(test.rawError))

			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", response.Code)
			}
			if !bytes.Contains(response.Body.Bytes(), []byte(`"request_id":"`+test.requestID+`"`)) {
				t.Fatalf("response = %q, want request ID %q", response.Body.String(), test.requestID)
			}
			if len(diagnostic.failures) != 1 {
				t.Fatalf("diagnostic failures = %d, want 1", len(diagnostic.failures))
			}
			failure := diagnostic.failures[0]
			if failure.Severity != observabilityport.SeverityError || failure.Condition.Kind != observabilityport.KindInternal || failure.TargetID != observabilityport.InternalHTTP5xx {
				t.Fatalf("unexpected diagnostic identity: %+v", failure)
			}
			if failure.RequestID != test.requestID || failure.AffectedID != "" {
				t.Fatalf("diagnostic carries unexpected context: %+v", failure)
			}
			if len(logger.events) != 1 {
				t.Fatalf("runtime events = %+v, want one event", logger.events)
			}
			event := logger.events[0]
			if event.RequestID != test.requestID || event.Message != "request failed with unknown application error" {
				t.Fatalf("runtime event = %+v, want fixed message and request ID", event)
			}

			observabilityValues := event.Message + "\x00" + failure.RequestID
			if strings.Contains(observabilityValues, test.rawError) {
				t.Fatalf("raw error entered runtime/episode values: %q", observabilityValues)
			}
			for _, forbidden := range []string{"relation", "sqlstate", "managed_anime"} {
				if strings.Contains(strings.ToLower(observabilityValues), forbidden) {
					t.Fatalf("database error detail %q entered runtime/episode values: %q", forbidden, observabilityValues)
				}
			}
		})
	}
}

func TestWriteErrorDiagnosticRecorderFailurePreservesResponseAndRequestID(t *testing.T) {
	const requestID = "request-diagnostic-write-failure"
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{err: errors.New("postgres://user:password@db.example/secret")}
	r := requestWithDependencies(requestID, logger, diagnostic)
	response := httptest.NewRecorder()

	writeError(response, r, errors.New("internal component failed"))

	if response.Code != http.StatusInternalServerError || !bytes.Contains(response.Body.Bytes(), []byte(`"request_id":"`+requestID+`"`)) {
		t.Fatalf("response = %d %q, want generic 500 with request ID", response.Code, response.Body.String())
	}
	if len(logger.events) != 2 {
		t.Fatalf("runtime events = %d, want unknown error and recorder failure", len(logger.events))
	}
	if logger.events[0].ReasonCode != "unknown_error" || logger.events[1].ReasonCode != "diagnostic_write_failed" {
		t.Fatalf("runtime events = %+v", logger.events)
	}
	failureEvent := logger.events[1]
	if failureEvent.RequestID != requestID || failureEvent.Message != "diagnostic write failed" || failureEvent.Component != "httpapi" || failureEvent.Operation != "request" {
		t.Fatalf("unexpected recorder failure event: %+v", failureEvent)
	}
	encoded, err := json.Marshal(failureEvent)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("postgres")) || bytes.Contains(encoded, []byte("password")) || bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("recorder failure event leaked recorder error: %s", encoded)
	}
}

func TestWriteErrorDiagnosticRecorderPanicPreservesResponse(t *testing.T) {
	const requestID = "request-diagnostic-recorder-panic"
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{panic: true}
	r := requestWithDependencies(requestID, logger, diagnostic)
	response := httptest.NewRecorder()

	writeError(response, r, errors.New("internal component failed"))

	if response.Code != http.StatusInternalServerError || !bytes.Contains(response.Body.Bytes(), []byte(`"request_id":"`+requestID+`"`)) {
		t.Fatalf("response = %d %q, want generic 500 with request ID", response.Code, response.Body.String())
	}
	if len(logger.events) != 2 || logger.events[1].ReasonCode != "diagnostic_write_failed" {
		t.Fatalf("runtime events = %+v, want recorder failure event", logger.events)
	}
}

func TestWriteErrorRecorderUsesDetachedDeadlineAfterRequestCancellation(t *testing.T) {
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{}
	r := requestWithDependencies("request-canceled", logger, diagnostic)
	ctx, cancel := context.WithCancel(r.Context())
	r = r.WithContext(ctx)
	cancel()
	response := httptest.NewRecorder()

	writeError(response, r, errors.New("internal component failed"))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if len(diagnostic.contextErrors) != 1 || diagnostic.contextErrors[0] != nil {
		t.Fatalf("recorder context errors = %+v, want nil while recorder was called", diagnostic.contextErrors)
	}
	if len(diagnostic.hasDeadline) != 1 || !diagnostic.hasDeadline[0] {
		t.Fatalf("recorder context hasDeadline = %v, want true", diagnostic.hasDeadline)
	}
	if len(diagnostic.deadlines) != 1 || !diagnostic.deadlines[0].After(time.Now()) {
		t.Fatalf("recorder deadline = %v, want a future deadline", diagnostic.deadlines)
	}
}

func TestWriteErrorSurvivesRuntimeLoggerPanic(t *testing.T) {
	logger := &panicRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{err: errors.New("recorder failed")}
	r := requestWithDependencies("request-logger-panic", logger, diagnostic)
	response := httptest.NewRecorder()

	writeError(response, r, errors.New("internal component failed"))

	if response.Code != http.StatusInternalServerError || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"INTERNAL"`)) {
		t.Fatalf("response = %d %q, want generic 500", response.Code, response.Body.String())
	}
	if len(diagnostic.failures) != 1 {
		t.Fatalf("diagnostic failures = %+v, want recorder to run after logger panic", diagnostic.failures)
	}
	if logger.errorCalls != 2 {
		t.Fatalf("logger error calls = %d, want unknown and recorder-failure attempts", logger.errorCalls)
	}
}

func TestWriteErrorWrappedTyped500RecordsUnknownFailure(t *testing.T) {
	const requestID = "request-typed-500"
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{}
	r := requestWithDependencies(requestID, logger, diagnostic)
	response := httptest.NewRecorder()

	writeError(response, r, fmt.Errorf("outer: %w", &APIError{Status: http.StatusInternalServerError, Code: "INTERNAL_TYPED", Message: "The typed request failed."}))

	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL_TYPED"`) {
		t.Fatalf("response = %d %q, want typed 500", response.Code, response.Body.String())
	}
	if len(logger.events) != 1 || logger.events[0].ReasonCode != "unknown_error" {
		t.Fatalf("runtime events = %+v, want one unknown-error event", logger.events)
	}
	if len(diagnostic.failures) != 1 || diagnostic.failures[0].TargetID != observabilityport.InternalHTTP5xx {
		t.Fatalf("diagnostic failures = %+v, want fixed http_500 bucket", diagnostic.failures)
	}
}

// A canceled or expired request context means the caller is gone; it is not a
// server fault and must never enter the fixed unknown-500 diagnostic bucket.
// Regression: POST /api/v1/anime/rss-list-view aborted by the browser while it
// was reading the release parser evidence surfaced as a false unknown 500.
func TestWriteErrorRequestCancellationStaysOutOfTheUnknownBucket(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "wrapped context canceled", err: fmt.Errorf("resolve release parser evidence %s: %w", "release-1", context.Canceled)},
		{name: "wrapped deadline exceeded", err: fmt.Errorf("resolve release parser evidence %s: %w", "release-1", context.DeadlineExceeded)},
		{name: "typed 500 wrapping the canceled read", err: fmt.Errorf("outer: %w: %w", context.Canceled, &APIError{Status: http.StatusInternalServerError, Code: "INTERNAL_TYPED", Message: "The typed request failed."})},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := &captureRuntimeLogger{}
			diagnostic := &captureDiagnosticRecorder{}
			r := requestWithDependencies("request-canceled-read", logger, diagnostic)
			response := httptest.NewRecorder()

			writeError(response, r, test.err)

			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", response.Code)
			}
			if len(logger.events) != 0 {
				t.Fatalf("runtime events = %+v, want none for a canceled request", logger.events)
			}
			if len(diagnostic.failures) != 0 {
				t.Fatalf("diagnostic failures = %+v, want none for a canceled request", diagnostic.failures)
			}
		})
	}
}

func TestWriteErrorTyped503RemainsQuiet(t *testing.T) {
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{}
	r := requestWithDependencies("request-typed-503", logger, diagnostic)
	response := httptest.NewRecorder()

	writeError(response, r, fmt.Errorf("outer: %w", unavailable("DEPENDENCY_UNAVAILABLE", "The dependency is unavailable.")))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if len(logger.events) != 0 || len(diagnostic.failures) != 0 {
		t.Fatalf("typed 503 emitted observability records: logger=%+v diagnostic=%+v", logger.events, diagnostic.failures)
	}
}

func TestWriteErrorTypedClientErrorIsQuiet(t *testing.T) {
	logger := &captureRuntimeLogger{}
	diagnostic := &captureDiagnosticRecorder{}
	r := requestWithDependencies("request-typed-4xx", logger, diagnostic)
	recorder := httptest.NewRecorder()

	writeError(recorder, r, badRequest("REQUEST_INVALID", "The request is invalid."))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if len(logger.events) != 0 {
		t.Fatalf("typed 4xx emitted runtime log: %+v", logger.events)
	}
	if len(diagnostic.failures) != 0 {
		t.Fatalf("typed 4xx emitted diagnostic episode: %+v", diagnostic.failures)
	}
}

func TestWriteErrorKnownApplicationErrorsAreQuiet(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not found", err: &apperrors.NotFound{Resource: "anime", ID: "anime-1"}, status: http.StatusNotFound},
		{name: "version conflict", err: &apperrors.VersionConflict{Owner: "anime", CurrentVersion: 3}, status: http.StatusConflict},
		{name: "business", err: &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "SERVICE_BUSY", Message: "try again"}, status: http.StatusServiceUnavailable},
		{name: "scan in progress", err: &apperrors.Business{Status: http.StatusConflict, Code: "SCAN_IN_PROGRESS", Message: "A scan is already running for this instance. Try again after it finishes."}, status: http.StatusConflict, code: "SCAN_IN_PROGRESS"},
		{name: "bad request", err: &apperrors.BadRequest{Code: "REQUEST_INVALID", Message: "invalid"}, status: http.StatusBadRequest},
		{name: "validation", err: &apperrors.Validation{Code: "VALIDATION_FAILED"}, status: http.StatusUnprocessableEntity},
		{
			name:   "bootstrap too old",
			err:    &apperrors.BootstrapTooOldError{Required: "embedded-bootstrap-v2", Current: "embedded-bootstrap-v1"},
			status: http.StatusConflict,
			code:   "BOOTSTRAP_TOO_OLD",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := &captureRuntimeLogger{}
			r := requestWithRuntimeLogger("request-known-application", logger)
			recorder := httptest.NewRecorder()

			writeError(recorder, r, test.err)

			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
			if test.code != "" {
				var payload generated.ErrorResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if payload.Code != test.code {
					t.Fatalf("code = %q, want %q", payload.Code, test.code)
				}
			}
			if len(logger.events) != 0 {
				t.Fatalf("known application error emitted runtime log: %+v", logger.events)
			}
		})
	}
}

func TestWriteJSONEncodeFailureEmitsSafeRuntimeEvent(t *testing.T) {
	const requestID = "request-encode-failure"
	logger := &captureRuntimeLogger{}
	ctx := context.WithValue(context.WithValue(context.Background(), requestIDKey, requestID), runtimeLoggerKey, logger)
	recorder := httptest.NewRecorder()

	writeJSON(recorder, ctx, http.StatusOK, func() {})

	if len(logger.events) != 1 {
		t.Fatalf("runtime events = %d, want 1", len(logger.events))
	}
	event := logger.events[0]
	if event.Component != "httpapi" || event.Operation != "response_encode" || event.Outcome != "failure" || event.ReasonCode != "response_encode_failed" || event.RequestID != requestID || event.ErrorClass != "response_encoding_error" {
		t.Fatalf("unexpected encode event: %+v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("func")) || bytes.Contains(encoded, []byte("unsupported")) {
		t.Fatalf("encode event leaked raw encoder error: %s", encoded)
	}
}
