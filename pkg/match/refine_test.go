package match

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubestellar/dibs/pkg/registry"
	"github.com/kubestellar/dibs/pkg/store"
)

// TestRefineWithoutLLM: no gateway configured → nil, the graceful "skip the
// refinement step" fallback. Never an error, never a panic.
func TestRefineWithoutLLM(t *testing.T) {
	e := &Engine{}
	if d := e.Refine(context.Background(), "Rough title", "rough body", nil); d != nil {
		t.Fatalf("no-LLM refine must return nil, got %+v", d)
	}
	var nilEngine *Engine
	if d := nilEngine.Refine(context.Background(), "t", "b", nil); d != nil {
		t.Fatalf("nil engine refine must return nil, got %+v", d)
	}
}

// fakeLLMServer returns an httptest server answering every chat completion
// with reply, and records the last user prompt.
func fakeLLMServer(t *testing.T, reply string, lastUser *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			if m.Role == "user" && lastUser != nil {
				*lastUser = m.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
	}))
}

// TestRefineParsesTitleAndBody: first line → title, rest → body; "Title:"
// and heading prefixes are stripped.
func TestRefineParsesTitleAndBody(t *testing.T) {
	srv := fakeLLMServer(t, "# Title: Polished idea\n\n## Problem\nUsers suffer.\n\n## Acceptance criteria\n- [ ] fixed", nil)
	defer srv.Close()
	e := &Engine{LLM: &LLM{BaseURL: srv.URL, Model: "test"}}
	d := e.Refine(context.Background(), "rough", "rough body", nil)
	if d == nil {
		t.Fatal("expected a refined draft")
	}
	if d.Title != "Polished idea" {
		t.Fatalf("title: %q", d.Title)
	}
	if !strings.Contains(d.Body, "## Problem") || !strings.Contains(d.Body, "Acceptance criteria") {
		t.Fatalf("body: %q", d.Body)
	}
}

// TestRefineWithRepoContext: the repo-targeted expansion feeds the repo's
// name and topics into the prompt.
func TestRefineWithRepoContext(t *testing.T) {
	var user string
	srv := fakeLLMServer(t, "Expanded title\n\nExpanded body.", &user)
	defer srv.Close()
	e := &Engine{LLM: &LLM{BaseURL: srv.URL, Model: "test"}}
	rp := &registry.RepoProfile{RepoID: "kubestellar/dibs", Description: "idea marketplace",
		Topics: []string{"kubernetes", "marketplace"}, Appetite: "small UX wins"}
	d := e.Refine(context.Background(), "rough", "rough body", rp)
	if d == nil || d.Title != "Expanded title" {
		t.Fatalf("draft: %+v", d)
	}
	for _, want := range []string{"kubestellar/dibs", "kubernetes, marketplace", "small UX wins"} {
		if !strings.Contains(user, want) {
			t.Errorf("repo context %q missing from prompt:\n%s", want, user)
		}
	}
}

// TestRefineLLMFailure: gateway errors → nil (skip), not an error surfaced
// to the user.
func TestRefineLLMFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	e := &Engine{LLM: &LLM{BaseURL: srv.URL, Model: "test"}}
	if d := e.Refine(context.Background(), "t", "b", nil); d != nil {
		t.Fatalf("failing LLM must yield nil, got %+v", d)
	}
}

// TestParseRefined: junk replies are rejected; overlong fields are clipped
// to the store limits.
func TestParseRefined(t *testing.T) {
	for _, junk := range []string{"", "single line only", "\n\nbody with no title"} {
		if d := parseRefined(junk); d != nil {
			t.Errorf("parseRefined(%q) = %+v, want nil", junk, d)
		}
	}
	longTitle := strings.Repeat("T", store.MaxTitleLen+50)
	longBody := strings.Repeat("é", store.MaxBodyBytes) // 2 bytes per rune → over the byte cap
	d := parseRefined(longTitle + "\n\n" + longBody)
	if d == nil {
		t.Fatal("expected a draft")
	}
	if len(d.Title) > store.MaxTitleLen*4 || len([]rune(d.Title)) > store.MaxTitleLen {
		t.Fatalf("title not clipped: %d runes", len([]rune(d.Title)))
	}
	if len(d.Body) > store.MaxBodyBytes {
		t.Fatalf("body not clipped: %d bytes", len(d.Body))
	}
	if !strings.HasPrefix(d.Body, "é") {
		t.Fatal("clipped body corrupted")
	}
}
