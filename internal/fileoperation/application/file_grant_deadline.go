package application

import (
	"fmt"
	"time"
)

// A DB deadline is translated using a monotonic sample taken before the entire
// gate call. Subtracting that call's full duration conservatively includes lock,
// commit and response time; it never grants a fresh timeout upon receipt.
func publicationGrantDeadline(callStarted, gateDBNow, attemptUntil, received time.Time) (time.Time, error) {
	elapsed := received.Sub(callStarted)
	remaining := attemptUntil.Sub(gateDBNow) - elapsed
	if callStarted.IsZero() || gateDBNow.IsZero() || attemptUntil.IsZero() || received.IsZero() || elapsed < 0 || remaining <= 0 || attemptUntil.Sub(gateDBNow) > 24*time.Hour {
		return time.Time{}, fmt.Errorf("FILE_GRANT_DEADLINE_UNAVAILABLE")
	}
	return received.Add(remaining), nil
}
