// Package server assembles the Ideate HTTP server: base-path routing, the
// embedded static UI, health, and the auth-guarded API.
//
// Every route lives under a base path (IDEATE_BASE_PATH, default "/") —
// Ideate is served at its own subdomain, idea.kubestellar.io, so the default
// is the root; a prefix (e.g. "/ideas") remains fully supported for
// path-based reverse-proxy deployments. The UI only uses RELATIVE URLs so it
// needs no base-path templating.
package server

import (
	"embed"
	"net/http"
	"strings"

	"github.com/kubestellar/ideate/pkg/api"
	"github.com/kubestellar/ideate/pkg/auth"
	"github.com/kubestellar/ideate/pkg/registry"
	"github.com/kubestellar/ideate/pkg/store"
)

// DefaultBasePath is where Ideate is mounted when IDEATE_BASE_PATH is unset:
// the root, because Ideate lives on its own subdomain (idea.kubestellar.io).
const DefaultBasePath = "/"

//go:embed static/index.html
var staticFS embed.FS

// Config assembles a server.
type Config struct {
	// BasePath is the URL prefix every route is served under: "/" (or "")
	// for the root, or "/prefix" (no trailing slash) for path-based proxying.
	BasePath string
	// HubURL is the human-facing hub origin (sign-in interstitial link).
	HubURL string
	Hub    auth.HubClient
	Store  *store.Store
	Repos  *registry.Registry
	// Version is the embedded git hash, exposed on the health endpoint.
	Version string
}

// NormalizeBasePath coerces a configured base path into canonical form:
// "" for the root ("", "/", and whitespace all mean root), otherwise
// "/prefix" with no trailing slash. The "" form is what route registration
// concatenates with, so root patterns come out as "/healthz", "/api/...".
func NormalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

// New builds the full handler.
func New(cfg Config) http.Handler {
	base := NormalizeBasePath(cfg.BasePath)

	indexHTML, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		panic("server: embedded index.html missing: " + err.Error())
	}

	mw := &auth.Middleware{
		Hub:    cfg.Hub,
		HubURL: cfg.HubURL,
		IsAPI: func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, base+"/api/")
		},
	}

	// Authenticated routes.
	authed := http.NewServeMux()
	(&api.API{Store: cfg.Store, Registry: cfg.Repos}).Register(authed, base)
	authed.HandleFunc("GET "+base+"/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})

	// Public routes + the auth-guarded rest.
	root := http.NewServeMux()
	root.HandleFunc("GET "+base+"/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + cfg.Version + `"}` + "\n"))
	})
	// With a prefix, the bare base path (no trailing slash) redirects to the
	// canonical UI URL: relative asset and API URLs in the page only resolve
	// correctly under "{base}/". At the root there is no bare form.
	if base != "" {
		root.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, base+"/", http.StatusMovedPermanently)
		})
	}
	root.Handle(base+"/", mw.Wrap(authed))
	return root
}
