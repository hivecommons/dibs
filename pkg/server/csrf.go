package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// csrfGuard rejects cross-origin state-changing requests before they reach
// the cookie-authenticated API.
//
// Why this exists: Dibs authenticates browsers purely by the hub's
// hive_hub_user cookie, which the hub scopes to the shared registrable
// domain (Domain=.hivecommons.dev) so it reaches every sibling subdomain.
// SameSite=Lax stops classic cross-SITE forgery, but every host under
// hivecommons.dev is the SAME site, so a page on any sibling subdomain can
// silently POST/PUT/DELETE here with the victim's session attached. This
// guard closes that gap by requiring state-changing requests to originate
// from Dibs itself.
//
// Decision order, per request with an unsafe method (not GET/HEAD/OPTIONS):
//
//  1. Sec-Fetch-Site (sent by all modern browsers, unforgeable from JS):
//     "same-origin" and "none" (direct navigation / non-browser) pass;
//     anything else — including "same-site" — is rejected.
//  2. Otherwise, an Origin header must match the host serving this request
//     (r.Host, or X-Forwarded-Host when a proxy sets it).
//  3. Neither header present: allow. Non-browser clients (curl, tests) omit
//     both, and they cannot be victims of CSRF — the attack requires a
//     browser attaching the cookie ambiently, and every browser that does so
//     sends Origin (and, today, Sec-Fetch-Site) on unsafe cross-origin
//     requests.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
			if site != "same-origin" && site != "none" {
				rejectCSRF(w)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
			if !originMatchesRequestHost(origin, r) {
				rejectCSRF(w)
				return
			}
		} else if origin == "null" {
			// "null" means an opaque origin (sandboxed iframe, data: URL):
			// never a legitimate source of authenticated Dibs traffic.
			rejectCSRF(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originMatchesRequestHost reports whether the Origin header names the same
// host the request was addressed to: r.Host, or the X-Forwarded-Host a
// reverse proxy recorded (first value, as proxies append).
func originMatchesRequestHost(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	originHost := strings.ToLower(u.Host)
	if originHost == strings.ToLower(r.Host) {
		return true
	}
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		first := strings.ToLower(strings.TrimSpace(strings.Split(fwd, ",")[0]))
		if first != "" && originHost == first {
			return true
		}
	}
	return false
}

func rejectCSRF(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "cross-origin request rejected"})
}
