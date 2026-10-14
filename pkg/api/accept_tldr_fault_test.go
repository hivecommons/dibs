package api

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// TestAcceptToleratesTLDRFailure pins that a failing TLDR generation after
// the accept Mutate is logged, not surfaced: the decision already landed, so
// the ideator still gets a 200 and the idea stays accepted for the repo.
func TestAcceptToleratesTLDRFailure(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	a.Engine = &faultMatcher{tldrErr: errBoom}
	idea := mustCreate(t, a, "bob", "TLDR fault", store.VisibilityPublic, store.StatusOffered)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error {
		i.Offers = []store.Offer{{RepoID: "kubestellar/dibs", Status: store.OfferPending, CreatedAt: now}}
		return nil
	}); err != nil {
		t.Fatalf("seed offer: %v", err)
	}

	rec := do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide",
		`{"ideaID":"`+idea.ID+`","decision":"accept"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept with failing TLDR: %d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != store.StatusAccepted || stored.TargetRepo != "kubestellar/dibs" {
		t.Fatalf("idea after accept = %+v, want accepted by kubestellar/dibs", stored)
	}
}

// TestMatchNotifierToleratesPersistFailure pins that MatchNotifier.NewMatch
// only logs when the notification store cannot persist: a fresh-match event
// from the engine must never panic or abort the matcher.
func TestMatchNotifierToleratesPersistFailure(t *testing.T) {
	dir := t.TempDir()
	ns, err := notify.New(dir)
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	// Removing the data dir makes every subsequent Add fail at the temp-file
	// step, without relying on file modes (root ignores them).
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	n := &MatchNotifier{Notify: ns}
	repo := &registry.RepoProfile{RepoID: "org/repo", Owner: "olivia"}
	idea := &store.Idea{ID: "i1", Title: "Unpersistable", Visibility: store.VisibilityPublic}
	n.NewMatch("alice", "olivia", idea, repo, 0.9)
}
