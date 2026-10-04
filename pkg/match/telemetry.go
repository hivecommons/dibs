package match

import (
	"errors"
	"log"
	"sort"
	"sync"
	"time"
)

// Fixed label sets: the only values ever used as counter keys, so the
// counter stays bounded (no per-idea or per-repo labels).
const (
	opTLDR      = "tldr"
	opScore     = "score"
	opCNCFScore = "cncf_score"
	opRefine    = "refine"

	outcomeLLMOK      = "llm_ok"
	outcomeLLMError   = "llm_error"
	outcomeLLMEmpty   = "llm_empty"
	outcomeUnparsable = "unparsable"
)

var (
	errLLMEmpty      = errors.New("match: empty llm reply")
	errLLMUnparsable = errors.New("match: unparsable llm reply")
)

type llmKey struct{ Op, Outcome string }

type llmSample struct {
	Op, Outcome string
	Count       int64
}

// llmOutcomes counts LLM call outcomes by (op, outcome) so the
// LLM-to-fallback rate is visible without grepping per-call log lines.
// Log-only, like the HTTP request metrics: no exporter until a backend is
// chosen.
type llmOutcomes struct {
	mu     sync.Mutex
	counts map[llmKey]int64
}

var llmStats = &llmOutcomes{counts: map[llmKey]int64{}}

func (c *llmOutcomes) record(op, outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[llmKey{op, outcome}]++
}

// snapshotAndReset returns the counts sorted deterministically and clears
// them.
func (c *llmOutcomes) snapshotAndReset() []llmSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.counts) == 0 {
		return nil
	}
	samples := make([]llmSample, 0, len(c.counts))
	for k, n := range c.counts {
		samples = append(samples, llmSample{Op: k.Op, Outcome: k.Outcome, Count: n})
	}
	c.counts = map[llmKey]int64{}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Op != samples[j].Op {
			return samples[i].Op < samples[j].Op
		}
		return samples[i].Outcome < samples[j].Outcome
	})
	return samples
}

// logSnapshot flushes accumulated outcome counts to the log and resets them.
func (c *llmOutcomes) logSnapshot() {
	for _, s := range c.snapshotAndReset() {
		log.Printf("metrics: match_llm op=%s outcome=%s count=%d", s.Op, s.Outcome, s.Count)
	}
}

// LogLLMOutcomesPeriodically flushes the matcher's LLM outcome counters on
// a fixed interval. Blocks; run it in a goroutine.
func LogLLMOutcomesPeriodically(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		llmStats.logSnapshot()
	}
}

// outcomeForErr maps an LLM scoring error to its outcome label.
func outcomeForErr(err error) string {
	switch {
	case errors.Is(err, errLLMEmpty):
		return outcomeLLMEmpty
	case errors.Is(err, errLLMUnparsable):
		return outcomeUnparsable
	default:
		return outcomeLLMError
	}
}
