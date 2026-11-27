package outbound

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

const (
	ReasonBucketCooldown = "PROVIDER_BUCKET_COOLDOWN"
	ReasonWaitBudget     = "PROVIDER_WAIT_BUDGET"
	ReasonRateLimited    = "PROVIDER_RATE_LIMITED"
	ReasonUnavailable    = "PROVIDER_RETRY_AFTER"
	maxBackoff           = 5 * time.Minute
)

type CooldownError struct {
	Bucket        BucketKey
	ReasonCode    string
	RetryAfter    time.Duration
	CooldownUntil time.Time
	CheckedAt     time.Time
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("provider bucket %s unavailable until %s (%s)", e.Bucket, e.CooldownUntil.Format(time.RFC3339Nano), e.ReasonCode)
}
func newCooldown(key BucketKey, reason string, now, until time.Time) *CooldownError {
	return &CooldownError{Bucket: key, ReasonCode: reason, CheckedAt: now, CooldownUntil: until, RetryAfter: max(0, until.Sub(now))}
}

// ThrottleError is evidence from a response that actually reached the upstream.
// An HTTP 503 with Retry-After triggers the same cooldown but retains its status.
type ThrottleError struct {
	CooldownError
	StatusCode int
}

func (e *ThrottleError) Error() string {
	return fmt.Sprintf("provider bucket %s returned HTTP %d", e.Bucket, e.StatusCode)
}

func (s *Scheduler) ObserveThrottle(ctx context.Context, key BucketKey, status int, header string) error {
	if !isThrottle(status, header) {
		return nil
	}
	s.mu.Lock()
	b := s.buckets[key]
	if b == nil {
		s.mu.Unlock()
		return fmt.Errorf("unknown outbound bucket %q", key)
	}
	now := s.clock.Now()
	consecutive := 1
	if !b.health.LastLimitedAt.IsZero() && now.Sub(later(b.health.LastLimitedAt, b.health.CooldownUntil)) <= maxBackoff {
		consecutive = min(b.health.Consecutive+1, 7)
	}
	delay, valid := ParseRetryAfter(header, now)
	if !valid {
		delay = min(5*time.Second<<(consecutive-1), maxBackoff)
	}
	reason := ReasonRateLimited
	if status == http.StatusServiceUnavailable {
		reason = ReasonUnavailable
	}
	health := Health{
		Bucket: key, CooldownUntil: later(b.health.CooldownUntil, now.Add(delay)), LastLimitedAt: now,
		ReasonCode: reason, Summary: fmt.Sprintf("HTTP %d", status), Consecutive: consecutive,
		Version: b.health.Version + 1, UpdatedAt: now,
	}
	b.health = health
	b.signal()
	logger := s.logger
	s.mu.Unlock()
	if logger != nil {
		logger.Warn(ctx, observabilityport.RuntimeEvent{
			Component:  "provider_outbound",
			Operation:  "observe_throttle",
			Outcome:    "throttled",
			HTTPStatus: status,
			ReasonCode: reason,
			Kind:       string(key),
			Attempt:    consecutive,
			DurationMS: max(0, health.CooldownUntil.Sub(now)).Milliseconds(),
			Message:    "provider outbound bucket entered cooldown",
		})
	}
	failure := &ThrottleError{CooldownError: *newCooldown(key, reason, now, health.CooldownUntil), StatusCode: status}
	if s.store != nil {
		// A response already proved the cooldown. Persist it even if the caller
		// disconnects, within a bounded deadline and without starting a goroutine.
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := s.store.Save(persist, health); err != nil {
			return errors.Join(failure, fmt.Errorf("save provider outbound health: %w", err))
		}
	}
	return failure
}

// ParseRetryAfter handles both forms of the standard Retry-After header.
func ParseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	value := strings.TrimSpace(header)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil && seconds <= uint64((1<<63-1)/time.Second) {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now), true
	}
	return 0, false
}

// AniBTOriginHost is the public origin covered by the shared RSS limit.
const AniBTOriginHost = "anibt.net"

func isThrottle(status int, header string) bool {
	return status == http.StatusTooManyRequests || (status == http.StatusServiceUnavailable && strings.TrimSpace(header) != "")
}

// Do applies a fixed policy to each request sent by the client, including
// redirects. It preserves the client's timeout, redirect policy and routing.
func Do(client *http.Client, coordinator Coordinator, bucket BucketKey, request *http.Request) (*http.Response, error) {
	if bucket == "" {
		return client.Do(request)
	}
	return do(client, coordinator, func(*http.Request) BucketKey { return bucket }, request)
}

// DoRSS applies the public RSS policy to each hop into the AniBT origin.
func DoRSS(client *http.Client, coordinator Coordinator, request *http.Request) (*http.Response, error) {
	return do(client, coordinator, func(r *http.Request) BucketKey {
		if strings.EqualFold(strings.TrimSuffix(r.URL.Hostname(), "."), AniBTOriginHost) {
			return AniBTRSS
		}
		return ""
	}, request)
}

func do(client *http.Client, coordinator Coordinator, bucket func(*http.Request) BucketKey, request *http.Request) (*http.Response, error) {
	if coordinator == nil {
		return client.Do(request)
	}
	copy := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = scheduledTransport{base: base, coordinator: coordinator, bucket: bucket}
	return copy.Do(request)
}

type scheduledTransport struct {
	base        http.RoundTripper
	coordinator Coordinator
	bucket      func(*http.Request) BucketKey
}

func (t scheduledTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	bucket := t.bucket(request)
	if bucket == "" {
		return t.base.RoundTrip(request)
	}
	if err := t.coordinator.Acquire(ctx, bucket); err != nil {
		return nil, err
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return response, err
	}
	header := response.Header.Get("Retry-After")
	if !isThrottle(response.StatusCode, header) {
		return response, nil
	}
	if err := t.coordinator.ObserveThrottle(ctx, bucket, response.StatusCode, header); err != nil {
		if response.Body != nil {
			response.Body.Close()
		}
		return nil, err
	}
	return response, nil
}
