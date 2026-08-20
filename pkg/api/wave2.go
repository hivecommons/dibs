// Wave-2 API: matching, offers, the repo-side feed, decisions, and the
// notification bell. Settlement itself moved to the matchmaker URL flow in
// settlement.go; accept only records the acceptance (plus the demoted
// legacy token-based settlement).
package api

import (
	"context"
	"log"
	"net/http"
	"sort"

	"github.com/kubestellar/dibs/pkg/notify"
	"github.com/kubestellar/dibs/pkg/registry"
	"github.com/kubestellar/dibs/pkg/settle"
	"github.com/kubestellar/dibs/pkg/store"
)

// maxFeedCandidates caps the repo-side candidate feed (LLM cost control).
const maxFeedCandidates = 25

// registerWave2 mounts the matching/settlement/notification routes.
func (a *API) registerWave2(mux *http.ServeMux, basePath string) {
	mux.HandleFunc("GET "+basePath+"/api/ideas/{id}/matches", a.handleIdeaMatches)
	mux.HandleFunc("POST "+basePath+"/api/ideas/{id}/offer", a.handleOffer)
	mux.HandleFunc("POST "+basePath+"/api/ideas/{id}/pass", a.handleIdeatorPass)
	mux.HandleFunc("GET "+basePath+"/api/repos/{org}/{repo}/feed", a.handleRepoFeed)
	mux.HandleFunc("POST "+basePath+"/api/repos/{org}/{repo}/decide", a.handleDecide)
	mux.HandleFunc("GET "+basePath+"/api/notifications", a.handleListNotifications)
	mux.HandleFunc("POST "+basePath+"/api/notifications/read", a.handleMarkRead)
}

// notifyAdd records a notification, logging (never failing the request) on
// store errors. No-op when notifications are not wired.
func (a *API) notifyAdd(user, kind, message, ideaID, repoID string) {
	if a.Notify == nil {
		return
	}
	if err := a.Notify.Add(user, kind, message, ideaID, repoID); err != nil {
		log.Printf("api: adding notification: %v", err)
	}
}

// MatchNotifier bridges the match engine's fresh-match events into the
// notification store. It NEVER notifies a repo owner about a private idea —
// private ideas reach repo owners only through an explicit offer.
type MatchNotifier struct{ Notify *notify.Store }

// NewMatch implements match.Notifier.
func (n *MatchNotifier) NewMatch(ideaAuthor, repoOwner string, idea *store.Idea, repo *registry.RepoProfile, score float64) {
	if n.Notify == nil {
		return
	}
	add := func(user, msg string) {
		if err := n.Notify.Add(user, notify.KindMatch, msg, idea.ID, repo.RepoID); err != nil {
			log.Printf("api: adding match notification: %v", err)
		}
	}
	add(ideaAuthor, "New match: your idea “"+idea.Title+"” looks like a fit for "+repo.RepoID+".")
	if idea.Visibility == store.VisibilityPublic && repoOwner != ideaAuthor {
		add(repoOwner, "New match: the idea “"+idea.Title+"” looks like a fit for "+repo.RepoID+".")
	}
}

// matchView is one scored candidate in either direction's feed.
type matchView struct {
	Repo   *registry.RepoProfile `json:"repo,omitempty"`
	Idea   *store.Idea           `json:"idea,omitempty"`
	Score  float64               `json:"score"`
	Reason string                `json:"reason"`
	ByLLM  bool                  `json:"byLLM"`
}

// handleIdeaMatches is the ideator side: candidate repos for one of the
// caller's own ideas, scored lazily and cached.
func (a *API) handleIdeaMatches(w http.ResponseWriter, r *http.Request) {
	idea := a.loadAuthorized(w, r, true)
	if idea == nil {
		return
	}
	if a.Engine == nil {
		writeError(w, http.StatusServiceUnavailable, "matching not configured")
		return
	}
	if _, err := a.Engine.EnsureTLDR(r.Context(), idea); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	matches, err := a.Engine.MatchesForIdea(r.Context(), idea)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	views := []matchView{}
	for _, m := range matches {
		rp, err := a.Registry.Get(m.RepoID)
		if err != nil {
			continue // repo vanished from the registry since scoring
		}
		views = append(views, matchView{Repo: rp, Score: m.Score, Reason: m.Reason, ByLLM: m.ByLLM})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tldr": idea.TLDR, "matches": views})
}

type offerInput struct {
	RepoID string `json:"repoID"`
}

// handleOffer is the ideator swiping right: offer the idea to one repo. For
// a PRIVATE idea this is the explicit reveal — and only to that repo's
// owner.
func (a *API) handleOffer(w http.ResponseWriter, r *http.Request) {
	idea := a.loadAuthorized(w, r, true)
	if idea == nil {
		return
	}
	var in offerInput
	if !decodeInput(w, r, &in) {
		return
	}
	rp, err := a.Registry.Get(in.RepoID)
	if err != nil {
		// Not a hive-managed repo: the ideator may still target ANY
		// open-source GitHub repo — the external path.
		a.offerExternal(w, idea, in.RepoID)
		return
	}
	if !rp.AcceptingIdeas {
		writeError(w, http.StatusBadRequest, "repo is not accepting ideas")
		return
	}
	updated, err := a.Store.Mutate(idea.ID, true, func(i *store.Idea) error {
		if o := i.OfferTo(rp.RepoID); o != nil && o.Status != store.OfferDeclined {
			return &store.ValidationError{Msg: "already offered to this repo"}
		}
		if i.Status != store.StatusOffered {
			if !store.CanTransition(i.Status, store.StatusOffered) {
				return &store.ValidationError{Msg: "cannot offer an idea in status " + i.Status}
			}
			i.Status = store.StatusOffered
		}
		if o := i.OfferTo(rp.RepoID); o != nil {
			o.Status = store.OfferPending
			o.CreatedAt = timeNow()
			o.DecidedAt = nil
		} else {
			i.Offers = append(i.Offers, store.Offer{RepoID: rp.RepoID, Status: store.OfferPending, CreatedAt: timeNow()})
		}
		return nil
	})
	if err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	a.notifyAdd(rp.Owner, notify.KindOffer,
		updated.AuthorDisplay+" offered the idea “"+updated.Title+"” to "+rp.RepoID+".", updated.ID, rp.RepoID)
	writeJSON(w, http.StatusOK, updated)
}

// offerExternal records an offer aimed at a repo OUTSIDE the hive registry.
// There is no repo owner on our side to notify or to accept, so the repo
// becomes the idea's launch target immediately: the ideator proceeds
// straight to the credited-issue launch flow (offered → issue_launched →
// settled). A private idea reveals nothing here — nobody on our side can
// see it, and the reveal happens only when the ideator files the public
// GitHub issue themselves.
func (a *API) offerExternal(w http.ResponseWriter, idea *store.Idea, repoID string) {
	if err := settle.ValidateRepoID(repoID); err != nil {
		writeError(w, http.StatusBadRequest, "not a hive-managed repo — to target any GitHub repo, use the org/repo format")
		return
	}
	updated, err := a.Store.Mutate(idea.ID, true, func(i *store.Idea) error {
		if o := i.OfferTo(repoID); o != nil && o.Status != store.OfferDeclined {
			return &store.ValidationError{Msg: "already offered to this repo"}
		}
		if i.Status != store.StatusOffered {
			if !store.CanTransition(i.Status, store.StatusOffered) {
				return &store.ValidationError{Msg: "cannot offer an idea in status " + i.Status}
			}
			i.Status = store.StatusOffered
		}
		if o := i.OfferTo(repoID); o != nil {
			o.Status = store.OfferPending
			o.External = true
			o.CreatedAt = timeNow()
			o.DecidedAt = nil
		} else {
			i.Offers = append(i.Offers, store.Offer{RepoID: repoID, Status: store.OfferPending, External: true, CreatedAt: timeNow()})
		}
		// No acceptance step: the external repo is the launch target now.
		i.TargetRepo = repoID
		return nil
	})
	if err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	a.notifyAdd(updated.Author, notify.KindOffer,
		"“"+updated.Title+"” is aimed at "+repoID+" (external repo) — file the credited GitHub issue whenever you're ready.",
		updated.ID, repoID)
	writeJSON(w, http.StatusOK, updated)
}

// handleIdeatorPass is the ideator swiping left on a suggested repo.
func (a *API) handleIdeatorPass(w http.ResponseWriter, r *http.Request) {
	idea := a.loadAuthorized(w, r, true)
	if idea == nil {
		return
	}
	var in offerInput
	if !decodeInput(w, r, &in) {
		return
	}
	if in.RepoID == "" {
		writeError(w, http.StatusBadRequest, "repoID is required")
		return
	}
	updated, err := a.Store.Mutate(idea.ID, false, func(i *store.Idea) error {
		if !i.HasPassed(in.RepoID) {
			i.PassedRepos = append(i.PassedRepos, in.RepoID)
		}
		return nil
	})
	if err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// ownedRepo loads the repo from the path and enforces that the caller owns
// it.
func (a *API) ownedRepo(w http.ResponseWriter, r *http.Request) *registry.RepoProfile {
	repoID := r.PathValue("org") + "/" + r.PathValue("repo")
	rp, err := a.Registry.Get(repoID)
	if err != nil {
		writeError(w, http.StatusNotFound, "repo not found")
		return nil
	}
	if rp.Owner != identity(r).Username {
		writeError(w, http.StatusForbidden, "only the repo owner can do this")
		return nil
	}
	return rp
}

// handleRepoFeed is the repo side: incoming offers (any visibility — the
// ideator explicitly revealed those) plus candidate PUBLIC ideas, scored.
// Private ideas can only ever appear in the offers list, and only on the
// one repo they were offered to — this is the invariant the tests pin down.
func (a *API) handleRepoFeed(w http.ResponseWriter, r *http.Request) {
	rp := a.ownedRepo(w, r)
	if rp == nil {
		return
	}
	offered, err := a.Store.ListOfferedTo([]string{rp.RepoID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	offers := []matchView{}
	for _, idea := range offered {
		offers = append(offers, a.repoView(r.Context(), idea, rp))
	}

	publics, err := a.Store.ListPublic()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	candidates := []matchView{}
	for _, idea := range publics {
		if idea.Status != store.StatusDraft && idea.Status != store.StatusOffered {
			continue // already accepted/settled/declined elsewhere
		}
		if rp.HasPassed(idea.ID) || idea.OfferTo(rp.RepoID) != nil {
			continue // swiped away, or already in the offers list
		}
		candidates = append(candidates, a.repoView(r.Context(), idea, rp))
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	if len(candidates) > maxFeedCandidates {
		candidates = candidates[:maxFeedCandidates]
	}
	writeJSON(w, http.StatusOK, map[string]any{"offers": offers, "candidates": candidates})
}

// repoView scores one idea for one repo (cached both directions) and
// ensures the TLDR exists for display.
func (a *API) repoView(ctx context.Context, idea *store.Idea, rp *registry.RepoProfile) matchView {
	v := matchView{Idea: idea}
	if a.Engine == nil {
		return v
	}
	if _, err := a.Engine.EnsureTLDR(ctx, idea); err != nil {
		log.Printf("api: ensuring tldr for %s: %v", idea.ID, err)
	}
	m, err := a.Engine.ScoreForRepo(ctx, idea, rp)
	if err != nil {
		log.Printf("api: scoring %s×%s: %v", idea.ID, rp.RepoID, err)
		return v
	}
	v.Score, v.Reason, v.ByLLM = m.Score, m.Reason, m.ByLLM
	return v
}

type decideInput struct {
	IdeaID   string `json:"ideaID"`
	Decision string `json:"decision"` // accept | decline | pass
}

// handleDecide is the repo owner swiping on an idea: accept (→ settlement),
// decline (pending offers only), or pass (hide a public candidate).
func (a *API) handleDecide(w http.ResponseWriter, r *http.Request) {
	rp := a.ownedRepo(w, r)
	if rp == nil {
		return
	}
	var in decideInput
	if !decodeInput(w, r, &in) {
		return
	}
	idea, err := a.Store.Get(in.IdeaID)
	if err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	offer := idea.OfferTo(rp.RepoID)
	// A private idea is invisible to this repo unless its ideator offered it
	// HERE — reply 404 so even its existence doesn't leak.
	if idea.Visibility == store.VisibilityPrivate && offer == nil {
		writeError(w, http.StatusNotFound, "idea not found")
		return
	}
	switch in.Decision {
	case "pass":
		if err := a.Registry.AddPassedIdea(rp.RepoID, identity(r).Username, idea.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"result": "passed"})
	case "decline":
		if offer == nil || offer.Status != store.OfferPending {
			writeError(w, http.StatusBadRequest, "no pending offer from this idea")
			return
		}
		updated, err := a.Store.Mutate(idea.ID, true, func(i *store.Idea) error {
			o := i.OfferTo(rp.RepoID)
			if o == nil || o.Status != store.OfferPending {
				return &store.ValidationError{Msg: "no pending offer from this idea"}
			}
			now := timeNow()
			o.Status = store.OfferDeclined
			o.DecidedAt = &now
			for _, other := range i.Offers {
				if other.Status == store.OfferPending {
					return nil // other repos are still considering it
				}
			}
			if store.CanTransition(i.Status, store.StatusDeclined) {
				i.Status = store.StatusDeclined
			}
			return nil
		})
		if err != nil {
			status, msg := storeErrStatus(err)
			writeError(w, status, msg)
			return
		}
		a.notifyAdd(updated.Author, notify.KindDeclined,
			rp.RepoID+" declined your idea “"+updated.Title+"”.", updated.ID, rp.RepoID)
		writeJSON(w, http.StatusOK, map[string]any{"result": "declined", "idea": updated})
	case "accept":
		a.accept(w, r, idea, rp, offer)
	default:
		writeError(w, http.StatusBadRequest, `decision must be "accept", "decline", or "pass"`)
	}
}

// accept records the acceptance (offered/draft → accepted). Settlement is
// the IDEATOR's move from here: Dibs is just the matchmaker, so the default
// flow hands them a prefilled GitHub new-issue URL (see settlement.go) and
// they file the issue under their own account. Only in the optional legacy
// mode (DIBS_GITHUB_TOKEN set → Settler.GitHub non-nil) does Dibs still
// open the issue server-side.
func (a *API) accept(w http.ResponseWriter, r *http.Request, idea *store.Idea, rp *registry.RepoProfile, offer *store.Offer) {
	if offer == nil && idea.Status != store.StatusDraft && idea.Status != store.StatusOffered {
		writeError(w, http.StatusBadRequest, "idea is not available to accept")
		return
	}
	updated, err := a.Store.Mutate(idea.ID, true, func(i *store.Idea) error {
		if !store.CanTransition(i.Status, store.StatusAccepted) {
			return &store.ValidationError{Msg: "cannot accept an idea in status " + i.Status}
		}
		if o := i.OfferTo(rp.RepoID); o != nil {
			now := timeNow()
			o.Status = store.OfferAccepted
			o.DecidedAt = &now
		}
		i.Status = store.StatusAccepted
		i.TargetRepo = rp.RepoID
		return nil
	})
	if err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	if a.Engine != nil {
		if _, err := a.Engine.EnsureTLDR(r.Context(), updated); err != nil {
			log.Printf("api: ensuring tldr after accept: %v", err)
		}
	}

	if a.Settler != nil && a.Settler.GitHub != nil {
		a.legacySettle(w, r, updated, rp)
		return
	}

	// Default matchmaker flow: the ideator files the issue themselves.
	a.notifyAdd(updated.Author, notify.KindAccepted,
		rp.RepoID+" accepted your idea “"+updated.Title+"”! 🎉 File the GitHub issue from My Ideas to claim the credit.",
		updated.ID, rp.RepoID)
	writeJSON(w, http.StatusOK, map[string]any{
		"result": "accepted",
		"idea":   updated,
		"next":   "the ideator files the prefilled GitHub issue themselves and confirms its URL",
	})
}

// legacySettle is the demoted token-based settlement: Dibs opens the
// credited issue itself. Reachable only when DIBS_GITHUB_TOKEN is set.
func (a *API) legacySettle(w http.ResponseWriter, r *http.Request, updated *store.Idea, rp *registry.RepoProfile) {
	a.notifyAdd(updated.Author, notify.KindAccepted,
		rp.RepoID+" accepted your idea “"+updated.Title+"”! 🎉", updated.ID, rp.RepoID)
	issueURL, err := a.Settler.Settle(r.Context(), updated, rp.RepoID)
	if err != nil {
		warning := "accepted, but opening the GitHub issue failed: " + err.Error()
		log.Printf("api: legacy settlement for %s on %s: %v", updated.ID, rp.RepoID, err)
		writeJSON(w, http.StatusOK, map[string]any{"result": "accepted", "idea": updated, "warning": warning})
		return
	}
	settled, err := a.Store.Mutate(updated.ID, true, func(i *store.Idea) error {
		if !store.CanTransition(i.Status, store.StatusSettled) {
			return &store.ValidationError{Msg: "cannot settle an idea in status " + i.Status}
		}
		i.Status = store.StatusSettled
		i.IssueURL = issueURL
		return nil
	})
	if err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	a.notifyAdd(settled.Author, notify.KindIssue,
		"Your idea “"+settled.Title+"” is now a GitHub issue on "+rp.RepoID+": "+issueURL, settled.ID, rp.RepoID)
	writeJSON(w, http.StatusOK, map[string]any{"result": "settled", "idea": settled, "issueURL": issueURL})
}

// handleListNotifications returns the caller's feed (?unread=1 filters) and
// the unread count for the bell badge.
func (a *API) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	if a.Notify == nil {
		writeJSON(w, http.StatusOK, map[string]any{"notifications": []notify.Notification{}, "unread": 0})
		return
	}
	user := identity(r).Username
	unreadOnly := r.URL.Query().Get("unread") == "1"
	writeJSON(w, http.StatusOK, map[string]any{
		"notifications": a.Notify.ListByUser(user, unreadOnly),
		"unread":        len(a.Notify.ListByUser(user, true)),
	})
}

type markReadInput struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
}

func (a *API) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	var in markReadInput
	if !decodeInput(w, r, &in) {
		return
	}
	if a.Notify != nil {
		if err := a.Notify.MarkRead(identity(r).Username, in.IDs, in.All); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
