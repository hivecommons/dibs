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

// TestHTTPHubClientListReposErrors pins the four failure arms of the hub
// fetch: an unbuildable URL, an unreachable hub, a non-200 status, and a
// body that is not a JSON repo list. Each surfaces as a "registry:" error
// and never as a partial repo slice.
func TestHTTPHubClientListReposErrors(t *testing.T) {
	t.Run("build request", func(t *testing.T) {
		c := &HTTPHubClient{BaseURL: ":bad"}
		repos, err := c.ListRepos(context.Background())
		if repos != nil || err == nil || !strings.Contains(err.Error(), "registry: building hub request") {
			t.Fatalf("repos=%v err=%v, want building request error", repos, err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		c := &HTTPHubClient{BaseURL: srv.URL}
		repos, err := c.ListRepos(context.Background())
		if repos != nil || err == nil || !strings.Contains(err.Error(), "registry: hub unreachable") {
			t.Fatalf("repos=%v err=%v, want unreachable error", repos, err)
		}
	})
	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		c := &HTTPHubClient{BaseURL: srv.URL}
		repos, err := c.ListRepos(context.Background())
		if repos != nil || err == nil || !strings.Contains(err.Error(), "hub returned status 502") {
			t.Fatalf("repos=%v err=%v, want status error", repos, err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"not":"a list"}`))
		}))
		defer srv.Close()
		c := &HTTPHubClient{BaseURL: srv.URL}
		repos, err := c.ListRepos(context.Background())
		if repos != nil || err == nil || !strings.Contains(err.Error(), "registry: decoding hub response") {
			t.Fatalf("repos=%v err=%v, want decoding error", repos, err)
		}
	})
}

// TestEnrichDescriptionsBestEffort: description enrichment is advisory. A
// GitHub endpoint that cannot be addressed, cannot be reached, answers
// non-200, or returns a blank description leaves the hub's profile intact
// and never fails ListRepos. Repos that already carry a description, or
// whose RepoID is not org/name, are never looked up at all.
func TestEnrichDescriptionsBestEffort(t *testing.T) {
	hubRepos := []RepoProfile{
		{RepoID: "owner/blank", Owner: "alice"},
		{RepoID: "owner/missing", Owner: "alice"},
		{RepoID: "owner/described", Owner: "alice", Description: "Already set"},
		{RepoID: "not-a-repo-id", Owner: "alice"},
	}
	newHub := func(t *testing.T) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(hubRepos)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	assertUntouched := func(t *testing.T, c *HTTPHubClient) {
		t.Helper()
		repos, err := c.ListRepos(context.Background())
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}
		if len(repos) != 4 {
			t.Fatalf("repos = %+v", repos)
		}
		if repos[0].Description != "" || repos[1].Description != "" || repos[2].Description != "Already set" || repos[3].Description != "" {
			t.Fatalf("descriptions changed: %+v", repos)
		}
	}

	t.Run("unaddressable github api", func(t *testing.T) {
		c := &HTTPHubClient{BaseURL: newHub(t).URL, GitHubAPI: ":bad"}
		assertUntouched(t, c)
	})
	t.Run("unreachable github api", func(t *testing.T) {
		gh := httptest.NewServer(http.NotFoundHandler())
		gh.Close()
		c := &HTTPHubClient{BaseURL: newHub(t).URL, GitHubAPI: gh.URL}
		assertUntouched(t, c)
	})
	t.Run("non-200 and blank description skip only the lookups that need it", func(t *testing.T) {
		var paths []string
		gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.Header.Get("Authorization") != "" {
				t.Errorf("Authorization sent without a token: %q", r.Header.Get("Authorization"))
			}
			switch r.URL.Path {
			case "/repos/owner/blank":
				_ = json.NewEncoder(w).Encode(map[string]string{"description": "   "})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer gh.Close()
		// Trailing slash on GitHubAPI must be tolerated; no Client means the
		// default timeout client is used.
		c := &HTTPHubClient{BaseURL: newHub(t).URL, GitHubAPI: gh.URL + "/"}
		assertUntouched(t, c)
		if len(paths) != 2 || paths[0] != "/repos/owner/blank" || paths[1] != "/repos/owner/missing" {
			t.Fatalf("github lookups = %v, want blank then missing only", paths)
		}
	})
}

// TestSyncPropagatesHubError: a failing hub leaves the registry untouched.
func TestSyncPropagatesHubError(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatal(err)
	}
	c := &HTTPHubClient{BaseURL: ":bad"}
	if err := r.Sync(context.Background(), c); err == nil || !strings.Contains(err.Error(), "registry: building hub request") {
		t.Fatalf("Sync err = %v, want hub error", err)
	}
	if got := r.List(false); len(got) != 2 {
		t.Fatalf("registry mutated by failed sync: %+v", got)
	}
}

// TestNewErrors: New refuses an unusable data dir, an unreadable repos.json,
// and a corrupt one, with distinct error messages.
func TestNewErrors(t *testing.T) {
	t.Run("data dir is a file", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "notadir")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		r, err := New(file)
		if r != nil || err == nil || !strings.Contains(err.Error(), "registry: creating data dir") {
			t.Fatalf("New(file) = %v, %v; want creating data dir error", r, err)
		}
	})
	t.Run("repos.json is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "repos.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		r, err := New(dir)
		if r != nil || err == nil || !strings.Contains(err.Error(), "registry: reading repos.json") {
			t.Fatalf("New = %v, %v; want reading repos.json error", r, err)
		}
	})
	t.Run("corrupt repos.json", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "repos.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		r, err := New(dir)
		if r != nil || err == nil || !strings.Contains(err.Error(), "registry: corrupt repos.json") {
			t.Fatalf("New = %v, %v; want corrupt repos.json error", r, err)
		}
	})
	t.Run("symbol migration cannot persist", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions")
		}
		dir := t.TempDir()
		legacy := []RepoProfile{{RepoID: "owner/legacy", Owner: "alice"}} // no Symbol: forces a migration write
		raw, _ := json.Marshal(legacy)
		if err := os.WriteFile(filepath.Join(dir, "repos.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		r, err := New(dir)
		if r != nil || err == nil || !strings.Contains(err.Error(), "registry: creating temp file") {
			t.Fatalf("New = %v, %v; want persist error from symbol migration", r, err)
		}
	})
}

// TestLoadSeedFileErrors: a missing or malformed seed file is reported and
// leaves the registry untouched.
func TestLoadSeedFileErrors(t *testing.T) {
	r, dir := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadSeedFile(filepath.Join(dir, "missing.json")); err == nil || !strings.Contains(err.Error(), "registry: reading seed file") {
		t.Fatalf("missing seed: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("[{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadSeedFile(bad); err == nil || !strings.Contains(err.Error(), "registry: parsing seed file") {
		t.Fatalf("malformed seed: %v", err)
	}
	if got := r.List(false); len(got) != 2 {
		t.Fatalf("registry mutated by failed seed load: %+v", got)
	}
}

// TestOwnerMutationsSurfacePersistErrors: an owner edit or a pass that cannot
// be written to disk returns the persist error instead of silently keeping
// the in-memory change as the only copy.
func TestOwnerMutationsSurfacePersistErrors(t *testing.T) {
	r, dir := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatal(err)
	}
	r.path = filepath.Join(dir, "missing", "repos.json")

	on := true
	rp, err := r.ApplyOwnerUpdate("kubestellar/dibs", "bob", OwnerUpdate{AcceptingIdeas: &on})
	if rp != nil || err == nil || !strings.Contains(err.Error(), "creating temp file") {
		t.Fatalf("ApplyOwnerUpdate = %+v, %v; want persist error", rp, err)
	}
	if err := r.AddPassedIdea("kubestellar/dibs", "bob", "idea-1"); err == nil || !strings.Contains(err.Error(), "creating temp file") {
		t.Fatalf("AddPassedIdea = %v; want persist error", err)
	}
	if !errors.Is(r.AddPassedIdea("kubestellar/dibs", "mallory", "idea-1"), ErrForbidden) {
		t.Fatal("non-owner pass must still be forbidden before any persist attempt")
	}
}

// TestListByOwnerSortsByRepoID: an owner with several repos gets them in
// RepoID order regardless of insertion order.
func TestListByOwnerSortsByRepoID(t *testing.T) {
	r, _ := newTestRegistry(t)
	err := r.Merge([]RepoProfile{
		{RepoID: "alice/zeta", Owner: "alice"},
		{RepoID: "alice/alpha", Owner: "alice"},
		{RepoID: "bob/beta", Owner: "bob"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.ListByOwner("alice")
	if len(got) != 2 || got[0].RepoID != "alice/alpha" || got[1].RepoID != "alice/zeta" {
		t.Fatalf("ListByOwner(alice) = %+v, want alpha then zeta", got)
	}
}
