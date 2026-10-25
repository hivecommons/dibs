package match

import (
	"testing"

	"github.com/hivecommons/dibs/pkg/store"
)

// TestCapMatchesSortsBeforeCapping: a strong match stored past the cap index
// must survive, and the caller's slice must not be reordered.
func TestCapMatchesSortsBeforeCapping(t *testing.T) {
	n := MaxMatches + 3
	in := make([]store.Match, n)
	for i := range in {
		in[i] = store.Match{RepoID: string(rune('a' + i)), Score: float64(i)}
	}
	orig := append([]store.Match(nil), in...)

	got := CapMatches(in)
	if len(got) != MaxMatches {
		t.Fatalf("len = %d, want %d", len(got), MaxMatches)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Fatalf("not sorted desc at %d: %v < %v", i, got[i-1].Score, got[i].Score)
		}
	}
	if got[0].Score != float64(n-1) {
		t.Fatalf("top score = %v, want %v", got[0].Score, float64(n-1))
	}
	for i := range in {
		if in[i] != orig[i] {
			t.Fatalf("input mutated at %d", i)
		}
	}
}
