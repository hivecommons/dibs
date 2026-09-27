package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/store"
)

// seedCatalog writes a cncf-catalog.json into dir and opens a catalog store
// over it. One project deliberately shares its RepoID with a hive-managed
// registry repo so the handler's "keep CNCF section non-hive only" filter
// has something to drop.
func seedCatalog(t *testing.T, dir string) *catalog.Store {
	t.Helper()
	projects := []catalog.Project{
		{Name: "Dibs", RepoID: "kubestellar/dibs", RepoURL: "https://github.com/kubestellar/dibs",
			Maturity: "sandbox", Category: "Exchange", Description: "ideas exchange kubernetes"},
		{Name: "Istio", RepoID: "istio/istio", RepoURL: "https://github.com/istio/istio",
			Maturity: "graduated", Category: "Service Mesh", Description: "ideas exchange kubernetes mesh"},
	}
	raw, err := json.Marshal(projects)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(dir+"/cncf-catalog.json", raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cs, err := catalog.New(dir, "")
	if err != nil {
		t.Fatalf("catalog.New: %v", err)
	}
	return cs
}

func TestIdeaMatchesWithoutEngineIs503(t *testing.T) {
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "bob", "Sched idea", store.VisibilityPrivate, store.StatusDraft)
	rec := do(t, mux, ident("bob"), "GET", "/api/ideas/"+idea.ID+"/matches", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("matches without engine: status=%d, want 503", rec.Code)
	}
}

func TestIdeaMatchesFallbackScoringAndCNCFFilter(t *testing.T) {
	a, mux := newAPIFixture(t)
	a.Engine = &match.Engine{Store: a.Store, Registry: a.Registry, Catalog: seedCatalog(t, t.TempDir())}

	idea := mustCreate(t, a, "bob", "Kubernetes ideas exchange", store.VisibilityPrivate, store.StatusDraft)
	rec := do(t, mux, ident("bob"), "GET", "/api/ideas/"+idea.ID+"/matches", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("matches: status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeBody[struct {
		TLDR    string            `json:"tldr"`
		Matches []matchView       `json:"matches"`
		CNCF    []store.CNCFMatch `json:"cncf"`
	}](t, rec)
	if out.TLDR == "" {
		t.Fatal("matches response has empty tldr; want fallback TLDR")
	}
	// The registry holds two repos, but the ideator feed is capped at
	// maxIdeaHiveMatches (1) and returns full repo profiles.
	if len(out.Matches) != maxIdeaHiveMatches {
		t.Fatalf("hive matches = %+v, want %d entry", out.Matches, maxIdeaHiveMatches)
	}
	if out.Matches[0].Repo == nil || out.Matches[0].Repo.RepoID == "" {
		t.Fatalf("hive match missing repo profile: %+v", out.Matches[0])
	}
	// kubestellar/dibs is hive-managed, so only istio/istio may appear in
	// the CNCF companion section.
	for _, m := range out.CNCF {
		if m.RepoID == "kubestellar/dibs" {
			t.Fatalf("cncf section includes hive-managed repo: %+v", out.CNCF)
		}
	}
	if len(out.CNCF) != 1 || out.CNCF[0].RepoID != "istio/istio" {
		t.Fatalf("cncf matches = %+v, want exactly istio/istio", out.CNCF)
	}

	// The scored matches were persisted on the idea.
	stored, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.Matches) == 0 || stored.MatchesUpdatedAt.IsZero() {
		t.Fatalf("stored idea matches = %+v (updated %v), want persisted scores", stored.Matches, stored.MatchesUpdatedAt)
	}
	if stored.TLDR == "" {
		t.Fatal("stored idea TLDR empty; want persisted fallback TLDR")
	}
}

func TestIdeaMatchesSkipsPassedAndOfferedRepos(t *testing.T) {
	a, mux := newAPIFixture(t)
	a.Engine = &match.Engine{Store: a.Store, Registry: a.Registry}

	idea := mustCreate(t, a, "bob", "Kubernetes ideas exchange", store.VisibilityPrivate, store.StatusDraft)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error {
		i.PassedRepos = append(i.PassedRepos, "kubestellar/dibs", "org/other")
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	rec := do(t, mux, ident("bob"), "GET", "/api/ideas/"+idea.ID+"/matches", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("matches: status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeBody[struct {
		Matches []matchView       `json:"matches"`
		CNCF    []store.CNCFMatch `json:"cncf"`
	}](t, rec)
	if len(out.Matches) != 0 {
		t.Fatalf("matches = %+v, want none after passing on every repo", out.Matches)
	}
	if len(out.CNCF) != 0 {
		t.Fatalf("cncf = %+v, want empty with nil catalog", out.CNCF)
	}
}
