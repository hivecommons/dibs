package api

import (
	"net/http"

	"github.com/hivecommons/dibs/pkg/news"
	"github.com/hivecommons/dibs/pkg/registry"
)

// HandleRepoNews returns cached repo news cards newest first.
func (a *API) HandleRepoNews(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("org") + "/" + r.PathValue("repo")
	if a.Registry != nil {
		if _, err := a.Registry.Get(repoID); err != nil {
			if err == registry.ErrNotFound {
				writeError(w, http.StatusNotFound, "repo not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	if a.News == nil {
		writeJSON(w, http.StatusOK, []news.Item{})
		return
	}
	items := a.News.Get(repoID)
	if items == nil {
		items = []news.Item{}
	}
	writeJSON(w, http.StatusOK, items)
}
