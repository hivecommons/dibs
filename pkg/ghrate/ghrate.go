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

// MaxBackoff caps every back-off Deadline returns, whichever header it came
// from, so one bad or hostile limited response cannot halt the pollers until
// process restart (#550). GitHub's primary window is one hour; the slack
// absorbs clock skew between this host and GitHub.
const MaxBackoff = 6 * time.Hour

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
	until := Deadline(resp, now)
	l.mu.Lock()
	if until.After(l.notBefore) {
		l.notBefore = until
	}
	l.mu.Unlock()
}

// Deadline returns when a rate-limited resp lets requests resume. Retry-After
// wins (GitHub sends it with secondary limits alongside the primary
// X-RateLimit-Reset); the reset time applies only when the primary quota is
// exhausted. Otherwise it falls back to now+Cooldown. The result never lies
// more than MaxBackoff after now.
func Deadline(resp *http.Response, now time.Time) time.Time {
	until, limit := resumeAt(resp, now), now.Add(MaxBackoff)
	if until.After(limit) {
		return limit
	}
	return until
}

// resumeAt is Deadline before the MaxBackoff ceiling is applied.
func resumeAt(resp *http.Response, now time.Time) time.Time {
	ra := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if secs, err := strconv.ParseInt(ra, 10, 64); err == nil && secs >= 0 {
		// Cap before multiplying so the Duration cannot wrap negative.
		secs = min(secs, int64(MaxBackoff/time.Second))
		return now.Add(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(ra); err == nil {
		return t
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			return time.Unix(reset, 0)
		}
	}
	return now.Add(Cooldown)
}

// IsLimited reports whether resp is a GitHub primary or secondary rate limit.
func IsLimited(resp *http.Response) bool {
	return resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden &&
			(resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"))
}
