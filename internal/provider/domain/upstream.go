package domain

import (
	"errors"
	"net/http"
	"time"

	"bansoko/internal/provider/outbound"
)

// State describes the outcome of an upstream read, independently of any cached data.
type State string

const (
	StateAvailable   State = "available"
	StateEmpty       State = "empty"
	StateNotIndexed  State = "not_indexed"
	StateRateLimited State = "rate_limited"
	StateCooldown    State = "cooldown"
	StateError       State = "error"
)

// Outcomes is a fixed, zero-inclusive breakdown of classified reads.
type Outcomes struct {
	Available   int `json:"available"`
	Empty       int `json:"empty"`
	NotIndexed  int `json:"not_indexed"`
	RateLimited int `json:"rate_limited"`
	Cooldown    int `json:"cooldown"`
	Error       int `json:"error"`
}

func (o *Outcomes) Count(state State, n int) {
	switch state {
	case StateAvailable:
		o.Available += n
	case StateEmpty:
		o.Empty += n
	case StateNotIndexed:
		o.NotIndexed += n
	case StateRateLimited:
		o.RateLimited += n
	case StateCooldown:
		o.Cooldown += n
	case StateError:
		o.Error += n
	}
}

func (s State) Deferred() bool {
	return s == StateCooldown || s == StateRateLimited
}

// ReasonCode is the stable machine code that names why a read ended in its
// state. It is bounded, has no upstream text, and is what diagnostics group by.
type ReasonCode string

const (
	ReasonAnimeNotFound         ReasonCode = "ANIME_NOT_FOUND"
	ReasonUpstreamStatus        ReasonCode = "PROVIDER_UPSTREAM_STATUS"
	ReasonRateLimited           ReasonCode = "PROVIDER_RATE_LIMITED"
	ReasonRetryAfter            ReasonCode = "PROVIDER_RETRY_AFTER"
	ReasonBucketCooldown        ReasonCode = "PROVIDER_BUCKET_COOLDOWN"
	ReasonWaitBudget            ReasonCode = "PROVIDER_WAIT_BUDGET"
	ReasonObservationIncomplete ReasonCode = "PROVIDER_OBSERVATION_INCOMPLETE"
	ReasonGroupReadFailed       ReasonCode = "PROVIDER_GROUP_READ_FAILED"
)

// Status holds the upstream outcome and any retry timing observed at CheckedAt.
type Status struct {
	State         State
	ReasonCode    ReasonCode
	RetryAfter    time.Duration
	CooldownUntil time.Time
	CheckedAt     time.Time
}

// Authoritative reports whether this status is a complete provider answer that
// may replace what a previous observation published.
func (s Status) Authoritative() bool {
	return s.State == StateAvailable || s.State == StateEmpty
}

// RetryAfterAt derives the remaining wait from the durable deadline.
func (s Status) RetryAfterAt(now time.Time) time.Duration {
	if s.CooldownUntil.IsZero() {
		return 0
	}
	return max(0, s.CooldownUntil.Sub(now))
}

// ReadFailure carries a classified outcome without hiding the original error.
type ReadFailure struct {
	Status Status
	Err    error
}

func (e *ReadFailure) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Status.ReasonCode)
}

func (e *ReadFailure) Unwrap() error { return e.Err }

// HTTPReadFailure classifies a non-success response from an uncoordinated read.
func HTTPReadFailure(response *http.Response, err error, checkedAt time.Time) *ReadFailure {
	status := Status{State: StateError, ReasonCode: ReasonUpstreamStatus, CheckedAt: checkedAt}
	if response.StatusCode == http.StatusTooManyRequests {
		status.State, status.ReasonCode = StateRateLimited, ReasonRateLimited
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable {
		if wait, ok := outbound.ParseRetryAfter(response.Header.Get("Retry-After"), checkedAt); ok {
			status.RetryAfter, status.CooldownUntil = wait, checkedAt.Add(wait)
		}
	}
	return &ReadFailure{Status: status, Err: err}
}

// GroupReadStatus requires completeness before treating an empty result as success.
func GroupReadStatus(result GroupReadResult) Status {
	status := Status{State: StateEmpty, CheckedAt: result.ObservedAt}
	if !result.Complete || result.CompletenessEvidence == "" {
		status.State, status.ReasonCode = StateError, ReasonObservationIncomplete
	} else if len(result.Groups) > 0 {
		status.State = StateAvailable
	}
	return status
}

func ClassifyReadError(err error, checkedAt time.Time) Status {
	var failure *ReadFailure
	if errors.As(err, &failure) {
		status := failure.Status
		if status.CheckedAt.IsZero() {
			status.CheckedAt = checkedAt
		}
		return status
	}
	var throttled *outbound.ThrottleError
	if errors.As(err, &throttled) {
		state, reason := StateError, ReasonRetryAfter
		if throttled.StatusCode == 429 {
			state, reason = StateRateLimited, ReasonRateLimited
		}
		return Status{State: state, ReasonCode: reason, CheckedAt: throttled.CheckedAt,
			CooldownUntil: throttled.CooldownUntil, RetryAfter: throttled.RetryAfter}
	}
	var cooling *outbound.CooldownError
	if errors.As(err, &cooling) {
		return Status{State: StateCooldown, ReasonCode: ReasonCode(cooling.ReasonCode), CheckedAt: cooling.CheckedAt,
			CooldownUntil: cooling.CooldownUntil, RetryAfter: cooling.RetryAfter}
	}
	return Status{State: StateError, ReasonCode: ReasonGroupReadFailed, CheckedAt: checkedAt}
}
