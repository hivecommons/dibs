package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/hivecommons/dibs/pkg/store"
)

// staleMatcher is a healthy faultMatcher whose hive matches name repos that
// may no longer exist in the registry — the window between scoring and the
// ideator reading their matches.
type staleMatcher struct {
	faultMatcher
	matches []store.Match
}

func (m *staleMatcher) MatchesForIdea(context.Context, *store.Idea) ([]store.Match, error) {
	return m.matches, nil
}

// TestIdeaMatchesSkipsVanishedRepo pins that GET /api/ideas/{id}/matches
// drops a scored repo that the registry can no longer resolve instead of
// failing the whole response, and still returns the repos that do resolve.
func TestIdeaMatchesSkipsVanishedRepo(t *testing.T) {
	a, mux := newAPIFixture(t)
	a.Engine = &staleMatcher{matches: []store.Match{
		{RepoID: "gone/repo", Score: 0.99, Reason: "stale"},
		{RepoID: "kubestellar/dibs", Score: 0.5, Reason: "still here"},
	}}
	idea := mustCreate(t, a, "bob", "Stale match", store.VisibilityPrivate, store.StatusDraft)

	rec := do(t, mux, ident("bob"), "GET", "/api/ideas/"+idea.ID+"/matches", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("matches: %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeBody[struct {
		Matches []matchView `json:"matches"`
	}](t, rec)
	if len(out.Matches) != 1 {
		t.Fatalf("matches = %+v, want exactly the surviving repo", out.Matches)
	}
	if m := out.Matches[0]; m.Repo == nil || m.Repo.RepoID != "kubestellar/dibs" || m.Reason != "still here" {
		t.Fatalf("surviving match = %+v, want kubestellar/dibs", m)
	}
}

// TestRepoFeedSkipsIdeasNoLongerOpen pins that a public idea that has already
// been accepted or settled elsewhere never shows up as a feed candidate for
// another repo, while draft and offered public ideas still do.
func TestRepoFeedSkipsIdeasNoLongerOpen(t *testing.T) {
	a, mux := newAPIFixture(t)
	a.Engine = &faultMatcher{}
	mustCreate(t, a, "bob", "Taken", store.VisibilityPublic, store.StatusAccepted)
	mustCreate(t, a, "bob", "Done", store.VisibilityPublic, store.StatusSettled)
	open := mustCreate(t, a, "bob", "Open", store.VisibilityPublic, store.StatusDraft)

	rec := do(t, mux, ident("alice"), "GET", "/api/repos/kubestellar/dibs/feed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeBody[struct {
		Offers     []matchView `json:"offers"`
		Candidates []matchView `json:"candidates"`
	}](t, rec)
	if len(out.Offers) != 0 {
		t.Fatalf("offers = %+v, want none", out.Offers)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Idea == nil || out.Candidates[0].Idea.ID != open.ID {
		t.Fatalf("candidates = %+v, want only the open draft %s", out.Candidates, open.ID)
	}
}
