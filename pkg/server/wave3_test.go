package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubestellar/dibs/pkg/api"
	"github.com/kubestellar/dibs/pkg/settle"
	"github.com/kubestellar/dibs/pkg/store"
)

// settleIdea walks an idea through offer → accept → settled so credit-wall
// and stats tests have a real settled record. Returns the issue URL.
func (f *wave2Fixture) settleIdea(t *testing.T, ideatorSession, ownerSession, repoID string, idea store.Idea) string {
	t.Helper()
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", ideatorSession, map[string]string{"repoID": repoID})
	if rec.Code != http.StatusOK {
		t.Fatalf("offer: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/repos/"+repoID+"/decide", ownerSession,
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	res := decode[map[string]any](t, rec)
	if res["result"] != "settled" {
		t.Fatalf("expected settled, got %v", res)
	}
	url, _ := res["issueURL"].(string)
	return url
}

// TestCreditWallPublic pins the Wave-3 public surface: the credit wall is
// readable WITHOUT a session, lists exactly the settled ideas (handle, TLDR,
// issue link), and never exposes idea bodies or unsettled/private ideas —
// while every other data endpoint stays auth-guarded.
func TestCreditWallPublic(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})

	settled := f.createIdea(t, "bob-session", "Cluster idea", "kubernetes marketplace operators body", "public")
	issueURL := f.settleIdea(t, "bob-session", "alice-session", "kubestellar/dibs", settled)

	// Noise that must NOT appear on the wall: an unsettled public idea and
	// a private draft.
	f.createIdea(t, "bob-session", "Unsettled public", "still just a draft body", "public")
	f.createIdea(t, "charlie-session", "Secret draft", "private thoughts body", "private")

	// No session at all.
	rec := doJSON(t, f.h, "GET", "/api/credits", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public credits: %d %s", rec.Code, rec.Body.String())
	}
	res := decode[struct {
		Credits []api.CreditEntry `json:"credits"`
	}](t, rec)
	if len(res.Credits) != 1 {
		t.Fatalf("expected exactly the settled idea, got %d entries: %s", len(res.Credits), rec.Body.String())
	}
	c := res.Credits[0]
	if c.Author != "bob" || c.AuthorDisplay != "Bob B" || c.Title != "Cluster idea" ||
		c.RepoID != "kubestellar/dibs" || c.IssueURL != issueURL {
		t.Fatalf("credit entry wrong: %+v", c)
	}
	if c.TLDR == "" {
		t.Fatalf("credit entry should carry the TLDR")
	}
	// The wall must never expose raw idea bodies (the entry has no body
	// field) nor any unsettled idea.
	body := rec.Body.String()
	if strings.Contains(body, `"body"`) {
		t.Fatalf("credit wall exposes a body field: %s", body)
	}
	for _, leak := range []string{"Unsettled public", "Secret draft", "private thoughts"} {
		if strings.Contains(body, leak) {
			t.Fatalf("credit wall leaked %q: %s", leak, body)
		}
	}

	// Everything else stays guarded for anonymous callers.
	for _, path := range []string{"/api/ideas", "/api/ideas?scope=public", "/api/me", "/api/me/stats",
		"/api/notifications", "/api/repos", "/api/ideas/" + settled.ID} {
		rec := doJSON(t, f.h, "GET", path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s should 401 without a session, got %d", path, rec.Code)
		}
	}
}

// TestLandingPagePublic: the UI page renders for unauthenticated visitors
// (landing + credit wall live in it) with the hub sign-in origin injected.
func TestLandingPagePublic(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})
	rec := doJSON(t, f.h, "GET", "/", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("landing: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Your <em>idea</em>, your <em>credit</em>, their code", "Credit wall",
		"Why not just open an issue?", "never implemented at all", "raise the odds your idea gets used",
		"Accepted = implemented", "open-source idea exchange", "How the market works",
		"Innovators invest ideas", "BACKING OPENS SOON", "put idle tokens to work",
		"Live markets", "New listings", "Trending",
		"https://hive.kubestellar.io"} {
		if !strings.Contains(body, want) {
			t.Fatalf("landing page missing %q", want)
		}
	}
	if strings.Contains(body, "__HUB_URL__") {
		t.Fatalf("hub URL placeholder was not substituted")
	}
}

// TestIdeatorStats pins the cumulative funnel counts: settled ⊆ accepted ⊆
// offered ⊆ posted, scoped to the caller only.
func TestIdeatorStats(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})

	stats := func(session string) api.IdeatorStats {
		t.Helper()
		rec := doJSON(t, f.h, "GET", "/api/me/stats", session, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("stats: %d %s", rec.Code, rec.Body.String())
		}
		return decode[api.IdeatorStats](t, rec)
	}

	if s := stats("bob-session"); s.Posted != 0 || s.Score != 0 {
		t.Fatalf("fresh user should have zero stats, got %+v", s)
	}

	// bob: one draft, one offered (pending), one settled.
	f.createIdea(t, "bob-session", "Draft only", "kubernetes body", "private")
	offered := f.createIdea(t, "bob-session", "Offered one", "kubernetes marketplace body", "public")
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+offered.ID+"/offer", "bob-session", map[string]string{"repoID": "org/other"})
	if rec.Code != http.StatusOK {
		t.Fatalf("offer: %d %s", rec.Code, rec.Body.String())
	}
	settled := f.createIdea(t, "bob-session", "Settled one", "kubernetes operators body", "public")
	f.settleIdea(t, "bob-session", "alice-session", "kubestellar/dibs", settled)

	if s := stats("bob-session"); s.Posted != 3 || s.Offered != 2 || s.Accepted != 1 || s.Settled != 1 {
		t.Fatalf("bob stats wrong: %+v", s)
	}
	// Stats are caller-scoped: charlie sees none of bob's.
	if s := stats("charlie-session"); s.Posted != 0 || s.Score != 0 {
		t.Fatalf("charlie should have zero stats, got %+v", s)
	}
}
