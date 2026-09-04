package news

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/match"
)

type fakeFetcher struct {
	prs   []history.MergedPullRequest
	calls int
}

func (f *fakeFetcher) FetchMergedPullRequests(context.Context, string) ([]history.MergedPullRequest, error) {
	f.calls++
	return append([]history.MergedPullRequest(nil), f.prs...), nil
}

func newsNow() time.Time {
	return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
}

func pr(title, at, author string) history.MergedPullRequest {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	return history.MergedPullRequest{Title: title, MergedAt: t, Author: author}
}

func TestFallbackTLDRFormatting(t *testing.T) {
	got := FallbackTLDR([]history.MergedPullRequest{
		{Title: "add quiet news cards"},
		{Title: "backfill merged pull request titles"},
		{Title: "tighten market chart spacing"},
		{Title: "wire cache invalidation"},
		{Title: "document API shape"},
	})
	if !strings.HasPrefix(got, "Merged add quiet news cards; backfill merged pull request titles") {
		t.Fatalf("fallback prefix = %q", got)
	}
	if !strings.Contains(got, "and 1 more") || !strings.HasSuffix(got, ".") {
		t.Fatalf("fallback should mention extra PRs and end with period: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("fallback must be one line: %q", got)
	}
}

func TestRefreshReusesCacheUntilDayFingerprintChanges(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ff := &fakeFetcher{prs: []history.MergedPullRequest{
		pr("first merge", "2026-08-19T10:00:00Z", "alice"),
	}}
	g := &Generator{Store: st, Fetcher: ff, Now: newsNow}
	if err := g.Refresh(context.Background(), "org/repo"); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	first := st.Get("org/repo")
	if len(first) != 1 || first[0].TLDR != "Merged first merge." || first[0].Source != "digest" {
		t.Fatalf("first items = %+v", first)
	}

	if err := g.Refresh(context.Background(), "org/repo"); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	second := st.Get("org/repo")
	if len(second) != 1 || second[0].TLDR != first[0].TLDR {
		t.Fatalf("same fingerprint should reuse cache: first=%+v second=%+v", first, second)
	}

	ff.prs = append(ff.prs, pr("second merge", "2026-08-19T11:00:00Z", "bob"))
	if err := g.Refresh(context.Background(), "org/repo"); err != nil {
		t.Fatalf("third Refresh: %v", err)
	}
	third := st.Get("org/repo")
	if len(third) != 1 || third[0].PRCount != 2 || !strings.Contains(third[0].TLDR, "second merge") {
		t.Fatalf("changed fingerprint should regenerate day: %+v", third)
	}
}

func TestRefreshLLMPath(t *testing.T) {
	var userPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for _, msg := range req.Messages {
			if msg.Role == "user" {
				userPrompt = msg.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "Auth and chart fixes shipped; cadence is rising across UI work."}}},
		})
	}))
	defer srv.Close()

	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	g := &Generator{
		Store: st,
		Fetcher: &fakeFetcher{prs: []history.MergedPullRequest{
			pr("fix auth refresh", "2026-08-19T10:00:00Z", "alice"),
			pr("add chart cards", "2026-08-19T11:00:00Z", "bob"),
		}},
		LLM: &match.LLM{BaseURL: srv.URL, Model: "test", Client: srv.Client()},
		Now: newsNow,
	}
	if err := g.Refresh(context.Background(), "org/repo"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	items := st.Get("org/repo")
	if len(items) != 1 || items[0].Source != "llm" || items[0].PRCount != 2 {
		t.Fatalf("items = %+v", items)
	}
	if !strings.Contains(userPrompt, "source PR count: 2") || !strings.Contains(userPrompt, "fix auth refresh") {
		t.Fatalf("prompt missing PR context:\n%s", userPrompt)
	}
}
