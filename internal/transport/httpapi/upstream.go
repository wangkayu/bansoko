package httpapi

import (
	"net/http"
	"strconv"
	"time"

	catalogtransport "bansoko/internal/catalog/transport"
	providerdomain "bansoko/internal/provider/domain"
)

func writeUpstreamFailure(w http.ResponseWriter, r *http.Request, status providerdomain.Status, code, message string) bool {
	if status.CheckedAt.IsZero() {
		status.CheckedAt = time.Now().UTC()
	}
	var failure *APIError
	switch status.State {
	case providerdomain.StateRateLimited, providerdomain.StateCooldown:
		if status.State == providerdomain.StateRateLimited {
			code, message = "PROVIDER_RATE_LIMITED", "The upstream provider is rate limiting requests."
		} else {
			code, message = "PROVIDER_COOLDOWN", "Requests are waiting for upstream quota to become available."
		}
		seconds, _ := strconv.Atoi(retryAfterSeconds(status.RetryAfter))
		status.RetryAfter = time.Duration(seconds) * time.Second
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		failure = &APIError{Status: http.StatusTooManyRequests, Code: code, Message: message}
	case providerdomain.StateError:
		failure = unavailable(code, message)
		if status.RetryAfter > 0 {
			w.Header().Set("Retry-After", retryAfterSeconds(status.RetryAfter))
		}
	default:
		return false
	}
	failure.Upstream = catalogtransport.UpstreamStatus(status)
	writeError(w, r, failure)
	return true
}
