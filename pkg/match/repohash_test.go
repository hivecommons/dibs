package match

import (
	"testing"

	"github.com/hivecommons/dibs/pkg/registry"
)

func baseRepoProfile() registry.RepoProfile {
	return registry.RepoProfile{
		RepoID:         "org/base",
		HiveID:         "hive-1",
		Owner:          "bob",
		Description:    "a service",
		Symbol:         "BASE",
		ContributeURL:  "https://hive.example/contribute",
		Topics:         []string{"go", "api"},
		AcceptingIdeas: true,
		Appetite:       "small fixes",
		PassedIdeas:    []string{"idea-1"},
	}
}

// TestRepoHashChangesWithScoringInputs: every RepoProfile field that
// score/scoreDeterministic/boostAccepting read must perturb RepoHash, or an
// edit to it leaves stale cached matches in place (the #401 class of bug).
func TestRepoHashChangesWithScoringInputs(t *testing.T) {
	base := baseRepoProfile()
	baseHash := RepoHash(&base)
	cases := map[string]func(rp *registry.RepoProfile){
		"RepoID":              func(rp *registry.RepoProfile) { rp.RepoID = "org/other" },
		"Description":         func(rp *registry.RepoProfile) { rp.Description = "a different service" },
		"Topics order":        func(rp *registry.RepoProfile) { rp.Topics = []string{"api", "go"} },
		"Topics added":        func(rp *registry.RepoProfile) { rp.Topics = append(rp.Topics, "grpc") },
		"Topics cleared":      func(rp *registry.RepoProfile) { rp.Topics = nil },
		"Appetite":            func(rp *registry.RepoProfile) { rp.Appetite = "big features" },
		"AcceptingIdeas":      func(rp *registry.RepoProfile) { rp.AcceptingIdeas = false },
		"Description cleared": func(rp *registry.RepoProfile) { rp.Description = "" },
	}
	for name, mutate := range cases {
		rp := baseRepoProfile()
		mutate(&rp)
		if got := RepoHash(&rp); got == baseHash {
			t.Errorf("%s: hash unchanged (%q) — cached matches would not be invalidated", name, got)
		}
	}
}

// TestRepoHashIgnoresNonScoringFields: fields the scorer never reads must not
// perturb the hash, or hub syncs and swipe-aways would force a gateway
// rescore of every cached match for the repo.
func TestRepoHashIgnoresNonScoringFields(t *testing.T) {
	base := baseRepoProfile()
	baseHash := RepoHash(&base)
	cases := map[string]func(rp *registry.RepoProfile){
		"HiveID":        func(rp *registry.RepoProfile) { rp.HiveID = "hive-2" },
		"Owner":         func(rp *registry.RepoProfile) { rp.Owner = "carol" },
		"Symbol":        func(rp *registry.RepoProfile) { rp.Symbol = "OTHR" },
		"ContributeURL": func(rp *registry.RepoProfile) { rp.ContributeURL = "" },
		"PassedIdeas":   func(rp *registry.RepoProfile) { rp.PassedIdeas = append(rp.PassedIdeas, "idea-2") },
	}
	for name, mutate := range cases {
		rp := baseRepoProfile()
		mutate(&rp)
		if got := RepoHash(&rp); got != baseHash {
			t.Errorf("%s: hash changed (%q → %q) — would force a needless rescore", name, baseHash, got)
		}
	}
}

// TestRepoHashFieldBoundaries: adjacent fields are NUL-separated, so moving
// text across a field boundary must change the hash. (Topics are joined with
// commas, which GitHub topics cannot contain, so the join is unambiguous for
// real topic lists and is not asserted here.)
func TestRepoHashFieldBoundaries(t *testing.T) {
	a := baseRepoProfile()
	a.Description, a.Appetite = "ab", "c"
	b := baseRepoProfile()
	b.Description, b.Appetite = "a", "bc"
	if RepoHash(&a) == RepoHash(&b) {
		t.Fatal("description/appetite boundary collapsed")
	}

	e := baseRepoProfile()
	e.Topics = []string{"go", ""}
	f := baseRepoProfile()
	f.Topics = []string{"go"}
	if RepoHash(&e) == RepoHash(&f) {
		t.Fatal("trailing empty topic collapsed into the shorter list")
	}

	if h := RepoHash(&a); len(h) != 16 {
		t.Fatalf("hash length %d, want 16", len(h))
	}
}
