// Repo value index: the derived "stock price" of a hive-managed repo,
// powering the landing's terminal chart and the repo tickers on the tape.
//
// The index is derived ENTIRELY from what Dibs already stores — idea
// offers, acceptances, and settlements against the repo — so it is
// deterministic, needs no external API, and leaks nothing: every input is
// an aggregate count of events whose existence the market surfaces already
// expose. (A GitHub-stats enrichment can be layered on later; the store-only
// derivation is the honest baseline and the graceful fallback.)
//
// Derivation, documented for the chart's sake:
//
//	weight(offer)  = 2   an idea matched/offered to the repo — interest
//	weight(accept) = 6   the repo committed agent capacity — a bid hit
//	weight(settle) = 10  credited issue filed — the idea shipped
//
//	index(day) = indexBase + Σ weights of all events up to that day,
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

	"github.com/kubestellar/dibs/pkg/registry"
	"github.com/kubestellar/dibs/pkg/store"
)

// Index derivation constants (see package comment).
const (
	weightOffer  = 2.0
	weightAccept = 6.0
	weightSettle = 10.0
	indexBase    = 100.0
	indexDays    = 30
	smoothWindow = 3
)

// repoEvent is one weighted activity event against a repo.
type repoEvent struct {
	at     time.Time
	weight float64
	// kind buckets the sub-chart bars: "ideas" (offers — listing/match
	// interest) or "agent" (accept/settle — implementation activity; an
	// honest PROXY until real token metering exists).
	kind string
}

// IndexPoint is one smoothed daily index value.
type IndexPoint struct {
	T     string  `json:"t"` // YYYY-MM-DD (UTC)
	Value float64 `json:"value"`
}

// IndexBar is one day's activity bars for the sub-chart.
type IndexBar struct {
	T     string `json:"t"`
	Ideas int    `json:"ideas"` // ideas listed/matched on the repo
	Agent int    `json:"agent"` // agent activity (est.) — accepts + settlements
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

// repoEvents collects the repo's weighted activity events from the store.
// Aggregate-only: nothing about any idea's content or visibility leaves
// this function — just timestamps and weights.
func repoEvents(ideas []*store.Idea, repoID string) []repoEvent {
	var evs []repoEvent
	for _, idea := range ideas {
		for _, o := range idea.Offers {
			if o.RepoID != repoID {
				continue
			}
			evs = append(evs, repoEvent{at: o.CreatedAt, weight: weightOffer, kind: "ideas"})
			if o.Status == store.OfferAccepted && o.DecidedAt != nil {
				evs = append(evs, repoEvent{at: *o.DecidedAt, weight: weightAccept, kind: "agent"})
			}
		}
		if idea.TargetRepo == repoID && idea.Status == store.StatusSettled {
			evs = append(evs, repoEvent{at: idea.UpdatedAt, weight: weightSettle, kind: "agent"})
		}
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].at.Before(evs[j].at) })
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
			activity++
			if e.kind == "ideas" {
				bars[i].Ideas++
			} else {
				bars[i].Agent++
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
		_, _, current, delta, activity := buildIndex(repoEvents(ideas, rp.RepoID), now)
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
	points, bars, current, delta, _ := buildIndex(repoEvents(ideas, repoID), timeNow())
	syms := repoSymbols(a.Registry.List(false))
	writeJSON(w, http.StatusOK, RepoIndex{
		RepoID: repoID, Symbol: syms[repoID],
		Points: points, Bars: bars, Current: current, Delta: delta,
	})
}
