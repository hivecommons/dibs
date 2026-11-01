package ghrate

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func resp(status int, kv ...string) *http.Response {
	r := &http.Response{StatusCode: status, Header: http.Header{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Header.Set(kv[i], kv[i+1])
	}
	return r
}

func TestLimiter(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name string
		resp *http.Response
		want time.Duration // zero means no back-off
	}{
		{"reset header", resp(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(base.Add(time.Hour).Unix(), 10)), time.Hour},
		{"retry-after beats reset", resp(403, "Retry-After", "60", "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(base.Add(time.Hour).Unix(), 10)), time.Minute},
		{"secondary limit ignores reset", resp(403, "Retry-After", "60", "X-RateLimit-Remaining", "4000", "X-RateLimit-Reset", strconv.FormatInt(base.Add(time.Hour).Unix(), 10)), time.Minute},
		{"reset without exhausted quota falls back", resp(429, "X-RateLimit-Remaining", "5", "X-RateLimit-Reset", strconv.FormatInt(base.Add(time.Hour).Unix(), 10)), Cooldown},
		{"retry-after", resp(403, "Retry-After", "90"), 90 * time.Second},
		{"retry-after http-date", resp(429, "Retry-After", base.Add(2*time.Hour).UTC().Format(http.TimeFormat)), 2 * time.Hour},
		{"retry-after http-date beats reset", resp(403, "Retry-After", base.Add(2*time.Hour).UTC().Format(http.TimeFormat), "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(base.Add(time.Hour).Unix(), 10)), 2 * time.Hour},
		{"retry-after http-date in the past", resp(429, "Retry-After", base.Add(-time.Minute).UTC().Format(http.TimeFormat)), 0},
		{"negative retry-after falls back", resp(429, "Retry-After", "-5"), Cooldown},
		{"garbage retry-after falls back", resp(429, "Retry-After", "later"), Cooldown},
		{"garbage retry-after defers to exhausted reset", resp(403, "Retry-After", "later", "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(base.Add(time.Hour).Unix(), 10)), time.Hour},
		{"bare 429 falls back", resp(429), Cooldown},
		{"unparseable falls back", resp(429, "X-RateLimit-Reset", "soon"), Cooldown},
		{"plain 403 ignored", resp(403), 0},
		{"200 ignored", resp(200, "X-RateLimit-Remaining", "0"), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := base
			l := &Limiter{Now: func() time.Time { return now }}
			if l.Wait() {
				t.Fatal("fresh limiter must not wait")
			}
			l.Observe(tc.resp)
			if got := l.Wait(); got != (tc.want > 0) {
				t.Fatalf("Wait() = %v, want %v", got, tc.want > 0)
			}
			if tc.want == 0 {
				return
			}
			now = base.Add(tc.want - time.Second)
			if !l.Wait() {
				t.Fatal("must still wait before reset")
			}
			now = base.Add(tc.want)
			if l.Wait() {
				t.Fatal("must stop waiting at reset")
			}
		})
	}
}

func TestHugeRetryAfterStillBacksOff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := &Limiter{Now: func() time.Time { return now }}
	l.Observe(resp(429, "Retry-After", "10000000000"))
	if !l.Wait() {
		t.Fatal("a huge Retry-After must back off, not overflow into the past")
	}
}

func TestObserveNil(t *testing.T) {
	var l Limiter
	l.Observe(nil)
	if l.Wait() {
		t.Fatal("nil response must not back off")
	}
}
