// Package game derives the Dibs gamification layer — points, levels, and
// badges — purely from stored idea state. Nothing here is a mutable
// counter: every value is recomputed from the ideas on each call, so
// retries, replays, and crash-recovery can never double-award a point.
package game

import (
	"github.com/kubestellar/dibs/pkg/store"
)

// Points awarded per lifecycle milestone. Milestones are cumulative down
// the funnel — a settled idea has been posted, matched, and accepted, so it
// earns all four awards (10+25+50+100 = 185).
const (
	// PointsPosted: the idea exists.
	PointsPosted = 10
	// PointsMatched: the idea entered matching — it was offered to at
	// least one repo (or otherwise left draft).
	PointsMatched = 25
	// PointsAccepted: a repo said yes.
	PointsAccepted = 50
	// PointsSettled: the ideator filed the credited GitHub issue and
	// confirmed its URL.
	PointsSettled = 100
)

// Level is a named score threshold in the hive hierarchy.
type Level struct {
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
	Min   int    `json:"min"`
}

// Levels ascending by Min. LevelFor depends on this ordering.
var Levels = []Level{
	{Name: "Larva", Emoji: "🐛", Min: 0},
	{Name: "Worker", Emoji: "🐝", Min: 100},
	{Name: "Forager", Emoji: "🍯", Min: 300},
	{Name: "Queen", Emoji: "👑", Min: 750},
}

// matched reports whether the idea entered matching: it carries at least
// one offer, or its status is anything past draft (offered/accepted/
// declined/issue_launched/settled all imply it was in play). Offers and
// status survive idea edits, so the derivation is stable.
func matched(i *store.Idea) bool {
	return len(i.Offers) > 0 || i.Status != store.StatusDraft
}

// accepted reports whether a repo said yes (accepted or any later state).
func accepted(i *store.Idea) bool {
	switch i.Status {
	case store.StatusAccepted, store.StatusIssueLaunched, store.StatusSettled:
		return true
	}
	return false
}

// IdeaPoints is one idea's cumulative score contribution.
func IdeaPoints(i *store.Idea) int {
	pts := PointsPosted
	if matched(i) {
		pts += PointsMatched
	}
	if accepted(i) {
		pts += PointsAccepted
	}
	if i.Status == store.StatusSettled {
		pts += PointsSettled
	}
	return pts
}

// Score is the ideator's total: the sum of IdeaPoints over their ideas.
// Pure function of stored state — recomputing is always safe.
func Score(ideas []*store.Idea) int {
	total := 0
	for _, i := range ideas {
		total += IdeaPoints(i)
	}
	return total
}

// LevelFor returns the level a score sits in and the next level up (nil at
// the top).
func LevelFor(score int) (current Level, next *Level) {
	current = Levels[0]
	for k := range Levels {
		if score >= Levels[k].Min {
			current = Levels[k]
			if k+1 < len(Levels) {
				lv := Levels[k+1]
				next = &lv
			} else {
				next = nil
			}
		}
	}
	return current, next
}

// Progress packages score + level for the API and UI.
type Progress struct {
	Score int   `json:"score"`
	Level Level `json:"level"`
	// NextLevel is nil once the ideator is Queen.
	NextLevel *Level `json:"nextLevel,omitempty"`
	// ToNext is the points still needed for NextLevel (0 at the top).
	ToNext int `json:"toNext"`
	// Pct is progress through the current level band, 0–100 (100 at the
	// top level).
	Pct int `json:"pct"`
}

// ProgressFor derives the full progress view for a score.
func ProgressFor(score int) Progress {
	cur, next := LevelFor(score)
	p := Progress{Score: score, Level: cur, NextLevel: next, Pct: 100}
	if next != nil {
		p.ToNext = next.Min - score
		band := next.Min - cur.Min
		p.Pct = (score - cur.Min) * 100 / band
	}
	return p
}

// Badge is one earned achievement.
type Badge struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
	Desc  string `json:"desc"`
}

// PollinatorRepos is the distinct idea→repo pairings needed for Pollinator.
const PollinatorRepos = 5

// HivemindRepos is the distinct accepting repos needed for Hivemind.
const HivemindRepos = 3

// Badges derives the ideator's earned badges from their ideas. Like Score,
// it is a pure function of stored state.
//
//   - First Dibs 🥇 — posted their first idea.
//   - Pollinator 🌼 — 5+ distinct idea→repo pairings (offers made or repos
//     that accepted).
//   - Hivemind 🧠 — ideas accepted by 3+ distinct repos.
//   - Rainmaker 🌧️ — first settled (filed & confirmed) issue.
func Badges(ideas []*store.Idea) []Badge {
	pairs := 0
	acceptedRepos := map[string]bool{}
	settledCount := 0
	for _, i := range ideas {
		repos := map[string]bool{}
		for _, o := range i.Offers {
			repos[o.RepoID] = true
		}
		if i.TargetRepo != "" {
			repos[i.TargetRepo] = true
			if accepted(i) {
				acceptedRepos[i.TargetRepo] = true
			}
		}
		pairs += len(repos)
		if i.Status == store.StatusSettled {
			settledCount++
		}
	}
	out := []Badge{}
	if len(ideas) >= 1 {
		out = append(out, Badge{ID: "first-dibs", Name: "First Dibs", Emoji: "🥇", Desc: "Posted a first idea"})
	}
	if pairs >= PollinatorRepos {
		out = append(out, Badge{ID: "pollinator", Name: "Pollinator", Emoji: "🌼", Desc: "Matched ideas with 5 repos"})
	}
	if len(acceptedRepos) >= HivemindRepos {
		out = append(out, Badge{ID: "hivemind", Name: "Hivemind", Emoji: "🧠", Desc: "Ideas accepted by 3+ repos"})
	}
	if settledCount >= 1 {
		out = append(out, Badge{ID: "rainmaker", Name: "Rainmaker", Emoji: "🌧️", Desc: "First settled issue filed"})
	}
	return out
}
