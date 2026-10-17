package server

import "net/http"

// securityHeaders adds the browser hardening headers every Dibs response
// should carry. It wraps the whole mux — public, authenticated, MCP and
// probe routes alike — so no route can be served without them.
//
// Framing is refused outright: the session is the hub's hive_hub_user
// cookie, scoped to the shared registrable domain with SameSite=Lax, so a
// frame served from a sibling subdomain renders the visitor's logged-in UI.
// csrfGuard (csrf.go) exists for that same sibling-subdomain threat but
// cannot stop clickjacking — the clicks it induces are genuine same-origin
// requests from the framed page. Nothing legitimately embeds Dibs; sign-in
// opens the hub in a new tab.
//
// The policy is frame-ancestors only. index.html runs one inline script, so
// a script-src policy would need a nonce or hash first and is a separate
// change.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
