package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/dibs/pkg/history"
	"github.com/hivecommons/dibs/pkg/indexformula"
	"github.com/hivecommons/dibs/pkg/store"
)

// stubHistory is a RepoHistory that answers every Get with a fixed record.
type stubHistory struct {
	h  history.RepoHistory
	ok bool
}

func (s stubHistory) Get(string) (history.RepoHistory, bool) { return s.h, s.ok }

// TestStoreFailuresMapTo500_PublicMarketRoutes pins that an unexpected
// store error on the public, unauthenticated market surfaces (board,
// ticker, stats, credits, leaderboard) and the per-ideator stats route
// surfaces as a generic 500 and never leaks the underlying error text.
// The ticker is exercised three times so each of its three store reads
// (ListPublic, ListSettled via marketIdeas, ListAll via repoTickers) is
// the one that fails.
func TestStoreFailuresMapTo500_PublicMarketRoutes(t *testing.T) {
	cases := []struct {
		name, fail string
		call       func(a *API, rec *httptest.ResponseRecorder)
	}{
		{"board/ListPublic", "ListPublic", func(a *API, rec *httptest.ResponseRecorder) {
			a.HandleBoard(rec, httptest.NewRequest("GET", "/api/board", nil))
		}},
		{"board/ListSettled", "ListSettled", func(a *API, rec *httptest.ResponseRecorder) {
			a.HandleBoard(rec, httptest.NewRequest("GET", "/api/board", nil))
		}},
		{"ticker/ListPublic", "ListPublic", func(a *API, rec *httptest.ResponseRecorder) {
			a.HandleTicker(rec, httptest.NewRequest("GET", "/api/ticker", nil))
		}},
		{"ticker/ListSettled", "ListSettled", func(a *API, rec *httptest.ResponseRecorder) {
			a.HandleTicker(rec, httptest.NewRequest("GET", "/api/ticker", nil))
		}},
		{"ticker/ListAll", "ListAll", func(a *API, rec *httptest.ResponseRecorder) {
			a.HandleTicker(rec, httptest.NewRequest("GET", "/api/ticker", nil))
		}},
		{"stats/ListAll", "ListAll", func(a *API, rec *httptest.ResponseRecorder) {
			a.HandleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
		}},
		{"credits/ListSettled", "ListSettled", func(a *API, rec *httptest.ResponseRecorder) {
			a.HandleCredits(rec, httptest.NewRequest("GET", "/api/credits", nil))
		}},
		{"leaderboard/ListAll", "ListAll", func(a *API, rec *httptest.ResponseRecorder) {
			a.HandleLeaderboard(rec, httptest.NewRequest("GET", "/api/leaderboard", nil))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAPIFixture(t)
			mustCreate(t, a, "alice", "Public idea", store.VisibilityPublic, store.StatusDraft)
			withFaultStore(a, tc.fail)
			rec := httptest.NewRecorder()
			tc.call(a, rec)
			want500(t, rec, tc.name)
			if strings.Contains(rec.Body.String(), errBoom.Error()) {
				t.Fatalf("%s: leaked internal error text: %s", tc.name, rec.Body.String())
			}
		})
	}

	t.Run("me/stats/ListByAuthor", func(t *testing.T) {
		a, mux := newAPIFixture(t)
		withFaultStore(a, "ListByAuthor")
		rec := do(t, mux, ident("alice"), "GET", "/api/me/stats", "")
		want500(t, rec, "me/stats")
		if strings.Contains(rec.Body.String(), errBoom.Error()) {
			t.Fatalf("leaked internal error text: %s", rec.Body.String())
		}
	})
}

// TestHandleRepoIndexDependencyFailures pins the two 500 arms of
// GET /api/repos/{org}/{repo}/index: a registry lookup failure that is not
// ErrNotFound, and a store listing failure after the repo resolved.
func TestHandleRepoIndexDependencyFailures(t *testing.T) {
	t.Run("registry Get fails", func(t *testing.T) {
		a, _ := newAPIFixture(t)
		a.Registry = &faultRegistry{RepoRegistry: a.Registry, getErr: errBoom}
		rec := httptest.NewRecorder()
		a.HandleRepoIndex(rec, repoPathReq("/api/repos/kubestellar/dibs/index", "kubestellar", "dibs"))
		want500(t, rec, "registry failure")
		if strings.Contains(rec.Body.String(), errBoom.Error()) {
			t.Fatalf("leaked internal error text: %s", rec.Body.String())
		}
	})

	t.Run("store ListAll fails", func(t *testing.T) {
		a, _ := newAPIFixture(t)
		withFaultStore(a, "ListAll")
		rec := httptest.NewRecorder()
		a.HandleRepoIndex(rec, repoPathReq("/api/repos/kubestellar/dibs/index", "kubestellar", "dibs"))
		want500(t, rec, "store failure")
		if strings.Contains(rec.Body.String(), errBoom.Error()) {
			t.Fatalf("leaked internal error text: %s", rec.Body.String())
		}
	})

	t.Run("unknown repo stays 404 with a healthy registry", func(t *testing.T) {
		a, _ := newAPIFixture(t)
		a.Registry = &faultRegistry{RepoRegistry: a.Registry}
		rec := httptest.NewRecorder()
		a.HandleRepoIndex(rec, repoPathReq("/api/repos/no/pe/index", "no", "pe"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
		}
	})
}

// TestHistoryIdeaCoverageAbsent pins that an unknown repo or a history
// record with no days yields no coverage, so live ideas are never deduped
// against a backfill that does not exist.
func TestHistoryIdeaCoverageAbsent(t *testing.T) {
	if c := historyIdeaCoverage(stubHistory{ok: false}, "org/repo"); c != nil {
		t.Fatalf("unknown repo coverage = %+v, want nil", c)
	}
	empty := stubHistory{ok: true, h: history.RepoHistory{RepoID: "org/repo"}}
	if c := historyIdeaCoverage(empty, "org/repo"); c != nil {
		t.Fatalf("empty-days coverage = %+v, want nil", c)
	}
	if evs := historyEvents(stubHistory{ok: false}, "org/repo"); evs != nil {
		t.Fatalf("unknown repo events = %+v, want nil", evs)
	}
}

// TestHistoryEventsSkipsBadDatesAndKeepsClankerCount pins two edge arms of
// the backfill conversion: a day whose date fails to parse is skipped
// rather than aborting the series, and a day whose raw ClankeR PR total
// exceeds the weighted activity count reports the larger number so the bar
// never under-reports what GitHub showed.
func TestHistoryEventsSkipsBadDatesAndKeepsClankerCount(t *testing.T) {
	hist := stubHistory{ok: true, h: history.RepoHistory{RepoID: "org/repo", Days: []history.DayActivity{
		{Date: "not-a-date", MergedPRs: 7, RegularIssuesCreated: 7},
		{Date: "2026-08-18", PRsClanker: 4},
		{Date: "2026-08-19", ClankerPRsCreated: 1, PRsClanker: 1},
	}}}
	evs := historyEvents(hist, "org/repo")
	if len(evs) != 2 {
		t.Fatalf("historyEvents = %+v, want 2 events (bad-date day skipped, no issue events)", evs)
	}
	if evs[0].weight != 0 || evs[0].count != 4 || evs[0].prsClanker != 4 {
		t.Fatalf("raw-clanker-only day = %+v, want weight 0 count 4 prsClanker 4", evs[0])
	}
	wantWeight := indexformula.Contribution(indexformula.Counts{ClankerPRsCreated: 1})
	if evs[1].weight != wantWeight || evs[1].count != 1 || evs[1].prsClanker != 1 {
		t.Fatalf("clanker-created day = %+v, want weight %v count 1 prsClanker 1", evs[1], wantWeight)
	}
}

// TestRepoEventsAndCombinedAreSortedOldestFirst pins the ordering contract
// buildIndex relies on: live settled ideas come back oldest first regardless
// of store order, and merging live events with backfilled history keeps the
// combined stream in time order.
func TestRepoEventsAndCombinedAreSortedOldestFirst(t *testing.T) {
	newer := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	older := newer.Add(-48 * time.Hour)
	ideas := []*store.Idea{
		{ID: "b", Status: store.StatusSettled, TargetRepo: "org/repo", UpdatedAt: newer},
		{ID: "a", Status: store.StatusSettled, TargetRepo: "org/repo", UpdatedAt: older},
	}
	evs := repoEvents(ideas, "org/repo", nil)
	if len(evs) != 2 || !evs[0].at.Equal(older) || !evs[1].at.Equal(newer) {
		t.Fatalf("repoEvents = %+v, want oldest first [%v, %v]", evs, older, newer)
	}

	// Backfill a day between the two live ideas, fetched before either so
	// neither live idea is deduped against it.
	hist := stubHistory{ok: true, h: history.RepoHistory{RepoID: "org/repo",
		FetchedAt: older.Add(-time.Hour),
		Days:      []history.DayActivity{{Date: "2026-08-19", RegularIssuesCreated: 1}},
	}}
	combined := combinedRepoEvents(ideas, hist, "org/repo")
	if len(combined) != 3 {
		t.Fatalf("combined = %+v, want 3 events", combined)
	}
	for i := 1; i < len(combined); i++ {
		if combined[i].at.Before(combined[i-1].at) {
			t.Fatalf("combined out of order at %d: %+v", i, combined)
		}
	}
	if combined[1].weight != indexformula.Contribution(indexformula.Counts{RegularIssuesCreated: 1}) {
		t.Fatalf("middle event = %+v, want the backfilled issue", combined[1])
	}
}
