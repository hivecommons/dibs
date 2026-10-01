// Package server assembles the Dibs HTTP server: base-path routing, the
// embedded static UI, health, and the auth-guarded API.
//
// Every route lives under a base path (DIBS_BASE_PATH, default "/") —
// Dibs is served at its own subdomain, dibs.hivecommons.dev, so the default
// is the root; a prefix (e.g. "/ideas") remains fully supported for
// path-based reverse-proxy deployments. The UI only uses RELATIVE URLs so it
// needs no base-path templating.
package server

import (
	"bytes"
	"embed"
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/hivecommons/dibs/pkg/api"
	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/deps"
	"github.com/hivecommons/dibs/pkg/mcpserver"
)

// DefaultBasePath is where Dibs is mounted when DIBS_BASE_PATH is unset:
// the root, because Dibs lives on its own subdomain (dibs.hivecommons.dev).
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
	// Deps are the subsystem dependencies, declared once in pkg/deps and
	// passed through to the API and MCP surfaces unchanged.
	deps.Deps
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
	// The page is served to UNAUTHENTICATED visitors too (landing + credit
	// wall), so it needs the hub origin for its sign-in links; substitute it
	// into the embedded HTML once at startup.
	indexHTML = bytes.ReplaceAll(indexHTML, []byte("__HUB_URL__"), []byte(template.HTMLEscapeString(cfg.HubURL)))

	mw := &auth.Middleware{
		Hub:    cfg.Hub,
		HubURL: cfg.HubURL,
		IsAPI: func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, base+"/api/")
		},
	}

	// Authenticated routes.
	authed := http.NewServeMux()
	dibsAPI := api.NewFromDeps(cfg.Deps)
	dibsAPI.Register(authed, base)

	// Public routes + the auth-guarded rest. The UI page itself is public:
	// logged-out visitors get the landing pitch + credit wall (the page asks
	// /api/me and downgrades itself); every data API except the credit wall
	// stays behind the auth middleware.
	root := http.NewServeMux()
	root.HandleFunc("GET "+base+"/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	// The credit wall is the public proof-of-flywheel: settled ideas only,
	// facts already public via the credited GitHub issue.
	root.HandleFunc("GET "+base+"/api/credits", dibsAPI.HandleCredits)
	// The leaderboard is the credit wall's gamified sibling: aggregate
	// scores/levels/badges for ideators already public via a settled issue.
	root.HandleFunc("GET "+base+"/api/leaderboard", dibsAPI.HandleLeaderboard)
	// The market surfaces power the logged-out landing's exchange look:
	// ticker tape, live-markets board, and aggregate stats. Public by
	// design — they expose only public ideas, already-public settled
	// facts, and aggregate counts (see pkg/api/market.go).
	root.HandleFunc("GET "+base+"/api/ticker", dibsAPI.HandleTicker)
	root.HandleFunc("GET "+base+"/api/board", dibsAPI.HandleBoard)
	root.HandleFunc("GET "+base+"/api/stats", dibsAPI.HandleStats)
	// The repo value-index chart series (see pkg/api/index.go): derived,
	// aggregate-only numbers per listed repo.
	root.HandleFunc("GET "+base+"/api/repos/{org}/{repo}/index", dibsAPI.HandleRepoIndex)
	root.HandleFunc("GET "+base+"/api/repos/{org}/{repo}/news", dibsAPI.HandleRepoNews)
	root.HandleFunc("GET "+base+"/api/repos/{org}/{repo}/qr.png", dibsAPI.HandleRepoQR)
	// /healthz is a liveness check only: it must stay cheap and independent
	// of downstream dependencies, so a transient store hiccup (which a
	// restart cannot fix on a single-replica, single-writer store) never
	// triggers a crash-loop.
	root.HandleFunc("GET "+base+"/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + cfg.Version + `"}` + "\n"))
	})
	// /readyz is the readiness check: it verifies the store's data
	// directory — the dependency every API call needs to serve traffic —
	// is actually accessible and writable before the pod is added to the
	// service endpoints.
	root.HandleFunc("GET "+base+"/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := cfg.Store.Ping(); err != nil {
			// The route is unauthenticated, so keep the body generic
			// and log the detail (which includes filesystem paths)
			// server-side instead.
			log.Printf("readyz: store ping failed: %v", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}` + "\n"))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + cfg.Version + `"}` + "\n"))
	})
	root.Handle(base+"/mcp", mcpserver.NewHandler(mcpserver.Config{Hub: cfg.Hub, Deps: cfg.Deps, BasePath: base}))
	// With a prefix, the bare base path (no trailing slash) redirects to the
	// canonical UI URL: relative asset and API URLs in the page only resolve
	// correctly under "{base}/". At the root there is no bare form.
	if base != "" {
		root.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, base+"/", http.StatusMovedPermanently)
		})
	}
	// csrfGuard runs before the session check: the hub cookie is scoped to
	// the shared registrable domain, so SameSite alone does not stop forged
	// state-changing requests from sibling subdomains (see csrf.go).
	root.Handle(base+"/", csrfGuard(mw.Wrap(authed)))

	// Bounded HTTP request visibility (see issue #184): counts/durations
	// keyed only by method, a small fixed route-group label, and status
	// class — never raw paths, idea/repo/user identifiers, or query
	// strings. Wrapping outermost covers public, authenticated, rejected,
	// and MCP requests alike, and accounts for /healthz and /readyz as
	// their own route group rather than mixing them into application
	// traffic. Log-only for now: no exporter, scrape endpoint, or metrics
	// SDK is added until a backend is chosen.
	reqMetrics := newRequestMetrics()
	go reqMetrics.logPeriodically(metricsLogInterval)
	return reqMetrics.wrap(base, root)
}
