package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// csrfRequest issues a POST /api/ideas with a valid session cookie and the
// given browser-context headers, returning the status code.
func csrfRequest(t *testing.T, h http.Handler, headers map[string]string) int {
	t.Helper()
	body := strings.NewReader(`{"title":"t","body":"b","visibility":"private"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ideas", body)
	req.AddCookie(&http.Cookie{Name: "hive_hub_user", Value: "alice-session"})
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestCSRFGuard(t *testing.T) {
	h := newTestServer(t, "")
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no browser headers (curl)", nil, http.StatusCreated},
		{"sec-fetch-site same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusCreated},
		{"sec-fetch-site none", map[string]string{"Sec-Fetch-Site": "none"}, http.StatusCreated},
		{"sec-fetch-site same-site (sibling subdomain)", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"sec-fetch-site cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		// Sec-Fetch-Site wins over a matching Origin: the site class is the
		// browser's own judgement and cannot be faked from a page.
		{"same-site despite matching origin", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://example.com"}, http.StatusForbidden},
		{"origin matches host", map[string]string{"Origin": "https://example.com"}, http.StatusCreated},
		{"origin mismatch", map[string]string{"Origin": "https://evil.hivecommons.dev"}, http.StatusForbidden},
		{"origin null", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"origin garbage", map[string]string{"Origin": "not a url"}, http.StatusForbidden},
		{"origin matches forwarded host", map[string]string{"Origin": "https://dibs.hivecommons.dev", "X-Forwarded-Host": "dibs.hivecommons.dev"}, http.StatusCreated},
		{"origin mismatches forwarded host", map[string]string{"Origin": "https://other.hivecommons.dev", "X-Forwarded-Host": "dibs.hivecommons.dev"}, http.StatusForbidden},
	}
	// httptest.NewRequest sets Host to example.com.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := csrfRequest(t, h, tc.headers); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCSRFGuardAllowsSafeMethods(t *testing.T) {
	h := newTestServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: "hive_hub_user", Value: "alice-session"})
	// A cross-site GET (e.g. top-level navigation, image load) must still
	// work: only state-changing methods are guarded.
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/me with cross-site header = %d, want 200", rec.Code)
	}
}
