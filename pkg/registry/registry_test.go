package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/ghrate"
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

func enrichHubServer(t *testing.T, repos []RepoProfile) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(repos)
	}))
}

func TestSyncEnrichesEachRepoOnce(t *testing.T) {
	hub := enrichHubServer(t, []RepoProfile{
		{RepoID: "o/a", HiveID: "h", Owner: "alice"},
		{RepoID: "o/b", HiveID: "h", Owner: "alice"},
	})
	defer hub.Close()
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"description": "from github"})
	}))
	defer gh.Close()
	c := &HTTPHubClient{BaseURL: hub.URL, GitHubAPI: gh.URL}
	reg, _ := newTestRegistry(t)
	for i := 0; i < 2; i++ {
		if err := reg.Sync(context.Background(), c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("GitHub hits = %d, want 2 (once per repo)", got)
	}
	if p, _ := reg.Get("o/a"); p == nil || p.Description != "from github" {
		t.Fatalf("description not stored: %+v", p)
	}
}

func TestEnrichmentStopsOnRateLimit(t *testing.T) {
	hub := enrichHubServer(t, []RepoProfile{
		{RepoID: "o/a", HiveID: "h", Owner: "alice"},
		{RepoID: "o/b", HiveID: "h", Owner: "alice"},
		{RepoID: "o/c", HiveID: "h", Owner: "alice"},
	})
	defer hub.Close()
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer gh.Close()
	c := &HTTPHubClient{BaseURL: hub.URL, GitHubAPI: gh.URL}
	reg, _ := newTestRegistry(t)
	if err := reg.Sync(context.Background(), c); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("GitHub hits = %d, want 1 (loop must stop at first 403)", got)
	}
}

func TestEnrichmentNotRetriedForEmptyDescription(t *testing.T) {
	hub := enrichHubServer(t, []RepoProfile{{RepoID: "o/a", HiveID: "h", Owner: "alice"}})
	defer hub.Close()
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"description": ""})
	}))
	defer gh.Close()
	c := &HTTPHubClient{BaseURL: hub.URL, GitHubAPI: gh.URL}
	reg, _ := newTestRegistry(t)
	for i := 0; i < 2; i++ {
		if err := reg.Sync(context.Background(), c); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("GitHub hits = %d, want 1", got)
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
		"too many topics":         {Topics: ptr(make([]string, MaxTopics+1))},
		"long topic":              {Topics: ptr([]string{strings.Repeat("x", MaxTopicLen+1)})},
		"blank topic":             {Topics: ptr([]string{"ok", "  "})},
		"long appetite":           {Appetite: ptr(strings.Repeat("x", MaxAppetiteLen+1))},
		"long multibyte topic":    {Topics: ptr([]string{strings.Repeat("é", MaxTopicLen+1)})},
		"long multibyte appetite": {Appetite: ptr(strings.Repeat("é", MaxAppetiteLen+1))},
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
	multi := OwnerUpdate{Topics: ptr([]string{strings.Repeat("é", MaxTopicLen)}), Appetite: ptr(strings.Repeat("é", MaxAppetiteLen))}
	if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "bob", multi); err != nil {
		t.Fatalf("at-limit multibyte update rejected: %v", err)
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

func TestSanitizeContributeURL(t *testing.T) {
	cases := map[string]string{
		"":                                    "",
		"   ":                                 "",
		"https://hive.example.org/contribute": "https://hive.example.org/contribute",
		"HTTP://hive.example.org/":            "HTTP://hive.example.org/",
		" https://hive.example.org ":          "https://hive.example.org",
		"javascript:alert(1)":                 "",
		"data:text/html,<script>1</script>":   "",
		"vbscript:msgbox":                     "",
		"file:///etc/passwd":                  "",
		"ftp://hive.example.org/":             "",
		"/contribute":                         "",
		"contribute":                          "",
		"https://":                            "",
		"http://[::1":                         "",
	}
	for in, want := range cases {
		if got := sanitizeContributeURL(in); got != want {
			t.Errorf("sanitizeContributeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeDropsNonHTTPContributeURL(t *testing.T) {
	r, _ := newTestRegistry(t)
	err := r.Merge([]RepoProfile{
		{RepoID: "demo/good", HiveID: "h", Owner: "alice", ContributeURL: "https://hive.example.org/contribute"},
		{RepoID: "demo/bad", HiveID: "h", Owner: "alice", ContributeURL: "javascript:alert(document.cookie)"},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if rp, _ := r.Get("demo/good"); rp.ContributeURL != "https://hive.example.org/contribute" {
		t.Fatalf("https contributeURL not preserved: %q", rp.ContributeURL)
	}
	if rp, _ := r.Get("demo/bad"); rp.ContributeURL != "" {
		t.Fatalf("non-http contributeURL accepted from hub input: %q", rp.ContributeURL)
	}

	// A later sync that advertises a bad URL must blank the stored one, not keep it.
	if err := r.Merge([]RepoProfile{{RepoID: "demo/good", HiveID: "h", Owner: "alice", ContributeURL: "data:text/html,x"}}); err != nil {
		t.Fatalf("re-Merge: %v", err)
	}
	if rp, _ := r.Get("demo/good"); rp.ContributeURL != "" {
		t.Fatalf("re-sync kept a non-http contributeURL: %q", rp.ContributeURL)
	}
}

func TestSeedFileDropsNonHTTPContributeURL(t *testing.T) {
	r, dir := newTestRegistry(t)
	seed := filepath.Join(dir, "seed.json")
	raw, _ := json.Marshal([]RepoProfile{
		{RepoID: "demo/repo", HiveID: "h", Owner: "alice", ContributeURL: "javascript:alert(1)"},
	})
	if err := os.WriteFile(seed, raw, 0o644); err != nil {
		t.Fatalf("writing seed: %v", err)
	}
	if err := r.LoadSeedFile(seed); err != nil {
		t.Fatalf("LoadSeedFile: %v", err)
	}
	if rp, _ := r.Get("demo/repo"); rp.ContributeURL != "" {
		t.Fatalf("non-http contributeURL accepted from seed file: %q", rp.ContributeURL)
	}
}

func TestOwnerMatchIsCaseInsensitive(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Merge(sampleRepos()); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := r.ListByOwner("ALICE"); len(got) != 1 || got[0].RepoID != "kubestellar/kubestellar" {
		t.Fatalf("ListByOwner(ALICE) = %+v", got)
	}
	on := true
	if _, err := r.ApplyOwnerUpdate("kubestellar/dibs", "Bob", OwnerUpdate{AcceptingIdeas: &on}); err != nil {
		t.Fatalf("ApplyOwnerUpdate mixed-case actor: %v", err)
	}
}

func TestEnrichmentBacksOffUntilRateLimitReset(t *testing.T) {
	hub := enrichHubServer(t, []RepoProfile{
		{RepoID: "o/a", HiveID: "h", Owner: "alice"},
		{RepoID: "o/b", HiveID: "h", Owner: "alice"},
	})
	defer hub.Close()
	var hits atomic.Int32
	reset := time.Now().Add(time.Hour).Unix()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer gh.Close()
	c := &HTTPHubClient{BaseURL: hub.URL, GitHubAPI: gh.URL}
	reg, _ := newTestRegistry(t)
	for i := 0; i < 3; i++ {
		if err := reg.Sync(context.Background(), c); err != nil {
			t.Fatalf("Sync #%d: %v", i+1, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("GitHub hits = %d, want 1 (later syncs must skip enrichment until the reset time)", got)
	}
	if !c.notBefore.Equal(time.Unix(reset, 0)) {
		t.Fatalf("notBefore = %v, want %v", c.notBefore, time.Unix(reset, 0))
	}
	c.enrichMu.Lock()
	looked := c.looked["o/a"]
	c.enrichMu.Unlock()
	if looked {
		t.Fatalf("rate-limited repo must not be marked as looked up, so it is retried after the reset")
	}
}

func TestEnrichmentRetryAfterBeatsReset(t *testing.T) {
	hub := enrichHubServer(t, []RepoProfile{{RepoID: "o/a", HiveID: "h", Owner: "alice"}})
	defer hub.Close()
	reset := time.Now().Add(time.Hour).Unix()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer gh.Close()
	c := &HTTPHubClient{BaseURL: hub.URL, GitHubAPI: gh.URL}
	reg, _ := newTestRegistry(t)
	before := time.Now()
	if err := reg.Sync(context.Background(), c); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	c.enrichMu.Lock()
	nb := c.notBefore
	c.enrichMu.Unlock()
	if nb.Before(before.Add(59*time.Second)) || nb.After(time.Now().Add(61*time.Second)) {
		t.Fatalf("notBefore = %v, want about 60s from now, not the hourly reset", nb)
	}
}

func TestEnrichmentResumesAfterRateLimitReset(t *testing.T) {
	hub := enrichHubServer(t, []RepoProfile{{RepoID: "o/a", HiveID: "h", Owner: "alice"}})
	defer hub.Close()
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"description": "after reset"})
	}))
	defer gh.Close()
	c := &HTTPHubClient{BaseURL: hub.URL, GitHubAPI: gh.URL}
	reg, _ := newTestRegistry(t)
	if err := reg.Sync(context.Background(), c); err != nil {
		t.Fatalf("Sync #1: %v", err)
	}
	// Let the observed reset elapse.
	c.enrichMu.Lock()
	c.notBefore = time.Now().Add(-time.Second)
	c.enrichMu.Unlock()
	if err := reg.Sync(context.Background(), c); err != nil {
		t.Fatalf("Sync #2: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("GitHub hits = %d, want 2 (an elapsed reset must not block the next sync)", got)
	}
	if p, _ := reg.Get("o/a"); p == nil || p.Description != "after reset" {
		t.Fatalf("description not enriched after reset: %+v", p)
	}
}

func TestEnrichmentRateLimitWithoutResetHeaderFallsBackToCooldown(t *testing.T) {
	hub := enrichHubServer(t, []RepoProfile{{RepoID: "o/a", HiveID: "h", Owner: "alice"}})
	defer hub.Close()
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-RateLimit-Reset", "soon")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer gh.Close()
	c := &HTTPHubClient{BaseURL: hub.URL, GitHubAPI: gh.URL}
	reg, _ := newTestRegistry(t)
	for i := 0; i < 2; i++ {
		if err := reg.Sync(context.Background(), c); err != nil {
			t.Fatalf("Sync #%d: %v", i+1, err)
		}
	}
	if time.Until(c.notBefore) < ghrate.Cooldown-time.Minute {
		t.Fatalf("notBefore = %v, want about ghrate.Cooldown from now", c.notBefore)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("GitHub hits = %d, want 1 (cooldown armed after the first refusal)", got)
	}
}
