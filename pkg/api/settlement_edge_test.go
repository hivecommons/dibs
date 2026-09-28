package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

// fakeChatServer answers every OpenAI-style chat completion with reply and
// records the last user prompt (for asserting the expand-vs-refine prompt).
func fakeChatServer(t *testing.T, reply string, lastUser *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Cleanup(srv.Close)
	return srv
}

func TestRefineWithEngine(t *testing.T) {
	a, mux := newAPIFixture(t)
	var lastUser string
	srv := fakeChatServer(t, "Sharper title\n\nSharper body.", &lastUser)
	a.Engine = &match.Engine{Store: a.Store, Registry: a.Registry,
		LLM: &match.LLM{BaseURL: srv.URL, Model: "test", Client: srv.Client()}}

	rec := do(t, mux, ident("bob"), "POST", "/api/refine", `{"title":"T","body":"B"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("refine: status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeBody[refineOutput](t, rec)
	if !out.Refined || out.Title != "Sharper title" || out.Body != "Sharper body." {
		t.Fatalf("refine = %+v", out)
	}

	rec = do(t, mux, ident("bob"), "POST", "/api/refine", `{"title":"T","body":"B","repoID":"kubestellar/dibs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("repo refine: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if out = decodeBody[refineOutput](t, rec); !out.Refined {
		t.Fatalf("repo refine = %+v", out)
	}
	if !strings.Contains(lastUser, "TARGET REPOSITORY") || !strings.Contains(lastUser, "kubestellar/dibs") {
		t.Fatalf("repo refine did not use the expand prompt: %q", lastUser)
	}

	rec = do(t, mux, ident("bob"), "POST", "/api/refine", `{"title":"T","body":"B","repoID":"no/such"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown repo: status=%d, want 404", rec.Code)
	}
	rec = do(t, mux, ident("bob"), "POST", "/api/refine", `{"title":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: status=%d, want 400", rec.Code)
	}
}

func TestLaunchGatesAndDefaults(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)

	draft := mustCreate(t, a, "bob", "Still a draft", store.VisibilityPublic, store.StatusDraft)
	if _, err := a.Store.Mutate(draft.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed draft target: %v", err)
	}
	rec := do(t, mux, ident("bob"), "POST", "/api/ideas/"+draft.ID+"/launch", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("draft launch: status=%d, want 400", rec.Code)
	}

	noTarget := mustCreate(t, a, "bob", "No target", store.VisibilityPublic, store.StatusAccepted)
	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+noTarget.ID+"/launch", `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no target repo") {
		t.Fatalf("no-target launch: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// A hive-managed target must be accepted first: offered is not enough.
	offeredHive := mustCreate(t, a, "bob", "Offered to hive", store.VisibilityPublic, store.StatusOffered)
	if _, err := a.Store.Mutate(offeredHive.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed offered hive: %v", err)
	}
	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+offeredHive.ID+"/launch", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("offered hive launch: status=%d, want 400", rec.Code)
	}

	// An external target has no owner on our side: offered launches directly.
	external := mustCreate(t, a, "bob", "External bound", store.VisibilityPublic, store.StatusOffered)
	if _, err := a.Store.Mutate(external.ID, false, func(i *store.Idea) error { i.TargetRepo = "outside/project"; return nil }); err != nil {
		t.Fatalf("seed external: %v", err)
	}
	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+external.ID+"/launch", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("external launch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	launch := decodeBody[map[string]any](t, rec)
	if u, _ := launch["url"].(string); !strings.Contains(u, "github.com/outside/project/issues/new") {
		t.Fatalf("external launch url = %v", launch["url"])
	}
	// Empty title/body fall back to the idea's own.
	stored, err := a.Store.Get(external.ID)
	if err != nil || stored.Status != store.StatusIssueLaunched {
		t.Fatalf("stored after external launch = %+v err=%v", stored, err)
	}
	if launch["title"] != settle.IssueTitle(stored) {
		t.Fatalf("default title = %v, want %q", launch["title"], settle.IssueTitle(stored))
	}
	if fb, _ := launch["fullBody"].(string); !strings.Contains(fb, stored.Body) {
		t.Fatalf("default body missing idea body: %q", fb)
	}

	// Re-launching from issue_launched is idempotent: no transition error.
	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+external.ID+"/launch", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-launch: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+external.ID+"/launch", `{"title":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json launch: status=%d, want 400", rec.Code)
	}
}

func TestConfirmIssueGates(t *testing.T) {
	now := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns

	draft := mustCreate(t, a, "bob", "Not there yet", store.VisibilityPublic, store.StatusDraft)
	rec := do(t, mux, ident("bob"), "POST", "/api/ideas/"+draft.ID+"/confirm-issue", `{"issueURL":"https://github.com/kubestellar/dibs/issues/1"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not awaiting") {
		t.Fatalf("draft confirm: status=%d body=%s", rec.Code, rec.Body.String())
	}

	noTarget := mustCreate(t, a, "bob", "Accepted nowhere", store.VisibilityPublic, store.StatusAccepted)
	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+noTarget.ID+"/confirm-issue", `{"issueURL":"https://github.com/kubestellar/dibs/issues/1"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no target repo") {
		t.Fatalf("no-target confirm: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// alice both authors the idea AND owns the accepting repo: settlement
	// succeeds but no self-notification is written.
	own := mustCreate(t, a, "alice", "Self accepted", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(own.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed own target: %v", err)
	}
	rec = do(t, mux, ident("alice"), "POST", "/api/ideas/"+own.ID+"/confirm-issue", `{"issueURL":"https://github.com/other/repo/issues/9"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong-repo URL: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, ident("alice"), "POST", "/api/ideas/"+own.ID+"/confirm-issue", `{"issueURL":"https://github.com/kubestellar/dibs/issues/9"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("self confirm: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := ns.ListByUser("alice", true); len(got) != 0 {
		t.Fatalf("owner==author must not self-notify, got %+v", got)
	}

	// Already settled: confirming again is refused.
	rec = do(t, mux, ident("alice"), "POST", "/api/ideas/"+own.ID+"/confirm-issue", `{"issueURL":"https://github.com/kubestellar/dibs/issues/9"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("double confirm: status=%d, want 400", rec.Code)
	}

	// Only the author may confirm.
	other := mustCreate(t, a, "bob", "Bobs accepted", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(other.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed bob target: %v", err)
	}
	rec = do(t, mux, ident("charlie"), "POST", "/api/ideas/"+other.ID+"/confirm-issue", `{"issueURL":"https://github.com/kubestellar/dibs/issues/2"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-author confirm: status=%d, want 403", rec.Code)
	}

	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+other.ID+"/confirm-issue", `{"issueURL":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json confirm: status=%d, want 400", rec.Code)
	}
}
