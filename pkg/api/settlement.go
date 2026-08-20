// Matchmaker settlement API: LLM-assisted refinement, the prefilled
// GitHub-issue launch URL, and the ideator's "I filed it" confirmation.
//
// Dibs never opens the issue itself in this flow — the ideator's browser is
// pointed at github.com/{org}/{repo}/issues/new with title/body/labels
// prefilled, so the issue is filed under THEIR account and GitHub natively
// attributes it to them. Dibs just records the resulting issue URL
// (accepted → issue_launched → settled).
package api

import (
	"net/http"

	"github.com/kubestellar/dibs/pkg/notify"
	"github.com/kubestellar/dibs/pkg/registry"
	"github.com/kubestellar/dibs/pkg/settle"
	"github.com/kubestellar/dibs/pkg/store"
)

// registerSettlement mounts the refine/launch/confirm routes.
func (a *API) registerSettlement(mux *http.ServeMux, basePath string) {
	mux.HandleFunc("POST "+basePath+"/api/refine", a.handleRefine)
	mux.HandleFunc("POST "+basePath+"/api/ideas/{id}/launch", a.handleLaunch)
	mux.HandleFunc("POST "+basePath+"/api/ideas/{id}/confirm-issue", a.handleConfirmIssue)
}

type refineInput struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// RepoID, when set, tailors the draft to that repo (the pre-submission
	// expansion); empty means the generic posting-time refinement.
	RepoID string `json:"repoID"`
}

type refineOutput struct {
	// Refined is false when no LLM is configured (or it failed) — the
	// caller should skip the refinement step; Title/Body echo the input.
	Refined bool   `json:"refined"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

// handleRefine runs the LLM refinement. The result is ONLY a suggestion:
// nothing is persisted — the user edits and decides what to keep.
func (a *API) handleRefine(w http.ResponseWriter, r *http.Request) {
	var in refineInput
	if !decodeInput(w, r, &in) {
		return
	}
	if in.Title == "" || in.Body == "" {
		writeError(w, http.StatusBadRequest, "title and body are required")
		return
	}
	out := refineOutput{Title: in.Title, Body: in.Body}
	if a.Engine != nil {
		var rp *registry.RepoProfile
		if in.RepoID != "" {
			p, err := a.Registry.Get(in.RepoID)
			if err != nil {
				writeError(w, http.StatusNotFound, "repo not found")
				return
			}
			rp = p
		}
		if d := a.Engine.Refine(r.Context(), in.Title, in.Body, rp); d != nil {
			out.Refined, out.Title, out.Body = true, d.Title, d.Body
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type launchInput struct {
	// Title/Body override the defaults (the user-edited, LLM-expanded
	// draft). Empty means use the idea's own title/body.
	Title string `json:"title"`
	Body  string `json:"body"`
}

// handleLaunch builds the prefilled GitHub new-issue URL for an ACCEPTED
// idea's target repo and records that the ideator launched it
// (accepted → issue_launched). Idempotent from issue_launched: re-launching
// just rebuilds the URL.
func (a *API) handleLaunch(w http.ResponseWriter, r *http.Request) {
	idea := a.loadAuthorized(w, r, true)
	if idea == nil {
		return
	}
	var in launchInput
	if !decodeInput(w, r, &in) {
		return
	}
	if idea.TargetRepo == "" {
		writeError(w, http.StatusBadRequest, "idea has no target repo")
		return
	}
	// hiveManaged decides the footer AND the status gate: hive repos must
	// accept first; external targets have no owner on our side, so an
	// offered idea launches directly (see offerExternal).
	_, regErr := a.Registry.Get(idea.TargetRepo)
	hiveManaged := regErr == nil
	switch idea.Status {
	case store.StatusAccepted, store.StatusIssueLaunched:
	case store.StatusOffered:
		if hiveManaged {
			writeError(w, http.StatusBadRequest, "idea must be accepted before launching the issue")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "idea must be accepted before launching the issue")
		return
	}
	title := in.Title
	if title == "" {
		title = settle.IssueTitle(idea)
	}
	body := in.Body
	if body == "" {
		body = idea.Body
	}
	fullBody := settle.LaunchBody(body, hiveManaged)
	issueURL, truncated := settle.NewIssueURL(idea.TargetRepo, title, fullBody, hiveManaged)

	if idea.Status != store.StatusIssueLaunched {
		if _, err := a.Store.Transition(idea.ID, store.StatusIssueLaunched); err != nil {
			status, msg := storeErrStatus(err)
			writeError(w, status, msg)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":       issueURL,
		"repoID":    idea.TargetRepo,
		"title":     title,
		"fullBody":  fullBody,
		"truncated": truncated,
	})
}

type confirmInput struct {
	IssueURL string `json:"issueURL"`
}

// handleConfirmIssue is the ideator's "I filed it — here's the link": the
// pasted URL must be a real github.com/{org}/{repo}/issues/N URL on the
// accepting repo. It completes settlement and feeds the credit wall.
func (a *API) handleConfirmIssue(w http.ResponseWriter, r *http.Request) {
	idea := a.loadAuthorized(w, r, true)
	if idea == nil {
		return
	}
	var in confirmInput
	if !decodeInput(w, r, &in) {
		return
	}
	if idea.Status != store.StatusAccepted && idea.Status != store.StatusIssueLaunched {
		writeError(w, http.StatusBadRequest, "idea is not awaiting an issue confirmation")
		return
	}
	if idea.TargetRepo == "" {
		writeError(w, http.StatusBadRequest, "idea has no target repo")
		return
	}
	if err := settle.ValidateIssueURL(in.IssueURL, idea.TargetRepo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settled, err := a.Store.Mutate(idea.ID, true, func(i *store.Idea) error {
		if !store.CanTransition(i.Status, store.StatusSettled) {
			return &store.ValidationError{Msg: "cannot settle an idea in status " + i.Status}
		}
		i.Status = store.StatusSettled
		i.IssueURL = in.IssueURL
		return nil
	})
	if err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	// Tell the accepting repo's owner the credited issue landed.
	if rp, err := a.Registry.Get(settled.TargetRepo); err == nil && rp.Owner != settled.Author {
		a.notifyAdd(rp.Owner, notify.KindIssue,
			settled.AuthorDisplay+" filed the idea “"+settled.Title+"” as an issue on "+rp.RepoID+": "+in.IssueURL,
			settled.ID, rp.RepoID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": "settled", "idea": ideaForViewer(settled, identity(r).Username)})
}
