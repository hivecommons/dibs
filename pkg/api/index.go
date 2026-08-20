// Repo value index: the derived "stock price" of a hive-managed repo,
// powering the landing's terminal chart and the repo tickers on the tape.
//
// The index is an explicit weighted composite over aggregate daily activity:
// regular GitHub issues, regular merged PRs, ClankeR-created PRs,
// Dibs-filed idea issues, and ClankeR-merged PRs. The weights and formula
// live in pkg/indexformula and are shared by backfilled GitHub history and
// live Dibs-native idea filing movement.
//
//	index(day) = indexBase + Σ daily composite contributions,
//	then smoothed with a trailing 3-day mean.
//
// Buckets are daily over the last indexDays days; events older than the
// window fold into the base so long-lived repos keep their level.
package api

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubestellar/dibs/pkg/history"
	"github.com/kubestellar/dibs/pkg/indexformula"
	"github.com/kubestellar/dibs/pkg/registry"
	"github.com/kubestellar/dibs/pkg/store"
)

// Index derivation constants (see package comment).
const (
	indexBase    = 100.0
	indexDays    = 30
	smoothWindow = 3
)

// repoEvent is one weighted activity event against a repo.
type repoEvent struct {
	at     time.Time
	weight float64
	count  int
	// kind buckets the sub-chart bars: "ideas" (offers — listing/match
	// interest) or "agent" (accept/settle — implementation activity; an
	// honest PROXY until real token metering exists).
	kind string
}

func (e repoEvent) barCount() int {
	if e.count > 0 {
		return e.count
	}
	return 1
}

// IndexPoint is one smoothed daily index value.
type IndexPoint struct {
	T     string  `json:"t"` // YYYY-MM-DD (UTC)
	Value float64 `json:"value"`
}

// IndexBar is one day's activity bars for the sub-chart.
type IndexBar struct {
	T     string `json:"t"`
	Ideas int    `json:"ideas"` // regular issues plus Dibs-filed ideas
	Agent int    `json:"agent"` // regular and ClankeR PR activity
}

// RepoIndex is the full chart payload for one repo.
type RepoIndex struct {
	RepoID  string       `json:"repoID"`
	Symbol  string       `json:"symbol"`
	Points  []IndexPoint `json:"points"`
	Bars    []IndexBar   `json:"bars"`
	Current float64      `json:"current"`
	Delta   float64      `json:"delta"` // today vs yesterday
}

// RepoTicker is one listed security on the tape / chip row.
type RepoTicker struct {
	RepoID   string  `json:"repoID"`
	Symbol   string  `json:"symbol"`
	Value    float64 `json:"value"`
	Delta    float64 `json:"delta"`
	Activity int     `json:"activity"` // events in the window — sort key
}

// repoSymbols assigns each registered repo a deterministic, uniquified
// ticker symbol derived from its name ("kubestellar/hive" → HIVE). Repos
// are processed in sorted RepoID order so the assignment is stable across
// requests without persisting anything.
func repoSymbols(repos []registry.RepoProfile) map[string]string {
	ids := make([]string, 0, len(repos))
	for _, rp := range repos {
		ids = append(ids, rp.RepoID)
	}
	sort.Strings(ids)
	taken := map[string]bool{}
	out := map[string]string{}
	for _, id := range ids {
		name := id
		if i := strings.LastIndexByte(id, '/'); i >= 0 {
			name = id[i+1:]
		}
		base := store.TickerSymbol(name)
		sym := base
		for n := 2; taken[sym]; n++ {
			suffix := strconv.Itoa(n)
			stem := base
			if len(stem)+len(suffix) > store.MaxSymbolLen {
				stem = stem[:store.MaxSymbolLen-len(suffix)]
			}
			sym = stem + suffix
		}
		taken[sym] = true
		out[id] = sym
	}
	return out
}

// repoEvents collects live Dibs-native idea filings from the store.
// Aggregate-only: nothing about any idea's content or visibility leaves
// this function — just timestamps and weights.
func repoEvents(ideas []*store.Idea, repoID string, coveredIdeas *ideaCoverage) []repoEvent {
	coveredIdeas = coveredIdeas.clone()
	var evs []repoEvent
	for _, idea := range ideas {
		if idea.TargetRepo == repoID && idea.Status == store.StatusSettled {
			date := idea.UpdatedAt.UTC().Format("2006-01-02")
			if coveredIdeas.covers(date, idea.UpdatedAt) {
				continue
			}
			evs = append(evs, repoEvent{at: idea.UpdatedAt, weight: indexformula.Contribution(indexformula.Counts{IdeasFiled: 1}), kind: "ideas"})
		}
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].at.Before(evs[j].at) })
	return evs
}

type ideaCoverage struct {
	byDate    map[string]int
	total     int
	startDate string
	endDate   string
	fetchedAt time.Time
}

func (c *ideaCoverage) clone() *ideaCoverage {
	if c == nil {
		return nil
	}
	out := *c
	out.byDate = make(map[string]int, len(c.byDate))
	for date, n := range c.byDate {
		out.byDate[date] = n
	}
	return &out
}

func (c *ideaCoverage) covers(date string, at time.Time) bool {
	if c == nil || c.total == 0 || at.After(c.fetchedAt) {
		return false
	}
	if c.byDate[date] > 0 {
		c.byDate[date]--
		c.total--
		return true
	}
	if c.startDate != "" && c.startDate <= date && date <= c.endDate {
		c.total--
		return true
	}
	return false
}

func historyIdeaCoverage(hist *history.Store, repoID string) *ideaCoverage {
	if hist == nil {
		return nil
	}
	h, ok := hist.Get(repoID)
	if !ok || len(h.Days) == 0 {
		return nil
	}
	out := &ideaCoverage{byDate: map[string]int{}, startDate: h.Days[0].Date, endDate: h.Days[len(h.Days)-1].Date, fetchedAt: h.FetchedAt}
	for _, d := range h.Days {
		if d.IdeasFiled > 0 {
			out.byDate[d.Date] += d.IdeasFiled
			out.total += d.IdeasFiled
		}
	}
	return out
}

func combinedRepoEvents(ideas []*store.Idea, hist *history.Store, repoID string) []repoEvent {
	evs := append(repoEvents(ideas, repoID, historyIdeaCoverage(hist, repoID)), historyEvents(hist, repoID)...)
	sort.Slice(evs, func(i, j int) bool { return evs[i].at.Before(evs[j].at) })
	return evs
}

// historyEvents converts backfilled GitHub activity into weighted composite
// events using the same formula as live Dibs-native movement.
func historyEvents(hist *history.Store, repoID string) []repoEvent {
	if hist == nil {
		return nil
	}
	h, ok := hist.Get(repoID)
	if !ok {
		return nil
	}
	evs := make([]repoEvent, 0, len(h.Days)*2)
	for _, d := range h.Days {
		at, err := time.Parse("2006-01-02", d.Date)
		if err != nil {
			continue
		}
		issueCounts := indexformula.Counts{RegularIssuesCreated: d.RegularIssuesCreated, IdeasFiled: d.IdeasFiled}
		if w := indexformula.Contribution(issueCounts); w != 0 {
			evs = append(evs, repoEvent{at: at, weight: w, count: indexformula.Activity(issueCounts), kind: "ideas"})
		}
		prCounts := indexformula.Counts{
			RegularPRsMerged:  d.MergedPRs,
			ClankerPRsCreated: d.ClankerPRsCreated,
			ClankerPRsMerged:  d.ClankerPRsMerged,
		}
		if w := indexformula.Contribution(prCounts); w != 0 {
			evs = append(evs, repoEvent{at: at, weight: w, count: indexformula.Activity(prCounts), kind: "agent"})
		}
	}
	return evs
}

// buildIndex turns events into the smoothed daily series ending at now.
// Pure function of its inputs — the determinism the chart tests pin down.
func buildIndex(evs []repoEvent, now time.Time) ([]IndexPoint, []IndexBar, float64, float64, int) {
	day := now.UTC().Truncate(24 * time.Hour)
	start := day.AddDate(0, 0, -(indexDays - 1))

	// Events before the window fold into the base level; events inside it
	// land in their day's bucket.
	base := indexBase
	daily := make([]float64, indexDays)
	bars := make([]IndexBar, indexDays)
	activity := 0
	for i := range bars {
		bars[i].T = start.AddDate(0, 0, i).Format("2006-01-02")
	}
	for _, e := range evs {
		d := e.at.UTC().Truncate(24 * time.Hour)
		switch {
		case d.Before(start):
			base += e.weight
		case !d.After(day):
			i := int(d.Sub(start).Hours() / 24)
			daily[i] += e.weight
			activity += e.barCount()
			if e.kind == "ideas" {
				bars[i].Ideas += e.barCount()
			} else {
				bars[i].Agent += e.barCount()
			}
		}
	}

	// Cumulative raw series, then a trailing-mean smooth.
	raw := make([]float64, indexDays)
	sum := base
	for i := range raw {
		sum += daily[i]
		raw[i] = sum
	}
	points := make([]IndexPoint, indexDays)
	for i := range points {
		lo := i - (smoothWindow - 1)
		if lo < 0 {
			lo = 0
		}
		s := 0.0
		for j := lo; j <= i; j++ {
			s += raw[j]
		}
		points[i] = IndexPoint{
			T:     bars[i].T,
			Value: round1(s / float64(i-lo+1)),
		}
	}
	current := points[indexDays-1].Value
	delta := round1(current - points[indexDays-2].Value)
	return points, bars, current, delta, activity
}

func round1(f float64) float64 {
	if f < 0 {
		return -round1(-f)
	}
	return float64(int64(f*10+0.5)) / 10
}

// repoTickers computes every registered repo's ticker, most active first
// (ties: RepoID asc) so the chart can default to the busiest market.
func (a *API) repoTickers() ([]RepoTicker, error) {
	repos := a.Registry.List(false)
	ideas, err := a.Store.ListAll()
	if err != nil {
		return nil, err
	}
	syms := repoSymbols(repos)
	now := timeNow()
	out := make([]RepoTicker, 0, len(repos))
	for _, rp := range repos {
		evs := combinedRepoEvents(ideas, a.History, rp.RepoID)
		_, _, current, delta, activity := buildIndex(evs, now)
		out = append(out, RepoTicker{
			RepoID: rp.RepoID, Symbol: syms[rp.RepoID],
			Value: current, Delta: delta, Activity: activity,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Activity != out[j].Activity {
			return out[i].Activity > out[j].Activity
		}
		return out[i].RepoID < out[j].RepoID
	})
	return out, nil
}

// HandleRepoIndex serves GET /api/repos/{org}/{repo}/index — the chart
// series for one listed repo. Public: aggregate numbers only.
func (a *API) HandleRepoIndex(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("org") + "/" + r.PathValue("repo")
	if _, err := a.Registry.Get(repoID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	ideas, err := a.Store.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	evs := combinedRepoEvents(ideas, a.History, repoID)
	points, bars, current, delta, _ := buildIndex(evs, timeNow())
	syms := repoSymbols(a.Registry.List(false))
	writeJSON(w, http.StatusOK, RepoIndex{
		RepoID: repoID, Symbol: syms[repoID],
		Points: points, Bars: bars, Current: current, Delta: delta,
	})
}
