package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Basic sign-in brute-force protection. The window starts at the first
// failure for a key and is fixed: five failures for one client IP + username,
// or twenty for one client IP (so rotating usernames does not bypass it),
// block that key for the rest of the window. A successful sign-in clears its
// IP + username key but not the shared IP counter.
//
// The state is in memory and intentionally not persisted: a restart is an
// owner action, and the self-use bar does not need a distributed limiter. The
// client IP is the transport peer address; X-Forwarded-For is never trusted
// (a caller-supplied header would let an attacker choose a fresh bucket).
const (
	loginLimiterWindow           = 15 * time.Minute
	loginLimiterIdentityFailures = 5
	loginLimiterIPFailures       = 20
	loginLimiterMaxEntries       = 4096
)

type loginLimitEntry struct {
	failures  int
	startedAt time.Time
}

type loginLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]loginLimitEntry
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginLimiter{now: now, entries: make(map[string]loginLimitEntry)}
}

// allow reports whether the request may proceed and, when blocked, how long
// the caller should wait before retrying.
func (l *loginLimiter) allow(ip, username string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var blockedUntil time.Time
	for _, spec := range []struct {
		key   string
		limit int
	}{
		{key: loginIPKey(ip), limit: loginLimiterIPFailures},
		{key: loginIdentityKey(ip, username), limit: loginLimiterIdentityFailures},
	} {
		entry, ok := l.entries[spec.key]
		if !ok {
			continue
		}
		until := entry.startedAt.Add(loginLimiterWindow)
		if !now.Before(until) {
			delete(l.entries, spec.key)
			continue
		}
		if entry.failures >= spec.limit && until.After(blockedUntil) {
			blockedUntil = until
		}
	}
	if blockedUntil.IsZero() {
		return 0, false
	}
	return blockedUntil.Sub(now), true
}

func (l *loginLimiter) recordFailure(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.bump(loginIPKey(ip), now)
	l.bump(loginIdentityKey(ip, username), now)
	l.sweep(now)
}

func (l *loginLimiter) recordSuccess(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, loginIdentityKey(ip, username))
}

func (l *loginLimiter) bump(key string, now time.Time) {
	entry, ok := l.entries[key]
	if !ok || !now.Before(entry.startedAt.Add(loginLimiterWindow)) {
		entry = loginLimitEntry{startedAt: now}
	}
	entry.failures++
	l.entries[key] = entry
}

// sweep only runs at the hard cap, so ordinary traffic never pays for a full
// map scan. After expired keys are gone, the oldest window is evicted until
// the map fits; an attacker rotating source addresses cannot grow it without
// bound.
func (l *loginLimiter) sweep(now time.Time) {
	if len(l.entries) < loginLimiterMaxEntries {
		return
	}
	for key, entry := range l.entries {
		if !now.Before(entry.startedAt.Add(loginLimiterWindow)) {
			delete(l.entries, key)
		}
	}
	for len(l.entries) >= loginLimiterMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range l.entries {
			if oldestKey == "" || entry.startedAt.Before(oldest) {
				oldestKey = key
				oldest = entry.startedAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(l.entries, oldestKey)
	}
}

func loginIPKey(ip string) string {
	return "ip:" + ip
}

func loginIdentityKey(ip, username string) string {
	return "id:" + ip + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
