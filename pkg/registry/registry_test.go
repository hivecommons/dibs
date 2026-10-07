package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestHTTPHubClientEnrichesEmptyDescriptions(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ReposPath {
			t.Fatalf("unexpected hub path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]RepoProfile{{RepoID: "owner/repo", HiveID: "h", Owner: "alice"}})
	}))
	defer hub.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo" {
			t.Fatalf("unexpected github path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"description": "Enriched from GitHub"})
	}))
	defer gh.Close()
	c := &HTTPHubClient{BaseURL: hub.URL, GitHubAPI: gh.URL, Token: "token"}
	repos, err := c.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Description != "Enriched from GitHub" {
		t.Fatalf("repos not enriched: %+v", repos)
	}
}

func sampleRepos() []RepoProfile {
	return []RepoProfile{
		{RepoID: "kubestellar/kubestellar", HiveID: "hive-ks", Owner: "alice", Description: "Multi-cluster"},
		{RepoID: "kubestellar/dibs", HiveID: "hive-ks", Owner: "bob", Description: "Idea marketplace"},
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
	if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "bob", OwnerUpdate{AcceptingIdeas: &on}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	accepting := r.List(true)
	if len(accepting) != 1 || accepting[0].RepoID != "kubestellar/dibs" {
		t.Fatalf("accepting list wrong: %+v", accepting)
	}
}

func TestListByOwnerFiltersAndCopies(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	alice := r.ListByOwner("alice")
	if len(alice) != 1 || alice[0].RepoID != "kubestellar/kubestellar" {
		t.Fatalf("alice repos = %+v", alice)
	}
	alice[0].Description = "mutated"
	rp, err := r.Get("kubestellar/kubestellar")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rp.Description == "mutated" {
		t.Fatal("ListByOwner returned mutable registry internals")
	}
	if got := r.ListByOwner("nobody"); len(got) != 0 {
		t.Fatalf("unknown owner repos = %+v, want none", got)
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
	if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "bob", OwnerUpdate{AcceptingIdeas: &on, Topics: &topics, Appetite: &appetite}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}

	if err := r.Sync(context.Background(), hub); err != nil {
		t.Fatalf("re-Sync: %v", err)
	}
	rp, err := r.Get("kubestellar/dibs")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !rp.AcceptingIdeas || len(rp.Topics) != 2 || rp.Appetite != appetite {
		t.Fatalf("re-sync clobbered local edits: %+v", rp)
	}
}

func TestSyncPreservesDescriptionWhenIncomingBlank(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Merge([]RepoProfile{{RepoID: "owner/repo", HiveID: "h", Owner: "alice", Description: "enriched"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := r.Sync(context.Background(), &FakeHub{Repos: []RepoProfile{{RepoID: "owner/repo", HiveID: "h", Owner: "alice"}}}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	rp, err := r.Get("owner/repo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rp.Description != "enriched" {
		t.Fatalf("description was not preserved: %+v", rp)
	}
}

// TestOwnerAuthorization: only the owner may edit a profile.
func TestOwnerAuthorization(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	on := true
	if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "mallory", OwnerUpdate{AcceptingIdeas: &on}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner toggle: want ErrForbidden, got %v", err)
	}
	rp, _ := r.Get("kubestellar/dibs")
	if rp.AcceptingIdeas {
		t.Fatal("non-owner toggle took effect")
	}
	if _, err := r.ApplyOwnerUpdate("nope/nope", "bob", OwnerUpdate{AcceptingIdeas: &on}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo: want ErrNotFound, got %v", err)
	}
}

// TestOwnerUpdateBounds: every owner-editable text field is capped so a repo
// owner cannot store oversized or blank values that are served publicly and
// embedded in every match prompt.
func TestOwnerUpdateBounds(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	cases := map[string]OwnerUpdate{
		"too many topics": {Topics: ptr(make([]string, MaxTopics+1))},
		"long topic":      {Topics: ptr([]string{strings.Repeat("x", MaxTopicLen+1)})},
		"blank topic":     {Topics: ptr([]string{"ok", "  "})},
		"long appetite":   {Appetite: ptr(strings.Repeat("x", MaxAppetiteLen+1))},
	}
	for name, upd := range cases {
		if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "bob", upd); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
	rp, _ := r.Get("kubestellar/dibs")
	if len(rp.Topics) != 0 || rp.Appetite != "" {
		t.Fatalf("rejected update took effect: %+v", rp)
	}
	ok := OwnerUpdate{Topics: ptr([]string{strings.Repeat("x", MaxTopicLen)}), Appetite: ptr(strings.Repeat("y", MaxAppetiteLen))}
	if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "bob", ok); err != nil {
		t.Fatalf("at-limit update rejected: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestPersistenceAcrossReopen(t *testing.T) {
	r, dir := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	on := true
	if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "bob", OwnerUpdate{AcceptingIdeas: &on}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}

	r2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp, err := r2.Get("kubestellar/dibs")
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

func TestLookupIsCaseInsensitive(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Sync(context.Background(), &FakeHub{Repos: sampleRepos()}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	rp, err := r.Get("KubeStellar/DIBS")
	if err != nil {
		t.Fatalf("Get case-folded: %v", err)
	}
	if rp.RepoID != "kubestellar/dibs" {
		t.Fatalf("Get must return the registered spelling, got %q", rp.RepoID)
	}
	on := true
	if _, err := r.ApplyOwnerUpdate("Kubestellar/Dibs", "bob", OwnerUpdate{AcceptingIdeas: &on}); err != nil {
		t.Fatalf("ApplyOwnerUpdate case-folded: %v", err)
	}
	if err := r.AddPassedIdea("KUBESTELLAR/dibs", "bob", "idea-1"); err != nil {
		t.Fatalf("AddPassedIdea case-folded: %v", err)
	}
	canon, _ := r.Get("kubestellar/dibs")
	if !canon.AcceptingIdeas || !canon.HasPassed("idea-1") {
		t.Fatalf("case-folded writes must land on the canonical entry: %+v", canon)
	}
	if len(r.List(false)) != 2 {
		t.Fatalf("case-folded writes must not create a second entry: %+v", r.List(false))
	}
	if _, err := r.Get("kubestellar/unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo: err=%v, want ErrNotFound", err)
	}
}

func TestMergeRecasedIDKeepsSingleProfile(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Sync(context.Background(), &FakeHub{Repos: sampleRepos()}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	on := true
	topics := []string{"ai"}
	appetite := "small"
	if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "bob", OwnerUpdate{AcceptingIdeas: &on, Topics: &topics, Appetite: &appetite}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	if err := r.AddPassedIdea("kubestellar/dibs", "bob", "idea-1"); err != nil {
		t.Fatalf("AddPassedIdea: %v", err)
	}
	before := len(r.List(false))
	symbol, _ := r.Get("kubestellar/dibs")

	recased := RepoProfile{RepoID: "KubeStellar/Dibs", HiveID: "hive-x", Owner: "bob"}
	if err := r.Merge([]RepoProfile{recased}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := len(r.List(false)); got != before {
		t.Fatalf("re-cased Merge created a duplicate: %d profiles, want %d", got, before)
	}
	rp, err := r.Get("kubestellar/dibs")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rp.RepoID != "kubestellar/dibs" || rp.Symbol != symbol.Symbol {
		t.Fatalf("canonical ID/symbol changed: %+v", rp)
	}
	if !rp.AcceptingIdeas || len(rp.Topics) != 1 || rp.Appetite != appetite || !rp.HasPassed("idea-1") {
		t.Fatalf("re-cased Merge lost owner fields: %+v", rp)
	}
}

func TestSeedFileRecasedEntryDoesNotClobberOrDuplicate(t *testing.T) {
	r, dir := newTestRegistry(t)
	if err := r.Merge([]RepoProfile{{RepoID: "demo/repo", HiveID: "hive-demo", Owner: "alice", AcceptingIdeas: true}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	off := false
	if _, err := r.ApplyOwnerUpdate("demo/repo", "alice", OwnerUpdate{AcceptingIdeas: &off}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	seed := filepath.Join(dir, "seed.json")
	raw, _ := json.Marshal([]RepoProfile{{RepoID: "Demo/Repo", HiveID: "hive-demo", Owner: "alice", AcceptingIdeas: true}})
	if err := os.WriteFile(seed, raw, 0o644); err != nil {
		t.Fatalf("writing seed: %v", err)
	}
	if err := r.LoadSeedFile(seed); err != nil {
		t.Fatalf("LoadSeedFile: %v", err)
	}
	if got := len(r.List(false)); got != 1 {
		t.Fatalf("re-cased seed entry created a duplicate: %d profiles", got)
	}
	rp, _ := r.Get("demo/repo")
	if rp.AcceptingIdeas {
		t.Fatal("re-cased seed entry clobbered a local edit")
	}
}
