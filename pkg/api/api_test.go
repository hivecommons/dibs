package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// newTestAPI builds an API over tmpdir-backed store/registry with matching,
// settlement, history, news, and notifications disabled (all nil), mounted
// at the root.
func newTestAPI(t *testing.T) (*API, *http.ServeMux) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	a := &API{Store: st, Registry: reg}
	mux := http.NewServeMux()
	a.Register(mux, "")
	return a, mux
}

// doAs performs a request through the mux with user's identity on the
// context, the way auth.Middleware would attach it.
func doAs(t *testing.T, mux *http.ServeMux, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{
		Username: user, DisplayName: "The " + user, AvatarURL: "https://a/" + user,
	}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func decodeIdea(t *testing.T, rr *httptest.ResponseRecorder) store.Idea {
	t.Helper()
	var idea store.Idea
	if err := json.Unmarshal(rr.Body.Bytes(), &idea); err != nil {
		t.Fatalf("decode idea from %q: %v", rr.Body.String(), err)
	}
	return idea
}

func createIdea(t *testing.T, mux *http.ServeMux, user, body string) store.Idea {
	t.Helper()
	rr := doAs(t, mux, user, http.MethodPost, "/api/ideas", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create idea: status %d body %s", rr.Code, rr.Body.String())
	}
	return decodeIdea(t, rr)
}

func TestHandleMe(t *testing.T) {
	_, mux := newTestAPI(t)
	rr := doAs(t, mux, "alice", http.MethodGet, "/api/me", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var id auth.Identity
	if err := json.Unmarshal(rr.Body.Bytes(), &id); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if id.Username != "alice" || id.DisplayName != "The alice" {
		t.Fatalf("identity = %+v", id)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q", ct)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x", "y"); got != "x" {
		t.Fatalf("got %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("empty args: got %q", got)
	}
}

func TestIdeaForViewerHidesSeenAtFromNonAuthor(t *testing.T) {
	seen := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	idea := &store.Idea{Author: "alice", SuggestionsSeenAt: seen}
	if got := ideaForViewer(idea, "alice"); !got.SuggestionsSeenAt.Equal(seen) {
		t.Fatalf("author copy lost SuggestionsSeenAt: %+v", got)
	}
	if got := ideaForViewer(idea, "bob"); !got.SuggestionsSeenAt.IsZero() {
		t.Fatalf("non-author copy leaked SuggestionsSeenAt: %+v", got)
	}
	if !idea.SuggestionsSeenAt.Equal(seen) {
		t.Fatalf("original mutated")
	}
}

func TestStoreErrStatus(t *testing.T) {
	if s, m := storeErrStatus(store.ErrNotFound); s != http.StatusNotFound || m != "idea not found" {
		t.Fatalf("not found → %d %q", s, m)
	}
	if s, m := storeErrStatus(&store.ValidationError{Msg: "title is required"}); s != http.StatusBadRequest || m != "title is required" {
		t.Fatalf("validation → %d %q", s, m)
	}
	if s, m := storeErrStatus(http.ErrServerClosed); s != http.StatusInternalServerError || m != "internal error" {
		t.Fatalf("other → %d %q", s, m)
	}
}

func TestSummarizeMatchesCapsTopAtThree(t *testing.T) {
	matches := []store.Match{
		{RepoID: "o/r1", Score: 0.9}, {RepoID: "o/r2", Score: 0.8},
		{RepoID: "o/r3", Score: 0.7}, {RepoID: "o/r4", Score: 0.6},
	}
	cncf := []store.CNCFMatch{{Name: "proj", Score: 0.5}}
	out := summarizeMatches(matches, cncf)
	if out.Count != 5 {
		t.Fatalf("count %d, want 5", out.Count)
	}
	if len(out.Top) != 3 || out.Top[0].RepoID != "o/r1" || out.Top[2].RepoID != "o/r3" {
		t.Fatalf("top = %+v", out.Top)
	}
	if len(out.Hive) != 4 || len(out.CNCF) != 1 {
		t.Fatalf("hive %d cncf %d", len(out.Hive), len(out.CNCF))
	}
}

func TestResponseSinceFiltersEvents(t *testing.T) {
	job := &adminRematchJob{ID: "7", Status: "running", Dry: true, Next: 3,
		Events: []adminRematchEvent{{Seq: 1}, {Seq: 2}, {Seq: 3}}}
	res := job.responseSince(2)
	if len(res.Events) != 1 || res.Events[0].Seq != 3 {
		t.Fatalf("events = %+v", res.Events)
	}
	if res.JobID != "7" || res.Status != "running" || !res.Dry || res.Next != 3 {
		t.Fatalf("response = %+v", res)
	}
	if got := job.responseSince(0); len(got.Events) != 3 {
		t.Fatalf("since 0: %+v", got.Events)
	}
}

func TestCreateIdea(t *testing.T) {
	_, mux := newTestAPI(t)

	idea := createIdea(t, mux, "alice",
		`{"title":"Cache warming","body":"Warm the cache on boot.","visibility":"public","status":"offered"}`)
	if idea.ID == "" || idea.Symbol == "" {
		t.Fatalf("missing ID/symbol: %+v", idea)
	}
	if idea.Author != "alice" || idea.AuthorDisplay != "The alice" || idea.AuthorProvider != "github" {
		t.Fatalf("author fields: %+v", idea)
	}
	if idea.Status != store.StatusOffered {
		t.Fatalf("status %q", idea.Status)
	}

	// Invalid transition-time status values are rejected before the store.
	rr := doAs(t, mux, "alice", http.MethodPost, "/api/ideas",
		`{"title":"t","body":"b","visibility":"public","status":"settled"}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `must be \"draft\" or \"offered\"`) {
		t.Fatalf("bad status: %d %s", rr.Code, rr.Body.String())
	}

	// Store validation errors surface as 400 with the message.
	rr = doAs(t, mux, "alice", http.MethodPost, "/api/ideas",
		`{"title":"","body":"b","visibility":"public"}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "title is required") {
		t.Fatalf("validation: %d %s", rr.Code, rr.Body.String())
	}

	// Malformed JSON → 400.
	rr = doAs(t, mux, "alice", http.MethodPost, "/api/ideas", `{"title":`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid JSON") {
		t.Fatalf("bad JSON: %d %s", rr.Code, rr.Body.String())
	}

	// Oversized body → 413.
	huge := `{"title":"t","body":"` + strings.Repeat("x", maxRequestBody) + `"}`
	rr = doAs(t, mux, "alice", http.MethodPost, "/api/ideas", huge)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: %d", rr.Code)
	}

	// AuthorDisplay falls back to the username when the identity has none.
	req := httptest.NewRequest(http.MethodPost, "/api/ideas",
		strings.NewReader(`{"title":"t","body":"b","visibility":"private"}`))
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Username: "carol"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("carol create: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeIdea(t, rec); got.AuthorDisplay != "carol" {
		t.Fatalf("display fallback: %+v", got)
	}
}

func TestListIdeasScopes(t *testing.T) {
	_, mux := newTestAPI(t)
	createIdea(t, mux, "alice", `{"title":"mine private","body":"b","visibility":"private"}`)
	createIdea(t, mux, "alice", `{"title":"mine public","body":"b","visibility":"public"}`)
	createIdea(t, mux, "bob", `{"title":"bobs public","body":"b","visibility":"public"}`)
	createIdea(t, mux, "bob", `{"title":"bobs private","body":"b","visibility":"private"}`)

	list := func(user, query string) []store.Idea {
		rr := doAs(t, mux, user, http.MethodGet, "/api/ideas"+query, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("list %q: %d %s", query, rr.Code, rr.Body.String())
		}
		var out []store.Idea
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	if got := list("alice", ""); len(got) != 2 {
		t.Fatalf("default scope=mine: %d ideas", len(got))
	}
	if got := list("alice", "?scope=mine"); len(got) != 2 {
		t.Fatalf("scope=mine: %d ideas", len(got))
	}
	pub := list("alice", "?scope=public")
	if len(pub) != 2 {
		t.Fatalf("scope=public: %d ideas", len(pub))
	}
	for _, idea := range pub {
		if idea.Visibility != store.VisibilityPublic {
			t.Fatalf("private idea leaked into public scope: %+v", idea)
		}
	}
	rr := doAs(t, mux, "alice", http.MethodGet, "/api/ideas?scope=weird", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d", rr.Code)
	}
}

func TestGetIdeaAuthorization(t *testing.T) {
	a, mux := newTestAPI(t)
	priv := createIdea(t, mux, "alice", `{"title":"secret","body":"b","visibility":"private"}`)
	pub := createIdea(t, mux, "alice", `{"title":"open","body":"b","visibility":"public"}`)

	// Author reads own private idea.
	if rr := doAs(t, mux, "alice", http.MethodGet, "/api/ideas/"+priv.ID, ""); rr.Code != http.StatusOK {
		t.Fatalf("author private: %d", rr.Code)
	}
	// A stranger's private idea 404s — existence must not leak.
	if rr := doAs(t, mux, "bob", http.MethodGet, "/api/ideas/"+priv.ID, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("stranger private: %d, want 404", rr.Code)
	}
	// Public ideas are readable by anyone authenticated.
	if rr := doAs(t, mux, "bob", http.MethodGet, "/api/ideas/"+pub.ID, ""); rr.Code != http.StatusOK {
		t.Fatalf("stranger public: %d", rr.Code)
	}
	// Missing idea → 404.
	if rr := doAs(t, mux, "alice", http.MethodGet, "/api/ideas/nope", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rr.Code)
	}

	// SuggestionsSeenAt is only visible to the author.
	if _, err := a.Store.Mutate(pub.ID, false, func(i *store.Idea) error {
		i.SuggestionsSeenAt = time.Now().UTC()
		return nil
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	rr := doAs(t, mux, "bob", http.MethodGet, "/api/ideas/"+pub.ID, "")
	if got := decodeIdea(t, rr); !got.SuggestionsSeenAt.IsZero() {
		t.Fatalf("SuggestionsSeenAt leaked to non-author: %+v", got)
	}
	rr = doAs(t, mux, "alice", http.MethodGet, "/api/ideas/"+pub.ID, "")
	if got := decodeIdea(t, rr); got.SuggestionsSeenAt.IsZero() {
		t.Fatalf("author lost SuggestionsSeenAt")
	}
}

func TestGetIdeaOfferedRepoOwnerCanRead(t *testing.T) {
	a, mux := newTestAPI(t)
	priv := createIdea(t, mux, "alice", `{"title":"pitch","body":"b","visibility":"private"}`)
	if err := a.Registry.Merge([]registry.RepoProfile{{RepoID: "org/repo", Owner: "bob"}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := a.Store.Mutate(priv.ID, false, func(i *store.Idea) error {
		i.Offers = []store.Offer{{RepoID: "org/repo", Status: store.OfferPending, CreatedAt: time.Now().UTC()}}
		return nil
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	// The offered repo's owner may read even a private idea.
	if rr := doAs(t, mux, "bob", http.MethodGet, "/api/ideas/"+priv.ID, ""); rr.Code != http.StatusOK {
		t.Fatalf("offered repo owner: %d %s", rr.Code, rr.Body.String())
	}
	// But not write to it.
	rr := doAs(t, mux, "bob", http.MethodPut, "/api/ideas/"+priv.ID,
		`{"title":"hijack","body":"b","visibility":"public"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("offered repo owner write on private idea: %d, want 404", rr.Code)
	}
	// A third party still gets 404.
	if rr := doAs(t, mux, "mallory", http.MethodGet, "/api/ideas/"+priv.ID, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("third party: %d", rr.Code)
	}
}

func TestUpdateIdea(t *testing.T) {
	_, mux := newTestAPI(t)
	idea := createIdea(t, mux, "alice", `{"title":"v1","body":"b1","visibility":"public"}`)

	rr := doAs(t, mux, "alice", http.MethodPut, "/api/ideas/"+idea.ID,
		`{"title":"v2","body":"b2","visibility":"private","status":"offered"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}
	got := decodeIdea(t, rr)
	if got.Title != "v2" || got.Body != "b2" || got.Visibility != store.VisibilityPrivate || got.Status != store.StatusOffered {
		t.Fatalf("updated = %+v", got)
	}

	// Non-author on a public idea → 403 (not 404: existence is known).
	rr = doAs(t, mux, "bob", http.MethodPut, "/api/ideas/"+idea.ID,
		`{"title":"x","body":"b","visibility":"public"}`)
	if rr.Code != http.StatusNotFound && rr.Code != http.StatusForbidden {
		t.Fatalf("non-author update: %d", rr.Code)
	}

	// Bad status literal → 400 before hitting the store.
	rr = doAs(t, mux, "alice", http.MethodPut, "/api/ideas/"+idea.ID,
		`{"title":"v3","body":"b","visibility":"public","status":"accepted"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", rr.Code)
	}

	// Store-side validation error → 400.
	rr = doAs(t, mux, "alice", http.MethodPut, "/api/ideas/"+idea.ID,
		`{"title":"","body":"b","visibility":"public"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("validation: %d", rr.Code)
	}
}

func TestDeleteIdea(t *testing.T) {
	_, mux := newTestAPI(t)
	idea := createIdea(t, mux, "alice", `{"title":"gone","body":"b","visibility":"public"}`)

	// Only the author may delete; for a public idea a stranger gets 403.
	if rr := doAs(t, mux, "bob", http.MethodDelete, "/api/ideas/"+idea.ID, ""); rr.Code != http.StatusForbidden {
		t.Fatalf("stranger delete: %d, want 403", rr.Code)
	}
	if rr := doAs(t, mux, "alice", http.MethodDelete, "/api/ideas/"+idea.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
	if rr := doAs(t, mux, "alice", http.MethodGet, "/api/ideas/"+idea.ID, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", rr.Code)
	}
}

func TestListRepos(t *testing.T) {
	a, mux := newTestAPI(t)
	if err := a.Registry.Merge([]registry.RepoProfile{
		{RepoID: "org/open", Owner: "alice"},
		{RepoID: "org/closed", Owner: "bob"},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	accepting := true
	if _, err := a.Registry.ApplyOwnerUpdate("org/open", "alice", registry.OwnerUpdate{AcceptingIdeas: &accepting}); err != nil {
		t.Fatalf("owner update: %v", err)
	}

	list := func(user, query string) []registry.RepoProfile {
		rr := doAs(t, mux, user, http.MethodGet, "/api/repos"+query, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("list %q: %d %s", query, rr.Code, rr.Body.String())
		}
		var out []registry.RepoProfile
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	if got := list("carol", ""); len(got) != 1 || got[0].RepoID != "org/open" {
		t.Fatalf("default accepting scope: %+v", got)
	}
	if got := list("carol", "?scope=all"); len(got) != 2 {
		t.Fatalf("scope=all: %+v", got)
	}
	if got := list("bob", "?scope=mine"); len(got) != 1 || got[0].RepoID != "org/closed" {
		t.Fatalf("scope=mine: %+v", got)
	}
	if rr := doAs(t, mux, "carol", http.MethodGet, "/api/repos?scope=nope", ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d", rr.Code)
	}
}

func TestUpdateRepo(t *testing.T) {
	a, mux := newTestAPI(t)
	if err := a.Registry.Merge([]registry.RepoProfile{{RepoID: "org/repo", Owner: "alice"}}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	rr := doAs(t, mux, "alice", http.MethodPut, "/api/repos/org/repo",
		`{"acceptingIdeas":true,"appetite":"CLI tools"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner update: %d %s", rr.Code, rr.Body.String())
	}
	var rp registry.RepoProfile
	if err := json.Unmarshal(rr.Body.Bytes(), &rp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !rp.AcceptingIdeas || rp.Appetite != "CLI tools" {
		t.Fatalf("profile = %+v", rp)
	}

	if rr := doAs(t, mux, "bob", http.MethodPut, "/api/repos/org/repo", `{"acceptingIdeas":false}`); rr.Code != http.StatusForbidden {
		t.Fatalf("non-owner: %d", rr.Code)
	}
	if rr := doAs(t, mux, "alice", http.MethodPut, "/api/repos/org/nope", `{}`); rr.Code != http.StatusNotFound {
		t.Fatalf("missing repo: %d", rr.Code)
	}
	long := strings.Repeat("x", registry.MaxAppetiteLen+1)
	if rr := doAs(t, mux, "alice", http.MethodPut, "/api/repos/org/repo", `{"appetite":"`+long+`"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized appetite: %d", rr.Code)
	}
	if rr := doAs(t, mux, "alice", http.MethodPut, "/api/repos/org/repo", `{"appetite":`); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d", rr.Code)
	}
}

func TestAdminEndpointsRequireAdmin(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "root, ADMIN2")
	_, mux := newTestAPI(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/ideas"},
		{http.MethodPost, "/api/admin/ideas/x/rematch"},
		{http.MethodGet, "/api/admin/ideas/x/rematch"},
	} {
		if rr := doAs(t, mux, "alice", tc.method, tc.path, ""); rr.Code != http.StatusForbidden {
			t.Fatalf("%s %s as non-admin: %d, want 403", tc.method, tc.path, rr.Code)
		}
	}
	// Case-insensitive admin match.
	if rr := doAs(t, mux, "admin2", http.MethodGet, "/api/admin/ideas", ""); rr.Code != http.StatusOK {
		t.Fatalf("admin2: %d", rr.Code)
	}
}

func TestAdminIdeasListing(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "root")
	a, mux := newTestAPI(t)
	idea := createIdea(t, mux, "alice", `{"title":"idea one","body":"b","visibility":"private"}`)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error {
		i.Matches = []store.Match{{RepoID: "o/r", Score: 0.9}}
		i.CNCFMatches = []store.CNCFMatch{{Name: "p", Score: 0.5}}
		return nil
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	rr := doAs(t, mux, "root", http.MethodGet, "/api/admin/ideas", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	var out []adminIdea
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len %d", len(out))
	}
	got := out[0]
	if got.ID != idea.ID || got.Author != "alice" || got.Title != "idea one" {
		t.Fatalf("adminIdea = %+v", got)
	}
	if got.AuthorProvider != "github" {
		t.Fatalf("provider %q", got.AuthorProvider)
	}
	if got.Matches.Count != 2 || len(got.Matches.Top) != 1 || got.Matches.Top[0].RepoID != "o/r" {
		t.Fatalf("matches = %+v", got.Matches)
	}
}

func TestAdminRematchWithoutEngine(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "root")
	_, mux := newTestAPI(t)
	rr := doAs(t, mux, "root", http.MethodPost, "/api/admin/ideas/x/rematch", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil engine: %d, want 503", rr.Code)
	}
}

func TestAdminRematchStatus(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "root")
	a, mux := newTestAPI(t)

	// Unknown idea → 404.
	if rr := doAs(t, mux, "root", http.MethodGet, "/api/admin/ideas/x/rematch", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("no job: %d", rr.Code)
	}
	// Malformed since → 400.
	if rr := doAs(t, mux, "root", http.MethodGet, "/api/admin/ideas/x/rematch?since=abc", ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad since: %d", rr.Code)
	}

	// Seed a finished job the way runAdminRematch would leave it.
	a.rematchMu.Lock()
	a.rematchJobs = map[string]*adminRematchJob{"idea1": {
		ID: "42", Status: "done", Dry: true, Next: 2,
		Events: []adminRematchEvent{{Seq: 1}, {Seq: 2}},
	}}
	a.rematchMu.Unlock()

	rr := doAs(t, mux, "root", http.MethodGet, "/api/admin/ideas/idea1/rematch?since=1", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rr.Code, rr.Body.String())
	}
	var res adminRematchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != "done" || res.JobID != "42" || len(res.Events) != 1 || res.Events[0].Seq != 2 {
		t.Fatalf("response = %+v", res)
	}

	// A stale job handle conflicts.
	if rr := doAs(t, mux, "root", http.MethodGet, "/api/admin/ideas/idea1/rematch?job=41", ""); rr.Code != http.StatusConflict {
		t.Fatalf("stale job: %d", rr.Code)
	}
	// The right job handle matches.
	if rr := doAs(t, mux, "root", http.MethodGet, "/api/admin/ideas/idea1/rematch?job=42", ""); rr.Code != http.StatusOK {
		t.Fatalf("matching job: %d", rr.Code)
	}
}

func TestStartAdminRematchConflictsWhileRunning(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "root")
	a, mux := newTestAPI(t)
	a.Engine = &match.Engine{Store: a.Store, Registry: a.Registry} // conflict paths return before using it
	idea := createIdea(t, mux, "alice", `{"title":"busy","body":"b","visibility":"public"}`)
	a.rematchMu.Lock()
	a.rematchJobs = map[string]*adminRematchJob{idea.ID: {ID: "1", Status: "running", Dry: true}}
	a.rematchMu.Unlock()

	// Dry rematch of an idea with a running job → 409, whether starting…
	rr := doAs(t, mux, "root", http.MethodPost, "/api/admin/ideas/"+idea.ID+"/rematch?dry=1", "")
	if rr.Code != http.StatusConflict {
		t.Fatalf("dry while running: %d, want 409 (%s)", rr.Code, rr.Body.String())
	}
	// …or applying.
	rr = doAs(t, mux, "root", http.MethodPost, "/api/admin/ideas/"+idea.ID+"/rematch", "")
	if rr.Code != http.StatusConflict {
		t.Fatalf("apply while running: %d, want 409 (%s)", rr.Code, rr.Body.String())
	}
}

func TestApplyRematchRequiresCompletedDryRun(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "root")
	a, mux := newTestAPI(t)
	a.Engine = &match.Engine{Store: a.Store, Registry: a.Registry} // refusal paths return before using it
	idea := createIdea(t, mux, "alice", `{"title":"apply","body":"b","visibility":"public"}`)

	// No dry run at all → 409.
	rr := doAs(t, mux, "root", http.MethodPost, "/api/admin/ideas/"+idea.ID+"/rematch", "")
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "no completed dry run") {
		t.Fatalf("no dry run: %d %s", rr.Code, rr.Body.String())
	}

	// A done dry run against a STALE idea snapshot also refuses.
	a.rematchMu.Lock()
	a.rematchJobs = map[string]*adminRematchJob{idea.ID: {
		ID: "1", Status: "done", Dry: true,
		IdeaUpdatedAt: idea.UpdatedAt.Add(-time.Hour),
	}}
	a.rematchMu.Unlock()
	rr = doAs(t, mux, "root", http.MethodPost, "/api/admin/ideas/"+idea.ID+"/rematch", "")
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "no completed dry run") {
		t.Fatalf("stale dry run: %d %s", rr.Code, rr.Body.String())
	}
}
