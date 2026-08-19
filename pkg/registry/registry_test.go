package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, dir
}

func sampleRepos() []RepoProfile {
	return []RepoProfile{
		{RepoID: "kubestellar/kubestellar", HiveID: "hive-ks", Owner: "alice", Description: "Multi-cluster"},
		{RepoID: "kubestellar/ideate", HiveID: "hive-ks", Owner: "bob", Description: "Idea marketplace"},
	}
}

func TestSyncAndList(t *testing.T) {
	r, _ := newTestRegistry(t)
	hub := &FakeHub{Repos: sampleRepos()}
	if err := r.Sync(context.Background(), hub); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	all := r.List(false)
	if len(all) != 2 {
		t.Fatalf("want 2 repos, got %d", len(all))
	}
	if len(r.List(true)) != 0 {
		t.Fatal("no repo opted in yet; accepting list should be empty")
	}

	// Owner opts in; accepting list now shows it.
	on := true
	if _, err := r.ApplyOwnerUpdate("kubestellar/ideate", "bob", OwnerUpdate{AcceptingIdeas: &on}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	accepting := r.List(true)
	if len(accepting) != 1 || accepting[0].RepoID != "kubestellar/ideate" {
		t.Fatalf("accepting list wrong: %+v", accepting)
	}
}

// TestSyncPreservesLocalEdits: a hub re-sync must not clobber owner toggles.
func TestSyncPreservesLocalEdits(t *testing.T) {
	r, _ := newTestRegistry(t)
	hub := &FakeHub{Repos: sampleRepos()}
	if err := r.Sync(context.Background(), hub); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	on := true
	topics := []string{"ai", "multicluster"}
	appetite := "small, well-scoped features"
	if _, err := r.ApplyOwnerUpdate("kubestellar/ideate", "bob", OwnerUpdate{AcceptingIdeas: &on, Topics: &topics, Appetite: &appetite}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}

	if err := r.Sync(context.Background(), hub); err != nil {
		t.Fatalf("re-Sync: %v", err)
	}
	rp, err := r.Get("kubestellar/ideate")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !rp.AcceptingIdeas || len(rp.Topics) != 2 || rp.Appetite != appetite {
		t.Fatalf("re-sync clobbered local edits: %+v", rp)
	}
}

// TestOwnerAuthorization: only the owner may edit a profile.
func TestOwnerAuthorization(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	on := true
	if _, err := r.ApplyOwnerUpdate("kubestellar/ideate", "mallory", OwnerUpdate{AcceptingIdeas: &on}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner toggle: want ErrForbidden, got %v", err)
	}
	rp, _ := r.Get("kubestellar/ideate")
	if rp.AcceptingIdeas {
		t.Fatal("non-owner toggle took effect")
	}
	if _, err := r.ApplyOwnerUpdate("nope/nope", "bob", OwnerUpdate{AcceptingIdeas: &on}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo: want ErrNotFound, got %v", err)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	r, dir := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	on := true
	if _, err := r.ApplyOwnerUpdate("kubestellar/ideate", "bob", OwnerUpdate{AcceptingIdeas: &on}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}

	r2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp, err := r2.Get("kubestellar/ideate")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !rp.AcceptingIdeas {
		t.Fatal("toggle lost across reopen")
	}
}

func TestSeedFile(t *testing.T) {
	r, dir := newTestRegistry(t)
	seed := filepath.Join(dir, "seed.json")
	seedRepos := []RepoProfile{
		{RepoID: "demo/repo", HiveID: "hive-demo", Owner: "alice", AcceptingIdeas: true, Topics: []string{"demo"}},
	}
	raw, _ := json.Marshal(seedRepos)
	if err := os.WriteFile(seed, raw, 0o644); err != nil {
		t.Fatalf("writing seed: %v", err)
	}
	if err := r.LoadSeedFile(seed); err != nil {
		t.Fatalf("LoadSeedFile: %v", err)
	}
	rp, err := r.Get("demo/repo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !rp.AcceptingIdeas {
		t.Fatal("seed acceptingIdeas not honored on first sight")
	}

	// Owner turns intake off; re-loading the seed must NOT flip it back.
	off := false
	if _, err := r.ApplyOwnerUpdate("demo/repo", "alice", OwnerUpdate{AcceptingIdeas: &off}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	if err := r.LoadSeedFile(seed); err != nil {
		t.Fatalf("re-LoadSeedFile: %v", err)
	}
	rp, _ = r.Get("demo/repo")
	if rp.AcceptingIdeas {
		t.Fatal("seed re-load clobbered a local edit")
	}
}
