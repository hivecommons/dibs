package game

import (
	"reflect"
	"testing"

	"github.com/kubestellar/dibs/pkg/store"
)

func idea(status string, offers []string, target string) *store.Idea {
	i := &store.Idea{Status: status, TargetRepo: target}
	for _, r := range offers {
		i.Offers = append(i.Offers, store.Offer{RepoID: r, Status: store.OfferPending})
	}
	return i
}

// TestIdeaPoints pins the cumulative per-milestone awards.
func TestIdeaPoints(t *testing.T) {
	cases := []struct {
		name string
		idea *store.Idea
		want int
	}{
		{"draft", idea(store.StatusDraft, nil, ""), 10},
		{"offered", idea(store.StatusOffered, []string{"a/b"}, ""), 35},
		{"declined still counts matched", idea(store.StatusDeclined, []string{"a/b"}, ""), 35},
		{"accepted", idea(store.StatusAccepted, []string{"a/b"}, "a/b"), 85},
		{"issue_launched", idea(store.StatusIssueLaunched, []string{"a/b"}, "a/b"), 85},
		{"settled", idea(store.StatusSettled, []string{"a/b"}, "a/b"), 185},
		{"direct-accepted public draft (no offer)", idea(store.StatusAccepted, nil, "a/b"), 85},
	}
	for _, c := range cases {
		if got := IdeaPoints(c.idea); got != c.want {
			t.Errorf("%s: IdeaPoints = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestScoreRecomputeIdempotent: Score is a pure derivation — recomputing
// over the same stored state N times never changes the answer (this is the
// no-double-award-on-retry property).
func TestScoreRecomputeIdempotent(t *testing.T) {
	ideas := []*store.Idea{
		idea(store.StatusDraft, nil, ""),
		idea(store.StatusSettled, []string{"a/b"}, "a/b"),
	}
	want := 10 + 185
	for n := 0; n < 5; n++ {
		if got := Score(ideas); got != want {
			t.Fatalf("recompute %d: Score = %d, want %d", n, got, want)
		}
	}
	if Score(nil) != 0 {
		t.Fatalf("empty score should be 0")
	}
}

// TestLevelThresholds pins the level boundaries exactly.
func TestLevelThresholds(t *testing.T) {
	cases := []struct {
		score    int
		want     string
		wantNext string // "" means top level
	}{
		{0, "Larva", "Worker"},
		{99, "Larva", "Worker"},
		{100, "Worker", "Forager"},
		{299, "Worker", "Forager"},
		{300, "Forager", "Queen"},
		{749, "Forager", "Queen"},
		{750, "Queen", ""},
		{10000, "Queen", ""},
	}
	for _, c := range cases {
		cur, next := LevelFor(c.score)
		if cur.Name != c.want {
			t.Errorf("LevelFor(%d) = %s, want %s", c.score, cur.Name, c.want)
		}
		switch {
		case c.wantNext == "" && next != nil:
			t.Errorf("LevelFor(%d) next = %s, want none", c.score, next.Name)
		case c.wantNext != "" && (next == nil || next.Name != c.wantNext):
			t.Errorf("LevelFor(%d) next = %v, want %s", c.score, next, c.wantNext)
		}
	}
}

// TestProgressFor pins the progress-bar math.
func TestProgressFor(t *testing.T) {
	p := ProgressFor(150)
	if p.Level.Name != "Worker" || p.NextLevel.Name != "Forager" || p.ToNext != 150 || p.Pct != 25 {
		t.Fatalf("ProgressFor(150) = %+v", p)
	}
	p = ProgressFor(750)
	if p.Level.Name != "Queen" || p.NextLevel != nil || p.ToNext != 0 || p.Pct != 100 {
		t.Fatalf("ProgressFor(750) = %+v", p)
	}
	p = ProgressFor(0)
	if p.Level.Name != "Larva" || p.Pct != 0 || p.ToNext != 100 {
		t.Fatalf("ProgressFor(0) = %+v", p)
	}
}

func badgeIDs(bs []Badge) []string {
	ids := []string{}
	for _, b := range bs {
		ids = append(ids, b.ID)
	}
	return ids
}

// TestBadges pins every badge rule, including their thresholds.
func TestBadges(t *testing.T) {
	if got := Badges(nil); len(got) != 0 {
		t.Fatalf("no ideas → no badges, got %v", got)
	}

	// One draft: First Dibs only.
	one := []*store.Idea{idea(store.StatusDraft, nil, "")}
	if got := badgeIDs(Badges(one)); !reflect.DeepEqual(got, []string{"first-dibs"}) {
		t.Fatalf("one draft: %v", got)
	}

	// Pollinator: 5 distinct idea→repo pairings; 4 is not enough.
	four := []*store.Idea{idea(store.StatusOffered, []string{"a/1", "a/2", "a/3", "a/4"}, "")}
	if got := badgeIDs(Badges(four)); !reflect.DeepEqual(got, []string{"first-dibs"}) {
		t.Fatalf("4 pairings should not earn pollinator: %v", got)
	}
	five := []*store.Idea{
		idea(store.StatusOffered, []string{"a/1", "a/2", "a/3"}, ""),
		idea(store.StatusOffered, []string{"a/4", "a/5"}, ""),
	}
	if got := badgeIDs(Badges(five)); !reflect.DeepEqual(got, []string{"first-dibs", "pollinator"}) {
		t.Fatalf("5 pairings: %v", got)
	}
	// A direct accept (TargetRepo, no offer) counts as a pairing, but a
	// repo both offered-to and accepting is one pairing, not two.
	dedup := []*store.Idea{
		idea(store.StatusAccepted, []string{"a/1"}, "a/1"),
		idea(store.StatusOffered, []string{"a/2", "a/3", "a/4"}, ""),
	}
	if got := badgeIDs(Badges(dedup)); !reflect.DeepEqual(got, []string{"first-dibs"}) {
		t.Fatalf("offer+accept on same repo must dedupe to 4 pairings: %v", got)
	}

	// Hivemind: accepted by 3+ DISTINCT repos. Two repos (one twice) is not
	// enough.
	twoRepos := []*store.Idea{
		idea(store.StatusAccepted, nil, "a/1"),
		idea(store.StatusSettled, nil, "a/1"),
		idea(store.StatusAccepted, nil, "a/2"),
	}
	for _, id := range badgeIDs(Badges(twoRepos)) {
		if id == "hivemind" {
			t.Fatalf("2 distinct repos must not earn hivemind")
		}
	}
	threeRepos := append(twoRepos, idea(store.StatusIssueLaunched, nil, "a/3"))
	found := map[string]bool{}
	for _, id := range badgeIDs(Badges(threeRepos)) {
		found[id] = true
	}
	if !found["hivemind"] {
		t.Fatalf("3 distinct accepting repos should earn hivemind: %v", badgeIDs(Badges(threeRepos)))
	}
	if !found["rainmaker"] {
		t.Fatalf("a settled idea should earn rainmaker: %v", badgeIDs(Badges(threeRepos)))
	}
	// A declined TargetRepo-less idea never contributes to hivemind, and an
	// accepted-but-unsettled portfolio has no rainmaker.
	noRain := []*store.Idea{idea(store.StatusAccepted, nil, "a/1")}
	for _, id := range badgeIDs(Badges(noRain)) {
		if id == "rainmaker" {
			t.Fatalf("unsettled ideas must not earn rainmaker")
		}
	}
}

// TestBadgesRecomputeIdempotent: badges, like the score, derive from state.
func TestBadgesRecomputeIdempotent(t *testing.T) {
	ideas := []*store.Idea{idea(store.StatusSettled, []string{"a/1"}, "a/1")}
	first := Badges(ideas)
	for n := 0; n < 3; n++ {
		if got := Badges(ideas); !reflect.DeepEqual(got, first) {
			t.Fatalf("recompute %d changed badges: %v vs %v", n, got, first)
		}
	}
}
