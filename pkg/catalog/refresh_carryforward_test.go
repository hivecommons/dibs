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
