// Package api implements the Ideate JSON API: author-scoped idea CRUD and
// the repo-registry endpoints. All handlers assume auth.Middleware has run
// and an identity is on the context.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/kubestellar/ideate/pkg/auth"
	"github.com/kubestellar/ideate/pkg/match"
	"github.com/kubestellar/ideate/pkg/notify"
	"github.com/kubestellar/ideate/pkg/registry"
	"github.com/kubestellar/ideate/pkg/settle"
	"github.com/kubestellar/ideate/pkg/store"
)

// timeNow is stubbed in tests.
var timeNow = func() time.Time { return time.Now().UTC() }

// maxRequestBody bounds any request body we parse: idea body cap plus
// generous headroom for JSON framing and other fields.
const maxRequestBody = store.MaxBodyBytes + 64*1024

// API wires the store, registry, match engine, settler, and notifications
// into HTTP handlers.
type API struct {
	Store    *store.Store
	Registry *registry.Registry
	// Engine is nil when matching is disabled (Wave-2 features degrade).
	Engine *match.Engine
	// Settler opens credited GitHub issues; a nil-GitHub settler records
	// accepts without opening issues.
	Settler *settle.Settler
	// Notify is the in-app notification store (nil disables).
	Notify *notify.Store
}

// Register mounts the API routes onto mux under basePath — "" for the root,
// or a normalized "/prefix" (e.g. "/ideas").
func (a *API) Register(mux *http.ServeMux, basePath string) {
	mux.HandleFunc("GET "+basePath+"/api/me", a.handleMe)
	mux.HandleFunc("GET "+basePath+"/api/ideas", a.handleListIdeas)
	mux.HandleFunc("POST "+basePath+"/api/ideas", a.handleCreateIdea)
	mux.HandleFunc("GET "+basePath+"/api/ideas/{id}", a.handleGetIdea)
	mux.HandleFunc("PUT "+basePath+"/api/ideas/{id}", a.handleUpdateIdea)
	mux.HandleFunc("DELETE "+basePath+"/api/ideas/{id}", a.handleDeleteIdea)
	mux.HandleFunc("GET "+basePath+"/api/repos", a.handleListRepos)
	mux.HandleFunc("PUT "+basePath+"/api/repos/{org}/{repo}", a.handleUpdateRepo)
	a.registerWave2(mux, basePath)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func identity(r *http.Request) *auth.Identity {
	return auth.FromContext(r.Context())
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, identity(r))
}

// ideaInput is the client-writable subset of an idea.
type ideaInput struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Visibility string `json:"visibility"`
	Status     string `json:"status"`
}

func decodeInput(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading request body")
		return false
	}
	if len(body) > maxRequestBody {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func storeErrStatus(err error) (int, string) {
	var ve *store.ValidationError
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "idea not found"
	case errors.As(err, &ve):
		return http.StatusBadRequest, ve.Msg
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// handleListIdeas lists ideas. ?scope=public returns everyone's PUBLIC ideas;
// the default scope=mine returns the caller's own ideas (any visibility).
// Private ideas can therefore only ever surface to their author.
func (a *API) handleListIdeas(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	switch scope := r.URL.Query().Get("scope"); scope {
	case "", "mine":
		ideas, err := a.Store.ListByAuthor(id.Username)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, ideas)
	case "public":
		ideas, err := a.Store.ListPublic()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, ideas)
	default:
		writeError(w, http.StatusBadRequest, `scope must be "mine" or "public"`)
	}
}

func (a *API) handleCreateIdea(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	var in ideaInput
	if !decodeInput(w, r, &in) {
		return
	}
	if in.Status != "" && in.Status != store.StatusDraft && in.Status != store.StatusOffered {
		writeError(w, http.StatusBadRequest, `status must be "draft" or "offered"`)
		return
	}
	idea := &store.Idea{
		Author:        id.Username,
		AuthorDisplay: id.DisplayName,
		Title:         in.Title,
		Body:          in.Body,
		Visibility:    in.Visibility,
		Status:        in.Status,
	}
	if idea.AuthorDisplay == "" {
		idea.AuthorDisplay = id.Username
	}
	if err := a.Store.Create(idea); err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusCreated, idea)
}

// loadAuthorized fetches an idea and enforces read access: the author
// always; anyone authenticated if the idea is public; and — new in Wave 2 —
// the owner of a repo the idea has been OFFERED to (the ideator's explicit
// reveal). Write access (mustOwn) is author-only. A private idea is
// presented to everyone else as 404, not 403, so its very existence doesn't
// leak.
func (a *API) loadAuthorized(w http.ResponseWriter, r *http.Request, mustOwn bool) *store.Idea {
	id := identity(r)
	idea, err := a.Store.Get(r.PathValue("id"))
	if err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return nil
	}
	if idea.Author == id.Username {
		return idea
	}
	if !mustOwn && idea.Visibility == store.VisibilityPublic {
		return idea
	}
	if !mustOwn && a.offeredToCallerRepo(idea, id.Username) {
		return idea
	}
	if idea.Visibility == store.VisibilityPrivate {
		writeError(w, http.StatusNotFound, "idea not found")
	} else {
		writeError(w, http.StatusForbidden, "not the author")
	}
	return nil
}

// offeredToCallerRepo reports whether the idea carries an offer to a repo
// owned by username.
func (a *API) offeredToCallerRepo(idea *store.Idea, username string) bool {
	for _, o := range idea.Offers {
		if rp, err := a.Registry.Get(o.RepoID); err == nil && rp.Owner == username {
			return true
		}
	}
	return false
}

func (a *API) handleGetIdea(w http.ResponseWriter, r *http.Request) {
	if idea := a.loadAuthorized(w, r, false); idea != nil {
		writeJSON(w, http.StatusOK, idea)
	}
}

func (a *API) handleUpdateIdea(w http.ResponseWriter, r *http.Request) {
	idea := a.loadAuthorized(w, r, true)
	if idea == nil {
		return
	}
	var in ideaInput
	if !decodeInput(w, r, &in) {
		return
	}
	if in.Status != "" && in.Status != store.StatusDraft && in.Status != store.StatusOffered {
		writeError(w, http.StatusBadRequest, `status must be "draft" or "offered"`)
		return
	}
	idea.Title = in.Title
	idea.Body = in.Body
	idea.Visibility = in.Visibility
	if in.Status != "" {
		idea.Status = in.Status
	}
	if err := a.Store.Update(idea); err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, idea)
}

func (a *API) handleDeleteIdea(w http.ResponseWriter, r *http.Request) {
	idea := a.loadAuthorized(w, r, true)
	if idea == nil {
		return
	}
	if err := a.Store.Delete(idea.ID); err != nil {
		status, msg := storeErrStatus(err)
		writeError(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListRepos lists repo profiles. Default: repos accepting ideas.
// ?scope=mine lists the caller's own repos (for the "For repos" page).
// ?scope=all lists everything.
func (a *API) handleListRepos(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	switch scope := r.URL.Query().Get("scope"); scope {
	case "", "accepting":
		writeJSON(w, http.StatusOK, a.Registry.List(true))
	case "all":
		writeJSON(w, http.StatusOK, a.Registry.List(false))
	case "mine":
		writeJSON(w, http.StatusOK, a.Registry.ListByOwner(id.Username))
	default:
		writeError(w, http.StatusBadRequest, `scope must be "accepting", "all", or "mine"`)
	}
}

func (a *API) handleUpdateRepo(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	repoID := r.PathValue("org") + "/" + r.PathValue("repo")
	var upd registry.OwnerUpdate
	if !decodeInput(w, r, &upd) {
		return
	}
	rp, err := a.Registry.ApplyOwnerUpdate(repoID, id.Username, upd)
	switch {
	case errors.Is(err, registry.ErrNotFound):
		writeError(w, http.StatusNotFound, "repo not found")
	case errors.Is(err, registry.ErrForbidden):
		writeError(w, http.StatusForbidden, "only the repo owner can edit its profile")
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, rp)
	}
}
