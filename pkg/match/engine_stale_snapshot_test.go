package match

// Contract: every Engine entrypoint that persists or notifies releases those
// side effects only through commitIfFresh. An idea edited while its results
// are being computed must end up with nothing persisted, nobody notified, and
// the stored record exactly as the edit left it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// blockingGateway is an LLM gateway whose first call parks until the test
// releases it, so the test can edit the idea while results are in flight.
// Strong scores (above NotifyThreshold) make any leaked notification visible.
func blockingGateway(t *testing.T) (llm *LLM, started <-chan struct{}, release chan struct{}) {
	t.Helper()
	startedCh := make(chan struct{})
	release = make(chan struct{})
	var once sync.Once
	gw, _ := fakeGateway(t, func(user string) string {
		once.Do(func() {
			close(startedCh)
			<-release
		})
		if strings.Contains(user, "\n\nREPO\n") {
			return `{"score": 90, "reason": "strong fit"}`
		}
		return "A punchy TLDR."
	})
	return &LLM{BaseURL: gw.URL + "/v1", Model: "test"}, startedCh, release
}

func storedJSON(t *testing.T, st *store.Store, id string) []byte {
	t.Helper()
	got, err := st.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return raw
}

func TestSideEffectsSkipStaleSnapshot(t *testing.T) {
	cases := []struct {
		name    string
		wantErr error
		run     func(ctx context.Context, e *Engine, idea *store.Idea, rp *registry.RepoProfile) error
	}{
		{
			name: "EnsureTLDR",
			run: func(ctx context.Context, e *Engine, idea *store.Idea, _ *registry.RepoProfile) error {
				_, err := e.EnsureTLDR(ctx, idea)
				return err
			},
		},
		{
			name: "MatchesForIdea",
			run: func(ctx context.Context, e *Engine, idea *store.Idea, _ *registry.RepoProfile) error {
				_, err := e.MatchesForIdea(ctx, idea)
				return err
			},
		},
		{
			name: "ScoreForRepo",
			run: func(ctx context.Context, e *Engine, idea *store.Idea, rp *registry.RepoProfile) error {
				_, err := e.ScoreForRepo(ctx, idea, rp)
				return err
			},
		},
		{
			name: "CNCFMatchesForIdea",
			run: func(ctx context.Context, e *Engine, idea *store.Idea, _ *registry.RepoProfile) error {
				_, err := e.CNCFMatchesForIdea(ctx, idea)
				return err
			},
		},
		{
			name:    "RematchIdea persist",
			wantErr: ErrIdeaChanged,
			run: func(ctx context.Context, e *Engine, idea *store.Idea, _ *registry.RepoProfile) error {
				_, _, _, err := e.RematchIdea(ctx, idea, true, nil)
				return err
			},
		},
		{
			name:    "PersistRematchResults after dry run",
			wantErr: ErrIdeaChanged,
			run: func(ctx context.Context, e *Engine, idea *store.Idea, _ *registry.RepoProfile) error {
				catalogHash := e.CatalogFingerprint()
				tldr, hive, cncf, err := e.RematchIdea(ctx, idea, false, nil)
				if err != nil {
					return err
				}
				return e.PersistRematchResults(idea.ID, idea.UpdatedAt, tldr, hive, cncf, catalogHash)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, reg := newFixtures(t)
			if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/ops", Owner: "bob",
				Description: "kubernetes operator", Topics: []string{"kubernetes"}}}); err != nil {
				t.Fatalf("Merge: %v", err)
			}
			rp, err := reg.Get("org/ops")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			cs := cncfTestCatalog(t, []catalog.Project{{Name: "Operator Framework", RepoID: "operator-framework/operator-sdk",
				RepoURL: "https://github.com/operator-framework/operator-sdk", Maturity: "incubating",
				Category: "Application Definition", Description: "kubernetes operator toolkit"}})
			llm, started, release := blockingGateway(t)
			releaseOnce := sync.OnceFunc(func() { close(release) })
			defer releaseOnce()
			rec := &notifyRecorder{}
			e := &Engine{Store: st, Registry: reg, Catalog: cs, LLM: llm, Notifier: rec}
			idea := createIdea(t, st, "Kubernetes operator", "Cluster automation with a kubernetes operator.")

			done := make(chan error, 1)
			go func() { done <- tc.run(context.Background(), e, idea, rp) }()
			select {
			case <-started:
			case err := <-done:
				t.Fatalf("entrypoint returned (%v) without calling the gateway", err)
			}
			editBody(t, st, idea)
			want := storedJSON(t, st, idea.ID)
			releaseOnce()
			err = <-done

			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("stale snapshot notified: %+v", rec.calls)
			}
			if got := storedJSON(t, st, idea.ID); !bytes.Equal(got, want) {
				t.Fatalf("stale snapshot persisted:\n got %s\nwant %s", got, want)
			}
		})
	}
}

// persistedNotifier asserts that a notified match is already in the store,
// i.e. notifications are released only after the persist committed.
type persistedNotifier struct {
	t     *testing.T
	st    *store.Store
	calls int
}

func (p *persistedNotifier) NewMatch(_, _ string, idea *store.Idea, repo *registry.RepoProfile, _ float64) {
	p.calls++
	got, err := p.st.Get(idea.ID)
	if err != nil {
		p.t.Errorf("Get: %v", err)
		return
	}
	for _, m := range got.Matches {
		if m.RepoID == repo.RepoID {
			return
		}
	}
	p.t.Errorf("notified %s before its match was persisted: %+v", repo.RepoID, got.Matches)
}

func TestFreshMatchNotifiesAfterCommit(t *testing.T) {
	st, reg := newFixtures(t)
	if err := reg.Merge([]registry.RepoProfile{
		{RepoID: "org/a", Owner: "bob"},
		{RepoID: "org/b", Owner: "carol"},
	}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	srv, _ := fakeGateway(t, func(string) string { return `{"score": 90, "reason": "strong fit"}` })
	pn := &persistedNotifier{t: t, st: st}
	e := &Engine{Store: st, Registry: reg, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}, Notifier: pn}

	idea := createIdea(t, st, "T", "B body")
	if _, err := e.MatchesForIdea(context.Background(), idea); err != nil {
		t.Fatalf("MatchesForIdea: %v", err)
	}
	if pn.calls != 2 {
		t.Fatalf("MatchesForIdea notified %d times, want 2", pn.calls)
	}

	other := createIdea(t, st, "T2", "B2 body")
	rp, err := reg.Get("org/a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := e.ScoreForRepo(context.Background(), other, rp); err != nil {
		t.Fatalf("ScoreForRepo: %v", err)
	}
	if pn.calls != 3 {
		t.Fatalf("ScoreForRepo notified %d times total, want 3", pn.calls)
	}
}
