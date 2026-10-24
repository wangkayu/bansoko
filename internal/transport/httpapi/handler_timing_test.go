package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeferHandlerTimingEmitsDuration(t *testing.T) {
	logger := &captureRuntimeLogger{}
	r := (&http.Request{}).WithContext(context.WithValue(
		context.WithValue(context.Background(), requestIDKey, "timing-request"),
		runtimeLoggerKey,
		logger,
	))

	finish := deferHandlerTiming(r, "preview_anime")
	finish()

	if len(logger.events) != 1 {
		t.Fatalf("runtime events = %d, want 1", len(logger.events))
	}
	event := logger.events[0]
	if event.Component != "httpapi" || event.Operation != "preview_anime" || event.Outcome != "completed" || event.RequestID != "timing-request" {
		t.Fatalf("event = %+v", event)
	}
	if event.DurationMS < 1 {
		t.Fatalf("duration_ms = %d, want at least 1", event.DurationMS)
	}

}

func TestSuccessfulHandlerKeepsResponseWhenTimingLoggerPanics(t *testing.T) {
	logger := &panicRuntimeLogger{}
	server := &Server{deps: Dependencies{
		Environment:   "development",
		SkipAuth:      true,
		RuntimeLogger: logger,
	}}
	handler := server.withCommonMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer deferHandlerTiming(r, "timed_success")()
		_, _ = w.Write([]byte("success body"))
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	request.Header.Set("X-Request-ID", "timing-panic-request")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "success body" {
		t.Fatalf("response = %d %q, want 200 with successful body", response.Code, response.Body.String())
	}
	if logger.infoCalls != 1 {
		t.Fatalf("timing logger info calls = %d, want 1", logger.infoCalls)
	}
}
