package match

// Gap-closing tests for previously uncovered pkg/match functions:
// ScoreForRepo (repo-side feed scoring + cache replace/append + notify),
// PersistRematchResults (ErrRepoChanged / ErrIdeaChanged / notify),
// LLMFromEnv + envValue (env config incl. legacy IDEATE_ prefix),
// firstNonEmpty, and nonHiveCNCF registry filtering.

import (
	"context"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// notifyRecorder records NewMatch calls.
type notifyRecorder struct {
	calls []notifyCall
}

type notifyCall struct {
	IdeaAuthor string
	RepoOwner  string
	RepoID     string
	Score      float64
}

func (n *notifyRecorder) NewMatch(ideaAuthor, repoOwner string, idea *store.Idea, repo *registry.RepoProfile, score float64) {
	n.calls = append(n.calls, notifyCall{IdeaAuthor: ideaAuthor, RepoOwner: repoOwner, RepoID: repo.RepoID, Score: score})
}

// TestScoreForRepoFreshCachedAndInvalidated: a fresh score is computed,
// persisted onto the idea, and notified when strong; a second call is served
// from the cache with zero gateway traffic; a repo-profile change invalidates
// the hash and the rescore REPLACES the cached entry instead of appending.
func TestScoreForRepoFreshCachedAndInvalidated(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/feed", Owner: "bob"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	srv, calls := fakeGateway(t, func(string) string { return `{"score": 90, "reason": "strong fit"}` })
	rec := &notifyRecorder{}
	e := &Engine{Store: st, Registry: reg, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}, Notifier: rec}
	idea := createIdea(t, st, "T", "B body")
	rp, err := reg.Get("org/feed")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	m, err := e.ScoreForRepo(context.Background(), idea, rp)
	if err != nil {
		t.Fatalf("ScoreForRepo: %v", err)
	}
	if m.Score != 90 || !m.ByLLM || m.RepoID != "org/feed" {
		t.Fatalf("fresh match: %+v", m)
	}
	// 90 >= NotifyThreshold: both sides are told.
	if len(rec.calls) != 1 || rec.calls[0].RepoID != "org/feed" || rec.calls[0].IdeaAuthor != "alice" || rec.calls[0].RepoOwner != "bob" || rec.calls[0].Score != 90 {
		t.Fatalf("notify calls: %+v", rec.calls)
	}
	// Persisted onto the idea record.
	idea, _ = st.Get(idea.ID)
	if len(idea.Matches) != 1 || idea.Matches[0].RepoID != "org/feed" || idea.MatchesUpdatedAt.IsZero() {
		t.Fatalf("persisted matches: %+v", idea.Matches)
	}

	// Cached: same hash, no gateway call, no extra notify.
	before := calls.Load()
	m2, err := e.ScoreForRepo(context.Background(), idea, rp)
	if err != nil {
		t.Fatalf("ScoreForRepo (cached): %v", err)
	}
	if calls.Load() != before {
		t.Fatalf("cached score must not hit the gateway (calls %d → %d)", before, calls.Load())
	}
	if m2.RepoHash != m.RepoHash || len(rec.calls) != 1 {
		t.Fatalf("cached result changed: %+v, notifies %d", m2, len(rec.calls))
	}

	// Repo profile edit invalidates; the rescore replaces the entry in place.
	if _, err := reg.ApplyOwnerUpdate("org/feed", "bob", registry.OwnerUpdate{Appetite: strPtr("new appetite")}); err != nil {
		t.Fatalf("ApplyOwnerUpdate: %v", err)
	}
	rp, _ = reg.Get("org/feed")
	idea, _ = st.Get(idea.ID)
	m3, err := e.ScoreForRepo(context.Background(), idea, rp)
	if err != nil {
		t.Fatalf("ScoreForRepo (invalidated): %v", err)
	}
	if calls.Load() == before {
		t.Fatal("repo edit must invalidate the cached score")
	}
	if m3.RepoHash == m.RepoHash {
		t.Fatalf("hash must change with the profile: %q", m3.RepoHash)
	}
	idea, _ = st.Get(idea.ID)
	if len(idea.Matches) != 1 || idea.Matches[0].RepoHash != m3.RepoHash {
		t.Fatalf("rescore must replace, not append: %+v", idea.Matches)
	}
}

// TestScoreForRepoAppendsNewRepo: scoring a second repo appends alongside the
// first instead of overwriting it, and a weak score never notifies.
func TestScoreForRepoAppendsNewRepo(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "org/a", Owner: "bob"},
		{RepoID: "org/b", Owner: "carol"},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	srv, _ := fakeGateway(t, func(string) string { return `{"score": 10, "reason": "weak"}` })
	rec := &notifyRecorder{}
	e := &Engine{Store: st, Registry: reg, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}, Notifier: rec}
	idea := createIdea(t, st, "T", "B body")
	for _, id := range []string{"org/a", "org/b"} {
		rp, err := reg.Get(id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if _, err := e.ScoreForRepo(context.Background(), idea, rp); err != nil {
			t.Fatalf("ScoreForRepo %s: %v", id, err)
		}
		idea, _ = st.Get(idea.ID)
	}
	if len(idea.Matches) != 2 {
		t.Fatalf("want 2 persisted matches, got %+v", idea.Matches)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("scores below NotifyThreshold must not notify: %+v", rec.calls)
	}
}

// TestScoreForRepoMutateError: an unknown idea surfaces the store error.
func TestScoreForRepoMutateError(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/a", Owner: "bob"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	e := &Engine{Store: st, Registry: reg}
	rp, _ := reg.Get("org/a")
	ghost := &store.Idea{ID: "no-such-idea", Author: "alice", Title: "T", Body: "B"}
	if _, err := e.ScoreForRepo(context.Background(), ghost, rp); err == nil {
		t.Fatal("expected error persisting to unknown idea")
	}
}

// TestPersistRematchResultsHappyPathAndNotify: results commit when nothing
// drifted, and strong persisted matches notify while weak ones stay silent.
func TestPersistRematchResultsHappyPathAndNotify(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "org/strong", Owner: "bob"},
		{RepoID: "org/weak", Owner: "carol"},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	rec := &notifyRecorder{}
	e := &Engine{Store: st, Registry: reg, Notifier: rec}
	idea := createIdea(t, st, "T", "B body")
	strongRp, _ := reg.Get("org/strong")
	weakRp, _ := reg.Get("org/weak")
	matches := []store.Match{
		{RepoID: "org/strong", Score: NotifyThreshold, RepoHash: RepoHash(strongRp)},
		{RepoID: "org/weak", Score: NotifyThreshold - 1, RepoHash: RepoHash(weakRp)},
	}
	cncf := []CNCFMatch{{Name: "Istio", RepoID: "istio/istio", Score: 42}}

	if err := e.PersistRematchResults(idea.ID, idea.UpdatedAt, "the tldr", matches, cncf); err != nil {
		t.Fatalf("PersistRematchResults: %v", err)
	}
	got, _ := st.Get(idea.ID)
	if got.TLDR != "the tldr" || len(got.Matches) != 2 || len(got.CNCFMatches) != 1 || got.MatchesUpdatedAt.IsZero() {
		t.Fatalf("persisted idea: tldr=%q matches=%+v cncf=%+v", got.TLDR, got.Matches, got.CNCFMatches)
	}
	// Only the >=threshold match notifies.
	if len(rec.calls) != 1 || rec.calls[0].RepoID != "org/strong" || rec.calls[0].RepoOwner != "bob" {
		t.Fatalf("notify calls: %+v", rec.calls)
	}
}

// TestPersistRematchResultsRepoChanged: a stale RepoHash (or a repo that
// vanished from the registry) aborts the commit with ErrRepoChanged.
func TestPersistRematchResultsRepoChanged(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/a", Owner: "bob"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	e := &Engine{Store: st, Registry: reg}
	idea := createIdea(t, st, "T", "B body")

	stale := []store.Match{{RepoID: "org/a", Score: 90, RepoHash: "stale-hash"}}
	if err := e.PersistRematchResults(idea.ID, idea.UpdatedAt, "t", stale, nil); err != ErrRepoChanged {
		t.Fatalf("stale hash: want ErrRepoChanged, got %v", err)
	}
	missing := []store.Match{{RepoID: "org/gone", Score: 90, RepoHash: "x"}}
	if err := e.PersistRematchResults(idea.ID, idea.UpdatedAt, "t", missing, nil); err != ErrRepoChanged {
		t.Fatalf("missing repo: want ErrRepoChanged, got %v", err)
	}
	got, _ := st.Get(idea.ID)
	if got.TLDR != "" || len(got.Matches) != 0 {
		t.Fatalf("aborted commit must not persist anything: %+v", got)
	}
}

// TestPersistRematchResultsIdeaChanged: an idea edited mid-rematch aborts the
// commit with ErrIdeaChanged.
func TestPersistRematchResultsIdeaChanged(t *testing.T) {
	st, reg := newFixtures(t)
	e := &Engine{Store: st, Registry: reg}
	idea := createIdea(t, st, "T", "B body")
	if err := e.PersistRematchResults(idea.ID, idea.UpdatedAt.Add(-time.Second), "t", nil, nil); err != ErrIdeaChanged {
		t.Fatalf("want ErrIdeaChanged, got %v", err)
	}
}

// TestLLMFromEnv: unset base URL disables the LLM; DIBS_ vars build a client
// with a trimmed base URL and the default model; legacy IDEATE_ names are
// honored as fallbacks but never override DIBS_ values.
func TestLLMFromEnv(t *testing.T) {
	for _, k := range []string{"DIBS_LLM_BASE_URL", "DIBS_LLM_API_KEY", "DIBS_LLM_MODEL",
		"IDEATE_LLM_BASE_URL", "IDEATE_LLM_API_KEY", "IDEATE_LLM_MODEL"} {
		t.Setenv(k, "")
	}
	if l := LLMFromEnv(); l != nil {
		t.Fatalf("no base URL must disable the LLM, got %+v", l)
	}

	t.Setenv("DIBS_LLM_BASE_URL", "http://litellm:4000/v1/")
	l := LLMFromEnv()
	if l == nil {
		t.Fatal("expected an LLM")
	}
	if l.BaseURL != "http://litellm:4000/v1" {
		t.Fatalf("trailing slash must be trimmed: %q", l.BaseURL)
	}
	if l.Model != DefaultModel || l.APIKey != "" {
		t.Fatalf("defaults: model=%q key=%q", l.Model, l.APIKey)
	}

	t.Setenv("DIBS_LLM_MODEL", "  custom-model  ")
	t.Setenv("DIBS_LLM_API_KEY", "sk-test")
	l = LLMFromEnv()
	if l.Model != "custom-model" || l.APIKey != "sk-test" {
		t.Fatalf("explicit vars: model=%q key=%q", l.Model, l.APIKey)
	}

	// Legacy IDEATE_ prefix is a fallback for each var independently.
	t.Setenv("DIBS_LLM_BASE_URL", "")
	t.Setenv("DIBS_LLM_MODEL", "")
	t.Setenv("DIBS_LLM_API_KEY", "")
	t.Setenv("IDEATE_LLM_BASE_URL", "http://legacy:4000/v1")
	t.Setenv("IDEATE_LLM_MODEL", "legacy-model")
	t.Setenv("IDEATE_LLM_API_KEY", "sk-legacy")
	l = LLMFromEnv()
	if l == nil || l.BaseURL != "http://legacy:4000/v1" || l.Model != "legacy-model" || l.APIKey != "sk-legacy" {
		t.Fatalf("legacy prefix fallback: %+v", l)
	}

	// DIBS_ wins over IDEATE_ when both are set.
	t.Setenv("DIBS_LLM_BASE_URL", "http://new:4000/v1")
	if l = LLMFromEnv(); l.BaseURL != "http://new:4000/v1" {
		t.Fatalf("DIBS_ must win over IDEATE_: %q", l.BaseURL)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "third", "fourth"); got != "third" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("no args must return empty, got %q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("all empty must return empty, got %q", got)
	}
}

// TestNonHiveCNCFFiltersRegistryRepos: CNCF suggestions that are already
// hive-managed repos are dropped; a nil registry keeps everything.
func TestNonHiveCNCFFiltersRegistryRepos(t *testing.T) {
	_, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{{RepoID: "istio/istio", Owner: "bob"}}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	in := []CNCFMatch{
		{Name: "Istio", RepoID: "istio/istio"},
		{Name: "Vitess", RepoID: "vitessio/vitess"},
	}
	e := &Engine{Registry: reg}
	out := e.nonHiveCNCF(in)
	if len(out) != 1 || out[0].RepoID != "vitessio/vitess" {
		t.Fatalf("hive-managed repo must be filtered: %+v", out)
	}
	e = &Engine{}
	if out = e.nonHiveCNCF(in); len(out) != 2 {
		t.Fatalf("nil registry must keep everything: %+v", out)
	}
}
