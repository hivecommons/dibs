package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRepoTickerSymbolEdgeShapes pins the derivation arms the fleet fixture
// never reaches: a name with no letters, a two-letter acronym followed by
// several words (middle initials fill the symbol), a long chain of
// one-letter words (initials stop at four), and a name too short for either
// path, which pads from its own unused letters before falling back to X.
func TestRepoTickerSymbolEdgeShapes(t *testing.T) {
	cases := map[string]string{
		"org/1234":          "XXXX", // no letters at all
		"org/db-sync-tool":  "DBTS", // acronym + last initial + middle initial
		"org/a-b-c-d-e-f":   "ABCD", // initials capped at four
		"org/a-bc":          "ABCX", // initials, no consonants to borrow, pad from letters then X
		"org/go":            "GOXX", // single short word
		"org/kv":            "KVXX", // single short word, all consonants
		"org/queue-io":      "QIQU", // initials, one borrowed consonant, then an unused letter
		"org/x-1-y":         "XYXX", // digits split words but contribute no letters
		"org/trailing-dash": "TRLD", // two words, consonant-led path
	}
	for repoID, want := range cases {
		if got := RepoTickerSymbol(repoID); got != want {
			t.Errorf("RepoTickerSymbol(%q) = %q, want %q", repoID, got, want)
		}
	}
}

// TestValidRepoSymbol: exactly four uppercase ASCII letters.
func TestValidRepoSymbol(t *testing.T) {
	for sym, want := range map[string]bool{
		"HIVE": true, "hive": false, "HIV": false, "HIVES": false, "HIV3": false, "": false, "HÍVE": false,
	} {
		if got := validRepoSymbol(sym); got != want {
			t.Errorf("validRepoSymbol(%q) = %v, want %v", sym, got, want)
		}
	}
}

// TestRepoSymbolRepairOnOpen: a persisted repos.json carrying malformed or
// duplicated symbols is repaired on open — invalid ones are regenerated,
// duplicates keep the first (sorted by RepoID) and reassign the rest — and
// the repaired set is written back so the fix survives a reopen.
func TestRepoSymbolRepairOnOpen(t *testing.T) {
	dir := t.TempDir()
	persisted := []RepoProfile{
		{RepoID: "a/hive", Owner: "x", Symbol: "HIVE"},
		{RepoID: "b/dup", Owner: "x", Symbol: "HIVE"},   // duplicate of a/hive
		{RepoID: "c/lower", Owner: "x", Symbol: "abcd"}, // not uppercase
		{RepoID: "d/short", Owner: "x", Symbol: "AB"},   // wrong length
		{RepoID: "e/ok", Owner: "x", Symbol: "OKAY"},
	}
	raw, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repos.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	check := func(t *testing.T, r *Registry) {
		t.Helper()
		seen := map[string]string{}
		for _, rp := range r.List(false) {
			if !validRepoSymbol(rp.Symbol) {
				t.Errorf("%s: symbol %q still invalid after repair", rp.RepoID, rp.Symbol)
			}
			if prev, dup := seen[rp.Symbol]; dup {
				t.Errorf("symbol %q shared by %s and %s", rp.Symbol, prev, rp.RepoID)
			}
			seen[rp.Symbol] = rp.RepoID
		}
		for id, want := range map[string]string{"a/hive": "HIVE", "e/ok": "OKAY"} {
			rp, err := r.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if rp.Symbol != want {
				t.Errorf("%s: valid symbol %q was rewritten to %q", id, want, rp.Symbol)
			}
		}
		if rp, _ := r.Get("b/dup"); rp.Symbol == "HIVE" {
			t.Error("duplicate symbol on b/dup was not reassigned")
		}
	}
	check(t, r)

	// The repair must have been persisted: reopening sees the same symbols.
	first := map[string]string{}
	for _, rp := range r.List(false) {
		first[rp.RepoID] = rp.Symbol
	}
	r2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	check(t, r2)
	for _, rp := range r2.List(false) {
		if first[rp.RepoID] != rp.Symbol {
			t.Errorf("%s: symbol %q after reopen, was %q", rp.RepoID, rp.Symbol, first[rp.RepoID])
		}
	}
}
