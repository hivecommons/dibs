package server

// End-to-end tests for the matchmaker settlement flow: refine → offer →
// accept → launch (prefilled GitHub URL, no API call) → "I filed it"
// confirmation → settled + credit wall.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kubestellar/dibs/pkg/notify"
	"github.com/kubestellar/dibs/pkg/settle"
	"github.com/kubestellar/dibs/pkg/store"
)

// TestMatchmakerSettlementFlow: the DEFAULT (tokenless) settlement path.
func TestMatchmakerSettlementFlow(t *testing.T) {
	f := newWave2Server(t, nil) // no GitHub client: pure matchmaker mode
	idea := f.createIdea(t, "bob-session", "Kubernetes marketplace boost",
		"Improve the kubernetes marketplace matching for dibs.", "public")

	// Offer + accept.
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", "bob-session",
		map[string]string{"repoID": "kubestellar/dibs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("offer: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/repos/kubestellar/dibs/decide", "alice-session",
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	if res := decode[map[string]any](t, rec); res["result"] != "accepted" || res["issueURL"] != nil {
		t.Fatalf("matchmaker accept must not open an issue: %+v", res)
	}

	// Launch before/after guards: only the author, only when accepted.
	if rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/launch", "alice-session", map[string]string{}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-author launch: want 403, got %d", rec.Code)
	}

	// Launch: prefilled GitHub new-issue URL, edited title/body honored.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/launch", "bob-session",
		map[string]string{"title": "Edited title", "body": "Edited body for the repo."})
	if rec.Code != http.StatusOK {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body.String())
	}
	launch := decode[struct {
		URL       string `json:"url"`
		RepoID    string `json:"repoID"`
		Title     string `json:"title"`
		FullBody  string `json:"fullBody"`
		Truncated bool   `json:"truncated"`
	}](t, rec)
	if !strings.HasPrefix(launch.URL, "https://github.com/kubestellar/dibs/issues/new?") {
		t.Fatalf("launch url: %q", launch.URL)
	}
	u, err := url.Parse(launch.URL)
	if err != nil {
		t.Fatalf("parsing launch url: %v", err)
	}
	q := u.Query()
	if q.Get("title") != "Edited title" || q.Get("labels") != settle.Label {
		t.Fatalf("launch url params: %v", q)
	}
	if !strings.Contains(q.Get("body"), "Edited body for the repo.") || !strings.Contains(q.Get("body"), settle.Footer) {
		t.Fatalf("launch body: %q", q.Get("body"))
	}
	if launch.Truncated || !strings.HasSuffix(launch.FullBody, settle.Footer) {
		t.Fatalf("launch response: %+v", launch)
	}
	got, _ := f.store.Get(idea.ID)
	if got.Status != store.StatusIssueLaunched {
		t.Fatalf("after launch: %+v", got)
	}
	// Re-launch is idempotent (still issue_launched).
	if rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/launch", "bob-session", map[string]string{}); rec.Code != http.StatusOK {
		t.Fatalf("re-launch: %d %s", rec.Code, rec.Body.String())
	}

	// Confirmation validation: wrong repo / non-issue URLs rejected.
	for _, bad := range []string{
		"https://github.com/org/other/issues/9",
		"https://github.com/kubestellar/dibs/pull/9",
		"http://github.com/kubestellar/dibs/issues/9",
		"nonsense",
	} {
		rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/confirm-issue", "bob-session",
			map[string]string{"issueURL": bad})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("confirm %q: want 400, got %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	// Only the author may confirm.
	if rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/confirm-issue", "alice-session",
		map[string]string{"issueURL": "https://github.com/kubestellar/dibs/issues/9"}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-author confirm: want 403, got %d", rec.Code)
	}

	// The real confirmation settles the idea.
	issueURL := "https://github.com/kubestellar/dibs/issues/9"
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/confirm-issue", "bob-session",
		map[string]string{"issueURL": issueURL})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = f.store.Get(idea.ID)
	if got.Status != store.StatusSettled || got.IssueURL != issueURL {
		t.Fatalf("after confirm: %+v", got)
	}
	// The accepting repo's owner hears about the filed issue.
	if !kinds(f.notify.ListByUser("alice", false))[notify.KindIssue] {
		t.Fatalf("alice notifications: %+v", f.notify.ListByUser("alice", false))
	}
	// Double confirm rejected — settled is terminal.
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/confirm-issue", "bob-session",
		map[string]string{"issueURL": issueURL})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("double confirm: want 400, got %d", rec.Code)
	}

	// The settlement feeds the public credit wall.
	rec = doJSON(t, f.h, "GET", "/api/credits", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("credits: %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, issueURL) || !strings.Contains(body, "bob") {
		t.Fatalf("credit wall missing the settled idea: %s", body)
	}
}

// TestLaunchGuards: launching or confirming an idea that is not accepted is
// rejected; drafts stay untouched.
func TestLaunchGuards(t *testing.T) {
	f := newWave2Server(t, nil)
	idea := f.createIdea(t, "bob-session", "Still a draft", "kubernetes marketplace body", "public")
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/launch", "bob-session", map[string]string{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("launch draft: want 400, got %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/confirm-issue", "bob-session",
		map[string]string{"issueURL": "https://github.com/kubestellar/dibs/issues/1"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("confirm draft: want 400, got %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.store.Get(idea.ID)
	if got.Status != store.StatusDraft {
		t.Fatalf("draft mutated: %+v", got)
	}
}

// TestRefineEndpointWithoutLLM: no LLM configured → refined:false and the
// input echoed back; the UI simply skips the refinement step.
func TestRefineEndpointWithoutLLM(t *testing.T) {
	f := newWave2Server(t, nil) // fixture engine has no LLM
	rec := doJSON(t, f.h, "POST", "/api/refine", "bob-session",
		map[string]string{"title": "Rough", "body": "Rough body."})
	if rec.Code != http.StatusOK {
		t.Fatalf("refine: %d %s", rec.Code, rec.Body.String())
	}
	res := decode[struct {
		Refined bool   `json:"refined"`
		Title   string `json:"title"`
		Body    string `json:"body"`
	}](t, rec)
	if res.Refined || res.Title != "Rough" || res.Body != "Rough body." {
		t.Fatalf("no-LLM refine must echo the input: %+v", res)
	}
	// Missing fields rejected.
	if rec := doJSON(t, f.h, "POST", "/api/refine", "bob-session", map[string]string{"title": "only"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("refine without body: want 400, got %d", rec.Code)
	}
	// Unknown repo context rejected.
	if rec := doJSON(t, f.h, "POST", "/api/refine", "bob-session",
		map[string]string{"title": "t", "body": "b", "repoID": "no/such"}); rec.Code != http.StatusNotFound {
		t.Fatalf("refine with unknown repo: want 404, got %d", rec.Code)
	}
}
