package store

import (
	"strings"
	"testing"
)

func TestTickerSymbol(t *testing.T) {
	cases := []struct{ title, want string }{
		{"Dark mode", "DARKMODE"},
		{"Dark mode for dashboards", "DARKMODE"}, // "for" dropped; "DASHBOARDS" doesn't fit
		{"Add offline sync", "OFFLINESYNC"},      // "add" is a stopword
		{"The auto-scaler", "AUTOSCALER"},
		{"a an the of", "IDEA"}, // all stopwords
		{"", "IDEA"},
		{"!!!", "IDEA"},
		{"Supercalifragilistic", "SUPERCALIFRA"}, // single long word truncated
		{"K8s GitOps", "K8SGITOPS"},              // digits kept
		{"émoji ✨ friendly", "MOJIFRIENDLY"},
	}
	for _, c := range cases {
		got := TickerSymbol(c.title)
		if got != c.want {
			t.Errorf("TickerSymbol(%q) = %q, want %q", c.title, got, c.want)
		}
		if len(got) > MaxSymbolLen {
			t.Errorf("TickerSymbol(%q) = %q exceeds %d chars", c.title, got, MaxSymbolLen)
		}
		if got == "" {
			t.Errorf("TickerSymbol(%q) returned empty", c.title)
		}
	}
}

// TestSymbolUniqueness: ideas with colliding titles get uniquified symbols,
// all within the length cap.
func TestSymbolUniqueness(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		idea := &Idea{Author: "alice", AuthorDisplay: "Alice", Title: "Supercalifragilistic idea",
			Body: "body", Visibility: VisibilityPublic}
		if err := s.Create(idea); err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		if idea.Symbol == "" {
			t.Fatalf("Create #%d: no symbol assigned", i)
		}
		if len(idea.Symbol) > MaxSymbolLen {
			t.Fatalf("Create #%d: symbol %q exceeds %d chars", i, idea.Symbol, MaxSymbolLen)
		}
		if seen[idea.Symbol] {
			t.Fatalf("Create #%d: duplicate symbol %q", i, idea.Symbol)
		}
		seen[idea.Symbol] = true
	}
	if !seen["SUPERCALIFRA"] {
		t.Fatalf("first idea should get the plain base symbol, got %v", seen)
	}
}

// TestSymbolSurvivesEditAndReload: the symbol is assigned once and sticks
// through edits and a store reopen (index round-trip).
func TestSymbolSurvivesEditAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idea := &Idea{Author: "alice", AuthorDisplay: "Alice", Title: "Dark mode",
		Body: "body", Visibility: VisibilityPublic}
	if err := s.Create(idea); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sym := idea.Symbol

	edited := *idea
	edited.Title = "Totally renamed"
	if err := s.Update(&edited); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if edited.Symbol != sym {
		t.Fatalf("symbol changed on edit: %q → %q", sym, edited.Symbol)
	}

	// Reopen: the persisted index must still block symbol reuse.
	s2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	dup := &Idea{Author: "bob", AuthorDisplay: "Bob", Title: "Dark mode",
		Body: "body", Visibility: VisibilityPublic}
	if err := s2.Create(dup); err != nil {
		t.Fatalf("Create dup: %v", err)
	}
	if dup.Symbol == sym {
		t.Fatalf("reloaded store reissued symbol %q", sym)
	}
	if !strings.HasPrefix(dup.Symbol, "DARKMODE") {
		t.Fatalf("dup symbol %q should derive from the title", dup.Symbol)
	}
}

// TestUniqueSymbolSuffixTrimming: suffixes never push a symbol past the cap.
func TestUniqueSymbolSuffixTrimming(t *testing.T) {
	taken := map[string]bool{}
	base := "ABCDEFGHIJKL" // exactly MaxSymbolLen
	for i := 0; i < 15; i++ {
		got := uniqueSymbol(base, func(s string) bool { return taken[s] })
		if len(got) > MaxSymbolLen {
			t.Fatalf("iteration %d: %q exceeds %d chars", i, got, MaxSymbolLen)
		}
		if taken[got] {
			t.Fatalf("iteration %d: %q already issued", i, got)
		}
		taken[got] = true
	}
	if !taken[base] || !taken["ABCDEFGHIJK2"] {
		t.Fatalf("expected trimmed suffixed symbols, got %v", taken)
	}
}
