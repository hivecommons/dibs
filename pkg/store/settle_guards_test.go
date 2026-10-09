package store

import (
	"errors"
	"testing"
	"time"
)

// TestMutateRejectsDuplicateIssueURL: one filed issue settles at most one
// idea, whatever casing, www. host, trailing slash, query, or fragment the
// second claim uses (hivecommons/dibs#343).
func TestMutateRejectsDuplicateIssueURL(t *testing.T) {
	s, _ := newTestStore(t)
	first := newIdea(t, s, VisibilityPublic)
	second := newIdea(t, s, VisibilityPublic)
	if _, err := s.Mutate(first.ID, true, func(i *Idea) error {
		i.IssueURL = "https://github.com/Org/Repo/issues/7"
		return nil
	}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Re-writing the same idea's own URL is not a conflict.
	if _, err := s.Mutate(first.ID, true, func(i *Idea) error {
		i.IssueURL = "https://github.com/org/repo/issues/7"
		return nil
	}); err != nil {
		t.Fatalf("same idea re-claim: %v", err)
	}
	for _, dup := range []string{
		"https://github.com/org/repo/issues/7",
		"https://www.github.com/ORG/repo/issues/7/",
		"https://github.com/org/repo/issues/7?x=1#top",
	} {
		_, err := s.Mutate(second.ID, true, func(i *Idea) error {
			i.IssueURL = dup
			return nil
		})
		var verr *ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("duplicate %q: err = %v, want *ValidationError", dup, err)
		}
	}
	if got, _ := s.Get(second.ID); got.IssueURL != "" {
		t.Fatalf("rejected claim persisted: %q", got.IssueURL)
	}
	if _, err := s.Mutate(second.ID, true, func(i *Idea) error {
		i.IssueURL = "https://github.com/org/repo/issues/70"
		return nil
	}); err != nil {
		t.Fatalf("distinct issue: %v", err)
	}
}

// TestUpdateKeepsConcurrentStatus: a stale copy written through Update
// cannot revert a status transition that committed after it was read
// (hivecommons/dibs#345).
func TestUpdateKeepsConcurrentStatus(t *testing.T) {
	s, _ := newTestStore(t)
	idea := newIdea(t, s, VisibilityPublic)
	if _, err := s.Transition(idea.ID, StatusOffered); err != nil {
		t.Fatalf("offer: %v", err)
	}
	stale, err := s.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := s.Transition(idea.ID, StatusAccepted); err != nil {
		t.Fatalf("accept: %v", err)
	}
	stale.Title = "Edited title"
	if err := s.Update(stale); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Get(idea.ID)
	if got.Status != StatusAccepted || got.Title != "Edited title" {
		t.Fatalf("after stale update: status=%s title=%q, want accepted and the edit", got.Status, got.Title)
	}
	if stale.Status != StatusAccepted {
		t.Fatalf("Update must report the stored status, got %s", stale.Status)
	}

	// A legal transition from the stored status is still applied.
	fresh, _ := s.Get(idea.ID)
	fresh.Status = StatusIssueLaunched
	if err := s.Update(fresh); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ := s.Get(idea.ID); got.Status != StatusIssueLaunched {
		t.Fatalf("legal transition dropped: %s", got.Status)
	}
}

// TestUpdateSettledIdea: a settled idea's title/body are frozen and its
// settle time survives later edits (hivecommons/dibs#344).
func TestUpdateSettledIdea(t *testing.T) {
	s, _ := newTestStore(t)
	idea := newIdea(t, s, VisibilityPublic)
	settledAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.Mutate(idea.ID, true, func(i *Idea) error {
		i.Status = StatusSettled
		i.SettledAt = settledAt
		return nil
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	edit, _ := s.Get(idea.ID)
	edit.Title = "Rewritten"
	var verr *ValidationError
	if err := s.Update(edit); !errors.As(err, &verr) {
		t.Fatalf("content edit of settled idea: err = %v, want *ValidationError", err)
	}
	vis, _ := s.Get(idea.ID)
	vis.Visibility = VisibilityPrivate
	if err := s.Update(vis); err != nil {
		t.Fatalf("visibility edit: %v", err)
	}
	got, _ := s.Get(idea.ID)
	if got.Title != idea.Title || got.Visibility != VisibilityPrivate || !got.SettledAt.Equal(settledAt) || !got.SettledTime().Equal(settledAt) {
		t.Fatalf("settled idea after edits = %+v", got)
	}
}

// TestUpdatePinsLegacySettledTime: a settled record from before SettledAt
// existed has its settle time pinned to the pre-edit UpdatedAt.
func TestUpdatePinsLegacySettledTime(t *testing.T) {
	s, _ := newTestStore(t)
	idea := newIdea(t, s, VisibilityPublic)
	legacy, err := s.Mutate(idea.ID, true, func(i *Idea) error {
		i.Status = StatusSettled
		return nil
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !legacy.SettledTime().Equal(legacy.UpdatedAt) {
		t.Fatalf("legacy SettledTime = %v, want UpdatedAt %v", legacy.SettledTime(), legacy.UpdatedAt)
	}
	legacyUpdatedAt := legacy.UpdatedAt
	legacy.Visibility = VisibilityPrivate
	if err := s.Update(legacy); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Get(idea.ID)
	if !got.SettledAt.Equal(legacyUpdatedAt) {
		t.Fatalf("legacy settle time not pinned: settledAt=%v, want %v", got.SettledAt, legacyUpdatedAt)
	}
}
