package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/registry"
)

// The fakehub stands in for the hive hub during local development, so its
// value is entirely in matching what Dibs' production hub clients decode.
// These tests drive it through those clients rather than asserting raw JSON.

func TestWhoAmI_ResolvesCookieThroughAuthClient(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	c := &auth.HTTPHubClient{BaseURL: srv.URL, Client: srv.Client()}
	id, err := c.WhoAmI(context.Background(), "alice")
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	want := auth.Identity{
		Username:    "alice",
		DisplayName: "Dev alice",
		Email:       "alice@example.com",
		AvatarURL:   "https://github.com/alice.png",
	}
	if *id != want {
		t.Fatalf("identity = %+v, want %+v", *id, want)
	}
}

func TestWhoAmI_MissingOrEmptyCookieIsUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	// No cookie at all.
	resp, err := srv.Client().Get(srv.URL + auth.WhoAmIPath)
	if err != nil {
		t.Fatalf("GET whoami: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no cookie: status = %d, want 401", resp.StatusCode)
	}

	// Empty cookie value must be rejected the same way, and the production
	// client must map that to ErrUnauthenticated.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+auth.WhoAmIPath, nil)
	req.AddCookie(&http.Cookie{Name: "hive_hub_user", Value: ""})
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET whoami (empty cookie): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty cookie: status = %d, want 401", resp.StatusCode)
	}

	c := &auth.HTTPHubClient{BaseURL: srv.URL, Client: srv.Client()}
	if _, err := c.WhoAmI(context.Background(), ""); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("WhoAmI(\"\") err = %v, want ErrUnauthenticated", err)
	}
}

func TestWhoAmI_RejectsNonGET(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+auth.WhoAmIPath, "application/json", nil)
	if err != nil {
		t.Fatalf("POST whoami: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestRepos_DecodeThroughRegistryClient(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	// Point GitHubAPI at the fakehub too: every seeded repo carries a
	// description, so enrichDescriptions must make no outbound call. If a
	// future seed drops its description the lookup lands on fakehub (404),
	// not on api.github.com, keeping the test hermetic either way.
	c := &registry.HTTPHubClient{BaseURL: srv.URL, Client: srv.Client(), GitHubAPI: srv.URL}
	repos, err := c.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2: %+v", len(repos), repos)
	}
	for _, rp := range repos {
		if rp.RepoID == "" || rp.HiveID == "" || rp.Owner == "" || rp.Description == "" {
			t.Errorf("repo %+v is missing a hub-fed field (repoID/hiveID/owner/description)", rp)
		}
	}
	if repos[0].RepoID != "kubestellar/kubestellar" || repos[1].RepoID != "kubestellar/dibs" {
		t.Fatalf("unexpected repo order/IDs: %q, %q", repos[0].RepoID, repos[1].RepoID)
	}
	if repos[0].HiveID != "hive-ks" || repos[0].Owner != "dev" {
		t.Fatalf("repo[0] = %+v, want hiveID=hive-ks owner=dev", repos[0])
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/api/saas/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
