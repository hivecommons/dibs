package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
