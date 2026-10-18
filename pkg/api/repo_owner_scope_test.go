package api

import (
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/store"
)

func seedForeignState(t *testing.T, a *API, id string, offers ...store.Offer) {
	t.Helper()
	if _, err := a.Store.Mutate(id, false, func(i *store.Idea) error {
		i.Matches = []store.Match{{RepoID: "match/repo", Score: 3}}
		i.CNCFMatches = []store.CNCFMatch{{Name: "cncfproj"}}
		i.PassedRepos = []string{"passed/repo"}
		i.Offers = offers
		return nil
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
}

func assertNoForeignState(t *testing.T, what, body string) {
	t.Helper()
	for _, leak := range []string{"match/repo", "cncfproj", "passed/repo", "org/other"} {
		if strings.Contains(body, leak) {
			t.Fatalf("%s leaked %q to the repo owner: %s", what, leak, body)
		}
	}
}

func TestRepoFeedScopesIdeasToRepoOwner(t *testing.T) {
	a, mux := newAPIFixture(t)
	now := time.Now().UTC()
	offered := mustCreate(t, a, "bob", "Offered plan", store.VisibilityPublic, store.StatusOffered)
	seedForeignState(t, a, offered.ID,
		store.Offer{RepoID: "kubestellar/dibs", Status: store.OfferPending, CreatedAt: now},
		store.Offer{RepoID: "org/other", Status: store.OfferPending, CreatedAt: now},
		store.Offer{RepoID: "done/repo", Status: store.OfferDeclined, CreatedAt: now})
	candidate := mustCreate(t, a, "bob", "Candidate plan", store.VisibilityPublic, store.StatusOffered)
	seedForeignState(t, a, candidate.ID,
		store.Offer{RepoID: "org/other", Status: store.OfferPending, CreatedAt: now},
		store.Offer{RepoID: "done/repo", Status: store.OfferDeclined, CreatedAt: now})

	rec := do(t, mux, ident("alice"), "GET", "/api/repos/kubestellar/dibs/feed", "")
	if rec.Code != 200 {
		t.Fatalf("feed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoForeignState(t, "feed", body)
	if strings.Count(body, "done/repo") != 2 {
		t.Fatalf("feed dropped decided offers: %s", body)
	}
	if !strings.Contains(body, "kubestellar/dibs") || !strings.Contains(body, offered.ID) || !strings.Contains(body, candidate.ID) {
		t.Fatalf("feed lost the viewer's own offer or ideas: %s", body)
	}
}

func TestDecideResponseScopesIdeaToRepoOwner(t *testing.T) {
	a, mux := newAPIFixture(t)
	now := time.Now().UTC()
	idea := mustCreate(t, a, "bob", "Decide plan", store.VisibilityPublic, store.StatusOffered)
	seedForeignState(t, a, idea.ID,
		store.Offer{RepoID: "kubestellar/dibs", Status: store.OfferPending, CreatedAt: now},
		store.Offer{RepoID: "org/other", Status: store.OfferPending, CreatedAt: now})

	rec := do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"`+idea.ID+`","decision":"decline"}`)
	if rec.Code != 200 {
		t.Fatalf("decline: status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertNoForeignState(t, "decline response", rec.Body.String())
	if !strings.Contains(rec.Body.String(), "kubestellar/dibs") {
		t.Fatalf("decline response lost the decided offer: %s", rec.Body.String())
	}
}
