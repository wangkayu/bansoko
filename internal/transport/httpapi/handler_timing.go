package httpapi

import (
	"net/http"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

func deferHandlerTiming(r *http.Request, operation string) func() {
	started := time.Now()
	return func() {
		durationMS := time.Since(started).Milliseconds()
		if durationMS < 1 {
			durationMS = 1
		}
		runtimeLoggerFrom(r.Context()).Info(r.Context(), observabilityport.RuntimeEvent{
			Message:    "http handler completed",
			Component:  "httpapi",
			Operation:  operation,
			Outcome:    "completed",
			RequestID:  RequestIDFrom(r.Context()),
			DurationMS: durationMS,
		})
	}
}
