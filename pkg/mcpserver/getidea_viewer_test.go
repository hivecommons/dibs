package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/deps"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// TestGetIdeaScopesMatchesAndPendingOffersToViewer pins the MCP get_idea
// tool to the same viewer rule as GET /api/ideas/{id}: only the author sees
// match state; pending offers surface only to the owner of the offered repo;
// decided offers are public record.
func TestGetIdeaScopesMatchesAndPendingOffersToViewer(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "org/owned-by-bob", HiveID: "h1", Owner: "Bob", AcceptingIdeas: true},
		{RepoID: "org/owned-by-dave", HiveID: "h2", Owner: "dave", AcceptingIdeas: true},
	}); err != nil {
		t.Fatalf("registry.Merge: %v", err)
	}
	tls := &tools{cfg: Config{
		Hub: &auth.FakeHub{BearerTokens: map[string]auth.Identity{
			"alice-token": {Username: "alice"},
			"bob-token":   {Username: "bob"},
			"carol-token": {Username: "carol"},
		}},
		Deps: deps.Deps{Store: st, Registry: reg},
	}}

	idea := &store.Idea{Author: "alice", AuthorDisplay: "Alice", Title: "Public", Body: "detail", Visibility: store.VisibilityPublic, Status: store.StatusOffered}
	if err := st.Create(idea); err != nil {
		t.Fatalf("Store.Create: %v", err)
	}
	decided := time.Now()
	idea.Matches = []store.Match{{RepoID: "org/owned-by-bob", Score: 0.9, Reason: "fit"}}
	idea.Offers = []store.Offer{
		{RepoID: "org/owned-by-bob", Status: store.OfferPending, CreatedAt: decided},
		{RepoID: "org/owned-by-dave", Status: store.OfferPending, CreatedAt: decided},
		{RepoID: "org/declined", Status: store.OfferDeclined, CreatedAt: decided, DecidedAt: &decided},
	}
	if err := st.Update(idea); err != nil {
		t.Fatalf("Store.Update: %v", err)
	}

	cases := []struct {
		name        string
		token       string
		wantMatches int
		wantOffers  []string
	}{
		{name: "anonymous", token: "", wantMatches: 0, wantOffers: []string{"org/declined"}},
		{name: "non-owner", token: "carol-token", wantMatches: 0, wantOffers: []string{"org/declined"}},
		{name: "repo owner casefold", token: "bob-token", wantMatches: 0, wantOffers: []string{"org/owned-by-bob", "org/declined"}},
		{name: "author", token: "alice-token", wantMatches: 1, wantOffers: []string{"org/owned-by-bob", "org/owned-by-dave", "org/declined"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, out, err := tls.getIdea(context.Background(), callReq(tc.token), getIdeaInput{ID: idea.ID})
			if err != nil {
				t.Fatalf("getIdea: %v", err)
			}
			if len(out.Matches) != tc.wantMatches {
				t.Fatalf("matches = %+v, want %d", out.Matches, tc.wantMatches)
			}
			if len(out.Offers) != len(tc.wantOffers) {
				t.Fatalf("offers = %+v, want %v", out.Offers, tc.wantOffers)
			}
			for i, want := range tc.wantOffers {
				if out.Offers[i].RepoID != want {
					t.Fatalf("offers[%d] = %q, want %q (all: %+v)", i, out.Offers[i].RepoID, want, out.Offers)
				}
			}
		})
	}
}
