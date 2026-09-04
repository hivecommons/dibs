package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hivecommons/dibs/pkg/api"
	"github.com/hivecommons/dibs/pkg/settle"
)

// TestStatsGamification: /api/me/stats carries the derived score, level,
// progress, and badges — and re-requesting never changes the numbers
// (recompute-safe, no double-award).
func TestStatsGamification(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})

	stats := func() api.IdeatorStats {
		t.Helper()
		rec := doJSON(t, f.h, "GET", "/api/me/stats", "bob-session", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("stats: %d %s", rec.Code, rec.Body.String())
		}
		return decode[api.IdeatorStats](t, rec)
	}

	s := stats()
	if s.Score != 0 || s.Level.Name != "Bronze" || len(s.Badges) != 0 {
		t.Fatalf("fresh user: %+v", s)
	}

	// One draft: +10, First Dibs.
	f.createIdea(t, "bob-session", "Draft", "kubernetes body", "private")
	s = stats()
	if s.Score != 10 || s.Level.Name != "Bronze" {
		t.Fatalf("after draft: %+v", s)
	}
	if len(s.Badges) != 1 || s.Badges[0].ID != "first-dibs" {
		t.Fatalf("first idea should earn first-dibs: %+v", s.Badges)
	}

	// One settled idea: +10+25+50+100 = 185 → 195 total → Silver, plus
	// Rainmaker.
	settled := f.createIdea(t, "bob-session", "Ship it", "kubernetes marketplace body", "public")
	f.settleIdea(t, "bob-session", "alice-session", "kubestellar/dibs", settled)
	s = stats()
	if s.Score != 195 || s.Level.Name != "Silver" {
		t.Fatalf("after settle: %+v", s)
	}
	if s.NextLevel == nil || s.NextLevel.Name != "Gold" || s.ToNext != 105 {
		t.Fatalf("progress wrong: %+v", s)
	}
	ids := map[string]bool{}
	for _, b := range s.Badges {
		ids[b.ID] = true
	}
	if !ids["first-dibs"] || !ids["rainmaker"] {
		t.Fatalf("expected first-dibs + rainmaker: %+v", s.Badges)
	}

	// Retry-safety: hitting the endpoint again awards nothing extra.
	if again := stats(); again.Score != s.Score {
		t.Fatalf("score changed on recompute: %d vs %d", again.Score, s.Score)
	}
}

// TestLeaderboardPublic: the leaderboard is readable WITHOUT a session,
// lists only ideators with a settled idea, ranks by score desc, and never
// leaks idea titles or bodies.
func TestLeaderboardPublic(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})

	// bob: one settled idea + one private draft → 185+10 = 195.
	bobSettled := f.createIdea(t, "bob-session", "Bob wins", "kubernetes marketplace body", "public")
	f.settleIdea(t, "bob-session", "alice-session", "kubestellar/dibs", bobSettled)
	f.createIdea(t, "bob-session", "Bob secret draft", "private thoughts body", "private")

	// charlie: one settled idea only → 185.
	chSettled := f.createIdea(t, "charlie-session", "Charlie ships", "kubernetes operators body", "public")
	f.settleIdea(t, "charlie-session", "alice-session", "kubestellar/dibs", chSettled)

	// alice: ideas but nothing settled → must NOT appear.
	f.createIdea(t, "alice-session", "Alice unsettled", "kubernetes widgets body", "public")

	// No session at all.
	rec := doJSON(t, f.h, "GET", "/api/leaderboard", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public leaderboard: %d %s", rec.Code, rec.Body.String())
	}
	res := decode[struct {
		Leaderboard []api.LeaderboardEntry `json:"leaderboard"`
	}](t, rec)
	if len(res.Leaderboard) != 2 {
		t.Fatalf("expected 2 entries, got %d: %s", len(res.Leaderboard), rec.Body.String())
	}
	first, second := res.Leaderboard[0], res.Leaderboard[1]
	if first.Author != "bob" || first.Rank != 1 || first.Score != 195 || first.Settled != 1 {
		t.Fatalf("first entry wrong: %+v", first)
	}
	if second.Author != "charlie" || second.Rank != 2 || second.Score != 185 {
		t.Fatalf("second entry wrong: %+v", second)
	}
	if first.Level.Name != "Silver" || first.Level.Emoji != "" {
		t.Fatalf("level wrong: %+v", first.Level)
	}
	hasRainmaker := false
	for _, b := range first.Badges {
		if b.ID == "rainmaker" {
			hasRainmaker = true
		}
	}
	if !hasRainmaker {
		t.Fatalf("settled ideator should show rainmaker: %+v", first.Badges)
	}
	// Aggregates only: no idea titles/bodies on the leaderboard.
	body := rec.Body.String()
	for _, leak := range []string{"Bob wins", "Bob secret draft", "private thoughts", "Alice unsettled", `"body"`, `"title"`} {
		if strings.Contains(body, leak) {
			t.Fatalf("leaderboard leaked %q: %s", leak, body)
		}
	}
}

// TestLeaderboardTieBreak: equal scores order by settled count desc, then
// handle asc.
func TestLeaderboardTieBreak(t *testing.T) {
	f := newWave2Server(t, &settle.Fake{})
	// bob and charlie both settle exactly one identical-shape idea → equal
	// score, equal settled → alphabetical.
	b := f.createIdea(t, "bob-session", "B idea", "kubernetes marketplace body", "public")
	f.settleIdea(t, "bob-session", "alice-session", "kubestellar/dibs", b)
	c := f.createIdea(t, "charlie-session", "C idea", "kubernetes marketplace body", "public")
	f.settleIdea(t, "charlie-session", "alice-session", "kubestellar/dibs", c)

	rec := doJSON(t, f.h, "GET", "/api/leaderboard", "", nil)
	res := decode[struct {
		Leaderboard []api.LeaderboardEntry `json:"leaderboard"`
	}](t, rec)
	if len(res.Leaderboard) != 2 || res.Leaderboard[0].Author != "bob" || res.Leaderboard[1].Author != "charlie" {
		t.Fatalf("tie-break ordering wrong: %+v", res.Leaderboard)
	}
}
