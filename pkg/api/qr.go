package api

import (
	"errors"
	"net/http"

	"github.com/hivecommons/dibs/pkg/registry"
	qrcode "github.com/skip2/go-qrcode"
)

const defaultContributeURL = "https://hive.kubestellar.io/#hives"

// HandleRepoQR serves GET /api/repos/{org}/{repo}/qr.png — a public QR code
// pointing scanners directly at the hive's contribute page.
func (a *API) HandleRepoQR(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("org") + "/" + r.PathValue("repo")
	rp, err := a.Registry.Get(repoID)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	url := rp.ContributeURL
	if url == "" {
		url = defaultContributeURL
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 512)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}
