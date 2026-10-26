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
		{"retry-after", resp(403, "Retry-After", "90"), 90 * time.Second},
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

func TestObserveNil(t *testing.T) {
	var l Limiter
	l.Observe(nil)
	if l.Wait() {
		t.Fatal("nil response must not back off")
	}
}
