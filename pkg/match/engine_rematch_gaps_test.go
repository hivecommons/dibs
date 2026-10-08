// Branch coverage for the rematch pipeline's cheaper arms: camel-case
// splitting in the hive corpus name, the cached-TLDR progress event, the
// passed/offered candidate skip, and the MaxMatches cap on persisted CNCF
// matches.
package match

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

func TestSplitCamel(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"lower":           "lower",
		"UPPER":           "UPPER",
		"kubeStellar":     "kube Stellar",
		"agentNewsLetter": "agent News Letter",
		"KubeStellar":     "Kube Stellar",
		"kube-stellar":    "kube-stellar",
		"x9Y":             "x9Y", // digit resets the lower-case run
		"httpServer2Fast": "http Server2Fast",
		"org/myRepoName":  "org/my Repo Name",
	}
	for in, want := range cases {
		if got := splitCamel(in); got != want {
			t.Errorf("splitCamel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHiveRepoCorpusNameExposesCamelWords: the corpus keeps the raw repo id
// and adds a tokenised copy so "myRepo" is searchable as "my" and "repo".
func TestHiveRepoCorpusNameExposesCamelWords(t *testing.T) {
	got := hiveRepoCorpusName("Org/myRepo-name_x")
	if !strings.HasPrefix(got, "Org/myRepo-name_x ") {
		t.Fatalf("corpus name must start with the raw id: %q", got)
	}
	for _, w := range []string{" Org ", " my ", " Repo ", " name ", " x"} {
		if !strings.Contains(got+" ", w) {
			t.Errorf("corpus %q lacks word %q", got, strings.TrimSpace(w))
		}
	}
}

// TestRematchIdeaCachedTLDRReportsProgress: with a TLDR already on the idea
// and a progress sink attached, the pipeline emits a single tldr_done event
// naming the cache hit instead of calling the LLM.
func TestRematchIdeaCachedTLDRReportsProgress(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "example/kubernetes-operator", HiveID: "hive-k8s", Owner: "alice",
			Description: "Kubernetes operators", Topics: []string{"kubernetes"}},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	idea := createIdea(t, st, "kubernetes operator", "cluster automation")
	idea.TLDR = "Already summarised."
	e := &Engine{Store: st, Registry: reg}

	var events []ProgressEvent
	tldr, _, _, err := e.RematchIdea(context.Background(), idea, false, func(ev ProgressEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("RematchIdea: %v", err)
	}
	if tldr != "Already summarised." {
		t.Fatalf("tldr = %q, want cached value", tldr)
	}
	if len(events) == 0 || events[0].Phase != "tldr_done" || events[0].Note != "Using cached TLDR" {
		t.Fatalf("first event = %+v, want tldr_done/Using cached TLDR", events)
	}
	for _, ev := range events {
		if ev.Phase == "tldr_start" {
			t.Fatalf("tldr_start emitted despite cached TLDR: %+v", events)
		}
	}
}

// TestRematchIdeaSkipsPassedAndOfferedRepos: repos the ideator swiped away
// and repos that already hold an offer are not re-scored as candidates.
func TestRematchIdeaSkipsPassedAndOfferedRepos(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "example/passed", HiveID: "h1", Owner: "a", Description: "kubernetes operator", Topics: []string{"kubernetes"}},
		{RepoID: "example/offered", HiveID: "h2", Owner: "b", Description: "kubernetes operator", Topics: []string{"kubernetes"}},
		{RepoID: "example/fresh", HiveID: "h3", Owner: "c", Description: "kubernetes operator", Topics: []string{"kubernetes"}},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	idea := createIdea(t, st, "kubernetes operator", "cluster automation")
	idea.TLDR = "cached"
	idea.PassedRepos = []string{"example/passed"}
	idea.Offers = []store.Offer{{RepoID: "example/offered", Status: store.OfferPending}}
	e := &Engine{Store: st, Registry: reg}

	var hiveStart *ProgressEvent
	_, hive, _, err := e.RematchIdea(context.Background(), idea, false, func(ev ProgressEvent) {
		if ev.Phase == "hive_start" {
			cp := ev
			hiveStart = &cp
		}
	})
	if err != nil {
		t.Fatalf("RematchIdea: %v", err)
	}
	if hiveStart == nil || hiveStart.Total != 1 {
		t.Fatalf("hive_start = %+v, want Total 1 (only example/fresh)", hiveStart)
	}
	if len(hive) != 1 || hive[0].RepoID != "example/fresh" {
		t.Fatalf("hive matches = %+v, want only example/fresh", hive)
	}
}

// TestCNCFMatchesPersistCapsAtMaxMatches: the caller receives every scored
// candidate, but the store copy is trimmed to the MaxMatches best.
func TestCNCFMatchesPersistCapsAtMaxMatches(t *testing.T) {
	n := MaxMatches + 2
	if n > MaxCNCFCandidates {
		t.Fatalf("test needs MaxMatches+2 (%d) <= MaxCNCFCandidates (%d)", n, MaxCNCFCandidates)
	}
	projects := make([]catalog.Project, 0, n)
	for i := 0; i < n; i++ {
		projects = append(projects, catalog.Project{
			Name:        fmt.Sprintf("Mesh%d", i),
			RepoID:      fmt.Sprintf("cncf/mesh-%d", i),
			RepoURL:     fmt.Sprintf("https://github.com/cncf/mesh-%d", i),
			Maturity:    "sandbox",
			Category:    "Service Mesh",
			Description: strings.Repeat("service mesh sidecar proxy ", i+1),
		})
	}
	cs := cncfTestCatalog(t, projects)
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	idea := &store.Idea{Title: "Service mesh sidecar proxy", Body: "Route traffic through sidecars.", Author: "u", Visibility: store.VisibilityPublic}
	if err := st.Create(idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	e := &Engine{Catalog: cs, Store: st}

	ms, err := e.cncfMatchesForIdea(context.Background(), idea, true, nil)
	if err != nil {
		t.Fatalf("cncfMatchesForIdea: %v", err)
	}
	if len(ms) != n {
		t.Fatalf("returned %d matches, want all %d candidates", len(ms), n)
	}
	got, err := st.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.CNCFMatches) != MaxMatches {
		t.Fatalf("persisted %d CNCF matches, want MaxMatches=%d", len(got.CNCFMatches), MaxMatches)
	}
	for i := range got.CNCFMatches {
		if got.CNCFMatches[i].RepoID != ms[i].RepoID {
			t.Fatalf("persisted[%d] = %s, want top-ranked %s", i, got.CNCFMatches[i].RepoID, ms[i].RepoID)
		}
	}
}
