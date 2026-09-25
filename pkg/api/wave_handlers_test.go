package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/match"
	"github.com/hivecommons/dibs/pkg/notify"
	"github.com/hivecommons/dibs/pkg/settle"
	"github.com/hivecommons/dibs/pkg/store"
)

func withFixedNow(t *testing.T, now time.Time) {
	t.Helper()
	old := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = old })
}

func TestWave2OfferFeedDecideAndNotifications(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns
	a.Engine = &match.Engine{Store: a.Store, Registry: a.Registry}

	private := mustCreate(t, a, "bob", "Private reveal", store.VisibilityPrivate, store.StatusDraft)
	public := mustCreate(t, a, "bob", "Public candidate", store.VisibilityPublic, store.StatusDraft)

	rec := do(t, mux, ident("bob"), "POST", "/api/ideas/"+private.ID+"/offer", `{"repoID":"kubestellar/dibs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("offer private: status=%d body=%s", rec.Code, rec.Body.String())
	}
	offered := decodeBody[store.Idea](t, rec)
	if offered.Status != store.StatusOffered || offered.OfferTo("kubestellar/dibs") == nil || offered.TargetRepo != "" {
		t.Fatalf("offered idea = %+v", offered)
	}
	if got := ns.ListByUser("alice", true); len(got) != 1 || got[0].Kind != notify.KindOffer || got[0].IdeaID != private.ID {
		t.Fatalf("alice notifications = %+v", got)
	}

	rec = do(t, mux, ident("alice"), "GET", "/api/repos/kubestellar/dibs/feed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("repo feed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	feed := decodeBody[struct {
		Offers     []matchView `json:"offers"`
		Candidates []matchView `json:"candidates"`
	}](t, rec)
	if len(feed.Offers) != 1 || feed.Offers[0].Idea.ID != private.ID {
		t.Fatalf("offers = %+v, want private offer", feed.Offers)
	}
	if len(feed.Candidates) != 1 || feed.Candidates[0].Idea.ID != public.ID {
		t.Fatalf("candidates = %+v, want only public draft", feed.Candidates)
	}

	rec = do(t, mux, ident("charlie"), "POST", "/api/repos/org/other/decide", `{"ideaID":"`+private.ID+`","decision":"accept"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unoffered private accept: status=%d, want 404", rec.Code)
	}
	rec = do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"`+private.ID+`","decision":"accept"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: status=%d body=%s", rec.Code, rec.Body.String())
	}
	accepted := decodeBody[struct {
		Result string     `json:"result"`
		Idea   store.Idea `json:"idea"`
		Next   string     `json:"next"`
	}](t, rec)
	if accepted.Result != "accepted" || accepted.Idea.Status != store.StatusAccepted || accepted.Idea.TargetRepo != "kubestellar/dibs" || !strings.Contains(accepted.Next, "prefilled") {
		t.Fatalf("accepted payload = %+v", accepted)
	}
	if got := ns.ListByUser("bob", true); len(got) != 1 || got[0].Kind != notify.KindAccepted {
		t.Fatalf("bob notifications after accept = %+v", got)
	}

	rec = do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"`+public.ID+`","decision":"pass"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pass: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = do(t, mux, ident("alice"), "GET", "/api/repos/kubestellar/dibs/feed", "")
	if feed = decodeBody[struct {
		Offers     []matchView `json:"offers"`
		Candidates []matchView `json:"candidates"`
	}](t, rec); len(feed.Candidates) != 0 {
		t.Fatalf("passed candidate still present: %+v", feed.Candidates)
	}
}

func TestWave2ExternalOfferDeclineSeenAndMarkRead(t *testing.T) {
	now := time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns
	idea := mustCreate(t, a, "bob", "External path", store.VisibilityPublic, store.StatusDraft)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error {
		i.MatchesUpdatedAt = now.Add(-time.Hour)
		return nil
	}); err != nil {
		t.Fatalf("seed matches updated: %v", err)
	}

	rec := do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/matches/seen", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("matches seen: status=%d body=%s", rec.Code, rec.Body.String())
	}
	seen := decodeBody[store.Idea](t, rec)
	if !seen.SuggestionsSeenAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("SuggestionsSeenAt = %s", seen.SuggestionsSeenAt)
	}

	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/offer", `{"repoID":"outside/project"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("external offer: status=%d body=%s", rec.Code, rec.Body.String())
	}
	ext := decodeBody[store.Idea](t, rec)
	if ext.Status != store.StatusOffered || ext.TargetRepo != "outside/project" || ext.OfferTo("outside/project") == nil || !ext.OfferTo("outside/project").External {
		t.Fatalf("external offer idea = %+v", ext)
	}
	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/pass", `{"repoID":"kubestellar/dibs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ideator pass: status=%d body=%s", rec.Code, rec.Body.String())
	}
	passed := decodeBody[store.Idea](t, rec)
	if !passed.HasPassed("kubestellar/dibs") {
		t.Fatalf("passed repos = %+v", passed.PassedRepos)
	}

	declined := mustCreate(t, a, "bob", "Decline me", store.VisibilityPrivate, store.StatusOffered)
	if _, err := a.Store.Mutate(declined.ID, false, func(i *store.Idea) error {
		i.Offers = []store.Offer{{RepoID: "kubestellar/dibs", Status: store.OfferPending, CreatedAt: now}}
		return nil
	}); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	rec = do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"`+declined.ID+`","decision":"decline"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("decline: status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeBody[struct {
		Result string     `json:"result"`
		Idea   store.Idea `json:"idea"`
	}](t, rec)
	if out.Result != "declined" || out.Idea.Status != store.StatusDeclined || out.Idea.OfferTo("kubestellar/dibs").Status != store.OfferDeclined {
		t.Fatalf("decline payload = %+v", out)
	}
	if got := ns.ListByUser("bob", true); len(got) < 2 {
		t.Fatalf("want unread notifications for bob, got %+v", got)
	}
	rec = do(t, mux, ident("bob"), "GET", "/api/notifications?unread=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list notifications: status=%d", rec.Code)
	}
	rec = do(t, mux, ident("bob"), "POST", "/api/notifications/read", `{"all":true}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("mark read: status=%d", rec.Code)
	}
	if got := ns.ListByUser("bob", true); len(got) != 0 {
		t.Fatalf("unread after mark all = %+v", got)
	}
}

func TestSettlementLaunchConfirmAndLegacySettle(t *testing.T) {
	now := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, mux := newAPIFixture(t)
	ns, err := notify.New(t.TempDir())
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	a.Notify = ns

	rec := do(t, mux, ident("bob"), "POST", "/api/refine", `{"title":"T","body":"B"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("refine fallback: status=%d", rec.Code)
	}
	refined := decodeBody[refineOutput](t, rec)
	if refined.Refined || refined.Title != "T" || refined.Body != "B" {
		t.Fatalf("refine fallback = %+v", refined)
	}
	rec = do(t, mux, ident("bob"), "POST", "/api/refine", `{"title":"","body":"B"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad refine: status=%d", rec.Code)
	}

	idea := mustCreate(t, a, "bob", "Accepted thing", store.VisibilityPublic, store.StatusAccepted)
	if _, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error { i.TargetRepo = "kubestellar/dibs"; return nil }); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/launch", `{"title":"Custom title","body":"Custom body"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("launch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	launch := decodeBody[map[string]any](t, rec)
	issueURL, ok := launch["url"].(string)
	if !ok || !strings.Contains(issueURL, "github.com/kubestellar/dibs/issues/new") || launch["repoID"] != "kubestellar/dibs" {
		t.Fatalf("launch payload = %+v", launch)
	}
	stored, err := a.Store.Get(idea.ID)
	if err != nil || stored.Status != store.StatusIssueLaunched {
		t.Fatalf("stored after launch = %+v err=%v", stored, err)
	}
	rec = do(t, mux, ident("bob"), "POST", "/api/ideas/"+idea.ID+"/confirm-issue", `{"issueURL":"https://github.com/kubestellar/dibs/issues/123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: status=%d body=%s", rec.Code, rec.Body.String())
	}
	confirmed := decodeBody[struct {
		Result string     `json:"result"`
		Idea   store.Idea `json:"idea"`
	}](t, rec)
	if confirmed.Result != "settled" || confirmed.Idea.Status != store.StatusSettled || confirmed.Idea.IssueURL == "" {
		t.Fatalf("confirm payload = %+v", confirmed)
	}
	if got := ns.ListByUser("alice", true); len(got) != 1 || got[0].Kind != notify.KindIssue {
		t.Fatalf("repo owner issue notification = %+v", got)
	}

	legacy := mustCreate(t, a, "bob", "Legacy", store.VisibilityPublic, store.StatusOffered)
	if _, err := a.Store.Mutate(legacy.ID, false, func(i *store.Idea) error {
		i.Offers = []store.Offer{{RepoID: "kubestellar/dibs", Status: store.OfferPending, CreatedAt: now}}
		return nil
	}); err != nil {
		t.Fatalf("seed legacy offer: %v", err)
	}
	fake := &settle.Fake{}
	a.Settler = &settle.Settler{GitHub: fake}
	rec = do(t, mux, ident("alice"), "POST", "/api/repos/kubestellar/dibs/decide", `{"ideaID":"`+legacy.ID+`","decision":"accept"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy accept: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(fake.Issues) != 1 || fake.Issues[0].RepoID != "kubestellar/dibs" {
		t.Fatalf("fake issues = %+v", fake.Issues)
	}
	settled, _ := a.Store.Get(legacy.ID)
	if settled.Status != store.StatusSettled || !strings.Contains(settled.IssueURL, "/issues/1") {
		t.Fatalf("legacy settled = %+v", settled)
	}
}

func TestMarketAndWave3PublicSurfaces(t *testing.T) {
	now := time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC)
	withFixedNow(t, now)
	a, _ := newAPIFixture(t)
	publicDraft := mustCreate(t, a, "alice", "Open market", store.VisibilityPublic, store.StatusDraft)
	accepted := mustCreate(t, a, "bob", "Building", store.VisibilityPublic, store.StatusAccepted)
	privateSettled := mustCreate(t, a, "carol", "Secret shipped", store.VisibilityPrivate, store.StatusSettled)
	privateDraft := mustCreate(t, a, "dana", "Invisible", store.VisibilityPrivate, store.StatusDraft)
	if _, err := a.Store.Mutate(accepted.ID, false, func(i *store.Idea) error {
		i.TargetRepo = "kubestellar/dibs"
		i.UpdatedAt = now.Add(-time.Hour)
		return nil
	}); err != nil {
		t.Fatalf("seed accepted: %v", err)
	}
	if _, err := a.Store.Mutate(privateSettled.ID, false, func(i *store.Idea) error {
		i.TargetRepo = "outside/project"
		i.IssueURL = "https://github.com/outside/project/issues/7"
		i.TLDR = "done"
		return nil
	}); err != nil {
		t.Fatalf("seed settled: %v", err)
	}
	if _, err := a.Store.Mutate(privateDraft.ID, false, func(i *store.Idea) error { i.UpdatedAt = now.Add(time.Hour); return nil }); err != nil {
		t.Fatalf("seed private draft: %v", err)
	}

	if MarketPhase(store.StatusDraft) != PhaseOpen || MarketPhase(store.StatusOffered) != PhaseMatched || MarketPhase(store.StatusAccepted) != PhaseBuilding || MarketPhase(store.StatusIssueLaunched) != PhaseBuilding || MarketPhase(store.StatusSettled) != PhaseShipped {
		t.Fatalf("MarketPhase mapping changed")
	}
	if !a.hiveManagedRepo("kubestellar/dibs") || a.hiveManagedRepo("outside/project") || a.hiveManagedRepo("") {
		t.Fatalf("hiveManagedRepo results unexpected")
	}
	ideas, err := a.marketIdeas()
	if err != nil {
		t.Fatalf("marketIdeas: %v", err)
	}
	ids := map[string]bool{}
	for _, idea := range ideas {
		ids[idea.ID] = true
	}
	if !ids[publicDraft.ID] || !ids[accepted.ID] || !ids[privateSettled.ID] || ids[privateDraft.ID] {
		t.Fatalf("market ids = %+v", ids)
	}

	req := httptest.NewRequest("GET", "/api/market/board", nil)
	boardRec := httptest.NewRecorder()
	a.HandleBoard(boardRec, req)
	if boardRec.Code != http.StatusOK {
		t.Fatalf("board status=%d", boardRec.Code)
	}
	board := decodeBody[struct {
		Board []BoardRow `json:"board"`
	}](t, boardRec)
	if len(board.Board) != 3 {
		t.Fatalf("board len=%d rows=%+v", len(board.Board), board.Board)
	}
	for _, row := range board.Board {
		if row.Title == privateDraft.Title {
			t.Fatalf("private draft leaked to board: %+v", board.Board)
		}
		if row.Title == accepted.Title && row.RepoID != "kubestellar/dibs" {
			t.Fatalf("hive venue missing on accepted row: %+v", row)
		}
		if row.Title == privateSettled.Title && row.RepoID != "" {
			t.Fatalf("external venue leaked on board: %+v", row)
		}
	}

	tickerRec := httptest.NewRecorder()
	a.HandleTicker(tickerRec, httptest.NewRequest("GET", "/api/market/ticker", nil))
	if tickerRec.Code != http.StatusOK {
		t.Fatalf("ticker status=%d body=%s", tickerRec.Code, tickerRec.Body.String())
	}
	ticker := decodeBody[struct {
		Ticker []TickerEntry `json:"ticker"`
	}](t, tickerRec)
	if len(ticker.Ticker) != 3 {
		t.Fatalf("ticker len=%d entries=%+v", len(ticker.Ticker), ticker.Ticker)
	}
	statsRec := httptest.NewRecorder()
	a.HandleStats(statsRec, httptest.NewRequest("GET", "/api/market/stats", nil))
	stats := decodeBody[MarketStats](t, statsRec)
	if stats.Listed != 4 || stats.Matched != 2 || stats.Shipped != 1 || stats.Ideators != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	creditsRec := httptest.NewRecorder()
	a.HandleCredits(creditsRec, httptest.NewRequest("GET", "/api/credits", nil))
	credits := decodeBody[struct {
		Credits []CreditEntry `json:"credits"`
	}](t, creditsRec)
	if len(credits.Credits) != 1 || credits.Credits[0].Title != privateSettled.Title || credits.Credits[0].TLDR != "done" {
		t.Fatalf("credits = %+v", credits.Credits)
	}

	statsUser := do(t, func() *http.ServeMux { m := http.NewServeMux(); a.Register(m, ""); return m }(), ident("bob"), "GET", "/api/me/stats", "")
	if statsUser.Code != http.StatusOK {
		t.Fatalf("my stats status=%d body=%s", statsUser.Code, statsUser.Body.String())
	}
	mine := decodeBody[IdeatorStats](t, statsUser)
	if mine.Posted != 1 || mine.Offered != 1 || mine.Accepted != 1 || mine.Settled != 0 || mine.Progress.Score == 0 {
		t.Fatalf("my stats = %+v", mine)
	}
	lbRec := httptest.NewRecorder()
	a.HandleLeaderboard(lbRec, httptest.NewRequest("GET", "/api/leaderboard", nil))
	leaderboard := decodeBody[struct {
		Leaderboard []LeaderboardEntry `json:"leaderboard"`
	}](t, lbRec)
	if len(leaderboard.Leaderboard) != 1 || leaderboard.Leaderboard[0].Author != "carol" || leaderboard.Leaderboard[0].Rank != 1 {
		t.Fatalf("leaderboard = %+v", leaderboard.Leaderboard)
	}
}

func TestTickerCapsMarketEntries(t *testing.T) {
	a, _ := newAPIFixture(t)
	for i := 0; i < maxTickerEntries+5; i++ {
		idea := mustCreate(t, a, "alice", "Idea "+url.PathEscape(string(rune('a'+i%26)))+string(rune('a'+i/26)), store.VisibilityPublic, store.StatusDraft)
		if _, err := a.Store.Mutate(idea.ID, false, func(it *store.Idea) error {
			it.UpdatedAt = timeNow().Add(time.Duration(i) * time.Minute)
			return nil
		}); err != nil {
			t.Fatalf("mutate %d: %v", i, err)
		}
	}
	rec := httptest.NewRecorder()
	a.HandleTicker(rec, httptest.NewRequest("GET", "/api/market/ticker", nil))
	got := decodeBody[struct {
		Ticker []TickerEntry `json:"ticker"`
	}](t, rec)
	if len(got.Ticker) != maxTickerEntries {
		t.Fatalf("ticker len=%d, want cap %d", len(got.Ticker), maxTickerEntries)
	}
}
