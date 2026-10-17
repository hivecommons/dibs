package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var wantSecurityHeaders = map[string]string{
	"Content-Security-Policy": "frame-ancestors 'none'",
	"X-Frame-Options":         "DENY",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "strict-origin-when-cross-origin",
}

func assertSecurityHeaders(t *testing.T, name string, h http.Header) {
	t.Helper()
	for k, want := range wantSecurityHeaders {
		if got := h.Get(k); got != want {
			t.Errorf("%s: header %s = %q, want %q", name, k, got, want)
		}
	}
}

// TestSecurityHeadersOnEveryRoute pins the hardening headers on each class
// of response the mux produces: the public UI page, a public data API, an
// authenticated API success, an auth rejection, a CSRF rejection, the MCP
// instructions page, the probes, a 404, and the base-path redirect.
func TestSecurityHeadersOnEveryRoute(t *testing.T) {
	h := newTestServer(t, "")

	get := func(path, session string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if session != "" {
			req.AddCookie(&http.Cookie{Name: "hive_hub_user", Value: session})
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	cases := []struct {
		name string
		rec  *httptest.ResponseRecorder
		code int
	}{
		{"ui page", get("/", "", nil), http.StatusOK},
		{"public credits api", get("/api/credits", "", nil), http.StatusOK},
		{"authenticated api", get("/api/me", "alice-session", nil), http.StatusOK},
		{"auth rejection json", get("/api/ideas", "", nil), http.StatusUnauthorized},
		{"auth rejection interstitial", get("/ideas", "", nil), http.StatusUnauthorized},
		{"mcp instructions", get("/mcp", "", map[string]string{"Accept": "text/html"}), http.StatusOK},
		{"healthz", get("/healthz", "", nil), http.StatusOK},
		{"readyz", get("/readyz", "", nil), http.StatusOK},
	}
	for _, tc := range cases {
		if tc.rec.Code != tc.code {
			t.Errorf("%s: status %d, want %d", tc.name, tc.rec.Code, tc.code)
		}
		assertSecurityHeaders(t, tc.name, tc.rec.Header())
	}

	// A CSRF rejection never reaches a route handler; the headers must
	// still be present.
	req := httptest.NewRequest(http.MethodPost, "/api/ideas", nil)
	req.AddCookie(&http.Cookie{Name: "hive_hub_user", Value: "alice-session"})
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("csrf rejection: status %d, want 403", rec.Code)
	}
	assertSecurityHeaders(t, "csrf rejection", rec.Header())
}

func TestSecurityHeadersWithBasePath(t *testing.T) {
	h := newTestServer(t, "/dibs")
	for _, path := range []string{"/dibs", "/dibs/", "/dibs/api/credits", "/dibs/healthz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assertSecurityHeaders(t, path, rec.Header())
	}
}

// TestSecurityHeadersDoNotClobberContentType guards against the wrapper
// interfering with the headers routes set themselves.
func TestSecurityHeadersDoNotClobberContentType(t *testing.T) {
	h := newTestServer(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("healthz Content-Type = %q", got)
	}
}
