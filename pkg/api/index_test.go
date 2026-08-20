package api

import (
	"reflect"
	"testing"
	"time"

	"github.com/kubestellar/dibs/pkg/history"
	"github.com/kubestellar/dibs/pkg/indexformula"
	"github.com/kubestellar/dibs/pkg/registry"
	"github.com/kubestellar/dibs/pkg/store"
)

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// TestBuildIndexDeterminism: identical inputs produce identical series —
// twice — and the derivation matches the documented weights.
func TestBuildIndexDeterminism(t *testing.T) {
	now := ts(t, "2026-08-19T15:00:00Z")
	old := ts(t, "2026-06-01T10:00:00Z")   // before the 30-day window → folds into base
	d5 := ts(t, "2026-08-15T09:00:00Z")    // inside the window
	d5b := ts(t, "2026-08-15T18:00:00Z")   // same day
	today := ts(t, "2026-08-19T01:00:00Z") // last bucket
	evs := []repoEvent{
		{at: old, weight: indexformula.Contribution(indexformula.Counts{ClankerPRsMerged: 1})},
		{at: d5, weight: indexformula.Contribution(indexformula.Counts{RegularIssuesCreated: 1}), issuesHuman: 1},
		{at: d5b, weight: indexformula.Contribution(indexformula.Counts{RegularPRsMerged: 1})},
		{at: today, weight: indexformula.Contribution(indexformula.Counts{IdeasFiled: 1}), issuesHuman: 1},
	}

	p1, b1, cur1, delta1, act1 := buildIndex(evs, now)
	p2, b2, cur2, delta2, act2 := buildIndex(evs, now)
	if !reflect.DeepEqual(p1, p2) || !reflect.DeepEqual(b1, b2) || cur1 != cur2 || delta1 != delta2 || act1 != act2 {
		t.Fatalf("buildIndex is not deterministic")
	}

	if len(p1) != indexDays || len(b1) != indexDays {
		t.Fatalf("series length: %d points, %d bars, want %d", len(p1), len(b1), indexDays)
	}
	// First bucket: only the pre-window settle in the base, smoothed
	// window of one → base + clanker-merged weight.
	if want := indexBase + indexformula.WeightClankerPRMerged; p1[0].Value != want {
		t.Fatalf("first point %v, want %v", p1[0].Value, want)
	}
	// Last bucket raw level: base + settle + offer + accept + offer.
	// Smoothing (3-day trailing mean) can only pull it down or keep it.
	rawLast := indexBase + indexformula.WeightClankerPRMerged + indexformula.WeightRegularIssueCreated + indexformula.WeightRegularPRMerged + indexformula.WeightIdeaFiled
	if cur1 > rawLast || cur1 <= indexBase {
		t.Fatalf("current %v out of range (base %v, raw %v)", cur1, indexBase, rawLast)
	}
	// Today's offer moved the index up.
	if delta1 <= 0 {
		t.Fatalf("delta %v, want positive (today has activity)", delta1)
	}
	if act1 != 3 { // three events inside the window
		t.Fatalf("activity %d, want 3", act1)
	}
	// Bars land in the right buckets and kinds.
	aug15 := b1[len(b1)-5] // 2026-08-15 is 4 days before the last bucket
	if aug15.T != "2026-08-15" || aug15.IssuesHuman != 1 || aug15.PRsClanker != 0 {
		t.Fatalf("aug15 bar wrong: %+v", aug15)
	}
	last := b1[len(b1)-1]
	if last.T != "2026-08-19" || last.IssuesHuman != 1 || last.PRsClanker != 0 {
		t.Fatalf("last bar wrong: %+v", last)
	}
}

// TestBuildIndexQuietRepo: no events → a flat line at the base, zero delta.
func TestBuildIndexQuietRepo(t *testing.T) {
	points, bars, current, delta, activity := buildIndex(nil, ts(t, "2026-08-19T15:00:00Z"))
	if current != indexBase || delta != 0 || activity != 0 {
		t.Fatalf("quiet repo: current=%v delta=%v activity=%d", current, delta, activity)
	}
	for _, p := range points {
		if p.Value != indexBase {
			t.Fatalf("quiet repo should be flat, got %+v", p)
		}
	}
	for _, b := range bars {
		if b.IssuesHuman != 0 || b.PRsClanker != 0 {
			t.Fatalf("quiet repo should have empty bars, got %+v", b)
		}
	}
}

// TestRepoSymbols: deterministic, unique, length-capped, name-derived.
func TestRepoSymbols(t *testing.T) {
	repos := []registry.RepoProfile{
		{RepoID: "kubestellar/hive"},
		{RepoID: "org/hive"}, // collides with kubestellar/hive's HIVE
		{RepoID: "org/supercalifragilistic-widgets"},
	}
	s1 := repoSymbols(repos)
	// Order of the input slice must not matter.
	s2 := repoSymbols([]registry.RepoProfile{repos[2], repos[0], repos[1]})
	if !reflect.DeepEqual(s1, s2) {
		t.Fatalf("repoSymbols not deterministic: %v vs %v", s1, s2)
	}
	if s1["kubestellar/hive"] != "HIVE" {
		t.Fatalf("kubestellar/hive → %q, want HIVE", s1["kubestellar/hive"])
	}
	if s1["org/hive"] == "HIVE" || s1["org/hive"] == "" {
		t.Fatalf("org/hive must get a uniquified symbol, got %q", s1["org/hive"])
	}
	seen := map[string]bool{}
	for id, sym := range s1 {
		if len(sym) > store.MaxSymbolLen {
			t.Fatalf("%s symbol %q exceeds %d chars", id, sym, store.MaxSymbolLen)
		}
		if seen[sym] {
			t.Fatalf("duplicate symbol %q", sym)
		}
		seen[sym] = true
	}
}

// TestRepoEventsWeights: events pick up the documented weights and kinds,
// and other repos' activity is excluded.
func TestRepoEventsWeights(t *testing.T) {
	decided := ts(t, "2026-08-10T12:00:00Z")
	ideas := []*store.Idea{
		{
			ID: "a", Status: store.StatusSettled, TargetRepo: "org/repo",
			UpdatedAt: ts(t, "2026-08-12T12:00:00Z"),
			Offers: []store.Offer{{
				RepoID: "org/repo", Status: store.OfferAccepted,
				CreatedAt: ts(t, "2026-08-09T12:00:00Z"), DecidedAt: &decided,
			}},
		},
		{
			ID: "b", Status: store.StatusOffered,
			Offers: []store.Offer{{RepoID: "org/other", Status: store.OfferPending,
				CreatedAt: ts(t, "2026-08-11T12:00:00Z")}},
		},
	}
	evs := repoEvents(ideas, "org/repo", nil)
	if len(evs) != 1 {
		t.Fatalf("want 1 idea-filed event, got %d: %+v", len(evs), evs)
	}
	wantWeight := indexformula.Contribution(indexformula.Counts{IdeasFiled: 1})
	if evs[0].weight != wantWeight || evs[0].issuesHuman != 1 {
		t.Fatalf("event = %+v, want weight %v human issue bar", evs[0], wantWeight)
	}
	if evs := repoEvents(ideas, "org/uninvolved", nil); len(evs) != 0 {
		t.Fatalf("uninvolved repo should have no events, got %+v", evs)
	}
}

func TestHistoryEventsUseNativeIndexSemantics(t *testing.T) {
	hist, err := history.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := hist.Upsert("org/repo", []history.DayActivity{{
		Date: "2026-08-18", RegularIssuesCreated: 2, IdeasFiled: 1, MergedPRs: 3, ClankerPRsCreated: 1, ClankerPRsMerged: 1,
		IssuesHuman: 2, PRsClanker: 1,
	}}, ts(t, "2026-08-19T12:00:00Z")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	evs := historyEvents(hist, "org/repo")
	if len(evs) != 2 {
		t.Fatalf("historyEvents len=%d, want 2", len(evs))
	}
	wantIssueWeight := indexformula.Contribution(indexformula.Counts{RegularIssuesCreated: 2, IdeasFiled: 1})
	wantPRWeight := indexformula.Contribution(indexformula.Counts{RegularPRsMerged: 3, ClankerPRsCreated: 1, ClankerPRsMerged: 1})
	if evs[0].weight != wantIssueWeight || evs[0].count != 3 || evs[0].issuesHuman != 2 {
		t.Fatalf("history issue event = %+v, want weight %v count 3 issuesHuman 2", evs[0], wantIssueWeight)
	}
	if evs[1].weight != wantPRWeight || evs[1].count != 5 || evs[1].prsClanker != 1 {
		t.Fatalf("history PR event = %+v, want weight %v count 5 prsClanker 1", evs[1], wantPRWeight)
	}
	_, bars1, cur1, _, act1 := buildIndex(evs, ts(t, "2026-08-19T12:00:00Z"))
	_, bars2, cur2, _, act2 := buildIndex(historyEvents(hist, "org/repo"), ts(t, "2026-08-19T12:00:00Z"))
	if cur1 != cur2 || act1 != act2 || !reflect.DeepEqual(bars1, bars2) {
		t.Fatalf("same persisted backfill should produce same series")
	}
	if bars1[len(bars1)-2].IssuesHuman != 2 || bars1[len(bars1)-2].PRsClanker != 1 {
		t.Fatalf("bars = %+v, want issuesHuman 2 prsClanker 1", bars1[len(bars1)-2])
	}
}

func TestCombinedRepoEventsDedupesBackfilledIdeas(t *testing.T) {
	hist, err := history.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := hist.Upsert("org/repo", []history.DayActivity{
		{Date: "2026-08-18", IdeasFiled: 1},
		{Date: "2026-08-19"},
	}, ts(t, "2026-08-19T12:00:00Z")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	ideas := []*store.Idea{{
		ID: "a", Status: store.StatusSettled, TargetRepo: "org/repo",
		UpdatedAt: ts(t, "2026-08-19T10:00:00Z"),
	}}
	evs := combinedRepoEvents(ideas, hist, "org/repo")
	if len(evs) != 1 {
		t.Fatalf("combined events len=%d, want only the backfilled idea despite next-day confirmation: %+v", len(evs), evs)
	}
	want := indexformula.Contribution(indexformula.Counts{IdeasFiled: 1})
	if evs[0].weight != want || evs[0].issuesHuman != 0 {
		t.Fatalf("event = %+v, want backfilled idea weight %v", evs[0], want)
	}
}
