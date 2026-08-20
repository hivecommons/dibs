package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubestellar/dibs/pkg/api"
	"github.com/kubestellar/dibs/pkg/store"
)

type boardResp struct {
	Board []api.BoardRow `json:"board"`
}
type tickerResp struct {
	Ticker []api.TickerEntry `json:"ticker"`
}

// TestMarketPhaseMapping pins the status → market-phase table.
func TestMarketPhaseMapping(t *testing.T) {
	cases := map[string]string{
		store.StatusDraft:         api.PhaseOpen,
		store.StatusDeclined:      api.PhaseOpen,
		store.StatusOffered:       api.PhaseMatched,
		store.StatusAccepted:      api.PhaseBuilding,
		store.StatusIssueLaunched: api.PhaseBuilding,
		store.StatusSettled:       api.PhaseShipped,
	}
	for status, want := range cases {
		if got := api.MarketPhase(status); got != want {
			t.Errorf("MarketPhase(%q) = %q, want %q", status, got, want)
		}
	}
}

// TestMarketEndpointsPrivacy: /api/board and /api/ticker are public and
// must expose PUBLIC ideas plus SETTLED ideas only — a private, unsettled
// idea must never appear, and neither may any idea body.
func TestMarketEndpointsPrivacy(t *testing.T) {
	f := newWave2Server(t, nil)
	pub := f.createIdea(t, "bob-session", "Public kubernetes widget", "public body text", "public")
	priv := f.createIdea(t, "bob-session", "Secret private scheme", "private body text", "private")
	// Settle a PRIVATE idea end-to-end: its title becomes public via the
	// credited issue, so the board may list it as SHIPPED.
	settled := f.createIdea(t, "bob-session", "Private but shipped", "settled body text", "private")
	settleViaLaunch(t, f, settled, "kubestellar/dibs", "bob-session", "alice-session")

	// Board: logged out.
	rec := doJSON(t, f.h, "GET", "/api/board", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("board: %d %s", rec.Code, rec.Body.String())
	}
	board := decode[boardResp](t, rec).Board
	seen := map[string]api.BoardRow{}
	for _, row := range board {
		seen[row.ID] = row
	}
	if _, ok := seen[priv.ID]; ok {
		t.Fatalf("private idea leaked onto the board: %s", rec.Body.String())
	}
	pubRow, ok := seen[pub.ID]
	if !ok {
		t.Fatalf("public idea missing from board: %s", rec.Body.String())
	}
	if pubRow.Phase != api.PhaseOpen || pubRow.Symbol == "" || pubRow.RepoID != "" {
		t.Fatalf("public row wrong: %+v", pubRow)
	}
	shippedRow, ok := seen[settled.ID]
	if !ok {
		t.Fatalf("settled idea missing from board: %s", rec.Body.String())
	}
	if shippedRow.Phase != api.PhaseShipped || shippedRow.RepoID != "kubestellar/dibs" || !shippedRow.Momentum {
		t.Fatalf("shipped row wrong: %+v", shippedRow)
	}
	// No body text anywhere in the payload.
	for _, s := range []string{"public body text", "private body text", "settled body text", "Secret private scheme"} {
		if strings.Contains(rec.Body.String(), s) {
			t.Fatalf("board payload leaked %q", s)
		}
	}

	// Ticker: logged out.
	rec = doJSON(t, f.h, "GET", "/api/ticker", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ticker: %d %s", rec.Code, rec.Body.String())
	}
	entries := decode[tickerResp](t, rec).Ticker
	events := map[string]string{}
	for _, e := range entries {
		events[e.Title] = e.Event
		if e.Symbol == "" {
			t.Fatalf("ticker entry missing symbol: %+v", e)
		}
	}
	if _, ok := events["Secret private scheme"]; ok {
		t.Fatalf("private idea leaked onto the ticker: %s", rec.Body.String())
	}
	if events["Public kubernetes widget"] != "listed" {
		t.Fatalf("public idea should tick as listed, got %v", events)
	}
	if events["Private but shipped"] != "shipped" {
		t.Fatalf("settled idea should tick as shipped, got %v", events)
	}
}

// TestMarketStats: aggregate counts over all ideas — no content exposed.
func TestMarketStats(t *testing.T) {
	f := newWave2Server(t, nil)
	f.createIdea(t, "bob-session", "Open one", "body", "public")
	f.createIdea(t, "bob-session", "Hidden one", "body", "private")
	shipped := f.createIdea(t, "bob-session", "Shipped one", "body", "public")
	settleViaLaunch(t, f, shipped, "kubestellar/dibs", "bob-session", "alice-session")

	rec := doJSON(t, f.h, "GET", "/api/stats", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", rec.Code, rec.Body.String())
	}
	st := decode[api.MarketStats](t, rec)
	if st.Listed != 3 || st.Matched != 1 || st.Shipped != 1 || st.Ideators != 1 {
		t.Fatalf("stats wrong: %+v", st)
	}
	if strings.Contains(rec.Body.String(), "Hidden one") {
		t.Fatalf("stats leaked idea title: %s", rec.Body.String())
	}
}

// settleViaLaunch walks an idea offer → accept → launch → confirm so market
// tests have a SHIPPED record.
func settleViaLaunch(t *testing.T, f *wave2Fixture, idea store.Idea, repoID, ideatorSession, ownerSession string) {
	t.Helper()
	rec := doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/offer", ideatorSession, map[string]string{"repoID": repoID})
	if rec.Code != http.StatusOK {
		t.Fatalf("offer: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/repos/"+repoID+"/decide", ownerSession,
		map[string]string{"ideaID": idea.ID, "decision": "accept"})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/launch", ideatorSession, map[string]string{})
	if rec.Code != http.StatusOK {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.h, "POST", "/api/ideas/"+idea.ID+"/confirm-issue", ideatorSession,
		map[string]string{"issueURL": "https://github.com/" + repoID + "/issues/77"})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
}

type repoIndexResp struct {
	RepoID  string  `json:"repoID"`
	Symbol  string  `json:"symbol"`
	Current float64 `json:"current"`
	Delta   float64 `json:"delta"`
	Points  []struct {
		T     string  `json:"t"`
		Value float64 `json:"value"`
	} `json:"points"`
	Bars []struct {
		T           string `json:"t"`
		IssuesHuman int    `json:"issuesHuman"`
		PRsClanker  int    `json:"prsClanker"`
	} `json:"bars"`
}

type tickerWithReposResp struct {
	Ticker []api.TickerEntry `json:"ticker"`
	Repos  []api.RepoTicker  `json:"repos"`
}

// TestRepoIndexEndpoint: /api/repos/{org}/{repo}/index is public, 404s on
// unknown repos, and reflects store activity in the derived series. The
// ticker carries every listed repo's symbol/value/delta.
func TestRepoIndexEndpoint(t *testing.T) {
	f := newWave2Server(t, nil)

	// Quiet repo: flat at the base, no activity.
	rec := doJSON(t, f.h, "GET", "/api/repos/org/other/index", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("index: %d %s", rec.Code, rec.Body.String())
	}
	quiet := decode[repoIndexResp](t, rec)
	if quiet.Current != 100 || quiet.Delta != 0 || len(quiet.Points) != 30 || quiet.Symbol != "OTHR" {
		t.Fatalf("quiet index wrong: %+v", quiet)
	}

	// Unknown repo → 404.
	if rec := doJSON(t, f.h, "GET", "/api/repos/no/nope/index", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown repo: %d %s", rec.Code, rec.Body.String())
	}

	// Activity moves the index: settle an idea (idea filed).
	idea := f.createIdea(t, "bob-session", "Index mover", "body", "public")
	settleViaLaunch(t, f, idea, "kubestellar/dibs", "bob-session", "alice-session")

	rec = doJSON(t, f.h, "GET", "/api/repos/kubestellar/dibs/index", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("index: %d %s", rec.Code, rec.Body.String())
	}
	busy := decode[repoIndexResp](t, rec)
	if busy.Current <= 100 || busy.Delta <= 0 || busy.Symbol != "DIBS" {
		t.Fatalf("busy index should be above base with positive delta: %+v", busy)
	}
	last := busy.Bars[len(busy.Bars)-1]
	if last.IssuesHuman < 1 || last.PRsClanker != 0 {
		t.Fatalf("today's bars should show the idea-filed activity only: %+v", last)
	}

	// Determinism at the HTTP layer: same request, same series.
	rec2 := doJSON(t, f.h, "GET", "/api/repos/kubestellar/dibs/index", "", nil)
	if rec.Body.String() != rec2.Body.String() {
		t.Fatalf("index endpoint not deterministic:\n%s\n%s", rec.Body.String(), rec2.Body.String())
	}

	// Ticker carries repo tickers, most active first.
	rec = doJSON(t, f.h, "GET", "/api/ticker", "", nil)
	tk := decode[tickerWithReposResp](t, rec)
	if len(tk.Repos) != 2 {
		t.Fatalf("want 2 repo tickers, got %+v", tk.Repos)
	}
	if tk.Repos[0].RepoID != "kubestellar/dibs" || tk.Repos[0].Symbol != "DIBS" || tk.Repos[0].Value <= 100 {
		t.Fatalf("busiest repo should lead the tape: %+v", tk.Repos)
	}
	if tk.Repos[1].RepoID != "org/other" || tk.Repos[1].Value != 100 {
		t.Fatalf("quiet repo ticker wrong: %+v", tk.Repos)
	}
}
