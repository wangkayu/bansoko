package httpapi

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginLimiterBlocksIdentityAfterFiveFailures(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < loginLimiterIdentityFailures; i++ {
		limiter.recordFailure("198.51.100.10", "Owner")
	}
	wait, blocked := limiter.allow("198.51.100.10", "owner")
	if !blocked || wait != loginLimiterWindow {
		t.Fatalf("identity must be blocked for the window: wait=%v blocked=%v", wait, blocked)
	}
	if _, blocked := limiter.allow("198.51.100.10", "another-user"); blocked {
		t.Fatal("the per-IP counter is below its limit, so another username must pass")
	}
	if _, blocked := limiter.allow("198.51.100.11", "owner"); blocked {
		t.Fatal("another client IP must not inherit the identity block")
	}

	now = now.Add(time.Minute)
	if wait, blocked := limiter.allow("198.51.100.10", "owner"); !blocked || wait != loginLimiterWindow-time.Minute {
		t.Fatalf("Retry-After must be the remaining window: wait=%v blocked=%v", wait, blocked)
	}
	now = now.Add(loginLimiterWindow - time.Minute)
	if _, blocked := limiter.allow("198.51.100.10", "owner"); blocked {
		t.Fatal("the block must end with the window")
	}
}

func TestLoginLimiterBlocksClientIPAcrossUsernames(t *testing.T) {
	limiter := newLoginLimiter(nil)
	for i := 0; i < loginLimiterIPFailures; i++ {
		limiter.recordFailure("203.0.113.7", fmt.Sprintf("user-%d", i%4))
	}
	if _, blocked := limiter.allow("203.0.113.7", "fresh-name"); !blocked {
		t.Fatal("20 failures from one IP must block every username")
	}
	if _, blocked := limiter.allow("203.0.113.8", "user-0"); blocked {
		t.Fatal("another IP must keep its own counter")
	}
}

func TestLoginLimiterSuccessClearsTheIdentityKeyOnly(t *testing.T) {
	limiter := newLoginLimiter(nil)
	for i := 0; i < loginLimiterIdentityFailures-1; i++ {
		limiter.recordFailure("192.0.2.5", "owner")
	}
	limiter.recordSuccess("192.0.2.5", "owner")
	for i := 0; i < loginLimiterIdentityFailures-1; i++ {
		limiter.recordFailure("192.0.2.5", "owner")
	}
	if _, blocked := limiter.allow("192.0.2.5", "owner"); blocked {
		t.Fatal("a successful sign-in must reset the identity failure count")
	}
	limiter.recordFailure("192.0.2.5", "owner")
	if _, blocked := limiter.allow("192.0.2.5", "owner"); !blocked {
		t.Fatal("the identity must block again after five fresh failures")
	}
}

func TestLoginLimiterStaysBounded(t *testing.T) {
	limiter := newLoginLimiter(nil)
	for i := 0; i < loginLimiterMaxEntries+64; i++ {
		limiter.recordFailure(fmt.Sprintf("10.0.%d.%d", i/256, i%256), "owner")
	}
	if len(limiter.entries) > loginLimiterMaxEntries {
		t.Fatalf("limiter grew to %d entries, cap is %d", len(limiter.entries), loginLimiterMaxEntries)
	}
}

func TestClientIPUsesTheTransportPeer(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	r.RemoteAddr = "198.51.100.20:54321"
	if got := clientIP(r); got != "198.51.100.20" {
		t.Fatalf("clientIP = %q", got)
	}
	r.RemoteAddr = "198.51.100.20"
	if got := clientIP(r); got != "198.51.100.20" {
		t.Fatalf("clientIP without a port = %q", got)
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	if got := clientIP(r); got != "198.51.100.20" {
		t.Fatalf("clientIP followed X-Forwarded-For: %q", got)
	}
}
