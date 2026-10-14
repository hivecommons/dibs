package match

import "testing"

// TestLLMOutcomesZeroValueRecord pins that record lazily allocates both
// maps, so a zero-value llmOutcomes is usable without the package-level
// initializer.
func TestLLMOutcomesZeroValueRecord(t *testing.T) {
	var c llmOutcomes
	c.record(opScore, outcomeLLMOK)
	c.record(opScore, outcomeLLMOK)

	got := c.snapshotAndReset()
	if len(got) != 1 || got[0] != (llmSample{opScore, outcomeLLMOK, 2}) {
		t.Fatalf("snapshot = %+v, want one %s/%s sample with count 2", got, opScore, outcomeLLMOK)
	}
	if c.totals[llmKey{opScore, outcomeLLMOK}] != 2 {
		t.Fatalf("totals = %+v, want cumulative count 2", c.totals)
	}
}
