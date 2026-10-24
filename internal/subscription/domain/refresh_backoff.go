package domain

import "time"

const (
	// RefreshBackoffBase is the delay after the first consecutive failure of
	// one RSS source; each further failure doubles it.
	RefreshBackoffBase = 5 * time.Minute
	// RefreshBackoffCap bounds the per-source delay.
	RefreshBackoffCap = 2 * time.Hour
	// ProviderCircuitThreshold consecutive refresh failures across a
	// provider's provider-group sources pause its automatic dispatch.
	ProviderCircuitThreshold = 5
	// ProviderCircuitCooldown is how long a tripped provider stays paused.
	ProviderCircuitCooldown = time.Hour
)

// RefreshBackoff returns the delay before the next automatic refresh of a
// source with consecutiveFailures failures in a row: 5, 10, 20, 40 minutes,
// then capped at 2 hours. Zero failures means no delay.
func RefreshBackoff(consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 0 {
		return 0
	}
	delay := RefreshBackoffBase
	for i := 1; i < consecutiveFailures; i++ {
		delay *= 2
		if delay >= RefreshBackoffCap {
			return RefreshBackoffCap
		}
	}
	return delay
}
