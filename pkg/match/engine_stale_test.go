package match

// Lazy cache writers must not persist results computed from a snapshot that
// an edit has since invalidated.

import (
	"context"
	"testing"

	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// editBody edits the stored idea's body (clearing its caches) and leaves the
// caller's snapshot untouched.
func editBody(t *testing.T, st *store.Store, snapshot *store.Idea) {
	t.Helper()
	edited := *snapshot
	edited.Body = "A completely different body"
	if err := st.Update(&edited); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func TestEnsureTLDRSkipsStaleSnapshot(t *testing.T) {
	st, reg := newFixtures(t)
	e := &Engine{Store: st, Registry: reg}
	idea := createIdea(t, st, "T", "B body")
	editBody(t, st, idea)

	tldr, err := e.EnsureTLDR(context.Background(), idea)
	if err != nil {
		t.Fatalf("EnsureTLDR: %v", err)
	}
	if tldr == "" {
		t.Fatalf("caller must still receive the computed TLDR")
	}
	if got, _ := st.Get(idea.ID); got.TLDR != "" {
		t.Fatalf("stale TLDR persisted: %q", got.TLDR)
	}
}

func TestMatchesForIdeaSkipsStaleSnapshot(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/feed", Owner: "bob"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	e := &Engine{Store: st, Registry: reg}
	idea := createIdea(t, st, "T", "B body")
	editBody(t, st, idea)

	ms, err := e.MatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("MatchesForIdea: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("caller must still receive computed matches, got %+v", ms)
	}
	got, _ := st.Get(idea.ID)
	if len(got.Matches) != 0 || !got.MatchesUpdatedAt.IsZero() {
		t.Fatalf("stale matches persisted: %+v", got.Matches)
	}
}

func TestScoreForRepoSkipsStaleSnapshot(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/feed", Owner: "bob"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	e := &Engine{Store: st, Registry: reg}
	idea := createIdea(t, st, "T", "B body")
	rp, err := reg.Get("org/feed")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	editBody(t, st, idea)

	m, err := e.ScoreForRepo(context.Background(), idea, rp)
	if err != nil {
		t.Fatalf("ScoreForRepo: %v", err)
	}
	if m.RepoID != "org/feed" {
		t.Fatalf("caller must still receive the computed match: %+v", m)
	}
	got, _ := st.Get(idea.ID)
	if len(got.Matches) != 0 || !got.MatchesUpdatedAt.IsZero() {
		t.Fatalf("stale match persisted: %+v", got.Matches)
	}
}

func TestCNCFMatchesSkipsStaleSnapshot(t *testing.T) {
	cs := cncfTestCatalog(t, []catalog.Project{
		{Name: "Istio", RepoID: "istio/istio", RepoURL: "https://github.com/istio/istio", Maturity: "graduated", Category: "Service Proxy", Description: "Service mesh sidecar proxy"},
	})
	st, _ := newFixtures(t)
	e := &Engine{Catalog: cs, Store: st}
	idea := createIdea(t, st, "Service mesh sidecar proxy", "Route traffic through sidecars.")
	editBody(t, st, idea)

	ms, err := e.CNCFMatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("CNCFMatchesForIdea: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("caller must still receive computed matches, got %+v", ms)
	}
	got, _ := st.Get(idea.ID)
	if len(got.CNCFMatches) != 0 || got.CNCFCatalogHash != "" {
		t.Fatalf("stale CNCF matches persisted: %+v", got.CNCFMatches)
	}
}
