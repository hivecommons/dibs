// Wave-3 API: the public credit wall and ideator profile stats.
//
// Credit-wall positioning: ideators are a first-class contributor class —
// the all-contributors spec has had a 💡 "ideas" emoji since 2016 and the
// academic CRediT taxonomy a "Conceptualization" role; Dibs makes that
// credit machine-readable and public.
package api

import (
	"net/http"
	"time"

	"github.com/kubestellar/dibs/pkg/store"
)

// CreditEntry is one row on the public credit wall: a settled idea's
// public-by-construction facts (settlement opened a public GitHub issue).
// The idea body is deliberately absent.
type CreditEntry struct {
	Author        string    `json:"author"`
	AuthorDisplay string    `json:"authorDisplay"`
	Title         string    `json:"title"`
	TLDR          string    `json:"tldr,omitempty"`
	RepoID        string    `json:"repoID"`
	IssueURL      string    `json:"issueURL"`
	SettledAt     time.Time `json:"settledAt"`
}

// HandleCredits serves the credit wall data. It is registered OUTSIDE the
// auth middleware — the wall is public by design — so it must only ever
// expose settled-idea facts that are already public via the credited issue.
func (a *API) HandleCredits(w http.ResponseWriter, r *http.Request) {
	ideas, err := a.Store.ListSettled()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	entries := []CreditEntry{}
	for _, idea := range ideas {
		entries = append(entries, CreditEntry{
			Author:        idea.Author,
			AuthorDisplay: idea.AuthorDisplay,
			Title:         idea.Title,
			TLDR:          idea.TLDR,
			RepoID:        idea.TargetRepo,
			IssueURL:      idea.IssueURL,
			SettledAt:     idea.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"credits": entries})
}

// IdeatorStats are the caller's lifecycle counts. Buckets are cumulative
// down the funnel: an accepted idea was offered, a settled idea was
// accepted — so settled ⊆ accepted ⊆ offered ⊆ posted.
type IdeatorStats struct {
	Posted   int `json:"posted"`
	Offered  int `json:"offered"`
	Accepted int `json:"accepted"`
	Settled  int `json:"settled"`
}

// handleMyStats returns the caller's ideator profile stats.
func (a *API) handleMyStats(w http.ResponseWriter, r *http.Request) {
	ideas, err := a.Store.ListByAuthor(identity(r).Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var st IdeatorStats
	for _, idea := range ideas {
		st.Posted++
		if len(idea.Offers) > 0 || idea.Status != store.StatusDraft {
			st.Offered++
		}
		if idea.Status == store.StatusAccepted || idea.Status == store.StatusIssueLaunched || idea.Status == store.StatusSettled {
			st.Accepted++
		}
		if idea.Status == store.StatusSettled {
			st.Settled++
		}
	}
	writeJSON(w, http.StatusOK, st)
}
