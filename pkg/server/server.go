// Package server assembles the Ideate HTTP server: base-path routing, the
// embedded static UI, health, and the auth-guarded API.
//
// Every route lives under a base path (IDEATE_BASE_PATH, default "/ideas")
// because the service is reverse-proxied at hive.kubestellar.io/ideas. The UI
// only uses RELATIVE URLs so it needs no base-path templating.
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

// DefaultBasePath is where Ideate is mounted when IDEATE_BASE_PATH is unset.
const DefaultBasePath = "/ideas"

//go:embed static/index.html
var staticFS embed.FS

// Config assembles a server.
type Config struct {
	// BasePath is the URL prefix every route is served under, e.g. "/ideas".
	// Must start with "/" and not end with "/".
	BasePath string
	// HubURL is the human-facing hub origin (sign-in interstitial link).
	HubURL string
	Hub    auth.HubClient
	Store  *store.Store
	Repos  *registry.Registry
	// Version is the embedded git hash, exposed on the health endpoint.
	Version string
}

// NormalizeBasePath coerces a configured base path into the canonical
// "/prefix" form ("" and "/" both mean the default).
func NormalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return DefaultBasePath
	}
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
	// Bare base path (no trailing slash) → canonical UI URL. Relative asset
	// and API URLs in the page only resolve correctly under "{base}/".
	root.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/", http.StatusMovedPermanently)
	})
	root.Handle(base+"/", mw.Wrap(authed))
	return root
}
