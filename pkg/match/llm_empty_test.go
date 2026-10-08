package match

import (
	"context"
	"errors"
	"testing"

	"github.com/hivecommons/dibs/pkg/catalog"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// outcomeRecorded reports whether the current telemetry window holds a
// sample for op/outcome, consuming the window.
func outcomeRecorded(op, outcome string) bool {
	for _, s := range llmStats.snapshotAndReset() {
		if s.Op == op && s.Outcome == outcome && s.Count > 0 {
			return true
		}
	}
	return false
}

// TestLLMScoreEmptyReply: a gateway that answers 200 with a blank message
// (whitespace-only content is trimmed by Chat) is reported as errLLMEmpty,
// not as "no JSON", so the llm_empty outcome label is attributed correctly
// and score() falls back to the lexical score.
func TestLLMScoreEmptyReply(t *testing.T) {
	idea := &store.Idea{Title: "kubernetes", Body: "cluster scheduling"}
	rp := &registry.RepoProfile{RepoID: "org/cluster", Description: "cluster scheduling"}
	for name, reply := range map[string]string{"empty": "", "whitespace": " \n\t "} {
		t.Run(name, func(t *testing.T) {
			srv, calls := fakeGateway(t, func(string) string { return reply })
			e := &Engine{LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
			ctx := context.Background()

			score, reason, err := e.llmScore(ctx, idea, rp)
			if !errors.Is(err, errLLMEmpty) {
				t.Fatalf("llmScore error = %v, want errLLMEmpty", err)
			}
			if score != 0 || reason != "" {
				t.Fatalf("llmScore = %v, %q; want zero result on empty reply", score, reason)
			}
			if outcomeForErr(err) != outcomeLLMEmpty {
				t.Fatalf("outcomeForErr = %q, want %q", outcomeForErr(err), outcomeLLMEmpty)
			}

			llmStats.snapshotAndReset()
			m := e.score(ctx, idea, rp, RepoHash(rp))
			wantScore, wantReason := FallbackScore(idea, rp)
			if m.ByLLM || m.Score != wantScore || m.Reason != wantReason {
				t.Fatalf("score must fall back after empty reply: %+v", m)
			}
			if !outcomeRecorded(opScore, outcomeLLMEmpty) {
				t.Fatalf("expected %s/%s to be recorded", opScore, outcomeLLMEmpty)
			}
			if calls.Load() != 2 {
				t.Fatalf("gateway calls = %d, want 2 (one per scoring attempt)", calls.Load())
			}
		})
	}
}

// TestLLMScoreCNCFEmptyReply: the CNCF scorer has its own copy of the
// empty-reply guard; it must classify a blank reply as errLLMEmpty and
// cncfMatchesForIdea must keep the BM25 baseline while recording llm_empty.
func TestLLMScoreCNCFEmptyReply(t *testing.T) {
	idea := &store.Idea{Title: "service mesh", Body: "sidecar proxy for a service mesh"}
	proj := catalog.Project{Name: "Istio", RepoID: "istio/istio", Description: "Service mesh sidecar proxy"}
	srv, _ := fakeGateway(t, func(string) string { return "   " })
	e := &Engine{LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}

	score, reason, err := e.llmScoreCNCF(context.Background(), idea, proj)
	if !errors.Is(err, errLLMEmpty) {
		t.Fatalf("llmScoreCNCF error = %v, want errLLMEmpty", err)
	}
	if score != 0 || reason != "" {
		t.Fatalf("llmScoreCNCF = %v, %q; want zero result on empty reply", score, reason)
	}

	e.Catalog = cncfTestCatalog(t, []catalog.Project{proj})
	llmStats.snapshotAndReset()
	ms, err := e.CNCFMatchesForIdea(context.Background(), idea)
	if err != nil {
		t.Fatalf("CNCFMatchesForIdea: %v", err)
	}
	if len(ms) != 1 || ms[0].ByLLM || ms[0].RepoID != proj.RepoID {
		t.Fatalf("matches = %+v, want one BM25-only Istio match", ms)
	}
	if !outcomeRecorded(opCNCFScore, outcomeLLMEmpty) {
		t.Fatalf("expected %s/%s to be recorded", opCNCFScore, outcomeLLMEmpty)
	}
}
