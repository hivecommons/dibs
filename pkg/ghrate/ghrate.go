// Package ghrate remembers GitHub rate-limit resets so pollers stop
// requesting until the limit lifts (#487, mirroring registry's #480).
package ghrate

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cooldown is the back-off used when a limited response carries no parseable
// X-RateLimit-Reset or Retry-After header.
const Cooldown = 15 * time.Minute

// Limiter holds the time before which GitHub must not be polled. The zero
// value is ready to use.
type Limiter struct {
	// Now overrides the clock; nil means time.Now.
	Now func() time.Time

	mu        sync.Mutex
	notBefore time.Time
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Wait reports whether a previously observed rate limit is still in effect.
func (l *Limiter) Wait() bool { return l.WaitAt(l.now()) }

// WaitAt is Wait against an explicit clock reading.
func (l *Limiter) WaitAt(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return now.Before(l.notBefore)
}

// Observe records a back-off when resp is a rate-limit response.
func (l *Limiter) Observe(resp *http.Response) { l.ObserveAt(resp, l.now()) }

// ObserveAt is Observe against an explicit clock reading.
func (l *Limiter) ObserveAt(resp *http.Response, now time.Time) {
	if resp == nil || !IsLimited(resp) {
		return
	}
	until := now.Add(Cooldown)
	if reset, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
		until = time.Unix(reset, 0)
	} else if secs, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("Retry-After")), 10, 64); err == nil && secs >= 0 {
		until = now.Add(time.Duration(secs) * time.Second)
	}
	l.mu.Lock()
	if until.After(l.notBefore) {
		l.notBefore = until
	}
	l.mu.Unlock()
}

// IsLimited reports whether resp is a GitHub primary or secondary rate limit.
func IsLimited(resp *http.Response) bool {
	return resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden &&
			(resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"))
}
