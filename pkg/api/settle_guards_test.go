package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

// TestConfirmIssueRejectsAlreadyCreditedIssue: one issue settles at most
// one idea, even for the same author (hivecommons/dibs#343).
func TestConfirmIssueRejectsAlreadyCreditedIssue(t *testing.T) {
	lookup := &settle.FakeIssueLookup{Issues: map[string]settle.FiledIssue{
		"kubestellar/dibs#5": {Author: "bob", CreatedAt: time.Now().UTC().Add(time.Hour)},
	}}
	a, mux, first := confirmFixture(t, "bob", lookup)
	second := mustCreate(t, a, "bob", "Second idea", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(second.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if code, body := confirm(t, mux, "bob", first.ID, "https://github.com/kubestellar/dibs/issues/5"); code != http.StatusOK {
		t.Fatalf("first confirm: status=%d body=%s", code, body)
	}
	code, body := confirm(t, mux, "bob", second.ID, "https://github.com/KubeStellar/dibs/issues/5/")
	if code != http.StatusBadRequest || !strings.Contains(body, "already credited") {
		t.Fatalf("second confirm: status=%d body=%s, want 400 naming the conflict", code, body)
	}
	got, err := a.Store.Get(second.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusAccepted || got.IssueURL != "" {
		t.Fatalf("duplicate confirm must not settle: status=%s issueURL=%q", got.Status, got.IssueURL)
	}
}

// TestSettledIdeaEditKeepsCreditWall: confirm stamps SettledAt, the credit
// wall reports it, and a later PUT can neither rewrite the credited title
// nor move the settle time (hivecommons/dibs#344).
func TestSettledIdeaEditKeepsCreditWall(t *testing.T) {
	now := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "bob", "Credited", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if code, body := confirm(t, mux, "bob", idea.ID, "https://github.com/kubestellar/dibs/issues/11"); code != http.StatusOK {
		t.Fatalf("confirm: status=%d body=%s", code, body)
	}

	rec := do(t, mux, ident("bob"), "PUT", "/api/ideas/"+idea.ID,
		`{"title":"Renamed","body":"body of Credited","visibility":"public"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "settled") {
		t.Fatalf("content edit of settled idea: status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	withFixedNow(t, now.Add(20*24*time.Hour))
	rec = do(t, mux, ident("bob"), "PUT", "/api/ideas/"+idea.ID,
		`{"title":"Credited","body":"body of Credited","visibility":"private"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("visibility edit: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	a.HandleCredits(rec, httptest.NewRequest("GET", "/api/credits", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("credits: status=%d", rec.Code)
	}
	credits := decodeBody[struct {
		Credits []CreditEntry `json:"credits"`
	}](t, rec)
	if len(credits.Credits) != 1 || credits.Credits[0].Title != "Credited" || !credits.Credits[0].SettledAt.Equal(now) {
		t.Fatalf("credits = %+v, want title Credited settled at %v", credits.Credits, now)
	}
}

// TestUpdateIdeaKeepsConcurrentAccept: a PUT built from a copy read before
// an accept committed must not revert the idea to offered
// (hivecommons/dibs#345). The race is reproduced by accepting between the
// handler's load and its write via a store wrapper.
func TestUpdateIdeaKeepsConcurrentAccept(t *testing.T) {
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "alice", "Racing", store.VisibilityPublic, store.StatusOffered)
	a.Store = &acceptBeforeUpdate{IdeaStore: a.Store, t: t}

	rec := do(t, mux, ident("alice"), "PUT", "/api/ideas/"+idea.ID,
		`{"title":"Racing edited","body":"body of Racing","visibility":"public"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.StatusAccepted || got.Title != "Racing edited" {
		t.Fatalf("after racing PUT: status=%s title=%q, want accepted and the edit", got.Status, got.Title)
	}
}

// acceptBeforeUpdate commits an accept right before delegating Update.
type acceptBeforeUpdate struct {
	IdeaStore
	t *testing.T
}

func (s *acceptBeforeUpdate) Update(idea *store.Idea) error {
	if _, err := s.IdeaStore.Transition(idea.ID, store.StatusAccepted); err != nil {
		s.t.Fatalf("concurrent accept: %v", err)
	}
	return s.IdeaStore.Update(idea)
}
