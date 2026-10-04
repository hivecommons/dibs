package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

var errBoom = errors.New("boom")

// faultStore wraps a real IdeaStore and fails the methods named in fail
// with errBoom (a non-ErrNotFound, non-ValidationError failure, so handlers
// must map it to 500). Mutate fails only after mutateOK successful calls,
// so tests can let an earlier Mutate in the same handler succeed.
type faultStore struct {
	IdeaStore
	fail        map[string]bool
	mutateOK    int
	mutateCalls int
}

func (f *faultStore) ListAll() ([]*store.Idea, error) {
	if f.fail["ListAll"] {
		return nil, errBoom
	}
	return f.IdeaStore.ListAll()
}

func (f *faultStore) ListByAuthor(author string) ([]*store.Idea, error) {
	if f.fail["ListByAuthor"] {
		return nil, errBoom
	}
	return f.IdeaStore.ListByAuthor(author)
}

func (f *faultStore) ListPublic() ([]*store.Idea, error) {
	if f.fail["ListPublic"] {
		return nil, errBoom
	}
	return f.IdeaStore.ListPublic()
}

func (f *faultStore) ListSettled() ([]*store.Idea, error) {
	if f.fail["ListSettled"] {
		return nil, errBoom
	}
	return f.IdeaStore.ListSettled()
}

func (f *faultStore) ListOfferedTo(repoIDs []string) ([]*store.Idea, error) {
	if f.fail["ListOfferedTo"] {
		return nil, errBoom
	}
	return f.IdeaStore.ListOfferedTo(repoIDs)
}

func (f *faultStore) Update(idea *store.Idea) error {
	if f.fail["Update"] {
		return errBoom
	}
	return f.IdeaStore.Update(idea)
}

func (f *faultStore) Delete(id string) error {
	if f.fail["Delete"] {
		return errBoom
	}
	return f.IdeaStore.Delete(id)
}

func (f *faultStore) Mutate(id string, touch bool, fn func(*store.Idea) error) (*store.Idea, error) {
	if f.fail["Mutate"] {
		f.mutateCalls++
		if f.mutateCalls > f.mutateOK {
			return nil, errBoom
		}
	}
	return f.IdeaStore.Mutate(id, touch, fn)
}

// faultMatcher wraps a Matcher and fails the configured calls.
type faultMatcher struct {
	Matcher
	tldrErr, matchesErr, cncfErr, scoreErr, rematchErr, persistErr error
}

func (m *faultMatcher) EnsureTLDR(ctx context.Context, idea *store.Idea) (string, error) {
	if m.tldrErr != nil {
		return "", m.tldrErr
	}
	return "tldr", nil
}

func (m *faultMatcher) MatchesForIdea(ctx context.Context, idea *store.Idea) ([]store.Match, error) {
	if m.matchesErr != nil {
		return nil, m.matchesErr
	}
	return nil, nil
}

func (m *faultMatcher) CNCFMatchesForIdea(ctx context.Context, idea *store.Idea) ([]match.CNCFMatch, error) {
	if m.cncfErr != nil {
		return nil, m.cncfErr
	}
	return nil, nil
}

func (m *faultMatcher) ScoreForRepo(ctx context.Context, idea *store.Idea, rp *registry.RepoProfile) (store.Match, error) {
	if m.scoreErr != nil {
		return store.Match{}, m.scoreErr
	}
	return store.Match{Score: 0.9, Reason: "ok"}, nil
}

func (m *faultMatcher) RematchIdea(ctx context.Context, idea *store.Idea, persist bool, progress match.ProgressFunc) (string, []store.Match, []match.CNCFMatch, error) {
	if m.rematchErr != nil {
		return "", nil, nil, m.rematchErr
	}
	return m.Matcher.RematchIdea(ctx, idea, persist, progress)
}

func (m *faultMatcher) PersistRematchResults(ideaID string, expectedUpdatedAt time.Time, tldr string, matches []store.Match, cncf []match.CNCFMatch) error {
	if m.persistErr != nil {
		return m.persistErr
	}
	return m.Matcher.PersistRematchResults(ideaID, expectedUpdatedAt, tldr, matches, cncf)
}

// faultNotifier wraps a Notifier and fails Add and MarkRead.
type faultNotifier struct {
	Notifier
	addErr, markReadErr error
}

func (n *faultNotifier) Add(user, kind, message, ideaID, repoID string) error {
	if n.addErr != nil {
		return n.addErr
	}
	return n.Notifier.Add(user, kind, message, ideaID, repoID)
}

func (n *faultNotifier) MarkRead(user string, ids []string, all bool) error {
	if n.markReadErr != nil {
		return n.markReadErr
	}
	return n.Notifier.MarkRead(user, ids, all)
}

func withFaultStore(a *API, fail ...string) *faultStore {
	fs := &faultStore{IdeaStore: a.Store, fail: map[string]bool{}}
	for _, m := range fail {
		fs.fail[m] = true
	}
	a.Store = fs
	return fs
}

func want500(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("%s: status = %d body=%s, want 500", what, rec.Code, rec.Body.String())
	}
}

// TestStoreFailuresMapTo500_IdeaRoutes pins that an unexpected store error
// (neither not-found nor validation) on the idea CRUD routes surfaces as a
// generic 500 and never leaks the underlying error text.
func TestStoreFailuresMapTo500_IdeaRoutes(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "root")
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "alice", "Fragile", store.VisibilityPublic, store.StatusDraft)
	withFaultStore(a, "ListAll", "ListByAuthor", "ListPublic", "Update", "Delete")

	cases := []struct {
		name, method, path, body string
		id                       string
	}{
		{"admin list", "GET", "/api/admin/ideas", "", "root"},
		{"list mine", "GET", "/api/ideas", "", "alice"},
		{"list public", "GET", "/api/ideas?scope=public", "", "alice"},
		{"update", "PUT", "/api/ideas/" + idea.ID, `{"title":"Fragile","body":"b","visibility":"public"}`, "alice"},
		{"delete", "DELETE", "/api/ideas/" + idea.ID, "", "alice"},
	}
	for _, tc := range cases {
		rec := do(t, mux, ident(tc.id), tc.method, tc.path, tc.body)
		want500(t, rec, tc.name)
		if strings.Contains(rec.Body.String(), errBoom.Error()) {
			t.Fatalf("%s: leaked internal error text: %s", tc.name, rec.Body.String())
		}
	}
	// The idea must still exist: the failed Delete never reached the store.
	if _, err := a.Store.Get(idea.ID); err != nil {
		t.Fatalf("idea vanished after failed delete: %v", err)
	}
}

// TestStoreFailuresMapTo500_Wave2Mutations covers the Mutate error branches
// of the ideator-side wave-2 handlers (matches/seen, pass) and the repo-side
// decline.
func TestStoreFailuresMapTo500_Wave2Mutations(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "bob", "Mutable", store.VisibilityPublic, store.StatusOffered)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error {
		i.Offers = []store.Offer{{RepoID: "kubestellar/dibs", Status: store.OfferPending, CreatedAt: now}}
		return nil
	}); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	withFaultStore(a, "Mutate")

	want500(t, do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/matches/seen", ""), "matches/seen")
	want500(t, do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/pass", `{"repoID":"org/other"}`), "ideator pass")
	want500(t, do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide",
		`{"ideaID":"`+idea.ID+`","decision":"decline"}`), "decline")

	stored, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != store.StatusOffered || stored.Offers[0].Status != store.OfferPending {
		t.Fatalf("failed decline must not change state: %+v", stored)
	}
}

// TestRepoFeedStoreFailures covers the two list failures in the repo feed.
func TestRepoFeedStoreFailures(t *testing.T) {
	a, mux := newAPIFixture(t)
	fs := withFaultStore(a, "ListOfferedTo")
	want500(t, do(t, mux, ident("alice"), "GET", "/api/repos/kubestellar/dibs/feed", ""), "feed offered")

	fs.fail = map[string]bool{"ListPublic": true}
	want500(t, do(t, mux, ident("alice"), "GET", "/api/repos/kubestellar/dibs/feed", ""), "feed public")
}

// TestDecidePassRegistryFailure pins the 500 when recording a repo-side
// pass fails in the registry.
func TestDecidePassRegistryFailure(t *testing.T) {
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "bob", "Passable", store.VisibilityPublic, store.StatusDraft)
	a.Registry = &faultRegistry{RepoRegistry: a.Registry}
	want500(t, do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide",
		`{"ideaID":"`+idea.ID+`","decision":"pass"}`), "pass")
}

// faultRegistry wraps a RepoRegistry: AddPassedIdea always fails, and Get
// fails with getErr when set (nil keeps the real lookup).
type faultRegistry struct {
	RepoRegistry
	getErr error
}

func (r *faultRegistry) AddPassedIdea(repoID, actor, ideaID string) error { return errBoom }

func (r *faultRegistry) Get(repoID string) (*registry.RepoProfile, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.RepoRegistry.Get(repoID)
}

// TestIdeaMatchesEngineFailures walks each engine failure in GET
// /api/ideas/{id}/matches: TLDR, hive matches, CNCF matches.
func TestIdeaMatchesEngineFailures(t *testing.T) {
	a, mux := newAPIFixture(t)
	idea := mustCreate(t, a, "alice", "Matchable", store.VisibilityPublic, store.StatusDraft)
	path := "/api/ideas/" + idea.ID + "/matches"

	for name, m := range map[string]*faultMatcher{
		"tldr":    {tldrErr: errBoom},
		"matches": {matchesErr: errBoom},
		"cncf":    {cncfErr: errBoom},
	} {
		a.Engine = m
		want500(t, do(t, mux, ident("alice"), "GET", path, ""), name)
	}

	// Sanity: the same fixture with a healthy matcher succeeds.
	a.Engine = &faultMatcher{}
	if rec := do(t, mux, ident("alice"), "GET", path, ""); rec.Code != http.StatusOK {
		t.Fatalf("healthy matcher: %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestRepoFeedToleratesEngineFailures pins that repoView degrades (zero
// score, candidate still listed) rather than failing the feed when the
// engine cannot produce a TLDR or a score.
func TestRepoFeedToleratesEngineFailures(t *testing.T) {
	a, mux := newAPIFixture(t)
	mustCreate(t, a, "bob", "Candidate", store.VisibilityPublic, store.StatusDraft)
	a.Engine = &faultMatcher{tldrErr: errBoom, scoreErr: errBoom}

	rec := do(t, mux, ident("alice"), "GET", "/api/repos/kubestellar/dibs/feed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeBody[struct {
		Candidates []matchView `json:"candidates"`
	}](t, rec)
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(out.Candidates))
	}
	if c := out.Candidates[0]; c.Score != 0 || c.Reason != "" || c.ByLLM {
		t.Fatalf("degraded candidate should have zero score: %+v", c)
	}
}

// TestLegacySettleStoreFailureAfterIssueOpened covers the branch where the
// GitHub issue was opened but recording the settlement fails: the handler
// must return 500 (the accept Mutate already succeeded, so the idea stays
// accepted).
func TestLegacySettleStoreFailureAfterIssueOpened(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	a.Settler = &settle.Settler{GitHub: &settle.Fake{}}
	idea := mustCreate(t, a, "bob", "Legacy store failure", store.VisibilityPublic, store.StatusOffered)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error {
		i.Offers = []store.Offer{{RepoID: "kubestellar/dibs", Status: store.OfferPending, CreatedAt: now}}
		return nil
	}); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	fs := withFaultStore(a, "Mutate")
	fs.mutateOK = 1 // accept's Mutate succeeds; the settle Mutate fails

	want500(t, do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide",
		`{"ideaID":"`+idea.ID+`","decision":"accept"}`), "legacy settle")

	stored, err := a.Store.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != store.StatusAccepted || stored.IssueURL != "" {
		t.Fatalf("idea after failed settle record = %+v", stored)
	}
}

// TestNotifierFailures pins that a failing notification store turns
// mark-read into a 500 but never fails the request that merely emits a
// notification.
func TestNotifierFailures(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = &faultNotifier{Notifier: ns, addErr: errBoom, markReadErr: errBoom}

	want500(t, do(t, mux, ident("bob"), "POST", "/api/notifications/read", `{"all":true}`), "mark read")

	// A decline emits a notification; the Add failure is logged, not surfaced.
	idea := mustCreate(t, a, "bob", "Notify me", store.VisibilityPublic, store.StatusOffered)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error {
		i.Offers = []store.Offer{{RepoID: "kubestellar/dibs", Status: store.OfferPending, CreatedAt: now}}
		return nil
	}); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	rec := do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide",
		`{"ideaID":"`+idea.ID+`","decision":"decline"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("decline with failing notifier: %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminRematchEngineFailures covers the two engine error paths of the
// admin rematch: a dry run whose RematchIdea fails ends in status "error"
// with a generic message, and applying a completed dry run whose persist
// fails returns 500 (or 409 when the idea/repo changed underneath).
func TestAdminRematchEngineFailures(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "root")
	a, mux := newAPIFixture(t)
	real := &match.Engine{Store: a.Store.(*store.Store), Registry: a.Registry.(*registry.Registry)}
	idea := mustCreate(t, a, "alice", "Kubernetes idea marketplace", store.VisibilityPublic, store.StatusDraft)
	path := "/api/admin/ideas/" + idea.ID + "/rematch"

	// 1. RematchIdea fails → job status "error", error text is generic.
	a.Engine = &faultMatcher{Matcher: real, rematchErr: errBoom}
	if rec := do(t, mux, ident("root"), "POST", path+"?dry=1", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("start dry run: %d body=%s", rec.Code, rec.Body.String())
	}
	status := pollRematch(t, mux, path, "error")
	if status.Error != "rematch failed" || strings.Contains(status.Error, errBoom.Error()) {
		t.Fatalf("error job = %+v", status)
	}
	// Applying after an errored dry run must be refused.
	if rec := do(t, mux, ident("root"), "POST", path, ""); rec.Code != http.StatusConflict {
		t.Fatalf("apply after error: %d, want 409", rec.Code)
	}

	// 2. Dry run succeeds, persist fails generically → 500.
	fm := &faultMatcher{Matcher: real}
	a.Engine = fm
	if rec := do(t, mux, ident("root"), "POST", path+"?dry=1", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("start dry run: %d body=%s", rec.Code, rec.Body.String())
	}
	pollRematch(t, mux, path, "done")
	fm.persistErr = errBoom
	want500(t, do(t, mux, ident("root"), "POST", path, ""), "apply with failing persist")
	// The job was marked stale, so a second apply is a 409 not another 500.
	if rec := do(t, mux, ident("root"), "POST", path, ""); rec.Code != http.StatusConflict {
		t.Fatalf("apply after stale: %d, want 409", rec.Code)
	}

	// 3. Persist reports the idea changed underneath → 409.
	if rec := do(t, mux, ident("root"), "POST", path+"?dry=1", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("start dry run: %d body=%s", rec.Code, rec.Body.String())
	}
	pollRematch(t, mux, path, "done")
	fm.persistErr = match.ErrIdeaChanged
	if rec := do(t, mux, ident("root"), "POST", path, ""); rec.Code != http.StatusConflict {
		t.Fatalf("apply with changed idea: %d, want 409", rec.Code)
	}
}

func pollRematch(t *testing.T, mux *http.ServeMux, path, wantStatus string) adminRematchResponse {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec := do(t, mux, ident("root"), "GET", path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("poll status: %d body=%s", rec.Code, rec.Body.String())
		}
		status := decodeBody[adminRematchResponse](t, rec)
		if status.Status == wantStatus {
			return status
		}
		if status.Status != "running" || time.Now().After(deadline) {
			t.Fatalf("rematch ended in %q, want %q: %+v", status.Status, wantStatus, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
