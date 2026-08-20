package history

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
}

func TestFetchMergedPRsBucketsByMergedDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/pulls" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"merged_at": "2026-08-18T23:30:00Z"},
			{"merged_at": "2026-08-18T01:00:00Z"},
			{"merged_at": "2026-08-17T09:00:00Z"},
			{"merged_at": nil},
		})
	}))
	defer srv.Close()

	b := &Backfiller{BaseURL: srv.URL, Token: "test-token", Client: srv.Client(), Now: fixedNow}
	got, err := b.fetchMergedPRBuckets(context.Background(), "org/repo")
	if err != nil {
		t.Fatalf("fetchMergedPRBuckets: %v", err)
	}
	want := map[string]int{"2026-08-18": 2, "2026-08-17": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buckets = %v, want %v", got, want)
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	var pullsCalls, statsCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/pulls":
			pullsCalls++
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"merged_at": "2026-08-18T23:30:00Z"},
			})
		case "/repos/org/repo/stats/commit_activity":
			statsCalls++
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"week": time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC).Unix(),
				"days": []int{0, 2, 0, 1, 0, 0, 0},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	b := &Backfiller{Store: st, BaseURL: srv.URL, Client: srv.Client(), Now: fixedNow}
	if err := b.Backfill(context.Background(), "org/repo"); err != nil {
		t.Fatalf("first Backfill: %v", err)
	}
	first, ok := st.Get("org/repo")
	if !ok {
		t.Fatalf("missing history after first backfill")
	}
	if err := b.Backfill(context.Background(), "org/repo"); err != nil {
		t.Fatalf("second Backfill: %v", err)
	}
	second, ok := st.Get("org/repo")
	if !ok {
		t.Fatalf("missing history after second backfill")
	}
	if !reflect.DeepEqual(first.Days, second.Days) {
		t.Fatalf("backfill not idempotent:\nfirst=%+v\nsecond=%+v", first.Days, second.Days)
	}
	if pullsCalls != 2 || statsCalls != 2 {
		t.Fatalf("calls pulls=%d stats=%d, want 2 each", pullsCalls, statsCalls)
	}
}

func TestCommitActivityAcceptedSkipsCommitsOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/pulls":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"merged_at": "2026-08-18T23:30:00Z"},
			})
		case "/repos/org/repo/stats/commit_activity":
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	b := &Backfiller{Store: st, BaseURL: srv.URL, Client: srv.Client(), Now: fixedNow}
	if err := b.Backfill(context.Background(), "org/repo"); err != nil {
		t.Fatalf("Backfill with 202 stats: %v", err)
	}
	h, ok := st.Get("org/repo")
	if !ok {
		t.Fatalf("missing history")
	}
	var found DayActivity
	for _, d := range h.Days {
		if d.Date == "2026-08-18" {
			found = d
		}
	}
	if found.MergedPRs != 1 || found.Commits != 0 {
		t.Fatalf("202 stats should keep PRs and skip commits, got %+v", found)
	}
}

func TestBackfillRateLimitDoesNotPersistPartialPulls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	b := &Backfiller{Store: st, BaseURL: srv.URL, Client: srv.Client(), Now: fixedNow}
	if err := b.Backfill(context.Background(), "org/repo"); err == nil {
		t.Fatalf("Backfill rate limit error = nil, want error")
	}
	if _, ok := st.Get("org/repo"); ok {
		t.Fatalf("rate-limited pulls should not persist partial history")
	}
}

func TestBackfillRateLimitDoesNotPersistPartialCommits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/pulls":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"merged_at": "2026-08-18T23:30:00Z"},
			})
		case "/repos/org/repo/stats/commit_activity":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	b := &Backfiller{Store: st, BaseURL: srv.URL, Client: srv.Client(), Now: fixedNow}
	if err := b.Backfill(context.Background(), "org/repo"); err == nil {
		t.Fatalf("Backfill commit rate limit error = nil, want error")
	}
	if _, ok := st.Get("org/repo"); ok {
		t.Fatalf("rate-limited commits should not persist partial history")
	}
}
