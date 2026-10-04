package match

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hivecommons/dibs/pkg/store"
)

func TestOutcomeForErr(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errLLMEmpty, outcomeLLMEmpty},
		{fmt.Errorf("%w: x", errLLMUnparsable), outcomeUnparsable},
		{errors.New("boom"), outcomeLLMError},
	}
	for _, tt := range tests {
		if got := outcomeForErr(tt.err); got != tt.want {
			t.Errorf("outcomeForErr(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestLLMOutcomesSnapshotAndReset(t *testing.T) {
	c := &llmOutcomes{counts: map[llmKey]int64{}}
	if got := c.snapshotAndReset(); got != nil {
		t.Fatalf("empty snapshot = %v, want nil", got)
	}
	c.record(opTLDR, outcomeLLMOK)
	c.record(opScore, outcomeLLMError)
	c.record(opScore, outcomeLLMError)
	c.record(opScore, outcomeLLMEmpty)
	got := c.snapshotAndReset()
	want := []llmSample{
		{opScore, outcomeLLMEmpty, 1},
		{opScore, outcomeLLMError, 2},
		{opTLDR, outcomeLLMOK, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
	if again := c.snapshotAndReset(); again != nil {
		t.Fatalf("snapshot after reset = %v, want nil", again)
	}
	c.record(opRefine, outcomeUnparsable)
	c.logSnapshot()
	if again := c.snapshotAndReset(); again != nil {
		t.Fatalf("logSnapshot must reset, got %v", again)
	}
}

func TestGenerateTLDREmptyReplyRecorded(t *testing.T) {
	srv, _ := fakeGateway(t, func(string) string { return "" })
	e := &Engine{LLM: &LLM{BaseURL: srv.URL + "/v1", Model: "test"}}
	llmStats.snapshotAndReset()
	idea := &store.Idea{Body: "First paragraph.\n\nSecond paragraph."}
	if got := e.generateTLDR(context.Background(), idea); got != "First paragraph." {
		t.Fatalf("generateTLDR = %q, want fallback", got)
	}
	for _, s := range llmStats.snapshotAndReset() {
		if s.Op == opTLDR && (s.Outcome == outcomeLLMEmpty || s.Outcome == outcomeLLMError) {
			return
		}
	}
	t.Fatal("expected a tldr fallback outcome to be recorded")
}
