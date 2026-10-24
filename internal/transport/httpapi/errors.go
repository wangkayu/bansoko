package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/redact"
	"bansoko/internal/transport/httpapi/generated"
)

// RuntimeLogger is kept as an alias for callers while the contract remains
// owned by the observability port.
type RuntimeLogger = observabilityport.RuntimeLogger

var defaultRuntimeLogger RuntimeLogger = observabilityport.NopRuntimeLogger{}

type panicSafeRuntimeLogger struct {
	delegate RuntimeLogger
}

func (l panicSafeRuntimeLogger) Debug(ctx context.Context, event observabilityport.RuntimeEvent) {
	defer func() { _ = recover() }()
	if l.delegate != nil {
		l.delegate.Debug(ctx, event)
	}
}

func (l panicSafeRuntimeLogger) Info(ctx context.Context, event observabilityport.RuntimeEvent) {
	defer func() { _ = recover() }()
	if l.delegate != nil {
		l.delegate.Info(ctx, event)
	}
}

func (l panicSafeRuntimeLogger) Warn(ctx context.Context, event observabilityport.RuntimeEvent) {
	defer func() { _ = recover() }()
	if l.delegate != nil {
		l.delegate.Warn(ctx, event)
	}
}

func (l panicSafeRuntimeLogger) Error(ctx context.Context, event observabilityport.RuntimeEvent) {
	defer func() { _ = recover() }()
	if l.delegate != nil {
		l.delegate.Error(ctx, event)
	}
}

func runtimeLoggerFrom(ctx context.Context) RuntimeLogger {
	logger := defaultRuntimeLogger
	if ctx != nil {
		if candidate, ok := ctx.Value(runtimeLoggerKey).(RuntimeLogger); ok && candidate != nil {
			logger = candidate
		}
	}
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	return panicSafeRuntimeLogger{delegate: logger}
}

func diagnosticRecorderFrom(ctx context.Context) observabilityport.Recorder {
	if ctx != nil {
		if recorder, ok := ctx.Value(diagnosticRecorderKey).(observabilityport.Recorder); ok && recorder != nil {
			return recorder
		}
	}
	return nil
}

// APIError is the typed transport error. Application packages return stable
// sentinel errors; handlers convert them into this safe HTTP shape.
type APIError struct {
	Status         int
	Code           string
	Message        string
	Details        []generated.ErrorDetail
	CurrentVersion *int64
	// RequiredScope names the missing bearer scope of a 403
	// TOKEN_SCOPE_INSUFFICIENT.
	RequiredScope string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func badRequest(code, message string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: code, Message: message}
}

func unauthorized(code, message string) *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: code, Message: message}
}

// scopeInsufficient is the stable 403 for a bearer (an API token) that lacks
// the scope an operation or a requested token needs.
func scopeInsufficient(scope string) *APIError {
	return &APIError{
		Status:        http.StatusForbidden,
		Code:          "TOKEN_SCOPE_INSUFFICIENT",
		Message:       "This API token does not have the " + scope + " scope needed for this request.",
		RequiredScope: scope,
	}
}

func webSessionRequired() *APIError {
	return &APIError{
		Status:  http.StatusForbidden,
		Code:    "WEB_SESSION_REQUIRED",
		Message: "Only a signed-in web session can do this; API tokens cannot.",
	}
}

// forbiddenCSRF is the stable 403 for a cookie-authenticated request whose
// X-CSRF-Token does not match the double-submit cookie.
func forbiddenCSRF() *APIError {
	return &APIError{
		Status:  http.StatusForbidden,
		Code:    "CSRF_TOKEN_INVALID",
		Message: "This request changed state without a matching CSRF token. Reload the page and try again.",
	}
}

func notFound(code, message string) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: code, Message: message}
}

func conflict(code, message string, currentVersion *int64) *APIError {
	return &APIError{Status: http.StatusConflict, Code: code, Message: message, CurrentVersion: currentVersion}
}

func unprocessable(code, message string, details []generated.ErrorDetail) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: code, Message: message, Details: details}
}

func unavailable(code, message string) *APIError {
	return &APIError{Status: http.StatusServiceUnavailable, Code: code, Message: message}
}

func serviceNotConfigured(name string) *APIError {
	return unavailable("SERVICE_NOT_CONFIGURED", name+" is not configured on this server")
}

// writeJSON emits a contract response. Generated types are the only response
// bodies; no handwritten parallel DTO crosses this boundary.
func writeJSON(w http.ResponseWriter, ctx context.Context, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		runtimeLoggerFrom(ctx).Error(ctx, observabilityport.RuntimeEvent{
			Message:    "response encoding failed",
			Component:  "httpapi",
			Operation:  "response_encode",
			Outcome:    "failure",
			ReasonCode: "response_encode_failed",
			RequestID:  RequestIDFrom(ctx),
			ErrorClass: "response_encoding_error",
		})
	}
}

// errRequestPanicRecovered intentionally has no message: panic values never
// cross the response, runtime-log, or diagnostic-episode boundaries.
var errRequestPanicRecovered = errors.New("")

// writeError maps an error to the generated ErrorResponse shape with the
// request ID. Unsafe internals never cross the boundary.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}

	// A canceled or expired request context is not a server fault: the client is
	// gone (or stopped waiting), so there is nobody to answer and nothing to
	// diagnose. It must never enter the fixed unknown-500 diagnostic bucket, or a
	// browser that aborts an in-flight read (navigation, refetch cancellation)
	// becomes a false internal error. The image proxy answers cancellation the
	// same quiet way (server_images.go).
	canceled := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)

	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		// Expected dependency-unavailable responses are intentionally quiet. Any
		// other typed 5xx still represents an unknown fault at this boundary and
		// enters the fixed HTTP diagnostic bucket.
		if apiErr.Status >= http.StatusInternalServerError && apiErr.Status != http.StatusServiceUnavailable && !canceled {
			recordUnknownRequestFailure(ctx, err)
		}
	} else {
		apiErr = mapApplicationError(err)
		if !isKnownApplicationError(err) && !canceled {
			recordUnknownRequestFailure(ctx, err)
		}
	}
	requestID := RequestIDFrom(ctx)
	if requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	// Last-resort scrub: an error message that names a feed or torrent URL
	// (an upstream fetch error) never carries its credential out, whichever
	// owner built the message.
	response := generated.ErrorResponse{
		Code:           apiErr.Code,
		Message:        redact.URLsInText(apiErr.Message),
		RequestId:      &requestID,
		CurrentVersion: apiErr.CurrentVersion,
	}
	if len(apiErr.Details) > 0 {
		details := append([]generated.ErrorDetail(nil), apiErr.Details...)
		for index := range details {
			details[index].Message = redact.URLsInText(details[index].Message)
		}
		response.Details = &details
	}
	if apiErr.RequiredScope != "" {
		scope := generated.ApiTokenScope(apiErr.RequiredScope)
		response.RequiredScope = &scope
	}
	writeJSON(w, ctx, apiErr.Status, response)
}

func recordUnknownRequestFailure(ctx context.Context, _ error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := runtimeLoggerFrom(ctx)
	logger.Error(ctx, observabilityport.RuntimeEvent{
		Message:    "request failed with unknown application error",
		Component:  "httpapi",
		Operation:  "request",
		Outcome:    "failure",
		ReasonCode: "unknown_error",
		RequestID:  RequestIDFrom(ctx),
		ErrorClass: "unknown_application_error",
	})

	recorder := diagnosticRecorderFrom(ctx)
	if recorder == nil {
		return
	}
	// This fixed identity is a cumulative unknown-fault bucket, not an endpoint
	// health signal. Never report success here: an arbitrary successful request
	// must not close an episode for a later or concurrent unknown failure.
	failure := observabilityport.DiagnosticFailureRequest{
		Identity: observabilityport.HTTPRequestFailure(observabilityport.DiagnosticErrorCode("http_500")),
		Severity: observabilityport.SeverityError, ScopeID: "global", LastError: redact.Redacted,
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if diagnosticRecorderFailed(recordCtx, recorder, failure) {
		logger.Error(ctx, observabilityport.RuntimeEvent{
			Message:    "diagnostic write failed",
			Component:  "httpapi",
			Operation:  "request",
			Outcome:    "failure",
			ReasonCode: "diagnostic_write_failed",
			RequestID:  RequestIDFrom(ctx),
			ErrorClass: "diagnostic_recorder",
		})
	}
}

func diagnosticRecorderFailed(ctx context.Context, recorder observabilityport.Recorder, failure observabilityport.DiagnosticFailureRequest) (failed bool) {
	defer func() {
		if recover() != nil {
			failed = true
		}
	}()
	return recorder.RecordDiagnosticFailure(ctx, failure) != nil
}

func isKnownApplicationError(err error) bool {
	var notFound *apperrors.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var versionConflict *apperrors.VersionConflict
	if errors.As(err, &versionConflict) {
		return true
	}
	var imageCompatibility *apperrors.ImageCompatibilityError
	if errors.As(err, &imageCompatibility) {
		return true
	}
	var bootstrapTooOld *apperrors.BootstrapTooOldError
	if errors.As(err, &bootstrapTooOld) {
		return true
	}
	var business *apperrors.Business
	if errors.As(err, &business) {
		return true
	}
	var badRequest *apperrors.BadRequest
	if errors.As(err, &badRequest) {
		return true
	}
	var validation *apperrors.Validation
	return errors.As(err, &validation)
}

// mapApplicationError converts typed application errors into the safe HTTP
// shape. Unknown errors become a generic 500 and are never logged here because
// this function does not have access to the request context.
func mapApplicationError(err error) *APIError {
	var notFound *apperrors.NotFound
	if errors.As(err, &notFound) {
		return &APIError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "The requested owner was not found."}
	}
	var versionConflict *apperrors.VersionConflict
	if errors.As(err, &versionConflict) {
		current := versionConflict.CurrentVersion
		return &APIError{Status: http.StatusConflict, Code: "OWNER_VERSION_CONFLICT", Message: "The owner changed before this request completed.", CurrentVersion: &current}
	}
	var imageCompatibility *apperrors.ImageCompatibilityError
	if errors.As(err, &imageCompatibility) {
		return &APIError{Status: http.StatusConflict, Code: "UPDATE_REQUIRES_IMAGE", Message: "This update requires a newer base image. Update the image channel first."}
	}
	var bootstrapTooOld *apperrors.BootstrapTooOldError
	if errors.As(err, &bootstrapTooOld) {
		return &APIError{Status: http.StatusConflict, Code: "BOOTSTRAP_TOO_OLD", Message: "This update requires a newer bootstrap in the base image. Update the image channel first."}
	}
	var business *apperrors.Business
	if errors.As(err, &business) {
		status := business.Status
		if status != http.StatusConflict && status != http.StatusUnprocessableEntity && status != http.StatusServiceUnavailable {
			status = http.StatusConflict
		}
		return &APIError{Status: status, Code: business.Code, Message: business.Message}
	}
	var badRequest *apperrors.BadRequest
	if errors.As(err, &badRequest) {
		return &APIError{Status: http.StatusBadRequest, Code: badRequest.Code, Message: badRequest.Message}
	}
	var validation *apperrors.Validation
	if errors.As(err, &validation) {
		return &APIError{Status: http.StatusUnprocessableEntity, Code: validation.Code, Message: "The request failed typed validation.", Details: mapValidationDetails(validation.Details)}
	}
	return &APIError{Status: http.StatusInternalServerError, Code: "INTERNAL", Message: "The request failed."}
}

func mapValidationDetails(details []apperrors.FieldViolation) []generated.ErrorDetail {
	result := make([]generated.ErrorDetail, 0, len(details))
	for _, detail := range details {
		result = append(result, generated.ErrorDetail{
			Field:   detail.Field,
			Code:    detail.Code,
			Message: detail.Message,
			Start:   detail.Start,
			End:     detail.End,
		})
	}
	return result
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return badRequest("REQUEST_INVALID", "The request body could not be decoded.")
	}
	if decoder.More() {
		return badRequest("REQUEST_INVALID", "The request body must contain a single JSON document.")
	}
	return nil
}
