package registry

import (
	"errors"
	"testing"
)

func TestRepoProfileHasPassed(t *testing.T) {
	rp := &RepoProfile{PassedIdeas: []string{"idea-1", "idea-2"}}
	if !rp.HasPassed("idea-2") {
		t.Fatal("HasPassed(idea-2) = false, want true")
	}
	if rp.HasPassed("idea-3") {
		t.Fatal("HasPassed(idea-3) = true, want false")
	}
	if (&RepoProfile{}).HasPassed("idea-1") {
		t.Fatal("empty profile HasPassed = true, want false")
	}
}

func TestAddPassedIdea(t *testing.T) {
	r, dir := newTestRegistry(t)
	if err := r.Merge([]RepoProfile{{RepoID: "org/repo", HiveID: "h", Owner: "alice"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if err := r.AddPassedIdea("org/missing", "alice", "idea-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo err = %v, want ErrNotFound", err)
	}
	if err := r.AddPassedIdea("org/repo", "mallory", "idea-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner err = %v, want ErrForbidden", err)
	}

	if err := r.AddPassedIdea("org/repo", "alice", "idea-1"); err != nil {
		t.Fatalf("AddPassedIdea: %v", err)
	}
	// Idempotent: a second swipe must not duplicate the entry.
	if err := r.AddPassedIdea("org/repo", "alice", "idea-1"); err != nil {
		t.Fatalf("repeat AddPassedIdea: %v", err)
	}
	rp, err := r.Get("org/repo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(rp.PassedIdeas) != 1 || rp.PassedIdeas[0] != "idea-1" {
		t.Fatalf("PassedIdeas = %v, want [idea-1]", rp.PassedIdeas)
	}

	// The pass must survive a reopen (persisted to disk).
	r2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp2, err := r2.Get("org/repo")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !rp2.HasPassed("idea-1") {
		t.Fatal("passed idea lost after reopen")
	}
}
