package match

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/store"
)

func cncfTestCatalog(t *testing.T, projects []catalog.Project) *catalog.Store {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(projects)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(dir+"/cncf-catalog.json", raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cs, err := catalog.New(dir, "")
	if err != nil {
		t.Fatalf("catalog.New: %v", err)
	}
	return cs
}

// TestCNCFMatchesNilEngineOrCatalog: both guard arms return an empty,
// non-nil slice without error.
func TestCNCFMatchesNilEngineOrCatalog(t *testing.T) {
	var nilEngine *Engine
	idea := &store.Idea{Title: "anything"}
	for name, e := range map[string]*Engine{"nil engine": nilEngine, "nil catalog": {}} {
		ms, err := e.CNCFMatchesForIdea(context.Background(), idea)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ms == nil || len(ms) != 0 {
			t.Fatalf("%s: matches = %#v, want empty non-nil slice", name, ms)
		}
	}
}

// TestCNCFMatchesNoCandidates: an empty catalog yields no candidates and an
// empty result before any scoring.
func TestCNCFMatchesNoCandidates(t *testing.T) {
	dir := t.TempDir()
	cs, err := catalog.New(dir, "")
	if err != nil {
		t.Fatalf("catalog.New: %v", err)
	}
	e := &Engine{Catalog: cs}
	ms, err := e.CNCFMatchesForIdea(context.Background(), &store.Idea{Title: "service mesh"})
	if err != nil {
		t.Fatalf("CNCFMatchesForIdea: %v", err)
	}
	if ms == nil || len(ms) != 0 {
		t.Fatalf("matches = %#v, want empty non-nil slice", ms)
	}
}

// TestCNCFMatchesProgressAndPersist drives the internal entry point the way
// RematchIdea does — with a progress sink — but with persist enabled, and
// asserts both the event stream shape and the persisted CNCFMatches.
func TestCNCFMatchesProgressAndPersist(t *testing.T) {
	cs := cncfTestCatalog(t, []catalog.Project{
		{Name: "Istio", RepoID: "istio/istio", RepoURL: "https://github.com/istio/istio", Maturity: "graduated", Category: "Service Proxy", Description: "Service mesh sidecar proxy"},
		{Name: "Vitess", RepoID: "vitessio/vitess", RepoURL: "https://github.com/vitessio/vitess", Maturity: "incubating", Category: "Database", Description: "MySQL clustering database mesh"},
	})
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	idea := &store.Idea{Title: "Service mesh sidecar proxy", Body: "Route traffic through sidecars.", Author: "u", Visibility: "public"}
	if err := st.Create(idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	e := &Engine{Catalog: cs, Store: st}

	var events []ProgressEvent
	ms, err := e.cncfMatchesForIdea(context.Background(), idea, true, func(ev ProgressEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("cncfMatchesForIdea: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("matches = %+v, want 2", ms)
	}

	// Event stream: one cncf_bm25_start, one cncf_bm25 per candidate, one
	// cncf_score per candidate — in that order.
	if len(events) != 5 {
		t.Fatalf("got %d events %+v, want 5", len(events), events)
	}
	if events[0].Phase != "cncf_bm25_start" || events[0].Done != 2 || events[0].Total != 2 ||
		!strings.Contains(events[0].Note, "2 projects scanned") {
		t.Fatalf("start event = %+v", events[0])
	}
	for i, ev := range events[1:3] {
		if ev.Phase != "cncf_bm25" || ev.Done != i+1 || ev.Total != 2 || ev.RepoID == "" || ev.Note == "" {
			t.Fatalf("bm25 event %d = %+v", i, ev)
		}
	}
	for i, ev := range events[3:5] {
		if ev.Phase != "cncf_score" || ev.Done != i+1 || ev.Total != 2 || ev.ByLLM {
			t.Fatalf("score event %d = %+v", i, ev)
		}
	}

	// Persisted: the store copy carries the matches and a fresh timestamp.
	got, err := st.Get(idea.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.CNCFMatches) != 2 || got.CNCFMatches[0].RepoID != ms[0].RepoID {
		t.Fatalf("persisted CNCFMatches = %+v, want %+v", got.CNCFMatches, ms)
	}
	if got.MatchesUpdatedAt.IsZero() {
		t.Fatal("MatchesUpdatedAt not set")
	}
}

// TestCNCFMatchesPersistError: a store miss on persist surfaces as the
// call's error.
func TestCNCFMatchesPersistError(t *testing.T) {
	cs := cncfTestCatalog(t, []catalog.Project{
		{Name: "Istio", RepoID: "istio/istio", Description: "Service mesh sidecar proxy"},
	})
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	e := &Engine{Catalog: cs, Store: st}
	ghost := &store.Idea{ID: "no-such-idea", Title: "Service mesh sidecar proxy"}
	if _, err := e.cncfMatchesForIdea(context.Background(), ghost, true, nil); err == nil {
		t.Fatal("persist against a missing idea succeeded")
	}
}

// TestCNCFMatchesLLMFailureFallsBackToBM25: a reply with no JSON object
// keeps the BM25 score and reason (ByLLM false) instead of failing the call.
func TestCNCFMatchesLLMFailureFallsBackToBM25(t *testing.T) {
	cs := cncfTestCatalog(t, []catalog.Project{
		{Name: "Istio", RepoID: "istio/istio", Description: "Service mesh sidecar proxy"},
	})
	srv, calls := fakeGateway(t, func(string) string { return "sorry, cannot help" })
	e := &Engine{Catalog: cs, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
	ms, err := e.CNCFMatchesForIdea(context.Background(), &store.Idea{Title: "Service mesh sidecar proxy"})
	if err != nil {
		t.Fatalf("CNCFMatchesForIdea: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("llm calls = %d, want 1", calls.Load())
	}
	if len(ms) != 1 || ms[0].ByLLM || !strings.Contains(ms[0].Reason, "BM25") {
		t.Fatalf("matches = %+v, want BM25 fallback", ms)
	}
}

// TestLLMScoreCNCFErrorAndClamps pins llmScoreCNCF's failure arms (transport
// error, unparseable JSON) and its 0/100 score clamps.
func TestLLMScoreCNCFErrorAndClamps(t *testing.T) {
	idea := &store.Idea{Title: "t", Body: "b"}
	proj := catalog.Project{Name: "Istio", RepoID: "istio/istio"}

	t.Run("transport error", func(t *testing.T) {
		srv, _ := fakeGateway(t, func(string) string { return "{}" })
		srv.Close()
		e := &Engine{LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
		if _, _, err := e.llmScoreCNCF(context.Background(), idea, proj); err == nil {
			t.Fatal("closed gateway scored successfully")
		}
	})

	t.Run("bad json types", func(t *testing.T) {
		srv, _ := fakeGateway(t, func(string) string { return `{"score": "high", "reason": "x"}` })
		e := &Engine{LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
		if _, _, err := e.llmScoreCNCF(context.Background(), idea, proj); err == nil {
			t.Fatal("non-numeric score parsed successfully")
		}
	})

	for _, tc := range []struct {
		raw  float64
		want float64
	}{{-5, 0}, {900, 100}} {
		t.Run(fmt.Sprintf("clamp %v", tc.raw), func(t *testing.T) {
			srv, _ := fakeGateway(t, func(string) string {
				return fmt.Sprintf(`{"score": %v, "reason": "fit"}`, tc.raw)
			})
			e := &Engine{LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
			score, reason, err := e.llmScoreCNCF(context.Background(), idea, proj)
			if err != nil {
				t.Fatalf("llmScoreCNCF: %v", err)
			}
			if score != tc.want || reason != "fit" {
				t.Fatalf("score=%v reason=%q, want %v/fit", score, reason, tc.want)
			}
		})
	}
}
