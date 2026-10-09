package store

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestIssueURLKeyUnparsableFallback: Validate does not inspect IssueURL
// (the API layer does), so the store's duplicate check must still compare
// something sensible when url.Parse rejects the string. The fallback is the
// lower-cased input minus a trailing slash.
func TestIssueURLKeyUnparsableFallback(t *testing.T) {
	const malformed = "://github.com/Org/Repo/issues/7/"
	if got, want := issueURLKey(malformed), "://github.com/org/repo/issues/7"; got != want {
		t.Fatalf("issueURLKey(%q) = %q, want %q", malformed, got, want)
	}
	if got, want := issueURLKey("  ://x/ "), "://x"; got != want {
		t.Fatalf("issueURLKey with whitespace = %q, want %q", got, want)
	}

	// The fallback key still dedupes through Mutate: a second idea claiming
	// the same malformed URL with different casing/trailing slash is refused.
	s, _ := newTestStore(t)
	first := newIdea(t, s, VisibilityPublic)
	second := newIdea(t, s, VisibilityPublic)
	if _, err := s.Mutate(first.ID, true, func(i *Idea) error {
		i.IssueURL = malformed
		return nil
	}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	_, err := s.Mutate(second.ID, true, func(i *Idea) error {
		i.IssueURL = "://github.com/org/repo/issues/7"
		return nil
	})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("duplicate malformed claim: err = %v, want *ValidationError", err)
	}
	if got, _ := s.Get(second.ID); got.IssueURL != "" {
		t.Fatalf("rejected claim persisted: %q", got.IssueURL)
	}
}

// TestMutateIssueURLCheckReadError: the duplicate-issue scan reads every
// other idea from disk; a corrupt sibling must surface as an error rather
// than be skipped (skipping could let one issue settle two ideas), and the
// claim must not persist.
func TestMutateIssueURLCheckReadError(t *testing.T) {
	s, _ := newTestStore(t)
	claimant := newIdea(t, s, VisibilityPublic)
	sibling := newIdea(t, s, VisibilityPublic)
	if err := os.WriteFile(s.ideaPath(sibling.ID), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := s.Mutate(claimant.ID, true, func(i *Idea) error {
		i.IssueURL = "https://github.com/org/repo/issues/9"
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "corrupt idea "+sibling.ID) {
		t.Fatalf("Mutate err = %v, want corrupt idea error naming %s", err, sibling.ID)
	}
	if got, err := s.Get(claimant.ID); err != nil || got.IssueURL != "" {
		t.Fatalf("claim persisted despite failed duplicate scan: idea=%+v err=%v", got, err)
	}

	// Not changing IssueURL skips the scan entirely, so the corrupt sibling
	// does not block unrelated edits to the claimant.
	if _, err := s.Mutate(claimant.ID, true, func(i *Idea) error {
		i.Title = "Retitled"
		return nil
	}); err != nil {
		t.Fatalf("unrelated Mutate blocked by corrupt sibling: %v", err)
	}
}
