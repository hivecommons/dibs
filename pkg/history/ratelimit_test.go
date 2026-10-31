package history

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/registry"
)

func TestRateLimitResetStopsFurtherRequests(t *testing.T) {
	var reqs atomic.Int32
	reset := fixedNow().Add(time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := fixedNow()
	b := &Backfiller{Store: st, BaseURL: srv.URL, Client: srv.Client(), Now: func() time.Time { return now }, Logf: t.Logf}

	if err := b.Backfill(context.Background(), "org/repo"); !errors.Is(err, errSkipRepo) {
		t.Fatalf("first Backfill error = %v, want errSkipRepo", err)
	}
	first := reqs.Load()
	if first == 0 {
		t.Fatal("first Backfill issued no request")
	}

	if err := b.Backfill(context.Background(), "org/repo"); !errors.Is(err, errSkipRepo) {
		t.Fatalf("second Backfill error = %v, want errSkipRepo", err)
	}
	b.RefreshAsync([]registry.RepoProfile{{RepoID: "org/repo"}})
	if _, err := b.FetchMergedPullRequests(context.Background(), "org/repo"); !errors.Is(err, errSkipRepo) {
		t.Fatalf("FetchMergedPullRequests error = %v, want errSkipRepo", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := reqs.Load(); got != first {
		t.Fatalf("requests before reset = %d, want %d", got, first)
	}

	now = reset.Add(time.Second)
	if b.RateLimited() {
		t.Fatal("still rate limited after reset")
	}
	_ = b.Backfill(context.Background(), "org/repo")
	if got := reqs.Load(); got <= first {
		t.Fatalf("no request after reset: %d", got)
	}
}

// A nil *Backfiller stored in an interface passes callers' `!= nil` checks
// (news.Generator.rateLimited asserts to rateLimitedFetcher), so the nil
// receiver must report "not limited" instead of dereferencing b.limiter.
func TestRateLimitedNilReceiverReportsNotLimited(t *testing.T) {
	var nilB *Backfiller
	if nilB.RateLimited() {
		t.Fatal("nil receiver: RateLimited() = true, want false")
	}
	var fetcher interface{ RateLimited() bool } = nilB
	if fetcher.RateLimited() {
		t.Fatal("nil receiver via interface: RateLimited() = true, want false")
	}
	b := &Backfiller{}
	if b.RateLimited() {
		t.Fatal("zero-value limiter: RateLimited() = true, want false")
	}
}
