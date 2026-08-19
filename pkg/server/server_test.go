package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubestellar/ideate/pkg/auth"
	"github.com/kubestellar/ideate/pkg/registry"
	"github.com/kubestellar/ideate/pkg/store"
)

// newTestServer wires the full handler with a fake hub. Sessions:
// "alice-session"→alice, "bob-session"→bob.
func newTestServer(t *testing.T, basePath string) http.Handler {
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
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "kubestellar/ideate", HiveID: "hive-ks", Owner: "alice"},
	}); err != nil {
		t.Fatalf("registry.Merge: %v", err)
	}
	return New(Config{
		BasePath: basePath,
		HubURL:   "https://hive.kubestellar.io",
		Hub: &auth.FakeHub{Sessions: map[string]auth.Identity{
			"alice-session": {Username: "alice", DisplayName: "Alice A"},
			"bob-session":   {Username: "bob", DisplayName: "Bob B"},
		}},
		Store:   st,
		Repos:   reg,
		Version: "test-hash",
	})
}

func doJSON(t *testing.T, h http.Handler, method, path, session string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if session != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return v
}

// TestBasePathRouting: every route must work at the default root base path
// AND under a configured prefix — and, when prefixed, NOT at the bare root.
func TestBasePathRouting(t *testing.T) {
	for _, configured := range []string{"/", "/ideas", "/some/other/prefix"} {
		t.Run(configured, func(t *testing.T) {
			h := newTestServer(t, configured)
			base := NormalizeBasePath(configured) // "" at root

			// Health (unauthenticated) with version.
			rec := doJSON(t, h, "GET", base+"/healthz", "", nil)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "test-hash") {
				t.Fatalf("healthz: %d %s", rec.Code, rec.Body.String())
			}

			// Bare base path (prefixed deployments only) redirects to the
			// canonical trailing-slash URL.
			if base != "" {
				rec = doJSON(t, h, "GET", base, "", nil)
				if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != base+"/" {
					t.Fatalf("bare base: %d loc=%q", rec.Code, rec.Header().Get("Location"))
				}
			}

			// UI page, authenticated.
			rec = doJSON(t, h, "GET", base+"/", "alice-session", nil)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Repos need them") {
				t.Fatalf("UI page: %d", rec.Code)
			}

			// UI page, unauthenticated → HTML interstitial.
			rec = doJSON(t, h, "GET", base+"/", "", nil)
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Sign in") {
				t.Fatalf("interstitial: %d %s", rec.Code, rec.Body.String())
			}

			// API round-trip under the prefix.
			rec = doJSON(t, h, "POST", base+"/api/ideas", "alice-session",
				map[string]string{"title": "T", "body": "B", "visibility": "public"})
			if rec.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			idea := decode[store.Idea](t, rec)
			rec = doJSON(t, h, "GET", base+"/api/ideas/"+idea.ID, "alice-session", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("get: %d", rec.Code)
			}

			// API unauthenticated → 401 JSON.
			rec = doJSON(t, h, "GET", base+"/api/ideas", "", nil)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unauth API: %d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
			}

			// With a prefix, nothing is served at the bare root.
			if base != "" {
				rec = doJSON(t, h, "GET", "/", "alice-session", nil)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("bare root should 404, got %d", rec.Code)
				}
			}
		})
	}
}

// TestPrivateIdeaInvariant is THE invariant: a private idea never appears in
// any listing (or fetch) other than its author's own.
func TestPrivateIdeaInvariant(t *testing.T) {
	h := newTestServer(t, "/ideas")

	rec := doJSON(t, h, "POST", "/ideas/api/ideas", "alice-session",
		map[string]string{"title": "Secret plan", "body": "shh", "visibility": "private", "status": "offered"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create private: %d %s", rec.Code, rec.Body.String())
	}
	private := decode[store.Idea](t, rec)

	rec = doJSON(t, h, "POST", "/ideas/api/ideas", "alice-session",
		map[string]string{"title": "Open plan", "body": "hi", "visibility": "public"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create public: %d", rec.Code)
	}

	// Bob's public listing: only the public idea.
	rec = doJSON(t, h, "GET", "/ideas/api/ideas?scope=public", "bob-session", nil)
	list := decode[[]store.Idea](t, rec)
	for _, i := range list {
		if i.ID == private.ID || i.Visibility == "private" {
			t.Fatalf("PRIVATE IDEA LEAKED to another user's listing: %+v", i)
		}
	}
	if len(list) != 1 {
		t.Fatalf("bob's public listing: want 1, got %d", len(list))
	}

	// Bob's "mine" listing: empty.
	rec = doJSON(t, h, "GET", "/ideas/api/ideas", "bob-session", nil)
	if l := decode[[]store.Idea](t, rec); len(l) != 0 {
		t.Fatalf("bob's own listing should be empty: %+v", l)
	}

	// Direct fetch by ID as bob: 404 (existence must not leak).
	rec = doJSON(t, h, "GET", "/ideas/api/ideas/"+private.ID, "bob-session", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob fetching private idea: want 404, got %d", rec.Code)
	}

	// Bob cannot edit or delete it either.
	rec = doJSON(t, h, "PUT", "/ideas/api/ideas/"+private.ID, "bob-session",
		map[string]string{"title": "hijack", "body": "x", "visibility": "public"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob editing private idea: want 404, got %d", rec.Code)
	}
	rec = doJSON(t, h, "DELETE", "/ideas/api/ideas/"+private.ID, "bob-session", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob deleting private idea: want 404, got %d", rec.Code)
	}

	// Alice still sees both of hers.
	rec = doJSON(t, h, "GET", "/ideas/api/ideas", "alice-session", nil)
	if l := decode[[]store.Idea](t, rec); len(l) != 2 {
		t.Fatalf("alice's listing: want 2, got %d", len(l))
	}
}

// TestPublicIdeaAuthorScoping: public ideas are readable by others but only
// writable by the author (403, not 404 — existence is public).
func TestPublicIdeaAuthorScoping(t *testing.T) {
	h := newTestServer(t, "/ideas")
	rec := doJSON(t, h, "POST", "/ideas/api/ideas", "alice-session",
		map[string]string{"title": "Open", "body": "b", "visibility": "public"})
	idea := decode[store.Idea](t, rec)

	if rec := doJSON(t, h, "GET", "/ideas/api/ideas/"+idea.ID, "bob-session", nil); rec.Code != http.StatusOK {
		t.Fatalf("bob reading public idea: %d", rec.Code)
	}
	rec = doJSON(t, h, "PUT", "/ideas/api/ideas/"+idea.ID, "bob-session",
		map[string]string{"title": "hijack", "body": "x", "visibility": "public"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob editing public idea: want 403, got %d", rec.Code)
	}
	if rec := doJSON(t, h, "DELETE", "/ideas/api/ideas/"+idea.ID, "bob-session", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("bob deleting public idea: want 403, got %d", rec.Code)
	}
}

// TestRegistryToggleAuthorization: only the repo owner can toggle intake.
func TestRegistryToggleAuthorization(t *testing.T) {
	h := newTestServer(t, "/ideas")

	// Non-owner (bob) cannot toggle alice's repo.
	rec := doJSON(t, h, "PUT", "/ideas/api/repos/kubestellar/ideate", "bob-session",
		map[string]any{"acceptingIdeas": true})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner toggle: want 403, got %d %s", rec.Code, rec.Body.String())
	}

	// Accepting list is still empty.
	rec = doJSON(t, h, "GET", "/ideas/api/repos", "bob-session", nil)
	if l := decode[[]registry.RepoProfile](t, rec); len(l) != 0 {
		t.Fatalf("accepting list should be empty after failed toggle: %+v", l)
	}

	// Owner toggles on with topics + appetite.
	rec = doJSON(t, h, "PUT", "/ideas/api/repos/kubestellar/ideate", "alice-session",
		map[string]any{"acceptingIdeas": true, "topics": []string{"ai"}, "appetite": "small features"})
	if rec.Code != http.StatusOK {
		t.Fatalf("owner toggle: %d %s", rec.Code, rec.Body.String())
	}
	rp := decode[registry.RepoProfile](t, rec)
	if !rp.AcceptingIdeas || rp.Appetite != "small features" {
		t.Fatalf("owner update not applied: %+v", rp)
	}

	// Everyone now sees it in the accepting list.
	rec = doJSON(t, h, "GET", "/ideas/api/repos", "bob-session", nil)
	if l := decode[[]registry.RepoProfile](t, rec); len(l) != 1 || l[0].RepoID != "kubestellar/ideate" {
		t.Fatalf("accepting list: %+v", l)
	}
}

// TestValidationOverAPI: bad payloads are rejected with 400s.
func TestValidationOverAPI(t *testing.T) {
	h := newTestServer(t, "/ideas")
	cases := []map[string]string{
		{"title": "", "body": "b", "visibility": "public"},
		{"title": "t", "body": "", "visibility": "public"},
		{"title": "t", "body": "b", "visibility": "secret"},
		{"title": "t", "body": "b", "visibility": "public", "status": "implemented"},
	}
	for i, c := range cases {
		rec := doJSON(t, h, "POST", "/ideas/api/ideas", "alice-session", c)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("case %d: want 400, got %d %s", i, rec.Code, rec.Body.String())
		}
	}
	// Oversized body → 400/413, never 201.
	rec := doJSON(t, h, "POST", "/ideas/api/ideas", "alice-session",
		map[string]string{"title": "t", "body": strings.Repeat("x", store.MaxBodyBytes+1), "visibility": "public"})
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: want 400/413, got %d", rec.Code)
	}
}

func TestNormalizeBasePath(t *testing.T) {
	for in, want := range map[string]string{
		"":        "", // root
		"/":       "", // root
		"/ideas":  "/ideas",
		"/ideas/": "/ideas",
		"ideas":   "/ideas",
		"/x/y/":   "/x/y",
	} {
		if got := NormalizeBasePath(in); got != want {
			t.Errorf("NormalizeBasePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMeEndpoint: identity round-trips for the UI header.
func TestMeEndpoint(t *testing.T) {
	h := newTestServer(t, "/ideas")
	rec := doJSON(t, h, "GET", "/ideas/api/me", "alice-session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}
	me := decode[auth.Identity](t, rec)
	if me.Username != "alice" || me.DisplayName != "Alice A" {
		t.Fatalf("me: %+v", me)
	}
}
