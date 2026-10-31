package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshRateLimitKeepsPriorEnrichmentAndStaleStamps(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cachedAt := now.Add(-3 * 24 * time.Hour)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/cncf/alpha":
			_ = json.NewEncoder(w).Encode(map[string]any{"description": "fresh alpha", "language": "Go"})
		case "/repos/cncf/alpha/readme":
			_, _ = w.Write([]byte("Alpha readme.\n"))
		default:
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer gh.Close()

	s := newRefreshStore(t, gh, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rateLimitLandscapeYAML))
	})
	s.Now = func() time.Time { return now }
	s.setProjects([]Project{{
		Name: "beta", RepoID: "cncf/beta", Description: "cached beta",
		Topics: []string{"cached"}, Language: "Rust", Readme: "cached readme", FetchedAt: cachedAt,
	}})

	if err := s.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got := map[string]Project{}
	for _, p := range s.List() {
		got[p.RepoID] = p
	}
	if a := got["cncf/alpha"]; a.Readme == "" || !a.FetchedAt.Equal(now) {
		t.Fatalf("alpha = %+v, want enriched and stamped now", a)
	}
	b := got["cncf/beta"]
	if b.Readme != "cached readme" || b.Language != "Rust" || len(b.Topics) != 1 || b.Description != "cached beta" {
		t.Fatalf("beta lost prior enrichment: %+v", b)
	}
	if !b.FetchedAt.Equal(cachedAt) {
		t.Fatalf("beta FetchedAt = %v, want cached %v", b.FetchedAt, cachedAt)
	}
	g := got["cncf/gamma"]
	if !g.FetchedAt.IsZero() || g.Readme != "" {
		t.Fatalf("gamma (never enriched) = %+v, want zero FetchedAt and empty readme", g)
	}
	if !s.needsRefresh() {
		t.Fatal("needsRefresh() = false with unenriched projects, want true")
	}
}

func TestNeedsRefreshAnyZeroFetchedAt(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	s, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Now = func() time.Time { return now }
	s.setProjects([]Project{
		{RepoID: "a/a", FetchedAt: now.Add(-time.Hour)},
		{RepoID: "b/b"},
	})
	if !s.needsRefresh() {
		t.Fatal("needsRefresh() = false with a zero FetchedAt project, want true")
	}
}

func TestRefreshReadmeRateLimitKeepsCachedReadmeAndStamp(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cachedAt := now.Add(-3 * 24 * time.Hour)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/cncf/beta":
			_ = json.NewEncoder(w).Encode(map[string]any{"description": "fresh beta", "language": "Go"})
		default:
			// The readme call is the one that trips the limiter.
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer gh.Close()

	s := newRefreshStore(t, gh, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rateLimitLandscapeYAML))
	})
	s.Now = func() time.Time { return now }
	s.setProjects([]Project{{
		Name: "beta", RepoID: "cncf/beta", Description: "cached beta",
		Topics: []string{"cached"}, Language: "Rust", Readme: "cached readme", FetchedAt: cachedAt,
	}})

	if err := s.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var b Project
	for _, p := range s.List() {
		if p.RepoID == "cncf/beta" {
			b = p
		}
	}
	if b.Readme != "cached readme" {
		t.Fatalf("beta readme = %q, want cached readme kept after readme rate limit", b.Readme)
	}
	if !b.FetchedAt.Equal(cachedAt) {
		t.Fatalf("beta FetchedAt = %v, want stale %v so it is retried", b.FetchedAt, cachedAt)
	}
	if !s.limiter.WaitAt(now) {
		t.Fatal("limiter not tripped by readme rate limit")
	}
}

func TestRefreshAsyncSkipsWhileRateLimited(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer gh.Close()

	calls := make(chan string, 8)
	s := newRefreshStore(t, gh, func(w http.ResponseWriter, r *http.Request) {
		calls <- r.URL.Path
		_, _ = w.Write([]byte(rateLimitLandscapeYAML))
	})
	s.Now = func() time.Time { return now }
	refreshed := make(chan error, 1)
	s.OnRefresh = func(err error) { refreshed <- err }

	// First pass downloads the landscape and trips the limiter.
	s.RefreshAsync()
	select {
	case err := <-refreshed:
		if err != nil {
			t.Fatalf("OnRefresh err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first RefreshAsync never completed")
	}
	<-calls
	if !s.limiter.WaitAt(now) {
		t.Fatal("limiter not tripped by first pass")
	}
	if !s.needsRefresh() {
		t.Fatal("needsRefresh() = false with unenriched projects, want true")
	}

	// Subsequent ticks while rate-limited must not refetch or rewrite.
	s.RefreshAsync()
	select {
	case p := <-calls:
		t.Fatalf("RefreshAsync fetched %s while rate-limited", p)
	case err := <-refreshed:
		t.Fatalf("OnRefresh(%v) called while rate-limited", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Once the back-off expires, the retry resumes.
	s.Now = func() time.Time { return now.Add(24 * time.Hour) }
	s.RefreshAsync()
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshAsync did not resume after the rate limit expired")
	}
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("resumed refresh never completed")
	}
}
