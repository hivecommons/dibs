package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/news"
	"github.com/hivecommons/dibs/pkg/registry"
)

func TestHandleRepoNews(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatalf("registry New: %v", err)
	}
	if err := reg.Sync(context.Background(), &registry.FakeHub{Repos: []registry.RepoProfile{{RepoID: "org/repo"}}}); err != nil {
		t.Fatalf("registry Sync: %v", err)
	}
	ns, err := news.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("news NewStore: %v", err)
	}
	ff := &apiNewsFetcher{prs: []history.MergedPullRequest{{
		Title: "ship news feed", MergedAt: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC),
	}}}
	g := &news.Generator{Store: ns, Fetcher: ff, Now: func() time.Time {
		return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	}}
	if err := g.Refresh(context.Background(), "org/repo"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	api := &API{Registry: reg, News: ns}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/repos/{org}/{repo}/news", api.HandleRepoNews)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/repos/org/repo/news", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var items []news.Item
	if err := json.NewDecoder(rec.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 1 || items[0].Date != "2026-08-19" || items[0].PRCount != 1 || items[0].Source != "digest" {
		t.Fatalf("items = %+v", items)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/repos/ORG/Repo/news", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("mixed-case status = %d body=%s", rec.Code, rec.Body.String())
	}
	items = nil
	if err := json.NewDecoder(rec.Body).Decode(&items); err != nil {
		t.Fatalf("decode mixed-case: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("mixed-case items = %+v, want the registered repo's news", items)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/repos/org/missing/news", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", rec.Code)
	}
}

type apiNewsFetcher struct {
	prs []history.MergedPullRequest
}

func (f *apiNewsFetcher) FetchMergedPullRequests(context.Context, string) ([]history.MergedPullRequest, error) {
	return append([]history.MergedPullRequest(nil), f.prs...), nil
}

// TestHandleRepoNewsDegradedDependencies pins the fallbacks a deployment
// without a news store (or without a registry) relies on: the handler must
// answer 200 with an empty JSON array — never null, never 500 — so the
// repo page's news column renders empty instead of breaking.
func TestHandleRepoNewsDegradedDependencies(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatalf("registry New: %v", err)
	}
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/repo"}}); err != nil {
		t.Fatalf("registry Merge: %v", err)
	}
	ns, err := news.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("news NewStore: %v", err)
	}

	cases := map[string]*API{
		"nil news store":          {Registry: reg},
		"nil registry":            {News: ns},
		"known repo without news": {Registry: reg, News: ns},
	}
	for name, api := range cases {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/repos/{org}/{repo}/news", api.HandleRepoNews)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/repos/org/repo/news", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
				t.Fatalf("body = %q, want empty JSON array", got)
			}
		})
	}
}

// TestHandleRepoNewsRegistryFailure: a registry error that is not
// ErrNotFound must surface as 500, not be mistaken for a missing repo.
func TestHandleRepoNewsRegistryFailure(t *testing.T) {
	api := &API{Registry: &getFailRegistry{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/repos/{org}/{repo}/news", api.HandleRepoNews)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/repos/org/repo/news", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rec.Code, rec.Body.String())
	}
}

// getFailRegistry fails every lookup with a non-ErrNotFound error.
type getFailRegistry struct{ RepoRegistry }

func (getFailRegistry) Get(string) (*registry.RepoProfile, error) { return nil, errBoom }
