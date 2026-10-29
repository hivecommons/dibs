package match

import (
	"context"
	"testing"

	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/store"
)

func TestCNCFLegacyCacheRefresh(t *testing.T) {
	for _, title := range []string{"Service mesh", "zxqv blorf"} {
		t.Run(title, func(t *testing.T) {
			cs := cncfTestCatalog(t, []catalog.Project{
				{Name: "Istio", RepoID: "istio/istio", Description: "Service mesh"},
				{Name: "Prometheus", RepoID: "prometheus/prometheus", Description: "Metrics monitoring"},
			})
			srv, calls := fakeGateway(t, func(string) string { return `{"score": 90, "reason": "fits"}` })
			dir := t.TempDir()
			st, err := store.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			idea := &store.Idea{Title: title, Body: title, Author: "u", Visibility: "public"}
			if err := st.Create(idea); err != nil {
				t.Fatal(err)
			}
			// Simulate a pre-fix persisted filler: its LLM score need not be zero.
			if _, err := st.Mutate(idea.ID, false, func(i *store.Idea) error {
				i.CNCFCatalogHash = cs.Fingerprint()
				i.CNCFMatches = []store.CNCFMatch{{RepoID: "prometheus/prometheus", Score: 90, ByLLM: true}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Reopen to exercise the on-disk cache rather than only live state.
			st, err = store.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			e := &Engine{Catalog: cs, Store: st, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
			get := func() *store.Idea {
				t.Helper()
				i, err := st.Get(idea.ID)
				if err != nil {
					t.Fatal(err)
				}
				return i
			}
			want := 0
			if title == "Service mesh" {
				want = 1
			}
			ms, err := e.CNCFMatchesForIdea(context.Background(), get())
			if err != nil {
				t.Fatal(err)
			}
			if ms == nil || len(ms) != want || (want > 0 && ms[0].RepoID != "istio/istio") {
				t.Fatalf("refreshed matches = %+v, want %d keyword fits", ms, want)
			}
			fresh := get()
			if len(fresh.CNCFMatches) != want || fresh.CNCFCatalogHash != e.CatalogFingerprint() {
				t.Fatalf("legacy cache not replaced: %+v", fresh)
			}
			if !fresh.UpdatedAt.Equal(idea.UpdatedAt) {
				t.Fatal("refresh changed idea edit timestamp")
			}
			if _, err := e.CNCFMatchesForIdea(context.Background(), fresh); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != int64(want) {
				t.Fatalf("LLM calls = %d, want %d", calls.Load(), want)
			}
			if !get().MatchesUpdatedAt.Equal(fresh.MatchesUpdatedAt) {
				t.Fatal("cache hit rewrote matches, including empty results")
			}
		})
	}
}
