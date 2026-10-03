package match

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

func TestLLMScoreErrorsAndClamps(t *testing.T) {
	idea := &store.Idea{Title: "kubernetes", Body: "cluster scheduling"}
	rp := &registry.RepoProfile{RepoID: "org/cluster", Description: "cluster scheduling"}
	for _, tc := range []struct {
		name, reply, errorText string
		chatError, jsonError   bool
		want                   float64
	}{
		{name: "chat error", chatError: true, errorText: "match: llm gateway unreachable"},
		{name: "prose only", reply: "Sorry, no score available", errorText: "match: no JSON in llm reply"},
		{name: "malformed JSON", reply: `{"score": }`, jsonError: true},
		{name: "negative clamp", reply: `{"score": -5, "reason": "fit"}`, want: 0},
		{name: "upper clamp", reply: `{"score": 250, "reason": "fit"}`, want: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeGateway(t, func(string) string { return tc.reply })
			e := &Engine{LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
			ctx := context.Background()
			if tc.chatError {
				// Cancellation makes the transport error deterministic without dialing an external service.
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			score, reason, err := e.llmScore(ctx, idea, rp)
			if tc.chatError || tc.jsonError || tc.errorText != "" {
				if err == nil || score != 0 || reason != "" {
					t.Fatalf("llmScore = %v, %q, %v; want zero result and error", score, reason, err)
				}
				if tc.chatError && !errors.Is(err, context.Canceled) {
					t.Fatalf("want context cancellation, got %v", err)
				}
				if tc.errorText != "" && !strings.Contains(err.Error(), tc.errorText) {
					t.Fatalf("error = %v, want %q", err, tc.errorText)
				}
				var syntaxErr *json.SyntaxError
				if tc.jsonError && !errors.As(err, &syntaxErr) {
					t.Fatalf("want JSON syntax error, got %v", err)
				}
				m := e.score(ctx, idea, rp, RepoHash(rp))
				wantScore, wantReason := FallbackScore(idea, rp)
				if m.ByLLM || m.Score != wantScore || m.Reason != wantReason {
					t.Fatalf("score must fall back after LLM error: %+v", m)
				}
				return
			}
			if err != nil || score != tc.want || reason != "fit" {
				t.Fatalf("llmScore = %v, %q, %v; want %v, fit, nil", score, reason, err, tc.want)
			}
		})
	}
}

func TestBM25ToScoreBounds(t *testing.T) {
	for _, tc := range []struct{ score, top, want float64 }{
		{150, 100, 100}, {100, 0, 0}, {100, -1, 0}, {50, 100, 50},
	} {
		if got := bm25ToScore(tc.score, tc.top); got != tc.want {
			t.Errorf("bm25ToScore(%v, %v) = %v, want %v", tc.score, tc.top, got, tc.want)
		}
	}
}

func TestRematchNilEngine(t *testing.T) {
	var e *Engine
	tldr, hive, cncf, err := e.RematchIdea(context.Background(), nil, false, nil)
	if err == nil || err.Error() != "match: engine is nil" || tldr != "" || hive != nil || cncf != nil {
		t.Fatalf("RematchIdea = %q, %+v, %+v, %v", tldr, hive, cncf, err)
	}
}

func TestRematchProgressAndRerankCap(t *testing.T) {
	st, reg := newFixtures(t)
	const repoCount = MaxCNCFCandidates + 2
	var repos []registry.RepoProfile
	for i := 0; i < repoCount; i++ {
		repos = append(repos, registry.RepoProfile{RepoID: fmt.Sprintf("org/cluster-%02d", i), Description: "kubernetes cluster scheduling", Symbol: "K", Owner: "bob"})
	}
	if err := reg.Merge(repos); err != nil {
		t.Fatal(err)
	}
	srv, calls := fakeGateway(t, func(user string) string {
		if strings.HasPrefix(user, "Title:") {
			return "A kubernetes cluster scheduling idea."
		}
		return `{"score": 80, "reason": "fit"}`
	})
	rec := &notifyRecorder{}
	e := &Engine{Store: st, Registry: reg, LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}, Notifier: rec}
	idea := createIdea(t, st, "kubernetes", "cluster scheduling")
	var events []ProgressEvent
	tldr, hive, cncf, err := e.RematchIdea(context.Background(), idea, false, func(ev ProgressEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if tldr != "A kubernetes cluster scheduling idea." || len(hive) != RematchHiveKeep || len(cncf) != 0 {
		t.Fatalf("unexpected result: %q, %+v, %+v", tldr, hive, cncf)
	}
	if calls.Load() != 1+MaxCNCFCandidates {
		t.Fatalf("gateway calls = %d, want TLDR + %d capped scores", calls.Load(), MaxCNCFCandidates)
	}
	wantPhases := []string{"tldr_start", "tldr_done", "hive_start"}
	for i := 0; i < repoCount; i++ {
		wantPhases = append(wantPhases, "hive_score")
	}
	for i := 0; i < MaxCNCFCandidates; i++ {
		wantPhases = append(wantPhases, "hive_rerank")
	}
	wantPhases = append(wantPhases, "final_selection", "done")
	var phases []string
	for _, ev := range events {
		phases = append(phases, ev.Phase)
	}
	if !reflect.DeepEqual(phases, wantPhases) {
		t.Fatalf("phases = %v, want %v", phases, wantPhases)
	}
	if events[1].Note != tldr || events[2].Total != repoCount {
		t.Fatalf("TLDR/start events: %+v", events[:3])
	}
	for i, ev := range events[3+repoCount : 3+repoCount+MaxCNCFCandidates] {
		rp, err := reg.Get(ev.RepoID)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Done != i+1 || ev.Total != MaxCNCFCandidates || !ev.ByLLM || ev.Symbol != rp.Symbol || ev.Note != "fit" || ev.Score != blendScores(100, 80) {
			t.Fatalf("rerank event %d: %+v", i, ev)
		}
	}
	// The two candidates outside the rerank budget retain their 100-point
	// baseline, so they sort ahead of the 92-point blended candidates.
	for i, m := range hive {
		wantLLM := i >= repoCount-MaxCNCFCandidates
		wantScore := 100.0
		if wantLLM {
			wantScore = blendScores(100, 80)
		}
		if m.ByLLM != wantLLM || m.Score != wantScore {
			t.Fatalf("selected match %d: %+v", i, m)
		}
	}
	stored, err := st.Get(idea.ID)
	if err != nil {
		t.Fatal(err)
	}
	if idea.TLDR != "" || stored.TLDR != "" || len(stored.Matches) != 0 || !stored.MatchesUpdatedAt.IsZero() || len(rec.calls) != 0 {
		t.Fatal("preview must not modify the idea, persist results, or notify")
	}
}

func TestDeletedIdeaPersistenceErrors(t *testing.T) {
	for _, name := range []string{"EnsureTLDR", "MatchesForIdea", "RematchIdea"} {
		t.Run(name, func(t *testing.T) {
			st, reg := newFixtures(t)
			if err := reg.Merge([]registry.RepoProfile{{RepoID: "org/cluster", Description: "kubernetes cluster"}}); err != nil {
				t.Fatal(err)
			}
			idea := createIdea(t, st, "kubernetes", "cluster scheduling")
			if err := st.Delete(idea.ID); err != nil {
				t.Fatal(err)
			}
			rec := &notifyRecorder{}
			e := &Engine{Store: st, Registry: reg, Notifier: rec}
			var err error
			switch name {
			case "EnsureTLDR":
				var tldr string
				tldr, err = e.EnsureTLDR(context.Background(), idea)
				if tldr != "" || idea.TLDR != "" {
					t.Fatal("failed TLDR persistence must return no TLDR and leave input unchanged")
				}
			case "MatchesForIdea":
				var matches []store.Match
				matches, err = e.MatchesForIdea(context.Background(), idea)
				if matches != nil {
					t.Fatalf("failed persistence returned matches: %+v", matches)
				}
			case "RematchIdea":
				var phases []string
				var tldr string
				var hive []store.Match
				var cncf []CNCFMatch
				tldr, hive, cncf, err = e.RematchIdea(context.Background(), idea, true, func(ev ProgressEvent) { phases = append(phases, ev.Phase) })
				if tldr != "" || hive != nil || cncf != nil || phases[len(phases)-1] != "final_selection" || len(rec.calls) != 0 {
					t.Fatalf("failed rematch: %q, %+v, %+v, phases %v, notifications %+v", tldr, hive, cncf, phases, rec.calls)
				}
			}
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
		})
	}
}

func TestGenerateTLDRChatErrorFallsBack(t *testing.T) {
	srv, _ := fakeGateway(t, func(string) string { return "unused" })
	e := &Engine{LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	idea := &store.Idea{Body: "First paragraph.\n\nSecond paragraph."}
	if got := e.generateTLDR(ctx, idea); got != "First paragraph." {
		t.Fatalf("generateTLDR = %q, want fallback", got)
	}
}

func TestFallbackScoreNoProfileTerms(t *testing.T) {
	score, reason := FallbackScore(&store.Idea{Title: "kubernetes"}, &registry.RepoProfile{RepoID: "a/b"})
	if score != 0 || reason != "no repo profile terms to match against" {
		t.Fatalf("FallbackScore = %v, %q", score, reason)
	}
}
