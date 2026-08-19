package store

import (
	"testing"
)

// TestCanTransition pins the full state machine:
// draft/public → offered → accepted/declined → settled.
func TestCanTransition(t *testing.T) {
	allowed := map[[2]string]bool{
		{StatusDraft, StatusOffered}:    true,
		{StatusDraft, StatusAccepted}:   true, // public idea accepted straight from a repo feed
		{StatusOffered, StatusAccepted}: true,
		{StatusOffered, StatusDeclined}: true,
		{StatusDeclined, StatusOffered}: true, // re-offer after a decline
		{StatusAccepted, StatusSettled}: true,
	}
	statuses := []string{StatusDraft, StatusOffered, StatusAccepted, StatusDeclined, StatusSettled}
	for _, from := range statuses {
		for _, to := range statuses {
			want := allowed[[2]string{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
	// Terminal states go nowhere; unknown statuses go nowhere.
	if CanTransition(StatusSettled, StatusDraft) || CanTransition("bogus", StatusOffered) {
		t.Error("terminal/unknown statuses must not transition")
	}
}

func newIdea(t *testing.T, s *Store, visibility string) *Idea {
	t.Helper()
	idea := &Idea{Author: "alice", AuthorDisplay: "Alice", Title: "Title", Body: "Body text here", Visibility: visibility}
	if err := s.Create(idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return idea
}

// TestTransitionEnforced: the store rejects illegal moves.
func TestTransitionEnforced(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idea := newIdea(t, s, VisibilityPublic)

	if _, err := s.Transition(idea.ID, StatusSettled); err == nil {
		t.Fatal("draft → settled must be rejected")
	}
	for _, step := range []string{StatusOffered, StatusDeclined, StatusOffered, StatusAccepted, StatusSettled} {
		if _, err := s.Transition(idea.ID, step); err != nil {
			t.Fatalf("transition to %s: %v", step, err)
		}
	}
	if _, err := s.Transition(idea.ID, StatusOffered); err == nil {
		t.Fatal("settled is terminal; transition out must be rejected")
	}
}

// TestUpdateInvalidatesMatchCache: editing content clears TLDR and matches;
// a no-content-change update preserves them.
func TestUpdateInvalidatesMatchCache(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idea := newIdea(t, s, VisibilityPublic)
	if _, err := s.Mutate(idea.ID, false, func(i *Idea) error {
		i.TLDR = "cached tldr"
		i.Matches = []Match{{RepoID: "org/repo", Score: 88, RepoHash: "h"}}
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	// Visibility-only update: cache survives.
	upd := &Idea{ID: idea.ID, Title: idea.Title, Body: idea.Body, Visibility: VisibilityPrivate, Status: StatusDraft}
	if err := s.Update(upd); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Get(idea.ID)
	if got.TLDR != "cached tldr" || len(got.Matches) != 1 {
		t.Fatalf("cache must survive a non-content update: %+v", got)
	}

	// Content update: cache cleared.
	upd = &Idea{ID: idea.ID, Title: "New title", Body: idea.Body, Visibility: VisibilityPrivate, Status: StatusDraft}
	if err := s.Update(upd); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = s.Get(idea.ID)
	if got.TLDR != "" || len(got.Matches) != 0 {
		t.Fatalf("content edit must clear TLDR and matches: %+v", got)
	}
}

// TestUpdatePreservesServerFields: offers/passes/target/issueURL cannot be
// clobbered through Update.
func TestUpdatePreservesServerFields(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idea := newIdea(t, s, VisibilityPublic)
	if _, err := s.Mutate(idea.ID, false, func(i *Idea) error {
		i.Offers = []Offer{{RepoID: "org/repo", Status: OfferPending}}
		i.PassedRepos = []string{"org/other"}
		i.TargetRepo = "org/repo"
		i.IssueURL = "https://github.com/org/repo/issues/1"
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	upd := &Idea{ID: idea.ID, Title: idea.Title, Body: idea.Body, Visibility: VisibilityPublic, Status: StatusDraft,
		Offers: nil, PassedRepos: nil, TargetRepo: "hijack/repo", IssueURL: "https://evil"}
	if err := s.Update(upd); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Get(idea.ID)
	if len(got.Offers) != 1 || len(got.PassedRepos) != 1 || got.TargetRepo != "org/repo" ||
		got.IssueURL != "https://github.com/org/repo/issues/1" {
		t.Fatalf("server-managed fields clobbered: %+v", got)
	}
}

// TestListOfferedTo: only ideas with PENDING offers to the given repos come
// back — including private ones (the explicit reveal) — and nothing else.
func TestListOfferedTo(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	offered := newIdea(t, s, VisibilityPrivate)
	if _, err := s.Mutate(offered.ID, true, func(i *Idea) error {
		i.Status = StatusOffered
		i.Offers = []Offer{{RepoID: "org/a", Status: OfferPending}}
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	declined := newIdea(t, s, VisibilityPublic)
	if _, err := s.Mutate(declined.ID, true, func(i *Idea) error {
		i.Status = StatusDeclined
		i.Offers = []Offer{{RepoID: "org/a", Status: OfferDeclined}}
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	newIdea(t, s, VisibilityPrivate) // never offered

	got, err := s.ListOfferedTo([]string{"org/a"})
	if err != nil {
		t.Fatalf("ListOfferedTo: %v", err)
	}
	if len(got) != 1 || got[0].ID != offered.ID {
		t.Fatalf("want exactly the pending-offer idea, got %+v", got)
	}
	if got, _ := s.ListOfferedTo([]string{"org/b"}); len(got) != 0 {
		t.Fatalf("other repo must see nothing, got %+v", got)
	}
}
