package match

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/kubestellar/dibs/pkg/catalog"
	"github.com/kubestellar/dibs/pkg/registry"
	"github.com/kubestellar/dibs/pkg/store"
)

func newFixtures(t *testing.T) (*store.Store, *registry.Registry) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return st, reg
}

func createIdea(t *testing.T, st *store.Store, title, body string) *store.Idea {
	t.Helper()
	idea := &store.Idea{Author: "alice", AuthorDisplay: "Alice", Title: title, Body: body, Visibility: store.VisibilityPublic}
	if err := st.Create(idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return idea
}

// TestFallbackScoreDeterministic: same inputs, same output; overlap scores
// higher than none.
func TestFallbackScoreDeterministic(t *testing.T) {
	idea := &store.Idea{Title: "Kubernetes multicluster scheduling", Body: "Schedule workloads across clusters with placement policies."}
	fit := &registry.RepoProfile{RepoID: "kubestellar/kubestellar", Description: "multicluster workload orchestration",
		Topics: []string{"kubernetes", "multicluster"}, Appetite: "scheduling and placement ideas"}
	misfit := &registry.RepoProfile{RepoID: "acme/website", Description: "marketing site", Topics: []string{"css", "design"}}

	s1, r1 := FallbackScore(idea, fit)
	s2, r2 := FallbackScore(idea, fit)
	if s1 != s2 || r1 != r2 {
		t.Fatalf("fallback must be deterministic: (%v,%q) vs (%v,%q)", s1, r1, s2, r2)
	}
	sm, _ := FallbackScore(idea, misfit)
	if s1 <= sm {
		t.Fatalf("fit repo must outscore misfit: %v <= %v", s1, sm)
	}
	if sm != 0 {
		t.Fatalf("zero-overlap repo should score 0, got %v", sm)
	}
	if s1 <= 0 || s1 > 100 {
		t.Fatalf("score out of range: %v", s1)
	}
}

func TestHiveBM25RanksNewsletterRepoFromRepoID(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "kellyaa/agent-newsletter", HiveID: "hive-news", Owner: "kelly", AcceptingIdeas: true},
		{RepoID: "example/kubernetes-operator", HiveID: "hive-k8s", Owner: "alice", AcceptingIdeas: true,
			Description: "Kubernetes operators and cluster automation", Topics: []string{"kubernetes", "operator"}},
		{RepoID: "example/web-dashboard", HiveID: "hive-web", Owner: "bob", AcceptingIdeas: true,
			Description: "Web dashboard for metrics", Topics: []string{"dashboard", "metrics"}},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	idea := createIdea(t, st, "Daily 5-minute audio newsletter",
		"Create a daily five minute audio newsletter for agents to summarize important updates.")
	e := &Engine{Store: st, Registry: reg}

	_, hive, _, err := e.RematchIdea(context.Background(), idea, false, nil)
	if err != nil {
		t.Fatalf("RematchIdea: %v", err)
	}
	if len(hive) == 0 {
		t.Fatal("expected hive matches")
	}
	if hive[0].RepoID != "kellyaa/agent-newsletter" {
		t.Fatalf("top hive match = %s, want kellyaa/agent-newsletter: %+v", hive[0].RepoID, hive)
	}
}

func TestFallbackTLDR(t *testing.T) {
	idea := &store.Idea{Title: "T", Body: "First paragraph  with   spaces.\n\nSecond paragraph is ignored."}
	if got := FallbackTLDR(idea); got != "First paragraph with spaces." {
		t.Fatalf("FallbackTLDR = %q", got)
	}
}

// fakeGateway is an OpenAI-compatible stub counting calls.
func fakeGateway(t *testing.T, reply func(user string) string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		user := ""
		if len(req.Messages) > 1 {
			user = req.Messages[1].Content
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply(user)}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestEngineLLMScoringAndCache: LLM scores are used, cached (no second call),
// and invalidated when the repo profile changes.
func TestEngineLLMScoringAndCache(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/a", Owner: "bob"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if _, err := reg.ApplyOwnerUpdate("org/a", "bob", registry.OwnerUpdate{AcceptingIdeas: boolPtr(true)}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	srv, calls := fakeGateway(t, func(string) string { return `{"score": 77, "reason": "great fit"}` })
	e := &Engine{Store: st, Registry: reg, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}

	idea := createIdea(t, st, "T", "B body content")
	ms, err := e.MatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("MatchesForIdea: %v", err)
	}
	if len(ms) != 1 || ms[0].Score != 77 || ms[0].Reason != "great fit" || !ms[0].ByLLM {
		t.Fatalf("llm match: %+v", ms)
	}
	before := calls.Load()

	// Second call: fully cached, zero gateway traffic.
	idea, _ = st.Get(idea.ID)
	if _, err := e.MatchesForIdea(context.Background(), idea); err != nil {
		t.Fatalf("MatchesForIdea (cached): %v", err)
	}
	if calls.Load() != before {
		t.Fatalf("cached matches must not hit the gateway (calls %d → %d)", before, calls.Load())
	}

	// Repo profile edit invalidates via RepoHash.
	if _, err := reg.ApplyOwnerUpdate("org/a", "bob", registry.OwnerUpdate{Appetite: strPtr("new appetite")}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	idea, _ = st.Get(idea.ID)
	if _, err := e.MatchesForIdea(context.Background(), idea); err != nil {
		t.Fatalf("MatchesForIdea (invalidated): %v", err)
	}
	if calls.Load() == before {
		t.Fatal("repo edit must invalidate the cached score")
	}
}

// TestEngineFallbackWhenGatewayDown: a dead gateway degrades to the
// deterministic scorer, never an error.
func TestEngineFallbackWhenGatewayDown(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/a", Owner: "bob", Topics: []string{"kubernetes"}}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if _, err := reg.ApplyOwnerUpdate("org/a", "bob", registry.OwnerUpdate{AcceptingIdeas: boolPtr(true)}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	e := &Engine{Store: st, Registry: reg, LLM: &LLM{BaseURL: "http://127.0.0.1:1", Model: "test"}}
	idea := createIdea(t, st, "Kubernetes thing", "All about kubernetes controllers")
	ms, err := e.MatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("MatchesForIdea with dead gateway: %v", err)
	}
	if len(ms) != 1 || ms[0].ByLLM || ms[0].Score <= 0 {
		t.Fatalf("expected fallback score: %+v", ms)
	}
	tldr, err := e.EnsureTLDR(context.Background(), idea)
	if err != nil || tldr == "" {
		t.Fatalf("EnsureTLDR fallback: %q, %v", tldr, err)
	}
}

// TestEnsureTLDRCached: the LLM is asked once; the TLDR persists.
func TestEnsureTLDRCached(t *testing.T) {
	st, reg := newFixtures(t)
	srv, calls := fakeGateway(t, func(string) string { return "A punchy TLDR." })
	e := &Engine{Store: st, Registry: reg, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
	idea := createIdea(t, st, "T", "B body")
	for i := 0; i < 3; i++ {
		idea, _ = st.Get(idea.ID)
		tldr, err := e.EnsureTLDR(context.Background(), idea)
		if err != nil || tldr != "A punchy TLDR." {
			t.Fatalf("EnsureTLDR #%d: %q, %v", i, tldr, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("TLDR must be generated exactly once, got %d calls", calls.Load())
	}
}

// TestMatchesExcludePassedAndOffered: swiped-away and already-offered repos
// never resurface as candidates.
func TestMatchesExcludePassedAndOffered(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "org/passed", Owner: "bob"},
		{RepoID: "org/offered", Owner: "bob"},
		{RepoID: "org/fresh", Owner: "bob"},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	for _, r := range []string{"org/passed", "org/offered", "org/fresh"} {
		if _, err := reg.ApplyOwnerUpdate(r, "bob", registry.OwnerUpdate{AcceptingIdeas: boolPtr(true)}); err != nil {
			t.Fatalf("ApplyOwnerUpdate: %v", err)
		}
	}
	e := &Engine{Store: st, Registry: reg}
	idea := createIdea(t, st, "T", "B body")
	if _, err := st.Mutate(idea.ID, false, func(i *store.Idea) error {
		i.PassedRepos = []string{"org/passed"}
		i.Offers = []store.Offer{{RepoID: "org/offered", Status: store.OfferPending}}
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	idea, _ = st.Get(idea.ID)
	ms, err := e.MatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("MatchesForIdea: %v", err)
	}
	if len(ms) != 1 || ms[0].RepoID != "org/fresh" {
		t.Fatalf("want only org/fresh, got %+v", ms)
	}
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func TestCNCFMatchesFallbackTopK(t *testing.T) {
	dir := t.TempDir()
	projects := []catalog.Project{
		{Name: "Istio", RepoID: "istio/istio", RepoURL: "https://github.com/istio/istio", Maturity: "graduated", Category: "Service Proxy", Description: "Service mesh sidecar proxy", Topics: []string{"service-mesh"}},
		{Name: "Vitess", RepoID: "vitessio/vitess", RepoURL: "https://github.com/vitessio/vitess", Maturity: "incubating", Category: "Database", Description: "MySQL clustering database"},
	}
	raw, err := json.Marshal(projects)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(dir+"/cncf-catalog.json", raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	catStore, err := catalog.New(dir, "")
	if err != nil {
		t.Fatalf("catalog.New: %v", err)
	}
	e := &Engine{Catalog: catStore}
	idea := &store.Idea{Title: "Service mesh sidecar proxy", Body: "Route traffic through sidecars."}
	ms, err := e.CNCFMatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("CNCFMatchesForIdea: %v", err)
	}
	if len(ms) != 2 || ms[0].RepoID != "istio/istio" || ms[0].Maturity != "graduated" || ms[0].ByLLM {
		t.Fatalf("cncf matches = %+v", ms)
	}
}

func TestCNCFMatchesLLMCappedAtTop15(t *testing.T) {
	dir := t.TempDir()
	projects := make([]catalog.Project, 20)
	for i := range projects {
		projects[i] = catalog.Project{Name: fmt.Sprintf("Project %02d", i), RepoID: fmt.Sprintf("org/repo%02d", i), RepoURL: fmt.Sprintf("https://github.com/org/repo%02d", i), Description: "service mesh proxy"}
	}
	raw, err := json.Marshal(projects)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(dir+"/cncf-catalog.json", raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	catStore, err := catalog.New(dir, "")
	if err != nil {
		t.Fatalf("catalog.New: %v", err)
	}
	srv, calls := fakeGateway(t, func(string) string { return `{"score": 42, "reason": "fits"}` })
	e := &Engine{Catalog: catStore, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
	idea := &store.Idea{ID: "idea", Title: "service mesh proxy", Body: "sidecars"}
	ms, err := e.CNCFMatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("CNCFMatchesForIdea: %v", err)
	}
	if len(ms) != MaxCNCFCandidates || calls.Load() != int64(MaxCNCFCandidates) {
		t.Fatalf("matches=%d calls=%d, want %d", len(ms), calls.Load(), MaxCNCFCandidates)
	}
}
